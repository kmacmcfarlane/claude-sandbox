package tmuxpane

// The resurrect hook forms of `tmux restore` (CS-TMUX-064..068; plan
// sandbox-reboot-restore 10 § 6, 08 § 1..3, 11 § 3/§ 4, 12 § 1; F4c).
//
//	set -g @resurrect-hook-pre-restore-all  '<shim> tmux restore --pin'
//	set -g @resurrect-hook-post-restore-all '<shim> tmux restore --rearm'
//
// resurrect runs both synchronously inside restore.sh (at boot too, from the
// tmux server's environment), so neither may hang it: every tmux call is
// bounded (CallTimeout, and never past the whole-run deadline), the whole
// run stops starting new work at its deadline (PinDeadline 2 s, RearmDeadline
// 5 s), nothing is printed, and every problem goes to the caller's log.
//
// --pin records, before resurrect creates anything, which save this restore
// reads (last's target, which a continuum save mid-restore could otherwise
// move), which panes already existed, and the sparse verdict on that save;
// a sparse save also leaves the notice (CS-TMUX-050) that the next command
// the operator types prints and claims. --rearm runs after resurrect created
// the panes and typed their processes: it gives every NEW pane that matches
// a row of the pinned save that row as a pending mark, and types the restore
// into the pending ones that sat at a bare shell when saved (answer 51 a) —
// only under --all's typing guard (idleshell.go): the pane's own shell leads
// its foreground group, nobody looks at it, the line is cleared first.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
)

// PinDeadline bounds --pin's whole run (11 § 3): the pin itself first, the
// sparse verdict and the notice only if they finish in time.
var PinDeadline = 2 * time.Second

// RearmDeadline bounds --rearm's whole run (11 § 3): rows still unhandled
// then are logged and left to their typed restore.
var RearmDeadline = 5 * time.Second

// PinMaxAge is how old a pin may be and still be this restore's: the typed
// --resurrected restores and --rearm read only an unconsumed pin of the
// running server at most this old (08 § 3); a resurrect restore takes
// seconds.
const PinMaxAge = 10 * time.Minute

// PinKeep is the age past which a pin, consumed or not, is pruned
// (round-4 low 2).
const PinKeep = 24 * time.Hour

// PinVersion is the pin's schema version ("v").
const PinVersion = 1

// RetypeKeys is the text --rearm types into a pending pane that sat at a
// bare shell (answer 51 a), and --all into an armed one: the same entry
// resurrect types into an active one, so only an operator's own typing ever
// claims the notice (12 § 1). It is sent inside TypeKeys (C-e C-u first).
const RetypeKeys = "claude-sandbox tmux restore --resurrected"

// pinPrefix names the pins in the resurrect dir:
// claude-sandbox-restore-pin.<serverPid>.<at>[.consumed].json, outside
// resurrect's prune glob and the save stamp patterns.
const pinPrefix = "claude-sandbox-restore-pin."

var pinRE = regexp.MustCompile(`^claude-sandbox-restore-pin\.([0-9]{1,10})\.([0-9]{1,16})(\.consumed)?\.json$`)

// PinName is a pin's base name.
func PinName(serverPID int, at int64) string {
	return fmt.Sprintf("%s%d.%d.json", pinPrefix, serverPID, at)
}

// PinVerdict is the sparse verdict a pin carries (CS-TMUX-050/064). Known is
// false when no baseline was found (or the save has no sidecar): no warning.
type PinVerdict struct {
	N         int  `json:"n"`
	M         int  `json:"m"`
	K         int  `json:"k"`
	Lifetimes bool `json:"lifetimes"`
	Known     bool `json:"known"`
	Sparse    bool `json:"sparse"`
}

// Line is the verdict's sparse line for stamp ("" when not sparse).
func (v *PinVerdict) Line(stamp string) string {
	if v == nil {
		return ""
	}
	return Sparse{Stamp: stamp, N: v.N, M: v.M, K: v.K, Lifetimes: v.Lifetimes, Known: v.Known, Sparse: v.Sparse}.Line()
}

