package sessionend

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/registry"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

// registryDirs is where the container's records are: the mark's registry dir
// (the claude-sandbox.registry label, CS-LNCH-109), else — a container from
// before the label — both places they could be, as the resume guard reads
// them (CS-SESS-066): the config dir's sessions and the legacy peers root.
func (r *run) registryDirs() []string {
	if r.o.Mark.RegistryDir != "" {
		return []string{r.o.Mark.RegistryDir}
	}
	var dirs []string
	if r.o.ConfigDir != "" {
		dirs = append(dirs, filepath.Join(r.o.ConfigDir, "sessions"))
	}
	if r.o.Home != "" {
		dirs = append(dirs, legacyRegistry(r.o.Home))
	}
	return dirs
}

// slot is one record the poll read: the record (the last good one when it
// stopped parsing) and whether it parsed now.
type slot struct {
	rec       registry.Record
	malformed bool
}

// activity reads the container's live records and decides (02 § 3).
func (r *run) activity() verdict {
	var slots []slot
	for _, dir := range r.registryDirs() {
		d, err := registry.OpenDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			r.logf("registry: %v", err)
			return verdictUnreadable
		}
		es, err := d.Entries()
		if err != nil {
			d.Close()
			r.logf("registry: %v", err)
			return verdictUnreadable
		}
		for _, e := range es {
			if r.class < 0 || e.PID%256 != r.class {
				continue
			}
			rec, err := d.Read(e)
			switch {
			case errors.Is(err, os.ErrNotExist):
				continue
			case err != nil:
				// A counted record that stops parsing keeps its last state;
				// one never seen parsing counts as unknown work (wait).
				if last, ok := r.last[e.PID]; ok {
					slots = append(slots, slot{rec: last})
				} else {
					slots = append(slots, slot{rec: registry.Record{PID: e.PID}, malformed: true})
				}
				continue
			}
			if rec.StartedAt < r.since || (r.domain != "" && rec.PIDDomain == r.domain) {
				continue
			}
			r.last[e.PID] = rec
			slots = append(slots, slot{rec: rec})
		}
		d.Close()
	}
	// Liveness (02 § 3.2): only when some record would hold the container.
	if anyWaits(slots) {
		slots = r.alive(slots)
	}
	r.counted = r.counted[:0]
	for _, s := range slots {
		if !s.malformed {
			r.counted = append(r.counted, s.rec)
		}
	}
	if len(slots) == 0 {
		return verdictNone
	}
	if anyWaits(slots) {
		return verdictWait
	}
	return verdictEnd
}

// Ends reports whether a record's status lets the session end: idle (a turn
// boundary) or waiting (blocked on a person nobody can reach). busy, shell,
// an empty or unknown value wait (02 § 3.3).
func Ends(status string) bool {
	return status == registry.StatusIdle || status == registry.StatusWaiting
}

func anyWaits(slots []slot) bool {
	for _, s := range slots {
		if s.malformed || !Ends(s.rec.Status) {
			return true
		}
	}
	return false
}

// alive drops the records whose process is confirmed dead in the container
// (CS-SESS-089's test): one bounded "docker top" lists its host pids, and a
// record lives while one of them has its procStart. A record without
// procStart, a failed docker top and an unreadable stat all count.
func (r *run) alive(slots []slot) []slot {
	now := r.clock.Now()
	need := false
	for _, s := range slots {
		if s.malformed || s.rec.ProcStart == "" {
			continue
		}
		st, ok := r.live[liveKey(s.rec)]
		if !ok || (st.alive && now.Sub(st.checked) >= LiveRecheck) {
			need = true
		}
	}
	if need {
		starts, err := r.topStarts()
		if err != nil {
			r.logf("docker top: %v (records count as alive)", err)
		}
		for _, s := range slots {
			if s.malformed || s.rec.ProcStart == "" {
				continue
			}
			k := liveKey(s.rec)
			if st, ok := r.live[k]; ok && !(st.alive && now.Sub(st.checked) >= LiveRecheck) {
				continue
			}
			if err != nil || starts == nil {
				r.live[k] = liveState{alive: true, checked: now}
				continue
			}
			r.live[k] = liveState{alive: starts[s.rec.ProcStart], checked: now}
		}
	}
	out := slots[:0]
	for _, s := range slots {
		if !s.malformed && s.rec.ProcStart != "" {
			if st, ok := r.live[liveKey(s.rec)]; ok && !st.alive {
				continue
			}
		}
		out = append(out, s)
	}
	return out
}

func liveKey(rec registry.Record) string { return strconv.Itoa(rec.PID) + "/" + rec.ProcStart }

