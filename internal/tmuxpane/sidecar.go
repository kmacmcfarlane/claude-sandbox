package tmuxpane

// The resurrect state file and the save hook's sidecar (CS-TMUX-031,
// 037..039). tmux-resurrect (read at cff343c) writes one
// tmux_resurrect_<YYYYmmddTHHMMSS>.txt per save; the save hook writes
// tmux_resurrect_<…>.claude-sandbox.json beside it. Upstream is dormant, so
// its line format and its prune glob are copied behaviour, pinned here.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/registry"
)

// SidecarVersion is the sidecar's schema version ("v").
const SidecarVersion = 1

// sidecarExt replaces a state file's ".txt".
const sidecarExt = ".claude-sandbox.json"

// stateFileRE is resurrect's own state file name (its prune glob
// tmux_resurrect_*.txt); the sidecar name falls outside it.
var stateFileRE = regexp.MustCompile(`^tmux_resurrect_[^/]*\.txt$`)

// sidecarRE is a sidecar written by the save hook.
var sidecarRE = regexp.MustCompile(`^(tmux_resurrect_[^/]*)\.claude-sandbox\.json$`)

// IsStateFileName reports whether base is a resurrect state file name.
func IsStateFileName(base string) bool { return stateFileRE.MatchString(base) }

// SidecarPath is the sidecar of a state file: ".txt" replaced by
// ".claude-sandbox.json" (CS-TMUX-037).
func SidecarPath(stateFile string) string {
	return strings.TrimSuffix(stateFile, ".txt") + sidecarExt
}

// StatePane is one "pane" line of a resurrect state file. Session, Window and
// Pane are the coordinates the sidecar is keyed by; Dir (field 8, the leading
// ":" removed, still in resurrect's escaped form) and FullCommand (field 11,
// "" for a bare shell) are for `tmux restore` (F4). FullCommandSaved is false
// when the line has no field 11 at all, so an empty FullCommand then says
// nothing about a bare shell (CS-TMUX-067).
type StatePane struct {
	Session          string
	Window           int
	Pane             int
	Dir              string
	FullCommand      string
	FullCommandSaved bool
}

// Key is the pane's coordinates as one comparable string.
func (p StatePane) Key() string { return coordKey(p.Session, p.Window, p.Pane) }

func coordKey(session string, window, pane int) string {
	return session + "\x00" + strconv.Itoa(window) + "\x00" + strconv.Itoa(pane)
}

// ParseStateFile returns the state file's "pane" lines:
//
//	pane <session> <window_index> <window_active> :<flags> <pane_index> <title> :<dir> <pane_active> <cmd> :<full command>
//
// tab-separated. A line without valid coordinates is skipped.
func ParseStateFile(data []byte) []StatePane {
	var out []StatePane
	for _, line := range bytes.Split(data, []byte("\n")) {
		f := strings.Split(strings.TrimRight(string(line), "\r"), "\t")
		if len(f) < 6 || f[0] != "pane" {
			continue
		}
		w, err1 := strconv.Atoi(f[2])
		p, err2 := strconv.Atoi(f[5])
		if err1 != nil || err2 != nil {
			continue
		}
		sp := StatePane{Session: f[1], Window: w, Pane: p}
		if len(f) > 7 {
			sp.Dir = strings.TrimPrefix(f[7], ":")
		}
		if len(f) > 10 {
			sp.FullCommand = strings.TrimPrefix(f[10], ":")
			sp.FullCommandSaved = true
		}
		out = append(out, sp)
	}
	return out
}

// Sidecar is what the save hook records for one resurrect save (CS-TMUX-038).
// Server is the tmux server that saved it (CS-TMUX-047), nil when unknown (a
// sidecar written before F4a, or a tmux without #{start_time}).
type Sidecar struct {
	V         int     `json:"v"`
	StateFile string  `json:"stateFile"`
	SavedAt   int64   `json:"savedAt"`
	Server    *Server `json:"server,omitempty"`
	Panes     []Row   `json:"panes"`
}