// Pin is one restore's pin (08 § 3, 10 § 6.1): the save it reads (Stamp, and
// its sidecar and state file names), the panes that existed before resurrect
// created any, and the sparse verdict (null when it did not finish within
// PinDeadline). At is unix milliseconds.
type Pin struct {
	V           int         `json:"v"`
	ServerPID   int         `json:"serverPid"`
	Server      *Server     `json:"server,omitempty"`
	At          int64       `json:"at"`
	Stamp       string      `json:"stamp"`
	Sidecar     string      `json:"sidecar"`
	StateFile   string      `json:"stateFile"`
	Preexisting []string    `json:"preexisting"`
	Sparse      *PinVerdict `json:"sparse"`
}

// HookOptions are the hook forms' seams.
type HookOptions struct {
	Runner execx.Runner
	// Dir resolves the resurrect dir (CS-TMUX-045); called once.
	Dir func() string
	// CacheDir holds the notice file.
	CacheDir string
	// Now stamps the pin and ages pins; nil means time.Now. The deadlines
	// always run on the real clock.
	Now func() time.Time
	// Logf records a problem; the caller writes it to the hook's log.
	Logf func(format string, a ...any)
	// Proc is --rearm's foreground-process check before it types (IdleShell).
	Proc ProcOptions
}

func (o HookOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o HookOptions) logf(format string, a ...any) {
	if o.Logf != nil {
		o.Logf(format, a...)
	}
}

// deadlineClock bounds a hook run: left is what a single call may take (at
// most CallTimeout, never past the deadline; <= 0 once it passed).
type deadlineClock time.Time

func (d deadlineClock) left() time.Duration {
	return min(time.Until(time.Time(d)), CallTimeout)
}

func (d deadlineClock) passed() bool { return !time.Now().Before(time.Time(d)) }

// NoticeText is the @claude-sandbox-notice value (11 § 4.1): digits and fixed
// words only, so it holds no "#" (a status-format hazard) and no user data.
func NoticeText(n, m int) string {
	return fmt.Sprintf("sparse restore: %d of %d sandbox panes — claude-sandbox tmux restore --list", n, m)
}

// PinResult says what --pin did, for the tests and the log.
type PinResult struct {
	// Path is the pin written, "" when none was.
	Path string
	Pin  Pin
	// Noticed is true when the sparse notice file was written; OptionSet
	// when the tmux option was set too.
	Noticed, OptionSet bool
	Pruned             int
}

// pinPanesFormat is --pin's one list-panes: the pre-existing pane ids and the
// server (the same on every line).
const pinPanesFormat = "#{pane_id}\t#{pid}\t#{start_time}"

// PinRestore is --pin (CS-TMUX-064), resurrect's pre-restore-all hook.
func PinRestore(o HookOptions) PinResult {
	dl := deadlineClock(time.Now().Add(PinDeadline))
	var res PinResult
	dir := o.Dir()
	if !filepath.IsAbs(dir) {
		o.logf("the resurrect dir %q is not absolute; no pin written", dir)
		return res
	}
	out, ok := bounded(o.Runner, dl.left(), "tmux", "list-panes", "-a", "-F", pinPanesFormat)
	if !ok {
		o.logf("tmux list-panes failed or timed out; no pin written")
		return res
	}
	pre := []string{}
	var srv *Server
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) != 3 || !paneID.MatchString(f[0]) {
			continue
		}
		pre = append(pre, f[0])
		if srv == nil {
			srv = parseServer(f[1], f[2])
		}
	}
	if srv == nil {
		o.logf("the tmux server reported no pid and start time; no pin written")
		return res
	}
	saves, err := OpenSaves(dir)
	if err != nil {
		o.logf("cannot read the resurrect dir %s: %v; no pin written", dir, err)
		return res
	}
	stamp := saves.Last()
	if stamp == "" || !saves.HasState(stamp) {
		o.logf("the \"last\" link in %s names no save; no pin written", dir)
		return res
	}
	now := o.now()
	pin := Pin{V: PinVersion, ServerPID: srv.PID, Server: srv, At: now.UnixMilli(), Stamp: stamp,
		Sidecar: SidecarName(stamp), StateFile: StateFileName(stamp), Preexisting: pre}
	path := filepath.Join(dir, PinName(srv.PID, pin.At))
	if err := writePin(path, pin); err != nil {
		o.logf("write pin: %v", err)
		return res
	}
	res.Path, res.Pin = path, pin

	// The verdict, only within the deadline: a scan it cannot finish is
	// "unknown" and the pin keeps "sparse": null.
	v, ok := pinVerdict(saves, stamp, dl, o)
	if !ok {
		o.logf("deadline: the sparse verdict on %s did not finish; pinned without it", stamp)
	} else {
		pin.Sparse = v
		if err := writePin(path, pin); err != nil {
			o.logf("write pin verdict: %v", err)
		}
		res.Pin = pin
		if v.Sparse {
			if err := WriteNotice(o.CacheDir, Notice{Stamp: stamp, N: v.N, M: v.M, K: v.K, Lifetimes: v.Lifetimes, At: now.UnixMilli()}); err != nil {
				o.logf("write the sparse notice: %v", err)
			} else {
				res.Noticed = true
			}
			if t := dl.left(); t <= 0 {
				o.logf("deadline: %s not set", NoticeOption)
			} else if _, ok := bounded(o.Runner, t, "tmux", "set", "-g", NoticeOption, NoticeText(v.N, v.M)); !ok {
				o.logf("tmux set -g %s failed", NoticeOption)
			} else {
				res.OptionSet = true
			}
		}
	}
	if !dl.passed() {
		res.Pruned = prunePins(dir, now, o)
	}
	return res
}

