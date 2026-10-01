package tmuxpane

// "tmux restore --all [--from SAVE]" (CS-TMUX-069; plan sandbox-reboot-restore
// 10 § 7 as amended by 12 § 1; F4d): arm a chosen save into the panes of the
// running tmux server that already exist. After a bad restore the windows
// are usually back as shells; this gives each pane at a row's coordinates —
// when it is provably idle at a shell in its saved place — the row as a
// pending mark, and types the restore into it. Each armed pane then restores
// itself (CS-TMUX-052..062), one start at a time under the start lock.
//
// It is the only form that touches panes it did not create, so it is typed by
// the operator only, never run by a hook. It types --resurrected, never the
// plain restore, so an armed pane prints the sparse notice without claiming
// it (12 § 1); --resurrected reads the pane's own pending mark first, so no
// pin is consulted.

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
)

// armPaneFields are the per-pane fields --all lists and re-checks right
// before it marks a pane: everything that says whether the pane is idle, at
// a shell, in its place and unwatched. The mark stays last (JSON holds no
// literal tab): a path holding a tab shifts it, and the pane then reads as
// marked, which --all leaves alone.
const armPaneFields = "#{pane_current_command}\t#{pane_current_path}\t#{pane_in_mode}\t#{pane_synchronized}\t#{pane_dead}" +
	"\t#{pane_active}\t#{window_active}\t#{session_attached}\t#{" + Option + "}"

// armPanesFormat is --all's one list-panes: id, coordinates, the server, then
// armPaneFields.
const armPanesFormat = "#{pane_id}\t#{session_name}\t#{window_index}\t#{pane_index}\t#{pid}\t#{start_time}\t" + armPaneFields

// armFieldCount is the number of tab-separated fields of armPaneFields.
const armFieldCount = 9

// ArmPane is one pane of the running server as --all sees it.
type ArmPane struct {
	ID, Session  string
	Window, Pane int
	// Command and Path are #{pane_current_command} and #{pane_current_path}.
	Command, Path string
	// InMode: copy mode or another mode, where keys would not reach the
	// shell. Synchronized: synchronize-panes is on, so keys would reach
	// every pane of the window. Dead: its program exited (remain-on-exit).
	// Focused: the active pane of the active window of an attached session —
	// a client is looking at it.
	InMode, Synchronized, Dead, Focused bool
	// Raw is the mark as tmux holds it ("" for none); Mark and Marked its
	// parse.
	Raw    string
	Mark   Mark
	Marked bool
	// fields is the armPaneFields text as listed, for the re-check.
	fields string
}

// Coords is "s:w.p".
func (p ArmPane) Coords() string { return fmt.Sprintf("%s:%d.%d", p.Session, p.Window, p.Pane) }

// Info is the pane as the read-only probes see it (row 9's "on screen").
func (p ArmPane) Info() PaneInfo {
	return PaneInfo{ID: p.ID, Session: p.Session, Window: p.Window, Pane: p.Pane, CurrentCommand: p.Command,
		Mark: p.Mark, Marked: p.Marked}
}

// ListArmPanes reads every pane of the running server with one bounded
// list-panes, and the server; ok is false when tmux does not answer.
func ListArmPanes(r execx.Runner) ([]ArmPane, *Server, bool) {
	out, ok := bounded(r, CallTimeout, "tmux", "list-panes", "-a", "-F", armPanesFormat)
	if !ok {
		return nil, nil, false
	}
	var (
		ps  []ArmPane
		srv *Server
	)
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(strings.TrimRight(line, "\r"), "\t", 6+armFieldCount)
		if len(f) != 6+armFieldCount || !paneID.MatchString(f[0]) {
			continue
		}
		w, err1 := strconv.Atoi(f[2])
		pn, err2 := strconv.Atoi(f[3])
		if err1 != nil || err2 != nil {
			continue
		}
		if srv == nil {
			srv = parseServer(f[4], f[5])
		}
		p := ArmPane{ID: f[0], Session: f[1], Window: w, Pane: pn}
		p.setFields(f[6:])
		ps = append(ps, p)
	}
	return ps, srv, true
}

// setFields fills the armPaneFields part of p from its armFieldCount values.
func (p *ArmPane) setFields(f []string) {
	p.fields = strings.Join(f, "\t")
	p.Command, p.Path = f[0], f[1]
	p.InMode, p.Synchronized, p.Dead = f[2] == "1", f[3] == "1", f[4] == "1"
	p.Focused = f[5] == "1" && f[6] == "1" && f[7] != "" && f[7] != "0"
	p.Raw = f[8]
	p.Mark, p.Marked = ParseMark(f[8])
}

