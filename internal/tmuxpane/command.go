package tmuxpane

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/kmacmcfarlane/claude-sandbox/internal/registry"
)

// NameSourceUser is the registry name source of a name the user gave
// (/rename, --name). Only such a name is replayed with --name (plan 07 § 5,
// 09 finding 2): a derived name must not become a user-given one.
const NameSourceUser = "user"

// ResumeCommand is the exact manual command that resumes a mark's
// conversation in a new container (plan 06 § 3 with 09 finding 1, decision
// 49 and 10 § 4.4):
//
//	cd <project> && [CLAUDE_CONFIG_DIR=<v> ]claude-sandbox --new
//	  --worktree=<w>|--no-worktree [--model <m>] -- --resume <id> [--name <n>] [<replay>…]
//
// Every word is shell-quoted. "" when the mark names no conversation, or a
// worktree whose generated name is not recorded yet.
func ResumeCommand(m Mark) string {
	args := ResumeArgs(m)
	if args == nil {
		return ""
	}
	var w []string
	if m.Project != "" {
		w = append(w, "cd", shq(m.Project), "&&")
	}
	if m.ConfigDirEnv != nil && *m.ConfigDirEnv != "" {
		w = append(w, "CLAUDE_CONFIG_DIR="+shq(*m.ConfigDirEnv))
	}
	w = append(w, "claude-sandbox")
	for _, a := range args {
		w = append(w, shq(a))
	}
	return strings.Join(w, " ")
}

// ResumeArgs are the launcher arguments a restore resumes a mark's
// conversation with (CS-TMUX-058; 10 § 4.4), unquoted:
//
//	--new --worktree=<w>|--no-worktree [--model <m>] -- --resume <id> [--name <n>] [<replay>…]
//
// --resume comes first after "--", so no replayed token can hide the id from
// GuardedResumeID's scan and the resume label is always set. nil when the
// mark names no conversation, or a worktree whose generated name is not
// recorded yet.
func ResumeArgs(m Mark) []string {
	if m.Conversation == "" || (m.WorktreeGenerated && m.Worktree == "") {
		// No id, or a worktree whose name is not known yet: any worktree
		// flag would resume in the wrong place (CS-TMUX-012).
		return nil
	}
	w := []string{"--new"}
	if m.Worktree != "" {
		w = append(w, "--worktree="+m.Worktree)
	} else {
		// Explicit both ways: a cascade or env that turned worktree mode on
		// since would otherwise resume in a new worktree, where the
		// transcript is not (plan 09 finding 1).
		w = append(w, "--no-worktree")
	}
	if m.Model != "" {
		w = append(w, "--model", m.Model)
	}
	w = append(w, "--", "--resume", m.Conversation)
	if m.Name != "" && m.NameSource == NameSourceUser {
		w = append(w, "--name", registry.Printable(m.Name))
	}
	return append(w, m.Replay...)
}

// PendingNote is the one line a launch prints when it replaces a pending or
// crashed mark that names another conversation (CS-TMUX-017); "" when there
// is nothing to say. resuming is the conversation the new launch resumes
// explicitly.
func PendingNote(prior string, resuming string) string {
	m, ok := ParseMark(prior)
	if !ok || (m.State != StatePending && m.State != StateCrashed) || !registry.IsUUID(m.Conversation) || strings.EqualFold(m.Conversation, resuming) {
		return ""
	}
	crashed := m.State == StateCrashed
	if !printableMark(m) {
		// Shell quoting does not stop an escape sequence from reaching the
		// terminal: print no value from the mark but the validated id.
		if crashed {
			return "Note: a session that crashed in this pane (" + m.Conversation +
				") left a mark that holds unprintable values; no resume command is shown"
		}
		return "Note: this pane was waiting to restore a conversation (" + m.Conversation +
			"), but its mark holds unprintable values; no resume command is shown"
	}
	name := m.Name
	if name == "" {
		name = m.Instance
	}
	what := "this pane was waiting to restore '" + name + "' (" + m.Conversation + ")"
	if crashed {
		what = fmt.Sprintf("'%s' (%s) crashed in this pane (exit %d)", registry.Printable(RowName(m)), m.Conversation, m.ExitCode)
	}
	if m.WorktreeGenerated && m.Worktree == "" {
		// "--no-worktree" would resume in the shared checkout, where the
		// transcript is not (CS-TMUX-012).
		return "Note: " + what + " in a worktree whose name is not recorded yet; no resume command is shown"
	}
	return "Note: " + what + "; resume it with: " + ResumeCommand(m)
}