// pinVerdict judges stamp by the sparse rule (CS-TMUX-050) before the
// deadline; ok is false when it could not finish.
func pinVerdict(saves *Saves, stamp string, dl deadlineClock, o HookOptions) (*PinVerdict, bool) {
	if dl.passed() {
		return nil, false
	}
	sc, err := saves.Sidecar(stamp)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return &PinVerdict{}, true // no record: never sparse
	case err != nil:
		o.logf("cannot read the sidecar of %s: %v", stamp, err)
		return &PinVerdict{}, true
	}
	idx, _ := ReadLifetimes(saves.Dir)
	s, done := saves.sparseOf(stamp, len(sc.Panes), sc.Server, idx, dl.passed)
	if !done || dl.passed() {
		return nil, false
	}
	return &PinVerdict{N: s.N, M: s.M, K: s.K, Lifetimes: s.Lifetimes, Known: s.Known, Sparse: s.Sparse}, true
}

// writePin writes a pin atomically, 0600 (the sidecar's writer).
func writePin(path string, p Pin) error {
	if p.Preexisting == nil {
		p.Preexisting = []string{}
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return writeAtomic(path, b)
}

// pinFile is one pin file found in the dir.
type pinFile struct {
	name     string
	pid      int
	at       int64
	consumed bool
}

func listPins(dir string) []pinFile {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []pinFile
	for _, e := range ents {
		m := pinRE.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		pid, err1 := strconv.Atoi(m[1])
		at, err2 := strconv.ParseInt(m[2], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, pinFile{name: e.Name(), pid: pid, at: at, consumed: m[3] != ""})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].at > out[j].at })
	return out
}

// prunePins removes pins, consumed or not, older than PinKeep by the time in
// their name; only regular files matching the pin pattern are touched.
func prunePins(dir string, now time.Time, o HookOptions) int {
	n := 0
	for _, p := range listPins(dir) {
		if now.Sub(time.UnixMilli(p.at)) <= PinKeep {
			continue
		}
		path := filepath.Join(dir, p.name)
		if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			o.logf("prune %s: %v", p.name, err)
			continue
		}
		n++
	}
	return n
}

// FindPin is the running server srv's newest pin, when it is unconsumed and
// at most PinMaxAge old (08 § 3): read with CS-TMUX-046's checks, its names
// checked against its stamp and its pane ids against tmux's shape. Only the
// newest pin of the server is ever looked at, consumed or not: when it is
// consumed, too old, from the future or does not read as one, there is no
// pin. An older pin is never taken in its place — its pane list is not this
// restore's, so --rearm would type into panes in use.
func FindPin(dir string, srv Server, now time.Time) (Pin, string, bool) {
	for _, p := range listPins(dir) {
		if p.pid != srv.PID {
			continue
		}
		if p.consumed {
			return Pin{}, "", false
		}
		at := time.UnixMilli(p.at)
		if now.Sub(at) > PinMaxAge || at.Sub(now) > time.Minute {
			return Pin{}, "", false
		}
		path := filepath.Join(dir, p.name)
		pin, err := readPin(path)
		if err != nil || pin.ServerPID != srv.PID || pin.At != p.at ||
			(pin.Server != nil && *pin.Server != srv) {
			return Pin{}, "", false
		}
		return pin, path, true
	}
	return Pin{}, "", false
}

