package tmuxpane

import (
	"strings"
	"unicode"

	"github.com/kmacmcfarlane/claude-sandbox/internal/registry"
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
// Every word is shell-quoted. "" when the mark names no conversation, or a
// worktree whose generated name is not recorded yet.
func ResumeCommand(m Mark) string {
	if m.Conversation == "" || (m.WorktreeGenerated && m.Worktree == "") {
		// No id, or a worktree whose name is not known yet: any worktree
		// flag would resume in the wrong place (CS-TMUX-012).
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
		w = append(w, "--name", shq(registry.Printable(m.Name)))
	}
	return strings.Join(w, " ")
}

// PendingNote is the one line a launch prints when it replaces a pending mark
// that names another conversation (CS-TMUX-017); "" when there is nothing to
// say. resuming is the conversation the new launch resumes explicitly.
func PendingNote(prior string, resuming string) string {
	m, ok := ParseMark(prior)
	if !ok || m.State != StatePending || !registry.IsUUID(m.Conversation) || strings.EqualFold(m.Conversation, resuming) {
		return ""
	}
	if !printableMark(m) {
		// Shell quoting does not stop an escape sequence from reaching the
		// terminal: print no value from the mark but the validated id.
		return "Note: this pane was waiting to restore a conversation (" + m.Conversation +
			"), but its mark holds unprintable values; no resume command is shown"
	}
	name := m.Name
	if name == "" {
		name = m.Instance
	}
	if m.WorktreeGenerated && m.Worktree == "" {
		// "--no-worktree" would resume in the shared checkout, where the
		// transcript is not (CS-TMUX-012).
		return "Note: this pane was waiting to restore '" + name + "' (" + m.Conversation +
			") in a worktree whose name is not recorded yet; no resume command is shown"
	}
	return "Note: this pane was waiting to restore '" + name + "' (" + m.Conversation +
		"); resume it with: " + ResumeCommand(m)
}

// printableMark reports whether every mark value the note prints is free of
// control characters (CS-TMUX-017).
func printableMark(m Mark) bool {
	vals := []string{m.Project, m.Worktree, m.Model, m.Name, m.Instance}
	if m.ConfigDirEnv != nil {
		vals = append(vals, *m.ConfigDirEnv)
	}
	vals = append(vals, m.Replay...)
	for _, v := range vals {
		if strings.IndexFunc(v, unicode.IsControl) >= 0 {
			return false
		}
	}
	return true
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
