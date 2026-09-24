// Package oomreport tells a session killed by the OOM killer apart from one
// that ended on its own (CS-LNCH-085..094, CS-SESS-059/060).
//
// It cannot tell WHICH OOM killer: docker's oom event is containerd's
// TaskOOM, published whenever the container's memory.events oom_kill rises,
// and oom_kill counts kills by any OOM killer — the container's memoryLimit
// or the host's global one, which --oom-score-adj (CS-LNCH-112) points at
// sandboxes. The "oom" counter that rises only at the container's own limit
// is gone with the --rm container, so the reports name both causes.
//
// The container runs with --rm, so it is gone before an inspect after exit
// could read State.OOMKilled. The launcher therefore subscribes to the
// daemon's event stream for the container (oom and die) BEFORE it starts the
// session child, and reads what arrived once the child returns. The die event
// carries the exit code and, like every container event, the container's
// labels — so the memoryLimit and its cascade source recorded at create time
// reach the report even on the attach and join paths, which never re-resolve
// the config.
package oomreport

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
)

// Label keys the launcher writes at create time (CS-LNCH-093). They are read
// back from the event attributes, which carry every container label.
const (
	LabelMemoryLimit       = "claude-sandbox.memorylimit"
	LabelMemoryLimitSource = "claude-sandbox.memorylimitsource"
)

// SourceDefault is the recorded source when no config.yaml in the cascade
// sets memoryLimit and the launcher's built-in default applies.
const SourceDefault = "default"

// OOMExit is the status of a process killed by SIGKILL, which is how the
// kernel's OOM killer ends one.
const OOMExit = 128 + int(syscall.SIGKILL)

// DieWait bounds the wait for the die event once the session child has
// returned (CS-LNCH-088). die follows the client's return by milliseconds
// (measured: ~3 ms); a client that returned while the container keeps running
// was detached, and no die comes at all.
var DieWait = 2 * time.Second

// OOMGrace is how long to wait for an oom event not seen yet once the wait
// for die is over: the daemon publishes oom asynchronously, so a die with
// exit 137 (or a joined exec's own 137, or its normal end) can arrive first
// (CS-LNCH-088, CS-SESS-060). Far shorter than DieWait: a 137 without an oom
// (docker kill, a stop timeout) must not hold the prompt for 2 s.
var OOMGrace = 150 * time.Millisecond

// Limit is the memory limit a container was created with and where in the
// cascade it came from ("" when not recorded).
type Limit struct {
	Value  string
	Source string
}

// Outcome is what the event stream reported for one session.
type Outcome struct {
	// Died is true once a die event arrived; ExitCode is its exitCode.
	Died     bool
	ExitCode int
	// OOMKills counts the oom events: one per process the kernel killed.
	OOMKills int
	// Limit is read from the events' labels (empty on a container that
	// predates them).
	Limit Limit
}

// Watch is a running "docker events" subscription for one container.
type Watch struct {
	container string
	proc      execx.Process
	pw        *io.PipeWriter

	mu      sync.Mutex
	out     Outcome
	changed chan struct{}
	ended   chan struct{}
}

// EventsArgs is the subscription (CS-LNCH-087). --since replays anything the
// daemon published between since and the moment the subscription connects,
// so a container that dies at once is not missed. docker's container= filter
// matches names by PREFIX, so read() also checks the exact name
// (CS-LNCH-095).
func EventsArgs(container string, since time.Time) []string {
	return []string{"events",
		"--since", fmt.Sprintf("%d.%09d", since.Unix(), since.Nanosecond()),
		"--filter", "container=" + container,
		"--filter", "event=oom", "--filter", "event=die",
		"--format", "{{json .}}"}
}

// Start subscribes to container's oom and die events. It never fails: a
// subscription that cannot start yields a Watch that has seen nothing and
// whose stream has ended, so the session still runs and nothing is reported.
func Start(r execx.Runner, container string, since time.Time) *Watch {
	pr, pw := io.Pipe()
	w := &Watch{container: container, pw: pw, changed: make(chan struct{}, 1), ended: make(chan struct{})}
	// The reader runs before Start: a runner may write its output
	// synchronously, and an unread pipe would block it.
	go w.read(pr)
	// DieWithParent: a launcher killed outright must not leave the watcher
	// running forever (CS-LNCH-098). Start's own process group keeps terminal
	// signals away from it.
	proc, err := r.Start(execx.Cmd{Name: "docker", Args: EventsArgs(container, since), Stdout: pw, DieWithParent: true})
	if err != nil {
		pw.Close()
		return w
	}
	w.proc = proc
	go func() {
		proc.Wait()
		pw.Close()
	}()
	return w
}

