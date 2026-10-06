package resumeguard

// The continue guard (CS-LNCH-182, CS-SESS-093..095; plan continue-resume-guard
// 00 § 4, 01 § 3, 02 §§ 1-2/5, 03 §§ 3-4). "--continue" names no conversation:
// Claude Code picks the newest one in the directory, inside the container,
// after the launcher has gone, and (2.1.290) skips only conversations held by
// live BACKGROUND sessions. The launcher never reads transcripts, so it cannot
// learn which conversation that will be; it refuses while ANY live session has
// a conversation open in the directories claude would continue in. Every live
// holder counts, whatever its kind or entrypoint (operator answer 87 a, "F0"):
// a filter on either can let a second writer through.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/registry"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
)

// ContinueTarget is where a guarded continue would continue (CS-SESS-093).
type ContinueTarget struct {
	// Exact are directories a holder's cwd must equal: the launcher's
	// physical cwd, or a join's exec -w directory.
	Exact []string
	// Under are directories a holder's cwd may equal or lie below: the named
	// worktrees (<git root>/.claude/worktrees/<name>).
	Under []string
	// Project is the launch's project dir: rule a' looks only at its
	// sandboxes.
	Project string
}

// Has reports whether cwd is in the target.
func (t ContinueTarget) Has(cwd string) bool {
	if cwd == "" {
		return false
	}
	for _, e := range t.Exact {
		if samePath(cwd, e) {
			return true
		}
	}
	for _, u := range t.Under {
		if InTree(cwd, u) {
			return true
		}
	}
	return false
}

// InTree reports whether p is root or lies below it: filepath.Rel gives "."
// or a path whose first component is not ".." (so "..foo" is inside, and
// "/wt/foo" is not under "/wt/fo"). Tried on the lexical forms, then on the
// symlink-resolved forms where both resolve.
func InTree(p, root string) bool {
	if within(filepath.Clean(p), filepath.Clean(root)) {
		return true
	}
	rp, errP := filepath.EvalSymlinks(p)
	rr, errR := filepath.EvalSymlinks(root)
	return errP == nil && errR == nil && within(rp, rr)
}

