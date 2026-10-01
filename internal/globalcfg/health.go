package globalcfg

// The launcher's health check (CS-GCFG-001..015). Claude Code keeps 5
// backups of its global file, at most one a minute, so a defaults write is
// followed within minutes by 5 backups of the damage. The launcher keeps its
// own last-good snapshots in the store the host commands use (snapshot-* in
// StateRoot/global-config/<key>/, CS-GCFG-053), compares each launch with the
// newest, and on damage prints one WARNING with the layout-correct restore
// command. It never writes the global file (operator decision 56); accept
// (CS-GCFG-054) is how a deliberate change becomes the new baseline.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
)

const (
	// SnapshotInterval is the refresh rule (CS-GCFG-006): a healthy file is
	// snapshotted only when the newest snapshot is at least this old.
	SnapshotInterval = time.Hour
	// AcceptCmd is the command the damage warning names (CS-GCFG-012).
	AcceptCmd = "claude-sandbox global-config accept"
)

// ReadRetries are the waits before the second and third read of a file that
// does not parse (CS-GCFG-004): reads at 0, 150 and 350 ms.
var ReadRetries = []time.Duration{150 * time.Millisecond, 200 * time.Millisecond}

// HealthOptions are the check's inputs and seams.
type HealthOptions struct {
	Home, StateRoot string
	Getenv          func(string) string
	Err             io.Writer
	Now             func() time.Time
	// Sleep waits between reads (CS-GCFG-004); nil means time.Sleep.
	Sleep func(time.Duration)
	// ReadFile reads the global file; nil means cascade.ReadRegularFile
	// (CS-GCFG-060: non-blocking, regular files only).
	ReadFile func(string) ([]byte, error)
	DirOps   *hostdirs.Ops
	// Previous is this launch's earlier result (the check before the
	// session); findings it already printed are not repeated (CS-GCFG-014).
	Previous *Health
}

// Health is one check's result.
type Health struct {
	// File is the global file judged (lexical); "" when the check stood
	// aside (CS-GCFG-002).
	File string
	// Findings are the damage found, key names and counts only; empty when
	// healthy.
	Findings []string
	// Snapshot is the snapshot written by this check, "" when none.
	Snapshot string
	// Warned is true when a WARNING was printed.
	Warned bool
}

// Damaged reports whether the check found damage.
func (h *Health) Damaged() bool { return h != nil && len(h.Findings) > 0 }

// configFacts are the parts of a global config the check compares — values
// are held only to compare them, never printed.
type configFacts struct {
	Summary
	FirstStart    json.RawMessage
	HasFirstStart bool
}

func readFacts(data []byte) (configFacts, error) {
	s, err := Summarize(data)
	if err != nil {
		return configFacts{}, err
	}
	f := configFacts{Summary: s}
	var m map[string]json.RawMessage
	json.Unmarshal(data, &m)
	if v, ok := m["firstStartTime"]; ok {
		f.FirstStart, f.HasFirstStart = v, true
	}
	return f, nil
}

// CheckHealth is the launcher's health check (CS-GCFG-001..015). It is
// warn-only: every problem is a WARNING on o.Err, never an error, and the
// global file is never written. The caller skips it inside a sandbox.
func CheckHealth(o HealthOptions) *Health {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Sleep == nil {
		o.Sleep = time.Sleep
	}
	if o.ReadFile == nil {
		o.ReadFile = cascade.ReadRegularFile
	}
	if o.Err == nil {
		o.Err = io.Discard
	}
	h := &Health{}
	// CS-GCFG-002: stand aside, silently, when the file is not knowable.
	if o.Home == "" || !filepath.IsAbs(o.Home) {
		return h
	}
	if testing.Testing() && isRealHome(o.Home) {
		// CS-GCFG-015: a forgotten fixture must fail before reading the
		// operator's real global file.
		panic(fmt.Sprintf("globalcfg: a test would check %s under the real home; use a scratch HOME", filepath.Join(o.Home, FileName)))
	}
	file, err := ResolveGlobalFile(o.Home, o.Getenv)
	if err != nil {
		return h
	}
	h.File = file

	// CS-GCFG-004: one read on the healthy path; retries only when it does
	// not parse.
	data, facts, readErr := readWithRetry(o, file)
	missing := errors.Is(readErr, fs.ErrNotExist)

	store, err := OpenStore(o.StateRoot, file, o.DirOps)
	if err != nil {
		fmt.Fprintf(o.Err, "WARNING: global-config health check skipped: the state directory: %v\n", err)
		return h
	}
	// CS-GCFG-007: the baseline is the newest snapshot that parses; a newer
	// one that does not is skipped, never compared with nor named.
	baseline, base := parseableBaseline(store)

	switch {
	case missing && baseline == "":
		return h // CS-GCFG-013: a first run.
	case missing:
		h.Findings = []string{"it is missing"}
	case readErr != nil:
		h.Findings = []string{fmt.Sprintf("it cannot be read (%v)", unwrapPath(readErr))}
	case facts == nil:
		h.Findings = []string{fmt.Sprintf("it does not parse as a JSON object (%d reads)", len(ReadRetries)+1)}
	case base != nil:
		h.Findings = compareFacts(*base, *facts)
	}

	if len(h.Findings) == 0 {
		// CS-GCFG-005/006: healthy — snapshot when there is none, or the
		// newest is at least SnapshotInterval old. Cheap look first, then
		// under the store's snapshot lock, looking again: of a burst of
		// launches one writes, the others skip the snapshot.
		if !snapshotDue(store, o.Now()) {
			return h
		}
		unlock, ok := store.TryLock()
		if !ok {
			return h // contention: another launch is snapshotting
		}
		defer unlock()
		if !snapshotDue(store, o.Now()) {
			return h
		}
		snap, werr := store.Write(SnapshotPrefix, data, o.Now())
		if werr != nil {
			fmt.Fprintf(o.Err, "WARNING: global-config health check: writing a snapshot of %s: %v\n", file, werr)
			return h
		}
		h.Snapshot = snap
		if perr := store.Prune(SnapshotPrefix, KeepSnapshots); perr != nil {
			fmt.Fprintf(o.Err, "WARNING: global-config health check: pruning old snapshots: %v\n", perr)
		}
		return h
	}

	// CS-GCFG-014: the same findings were printed before the session.
	if o.Previous != nil && o.Previous.Warned && o.Previous.File == h.File && reflect.DeepEqual(o.Previous.Findings, h.Findings) {
		h.Warned = true
		return h
	}
	h.Warned = true
	if baseline == "" {
		// CS-GCFG-013: nothing to restore from.
		fmt.Fprintf(o.Err, "WARNING: the global config %s: %s, and there is no snapshot of it to restore from. Claude Code keeps copies in %s. Nothing was changed.\n",
			file, strings.Join(h.Findings, "; "), filepath.Join(configDirFor(o.Home, o.Getenv), "backups")+"/")
		return h
	}
	fmt.Fprint(o.Err, damageWarning(o, file, h.Findings, baseline))
	return h
}

