// Package sessionend is the session-end helper (CS-LNCH-206..210, CS-LNCH-212;
// plan detached-sandboxes 01 F2 as amended by 02..04).
//
// A sandbox's docker client proxies no signal into a TTY container, so a
// terminal that hangs up (a closed tab, an SSH drop, tmux kill-pane) used to
// leave the container running with nobody attached. On a hang-up the
// launcher now starts "claude-sandbox session-end" detached and exits; the
// helper lets a running turn finish (Claude Code's own registry status),
// steps aside when a terminal re-attaches or a join is still in use, and then
// stops the container with SIGTERM — the end a host claude gets when its
// terminal closes. Every outcome is logged to <StateRoot>/session-ends.jsonl
// with the command that resumes the conversation, and an ended session
// leaves a notice the next interactive launch in the project prints once.
//
// The helper never prints; diagnostics go to <cache>/session-end.log.
package sessionend

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/clients"
	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
	"github.com/kmacmcfarlane/claude-sandbox/internal/oomreport"
	"github.com/kmacmcfarlane/claude-sandbox/internal/registry"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

// Subcommand is the hidden subcommand the launcher starts the helper as.
const Subcommand = "session-end"

// LogFile is the helper's diagnostics log, under the cache root.
const LogFile = "session-end.log"

// Tunables (vars so tests can shorten them).
var (
	// EndPoll spaces the helper's polls and its lock retries.
	EndPoll = 2 * time.Second
	// RecordGrace is how long the helper waits for a live registry record
	// before it keeps the container (02 § 3.3: never end what cannot be
	// seen).
	RecordGrace = 30 * time.Second
	// TurnMax caps the wait for a running turn (operator answer: 30 min,
	// then end anyway). Only polls whose verdict was "wait" count.
	TurnMax = 30 * time.Minute
	// LiveRecheck is how often a record confirmed alive is confirmed again.
	LiveRecheck = 30 * time.Second
	// ReattachSpan is how far apart two sightings of a terminal client older
	// than the hang-up must be to keep the container (03 § 3).
	ReattachSpan = oomreport.DieWait + time.Second
	// InspectBound, TopBound, UnitBound and StopBound bound the docker and
	// systemctl calls.
	InspectBound = tmuxpane.ProbeTimeout
	TopBound     = 5 * time.Second
	UnitBound    = tmuxpane.CallTimeout
	StopBound    = 40 * time.Second
)

// StopSignal is the signal "docker stop" sends: explicit, so a child image's
// STOPSIGNAL never applies (02 § 4). The host check that gates the default
// may change it to SIGHUP (03 § 2).
const StopSignal = "SIGTERM"

// StopGrace is docker stop's -t (the short form works on every docker
// version; docker 29 deprecates --time for --timeout): SIGKILL follows after it.
const StopGrace = 30

// Outcomes, as logged.
const (
	OutcomeEnded          = "ended"
	OutcomeEndedOnItsOwn  = "ended on its own"
	OutcomeReattached     = "kept: reattached"
	OutcomePaused         = "kept: paused"
	OutcomeNoRecord       = "kept: no live record"
	OutcomeRegistry       = "kept: registry unreadable"
	OutcomeClientsUnknown = "kept: clients unknown"
	OutcomeSignalled      = "kept: signalled"
	OutcomeUnitStopping   = "kept: unit stopping"
	OutcomeStopFailed     = "kept: stop failed"
	OutcomeNoID           = "kept: no container id"
	// OutcomeStoodDown is a sandbox or a non-Linux host: nothing logged.
	OutcomeStoodDown = "kept: not on a Linux host"
	// OutcomeLostLock is another helper's container gone while this one
	// waited for the lock: nothing logged.
	OutcomeLostLock = "lost the lock"
)

// Clock is the helper's time seam.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Options is one helper run.
type Options struct {
	// Mark is the session's pane mark as the launcher built it: container
	// name and 64-hex id, project, instance, class, registry dir, config
	// dir, worktree, model, since.
	Mark tmuxpane.Mark
	// HangupAt is the launcher's clock when it classified the hang-up.
	HangupAt time.Time
	// StateRoot holds the leases, the end lock, the log and the notices;
	// "" panics under go test.
	StateRoot string
	// Home and ConfigDir name the fallback registry dirs of a container
	// without a registry label.
	Home      string
	ConfigDir string
	// ProcRoot is /proc; "" panics under go test. MachineIDPath is
	// /etc/machine-id; "" panics under go test.
	ProcRoot      string
	MachineIDPath string
	Runner        execx.Runner
	Clock         Clock
	// Signals delivers the helper's own SIGTERM/SIGHUP: keep.
	Signals <-chan os.Signal
	// UID is the invoking user; negative means os.Getuid.
	UID int
	// GOOS is runtime.GOOS unless set; InSandbox stands the helper down.
	GOOS      string
	InSandbox bool
	// Log receives diagnostics; nil discards them.
	Log io.Writer
}

