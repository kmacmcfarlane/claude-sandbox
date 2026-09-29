package globalcfg

// The host commands "claude-sandbox global-config migrate|revert"
// (CS-GCFG-041..053). Order: the refusals that need no lock (sandbox,
// layout, containers), the advisory checks, then Claude Code's own lock
// ($HOME/.claude.json.lock) and the swap, bounded by SwapBound.
//
// Residual, by design: host claude's unlocked writers (synchronous exit-time
// saves, the Configuration-error Reset) take no lock, and a write from one of
// them between the final re-stat and the rename is lost.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
)

const (
	// VerifiedClaudeVersion is the Claude Code release whose writers were
	// read and found to write through the link atomically (CS-GCFG-049).
	VerifiedClaudeVersion = "2.1.283"
	// SwapBound bounds the steps under the lock (CS-GCFG-051): well inside
	// Claude Code's ELOCKED retry budget (10-20 s).
	SwapBound = 5 * time.Second
	// LockWait is how long a fresh lock held by another process is waited
	// for: longer than LockStale, Claude Code's staleness rule
	// (proper-lockfile), so a lock a crashed claude left a moment ago is
	// reclaimed rather than refused. LockRefresh is its mtime refresh
	// (CS-GCFG-050).
	LockWait    = LockStale + 2*time.Second
	LockStale   = 10 * time.Second
	LockRefresh = 5 * time.Second
	// VersionTimeout bounds "claude --version" (CS-GCFG-049).
	VersionTimeout = 5 * time.Second
)

// SmokeTest is the check to run after a switch and after each Claude Code
// update.
const SmokeTest = "on the host and in a sandbox, /rename, /model and accepting a trust dialog must leave ~/.claude.json a symlink while ~/.claude/.claude.json changes"

// Refused is a refusal: nothing was changed.
type Refused struct{ Msg string }

func (r *Refused) Error() string { return r.Msg }

func refuse(format string, a ...any) error { return &Refused{Msg: fmt.Sprintf(format, a...)} }

// SwapOptions are migrate's and revert's inputs and seams.
type SwapOptions struct {
	// Home is $HOME; StateRoot the launcher's state root.
	Home, StateRoot string
	// ConfigDirEnv is the launcher's CLAUDE_CONFIG_DIR.
	ConfigDirEnv string
	// InSandbox refuses the command (CS-GCFG-044).
	InSandbox bool
	// Force overrides the container refusals (CS-GCFG-047).
	Force bool
	// Runner runs docker, pgrep and claude.
	Runner execx.Runner
	// Out carries the result, Err the warnings.
	Out, Err io.Writer
	// UID is the invoking user, for pgrep.
	UID int
	// Now and Sleep are the clock; nil means the real ones.
	Now   func() time.Time
	Sleep func(time.Duration)
	// Bound, LockWait, LockStale and LockRefresh override the constants
	// (tests); zero means the constant.
	Bound, LockWait, LockStale, LockRefresh time.Duration
	// BeforeSwap runs under the lock just before the final re-stat (a test
	// hook for CS-GCFG-051).
	BeforeSwap func()
	// Signals are caught while the lock is held (CS-GCFG-050); nil means
	// SIGINT, SIGTERM and SIGHUP. One the process inherited as ignored
	// (nohup) stays ignored. Tests use a signal the runner ignores.
	Signals []os.Signal
	// Notify registers the lock's signal channel; nil means signal.Notify.
	// A test seam: it hands the test the channel, so the test can wait for
	// a signal's delivery instead of sleeping.
	Notify func(c chan<- os.Signal, sig ...os.Signal)
	// DirOps are the store directories' seams.
	DirOps *hostdirs.Ops
}