// event is the subset of docker's JSON event this package reads.
type event struct {
	Status string `json:"status"`
	Action string `json:"Action"`
	Actor  struct {
		Attributes map[string]string `json:"Attributes"`
	} `json:"Actor"`
}

func (w *Watch) read(r io.Reader) {
	defer close(w.ended)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var e event
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		action := e.Action
		if action == "" {
			action = e.Status
		}
		// CS-LNCH-095: "container=cs-otter-2" also matches cs-otter-20.
		if e.Actor.Attributes["name"] != w.container {
			continue
		}
		w.mu.Lock()
		switch action {
		case "oom":
			w.out.OOMKills++
		case "die":
			w.out.Died = true
			w.out.ExitCode, _ = strconv.Atoi(e.Actor.Attributes["exitCode"])
		default:
			w.mu.Unlock()
			continue
		}
		if v, ok := e.Actor.Attributes[LabelMemoryLimit]; ok && v != "" {
			w.out.Limit = Limit{Value: v, Source: e.Actor.Attributes[LabelMemoryLimitSource]}
		}
		w.mu.Unlock()
		select {
		case w.changed <- struct{}{}:
		default:
		}
	}
	// Drain what is left so a writer never blocks on a reader that quit.
	io.Copy(io.Discard, r)
}

// Await waits until done reports true for what has arrived, the stream ends,
// timeout passes, or a signal arrives on stop, and returns what has arrived
// by then; stopped reports the signal (CS-LNCH-097). A nil stop never fires.
func (w *Watch) Await(timeout time.Duration, done func(Outcome) bool, stop <-chan os.Signal) (o Outcome, stopped bool) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		o = w.Snapshot()
		if done(o) {
			return o, false
		}
		select {
		case <-w.changed:
		case <-w.ended:
			return w.Snapshot(), false
		case <-timer.C:
			return w.Snapshot(), false
		case <-stop:
			return w.Snapshot(), true
		}
	}
}

// AwaitDeath is the wait of a primary session (new, attach, headless): up to
// DieWait for die, then — when the die says 137 and no oom has arrived —
// OOMGrace more for the oom that may trail it (CS-LNCH-088).
func (w *Watch) AwaitDeath(stop <-chan os.Signal) (Outcome, bool) {
	o, stopped := w.Await(DieWait, Died, stop)
	if stopped || !o.Died || o.ExitCode != OOMExit || o.OOMKills > 0 {
		return o, stopped
	}
	return w.Await(OOMGrace, SawOOM, stop)
}

// AwaitJoined is the wait of a joined session that ended with status code
// (CS-SESS-060). With 137, up to DieWait for an oom — ending early when the
// container's die arrives, then OOMGrace more for a trailing oom; otherwise
// OOMGrace for ooms still in flight.
func (w *Watch) AwaitJoined(code int, stop <-chan os.Signal) (Outcome, bool) {
	if code != OOMExit {
		return w.Await(OOMGrace, Never, stop)
	}
	o, stopped := w.Await(DieWait, SawOOMOrDied, stop)
	if stopped || o.OOMKills > 0 {
		return o, stopped
	}
	return w.Await(OOMGrace, SawOOM, stop)
}

// Snapshot returns what has arrived so far.
func (w *Watch) Snapshot() Outcome {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.out
}

// Stop ends the subscription.
func (w *Watch) Stop() {
	if w.proc != nil {
		w.proc.Signal(syscall.SIGTERM)
	}
}

// Died is the Await predicate for the container's die.
func Died(o Outcome) bool { return o.Died }

// SawOOM is the Await predicate for at least one oom.
func SawOOM(o Outcome) bool { return o.OOMKills > 0 }

// SawOOMOrDied is done at the first oom or at die.
func SawOOMOrDied(o Outcome) bool { return o.OOMKills > 0 || o.Died }

// Never is the predicate that waits out the whole timeout (a grace wait).
func Never(Outcome) bool { return false }

// Verdict classifies a finished session.
type Verdict int

const (
	// Quiet: nothing to report (a clean exit, a detach, no subscription).
	Quiet Verdict = iota
	// Killed: the OOM killer ended the session itself.
	Killed
	// Survived: the OOM killer killed something during the session, which
	// then ended some other way.
	Survived
)

