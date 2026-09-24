package ralphloop

import (
	"bytes"

	"fmt"
	"github.com/kmacmcfarlane/claude-sandbox/internal/pidslot"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// pipelineTracker lets an interrupt handler terminate the in-flight
// iteration (CS-RLP-017).
type pipelineTracker struct {
	mu    sync.Mutex
	procs []*exec.Cmd
}

func (t *pipelineTracker) set(procs []*exec.Cmd) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.procs = procs
}

func (t *pipelineTracker) signal(sig syscall.Signal) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, c := range t.procs {
		if c.Process != nil {
			// Negative pid: whole process group.
			syscall.Kill(-c.Process.Pid, sig)
		}
	}
}

var tracker pipelineTracker

// reserveClass advances the pid counter so the next fork lands on the class
// (CS-PID-006); ReserveClass overrides it under test.
func (l *Loop) reserveClass() {
	if l.ReserveClass != nil {
		l.ReserveClass()
		return
	}
	pidslot.BurnFromEnv(pidslot.Real())
}

// Terminate TERMs the current iteration's process tree (interrupt path).
func Terminate() { tracker.signal(syscall.SIGTERM) }

// runIterationReal launches claude piped through the logstream stages
// (CS-RLP-011..015) and returns the pipeline exit code (pipefail semantics,
// 124 on hard timeout).
func (l *Loop) runIterationReal(iter int, resume bool) int {
	l.claudeExit = 0
	l.timedOut = false
	promptBytes, err := l.promptData()
	if err != nil {
		fmt.Fprintln(l.Err, err)
		return 1
	}
	args := l.claudeArgs(resume)
	fmt.Fprintf(l.Out, "Launching claude %s\n", strings.Join(args, " "))

	stderrF, err := os.Create(l.StderrFile)
	if err != nil {
		fmt.Fprintln(l.Err, err)
		return 1
	}
	defer stderrF.Close()

	rawLog := fmt.Sprintf("%s_%s_iter%d", l.RawLogBase, time.Now().Format("20060102150405"), iter)
	ls := filepath.Join(l.RepoRoot, "logstream")

	cmds := []*exec.Cmd{exec.Command(l.ClaudeBin, args...)}
	if !l.Interactive {
		stages := [][]string{
			{"node", filepath.Join(ls, "raw-json-logger.js"), "--out", rawLog},
			{"node", filepath.Join(ls, "run-logger.js"), "--log-file", l.RunlogFile,
				"--iteration", strconv.Itoa(iter), "--quota-status-file", l.QuotaFile},
			{"node", filepath.Join(ls, "exit-on-result.js")},
		}
		if l.watchdogMinutes() > 0 {
			stages = append(stages, []string{"node", filepath.Join(ls, "activity-watchdog.js"),
				"--timeout", strconv.Itoa(l.watchdogMinutes()), "--marker-file", l.MarkerFile})
		}
		stages = append(stages, []string{"node", filepath.Join(ls, "console-output.js")})
		for _, s := range stages {
			cmds = append(cmds, exec.Command(s[0], s[1:]...))
		}
	}

	// Wire the pipe chain: prompt -> claude -> stages... -> l.Out.
	cmds[0].Stdin = bytes.NewReader(promptBytes)
	// CS-RLP-020: only claude needs the project root; the node stages get
	// their paths as flags.
	cmds[0].Env = append(os.Environ(), l.childEnv()...)
	for i := 0; i < len(cmds)-1; i++ {
		pipe, perr := cmds[i].StdoutPipe()
		if perr != nil {
			fmt.Fprintln(l.Err, perr)
			return 1
		}
		cmds[i+1].Stdin = pipe
	}
	cmds[len(cmds)-1].Stdout = l.Out
	for _, c := range cmds {
		c.Stderr = stderrF
		c.Dir = l.WorkDir
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}

	// CS-PID-006: land claude on the container's pid class. The loop is the
	// parent, so its own fork is the landing fork.
	l.reserveClass()
	for i, c := range cmds {
		if serr := c.Start(); serr != nil {
			fmt.Fprintln(l.Err, serr)
			for _, prev := range cmds[:i] {
				syscall.Kill(-prev.Process.Pid, syscall.SIGKILL)
				prev.Wait()
			}
			return 1
		}
	}
	tracker.set(cmds)
	defer tracker.set(nil)

	// Hard iteration timeout: TERM the tree, KILL after the grace
	// (CS-RLP-015). The guard's state machine makes the timer and the
	// pipeline's end mutually exclusive (CS-RLP-031): whichever wins the
	// transition out of "running" decides both whether TERM is sent and how
	// the iteration is classified.
	grace := l.killGrace
	if grace == 0 {
		grace = 30 * time.Second
	}
	guard := newTimeoutGuard(grace,
		func() { tracker.signal(syscall.SIGTERM) },
		func() { tracker.signal(syscall.SIGKILL) })
	timer := time.AfterFunc(time.Duration(l.IterationTimeout)*time.Second, guard.fire)
	defer func() {
		timer.Stop()
		guard.finish() // idempotent; covers an early exit
	}()

	// pipefail: the rightmost non-zero exit wins.
	rc := 0
	for _, c := range cmds {
		if werr := c.Wait(); werr != nil {
			code := 1
			if ee, ok := werr.(*exec.ExitError); ok {
				code = ee.ExitCode()
				if code < 0 {
					code = 143 // killed by signal
				}
			}
			rc = code
		}
	}
	// Settle the race first: from here on the timer's callback is a no-op
	// (and a TERM it was sending has completed), and the classification
	// reads the winner, never a flag the timer may still be about to set.
	timedOut := guard.finish()
	timer.Stop()
	if l.afterPipeline != nil {
		l.afterPipeline(guard.fire)
	}
	// CS-RLP-023: claude's own status for OOM classification; a signal
	// death is 128+N (SIGKILL -> 137) where ExitCode() would say -1.
	l.claudeExit = claudeExitStatus(cmds[0])
	if timedOut {
		l.timedOut = true
		return 124
	}
	return rc
}