func (o *SwapOptions) fill() {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Sleep == nil {
		o.Sleep = time.Sleep
	}
	if o.Bound == 0 {
		o.Bound = SwapBound
	}
	if o.LockWait == 0 {
		o.LockWait = LockWait
	}
	if o.LockStale == 0 {
		o.LockStale = LockStale
	}
	if o.LockRefresh == 0 {
		o.LockRefresh = LockRefresh
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.Err == nil {
		o.Err = io.Discard
	}
	if o.Signals == nil {
		o.Signals = []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP}
	}
	if o.Notify == nil {
		o.Notify = signal.Notify
	}
}

// common is the check both commands start with: host only, default layout
// only, and never the real home under go test.
func (o *SwapOptions) common() error {
	if o.InSandbox {
		return refuse("refused inside a sandbox: $HOME and the state directory here are the container's own. Run it on the host.")
	}
	if o.Home == "" || !filepath.IsAbs(o.Home) {
		return refuse("HOME is not an absolute path (%q)", o.Home)
	}
	if testing.Testing() && isRealHome(o.Home) {
		panic(fmt.Sprintf("globalcfg: a test would change %s under the real home; use a scratch HOME", filepath.Join(o.Home, FileName)))
	}
	if o.ConfigDirEnv != "" {
		return refuse("CLAUDE_CONFIG_DIR is set (%q): Claude Code keeps its global file inside that directory, which every sandbox mounts, so there is nothing to link. (A direnv .envrc is a likely source.) Run it where CLAUDE_CONFIG_DIR is unset if you mean the default layout.", o.ConfigDirEnv)
	}
	return nil
}