// DefaultShell is the basename of the server's default-shell, from one
// bounded "tmux show -gv default-shell"; "" when tmux does not answer.
func DefaultShell(r execx.Runner) string {
	out, ok := bounded(r, CallTimeout, "tmux", "show", "-gv", "default-shell")
	sh := strings.TrimSpace(out)
	if !ok || sh == "" {
		return ""
	}
	return filepath.Base(sh)
}

// ReadStatePanes reads a save's state file in the resurrect dir with
// CS-TMUX-046's checks and returns its pane lines.
func ReadStatePanes(dir, stamp string) ([]StatePane, error) {
	b, err := readOwned(filepath.Join(dir, StateFileName(stamp)), maxStateFile)
	if err != nil {
		return nil, err
	}
	return ParseStateFile(b), nil
}

// ArmVerdict is what --all did with one row.
type ArmVerdict string

const (
	// ArmTyped: marked pending and the restore typed into it.
	ArmTyped ArmVerdict = "typed"
	// ArmMarked: marked pending only; the operator types the restore.
	ArmMarked ArmVerdict = "marked"
	// ArmMissing: no pane at the coordinates, or not in its saved place.
	ArmMissing ArmVerdict = "missing"
	// ArmBusy: the pane runs something, is in use, or changed since the list.
	ArmBusy ArmVerdict = "busy"
	// ArmOnScreen: the session already runs in another pane.
	ArmOnScreen ArmVerdict = "on screen"
	// ArmFinal: the decision table's answer is final; nothing to arm.
	ArmFinal ArmVerdict = "final"
	// ArmUnusable: the row fails CS-TMUX-046's checks.
	ArmUnusable ArmVerdict = "unusable"
	// ArmFailed: a tmux call failed.
	ArmFailed ArmVerdict = "failed"
)

// ArmRow is --all's answer for one row of the save.
type ArmRow struct {
	// Coords is the row's "s:w.p" ("(an unprintable session name)" when its
	// session name holds a control character).
	Coords  string
	Row     *Row
	Verdict ArmVerdict
	// Why says why, for every verdict but ArmTyped.
	Why string
	// Decision is the decision table's answer (zero before step 5).
	Decision Decision
}

// ArmOptions are Arm's inputs: the save's rows and state-file lines, the
// server's panes, the default shell's basename, the pane the command runs in
// ("" outside tmux) and the read-only probes for the decision table.
type ArmOptions struct {
	Runner execx.Runner
	Rows   []Row
	State  []StatePane
	// StateErr is why the state file could not be read; every row is then
	// skipped (no saved place can be proved).
	StateErr error
	Panes    []ArmPane
	Shell    string
	Self     string
	Probes   Probes
}

// Arm arms the save's rows into the existing panes (CS-TMUX-069).
func Arm(o ArmOptions) []ArmRow {
	byCoord := map[string]ArmPane{}
	for _, p := range o.Panes {
		byCoord[coordKey(p.Session, p.Window, p.Pane)] = p
	}
	saved := map[string]StatePane{}
	for _, sp := range o.State {
		saved[sp.Key()] = sp
	}
	out := make([]ArmRow, 0, len(o.Rows))
	for i := range o.Rows {
		out = append(out, armRow(o, &o.Rows[i], byCoord, saved))
	}
	return out
}

