package tmuxpane

// The resurrect state file and the save hook's sidecar (CS-TMUX-031,
// 037..039). tmux-resurrect (read at cff343c) writes one
// tmux_resurrect_<YYYYmmddTHHMMSS>.txt per save; the save hook writes
// tmux_resurrect_<…>.claude-sandbox.json beside it. Upstream is dormant, so
// its line format and its prune glob are copied behaviour, pinned here.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
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
// "" for a bare shell) are for `tmux restore` (F4).
type StatePane struct {
	Session     string
	Window      int
	Pane        int
	Dir         string
	FullCommand string
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
		}
		out = append(out, sp)
	}
	return out
}

// Sidecar is what the save hook records for one resurrect save (CS-TMUX-038).
type Sidecar struct {
	V         int    `json:"v"`
	StateFile string `json:"stateFile"`
	SavedAt   int64  `json:"savedAt"`
	Panes     []Row  `json:"panes"`
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