// Migrate is "global-config migrate" (CS-GCFG-041..051).
func Migrate(o SwapOptions) error {
	o.fill()
	if err := o.common(); err != nil {
		return err
	}
	l := Classify(o.Home, "")
	switch l.Mode {
	case ModeLinked:
		fmt.Fprintf(o.Out, "%s is already a link to %s (the linked layout); nothing to do.\n", l.Link, l.Target)
		return nil
	case ModeConfigJSON:
		return refuse("%s exists, so Claude Code uses it as the global config and not %s; there is nothing to migrate.", filepath.Join(ConfigDir(o.Home), LegacyConfigName), l.Link)
	case ModeMissing:
		return refuse("%s does not exist; there is nothing to migrate.", l.Link)
	case ModeRefused:
		return refuse("%s is not a regular file this command can migrate: %s. Fix it by hand (README \"Global config (~/.claude.json)\").", l.Link, l.Problem)
	}
	cfgDir := ConfigDir(o.Home)
	ci, err := os.Lstat(cfgDir)
	switch {
	case err != nil:
		return refuse("the config dir %s: %v", cfgDir, unwrapPath(err))
	case ci.Mode()&os.ModeSymlink != 0:
		return refuse("the config dir %s is a symlink: sandboxes mount it at its own path, so the linked layout would name a file they cannot see.", cfgDir)
	case !ci.IsDir():
		return refuse("the config dir %s is not a directory", cfgDir)
	}
	targetExists := false
	if ti, err := os.Lstat(l.Target); err == nil {
		if !ti.Mode().IsRegular() {
			return refuse("%s exists and is not a regular file (%s); move it away first.", l.Target, describeType(ti.Mode()))
		}
		targetExists = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return refuse("%s: %v", l.Target, unwrapPath(err))
	}
	src, err := os.ReadFile(l.Link)
	if err != nil {
		return refuse("%s: %v", l.Link, unwrapPath(err))
	}
	if !json.Valid(src) {
		return refuse("%s does not parse as JSON; restore it before migrating (Claude Code keeps copies in ~/.claude/backups/).", l.Link)
	}
	if targetExists {
		if err := sameBytes(l.Target, src); err != nil {
			return refuse("%s and %s both exist and differ (split brain): Claude Code uses %s. Merge what you need into it by hand, move the stale %s away (keep a copy), then run migrate again.", l.Link, l.Target, l.Link, l.Target)
		}
	}
	if err := o.checkContainers(l, false); err != nil {
		return err
	}
	o.warnHostClaude()
	o.warnVersion()
	store, err := OpenStore(o.StateRoot, l.Link, o.DirOps)
	if err != nil {
		return refuse("the state directory: %v", err)
	}

	lock, err := o.acquireLock(l.Link)
	if err != nil {
		return err
	}
	defer lock.release()
	start := o.Now()

	id1, err := statID(l.Link)
	if err != nil {
		return refuse("%s: %v", l.Link, err)
	}
	data, err := os.ReadFile(l.Link)
	if err != nil {
		return refuse("%s: %v", l.Link, unwrapPath(err))
	}
	if !json.Valid(data) {
		return refuse("%s no longer parses as JSON; nothing was changed.", l.Link)
	}
	pre, err := store.Write(PreMigratePrefix, data, o.Now())
	if err != nil {
		return refuse("keeping the pre-migration copy: %v; nothing was changed.", err)
	}
	swapped := false
	defer func() {
		// An aborted attempt leaves the source in place, identical to this
		// copy: remove it, so aborts never evict an earlier migration's
		// copy through the cap.
		if !swapped {
			os.Remove(pre)
		}
	}()

	created := false
	undo := func() {
		if created {
			os.Remove(l.Target)
		}
	}
	if _, err := os.Lstat(l.Target); err == nil {
		if err := sameBytes(l.Target, data); err != nil {
			return refuse("%s appeared or changed during the migration and differs from %s; nothing was changed.", l.Target, l.Link)
		}
		// Not undone by an abort (CS-GCFG-051): the file holds the OAuth
		// account and Claude Code writes it 0600 itself, so owner-only is
		// never wrong, while restoring a group- or world-readable mode
		// would re-open what this just closed.
		if err := os.Chmod(l.Target, 0o600); err != nil {
			return refuse("restricting %s to 0600: %v; nothing was changed.", l.Target, unwrapPath(err))
		}
	} else {
		if err := writeExcl(l.Target, data); err != nil {
			return refuse("writing %s: %v; nothing was changed.", l.Target, err)
		}
		created = true
	}
	if o.BeforeSwap != nil {
		o.BeforeSwap()
	}
	if id2, err := statID(l.Link); err != nil || id2 != id1 {
		undo()
		return refuse("%s changed during the migration (a Claude process wrote it); nothing was changed. Exit every Claude session and run it again.", l.Link)
	}
	if o.Now().Sub(start) > o.Bound {
		undo()
		return refuse("the migration took longer than %s under the lock; nothing was changed. Run it again.", o.Bound)
	}
	if sig := lock.interrupted(); sig != nil {
		undo()
		return refuse("interrupted (%v) before the swap; nothing was changed.", sig)
	}
	tmp := fmt.Sprintf("%s.migrate-%d", l.Link, o.Now().UnixMilli())
	if err := os.Symlink(LinkText, tmp); err != nil {
		undo()
		return refuse("making the link %s: %v; nothing was changed.", tmp, unwrapPath(err))
	}
	if err := os.Rename(tmp, l.Link); err != nil {
		os.Remove(tmp)
		undo()
		return refuse("replacing %s with the link: %v; nothing was changed.", l.Link, unwrapPath(err))
	}
	swapped = true
	syncDir(o.Home)
	store.Prune(PreMigratePrefix, KeepPreMigrate)
	fmt.Fprintf(o.Out, "Migrated: %s is now a link to %s, which holds the global config.\n", l.Link, LinkText)
	fmt.Fprintf(o.Out, "  Pre-migration copy: %s\n", pre)
	fmt.Fprintf(o.Out, "  Update every claude-sandbox launcher on this host (other checkouts, a pinned path in Paseo): an older one follows the link and single-file-mounts its target.\n")
	fmt.Fprintf(o.Out, "  Relaunch your sandboxes, then check: %s.\n", SmokeTest)
	fmt.Fprintf(o.Out, "  Undo with: claude-sandbox global-config revert\n")
	return nil
}

