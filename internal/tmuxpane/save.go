package tmuxpane

// The tmux-resurrect post-save-layout hook (CS-TMUX-030..040, plan
// sandbox-reboot-restore 05 § 2 as amended by 06..09; F3). resurrect runs it
// synchronously inside save.sh with the path of the state file it just wrote.
// It records which conversation each sandbox pane of that save holds, in a
// sidecar beside the state file, and carries the id forward in the pane mark.
// It never prints, and every tmux or docker call it waits on is bounded (1 s
// each, 3 s for the whole run), so a hung tmux server or docker daemon costs
// a save a few seconds at most: the caller logs the returned problems and
// exits 0.

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
	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
	"github.com/kmacmcfarlane/claude-sandbox/internal/registry"
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

// listPanesFormat is the one list-panes format the hook reads (CS-TMUX-031):
// #{pid} and #{start_time} are the server's, the same on every line, and name
// its lifetime (CS-TMUX-047). The mark stays last: it is JSON, which holds no
// literal tab, and SplitN keeps it whole.
const listPanesFormat = "#{session_name}\t#{window_index}\t#{pane_index}\t#{pane_id}\t#{pane_current_command}\t#{pid}\t#{start_time}\t#{" + Option + "}"

// listPanesFields is listPanesFormat's field count.
const listPanesFields = 8

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
	Sidecar string
	// Server is the tmux server that ran the save, nil when unknown
	// (CS-TMUX-047).
	Server *Server
	// Kept is true when the sidecar was not written because its target
	// belongs to another server's lifetime (CS-TMUX-037/047).
	Kept       bool
	Rows       []Row
	WriteBacks int
	Skipped    int // write-backs left for the next save at the deadline
	Superseded int // write-backs dropped: the pane's mark changed since the list
	Relabeled  int // windows renamed to a /rename (CS-TMUX-041)
}