// parseableBaseline is the newest snapshot that parses as a JSON object, and
// its facts; "" and nil when there is none (CS-GCFG-007).
func parseableBaseline(store *Store) (string, *configFacts) {
	for _, p := range store.List(SnapshotPrefix) {
		b, err := cascade.ReadRegularFile(p)
		if err != nil {
			continue
		}
		if f, ferr := readFacts(b); ferr == nil {
			return p, &f
		}
	}
	return "", nil
}

// snapshotDue reports whether the store's newest snapshot is missing or at
// least SnapshotInterval old (CS-GCFG-006).
func snapshotDue(store *Store, now time.Time) bool {
	newest := store.Newest(SnapshotPrefix)
	return newest == "" || now.Sub(snapshotTime(newest)) >= SnapshotInterval
}

// readWithRetry reads file and parses it, re-reading after each of
// ReadRetries while it does not parse (CS-GCFG-004). facts is nil when the
// last read did not parse; err is the read error (no retry for a missing
// file).
func readWithRetry(o HealthOptions, file string) ([]byte, *configFacts, error) {
	for i := 0; ; i++ {
		data, err := o.ReadFile(file)
		if err == nil {
			if f, perr := readFacts(data); perr == nil {
				return data, &f, nil
			}
		} else if errors.Is(err, fs.ErrNotExist) || errors.Is(err, cascade.ErrNotRegular) {
			// CS-GCFG-060: a FIFO, device or directory is not a partial
			// write; re-reading would not change the verdict.
			return nil, nil, err
		}
		if i >= len(ReadRetries) {
			return data, nil, err
		}
		o.Sleep(ReadRetries[i])
	}
}

// compareFacts is the CS-GCFG-007 rule: findings as key names and counts.
func compareFacts(base, cur configFacts) []string {
	var out []string
	if base.OAuth && !cur.OAuth {
		out = append(out, "oauthAccount is gone")
	}
	if base.Onboarding && !cur.Onboarding {
		out = append(out, "hasCompletedOnboarding is no longer true")
	}
	if base.Projects > 0 && cur.Projects*2 < base.Projects {
		out = append(out, fmt.Sprintf("projects fell from %d to %d", base.Projects, cur.Projects))
	}
	if base.HasFirstStart {
		switch {
		case !cur.HasFirstStart:
			out = append(out, "firstStartTime is gone")
		case !jsonEqual(base.FirstStart, cur.FirstStart):
			out = append(out, "firstStartTime changed")
		}
	}
	return out
}

func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return string(a) == string(b)
	}
	return reflect.DeepEqual(x, y)
}