// topStarts returns the starttimes of the container's host processes, nil
// when no listed pid's stat could be read or one could not (cannot rule a
// record out).
func (r *run) topStarts() (map[string]bool, error) {
	out, err := tmuxpane.BoundedRunner{R: r.o.Runner, Timeout: TopBound}.Output(execx.Cmd{
		Name: "docker", Args: []string{"top", r.id, "-o", "pid"},
	})
	if err != nil {
		return nil, err
	}
	starts := map[string]bool{}
	read, unknown := 0, false
	for i, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) == 0 {
			continue
		}
		if _, err := strconv.ParseUint(f[0], 10, 32); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(procRootOf(r.o.ProcRoot), f[0], "stat"))
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				unknown = true
			}
			continue
		}
		st, ok := StartTime(string(data))
		if !ok {
			unknown = true
			continue
		}
		read++
		starts[st] = true
	}
	if unknown || read == 0 {
		return nil, nil
	}
	return starts, nil
}

// StartTime returns field 22 (starttime) of a /proc/<pid>/stat line, split
// after its last ")".
func StartTime(stat string) (string, bool) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return "", false
	}
	f := strings.Fields(stat[i+1:])
	if len(f) < 20 {
		return "", false
	}
	if _, err := strconv.ParseUint(f[19], 10, 64); err != nil {
		return "", false
	}
	return f[19], true
}

// sessionsOf is the logged form of the counted records: the earliest start
// is the primary, the rest joins; each with its resume command.
func (r *run) sessionsOf() []Session {
	recs := append([]registry.Record(nil), r.counted...)
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].StartedAt < recs[j].StartedAt })
	var out []Session
	for i, rec := range recs {
		kind := "join"
		if i == 0 {
			kind = "primary"
		}
		out = append(out, Session{SessionID: rec.SessionID, Name: rec.Name, NameSource: rec.NameSource,
			PID: rec.PID, Kind: kind, Resume: ResumeCommand(r.o.Mark, rec)})
	}
	return out
}

// worktreeName is a worktree name read from a record's cwd (CS-TMUX-036).
var worktreeName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// worktreeOf says where rec's claude ran, for its resume command: a worktree
// read from a cwd under <root>/.claude/worktrees/<name> for a root holding
// the project (as tmuxpane.Apply reads a join's), else the mark's own place
// (a cwd under its CwdRoot), else the shared checkout (a cwd under the
// project). ok is false for a cwd under none of them: no
// command is better than a wrong one.
func worktreeOf(m tmuxpane.Mark, cwd string) (worktree string, ok bool) {
	if cwd == "" {
		return "", false
	}
	cwd = filepath.Clean(cwd)
	const sep = "/.claude/worktrees/"
	// The FIRST worktrees dir whose root holds the project, and whose
	// worktree does not itself hold it (a project launched inside a
	// worktree), decides: nothing after it falls through.
	for off := 0; ; {
		j := strings.Index(cwd[off:], sep)
		if j < 0 {
			break
		}
		i := off + j
		off = i + len(sep)
		root := cwd[:i]
		name, _, _ := strings.Cut(cwd[off:], "/")
		if i == 0 || name == "" || !within(m.Project, root) || within(m.Project, root+sep+name) {
			continue
		}
		if strings.Contains(cwd[off+len(name):]+"/", sep) {
			return "", false // a worktree nested in a worktree: no command
		}
		if worktreeName.MatchString(name) && name != "." && name != ".." {
			return name, true
		}
		return "", false // a name no worktree flag can carry
	}
	if m.CwdRoot != "" && within(cwd, m.CwdRoot) {
		return m.Worktree, !m.WorktreeGenerated || m.Worktree != ""
	}
	if m.Project != "" && within(cwd, m.Project) {
		return "", true
	}
	return "", false
}

// within reports whether p is root or below it, lexically.
func within(p, root string) bool {
	p, root = filepath.Clean(p), filepath.Clean(root)
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

// ResumeCommand is the command that resumes rec's conversation in a new
// container of the mark's project (tmuxpane.ResumeCommand), in the worktree
// rec's cwd names; "" when that place cannot be told or a value it would
// print is not printable.
func ResumeCommand(m tmuxpane.Mark, rec registry.Record) string {
	wt, ok := worktreeOf(m, rec.Cwd)
	if !ok {
		return ""
	}
	m.Worktree, m.WorktreeGenerated = wt, false
	m.Conversation, m.Name, m.NameSource = rec.SessionID, rec.Name, rec.NameSource
	if !tmuxpane.PrintableMark(m) {
		return ""
	}
	return tmuxpane.ResumeCommand(m)
}