// CrashHint is decision row 19's one line for a crashed row (CS-TMUX-077):
//
//	'<name>' crashed in this pane on <date> (exit N[, killed by the OOM killer]);
//	not restarted. Resume it with: <command>[; it was also launched with <names>]
//
// m must have passed ValidateRow: every value printed is free of control and
// bidi characters, and the name is cleaned for display.
func CrashHint(m Mark) string {
	var b strings.Builder
	b.WriteString("'" + registry.Printable(RowName(m)) + "' crashed in this pane")
	if m.EndedAt > 0 {
		b.WriteString(" on " + time.UnixMilli(m.EndedAt).Local().Format("2006-01-02 15:04"))
	}
	fmt.Fprintf(&b, " (exit %d", m.ExitCode)
	if m.OOMKilled {
		// Cause-neutral, as CS-LNCH-089/090: the container's own limit or
		// the host running out of memory.
		b.WriteString(", killed by the OOM killer")
	}
	b.WriteString("); not restarted.")
	if cmd := ResumeCommand(m); cmd != "" {
		b.WriteString(" Resume it with: " + cmd)
	} else {
		// A worktree whose generated name was never recorded: any worktree
		// flag would resume in the wrong place (CS-TMUX-012).
		b.WriteString(" Resume it by hand from its worktree: claude-sandbox --new -- --resume " + m.Conversation)
	}
	if len(m.Unreplayed) > 0 {
		b.WriteString("; it was also launched with " + strings.Join(m.Unreplayed, ", "))
	}
	return b.String()
}

// CrashedPaneLine is what a restore prints about a pane's own crashed mark
// when it will not act on it (CS-TMUX-069 step 2a, the --from refusal of
// CS-TMUX-077): the hint followed by then, when the mark passes
// ValidateRow; else a line naming only the coordinates and the field, so no
// unchecked value is printed.
func CrashedPaneLine(m Mark, coords, then string) string {
	if field := ValidateRow(m); field != "" {
		return "pane " + coords + " holds a crashed mark that cannot be used (its " + field +
			" is not valid); forget it with claude-sandbox tmux restore --drop in that pane"
	}
	return CrashHint(m) + then
}

// printableMark reports whether every mark value the note prints is free of
// control and bidi characters (CS-TMUX-017).
func printableMark(m Mark) bool {
	vals := []string{m.Project, m.Worktree, m.Model, m.Name, m.Instance}
	if m.ConfigDirEnv != nil {
		vals = append(vals, *m.ConfigDirEnv)
	}
	vals = append(vals, m.Replay...)
	for _, v := range vals {
		if strings.IndexFunc(v, unprintable) >= 0 {
			return false
		}
	}
	return true
}

// unprintable is the rune predicate every printed mark value is checked with
// (CS-TMUX-017, CS-TMUX-046; plan 18 section 1): a control character (Cc), or
// one of the twelve Bidi_Control characters, which reorder what is displayed
// so a printed command could read differently from what it runs. Other
// format, private-use and surrogate characters (a ZWNJ or ZWJ in a folder
// name, a soft hyphen, a Nerd Font glyph) cannot, and pass.
func unprintable(r rune) bool {
	return unicode.IsControl(r) || isBidiControl(r)
}

// isBidiControl is Unicode's Bidi_Control property (Go's unicode package has
// no table for it): ALM; LRM, RLM; LRE, RLE, PDF, LRO, RLO; LRI, RLI, FSI, PDI.
func isBidiControl(r rune) bool {
	switch {
	case r == 0x061C, r == 0x200E, r == 0x200F:
		return true
	case r >= 0x202A && r <= 0x202E:
		return true
	case r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

// ShellQuote quotes s for a POSIX shell when it needs it.
func ShellQuote(s string) string { return shq(s) }

// shq quotes s for a POSIX shell when it needs it.
func shq(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+=:,@%", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