// Server identifies one tmux server lifetime (CS-TMUX-047): its pid and its
// start time in unix seconds (#{pid}, #{start_time}). A pid alone is reused
// across reboots; with the start time it is unique.
type Server struct {
	PID   int   `json:"pid"`
	Start int64 `json:"start"`
}

// sameServer reports whether a and b name one known server.
func sameServer(a, b *Server) bool {
	return a != nil && b != nil && *a == *b
}

// Row is one sandbox pane of a save: resurrect's coordinates and the pane's
// mark as the save hook left it. Per-window data a later feature adds (a
// window label, decision 52) goes beside Mark, never inside it.
type Row struct {
	Session string `json:"session"`
	Window  int    `json:"window"`
	Pane    int    `json:"pane"`
	Mark    Mark   `json:"mark"`
}

// tempPrefix names the save hook's temp files (pruned after an hour).
const tempPrefix = ".claude-sandbox-save-"

// WriteSidecar writes sc to path atomically: a 0600 temp file in the same
// directory, synced, renamed over path (CS-TMUX-038). On error the previous
// sidecar is untouched.
func WriteSidecar(path string, sc Sidecar) error {
	if sc.Panes == nil {
		sc.Panes = []Row{}
	}
	b, err := json.Marshal(sc)
	if err != nil {
		return err
	}
	return writeAtomic(path, b)
}

// writeAtomic writes b and a newline to path: a 0600 temp file in the same
// directory (named like the save hook's other temp files, so a crash leaves
// nothing PruneSidecars will not sweep), synced, renamed over path.
func writeAtomic(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), tempPrefix+"*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// PruneSidecars removes, in dir, every sidecar whose state file is gone and
// the save hook's temp files older than an hour (CS-TMUX-039). Nothing else
// is touched. It returns the first error, having tried every entry.
func PruneSidecars(dir string, now time.Time) error {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var first error
	rm := func(name string) {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) && first == nil {
			first = err
		}
	}
	for _, e := range ents {
		name := e.Name()
		if m := sidecarRE.FindStringSubmatch(name); m != nil {
			if _, err := os.Lstat(filepath.Join(dir, m[1]+".txt")); os.IsNotExist(err) {
				rm(name)
			}
			continue
		}
		if strings.HasPrefix(name, tempPrefix) && strings.HasSuffix(name, ".tmp") {
			if fi, err := e.Info(); err == nil && fi.Mode().IsRegular() && now.Sub(fi.ModTime()) > time.Hour {
				rm(name)
			}
		}
	}
	return first
}

