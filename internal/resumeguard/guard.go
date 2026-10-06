// Package resumeguard decides whether a conversation is already open in
// another session, so a launch that resumes it would make two sessions write
// one transcript (CS-SESS-065..069; plan sandbox-reboot-restore 05 F6, 06 § 4,
// 07 § 4/§ 6/§ 7, 08 § 4).
//
// Claude Code (2.1.290) does not stop a second interactive session from
// opening a conversation another one has open — neither "--resume <id>" nor
// "--continue" checks for an interactive holder, in any pid namespace — so
// nothing stops a second sandbox, a join, or a sandbox and the host, from
// opening one transcript. The launcher runs Run inside the host launch lock,
// before docker create, for a launch carrying the claude-sandbox.resume label
// (CS-LNCH-110), and RunContinue (continue.go) for a guarded continue
// (CS-LNCH-182, CS-SESS-093..095), which names no id and is matched by
// directory. Both fail closed: what cannot be read counts as open.
package resumeguard

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
	"github.com/kmacmcfarlane/claude-sandbox/internal/registry"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
)

// Retries is how often a malformed record at a watched class is read again
// before it counts as unreadable (07 § 7): Claude Code writes by rename, but a
// partial write is possible on some paths. Only the failures a partial write
// can cause (registry.ErrPartial) are retried.
const Retries = 3

// RetryDelay spaces the retries, about 1 s in all.
const RetryDelay = 330 * time.Millisecond

// TopTimeout bounds the one "docker top" that confirms a matching record's
// process is alive (CS-SESS-089); it is killed after it.
var TopTimeout = 5 * time.Second

// killGrace is how long the guard waits for a killed "docker top" to be
// reaped before it moves on (CS-SESS-089).
var killGrace = 500 * time.Millisecond

// Check is one guard run.
type Check struct {
	// ID is the conversation the launch resumes (a canonical UUID).
	ID string
	// Sessions is the discovery the launch holds the lock over, and
	// DiscoveryErr its failure (which fails closed).
	Sessions     []sessions.Session
	DiscoveryErr error
	// Runner runs the confirming "docker top" (CS-SESS-089).
	Runner execx.Runner
	// Home is the invoking user's home; ConfigDir the launcher's resolved
	// Claude config dir (CLAUDE_CONFIG_DIR, else ~/.claude). Both name the
	// fallback registries of containers from before the registry label and
	// the host claude's registries.
	Home      string
	ConfigDir string
	// ProcRoot is /proc; "" means the real one. GOOS is runtime.GOOS unless
	// set. Sleep spaces retries; nil means time.Sleep.
	ProcRoot string
	// MachineIDPath is the file Claude Code reads for the machine id in
	// pidDomain (/etc/machine-id); "" means that one, and panics under go test
	// so a test never reads the real /etc.
	MachineIDPath string
	GOOS          string
	Sleep         func(time.Duration)

	// Continue is the continue check's target (RunContinue, CS-SESS-093):
	// the directories claude would continue in. Unused by Run.
	Continue *ContinueTarget
	// RecordsOnly is the advisory pre-build continue check (CS-SESS-094):
	// rules b' and h' only — no label and no reservation holds.
	RecordsOnly bool
}

// Verdict is the check's answer. Open is true when the launch must not go
// ahead: Holder names the sandbox holding the conversation, HostPID a claude
// process on the host, or, when neither, Reason says what could not be read.
// The continue check (RunContinue) also gives the held conversation and the
// holder record's cwd (SessionID, Cwd), or, for a holder that is still
// starting (rule a'), Starting.
type Verdict struct {
	Open      bool
	Holder    *sessions.Session
	HostPID   int
	Reason    string
	SessionID string
	Cwd       string
	Starting  bool
}