// Revert is "global-config revert" (CS-GCFG-045..053).
func Revert(o SwapOptions) error {
	o.fill()
	if err := o.common(); err != nil {
		return err
	}
	l := Classify(o.Home, "")
	switch {
	case l.Mode == ModeLegacy && !l.SplitBrain:
		fmt.Fprintf(o.Out, "%s is already a regular file (the legacy layout); nothing to do.\n", l.Link)
		return nil
	case l.Mode == ModeLegacy:
		if identicalFiles(l.Link, l.Target) {
			return o.finishRevert(l)
		}
		return refuse("%s is a regular file and %s also exists (split brain); there is no link to revert. Claude Code uses %s: move the stale %s away (keep a copy).", l.Link, l.Target, l.Link, l.Target)
	case l.Mode == ModeConfigJSON:
		return refuse("%s exists, so Claude Code uses it as the global config; revert does not apply.", filepath.Join(ConfigDir(o.Home), LegacyConfigName))
	case l.Mode == ModeMissing:
		return refuse("%s does not exist; there is nothing to revert.", l.Link)
	case l.Mode == ModeRefused:
		return refuse("%s is not the linked layout: %s. Fix it by hand (README \"Global config (~/.claude.json)\").", l.Link, l.Problem)
	}
	if err := sameFile(l.Link, l.Target); err != nil {
		return refuse("%v; nothing was changed.", err)
	}
	if err := o.checkContainers(l, true); err != nil {
		return err
	}
	o.warnHostClaude()
	store, err := OpenStore(o.StateRoot, l.Link, o.DirOps)
	if err != nil {
		return refuse("the state directory: %v", err)
	}

	lock, err := o.acquireLock(l.Link)
	if err != nil {
		return err
	}
	defer lock.release()
	start := o.Now()

	if l2 := Classify(o.Home, ""); l2.Mode != ModeLinked {
		return refuse("%s changed before the lock was taken; nothing was changed. Run it again.", l.Link)
	}
	if err := sameFile(l.Link, l.Target); err != nil {
		return refuse("%v; nothing was changed.", err)
	}
	id1, err := statID(l.Target)
	if err != nil {
		return refuse("%s: %v", l.Target, err)
	}
	data, err := os.ReadFile(l.Target)
	if err != nil {
		return refuse("%s: %v", l.Target, unwrapPath(err))
	}
	tmp := fmt.Sprintf("%s.revert-%d", l.Link, o.Now().UnixMilli())
	if err := writeExcl(tmp, data); err != nil {
		return refuse("writing %s: %v; nothing was changed.", tmp, err)
	}
	if o.BeforeSwap != nil {
		o.BeforeSwap()
	}
	if id2, err := statID(l.Target); err != nil || id2 != id1 {
		os.Remove(tmp)
		return refuse("%s changed during the revert (a Claude process wrote it); nothing was changed. Exit every Claude session and run it again.", l.Target)
	}
	if o.Now().Sub(start) > o.Bound {
		os.Remove(tmp)
		return refuse("the revert took longer than %s under the lock; nothing was changed. Run it again.", o.Bound)
	}
	if l2 := Classify(o.Home, ""); l2.Mode != ModeLinked || sameFile(l.Link, l.Target) != nil {
		os.Remove(tmp)
		return refuse("%s changed during the revert; nothing was changed. Run it again.", l.Link)
	}
	if sig := lock.interrupted(); sig != nil {
		os.Remove(tmp)
		return refuse("interrupted (%v) before the swap; nothing was changed.", sig)
	}
	if err := os.Rename(tmp, l.Link); err != nil {
		os.Remove(tmp)
		return refuse("replacing the link %s: %v; nothing was changed.", l.Link, unwrapPath(err))
	}
	syncDir(o.Home)
	// The layout is legacy now. Park the linked file under StateRoot so
	// nothing stale is left in the container-visible config dir.
	parked, err := parkTarget(store, l.Target, o.Now())
	if err != nil {
		return fmt.Errorf("reverted: %s is a regular file again, but %s could not be moved away (%v); it is still there, and each launch will warn about two files until you move it away yourself (keep a copy)", l.Link, l.Target, err)
	}
	fmt.Fprintf(o.Out, "Reverted: %s is a regular file again (the legacy layout).\n", l.Link)
	fmt.Fprintf(o.Out, "  The linked file %s is kept at %s\n", l.Target, parked)
	fmt.Fprintf(o.Out, "  Relaunch your sandboxes.\n")
	return nil
}