func readPin(path string) (Pin, error) {
	var p Pin
	b, err := readOwned(path, maxRecordFile)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return Pin{}, fmt.Errorf("does not parse: %v", err)
	}
	switch {
	case p.V != PinVersion:
		return Pin{}, fmt.Errorf("version %d, not %d", p.V, PinVersion)
	case !IsStamp(p.Stamp) || p.Sidecar != SidecarName(p.Stamp) || p.StateFile != StateFileName(p.Stamp):
		return Pin{}, errors.New("its save names do not match its stamp")
	}
	for _, id := range p.Preexisting {
		if !paneID.MatchString(id) {
			return Pin{}, errors.New("a pane id is not tmux's")
		}
	}
	return p, nil
}

// ConsumePin renames a pin to its ".consumed.json" name, so overlapping
// restores each consume their own (08 § 3).
func ConsumePin(path string) error {
	return os.Rename(path, strings.TrimSuffix(path, ".json")+".consumed.json")
}

// RearmResult says what --rearm did, for the tests and the log.
type RearmResult struct {
	Pin string
	// Marked and Retyped count the new panes given a pending mark and the
	// ones the restore was typed into; Skipped the rows not armed (a moved
	// layout, a pane already marked, a row that cannot be used).
	Marked, Retyped, Skipped int
	// Consumed is true when the pin was renamed aside (every row handled).
	Consumed bool
	// Late counts the rows left at the deadline.
	Late int
}

// rearmPanesFormat is --rearm's one list-panes. The mark stays last (JSON
// holds no literal tab); a path holding a tab shifts the mark field, which
// then reads as a mark that does not parse — a pane --rearm leaves alone.
const rearmPanesFormat = "#{pane_id}\t#{session_name}\t#{window_index}\t#{pane_index}\t#{pid}\t#{start_time}\t#{pane_current_command}\t#{pane_current_path}\t#{" + Option + "}"

// rearmRecheckFormat is the per-pane re-check right before a mark is set: the
// current command and the mark, plus what IdleShell needs before the keys
// (mode, synchronize-panes, the pane's own pid). The mark stays last.
const rearmRecheckFormat = "#{pane_current_command}\t#{pane_in_mode}\t#{pane_synchronized}\t#{pane_pid}\t#{" + Option + "}"

// rearmFocusUnknown is the refusal when the focus re-read gets no answer.
const rearmFocusUnknown = "cannot tell whether a client is looking at it (tmux list-panes did not answer)"

type rearmPane struct {
	id, cmd, path, raw string
}