// Run evaluates the check. Every sandbox is looked at before a failure to
// read one decides, so a holder is named whenever one is found.
func (c Check) Run() Verdict {
	c.testGuards()
	if c.DiscoveryErr != nil {
		return Verdict{Open: true, Reason: c.DiscoveryErr.Error()}
	}
	domain, domainErr := c.hostDomain()
	reason := ""
	for i := range c.Sessions {
		s := c.Sessions[i]
		h, err := c.holds(s, domain)
		if err != nil {
			if h.labelled {
				return Verdict{Open: true, Holder: &s}
			}
			if reason == "" {
				reason = fmt.Sprintf("the registry of %s: %v", s.Name, err)
			}
			continue
		}
		if len(h.matches) > 0 {
			alive, err := c.alive(s, h.matches)
			if err != nil {
				if reason == "" {
					reason = err.Error()
				}
				continue
			}
			if alive {
				return Verdict{Open: true, Holder: &s}
			}
			// Every matching record is stale: judge the container without
			// them (CS-SESS-089).
		}
		if h.labelled && h.others == 0 {
			// Labelled, and no live record of its own yet: it is still
			// opening the id. A labelled container whose records all name
			// other ids switched away.
			return Verdict{Open: true, Holder: &s}
		}
	}
	if reason != "" {
		return Verdict{Open: true, Reason: reason}
	}
	return c.hostCheck(domain, domainErr)
}

// testGuards panics under go test when the check would read the real home's
// or the real ~/.claude's registry.
func (c Check) testGuards() {
	if !testing.Testing() {
		return
	}
	if c.Home != "" && isRealHome(c.Home) {
		panic("resumeguard: a test would read the real home's registry; set HOME to a scratch directory")
	}
	if c.ConfigDir != "" && isRealConfigDir(c.ConfigDir) {
		panic("resumeguard: a test would read the real ~/.claude registry; point CLAUDE_CONFIG_DIR/HOME at a scratch directory")
	}
}

// state is a session's docker state, with an older row's derived from its
// status text. Discovery never lists an exited container (CS-SESS-070); any
// state but created, running and paused holds nothing.
func state(s sessions.Session) string {
	switch {
	case s.Reserved():
		return sessions.StateCreated
	case s.State == "":
		return "running"
	}
	return s.State
}

// holding is what one sandbox's label and records say (CS-SESS-065).
type holding struct {
	// labelled: the container carries the resume label for the id.
	labelled bool
	// matches are its current records naming the id, still to be confirmed
	// alive (CS-SESS-089); others counts its current records naming other ids.
	matches []registry.Record
	others  int
}

// holds reads rules a and b (CS-SESS-065) for one sandbox. domain is the
// launcher's own pid namespace ("" when unknown): a record from it belongs to
// a claude on the host, not to the container (CS-SESS-068 judges it), even
// when the container's registry dir is the host's <config dir>/sessions.
func (c Check) holds(s sessions.Session, domain string) (holding, error) {
	h := holding{labelled: s.Resume != "" && strings.EqualFold(s.Resume, c.ID)}
	switch state(s) {
	case sessions.StateCreated:
		// A reservation cannot have written a record yet.
		return h, nil
	case "running", "paused":
	default:
		return holding{}, nil
	}
	class, err := strconv.Atoi(s.PIDClass)
	if err != nil || class < 0 || class > 255 {
		// No class: its records cannot be told from anyone else's.
		return h, nil
	}
	recs, err := c.recordsAt(c.registryDirs(s), class)
	if err != nil {
		return h, err
	}
	var since int64
	if !s.CreatedAt.IsZero() {
		// docker ps reports whole seconds; the container was created within
		// the second, so the truncated time never drops its own records.
		since = s.CreatedAt.Truncate(time.Second).UnixMilli()
	}
	for _, r := range recs {
		if r.StartedAt < since {
			continue // an earlier container's leftover at the same class
		}
		if domain != "" && r.PIDDomain == domain {
			continue // the host's own claude, not this container's
		}
		if strings.EqualFold(r.SessionID, c.ID) {
			h.matches = append(h.matches, r)
		} else {
			h.others++
		}
	}
	return h, nil
}

// alive reports whether one of the records naming the id is still its
// process (CS-SESS-089). registry.Record pids are the container's own, so the check
// goes by start time: /proc/<pid>/stat field 22 counts clock ticks since boot,
// the same in every pid namespace. A record without a procStart cannot be
// ruled out and needs no docker call. Otherwise ONE bounded "docker top"
// lists the container's host pids; a pid whose stat cannot be read or parsed
// cannot be ruled out either. A failed docker top is an error: fail closed.
func (c Check) alive(s sessions.Session, matches []registry.Record) (bool, error) {
	_, ok, err := c.aliveRecord(s, matches)
	return ok, err
}

