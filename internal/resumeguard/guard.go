// Package resumeguard decides whether a conversation is already open in
// another session, so a launch that resumes it would make two sessions write
// one transcript (CS-SESS-065..069; plan sandbox-reboot-restore 05 F6, 06 § 4,
// 07 § 4/§ 6/§ 7, 08 § 4).
//
// Claude Code guards a conversation only against a holder in its own pid
// namespace, so nothing stops a second sandbox, or a sandbox and the host,
// from resuming one id. The launcher runs this check inside the host launch
// lock, before docker create, for a launch carrying the claude-sandbox.resume
// label (CS-LNCH-110). It fails closed: what cannot be read counts as open.
package resumeguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
)

// Retries is how often a malformed record at a watched class is read again
// before it counts as unreadable (07 § 7): Claude Code writes by rename, but a
// partial write is possible on some paths.
const Retries = 3

// RetryDelay spaces the retries, about 1 s in all.
const RetryDelay = 330 * time.Millisecond

// Check is one guard run.
type Check struct {
	// ID is the conversation the launch resumes (a canonical UUID).
	ID string
	// Sessions is the discovery the launch holds the lock over, and
	// DiscoveryErr its failure (which fails closed).
	Sessions     []sessions.Session
	DiscoveryErr error
	// Home is the invoking user's home; ConfigDir the launcher's resolved
	// Claude config dir (CLAUDE_CONFIG_DIR, else ~/.claude). Both name the
	// fallback registries of containers from before the registry label and
	// the host claude's registries.
	Home      string
	ConfigDir string
	// ProcRoot is /proc; "" means the real one. GOOS is runtime.GOOS unless
	// set. Sleep spaces retries; nil means time.Sleep.
	ProcRoot string
	GOOS     string
	Sleep    func(time.Duration)
}

// Verdict is the check's answer. Open is true when the launch must not go
// ahead: Holder names the sandbox holding the conversation, HostPID a claude
// process on the host, or, when neither, Reason says what could not be read.
type Verdict struct {
	Open    bool
	Holder  *sessions.Session
	HostPID int
	Reason  string
}

// Run evaluates the check. Every sandbox is looked at before a failure to
// read one decides, so a holder is named whenever one is found.
func (c Check) Run() Verdict {
	if testing.Testing() && c.Home != "" && isRealHome(c.Home) {
		panic("resumeguard: a test would read the real home's registry; set HOME to a scratch directory")
	}
	if c.DiscoveryErr != nil {
		return Verdict{Open: true, Reason: c.DiscoveryErr.Error()}
	}
	reason := ""
	for i := range c.Sessions {
		s := c.Sessions[i]
		held, err := c.holds(s)
		if held {
			return Verdict{Open: true, Holder: &s}
		}
		if err != nil && reason == "" {
			reason = fmt.Sprintf("the registry of %s: %v", s.Name, err)
		}
	}
	if reason != "" {
		return Verdict{Open: true, Reason: reason}
	}
	return c.hostCheck()
}

// state is a session's docker state, with an older row's derived from its
// status text; "" for a container that holds nothing (exited, restarting).
func state(s sessions.Session) string {
	switch {
	case s.Reserved():
		return sessions.StateCreated
	case s.Down():
		return ""
	case s.State == "":
		return "running"
	}
	return s.State
}

// holds applies rules a and b (CS-SESS-065) to one sandbox.
func (c Check) holds(s sessions.Session) (bool, error) {
	labelled := s.Resume != "" && strings.EqualFold(s.Resume, c.ID)
	switch state(s) {
	case sessions.StateCreated:
		// A reservation cannot have written a record yet.
		return labelled, nil
	case "running", "paused":
	default:
		return false, nil
	}
	class, err := strconv.Atoi(s.PIDClass)
	if err != nil || class < 0 || class > 255 {
		// No class: its records cannot be told from anyone else's.
		return labelled, nil
	}
	recs, err := c.recordsAt(c.registryDirs(s), class)
	if err != nil {
		return labelled, err
	}
	var since int64
	if !s.CreatedAt.IsZero() {
		// docker ps reports whole seconds; the container was created within
		// the second, so the truncated time never drops its own records.
		since = s.CreatedAt.Truncate(time.Second).UnixMilli()
	}
	current := 0
	for _, r := range recs {
		if r.StartedAt < since {
			continue // an earlier container's leftover at the same class
		}
		current++
		if strings.EqualFold(r.SessionID, c.ID) {
			return true, nil
		}
	}
	// Labelled, and no record of its own yet: it is still opening the id. A
	// labelled container whose records all name other ids switched away.
	return labelled && current == 0, nil
}