// finishRevert handles both files regular and byte-identical (CS-GCFG-052):
// a revert killed after its rename and before it parked the target — or a
// killed migrate, or a copy made by hand; the files cannot tell which. It
// keeps ~/.claude.json and parks the identical copy, after the same
// container checks and under the same lock as a revert.
func (o *SwapOptions) finishRevert(l Layout) error {
	if err := o.checkContainers(l, true); err != nil {
		return err
	}
	store, err := OpenStore(o.StateRoot, l.Link, o.DirOps)
	if err != nil {
		return refuse("the state directory: %v", err)
	}
	lock, err := o.acquireLock(l.Link)
	if err != nil {
		return err
	}
	defer lock.release()
	if !identicalFiles(l.Link, l.Target) {
		return refuse("%s and %s changed before the lock was taken; nothing was changed. Run it again.", l.Link, l.Target)
	}
	parked, err := parkTarget(store, l.Target, o.Now())
	if err != nil {
		return fmt.Errorf("%s and %s are identical, but the copy %s could not be moved away (%v); it is still there", l.Link, l.Target, l.Target, err)
	}
	fmt.Fprintf(o.Out, "Found two identical files: kept %s (the legacy layout) and parked the copy %s at %s\n", l.Link, l.Target, parked)
	return nil
}

// identicalFiles reports whether a and b are both regular files (Lstat) with
// the same bytes.
func identicalFiles(a, b string) bool {
	for _, p := range []string{a, b} {
		fi, err := os.Lstat(p)
		if err != nil || !fi.Mode().IsRegular() {
			return false
		}
	}
	data, err := os.ReadFile(a)
	return err == nil && sameBytes(b, data) == nil
}

// parkTarget copies target into the store as reverted-<ms> (fsynced and
// compared by Store.Write) and then unlinks it.
func parkTarget(store *Store, target string, now time.Time) (string, error) {
	data, err := os.ReadFile(target)
	if err != nil {
		return "", unwrapPath(err)
	}
	parked, err := store.Write(RevertedPrefix, data, now)
	if err != nil {
		return "", err
	}
	store.Prune(RevertedPrefix, KeepReverted)
	if err := os.Remove(target); err != nil {
		return "", unwrapPath(err)
	}
	syncDir(filepath.Dir(target))
	return parked, nil
}

// sameFile verifies that link, read through, is target: the lexical rule was
// Classify's; this is the shell's -ef (one device and inode).
func sameFile(link, target string) error {
	lfi, err := os.Stat(link)
	if err != nil {
		return fmt.Errorf("%s: %v", link, unwrapPath(err))
	}
	tfi, err := os.Lstat(target)
	if err != nil {
		return fmt.Errorf("%s: %v", target, unwrapPath(err))
	}
	if !os.SameFile(lfi, tfi) {
		return fmt.Errorf("%s does not lead to %s (not the same file)", link, target)
	}
	return nil
}

// fileID is what the re-stat compares.
type fileID struct {
	dev, ino uint64
	size     int64
	mtime    time.Time
}

func statID(p string) (fileID, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return fileID{}, unwrapPath(err)
	}
	if !fi.Mode().IsRegular() {
		return fileID{}, fmt.Errorf("not a regular file (%s)", describeType(fi.Mode()))
	}
	id := fileID{size: fi.Size(), mtime: fi.ModTime()}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		id.dev, id.ino = uint64(st.Dev), uint64(st.Ino)
	}
	return id, nil
}