// snapshotTime is when a snapshot was taken: the <ms> in its name, else its
// mtime (CS-GCFG-006).
func snapshotTime(path string) time.Time {
	rest := strings.TrimPrefix(filepath.Base(path), SnapshotPrefix)
	if i := strings.IndexByte(rest, '-'); i >= 0 {
		rest = rest[:i]
	}
	if ms, err := strconv.ParseInt(rest, 10, 64); err == nil {
		return time.UnixMilli(ms)
	}
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

// configDirFor is the config dir Claude Code uses: CLAUDE_CONFIG_DIR, else
// $HOME/.claude.
func configDirFor(home string, getenv func(string) string) string {
	if getenv != nil {
		if cd := getenv("CLAUDE_CONFIG_DIR"); cd != "" {
			return filepath.Clean(cd)
		}
	}
	return ConfigDir(home)
}

// damageWarning is the CS-GCFG-008 warning with the restore command of
// CS-GCFG-009..011.
func damageWarning(o HealthOptions, file string, findings []string, snapshot string) string {
	var b strings.Builder
	taken := snapshotTime(snapshot)
	fmt.Fprintf(&b, "WARNING: the global config %s looks damaged: %s.\n", file, strings.Join(findings, "; "))
	fmt.Fprintf(&b, "  Last good snapshot: %s (taken %s, %s ago). Nothing was changed.\n",
		snapshot, taken.UTC().Format(time.RFC3339), roundAge(o.Now().Sub(taken)))
	how, cmd := restoreCommand(o, file, snapshot)
	fmt.Fprintf(&b, "  To restore it, %s\n", how)
	if cmd != "" {
		fmt.Fprintf(&b, "    %s\n", cmd)
	}
	fmt.Fprintf(&b, "  If the change was intended (a /logout, an API-key switch, a project purge), make it the new baseline:\n    %s\n", AcceptCmd)
	return b.String()
}

func roundAge(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	return d.Round(time.Minute)
}

// restoreCommand is the layout-correct restore (CS-GCFG-009..011): how to
// run it, and the command ("" when none can be given).
func restoreCommand(o HealthOptions, file, snapshot string) (string, string) {
	atomic := func(dst string) (string, string) {
		tmp := dst + ".restore"
		return "copy the snapshot beside the file and rename it into place (atomic: every session sees the whole file at once):",
			fmt.Sprintf("cp %s %s && mv -f %s %s", ShellQuote(snapshot), ShellQuote(tmp), ShellQuote(tmp), ShellQuote(dst))
	}
	const exitFirst = "first exit every Claude session, host and sandboxes (tmux kill-server does not stop a sandbox)"
	inPlace := func(dst string) (string, string) {
		return exitFirst + ": an in-place write while one runs can itself be read torn. Then copy the snapshot over the file in place (cp keeps the inode every running sandbox has mounted; a rename would orphan them):",
			fmt.Sprintf("cp %s %s", ShellQuote(snapshot), ShellQuote(dst))
	}
	defaultFile := filepath.Join(filepath.Clean(o.Home), FileName)
	if file == defaultFile {
		l := Classify(o.Home, "")
		cfgDir := ConfigDir(o.Home)
		// A symlinked ~/.claude refuses the linked layout (CS-GCFG-023): a
		// link into it, or a restore of a dangling link's target through
		// it, gives no layout the sandboxes can use, so those cases get no
		// command (CS-GCFG-010/011).
		cfgDirLinked := false
		if ci, err := os.Lstat(cfgDir); err == nil && ci.Mode()&os.ModeSymlink != 0 {
			cfgDirLinked = true
		}
		switch l.Mode {
		case ModeLinked:
			return atomic(l.Target) // CS-GCFG-009
		case ModeRefused:
			if l.Resolved == l.Target && !cfgDirLinked {
				if _, err := os.Lstat(l.Target); errors.Is(err, fs.ErrNotExist) {
					return atomic(l.Target) // CS-GCFG-009: a dangling link
				}
			}
			// CS-GCFG-011: attach and join print no layout warning, so
			// the problem is named here.
			return fmt.Sprintf("first fix the layout of %s (%s), then restore from the snapshot above.", file, l.Problem), ""
		case ModeMissing:
			if ti, err := os.Lstat(l.Target); err == nil && ti.Mode().IsRegular() {
				if cfgDirLinked {
					// CS-GCFG-010: the link would be refused (CS-GCFG-023),
					// and a cp would make a split brain from an older
					// snapshot.
					return fmt.Sprintf("first fix the layout: the link %s is gone and %s is the live file (newer than the snapshot), but the config dir %s is itself a symlink, so a new link to it would be refused. Make %s a real directory, then restore the link (do not copy the snapshot).", file, l.Target, cfgDir, cfgDir), ""
				}
				// CS-GCFG-010: the link of a migrated layout was deleted;
				// the live file is newer than any snapshot.
				return fmt.Sprintf("the link is gone: %s is the live file (newer than the snapshot), so do not copy the snapshot; restore the link:", l.Target),
					fmt.Sprintf("ln -s %s %s", LinkText, ShellQuote(file))
			}
			return exitFirst + ": a running legacy sandbox still holds the deleted file and would never see the restored one. Then copy the snapshot back:",
				fmt.Sprintf("cp %s %s", ShellQuote(snapshot), ShellQuote(file))
		}
		return inPlace(file) // CS-GCFG-010
	}
	// CS-GCFG-011: $CLAUDE_CONFIG_DIR/.claude.json or .config.json.
	if fi, err := os.Lstat(file); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return inPlace(file)
	}
	return atomic(file)
}

// ShellQuote quotes s for a POSIX shell when it needs it.
func ShellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+=:,@%", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