// aliveRecord is alive, also returning a record that counts as alive (the
// first that cannot be ruled out), so the continue check can name the
// conversation it holds.
func (c Check) aliveRecord(s sessions.Session, matches []registry.Record) (registry.Record, bool, error) {
	for _, m := range matches {
		if m.ProcStart == "" {
			return m, true, nil
		}
	}
	pids, err := c.top(s.Name)
	if err != nil {
		return registry.Record{}, false, fmt.Errorf("docker top %s: %v", s.Name, err)
	}
	// Only a pid whose stat was read can rule the record out. Nothing listed,
	// or no listed pid visible here (a nested launcher's /proc does not show
	// host pids), cannot rule it out: alive, failing closed.
	unknown, read := false, 0
	for _, pid := range pids {
		data, err := os.ReadFile(filepath.Join(c.procRoot(), pid, "stat"))
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				unknown = true
			}
			continue
		}
		start, ok := StartTime(string(data))
		if !ok {
			unknown = true
			continue
		}
		read++
		for _, m := range matches {
			if start == m.ProcStart {
				return m, true, nil
			}
		}
	}
	if unknown || read == 0 {
		return matches[0], true, nil
	}
	return registry.Record{}, false, nil
}

// top runs "docker top <name> -o pid" through Runner.Start, killed with the
// launcher and after TopTimeout, and returns the host pids it lists.
func (c Check) top(name string) ([]string, error) {
	if c.Runner == nil {
		return nil, errors.New("no docker runner")
	}
	var out, errOut syncBuffer
	proc, err := c.Runner.Start(execx.Cmd{
		Name: "docker", Args: []string{"top", name, "-o", "pid"},
		Stdout: &out, Stderr: &errOut, DieWithParent: true,
	})
	if err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- proc.Wait() }()
	select {
	case werr := <-done:
		if werr != nil {
			if msg := strings.TrimSpace(errOut.String()); msg != "" {
				return nil, fmt.Errorf("%v: %s", werr, msg)
			}
			return nil, werr
		}
	case <-time.After(TopTimeout):
		// The whole group (Start gave it its own): a grandchild holding the
		// stdout pipe would otherwise keep Wait waiting. Then at most
		// killGrace more, so the lock is held about TopTimeout, not twice it.
		if g, ok := proc.(execx.GroupKiller); ok {
			g.KillGroup()
		} else {
			proc.Signal(os.Kill)
		}
		select {
		case <-done:
		case <-time.After(killGrace):
		}
		return nil, fmt.Errorf("no answer in %s", TopTimeout)
	}
	var pids []string
	for i, line := range strings.Split(out.String(), "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) == 0 {
			continue // the header row
		}
		if _, err := strconv.ParseUint(f[0], 10, 32); err == nil {
			pids = append(pids, f[0])
		}
	}
	return pids, nil
}

// syncBuffer is a buffer safe to read while a killed process may still write.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// registryDirs is where a sandbox's records land: its registry label — the
// dir its launch applied, the legacy or the new peers root alike — else (a
// container from before the label, and so from before the peers move,
// CS-DIR-011) both places they could be: its config dir and the LEGACY root.
func (c Check) registryDirs(s sessions.Session) []string {
	if s.RegistryDir != "" {
		return []string{s.RegistryDir}
	}
	var dirs []string
	if c.ConfigDir != "" {
		dirs = append(dirs, filepath.Join(c.ConfigDir, "sessions"))
	}
	if c.Home != "" {
		dirs = append(dirs, filepath.Join(c.Home, launch.LegacyPeerRegistryRoot, "sessions"))
	}
	return dirs
}

// recordsAt reads every record at a pid class across dirs. A directory that
// exists but cannot be read, or a record there that stays malformed after
// the retries, is an error.
func (c Check) recordsAt(dirs []string, class int) ([]registry.Record, error) {
	var out []registry.Record
	for _, dir := range dirs {
		d, err := registry.OpenDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue // a missing directory holds no records
		}
		if err != nil {
			return nil, err
		}
		recs, err := c.readClass(d, class)
		d.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", dir, err)
		}
		out = append(out, recs...)
	}
	return out, nil
}

