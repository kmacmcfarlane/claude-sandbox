package tmuxpane

// The tmux-resurrect post-save-layout hook (CS-TMUX-030..040, plan
// sandbox-reboot-restore 05 § 2 as amended by 06..09; F3). resurrect runs it
// synchronously inside save.sh with the path of the state file it just wrote.
// It records which conversation each sandbox pane of that save holds, in a
// sidecar beside the state file, and carries the id forward in the pane mark.
// It is bounded, never prints, and never waits on tmux or docker: the caller
// logs the returned problems and exits 0.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
)

// SaveDeadline bounds the whole hook (CS-TMUX-040): continuum saves every
// minute, and a slow hook makes saves overlap.
var SaveDeadline = 3 * time.Second

// LauncherCommand is what tmux reports as pane_current_command while a
// launcher runs in the pane (CS-TMUX-001 makes argv[0] exactly this).
const LauncherCommand = "claude-sandbox"

// maxStateFile caps the state file read.
const maxStateFile = 16 << 20

// listPanesFormat is the one list-panes format the hook reads (CS-TMUX-031).
const listPanesFormat = "#{session_name}\t#{window_index}\t#{pane_index}\t#{pane_id}\t#{pane_current_command}\t#{" + Option + "}"

// dockerPSFormat lists sandbox containers for the liveness check
// (CS-TMUX-032).
const dockerPSFormat = "{{.ID}}\t{{.Names}}\t{{.State}}"

// SaveOptions are the hook's seams.
type SaveOptions struct {
	Runner execx.Runner
	// Now stamps savedAt and ages temp files; nil means time.Now. The
	// deadline always runs on the real clock.
	Now func() time.Time
	// Home and ConfigDirEnv (the tmux server's raw CLAUDE_CONFIG_DIR) name
	// the fallback registry dirs of a mark without registryDir.
	Home         string
	ConfigDirEnv string
	// Logf records a problem; the caller writes it to the hook's log.
	Logf func(format string, a ...any)
}

func (o SaveOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o SaveOptions) logf(format string, a ...any) {
	if o.Logf != nil {
		o.Logf(format, a...)
	}
}

// ErrNotStateFile is an argument that is not a resurrect state file
// (CS-TMUX-030): nothing is run.
var ErrNotStateFile = errors.New("not a tmux-resurrect state file")

// SaveResult says what one hook run did, for the tests and the log.
type SaveResult struct {
	Sidecar    string
	Rows       []Row
	WriteBacks int
	Skipped    int // write-backs left for the next save at the deadline
}

// livePane is a pane the hook keeps.
type livePane struct {
	id   string
	mark Mark
	row  Row
}

// Save runs the hook for stateFile (CS-TMUX-030..040).
func Save(stateFile string, o SaveOptions) (SaveResult, error) {
	start := time.Now()
	left := func() time.Duration {
		d := SaveDeadline - time.Since(start)
		if d > CallTimeout {
			return CallTimeout
		}
		return d
	}
	var res SaveResult

	// CS-TMUX-030: an absolute path to a regular tmux_resurrect_*.txt.
	if !filepath.IsAbs(stateFile) || !IsStateFileName(filepath.Base(stateFile)) {
		return res, fmt.Errorf("%w: %q", ErrNotStateFile, stateFile)
	}
	stateFile = filepath.Clean(stateFile)
	data, err := readRegular(stateFile, maxStateFile)
	if err != nil {
		return res, fmt.Errorf("%w: %v", ErrNotStateFile, err)
	}
	saved := map[string]StatePane{}
	for _, p := range ParseStateFile(data) {
		saved[p.Key()] = p
	}

	// CS-TMUX-031: one list-panes.
	out, ok := bounded(o.Runner, left(), "tmux", "list-panes", "-a", "-F", listPanesFormat)
	if !ok {
		return res, errors.New("tmux list-panes failed or timed out; no sidecar written")
	}
	var kept []*livePane
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(strings.TrimRight(line, "\r"), "\t", 6)
		if len(f) != 6 {
			continue
		}
		w, err1 := strconv.Atoi(f[1])
		p, err2 := strconv.Atoi(f[2])
		if err1 != nil || err2 != nil {
			continue
		}
		if _, ok := saved[coordKey(f[0], w, p)]; !ok {
			continue // not in this save (a grouped session, or opened since)
		}
		m, ok := ParseMark(f[5])
		if !ok || (m.State != StateActive && m.State != StatePending) {
			continue
		}
		// CS-TMUX-032: an active mark only while the launcher runs there.
		if m.State == StateActive && f[4] != LauncherCommand {
			continue
		}
		kept = append(kept, &livePane{id: f[3], mark: m,
			row: Row{Session: f[0], Window: w, Pane: p}})
	}

	kept = dropDeadContainers(kept, o, left)

	// CS-TMUX-033..036: resolve each active mark against the registry.
	regs := registryCache{}
	var writeBacks []*livePane
	for _, lp := range kept {
		if lp.mark.State == StateActive {
			if r, ok := resolve(lp.mark, o, regs); ok {
				updated := Apply(lp.mark, r)
				if updated.JSON() != lp.mark.JSON() {
					lp.mark = updated
					writeBacks = append(writeBacks, lp)
				}
			}
		}
		lp.row.Mark = lp.mark
		res.Rows = append(res.Rows, lp.row)
	}

	// CS-TMUX-037/038: the sidecar, for the state file resurrect keeps.
	target := sidecarTarget(stateFile, data)
	res.Sidecar = SidecarPath(target)
	if err := WriteSidecar(res.Sidecar, Sidecar{
		V: SidecarVersion, StateFile: filepath.Base(target), SavedAt: o.now().UnixMilli(), Panes: res.Rows,
	}); err != nil {
		return res, fmt.Errorf("write sidecar: %v", err)
	}
	// CS-TMUX-039.
	if err := PruneSidecars(filepath.Dir(stateFile), o.now()); err != nil {
		o.logf("prune: %v", err)
	}

	// CS-TMUX-035/040: write-backs, within the deadline.
	for i, lp := range writeBacks {
		t := left()
		if t <= 0 {
			res.Skipped = len(writeBacks) - i
			o.logf("deadline: %d mark write-back(s) left for the next save", res.Skipped)
			break
		}
		if _, ok := bounded(o.Runner, t, "tmux", "set-option", "-p", "-t", lp.id, Option, lp.mark.JSON()); !ok {
			o.logf("tmux set-option for %s failed", lp.id)
			continue
		}
		res.WriteBacks++
	}
	return res, nil
}