// Session is one live conversation of the container, as logged.
type Session struct {
	SessionID  string `json:"sessionId"`
	Name       string `json:"name,omitempty"`
	NameSource string `json:"nameSource,omitempty"`
	PID        int    `json:"pid"`
	Kind       string `json:"kind"`
	Resume     string `json:"resume"`
}

// Result is how a helper run ended.
type Result struct {
	Outcome  string
	Capped   bool
	Waited   time.Duration
	Sessions []Session
	// Logged is true when the outcome was appended to the log.
	Logged bool
}

// run is one helper run's state.
type run struct {
	o       Options
	clock   Clock
	id      string
	boot    time.Time
	turn    *Turn
	seenOld map[string]time.Time
}

type liveState struct {
	alive   bool
	checked time.Time
}

// Run runs the helper to its outcome.
func Run(o Options) Result {
	if o.Clock == nil {
		o.Clock = realClock{}
	}
	if o.UID < 0 {
		o.UID = os.Getuid()
	}
	goos := o.GOOS
	if goos == "" {
		goos = runtimeGOOS
	}
	r := &run{o: o, clock: o.Clock, id: o.Mark.ContainerID, seenOld: map[string]time.Time{}}
	if o.InSandbox || goos != "linux" {
		// CS-LNCH-210: a nested launcher cannot see the host's leases or
		// processes, and only Linux has the /proc scan.
		return Result{Outcome: OutcomeStoodDown}
	}
	stateRoot := checkStateRoot(o.StateRoot)
	if !clients.IsFullID(r.id) {
		return r.finish(Result{Outcome: OutcomeNoID})
	}
	class := -1
	if c, err := strconv.Atoi(o.Mark.Class); err == nil && c >= 0 {
		class = c % 256
	}
	// The turn rule (CS-LNCH-208), shared with the restart (CS-SESS-108).
	r.turn = NewTurn(TurnOptions{
		Runner: o.Runner, ProcRoot: o.ProcRoot, Container: r.id, Class: class,
		Since: o.Mark.Since, OwnDomain: ownDomain(o.ProcRoot, o.MachineIDPath),
		RegistryDirs: r.registryDirs(), Clock: o.Clock, Logf: r.logf,
	})
	r.boot, _ = clients.BootTime(procRootOf(o.ProcRoot))
	r.logf("hang-up of %s (%s) at %s", o.Mark.Container, r.id, o.HangupAt.UTC().Format(time.RFC3339))

	if r.guardsKeep() {
		return r.finish(Result{Outcome: OutcomeUnitStopping})
	}
	lock, res, ok := r.lock(stateRoot)
	if !ok {
		return res
	}
	if lock != nil {
		defer lock.Close()
	}
	return r.poll()
}

var runtimeGOOS = runtime.GOOS

// lock takes the container's end.lock (one helper per container). A held
// lock is retried every EndPoll until it is taken, or the container is gone
// (lost: nothing logged) or paused (kept). A state root that cannot be used
// leaves the helper running without the lock: two helpers both running
// docker stop is harmless.
func (r *run) lock(stateRoot string) (*os.File, Result, bool) {
	for {
		f, held, err := TryEndLock(stateRoot, r.id)
		if err != nil {
			r.logf("no end lock (%v); going on without it", err)
			return nil, Result{}, true
		}
		if !held {
			return f, Result{}, true
		}
		if !r.wait(EndPoll) {
			return nil, r.finish(Result{Outcome: OutcomeSignalled}), false
		}
		switch st, _ := r.inspect(); st {
		case stateGone:
			r.logf("another helper's container is gone; exiting")
			return nil, Result{Outcome: OutcomeLostLock}, false
		case statePaused:
			return nil, r.finish(Result{Outcome: OutcomePaused}), false
		}
	}
}