func (c Check) readClass(d *registry.Dir, class int) ([]registry.Record, error) {
	es, err := d.Entries()
	if err != nil {
		return nil, err
	}
	var out []registry.Record
	for _, e := range es {
		if e.PID%256 != class {
			continue
		}
		var r registry.Record
		for attempt := 0; ; attempt++ {
			r, err = d.Read(e)
			if err == nil || !errors.Is(err, registry.ErrPartial) || attempt >= Retries {
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

// machineID is /etc/machine-id as Claude Code reads it: utf8, trimmed, ""
// when absent or unreadable.
func (c Check) machineID() string {
	p := c.MachineIDPath
	if p == "" {
		if testing.Testing() {
			panic("resumeguard: MachineIDPath unset under go test (would read the real /etc/machine-id)")
		}
		p = "/etc/machine-id"
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return JSTrim(string(b))
}

// JSTrim is String.prototype.trim: invalid UTF-8 first becomes U+FFFD (as
// the utf8 decode does), then the ECMAScript WhiteSpace and LineTerminator
// code points are trimmed from both ends — a BOM goes, U+0085 stays, which
// strings.TrimSpace gets the other way round.
func JSTrim(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	return strings.TrimFunc(s, func(r rune) bool {
		switch r {
		case '\t', '\v', '\f', ' ', '\u00a0', '\ufeff', '\n', '\r', '\u2028', '\u2029':
			return true
		}
		return unicode.Is(unicode.Zs, r)
	})
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
func (c Check) hostCheck(domain string, domainErr error) Verdict {
	if domainErr != nil {
		return Verdict{Open: true, Reason: fmt.Sprintf("this host's pid namespace: %v", domainErr)}
	}
	if domain == "" {
		return Verdict{} // not Linux
	}
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
		d, err := registry.OpenDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue // a missing directory holds no records
		}
		if err != nil {
			return Verdict{Open: true, Reason: err.Error()}
		}
		es, err := d.Entries()
		if err != nil {
			d.Close()
			return Verdict{Open: true, Reason: err.Error()}
		}
		for _, e := range es {
			// A malformed record is skipped: its namespace cannot be known,
			// and the host check has no class to narrow what it watches.
			r, err := d.Read(e)
			if err != nil || r.PIDDomain != domain || !strings.EqualFold(r.SessionID, c.ID) {
				continue
			}
			if c.procLive(r.PID, r.ProcStart) {
				d.Close()
				return Verdict{Open: true, HostPID: r.PID}
			}
		}
		d.Close()
	}
	return Verdict{}
}

// hostDomain is the launcher's own pid domain as Claude Code records it
// (Claude Code 2.1.290 reads only /etc/machine-id): "linux:<machine-id>:<readlink /proc/self/ns/pid>", the id
// being /etc/machine-id trimmed (no other file is tried; absent or unreadable
// = empty, as in a container), so "linux::pid:[<inode>]" without one; ""
// off Linux, where the host check is skipped.
func (c Check) hostDomain() (string, error) {
	goos := c.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos != "linux" {
		return "", nil
	}
	ns, err := os.Readlink(filepath.Join(c.procRoot(), "self", "ns", "pid"))
	if err != nil {
		return "", err
	}
	return "linux:" + c.machineID() + ":" + ns, nil
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

// realHomes are the invoking user's real home directories, by $HOME and by
// the user database (HOME can be unset or point elsewhere; the CS-LNCH-138
// precedent).
func realHomes() []string {
	var out []string
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		out = append(out, h)
	}
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		out = append(out, u.HomeDir)
	}
	return out
}

// samePath compares two paths lexically and, where both resolve, by their
// symlink-resolved forms.
func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// isRealHome reports whether home is the invoking user's real home, which a
// test must never read.
func isRealHome(home string) bool {
	for _, h := range realHomes() {
		if samePath(h, home) {
			return true
		}
	}
	return false
}

// isRealConfigDir reports whether dir is the real ~/.claude, whose registry a
// test must never read either.
func isRealConfigDir(dir string) bool {
	for _, h := range realHomes() {
		if samePath(filepath.Join(h, ".claude"), dir) {
			return true
		}
	}
	return false
}