// liveStates are the container states an active mark may name.
var liveStates = map[string]bool{
	sessions.StateCreated: true, "running": true, "paused": true, sessions.StateRestarting: true,
}

// dropDeadContainers removes active marks whose container a successful
// docker listing does not show live (CS-TMUX-032). Any docker trouble keeps
// every mark.
func dropDeadContainers(kept []*livePane, o SaveOptions, left func() time.Duration) []*livePane {
	need := false
	for _, lp := range kept {
		if lp.mark.State == StateActive && (lp.mark.ContainerID != "" || lp.mark.Container != "") {
			need = true
		}
	}
	if !need {
		return kept
	}
	out, ok := bounded(o.Runner, left(), "docker", "ps", "-a", "--no-trunc",
		"--filter", "label="+sessions.LabelProject, "--format", dockerPSFormat)
	if !ok {
		o.logf("docker ps failed or timed out; active marks kept unchecked")
		return kept
	}
	byID, byName := map[string]string{}, map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimSpace(line), "\t")
		if len(f) != 3 {
			continue
		}
		byID[f[0]], byName[strings.TrimPrefix(f[1], "/")] = f[2], f[2]
	}
	var live []*livePane
	for _, lp := range kept {
		if lp.mark.State == StateActive {
			var state string
			var found bool
			switch {
			case lp.mark.ContainerID != "":
				state, found = byID[lp.mark.ContainerID]
			case lp.mark.Container != "":
				state, found = byName[lp.mark.Container]
			default:
				found, state = true, "running"
			}
			if !found || !liveStates[state] {
				o.logf("dropped a stale mark in %s (container %s: %s)", lp.id, lp.mark.Container, orGone(state, found))
				continue
			}
		}
		live = append(live, lp)
	}
	return live
}

func orGone(state string, found bool) string {
	if !found {
		return "gone"
	}
	return state
}

// registryCache reads each registry dir once per save.
type registryCache map[string][]RegistryRecord

func (c registryCache) read(dir string, o SaveOptions) []RegistryRecord {
	if recs, ok := c[dir]; ok {
		return recs
	}
	recs, err := ReadRegistry(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		o.logf("registry %s: %v", dir, err)
	}
	c[dir] = recs
	return recs
}

// registryDirs is where an active mark's record may be (CS-TMUX-033).
func registryDirs(m Mark, o SaveOptions) []string {
	if m.RegistryDir != "" {
		return []string{m.RegistryDir}
	}
	var dirs []string
	cfg := o.ConfigDirEnv
	if !filepath.IsAbs(cfg) && o.Home != "" {
		cfg = filepath.Join(o.Home, ".claude")
	}
	if filepath.IsAbs(cfg) {
		dirs = append(dirs, filepath.Join(cfg, "sessions"))
	}
	if filepath.IsAbs(o.Home) {
		dirs = append(dirs, filepath.Join(o.Home, ".cache", "claude-sandbox", "peers", "sessions"))
	}
	return dirs
}

// resolve finds m's record in its registry dirs, in order.
func resolve(m Mark, o SaveOptions, c registryCache) (RegistryRecord, bool) {
	for _, dir := range registryDirs(m, o) {
		if !filepath.IsAbs(dir) {
			continue
		}
		if r, ok := Match(m, c.read(filepath.Clean(dir), o)); ok {
			return r, true
		}
	}
	return RegistryRecord{}, false
}

// sidecarTarget is the state file the sidecar belongs to (CS-TMUX-037):
// "last"'s target when stateFile is byte-equal to it (resurrect then deletes
// stateFile), else stateFile.
func sidecarTarget(stateFile string, data []byte) string {
	dir := filepath.Dir(stateFile)
	link, err := os.Readlink(filepath.Join(dir, "last"))
	if err != nil {
		return stateFile
	}
	if !filepath.IsAbs(link) {
		link = filepath.Join(dir, link)
	}
	link = filepath.Clean(link)
	if link == stateFile || filepath.Dir(link) != dir || !IsStateFileName(filepath.Base(link)) {
		return stateFile
	}
	prev, err := readRegular(link, maxStateFile)
	if err != nil || !bytes.Equal(prev, data) {
		return stateFile
	}
	return link
}

// readRegular reads path when it is a regular file (never through a
// symlink) of at most max bytes.
func readRegular(path string, max int64) ([]byte, error) {
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
		return nil, fmt.Errorf("%s: not a regular file", path)
	}
	if fi.Size() > max {
		return nil, fmt.Errorf("%s: larger than %d bytes", path, max)
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s: larger than %d bytes", path, max)
	}
	return b, nil
}