// sameBytes errors unless p holds exactly want.
func sameBytes(p string, want []byte) error {
	got, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return errors.New("differs")
	}
	return nil
}

// writeExcl creates p (O_EXCL, 0600), writes data, fsyncs, and compares the
// bytes read back. A failure removes what it created.
func writeExcl(p string, data []byte) error {
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return unwrapPath(err)
	}
	fail := func(err error) error {
		f.Close()
		os.Remove(p)
		return err
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(p)
		return err
	}
	if err := sameBytes(p, data); err != nil {
		os.Remove(p)
		return fmt.Errorf("the copy does not match: %v", err)
	}
	syncDir(filepath.Dir(p))
	return nil
}

// ---- the lock (CS-GCFG-050) ----

type heldLock struct {
	path string
	stop chan struct{}
	done chan struct{}
	// sig receives the caught signals while the lock is held: they no
	// longer kill the process, so the lock is always released (CS-GCFG-050).
	sig chan os.Signal
}

// interrupted returns a signal caught since the lock was taken, or nil.
func (h *heldLock) interrupted() os.Signal {
	select {
	case s := <-h.sig:
		h.sig <- s // keep it for a later check
		return s
	default:
		return nil
	}
}

// acquireLock takes <link>.lock the way Claude Code (proper-lockfile) does:
// mkdir; a lock whose mtime is older than LockStale is removed and retried;
// the mtime is refreshed every LockRefresh while held.
func (o *SwapOptions) acquireLock(link string) (*heldLock, error) {
	path := link + ".lock"
	deadline := o.Now().Add(o.LockWait)
	for {
		err := os.Mkdir(path, 0o755)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, refuse("taking the lock %s: %v; nothing was changed.", path, unwrapPath(err))
		}
		if fi, serr := os.Lstat(path); serr == nil && time.Since(fi.ModTime()) > o.LockStale {
			// Look again right before removing: a holder that refreshed its
			// mtime meanwhile keeps its lock. A refresh between this look and
			// the removal is still lost — the race proper-lockfile has too.
			if fi2, serr2 := os.Lstat(path); serr2 != nil || time.Since(fi2.ModTime()) <= o.LockStale {
				continue
			}
			if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
				return nil, refuse("removing the stale lock %s: %v; nothing was changed.", path, unwrapPath(rerr))
			}
			continue
		}
		if !o.Now().Before(deadline) {
			return nil, refuse("the lock %s is held by another process (a Claude session saving its config); nothing was changed. Exit every Claude session and run it again.", path)
		}
		o.Sleep(50 * time.Millisecond)
	}
	h := &heldLock{path: path, stop: make(chan struct{}), done: make(chan struct{}), sig: make(chan os.Signal, 1)}
	// A signal inherited as ignored stays ignored (a nohup'ed run keeps
	// ignoring SIGHUP; the execx precedent). Notify with no signals would
	// relay every signal, so an empty list registers nothing.
	var caught []os.Signal
	for _, s := range o.Signals {
		if !signal.Ignored(s) {
			caught = append(caught, s)
		}
	}
	if len(caught) > 0 {
		o.Notify(h.sig, caught...)
	}
	go func() {
		defer close(h.done)
		t := time.NewTicker(o.LockRefresh)
		defer t.Stop()
		for {
			select {
			case <-h.stop:
				return
			case <-t.C:
				now := time.Now()
				os.Chtimes(path, now, now)
			}
		}
	}()
	return h, nil
}

func (h *heldLock) release() {
	close(h.stop)
	<-h.done
	os.Remove(h.path)
	signal.Stop(h.sig)
}

// ---- the container checks (CS-GCFG-045..047) ----

// psMountsFormat lists every container's name and its mount sources.
const psMountsFormat = "{{.Names}}\x1f{{.Mounts}}"