// registryDirs is where a sandbox's records land: its registry label, else
// (a container from before the label) both places they could be.
func (c Check) registryDirs(s sessions.Session) []string {
	if s.RegistryDir != "" {
		return []string{s.RegistryDir}
	}
	var dirs []string
	if c.ConfigDir != "" {
		dirs = append(dirs, filepath.Join(c.ConfigDir, "sessions"))
	}
	if c.Home != "" {
		dirs = append(dirs, filepath.Join(c.Home, launch.PeerRegistryRoot, "sessions"))
	}
	return dirs
}

// recordsAt reads every record at a pid class across dirs. A directory that
// exists but cannot be read, or a record there that stays malformed after
// the retries, is an error.
func (c Check) recordsAt(dirs []string, class int) ([]Record, error) {
	var out []Record
	for _, dir := range dirs {
		d, err := openDir(dir)
		if err != nil {
			return nil, err
		}
		if d == nil {
			continue
		}
		recs, err := c.readClass(d, class)
		d.close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", dir, err)
		}
		out = append(out, recs...)
	}
	return out, nil
}

func (c Check) readClass(d *registryDir, class int) ([]Record, error) {
	es, err := d.entries()
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, e := range es {
		if e.pid%256 != class {
			continue
		}
		var r Record
		for attempt := 0; ; attempt++ {
			r, err = d.read(e)
			if err == nil || errors.Is(err, os.ErrNotExist) || attempt >= Retries {
				break
			}
			c.sleep(RetryDelay)
		}
		if errors.Is(err, os.ErrNotExist) {
			continue // the session exited while the directory was read
		}
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func (c Check) sleep(d time.Duration) {
	if c.Sleep != nil {
		c.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (c Check) procRoot() string {
	if c.ProcRoot != "" {
		return c.ProcRoot
	}
	return "/proc"
}

// hostCheck finds a claude running on the host itself with the conversation
// open (CS-SESS-068): a record in the launcher's own pid namespace whose
// process is still the one that wrote it.
func (c Check) hostCheck() Verdict {
	goos := c.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos != "linux" {
		return Verdict{}
	}
	ns, err := os.Readlink(filepath.Join(c.procRoot(), "self", "ns", "pid"))
	if err != nil {
		return Verdict{Open: true, Reason: fmt.Sprintf("this host's pid namespace: %v", err)}
	}
	domain := "linux::" + ns
	var dirs []string
	add := func(configDir string) {
		if configDir == "" {
			return
		}
		if d := filepath.Join(configDir, "sessions"); !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	if c.Home != "" {
		add(filepath.Join(c.Home, ".claude"))
	}
	add(c.ConfigDir)
	for _, dir := range dirs {
		d, err := openDir(dir)
		if err != nil {
			return Verdict{Open: true, Reason: err.Error()}
		}
		if d == nil {
			continue
		}
		es, err := d.entries()
		if err != nil {
			d.close()
			return Verdict{Open: true, Reason: err.Error()}
		}
		for _, e := range es {
			// A malformed record is skipped: its namespace cannot be known,
			// and the host check has no class to narrow what it watches.
			r, err := d.read(e)
			if err != nil || r.PIDDomain != domain || !strings.EqualFold(r.SessionID, c.ID) {
				continue
			}
			if c.procLive(r.PID, r.ProcStart) {
				d.close()
				return Verdict{Open: true, HostPID: r.PID}
			}
		}
		d.close()
	}
	return Verdict{}
}

// procLive reports whether pid is still the process that wrote a record with
// procStart (07 § 4): /proc/<pid> absent = no; its stat's field 22 equal to
// procStart = yes, different = no (the pid was reused); present but
// unreadable (hidepid, EACCES) or unparsable = yes, failing closed.
func (c Check) procLive(pid int, procStart string) bool {
	dir := filepath.Join(c.procRoot(), strconv.Itoa(pid))
	if _, err := os.Lstat(dir); err != nil {
		return !errors.Is(err, os.ErrNotExist)
	}
	data, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return !errors.Is(err, os.ErrNotExist)
	}
	start, ok := StartTime(string(data))
	if !ok {
		return true
	}
	return start == procStart
}

// StartTime returns field 22 (starttime) of a /proc/<pid>/stat line. The
// line is split after its LAST ")": the comm in field 2 may hold spaces and
// parentheses.
func StartTime(stat string) (string, bool) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return "", false
	}
	f := strings.Fields(stat[i+1:]) // f[0] is field 3
	if len(f) < 20 {
		return "", false
	}
	if _, err := strconv.ParseUint(f[19], 10, 64); err != nil {
		return "", false
	}
	return f[19], true
}

// isRealHome reports whether home is the invoking user's real home, which a
// test must never read.
func isRealHome(home string) bool {
	h, err := os.UserHomeDir()
	return err == nil && h != "" && filepath.Clean(h) == filepath.Clean(home)
}