// TryEndLock makes one attempt at container id's end.lock (one ender per
// container, CS-LNCH-208; the restart takes it too, CS-LNCH-218): the state
// root, its clients dir and the container's dir made owned (0700), the lock
// file opened O_NOFOLLOW|O_CLOEXEC, one LOCK_EX|LOCK_NB. It returns the
// locked file (closing it releases the lock); held when another process
// holds it; an error when the lock cannot be used at all.
func TryEndLock(stateRoot, id string) (f *os.File, held bool, err error) {
	dir := clients.ContainerDir(stateRoot, id)
	for _, d := range []string{stateRoot, hostdirs.ClientsDir(stateRoot), dir} {
		if err := hostdirs.EnsureOwnedDir(d, hostdirs.OwnedDirMode, nil); err != nil {
			return nil, false, err
		}
	}
	f, err = os.OpenFile(filepath.Join(dir, clients.EndLockName), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, false, err
	}
	switch err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); {
	case err == nil:
		return f, false, nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		f.Close()
		return nil, true, nil
	default:
		f.Close()
		return nil, false, err
	}
}

// poll is the wait for the turn and the stop (CS-LNCH-208/209).
func (r *run) poll() Result {
	var (
		waited    time.Duration
		noneSince time.Time
		failures  int
		capped    bool
	)
	for first := true; ; first = false {
		// capped is this poll's claim only: a later poll that ends on an
		// idle verdict is never logged as ended at the cap.
		capped = false
		if !first && !r.wait(EndPoll) {
			return r.finish(Result{Outcome: OutcomeSignalled, Waited: waited})
		}
		switch st, err := r.inspect(); st {
		case stateGone:
			return r.finish(Result{Outcome: OutcomeEndedOnItsOwn, Waited: waited})
		case statePaused:
			return r.finish(Result{Outcome: OutcomePaused, Waited: waited})
		case stateUnknown:
			r.logf("inspect: cannot tell (%v)", err)
			continue
		}
		pres := r.present()
		if pres.CannotTell() {
			r.logf("clients: cannot tell (%v)", pres.Err)
			return r.finish(Result{Outcome: OutcomeClientsUnknown, Waited: waited})
		}
		switch r.clientsVerdict(pres) {
		case clientsReattached:
			return r.finish(Result{Outcome: OutcomeReattached, Waited: waited})
		case clientsHold:
			continue
		}
		v, _ := r.turn.activity()
		switch v {
		case VerdictUnreadable:
			return r.finish(Result{Outcome: OutcomeRegistry, Waited: waited})
		case VerdictNone:
			now := r.clock.Now()
			if noneSince.IsZero() {
				noneSince = now
			}
			if now.Sub(noneSince) >= RecordGrace {
				return r.finish(Result{Outcome: OutcomeNoRecord, Waited: waited})
			}
			continue
		case VerdictWait:
			noneSince = time.Time{}
			if !first {
				waited += EndPoll
			}
			if waited < TurnMax {
				continue
			}
			capped = true
		case VerdictEnd:
			noneSince = time.Time{}
		}

		// The re-check right before the stop (02 § 4 step 3, 04 M2): only a
		// present container with no client and the same verdict proceeds.
		if r.guardsKeep() {
			return r.finish(Result{Outcome: OutcomeUnitStopping, Waited: waited})
		}
		if r.signalled() {
			return r.finish(Result{Outcome: OutcomeSignalled, Waited: waited})
		}
		switch st, _ := r.inspect(); st {
		case stateGone:
			return r.finish(Result{Outcome: OutcomeEndedOnItsOwn, Waited: waited})
		case statePresent:
		default:
			continue
		}
		if p := r.present(); p.CannotTell() || len(p.Clients) > 0 {
			continue
		}
		v2, _ := r.turn.activity()
		if !(v2 == VerdictEnd || (capped && v2 == VerdictWait)) {
			continue
		}
		switch r.stop() {
		case stopDone:
			return r.finish(Result{Outcome: OutcomeEnded, Capped: capped && v2 == VerdictWait, Waited: waited})
		case stopGone:
			return r.finish(Result{Outcome: OutcomeEndedOnItsOwn, Waited: waited})
		default:
			failures++
			if failures >= 2 {
				return r.finish(Result{Outcome: OutcomeStopFailed, Waited: waited})
			}
		}
	}
}