// inspectEnvFormat is one line per container: its name and its env as JSON.
const inspectEnvFormat = "{{.Name}}\x1f{{json .Config.Env}}"

// checkContainers refuses (or, with Force, warns) while any container on the
// host mounts $HOME/.claude.json or the link's target, or — for revert —
// carries CLAUDE_SANDBOX_GLOBAL_CONFIG naming the link's target.
func (o *SwapOptions) checkContainers(l Layout, revert bool) error {
	envTarget := ""
	if revert {
		envTarget = l.Target
	}
	users, err := containersUsing(o.Runner, []string{l.Link, l.Target}, envTarget)
	if err != nil {
		if !o.Force {
			return refuse("cannot tell whether a container uses %s (%v); nothing was changed. Rerun with --force to skip this check.", l.Link, err)
		}
		fmt.Fprintf(o.Err, "WARNING: cannot tell whether a container uses %s (%v); going on because of --force.\n", l.Link, err)
		return nil
	}
	if len(users) == 0 {
		return nil
	}
	names := strings.Join(users, ", ")
	if !o.Force {
		return refuse("these containers still use %s: %s. Exit them (/exit, or docker stop / docker rm — tmux kill-server does not stop a sandbox), or rerun with --force. Nothing was changed.", l.Link, names)
	}
	if revert {
		fmt.Fprintf(o.Err, "WARNING: --force: these containers still use the global config: %s. Linked containers still running keep a link to %s, which revert moves away: their saves are dropped, or re-create %s as a defaults-based file that the launcher's split-brain warning then flags. Stop them and relaunch.\n", names, l.Target, l.Target)
	} else {
		fmt.Fprintf(o.Err, "WARNING: --force: these containers still mount %s: %s. They keep its old inode: their writes are lost and they never see the migrated file. Stop them and relaunch (attach and join report drift).\n", l.Link, names)
	}
	return nil
}