func armRow(o ArmOptions, row *Row, byCoord map[string]ArmPane, saved map[string]StatePane) ArmRow {
	coords := fmt.Sprintf("%s:%d.%d", row.Session, row.Window, row.Pane)
	if strings.ContainsFunc(row.Session, func(c rune) bool { return c < 0x20 || c == 0x7f }) {
		coords = "(an unprintable session name)"
	}
	res := ArmRow{Coords: coords, Row: row}
	skip := func(v ArmVerdict, why string) ArmRow {
		res.Verdict, res.Why = v, why
		return res
	}
	m := row.Mark
	// 1. The row must be usable before anything of it is printed or used.
	if field := ValidateRow(m); field != "" {
		return skip(ArmUnusable, "its "+field+" cannot be used")
	}
	// 2. A pane at the coordinates, in its saved place.
	key := coordKey(row.Session, row.Window, row.Pane)
	p, ok := byCoord[key]
	if !ok {
		return skip(ArmMissing, "no pane at "+coords)
	}
	if o.StateErr != nil {
		return skip(ArmMissing, "the save's state file cannot be read ("+o.StateErr.Error()+"), so the pane's place cannot be checked")
	}
	sp, ok := saved[key]
	if !ok {
		return skip(ArmMissing, "the save's state file has no line for "+coords)
	}
	compared, moved := armPlace(sp.Dir, p.Path, m)
	if moved != "" {
		return skip(ArmMissing, moved)
	}
	// 3. Idle: nothing runs in it that is not a shell's business, and it
	// holds no other session.
	switch {
	case o.Self != "" && p.ID == o.Self:
		return skip(ArmBusy, "it is the pane this command runs in")
	case p.Dead:
		return skip(ArmBusy, "its program has exited (remain-on-exit)")
	case p.Command == LauncherCommand:
		return skip(ArmBusy, "it runs claude-sandbox")
	case p.Raw != "" && !p.Marked:
		return skip(ArmBusy, "it holds a claude-sandbox mark that does not parse")
	case p.Marked && p.Mark.State != StatePending:
		return skip(ArmBusy, "it holds a running session's mark")
	case p.Marked && !sameSession(p.Mark, m):
		return skip(ArmBusy, "it holds a pending mark for another session (forget it with claude-sandbox tmux restore --drop in it, or type claude-sandbox tmux restore in it)")
	}
	// 4. Not already on screen elsewhere.
	if where, ok := armOnScreen(o.Panes, p.ID, m); ok {
		return skip(ArmOnScreen, "already on screen in "+where)
	}
	// 5. The decision table, read-only: a final answer arms nothing.
	res.Decision = Decide(row, coords, o.Probes)
	if res.Decision.Outcome == OutcomeClear || res.Decision.Outcome == OutcomeNone {
		return skip(ArmFinal, res.Decision.Line)
	}
	// 6. Right before the mark, the pane must be exactly as listed.
	cur, ok := bounded(o.Runner, CallTimeout, "tmux", "display-message", "-p", "-t", p.ID, armPaneFields)
	if !ok {
		return skip(ArmFailed, "tmux did not answer for "+p.ID)
	}
	if strings.TrimRight(cur, "\r\n") != p.fields {
		return skip(ArmBusy, "the pane changed since it was listed")
	}
	// 7. The row, every field kept (labelled included: CS-TMUX-022's reclaim
	// is the row's, CS-TMUX-069), as the pane's pending mark.
	pend := m
	pend.State = StatePending
	if _, ok := bounded(o.Runner, CallTimeout, "tmux", "set-option", "-p", "-t", p.ID, Option, pend.JSON()); !ok {
		return skip(ArmFailed, "tmux set-option failed for "+p.ID)
	}
	if why := armNoType(p, o.Shell, compared); why != "" {
		return skip(ArmMarked, why)
	}
	if _, ok := bounded(o.Runner, CallTimeout, "tmux", "send-keys", "-t", p.ID, RetypeKeys, "C-m"); !ok {
		return skip(ArmMarked, "tmux send-keys failed")
	}
	res.Verdict = ArmTyped
	return res
}

// armPlace compares a pane's current path with its saved dir (CS-TMUX-066's
// comparison, rearmMoved): moved says why it is not in its place. compared is
// false when the place could not be proved — no saved dir, or a lossy one
// resurrect's save does not keep — and the pane is then never typed into.
func armPlace(field8, current string, m Mark) (compared bool, moved string) {
	dir := strings.ReplaceAll(field8, `\ `, " ")
	if moved := rearmMoved(field8, current, m); moved != "" {
		return false, moved
	}
	return dir != "" && !lossyDir(dir) && !lossyDir(current), ""
}

// armNoType says why an armed pane is marked only, "" when the restore may
// be typed into it: it must be provably idle at a shell, in its saved place,
// with no client looking at it.
func armNoType(p ArmPane, shell string, compared bool) string {
	switch {
	case shell == "":
		return "the default shell is unknown (tmux show -gv default-shell did not answer)"
	case p.Command != shell:
		return "it runs " + printable(p.Command) + ", not the shell"
	case p.InMode:
		return "it is in copy mode"
	case p.Synchronized:
		return "its window has synchronize-panes on"
	case p.Focused:
		return "a client is looking at it"
	case !compared:
		return "its saved directory cannot be compared with where it is"
	}
	return ""
}

// sameSession reports whether two marks name one session: the 64-hex
// container id when both have one, else the conversation when both have
// one, else the container name.
func sameSession(a, b Mark) bool {
	switch {
	case hex64RE.MatchString(a.ContainerID) && hex64RE.MatchString(b.ContainerID):
		return a.ContainerID == b.ContainerID
	case a.Conversation != "" && b.Conversation != "":
		return strings.EqualFold(a.Conversation, b.Conversation)
	}
	return a.Container != "" && a.Container == b.Container
}

// armOnScreen names another pane running the launcher whose active mark
// holds the row's container (by its 64-hex id) or conversation.
func armOnScreen(panes []ArmPane, self string, m Mark) (string, bool) {
	for _, p := range panes {
		if p.ID == self || !p.Marked || p.Mark.State != StateActive || p.Command != LauncherCommand {
			continue
		}
		if hex64RE.MatchString(m.ContainerID) && p.Mark.ContainerID == m.ContainerID ||
			m.Conversation != "" && strings.EqualFold(p.Mark.Conversation, m.Conversation) {
			return p.Coords(), true
		}
	}
	return "", false
}