func within(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// RunContinue evaluates the continue check: rule a' (a sandbox of the project
// still starting a session that resumes or continues), rule b' (a sandbox's
// live record whose cwd is in the target), then rule h' (a claude on the
// host). Like Run, every sandbox is looked at before a failure to read one
// decides, and it fails closed.
func (c Check) RunContinue() Verdict {
	c.testGuards()
	if c.Continue == nil {
		panic("resumeguard: RunContinue without a target")
	}
	if c.DiscoveryErr != nil {
		return Verdict{Open: true, Reason: c.DiscoveryErr.Error()}
	}
	domain, domainErr := c.hostDomain()
	reason := ""
	for i := range c.Sessions {
		s := c.Sessions[i]
		if !c.sameStore(s) {
			continue // another transcript store: nothing this --continue picks
		}
		h, err := c.holdsDir(s, domain)
		if err != nil {
			if h.starting {
				return Verdict{Open: true, Holder: &s, Starting: true}
			}
			if reason == "" {
				reason = fmt.Sprintf("the registry of %s: %v", s.Name, err)
			}
			continue
		}
		if len(h.matches) > 0 {
			r, alive, err := c.aliveRecord(s, h.matches)
			if err != nil {
				if reason == "" {
					reason = err.Error()
				}
				continue
			}
			if alive {
				return Verdict{Open: true, Holder: &s, SessionID: strings.ToLower(r.SessionID), Cwd: r.Cwd}
			}
			// Every matching record is stale (CS-SESS-089): judge the
			// container without them.
		}
		if h.starting && h.others == 0 {
			return Verdict{Open: true, Holder: &s, Starting: true}
		}
	}
	if reason != "" {
		return Verdict{Open: true, Reason: reason}
	}
	return c.hostCheckDir(domain, domainErr)
}

// dirHolding is what one sandbox's labels and records say (CS-SESS-093).
type dirHolding struct {
	// starting: rule a' applies to it — a sandbox of the project carrying
	// the continue or the resume label (not in a RecordsOnly check).
	starting bool
	// matches are its current records whose cwd is in the target, still to
	// be confirmed alive; others counts its current records elsewhere.
	matches []registry.Record
	others  int
}

// holdsDir reads rules a' and b' for one sandbox.
func (c Check) holdsDir(s sessions.Session, domain string) (dirHolding, error) {
	h := dirHolding{starting: !c.RecordsOnly && (s.Continue || s.Resume != "") &&
		c.Continue.Project != "" && samePath(s.Project, c.Continue.Project)}
	switch state(s) {
	case sessions.StateCreated:
		// A reservation cannot have written a record yet.
		if c.RecordsOnly {
			return dirHolding{}, nil
		}
		return h, nil
	case "running", "paused":
	default:
		return dirHolding{}, nil
	}
	class, err := strconv.Atoi(s.PIDClass)
	if err != nil || class < 0 || class > 255 {
		// No class: its records cannot be told from anyone else's; judged by
		// its labels only.
		return h, nil
	}
	recs, err := c.recordsAt(c.registryDirs(s), class)
	if err != nil {
		return h, err
	}
	var since int64
	if !s.CreatedAt.IsZero() {
		since = s.CreatedAt.Truncate(time.Second).UnixMilli()
	}
	for _, r := range recs {
		if r.StartedAt < since {
			continue // an earlier container's leftover at the same class
		}
		if domain != "" && r.PIDDomain == domain {
			continue // the host's own claude: rule h' judges it
		}
		if r.Cwd == "" {
			// CS-SESS-093: a record that cannot be placed fails closed, as a
			// malformed one does; a renamed field must not hide every holder.
			return h, fmt.Errorf("%d.json: no cwd", r.PID)
		}
		if c.Continue.Has(r.Cwd) {
			h.matches = append(h.matches, r)
		} else {
			h.others++
		}
	}
	return h, nil
}

// sameStore reports whether a sandbox may share the launch's transcript
// store, i.e. its config dir is the launch's or cannot be told apart from it
// (CS-SESS-093; plan 03 § 3, 02 § 5). Without the registry label the
// container predates the labels: unknown, so it counts. With it, an empty
// configdir label means CLAUDE_CONFIG_DIR was unset: <home>/.claude. It is
// left out only when the two differ lexically AND both resolve AND the
// resolved forms differ.
func (c Check) sameStore(s sessions.Session) bool {
	if s.RegistryDir == "" {
		return true
	}
	dir := s.ConfigDirEnv
	if dir == "" {
		if c.Home == "" {
			return true
		}
		dir = filepath.Join(c.Home, ".claude")
	}
	if c.ConfigDir == "" || filepath.Clean(dir) == filepath.Clean(c.ConfigDir) {
		return true
	}
	rd, errD := filepath.EvalSymlinks(dir)
	rc, errC := filepath.EvalSymlinks(c.ConfigDir)
	return errD != nil || errC != nil || rd == rc
}

// hostCheckDir finds a claude running on the host itself with a conversation
// open in the target (CS-SESS-094): a record in the launch's config dir only
// (another config dir is another transcript store), in the launcher's own pid
// namespace, whose process is still the one that wrote it.
func (c Check) hostCheckDir(domain string, domainErr error) Verdict {
	if domainErr != nil {
		return Verdict{Open: true, Reason: fmt.Sprintf("this host's pid namespace: %v", domainErr)}
	}
	if domain == "" || c.ConfigDir == "" {
		return Verdict{} // not Linux
	}
	dir := filepath.Join(c.ConfigDir, "sessions")
	d, err := registry.OpenDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return Verdict{}
	}
	if err != nil {
		return Verdict{Open: true, Reason: err.Error()}
	}
	defer d.Close()
	es, err := d.Entries()
	if err != nil {
		return Verdict{Open: true, Reason: err.Error()}
	}
	for _, e := range es {
		// A malformed record is skipped, as in CS-SESS-068.
		r, err := d.Read(e)
		if err != nil || r.PIDDomain != domain || !c.Continue.Has(r.Cwd) {
			continue
		}
		if c.procLive(r.PID, r.ProcStart) {
			return Verdict{Open: true, HostPID: r.PID, SessionID: strings.ToLower(r.SessionID), Cwd: r.Cwd}
		}
	}
	return Verdict{}
}