// stampRE is resurrect's save stamp: date '+%Y%m%dT%H%M%S' (cff343c,
// scripts/helpers.sh). A save is named by it, never by an index
// (CS-TMUX-048): continuum saves most minutes, so "the 3rd newest" moves.
var stampRE = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}$`)

const statePrefix = "tmux_resurrect_"

// IsStamp reports whether s is a save stamp.
func IsStamp(s string) bool { return stampRE.MatchString(s) }

// StampOf returns the stamp of a state file or sidecar base name, "" when it
// has none.
func StampOf(base string) string {
	var st string
	switch {
	case stateFileRE.MatchString(base):
		st = strings.TrimSuffix(strings.TrimPrefix(base, statePrefix), ".txt")
	case sidecarRE.MatchString(base):
		st = strings.TrimSuffix(strings.TrimPrefix(base, statePrefix), sidecarExt)
	}
	if !IsStamp(st) {
		return ""
	}
	return st
}

// StateFileName is the state file base name of a stamp.
func StateFileName(stamp string) string { return statePrefix + stamp + ".txt" }

// SidecarName is the sidecar base name of a stamp.
func SidecarName(stamp string) string { return statePrefix + stamp + sidecarExt }

// StampTime is a stamp as a local time (resurrect stamps in local time);
// zero when it does not parse.
func StampTime(stamp string) time.Time {
	t, err := time.ParseInLocation("20060102T150405", stamp, time.Local)
	if err != nil {
		return time.Time{}
	}
	return t
}

// maxRecordFile caps a sidecar or lifetimes-index read (CS-TMUX-046).
const maxRecordFile = 1 << 20

// readOwned reads path for CS-TMUX-046: opened O_NOFOLLOW (never through a
// symlink), a regular file owned by the user, not group- or world-writable,
// at most max bytes.
func readOwned(path string, max int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return nil, fmt.Errorf("owned by uid %d, not the user", st.Uid)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("group- or world-writable (mode %04o)", fi.Mode().Perm())
	}
	if fi.Size() > max {
		return nil, fmt.Errorf("larger than %d bytes", max)
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("larger than %d bytes", max)
	}
	return b, nil
}

// ReadSidecar reads a sidecar with CS-TMUX-046's checks. Rows are not
// validated here: a bad row is that row's own outcome (ValidateRow).
func ReadSidecar(path string) (Sidecar, error) {
	var sc Sidecar
	b, err := readOwned(path, maxRecordFile)
	if err != nil {
		return sc, err
	}
	if err := json.Unmarshal(b, &sc); err != nil {
		return Sidecar{}, fmt.Errorf("does not parse: %v", err)
	}
	if sc.V != SidecarVersion {
		return Sidecar{}, fmt.Errorf("version %d, not %d", sc.V, SidecarVersion)
	}
	return sc, nil
}

var (
	hex64RE = regexp.MustCompile(`^[0-9a-f]{64}$`)
	modelRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:\[\]-]*$`)
)

// ValidateRow checks a row's mark before any of it is used (CS-TMUX-046):
// "" when it is usable, else the field that is not. Sidecar and pane-mark
// values reach argv, env and the terminal; a row that fails is never acted
// on, only cleared (decision row 3).
func ValidateRow(m Mark) string {
	switch {
	case m.V != MarkVersion:
		return "v"
	case m.State != StateActive && m.State != StatePending && m.State != StateCrashed:
		return "state"
	case m.State == StateCrashed && (m.Mode != ModeClaude || !registry.IsUUID(m.Conversation)):
		// CS-TMUX-075: only a claude session with a known conversation is
		// ever left crashed; its hint needs the id.
		return "state"
	case m.Mode != ModeClaude && m.Mode != ModeJoin && m.Mode != ModeRalph:
		return "mode"
	case !filepath.IsAbs(m.Project):
		return "project"
	case m.ConfigDir != "" && !filepath.IsAbs(m.ConfigDir):
		return "configDir"
	case m.CwdRoot != "" && !filepath.IsAbs(m.CwdRoot):
		return "cwdRoot"
	case m.ConfigDirEnv != nil && *m.ConfigDirEnv != "" && !filepath.IsAbs(*m.ConfigDirEnv):
		return "configDirEnv"
	case m.Worktree != "" && !worktreeNameRE.MatchString(m.Worktree):
		return "worktree"
	case m.Model != "" && !modelRE.MatchString(m.Model):
		return "model"
	case m.ContainerID != "" && !hex64RE.MatchString(m.ContainerID):
		return "containerId"
	case m.Conversation != "" && !registry.IsUUID(m.Conversation):
		return "conversation"
	}
	// Unreplayed names reach ResumeNotes' line; Replay and the rest of
	// what a line prints are printableMark's.
	for _, v := range append([]string{m.Container, m.Instance, m.Project, m.CwdRoot, m.ConfigDir, m.Name}, m.Unreplayed...) {
		if strings.IndexFunc(v, unprintable) >= 0 {
			return "a control or bidi character"
		}
	}
	if !printableMark(m) {
		return "a control or bidi character"
	}
	return ""
}
