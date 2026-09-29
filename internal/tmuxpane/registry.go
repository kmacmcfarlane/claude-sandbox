package tmuxpane

// Claude Code's peer registry, read by the save hook (CS-TMUX-033/034).
// Records are <registry dir>/<pid>.json, written by claude — inside a sandbox
// for every sandbox session — so the directory is sandbox-writable (answer
// 31b). Only the conversation id and the name are taken from a record, and
// every read is defensive: one O_NOFOLLOW directory fd, each record opened
// relative to it with O_NOFOLLOW|O_NONBLOCK (a FIFO never blocks), a regular
// file of at most 64 KiB, the file name's pid equal to the JSON pid, and a
// canonical UUID. A record that fails a check is skipped, never an error.

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// maxRecord is the largest record read (06 § 6).
const maxRecord = 64 << 10

// maxNameLen is the longest name kept, in characters (06 § 6).
const maxNameLen = 200

// NameSources is the registry's name source set in Claude Code 2.1.284 (plan
// 09 finding 2): the registry reader there whitelists exactly these. Only
// NameSourceUser is a name the user gave (/rename, --name).
var NameSources = map[string]bool{
	"user": true, "peer": true, "derived": true, "collision": true, "auto": true, "hook": true,
}

// recordName is a registry record's file name.
var recordName = regexp.MustCompile(`^([0-9]{1,10})\.json$`)

// RegistryRecord is what the save hook takes from one registry record.
type RegistryRecord struct {
	PID        int
	SessionID  string
	Cwd        string
	StartedAt  int64 // unix ms
	Name       string
	NameSource string
}

// rawRecord is the part of Claude Code's record the hook reads.
type rawRecord struct {
	PID        int    `json:"pid"`
	SessionID  string `json:"sessionId"`
	Cwd        string `json:"cwd"`
	StartedAt  int64  `json:"startedAt"`
	Name       string `json:"name"`
	NameSource string `json:"nameSource"`
}

// ReadRegistry returns the valid records in dir. The error is for the
// directory itself (missing, a symlink, unreadable); bad records are skipped.
func ReadRegistry(dir string) ([]RegistryRecord, error) {
	fd, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: dir, Err: err}
	}
	d := os.NewFile(uintptr(fd), dir)
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	var out []RegistryRecord
	for _, n := range names {
		m := recordName.FindStringSubmatch(n)
		if m == nil {
			continue
		}
		pid, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if r, ok := readRecord(fd, dir, n, pid); ok {
			out = append(out, r)
		}
	}
	return out, nil
}

// readRecord opens name relative to dirfd and validates it.
func readRecord(dirfd int, dir, name string, pid int) (RegistryRecord, bool) {
	fd, err := openRecord(dirfd, dir, name)
	if err != nil {
		return RegistryRecord{}, false
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxRecord {
		return RegistryRecord{}, false
	}
	b, err := io.ReadAll(io.LimitReader(f, maxRecord+1))
	if err != nil || len(b) > maxRecord {
		return RegistryRecord{}, false
	}
	var raw rawRecord
	if json.Unmarshal(b, &raw) != nil || raw.PID != pid || !uuidRE.MatchString(raw.SessionID) {
		return RegistryRecord{}, false
	}
	r := RegistryRecord{PID: raw.PID, SessionID: raw.SessionID, Cwd: raw.Cwd, StartedAt: raw.StartedAt}
	r.Name = CleanName(raw.Name)
	if r.Name != "" && NameSources[raw.NameSource] {
		r.NameSource = raw.NameSource
	}
	return r, true
}

// CleanName is a registry name as the hook keeps it (CS-TMUX-034): control
// characters stripped, whitespace collapsed; "" when the result is longer
// than 200 characters or starts with "-" (it could read as a flag).
func CleanName(s string) string {
	s = Printable(s)
	if utf8.RuneCountInString(s) > maxNameLen || strings.HasPrefix(s, "-") {
		return ""
	}
	return s
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
// join. A candidate naming the mark's current conversation wins; else the
// earliest start. ok is false for a ralph mark and when nothing matches.
func Match(m Mark, recs []RegistryRecord) (RegistryRecord, bool) {
	class, err := strconv.Atoi(m.Class)
	if err != nil || class < 0 || class > 255 {
		return RegistryRecord{}, false
	}
	var min, max int64
	switch m.Mode {
	case ModeClaude:
		min, max = m.Since-sinceSlack, m.Since+PrimaryWindow.Milliseconds()
	case ModeJoin:
		min, max = m.Since, m.Since+JoinWindow.Milliseconds()
	default:
		return RegistryRecord{}, false
	}
	var best RegistryRecord
	found, current := false, false
	for _, r := range recs {
		if r.PID%256 != class || r.StartedAt < min || r.StartedAt > max {
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
func Apply(m Mark, r RegistryRecord) Mark {
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