// wait waits d, or less when the helper is signalled (false).
func (r *run) wait(d time.Duration) bool {
	if r.signalled() {
		return false
	}
	select {
	case <-r.o.Signals:
		return false
	case <-r.clock.After(d):
	}
	return !r.signalled()
}

func (r *run) signalled() bool {
	select {
	case <-r.o.Signals:
		return true
	default:
		return false
	}
}

// container states as the helper reads them (04 M2).
type containerState int

const (
	statePresent containerState = iota
	stateGone
	statePaused
	stateUnknown
)

func (r *run) inspect() (containerState, error) {
	c, err := sessions.InspectContainer(tmuxpane.BoundedRunner{R: r.o.Runner, Timeout: InspectBound}, r.id)
	if errors.Is(err, sessions.ErrNoSuchContainer) {
		return stateGone, nil
	}
	if err != nil {
		return stateUnknown, err
	}
	return StateOf(c.State), nil
}

// StateOf classifies an inspected docker state (04 M2): exited, dead and
// removing are gone (an --rm container after its die), paused is paused,
// running, created and restarting are present, anything else cannot tell.
func StateOf(state string) containerState {
	switch state {
	case "exited", "dead", "removing":
		return stateGone
	case "paused":
		return statePaused
	case "running", "created", "restarting":
		return statePresent
	}
	return stateUnknown
}

// Gone reports whether an InspectContainer answer means the container is
// gone (an attach or join to it prints the ended-session notice, CS-LNCH-212).
func Gone(c sessions.Container, err error) bool {
	if errors.Is(err, sessions.ErrNoSuchContainer) {
		return true
	}
	return err == nil && StateOf(c.State) == stateGone
}

func (r *run) present() clients.Presence {
	t := clients.Target{ID: r.id, Name: r.o.Mark.Container}
	// Only this container is passed for docker's unique-prefix rule: a
	// prefix another container shares then still counts here, which can
	// only keep the container longer (conservative).
	return clients.Present(clients.Options{StateRoot: r.o.StateRoot, ProcRoot: procRootOf(r.o.ProcRoot), UID: r.o.UID}, t, []clients.Target{t})
}

type clientsAnswer int

const (
	clientsNone clientsAnswer = iota
	clientsHold
	clientsReattached
)

// clientsVerdict applies the reattach rule (03 § 3) and the join wait.
func (r *run) clientsVerdict(p clients.Presence) clientsAnswer {
	now := r.clock.Now()
	hold := len(p.Joins()) > 0
	seen := map[string]bool{}
	for _, c := range p.Terminal() {
		if r.isNew(c) {
			r.logf("a terminal client started after the hang-up (%s)", describe(c))
			return clientsReattached
		}
		key := c.Lease
		if c.Proc != nil {
			key = "pid:" + strconv.Itoa(c.Proc.PID)
		}
		seen[key] = true
		first, ok := r.seenOld[key]
		if !ok {
			r.seenOld[key] = now
			hold = true
			continue
		}
		if now.Sub(first) >= ReattachSpan {
			r.logf("a terminal client stayed (%s)", describe(c))
			return clientsReattached
		}
		hold = true
	}
	for k := range r.seenOld {
		if !seen[k] {
			delete(r.seenOld, k)
		}
	}
	if hold {
		return clientsHold
	}
	return clientsNone
}

func describe(c clients.Client) string {
	if c.Proc != nil {
		return fmt.Sprintf("pid %d", c.Proc.PID)
	}
	return "lease " + filepath.Base(c.Lease)
}

// isNew reports whether a terminal client started after the hang-up.
func (r *run) isNew(c clients.Client) bool {
	switch c.Source {
	case clients.SourceLease:
		return !c.Since.IsZero() && c.Since.UnixMilli() >= r.o.HangupAt.UnixMilli()
	case clients.SourceScan:
		if c.Proc == nil {
			return false
		}
		started := c.Proc.Started(r.boot)
		return !started.IsZero() && !started.Before(r.o.HangupAt.Add(time.Second))
	}
	return false
}

// guardsKeep is the unit and system guard (03 § 1.3, 04 M1).
func (r *run) guardsKeep() bool {
	if r.unitStopping() {
		r.logf("its systemd unit is stopping")
		return true
	}
	if tmuxpane.SystemStopping(r.o.Runner) {
		r.logf("the host is shutting down")
		return true
	}
	return false
}

var userUnit = regexp.MustCompile(`/user@[0-9]+\.service/`)

