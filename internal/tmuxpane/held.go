package tmuxpane

// The pane marks of a container a restart stops (CS-TMUX-080; plan
// restart-session 00 § 4.5, 02 § L3): read before the decision (a mark can
// name the conversation when no registry record does), unset right before
// the stop so the old pane's launcher settles to unset rather than pending,
// and set back when the stop failed and the container still runs.

import (
	"strings"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
)

// HeldMark is one pane holding a container's mark: the pane id, the raw mark
// as tmux holds it, and the parsed mark.
type HeldMark struct {
	Pane string
	Raw  string
	Mark Mark
}

// heldFormat is the one list-panes the restart runs: the mark stays last, so
// a tab in its JSON cannot shift a field.
const heldFormat = "#{pane_id}\t#{" + Option + "}"

// MarksFor lists the panes of the running tmux server whose mark names the
// container — its 64-hex id when the mark records one, else its name — from
// one bounded "tmux list-panes -a". Nothing when tmux does not answer.
func MarksFor(r execx.Runner, containerID, name string) []HeldMark {
	out, ok := bounded(r, CallTimeout, "tmux", "list-panes", "-a", "-F", heldFormat)
	if !ok {
		return nil
	}
	var held []HeldMark
	for _, line := range strings.Split(out, "\n") {
		id, raw, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		if !ok || !paneID.MatchString(id) || raw == "" {
			continue
		}
		m, ok := ParseMark(raw)
		if !ok {
			continue
		}
		switch {
		case m.ContainerID != "" && containerID != "":
			ok = m.ContainerID == containerID
		default:
			ok = name != "" && m.Container == name
		}
		if ok {
			held = append(held, HeldMark{Pane: id, Raw: raw, Mark: m})
		}
	}
	return held
}

// UnsetMarks unsets each held pane's mark (one bounded set-option each).
func UnsetMarks(r execx.Runner, held []HeldMark) {
	for _, h := range held {
		Pane{Runner: r, ID: h.Pane}.Unset()
	}
}

// PutBackMarks sets each held mark back, but only on a pane whose mark is
// still unset (one bounded show-options first): a mark written since — a
// new launch in that pane, the old launcher's own settle — is left alone.
// The save hook's compare-before-write (CS-TMUX-070): it narrows the race,
// it cannot close it.
func PutBackMarks(r execx.Runner, held []HeldMark) {
	for _, h := range held {
		p := Pane{Runner: r, ID: h.Pane}
		if p.Read() == "" {
			p.Set(h.Raw)
		}
	}
}
