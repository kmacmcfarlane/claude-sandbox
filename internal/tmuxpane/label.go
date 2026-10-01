package tmuxpane

// Window labels (CS-TMUX-020..026, CS-TMUX-041..044; plan
// sandbox-reboot-restore 14, operator answer 52 a = B2e). A launch in a tmux
// pane names its window by rule 42 — the conversation's user-given name, else
// the project folder's base name — with "rename-window", which turns tmux's
// automatic-rename off for that window, so tmux-resurrect saves and restores
// the name verbatim and no tmux.conf line is needed. Two window user options
// record what claude-sandbox did: the label it gave and the pane that owns
// it. A /rename the save hook resolves follows only while the window still
// carries that label (a hand rename wins forever), and the session's end hands
// the window back to automatic-rename. Every call is argv only, bounded and
// silent, like the pane mark's.

import (
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kmacmcfarlane/claude-sandbox/internal/registry"
)

const (
	// LabelOption is the window user option holding the label claude-sandbox
	// gave the window.
	LabelOption = "@claude-sandbox-label"
	// LabelPaneOption is the window user option naming the pane ("%N") that
	// owns the label (CS-TMUX-023).
	LabelPaneOption = "@claude-sandbox-label-pane"
	// LabelMax caps a label, in characters (CS-TMUX-021): the stopgap
	// restore-tmux.py's width.
	LabelMax = 40
)

// windowFormat is the one read of a window (CS-TMUX-020): the user option
// last, since it is the one field tmux does not escape and SplitN keeps it
// whole (tmux vis-escapes tabs in window names).
const windowFormat = "#{window_id}\t#{automatic-rename}\t#{" + LabelPaneOption + "}\t#{window_name}\t#{" + LabelOption + "}"