// Rearm is --rearm (CS-TMUX-066/067), resurrect's post-restore-all hook.
func Rearm(o HookOptions) RearmResult {
	dl := deadlineClock(time.Now().Add(RearmDeadline))
	var res RearmResult
	dir := o.Dir()
	if !filepath.IsAbs(dir) {
		o.logf("the resurrect dir %q is not absolute; nothing re-armed", dir)
		return res
	}
	out, ok := bounded(o.Runner, dl.left(), "tmux", "list-panes", "-a", "-F", rearmPanesFormat)
	if !ok {
		o.logf("tmux list-panes failed or timed out; nothing re-armed")
		return res
	}
	panes := map[string]rearmPane{}
	var srv *Server
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(strings.TrimRight(line, "\r"), "\t", 9)
		if len(f) != 9 || !paneID.MatchString(f[0]) {
			continue
		}
		if srv == nil {
			srv = parseServer(f[4], f[5])
		}
		w, err1 := strconv.Atoi(f[2])
		p, err2 := strconv.Atoi(f[3])
		if err1 != nil || err2 != nil {
			continue
		}
		panes[coordKey(f[1], w, p)] = rearmPane{id: f[0], cmd: f[6], path: f[7], raw: f[8]}
	}
	if srv == nil {
		o.logf("the tmux server reported no pid and start time; nothing re-armed")
		return res
	}
	pin, path, ok := FindPin(dir, *srv, o.now())
	if !ok {
		// Never guess which panes are new (08 § 3).
		o.logf("no usable pin of this tmux server from the last %d minutes (--pin did not run, or failed); nothing re-armed", int(PinMaxAge/time.Minute))
		return res
	}
	res.Pin = path
	sc, err := ReadSidecar(filepath.Join(dir, pin.Sidecar))
	if errors.Is(err, os.ErrNotExist) {
		// The pinned save has no record: nothing to arm, and no typed
		// restore will find a row in it either.
		res.Consumed = consume(path, o)
		return res
	}
	if err != nil {
		o.logf("cannot read %s: %v; nothing re-armed", pin.Sidecar, err)
		return res
	}
	data, err := readOwned(filepath.Join(dir, pin.StateFile), maxStateFile)
	if err != nil {
		o.logf("cannot read %s: %v; nothing re-armed", pin.StateFile, err)
		return res
	}
	saved := map[string]StatePane{}
	for _, sp := range ParseStateFile(data) {
		saved[sp.Key()] = sp
	}
	pre := map[string]bool{}
	for _, id := range pin.Preexisting {
		pre[id] = true
	}

	// shell is the default shell's basename, asked once per run, only when
	// a pane is to be typed into.
	var shell string
	var shellAsked bool
	for i, row := range sc.Panes {
		// late stops the run at the deadline: the rows from i on are left
		// to their typed restore, and the pin stays unconsumed for them.
		late := func() bool {
			if !dl.passed() {
				return false
			}
			res.Late = len(sc.Panes) - i
			o.logf("deadline: %d row(s) of %s left to their typed restore", res.Late, pin.Stamp)
			return true
		}
		if late() {
			break
		}
		coords := fmt.Sprintf("%s:%d.%d", printable(row.Session), row.Window, row.Pane)
		key := coordKey(row.Session, row.Window, row.Pane)
		p, ok := panes[key]
		if !ok || pre[p.id] {
			continue // not a pane this restore created (08 § 1.1)
		}
		sp, ok := saved[key]
		if !ok {
			res.Skipped++
			o.logf("%s: no pane line in %s; not re-armed", coords, pin.StateFile)
			continue
		}
		if p.raw != "" || p.cmd == LauncherCommand {
			// A typed restore marked it first, or runs there now and marks
			// it itself; it reads its own mark.
			res.Skipped++
			continue
		}
		if field := ValidateRow(row.Mark); field != "" {
			res.Skipped++
			o.logf("%s: the row's %s cannot be used; not re-armed", coords, field)
			continue
		}
		if why := rearmMoved(sp.Dir, p.path, row.Mark); why != "" {
			// That pane's typed restore then reads this server's unconsumed
			// pin (at most PinMaxAge old; consumed only when this run ends)
			// before last (round-4 low 4).
			res.Skipped++
			o.logf("%s: %s; not re-armed", coords, why)
			continue
		}
		// The pane may have been marked since the list, or a typed restore
		// may be running in it now: right before the set, arm only a pane
		// that still holds no mark and runs no launcher. (A typed restore
		// that started AND finished with a final outcome — clearing its own
		// mark — inside this window is not seen; the window is the time
		// since the list.)
		cur, ok := bounded(o.Runner, dl.left(), "tmux", "display-message", "-p", "-t", p.id, rearmRecheckFormat)
		if !ok {
			if late() {
				break
			}
			res.Skipped++
			o.logf("%s: tmux display-message failed; not re-armed", coords)
			continue
		}
		f := strings.SplitN(strings.TrimRight(cur, "\r\n"), "\t", 5)
		if len(f) != 5 || f[4] != "" || f[0] == LauncherCommand {
			res.Skipped++
			continue
		}
		pid, _ := strconv.Atoi(f[3]) // 0 when unparsable: Foreground says unknown
		sh := ShellPane{ID: p.id, Command: f[0], PID: pid, InMode: f[1] == "1", Synchronized: f[2] == "1"}
		// A crashed row is armed as crashed, every field kept (CS-TMUX-078):
		// its --resurrected prints the hint and starts nothing.
		pend := row.Mark
		if pend.State != StateCrashed {
			pend.State = StatePending
		}
		if _, ok := bounded(o.Runner, dl.left(), "tmux", "set-option", "-p", "-t", p.id, Option, pend.JSON()); !ok {
			if late() {
				break
			}
			res.Skipped++
			o.logf("%s: tmux set-option failed; not re-armed", coords)
			continue
		}
		res.Marked++
		// CS-TMUX-067: retype only a row that was pending (or crashed,
		// CS-TMUX-078: its launcher had exited, so it sat at its shell) AND
		// whose pane sat at a bare shell when saved: resurrect typed nothing
		// into it, so the keys can neither double a typed restore nor land in
		// another program resurrect restarted.
		if (row.Mark.State != StatePending && row.Mark.State != StateCrashed) || !sp.FullCommandSaved || sp.FullCommand != "" {
			continue
		}
		// The typing guard --all uses (CS-TMUX-069): a pane resurrect just
		// created is rarely anything but its shell at a prompt, but a
		// default-command, a shell rc that starts a program, or a client
		// that attached meanwhile can make it so. The cheap checks first
		// (IdleShell with focus unread), focus read again last, right
		// before the keys. Every call gets what the deadline leaves; a
		// definite refusal is logged and the row handled, while one the
		// deadline may have caused (a call cut short) makes it late.
		if !shellAsked {
			if late() {
				break
			}
			shellAsked = true
			shell = DefaultShellWithin(o.Runner, dl.left())
		}
		// Past the deadline the row is late even if a cheap check would
		// refuse it: IdleShell may start the foreground check, and no call
		// starts past the deadline.
		t := dl.left()
		if t <= 0 {
			late()
			break
		}
		proc := o.Proc
		proc.Timeout = t
		why := IdleShell(proc, sh, shell)
		if why == "" {
			t = dl.left()
			if t <= 0 {
				late()
				break
			}
			focused, known := PaneFocused(o.Runner, t, p.id)
			switch {
			case !known:
				why = rearmFocusUnknown
			case focused:
				why = ReasonFocused
			}
		}
		if why != "" {
			indefinite := why == ReasonShellUnknown || why == ReasonForegroundUnknown || why == rearmFocusUnknown
			if indefinite && late() {
				break
			}
			o.logf("%s: marked only: %s; type claude-sandbox tmux restore in it", coords, why)
			continue
		}
		t = dl.left()
		if t <= 0 {
			late() // the keys were never sent
			break
		}
		if !TypeRestore(o.Runner, t, p.id) {
			o.logf("%s: tmux send-keys failed; the pane is marked, type claude-sandbox tmux restore in it", coords)
			if late() {
				break
			}
			continue
		}
		res.Retyped++
	}
	if res.Late == 0 {
		res.Consumed = consume(path, o)
	}
	return res
}

