package tmuxpane

// Claude Code's peer registry, read by the save hook (CS-TMUX-033/034)
// through the shared hardened reader, internal/registry. Only the
// conversation id and the name are taken from a record. The hook's policy: a
// record that fails a check is a miss, never an error.

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/registry"
)

// ReadRegistry returns the valid records in dir. The error is for the
// directory itself (missing, a symlink, unreadable, more than
// registry.MaxEntries entries); bad records are skipped.
func ReadRegistry(dir string) ([]registry.Record, error) {
	res, err := registry.ReadAll(dir)
	if err != nil {
		return nil, err
	}
	var out []registry.Record
	for _, r := range res {
		if r.Err == nil {
			out = append(out, r.Record)
		}
	}
	return out, nil
}

// under reports whether p is dir or below it (both cleaned, absolute).
func under(p, dir string) bool {
	if p == "" || dir == "" || !filepath.IsAbs(p) || !filepath.IsAbs(dir) {
		return false
	}
	p, dir = filepath.Clean(p), filepath.Clean(dir)
	return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")
}

// sinceSlack is how far before a primary's "since" its record may start
// (the mark's since is taken on the host, startedAt in the container).
const sinceSlack = 5000 // ms

// PrimaryWindow and JoinWindow cap how long after the mark's "since" its
// claude may have started (CS-TMUX-033). A primary starts once the entrypoint
// has run (its first start can be slow); a join's claude right after its
// exec. A record that starts later belongs to a later join in the same
// container (same class, class + 256·n): when the pane's own record is
// missing or rejected, taking it would give the pane another session's
// conversation, so it is a miss instead.
const (
	PrimaryWindow = 120 * time.Second
	JoinWindow    = 30 * time.Second
)

// Match picks the record holding the conversation of an active mark
// (CS-TMUX-033): pid % 256 == the mark's class, a cwd under the project or
// cwdRoot, and a start inside the mark's window — since - 5 s to
// since + PrimaryWindow for a primary, since to since + JoinWindow for a
// join; with since 0 (unknown) there is no window. A candidate naming the
// mark's current conversation wins; else the earliest start. ok is false for
// a ralph mark and when nothing matches.
func Match(m Mark, recs []registry.Record) (registry.Record, bool) {
	class, err := strconv.Atoi(m.Class)
	if err != nil || class < 0 || class > 255 {
		return registry.Record{}, false
	}
	var min, max int64
	switch m.Mode {
	case ModeClaude:
		min, max = m.Since-sinceSlack, m.Since+PrimaryWindow.Milliseconds()
	case ModeJoin:
		min, max = m.Since, m.Since+JoinWindow.Milliseconds()
	default:
		return registry.Record{}, false
	}
	var best registry.Record
	found, current := false, false
	for _, r := range recs {
		if r.PID%256 != class {
			continue
		}
		// since 0 is unknown (an attach whose container creation time could
		// not be read): no window, class and cwd decide.
		if m.Since != 0 && (r.StartedAt < min || r.StartedAt > max) {
			continue
		}
		if !under(r.Cwd, m.Project) && !under(r.Cwd, m.CwdRoot) {
			continue
		}
		isCurrent := m.Conversation != "" && strings.EqualFold(r.SessionID, m.Conversation)
		switch {
		case !found, isCurrent && !current, isCurrent == current && r.StartedAt < best.StartedAt:
			best, found, current = r, true, isCurrent
		}
	}
	return best, found
}

// worktreeNameRE is a worktree name the hook fills in (CS-TMUX-036).
var worktreeNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Apply returns m updated from its matching record r (CS-TMUX-035/036): the
// conversation id, the name and its source, and — for a mark whose worktree
// name claude generated — the worktree read from the record's cwd.
func Apply(m Mark, r registry.Record) Mark {
	m.Conversation, m.Name, m.NameSource = r.SessionID, r.Name, r.NameSource
	if m.WorktreeGenerated && m.Worktree == "" && m.CwdRoot != "" {
		base := filepath.Join(filepath.Clean(m.CwdRoot), ".claude", "worktrees")
		if under(r.Cwd, base) && filepath.Clean(r.Cwd) != base {
			rel := strings.TrimPrefix(filepath.Clean(r.Cwd), base+"/")
			name, _, _ := strings.Cut(rel, "/")
			if worktreeNameRE.MatchString(name) && name != "." && name != ".." {
				m.Worktree = name
				m.CwdRoot = filepath.Join(base, name)
			}
		}
	}
	return m
}