// livePane is a pane the hook keeps.
type livePane struct {
	id   string
	orig Mark // the mark as list-panes returned it
	mark Mark
	row  Row
	// superseded is set when the pane's mark changed since the list: its
	// window is no longer this mark's to rename (CS-TMUX-044).
	superseded bool
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
		f := strings.SplitN(strings.TrimRight(line, "\r"), "\t", listPanesFields)
		if len(f) != listPanesFields {
			continue
		}
		if res.Server == nil {
			res.Server = parseServer(f[5], f[6])
		}
		w, err1 := strconv.Atoi(f[1])
		p, err2 := strconv.Atoi(f[2])
		if err1 != nil || err2 != nil {
			continue
		}
		if _, ok := saved[coordKey(f[0], w, p)]; !ok {
			continue // not in this save (a grouped session, or opened since)
		}
		m, ok := ParseMark(f[7])
		if !ok || (m.State != StateActive && m.State != StatePending && m.State != StateCrashed) {
			continue
		}
		// CS-TMUX-032: an active mark only while the launcher runs there.
		if m.State == StateActive && f[4] != LauncherCommand {
			continue
		}
		kept = append(kept, &livePane{id: f[3], orig: m, mark: m,
			row: Row{Session: f[0], Window: w, Pane: p}})
	}

	pendStoppedContainers(kept, o, left)

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
	}
	// CS-TMUX-076: a crashed row whose conversation a kept active or pending
	// row of this save names was resumed elsewhere already: the restore acts
	// on that row, and a hint would point at a session that is back. The
	// pane's own crashed mark is left as it is.
	live := map[string]bool{}
	for _, lp := range kept {
		if lp.mark.State != StateCrashed && lp.mark.Conversation != "" {
			live[strings.ToLower(lp.mark.Conversation)] = true
		}
	}
	for _, lp := range kept {
		if lp.mark.State == StateCrashed && live[strings.ToLower(lp.mark.Conversation)] {
			o.logf("%s:%d.%d: crashed row for %s left out: another pane of this save holds it", printable(lp.row.Session), lp.row.Window, lp.row.Pane, printable(lp.mark.Conversation))
			continue
		}
		res.Rows = append(res.Rows, lp.row)
	}

	// CS-TMUX-037/038: the sidecar, for the state file resurrect keeps.
	target := sidecarTarget(stateFile, data)
	res.Sidecar = SidecarPath(target)
	if target != stateFile && otherServer(res.Sidecar, res.Server) {
		// CS-TMUX-037/047: a new server whose first save equals the previous
		// server's final one. That sidecar is the previous lifetime's final
		// state; the new server gets its own at its first distinct save.
		res.Kept = true
		o.logf("kept %s: it records another tmux server's final save", filepath.Base(res.Sidecar))
	} else {
		if err := WriteSidecar(res.Sidecar, Sidecar{
			V: SidecarVersion, StateFile: filepath.Base(target), SavedAt: o.now().UnixMilli(),
			Server: res.Server, Panes: res.Rows,
		}); err != nil {
			return res, fmt.Errorf("write sidecar: %v", err)
		}
		// CS-TMUX-047: the lifetimes index, within the deadline.
		if res.Server != nil {
			wait := min(IndexLockWait, SaveDeadline-time.Since(start))
			if wait <= 0 {
				o.logf("deadline: lifetimes index not updated")
			} else if err := UpdateLifetimes(filepath.Dir(target), *res.Server, StampOf(filepath.Base(target)), len(res.Rows), o.now(), wait); err != nil {
				o.logf("lifetimes index not updated: %v", err)
			}
		}
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
		// CS-TMUX-070: the pane may have been relaunched, unmarked or
		// restored since the list; write back only over the same mark.
		cur, ok := bounded(o.Runner, t, "tmux", "show-options", "-p", "-q", "-v", "-t", lp.id, Option)
		if !ok {
			o.logf("tmux show-options for %s failed; mark not written back", lp.id)
			continue
		}
		if now, ok := ParseMark(strings.TrimRight(cur, "\r\n")); !ok || now.JSON() != lp.orig.JSON() {
			res.Superseded++
			lp.superseded = true
			o.logf("the mark in %s changed since the list; not written back", lp.id)
			continue
		}
		if t = left(); t <= 0 {
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

	// CS-TMUX-041..044: an owned window follows a /rename, after the
	// write-backs and within the deadline.
	for _, lp := range kept {
		if lp.superseded || lp.mark.State != StateActive || lp.mark.NameSource != NameSourceUser {
			continue
		}
		if left() <= 0 {
			o.logf("deadline: window labels left for the next save")
			break
		}
		// CS-TMUX-044: the pane may have been relaunched since the list
		// (with or without a write-back): re-read its mark and refresh only
		// while it is still the one this save resolved.
		cur, ok := bounded(o.Runner, left(), "tmux", "show-options", "-p", "-q", "-v", "-t", lp.id, Option)
		if !ok {
			o.logf("tmux show-options for %s failed; window label not refreshed", lp.id)
			continue
		}
		if now, ok := ParseMark(strings.TrimRight(cur, "\r\n")); !ok || now.JSON() != lp.mark.JSON() {
			o.logf("the mark in %s changed since the list; window label not refreshed", lp.id)
			continue
		}
		if left() <= 0 {
			o.logf("deadline: window labels left for the next save")
			break
		}
		renamed, problem := RefreshLabel(Pane{Runner: o.Runner, ID: lp.id}, lp.mark, left)
		if problem != "" {
			o.logf("%s", problem)
		}
		if renamed {
			res.Relabeled++
		}
	}
	return res, nil
}

// parseServer reads the #{pid} and #{start_time} fields; nil unless both are
// positive integers (CS-TMUX-047).
func parseServer(pid, start string) *Server {
	p, err1 := strconv.Atoi(pid)
	st, err2 := strconv.ParseInt(start, 10, 64)
	if err1 != nil || err2 != nil || p <= 0 || st <= 0 {
		return nil
	}
	return &Server{PID: p, Start: st}
}

// otherServer reports whether the sidecar at path records a tmux server
// known to differ from cur (CS-TMUX-037/047). Only a proven difference keeps
// the sidecar: a current server that is unknown (a tmux without
// #{start_time}) writes as before, without "server", and so does a sidecar
// that records none or cannot be read.
func otherServer(path string, cur *Server) bool {
	if cur == nil {
		return false
	}
	sc, err := ReadSidecar(path)
	if err != nil || sc.Server == nil {
		return false
	}
	return *sc.Server != *cur
}

// liveStates are the container states an active mark may name.
var liveStates = map[string]bool{
	sessions.StateCreated: true, "running": true, "paused": true, sessions.StateRestarting: true,
}

// pendStoppedContainers records as pending the active marks whose container a
// successful docker listing does not show live (CS-TMUX-032/072). Every kept
// active mark's pane still runs the launcher (a mark left by a launcher killed
// outright was dropped by the pane_current_command filter), so a stopped
// container here is one its launcher is still classifying — in its die wait at
// a shutdown, say — and the row is kept with the last good conversation. The
// pane's own mark is not written back: the launcher settles it moments later
// (CS-TMUX-071). Any docker trouble keeps every mark active.
func pendStoppedContainers(kept []*livePane, o SaveOptions, left func() time.Duration) {
	need := false
	for _, lp := range kept {
		if lp.mark.State == StateActive && (lp.mark.ContainerID != "" || lp.mark.Container != "") {
			need = true
		}
	}
	if !need {
		return
	}
	out, ok := bounded(o.Runner, left(), "docker", "ps", "-a", "--no-trunc",
		"--filter", "label="+sessions.LabelProject, "--format", dockerPSFormat)
	if !ok {
		o.logf("docker ps failed or timed out; active marks kept unchecked")
		return
	}
	byID, byName := map[string]string{}, map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimSpace(line), "\t")
		if len(f) != 3 {
			continue
		}
		byID[f[0]], byName[strings.TrimPrefix(f[1], "/")] = f[2], f[2]
	}
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
				o.logf("recorded the mark in %s as pending (container %s: %s; its launcher still runs)", lp.id, lp.mark.Container, orGone(state, found))
				lp.mark.State = StatePending
			}
		}
	}
}

func orGone(state string, found bool) string {
	if !found {
		return "gone"
	}
	return state
}

// registryCache reads each registry dir once per save.
type registryCache map[string][]registry.Record

func (c registryCache) read(dir string, o SaveOptions) []registry.Record {
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
		// The LEGACY peers root on purpose: a mark without registryDir comes
		// from a container that predates the peers move (CS-DIR-011). Every
		// later mark names the dir its launch applied, legacy or new root.
		dirs = append(dirs, filepath.Join(hostdirs.LegacyPeersRoot(o.Home), "sessions"))
	}
	return dirs
}

// resolve finds m's record in its registry dirs, in order.
func resolve(m Mark, o SaveOptions, c registryCache) (registry.Record, bool) {
	for _, dir := range registryDirs(m, o) {
		if !filepath.IsAbs(dir) {
			continue
		}
		if r, ok := Match(m, c.read(filepath.Clean(dir), o)); ok {
			return r, true
		}
	}
	return registry.Record{}, false
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