func consume(path string, o HookOptions) bool {
	if err := ConsumePin(path); err != nil {
		o.logf("consume %s: %v", filepath.Base(path), err)
		return false
	}
	return true
}

// rearmMoved says why a new pane does not match its row's saved place (08 § 2),
// "" when it does. The pinned state file's field 8 is where resurrect
// restored the pane: unescaped (resurrect's save writes `echo $dir | sed
// 's/ /\\ /'`, escaping a space; every "\ " is turned back), then compared
// with the pane's current path, symlinks resolved. A single space survives
// that round trip, so such a dir is compared like any other. A LOSSY dir —
// one holding a tab, a newline or a run of whitespace, which echo collapses —
// is not compared: coordinates and new-pane membership decide alone. The
// saved form shows no such run any more, so the pane's current path (where
// resurrect's cd landed) is what says so. An active row's saved dir must also
// be its project or cwdRoot: it was saved while the launcher ran from there,
// so anything else means the layout moved.
func rearmMoved(field8, current string, m Mark) string {
	dir := strings.ReplaceAll(field8, `\ `, " ")
	if dir == "" || lossyDir(dir) || lossyDir(current) {
		return ""
	}
	sd := resolvedDir(dir)
	if sd != resolvedDir(current) {
		return "the pane is not in its saved directory"
	}
	if m.State == StateActive && sd != resolvedDir(m.Project) && (m.CwdRoot == "" || sd != resolvedDir(m.CwdRoot)) {
		return "its saved directory is not the session's project"
	}
	return ""
}

// lossyDir reports whether resurrect's save would change p: a tab, newline or
// other non-space whitespace, a run of two or more whitespace characters, or
// whitespace at either end (echo drops it).
func lossyDir(p string) bool {
	prev := false
	for i, r := range p {
		ws := unicode.IsSpace(r)
		switch {
		case ws && r != ' ':
			return true
		case ws && (prev || i == 0):
			return true
		}
		prev = ws
	}
	return prev
}

// resolvedDir is p with symlinks resolved when that works, cleaned.
func resolvedDir(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// printable replaces control characters, for a log line.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, s)
}
