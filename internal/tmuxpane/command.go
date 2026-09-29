package tmuxpane

import (
	"strings"
	"unicode"
)

// NameSourceUser is the registry name source of a name the user gave
// (/rename, --name). Only such a name is replayed with --name (plan 07 § 5,
// 09 finding 2): a derived name must not become a user-given one.
const NameSourceUser = "user"

// ResumeCommand is the exact manual command that resumes a mark's
// conversation in a new container (plan 06 § 3 with 09 finding 1 and
// decision 49):
//
//	cd <project> && [CLAUDE_CONFIG_DIR=<v> ]claude-sandbox --new
//	  --worktree=<w>|--no-worktree [--model <m>] -- [<replay>…] --resume <id> [--name <n>]
//
// Every word is shell-quoted. "" when the mark names no conversation.
func ResumeCommand(m Mark) string {
	if m.Conversation == "" {
		return ""
	}
	var w []string
	if m.Project != "" {
		w = append(w, "cd", shq(m.Project), "&&")
	}
	if m.ConfigDirEnv != nil && *m.ConfigDirEnv != "" {
		w = append(w, "CLAUDE_CONFIG_DIR="+shq(*m.ConfigDirEnv))
	}
	w = append(w, "claude-sandbox", "--new")
	if m.Worktree != "" {
		w = append(w, shq("--worktree="+m.Worktree))
	} else {
		// Explicit both ways: a cascade or env that turned worktree mode on
		// since would otherwise resume in a new worktree, where the
		// transcript is not (plan 09 finding 1).
		w = append(w, "--no-worktree")
	}
	if m.Model != "" {
		w = append(w, "--model", shq(m.Model))
	}
	w = append(w, "--")
	for _, t := range m.Replay {
		w = append(w, shq(t))
	}
	w = append(w, "--resume", shq(m.Conversation))
	if m.Name != "" && m.NameSource == NameSourceUser {
		w = append(w, "--name", shq(Printable(m.Name)))
	}
	return strings.Join(w, " ")
}

// PendingNote is the one line a launch prints when it replaces a pending mark
// that names another conversation (CS-TMUX-017); "" when there is nothing to
// say. resuming is the conversation the new launch resumes explicitly.
func PendingNote(prior string, resuming string) string {
	m, ok := ParseMark(prior)
	if !ok || m.State != StatePending || !uuidRE.MatchString(m.Conversation) || strings.EqualFold(m.Conversation, resuming) {
		return ""
	}
	name := Printable(m.Name)
	if name == "" {
		name = Printable(m.Instance)
	}
	return "Note: this pane was waiting to restore '" + name + "' (" + m.Conversation +
		"); resume it with: " + ResumeCommand(m)
}

// Printable drops control characters and collapses whitespace, for text read
// from a mark and shown to the operator (the notify-webhook precedent).
func Printable(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// shq quotes s for a POSIX shell when it needs it.
func shq(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+=:,@%", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