// Label cleans s into a window label (CS-TMUX-021): Printable
// (control characters to spaces, format characters dropped, whitespace
// collapsed); "#", "\" and ";" removed; leading "-" and spaces trimmed, so it
// never reads as a flag; at most LabelMax characters. "" means no label.
// Label(Label(s)) == Label(s): ownership is a string comparison.
//
// Removing "#" is the SANDBOX-TO-HOST COMMAND-EXECUTION BARRIER, not
// cosmetics: rename-window format-expands its argument with jobs enabled
// (tmux cmd-rename-window.c → format_single_from_target, no FORMAT_NOJOBS),
// so a "#(cmd)" in a name would run cmd as a host shell command — and a
// /rename's name comes from a registry record code inside a sandbox writes.
// No "#" means no format at all ("#{…}", "#(…)", "#[…]"). "\" goes because
// resurrect's restore reads its state file with read without -r; ";"
// because a trailing one separates tmux commands, even in argv.
func Label(s string) string {
	s = registry.Printable(s)
	s = strings.Map(func(r rune) rune {
		if r == '#' || r == '\\' || r == ';' {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimLeft(s, "- ")
	if utf8.RuneCountInString(s) > LabelMax {
		s = string([]rune(s)[:LabelMax])
	}
	return strings.TrimRight(s, " ")
}

// LaunchLabel is rule 42 (CS-TMUX-021): the user-given name, else the
// project folder's base name, cleaned.
func LaunchLabel(userName, project string) string {
	if l := Label(userName); l != "" {
		return l
	}
	if project == "" {
		return ""
	}
	base := filepath.Base(filepath.Clean(project))
	if base == "/" || base == "." {
		return ""
	}
	return Label(base)
}

// NameArg is the conversation name a passthrough gives claude — the last
// "--name <v>", "--name=<v>", "-n <v>" or "-n<v>" before ResumeID's stop —
// "" when none (CS-TMUX-021).
func NameArg(args []string) string {
	last := ""
	for i := 0; i < len(args); {
		a := args[i]
		if a == "--" || !strings.HasPrefix(a, "-") || a == "-" {
			break
		}
		name, hasValue := flagName(a)
		if name == "--name" || name == "-n" {
			switch {
			case hasValue && name == "--name":
				_, last, _ = strings.Cut(a, "=")
				i++
			case hasValue:
				last = a[2:]
				i++
			case i+1 < len(args):
				last = args[i+1]
				i += 2
			default:
				i++
			}
			continue
		}
		next, stop := step(args, i)
		if stop {
			break
		}
		i = next
	}
	return last
}

// Window is one read of a pane's window.
type Window struct {
	ID string
	// Auto is the effective automatic-rename ("1"/"on").
	Auto bool
	// Label and LabelPane are claude-sandbox's window user options, "" when
	// unset.
	Label     string
	LabelPane string
	Name      string
}

// ours reports whether the window still carries the label claude-sandbox
// gave it: automatic-rename off and the name equal to the label option.
func (w Window) ours() bool {
	return !w.Auto && w.Label != "" && w.Name == w.Label
}

// readWindow reads pane's window in one bounded call; ok is false when tmux
// failed or the output does not parse (CS-TMUX-025).
func readWindow(p Pane, timeout time.Duration) (Window, bool) {
	out, ok := bounded(p.Runner, timeout, "tmux", "display-message", "-p", "-t", p.ID, windowFormat)
	if !ok {
		return Window{}, false
	}
	f := strings.SplitN(strings.TrimRight(out, "\r\n"), "\t", 5)
	if len(f) != 5 || !strings.HasPrefix(f[0], "@") {
		return Window{}, false
	}
	return Window{ID: f[0], Auto: f[1] == "1" || f[1] == "on", LabelPane: f[2], Name: f[3], Label: f[4]}, true
}

// paneInWindow reports whether pane id is one of the panes of p's window
// (CS-TMUX-022 case B). A failed listing counts as present: never fight.
func paneInWindow(p Pane, id string) bool {
	out, ok := p.run("list-panes", "-t", p.ID, "-F", "#{pane_id}")
	if !ok {
		return true
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) == id {
			return true
		}
	}
	return false
}

// setLabel writes the options first, then the name (CS-TMUX-020): a failed
// rename leaves a label that does not equal the name, which reads as a hand
// name and is never touched; the reverse order could freeze a name nobody
// owns. rename is false when the window already has the name.
func setLabel(p Pane, label string, rename bool) bool {
	if _, ok := p.run("set-option", "-w", "-t", p.ID, LabelOption, label); !ok {
		return false
	}
	if _, ok := p.run("set-option", "-w", "-t", p.ID, LabelPaneOption, p.ID); !ok {
		return false
	}
	if rename {
		if _, ok := p.run("rename-window", "-t", p.ID, "--", label); !ok {
			return false
		}
	}
	return true
}

// confirm re-reads the window after this launch wrote it (CS-TMUX-023): two
// launches in one automatic window can interleave so that another pane's
// options won while this pane's rename came last. The loser then puts the
// winner's label back as the name, so the window stays owned and consistent,
// and reports that it does not own it. A failed read keeps what was written.
func confirm(p Pane, label string) bool {
	w, ok := readWindow(p, CallTimeout)
	if !ok || w.LabelPane == p.ID {
		return true
	}
	if !w.Auto && w.Label != "" && w.Name == label && w.Name != w.Label {
		p.run("rename-window", "-t", p.ID, "--", w.Label)
	}
	return false
}

// BeginLabel names the pane's window at a launch (CS-TMUX-020..023) and
// reports whether this pane owns the label afterwards — only an owner hands
// the window back at the end (CS-TMUX-024). reclaim is true only for a
// restore whose row records that its launch owned the label (Mark.Labelled):
// only then is a non-automatic window without options, whose name equals
// label, taken as one claude-sandbox named before resurrect restored it
// (CS-TMUX-022 case C). A hand launch never reclaims, since a window the
// operator named by hand with the same text (often the folder's name) looks
// exactly alike.
func (p Pane) BeginLabel(label string, reclaim bool) bool {
	if label == "" {
		return false
	}
	w, ok := readWindow(p, CallTimeout)
	if !ok {
		return false
	}
	switch {
	case w.Auto:
		// Case A: tmux names it; claude-sandbox does now. The rename runs
		// even over an equal name: it is what turns automatic-rename off.
		return setLabel(p, label, true) && confirm(p, label)
	case w.ours():
		// Cases B/B′: claude-sandbox's label. One pane owns it; another
		// takes over only when the owner is this pane or gone.
		if w.LabelPane != p.ID && w.LabelPane != "" && paneInWindow(p, w.LabelPane) {
			return false
		}
		return setLabel(p, label, w.Name != label) && confirm(p, label)
	case reclaim && w.Label == "" && w.Name == label:
		// Case C: a window resurrect restored (it saves no user options)
		// under the name this restored session's launch gave it.
		return setLabel(p, label, false) && confirm(p, label)
	}
	// Case D: a name the operator gave.
	return false
}

// EndLabel hands the window back when the session ended and this pane owns
// the label (CS-TMUX-024): automatic-rename is unset at the window level (the
// global applies again — resurrect's own ":" model) while the name is still
// the label — or alt, the label of the conversation's user-given name, which
// a refresh cut between its rename and its option write leaves as the name
// (CS-TMUX-043) — then both options are removed. A hand rename since keeps
// its name; only the options go.
func (p Pane) EndLabel(alt string) {
	w, ok := readWindow(p, CallTimeout)
	if !ok || w.LabelPane != p.ID {
		return
	}
	if w.ours() || (!w.Auto && alt != "" && w.Name == alt) {
		if _, ok := p.run("set-option", "-w", "-u", "-t", p.ID, "automatic-rename"); !ok {
			return
		}
	}
	p.run("set-option", "-w", "-u", "-t", p.ID, LabelOption)
	p.run("set-option", "-w", "-u", "-t", p.ID, LabelPaneOption)
}

// RefreshLabel is the save hook's step for one pane with an active mark
// (CS-TMUX-041..044): when the mark carries a user-given name, the pane owns
// its window's label and the window still carries it, the window is renamed
// to the name. It reports whether it renamed, and false with a reason when it
// tried and failed; every call is bounded by timeout().
//
// The rename runs BEFORE the option write (CS-TMUX-043): a deadline between
// them leaves the name = the wanted label with the old option, which the next
// refresh repairs (a window owned by this pane whose name already equals the
// conversation's label is still ours: only the option is written) and the
// end hands back (EndLabel's alt). The reverse order would leave the option
// ahead of the name, which reads as a hand rename forever.
func RefreshLabel(p Pane, m Mark, timeout func() time.Duration) (renamed bool, problem string) {
	if m.State != StateActive || m.NameSource != NameSourceUser {
		return false, ""
	}
	want := Label(m.Name)
	if want == "" {
		return false, ""
	}
	t := timeout()
	if t <= 0 {
		return false, "deadline: window label not refreshed for " + p.ID
	}
	w, ok := readWindow(p, t)
	if !ok {
		return false, "tmux display-message for " + p.ID + " failed; window label not refreshed"
	}
	if w.LabelPane != p.ID || w.Auto || w.Label == "" || w.Label == want {
		return false, ""
	}
	switch w.Name {
	case w.Label:
		if _, ok := bounded(p.Runner, timeout(), "tmux", "rename-window", "-t", p.ID, "--", want); !ok {
			return false, "renaming the window of " + p.ID + " failed"
		}
		renamed = true
	case want:
		// A refresh cut between its rename and its option write: repair.
	default:
		return false, "" // renamed by hand (CS-TMUX-042)
	}
	if _, ok := bounded(p.Runner, timeout(), "tmux", "set-option", "-w", "-t", p.ID, LabelOption, want); !ok {
		return renamed, "the window label option of " + p.ID + " was not written; the next save repairs it"
	}
	return renamed, ""
}