// Primary classifies a primary session from its events (CS-LNCH-088..090):
// no die means the client detached from a container that is still running.
func Primary(o Outcome) Verdict {
	switch {
	case !o.Died || o.OOMKills == 0:
		return Quiet
	case o.ExitCode == OOMExit:
		return Killed
	}
	return Survived
}

// Joined classifies a joined session (CS-SESS-060). The container normally
// outlives the exec, so the exec's own status is the signal: 137 with an oom
// in the window is a kill; an oom without it is a survived one.
func Joined(code int, o Outcome) Verdict {
	switch {
	case o.OOMKills == 0:
		return Quiet
	case code == OOMExit:
		return Killed
	}
	return Survived
}

// TerminalReset undoes the terminal modes Claude Code's TUI sets, for a TUI
// that was killed before it could (CS-LNCH-092). Read from Claude Code
// 2.1.277: at startup it enables bracketed paste (2004), focus events (1004)
// and theme notifications (2031) and hides the cursor; it enables the kitty
// keyboard protocol and xterm modifyOtherKeys on terminals that support them,
// mouse tracking in its fullscreen mode, and wraps frames in synchronized
// updates (2026). Each is switched off here. Deliberately absent: leaving the
// alternate screen (?1049l restores a saved cursor) and resetting the scroll
// region (DECSTBM homes the cursor), which move the cursor on a terminal that
// never entered them; the docker client restores the termios itself.
const TerminalReset = "\x1b[?2026l" + // synchronized update off
	"\x1b[?2004l" + // bracketed paste off
	"\x1b[?1004l" + // focus events off
	"\x1b[?2031l" + // theme-change notifications off
	"\x1b[?1006l\x1b[?1003l\x1b[?1002l\x1b[?1000l" + // mouse tracking off
	"\x1b[<u" + // pop the kitty keyboard flags
	"\x1b[>4m" + // modifyOtherKeys reset
	"\x1b[0m" + // attributes reset
	"\x1b[?25h" // cursor visible

// KilledReport is the report of a session the OOM killer ended
// (CS-LNCH-089). kills is the number of oom events seen. It names both
// causes, since the events cannot tell a limit kill from a host one.
func KilledReport(kills int, lim Limit) string {
	var b strings.Builder
	fmt.Fprintf(&b, "claude-sandbox: this session was killed by the OOM killer (exit %d; %s) — %s.\n", OOMExit, plural(kills, "OOM kill"), EitherCause)
	fmt.Fprintf(&b, "  memoryLimit: %s; swap is off by design.\n", DescribeLimit(lim))
	fmt.Fprintf(&b, "  At memoryLimit: %s, or cap build/test parallelism (e.g. ginkgo --procs=N, go test -p N, make -jN).\n", remedy(lim))
	b.WriteString("  Host out of memory: run fewer sandboxes at once or cap their parallelism; see \"When the host runs out of memory\" in the claude-sandbox README.\n")
	b.WriteString("  To tell which: the host's kernel log (journalctl -k) says \"Memory cgroup out of memory\" for a limit, plain \"Out of memory\" for the host.\n")
	return b.String()
}

// EitherCause names the two things an oom event can mean (CS-LNCH-089/090,
// CS-SESS-061/063).
const EitherCause = "the container's memoryLimit or the host running out of memory"

// SurvivedReport is the one softer line for OOM kills the session outlived
// (CS-LNCH-090).
func SurvivedReport(kills int, lim Limit) string {
	return fmt.Sprintf("claude-sandbox: note: the OOM killer killed %s in this container during this session (the container's memoryLimit %s or the host running out of memory); the session itself was not killed.\n",
		plural(kills, "process"), DescribeLimit(lim))
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	if strings.HasSuffix(noun, "s") {
		return fmt.Sprintf("%d %ses", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// DescribeLimit renders the limit and its source, as the exit report and the
// attach/join note name it (CS-LNCH-089, CS-SESS-063); "not recorded on this
// container" when the create-time labels are absent.
func DescribeLimit(lim Limit) string {
	switch {
	case lim.Value == "":
		return "not recorded on this container"
	case lim.Source == SourceDefault:
		return lim.Value + " (the default; no config.yaml in the cascade sets it)"
	case lim.Source == "":
		return lim.Value
	}
	return fmt.Sprintf("%s (from %s)", lim.Value, lim.Source)
}

func remedy(lim Limit) string {
	if lim.Source == "" || lim.Source == SourceDefault {
		return "set a higher memoryLimit in .claude-sandbox/config.yaml"
	}
	return "raise memoryLimit in that file"
}