// Unit reads a /proc/self/cgroup's v2 line and returns the helper's unit:
// the last path component ending in .service or .scope; user is true for a
// unit under a user manager. "" for none.
func Unit(cgroup string) (unit string, user bool) {
	for _, line := range strings.Split(cgroup, "\n") {
		path, ok := strings.CutPrefix(strings.TrimSpace(line), "0::")
		if !ok {
			continue
		}
		parts := strings.Split(path, "/")
		for i := len(parts) - 1; i >= 0; i-- {
			if strings.HasSuffix(parts[i], ".service") || strings.HasSuffix(parts[i], ".scope") {
				return parts[i], userUnit.MatchString(path + "/")
			}
		}
		return "", false
	}
	return "", false
}

func (r *run) unitStopping() bool {
	unit, user := Unit(string(readSmall(filepath.Join(procRootOf(r.o.ProcRoot), "self", "cgroup"), 64<<10)))
	if unit == "" {
		return false
	}
	args := []string{"show", "-p", "ActiveState", "--value", unit}
	if user {
		args = append([]string{"--user"}, args...)
	}
	out, err := tmuxpane.BoundedRunner{R: r.o.Runner, Timeout: UnitBound}.Output(execx.Cmd{Name: "systemctl", Args: args})
	if err != nil {
		return false
	}
	switch strings.TrimSpace(out) {
	case "deactivating", "inactive", "failed":
		return true
	}
	return false
}

type stopAnswer int

const (
	stopDone stopAnswer = iota
	stopGone
	stopFailed
)

// StopArgs is the stop's argv (02 § 4 step 4).
func StopArgs(id string) []string {
	return []string{"stop", "--signal", StopSignal, "-t", strconv.Itoa(StopGrace), id}
}

func (r *run) stop() stopAnswer {
	var stderr bytes.Buffer
	err := tmuxpane.BoundedRunner{R: r.o.Runner, Timeout: StopBound}.Run(execx.Cmd{Name: "docker", Args: StopArgs(r.id), Stderr: &stderr})
	if err == nil {
		r.logf("stopped %s", r.id)
		return stopDone
	}
	if e := strings.ToLower(stderr.String() + " " + err.Error()); strings.Contains(e, "no such container") {
		return stopGone
	}
	r.logf("docker stop failed: %v %s", err, strings.TrimSpace(stderr.String()))
	return stopFailed
}

func (r *run) logf(format string, a ...any) {
	if r.o.Log == nil {
		return
	}
	fmt.Fprintf(r.o.Log, "%s %s: %s\n", r.clock.Now().UTC().Format(time.RFC3339), r.o.Mark.Container, fmt.Sprintf(format, a...))
}

// checkStateRoot refuses an empty state root, and under go test the real one
// (clients.IsRealStateRoot, CS-DIR-007).
func checkStateRoot(root string) string {
	if root == "" {
		if testing.Testing() {
			panic("sessionend: an empty state root under go test would be the real one; pass a scratch directory")
		}
		panic("sessionend: empty state root")
	}
	if testing.Testing() && clients.IsRealStateRoot(root) {
		panic("sessionend: a test would use the real state root " + root + "; pass a scratch directory")
	}
	return root
}

func procRootOf(p string) string {
	if p != "" {
		return p
	}
	if testing.Testing() {
		panic("sessionend: ProcRoot unset under go test (would read the real /proc); pass a scratch directory")
	}
	return "/proc"
}

// readSmall reads at most limit bytes, never following a link or blocking on
// a FIFO.
func readSmall(p string, limit int64) []byte {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, limit))
	return b
}

// ownDomain is the helper's own pid domain as Claude Code writes it
// ("linux:<machine-id>:<pid ns link>"): a host claude's record in an
// unbridged registry dir is never the container's. "" when the namespace
// link cannot be read (then nothing is skipped by domain).
func ownDomain(procRoot, machineIDPath string) string {
	ns, err := os.Readlink(filepath.Join(procRootOf(procRoot), "self", "ns", "pid"))
	if err != nil {
		return ""
	}
	p := machineIDPath
	if p == "" {
		if testing.Testing() {
			panic("sessionend: MachineIDPath unset under go test (would read the real /etc/machine-id)")
		}
		p = "/etc/machine-id"
	}
	b, _ := os.ReadFile(p)
	return "linux:" + registry.JSTrim(string(b)) + ":" + ns
}