// Timeout guard states (CS-RLP-031): running -> done (the pipeline finished
// first) or running -> timedOut (the timer fired first) -> timedOutDone.
const (
	guardRunning = iota
	guardDone
	guardTimedOut
	guardTimedOutDone
)

// timeoutGuard serializes the hard-timeout callback against the pipeline's
// end. time.Timer.Stop cannot cancel a callback that is already running, so
// the callbacks themselves check the state under the mutex and signal while
// holding it: once finish returns, no TERM or KILL from this iteration can
// still be in flight or sent later.
type timeoutGuard struct {
	mu        sync.Mutex
	state     int
	grace     time.Duration
	term      func()
	kill      func()
	killTimer *time.Timer
	// afterFunc schedules the delayed KILL; time.AfterFunc unless a test
	// replaces it.
	afterFunc func(time.Duration, func()) *time.Timer
}

func newTimeoutGuard(grace time.Duration, term, kill func()) *timeoutGuard {
	return &timeoutGuard{grace: grace, term: term, kill: kill, afterFunc: time.AfterFunc}
}

// fire is the timer callback: it TERMs and arms the KILL only when it wins
// the transition out of running.
func (g *timeoutGuard) fire() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state != guardRunning {
		return
	}
	g.state = guardTimedOut
	g.term()
	g.killTimer = g.afterFunc(g.grace, g.killNow)
}

// killNow is the delayed KILL; a no-op once the pipeline has finished.
func (g *timeoutGuard) killNow() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state != guardTimedOut {
		return
	}
	g.kill()
}

// finish records the pipeline's end, cancels a pending KILL and reports
// whether the timeout won. Idempotent.
func (g *timeoutGuard) finish() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch g.state {
	case guardRunning:
		g.state = guardDone
	case guardTimedOut:
		g.state = guardTimedOutDone
	}
	if g.killTimer != nil {
		g.killTimer.Stop()
	}
	return g.state == guardTimedOutDone
}

// claudeExitStatus is a finished command's exit status in shell terms:
// 128+N when signal N killed it.
func claudeExitStatus(c *exec.Cmd) int {
	ps := c.ProcessState
	if ps == nil {
		return 1
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ps.ExitCode()
}