// containersUsing lists every container on the host (docker ps -a
// --no-trunc, no label filter) and returns those with a mount whose cleaned
// source is exactly one of paths, plus — envTarget set — those whose
// environment (one docker inspect over all of them) sets EnvVar to exactly
// envTarget: a linked container of this home (another home's, or the empty
// override of a non-linked launch, does not use this file).
//
// The same-file test by device and inode stats a path, so it is applied only
// to a path whose last element is the file's own name (.claude.json): the
// spelling it exists for — a HOME reached through a symlinked ancestor —
// keeps that name, while a mount source with any other name (a project, an
// NFS or sshfs path whose server hangs) is never stat'ed (CS-GCFG-045). A
// filter on "under $HOME" would not do: the launcher's $HOME may be the other
// spelling, which lies outside this one.
func containersUsing(r execx.Runner, paths []string, envTarget string) ([]string, error) {
	out, err := r.Output(execx.Cmd{
		Name:   "docker",
		Args:   []string{"ps", "-a", "--no-trunc", "--format", psMountsFormat},
		Stderr: io.Discard,
	})
	if err != nil {
		return nil, fmt.Errorf("docker ps: %v", err)
	}
	want := map[string]bool{}
	var wantFI []os.FileInfo
	for _, p := range paths {
		want[filepath.Clean(p)] = true
		if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() {
			wantFI = append(wantFI, fi)
		}
	}
	// sameAsWanted: the same file by another spelling (a HOME reached
	// through a symlinked ancestor), by device and inode.
	sameAsWanted := func(p string, among []os.FileInfo) bool {
		if len(among) == 0 || !filepath.IsAbs(p) || filepath.Base(filepath.Clean(p)) != FileName {
			return false
		}
		fi, err := os.Stat(p)
		if err != nil {
			return false
		}
		for _, w := range among {
			if os.SameFile(fi, w) {
				return true
			}
		}
		return false
	}
	var names, users []string
	seen := map[string]bool{}
	add := func(n string) {
		if !seen[n] {
			seen[n] = true
			users = append(users, n)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		name, mounts, _ := strings.Cut(line, "\x1f")
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		names = append(names, name)
		for _, m := range strings.Split(mounts, ",") {
			if m = strings.TrimSpace(m); m != "" && (want[filepath.Clean(m)] || (filepath.IsAbs(m) && sameAsWanted(m, wantFI))) {
				add(name)
			}
		}
	}
	if envTarget == "" || len(names) == 0 {
		return users, nil
	}
	args := append([]string{"inspect", "--type", "container", "--format", inspectEnvFormat}, names...)
	// stdout is parsed even on a non-zero exit: docker prints the containers
	// it found before failing on one removed since the listing.
	iout, ierr := r.Output(execx.Cmd{Name: "docker", Args: args, Stderr: io.Discard})
	if ierr != nil && strings.TrimSpace(iout) == "" {
		return nil, fmt.Errorf("docker inspect: %v", ierr)
	}
	var targetFI []os.FileInfo
	if fi, err := os.Lstat(envTarget); err == nil && fi.Mode().IsRegular() {
		targetFI = append(targetFI, fi)
	}
	for _, line := range strings.Split(iout, "\n") {
		name, envJSON, ok := strings.Cut(strings.TrimRight(line, "\r"), "\x1f")
		if !ok {
			continue
		}
		var envs []string
		if json.Unmarshal([]byte(envJSON), &envs) != nil {
			continue
		}
		for _, e := range envs {
			v, ok := strings.CutPrefix(e, EnvVar+"=")
			if !ok || v == "" || !filepath.IsAbs(v) {
				continue
			}
			if filepath.Clean(v) == filepath.Clean(envTarget) || sameAsWanted(v, targetFI) {
				add(strings.TrimPrefix(strings.TrimSpace(name), "/"))
			}
		}
	}
	return users, nil
}

// ---- the advisory checks (CS-GCFG-048/049) ----

// warnHostClaude warns about host claude processes (pgrep -u <uid> -x claude).
func (o *SwapOptions) warnHostClaude() {
	if o.Runner == nil {
		return
	}
	out, err := o.Runner.Output(execx.Cmd{Name: "pgrep", Args: []string{"-u", strconv.Itoa(o.UID), "-x", "claude"}, Stderr: io.Discard})
	if err != nil {
		return // no match (exit 1), or no pgrep
	}
	pids := strings.Fields(out)
	if len(pids) == 0 {
		return
	}
	fmt.Fprintf(o.Err, "WARNING: %d host claude process(es) are running (pids %s). Exit them first: the lock keeps their locked saves out, but an unlocked exit-time save can still land during the swap and be lost.\n", len(pids), strings.Join(pids, ", "))
}

// lockedBuffer is a writer safe to read after the writer goroutine is gone.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *lockedBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *lockedBuffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

// warnVersion runs "claude --version" (VersionTimeout) and warns when it is
// not VerifiedClaudeVersion. No claude on PATH (a start error) is silent.
func (o *SwapOptions) warnVersion() {
	if o.Runner == nil {
		return
	}
	var out lockedBuffer
	p, err := o.Runner.Start(execx.Cmd{Name: "claude", Args: []string{"--version"}, Stdout: &out, Stderr: io.Discard, DieWithParent: true})
	if err != nil {
		return
	}
	done := make(chan error, 1)
	go func() { done <- p.Wait() }()
	version := ""
	select {
	case werr := <-done:
		if werr == nil {
			if f := strings.Fields(out.String()); len(f) > 0 {
				version = f[0]
			}
		}
	case <-time.After(VersionTimeout):
		p.Signal(os.Kill)
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	}
	if version == VerifiedClaudeVersion {
		return
	}
	if version == "" {
		version = "unknown"
	}
	fmt.Fprintf(o.Err, "WARNING: the linked layout was verified with Claude Code %s; the host runs %s. After migrating, run the smoke test: %s.\n", VerifiedClaudeVersion, version, SmokeTest)
}
