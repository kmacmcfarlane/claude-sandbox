// Package execx is the single seam through which claude-sandbox runs external
// commands (docker, git, claude, node). Tests inject a recording fake; the
// real implementation shells out.
package execx

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
)

// Cmd describes one external command invocation.
type Cmd struct {
	Name   string
	Args   []string
	Dir    string
	Env    []string // extra KEY=VAL entries appended to the process env
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// DieWithParent asks Start to kill the process when the launcher dies,
	// even by SIGKILL (Linux parent-death signal; CS-LNCH-098). Start keeps
	// its own process group either way, so terminal signals never reach it.
	DieWithParent bool
}

// Process is a started command that can be signalled and waited on.
type Process interface {
	Signal(sig os.Signal) error
	Wait() error
	Pid() int
}

// Runner runs external commands.
type Runner interface {
	// Run starts c and waits. A non-zero exit is returned as an error
	// carrying the exit code (see ExitCode).
	Run(c Cmd) error
	// Output runs c and returns its captured stdout.
	Output(c Cmd) (string, error)
	// Start launches c without waiting.
	Start(c Cmd) (Process, error)
	// RunSession runs c as the interactive session child the launcher waits
	// on (docker start/attach/exec, CS-LNCH-085), with the session signal
	// policy of CS-LNCH-086. It returns when the child exits, but its signal
	// handlers stay installed until the result's Release is called, so the
	// caller can finish its report without a signal killing it (CS-LNCH-097).
	// The error is non-nil only when c could not be started; the child's own
	// outcome is in the result.
	RunSession(c Cmd) (SessionResult, error)
}

// SessionResult is how a session child ended.
type SessionResult struct {
	// Code is the child's exit status, 128+n when it died of signal n — the
	// convention a shell uses, so the launcher can pass it through unchanged.
	Code int
	// Forwarded is the signal the launcher received and forwarded to the
	// child (SIGTERM or SIGHUP), nil when the session ended on its own. A
	// signal-initiated exit is the caller's to end quietly (CS-LNCH-091).
	Forwarded os.Signal
	// Late delivers the signals the launcher receives after the child has
	// exited and before Release (CS-LNCH-097); nil when none can arrive.
	Late <-chan os.Signal
	// Release restores the launcher's signal dispositions. Nil-safe via
	// SessionResult.Done.
	Release func()
}

// Done releases the session's signal handlers. Safe to call on a zero result
// and more than once.
func (r SessionResult) Done() {
	if r.Release != nil {
		r.Release()
	}
}

// ExitCode extracts the exit status from an error returned by Run/Wait.
// Returns 0 for nil, -1 for non-exit errors.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	var ce *CodeError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return -1
}

// CodeError is an error carrying an explicit exit code (used by fakes and
// pipeline results).
type CodeError struct {
	Code int
	Msg  string
}

func (e *CodeError) Error() string { return e.Msg }

// System is the real Runner.
type System struct{}

func (System) build(c Cmd) *exec.Cmd {
	cmd := exec.Command(c.Name, c.Args...)
	cmd.Dir = c.Dir
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	cmd.Stdin = c.Stdin
	cmd.Stdout = c.Stdout
	cmd.Stderr = c.Stderr
	return cmd
}

func (s System) Run(c Cmd) error { return s.build(c).Run() }

func (s System) Output(c Cmd) (string, error) {
	cmd := s.build(c)
	cmd.Stdout = nil
	out, err := cmd.Output()
	return string(out), err
}

type sysProcess struct{ cmd *exec.Cmd }

func (p *sysProcess) Signal(sig os.Signal) error { return p.cmd.Process.Signal(sig) }
func (p *sysProcess) Wait() error                { return p.cmd.Wait() }
func (p *sysProcess) Pid() int                   { return p.cmd.Process.Pid }

func (s System) Start(c Cmd) (Process, error) {
	if c.DieWithParent {
		return s.startTethered(c)
	}
	cmd := s.build(c)
	// Own process group so the whole tree can be signalled together.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &sysProcess{cmd: cmd}, nil
}

// tetheredProcess is a process started with a parent-death signal; its Wait
// runs on the goroutine that holds the forking OS thread.
type tetheredProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

func (p *tetheredProcess) Signal(sig os.Signal) error { return p.cmd.Process.Signal(sig) }
func (p *tetheredProcess) Wait() error                { <-p.done; return p.err }
func (p *tetheredProcess) Pid() int                   { return p.cmd.Process.Pid }

// startTethered starts c in its own process group with a parent-death
// signal. Linux delivers that signal when the forking THREAD exits, so one
// goroutine locks its thread, forks, and waits, keeping the thread alive for
// the child's whole life.
func (s System) startTethered(c Cmd) (Process, error) {
	cmd := s.build(c)
	cmd.SysProcAttr = tetherAttr()
	p := &tetheredProcess{cmd: cmd, done: make(chan struct{})}
	started := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := cmd.Start(); err != nil {
			started <- err
			return
		}
		started <- nil
		p.err = cmd.Wait()
		close(p.done)
	}()
	if err := <-started; err != nil {
		return nil, err
	}
	return p, nil
}

// sessionSignals are the signals RunSession handles.
var sessionSignals = []os.Signal{syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT}

// inForeground reports whether the launcher's process group is the
// foreground group of its controlling terminal — the case in which the
// terminal already delivered a keyboard SIGINT/SIGQUIT to docker (same
// group). A seam for tests.
var inForeground = foregroundOfTTY

// RunSession starts c as a child in the launcher's process group — docker
// does not get a group of its own, so the terminal keeps delivering keys and
// job-control signals to it directly — and waits for it (CS-LNCH-085/086).
//
//   - SIGTERM and SIGHUP are forwarded: sent to the launcher's pid alone (an
//     SDK client stopping its session, CS-LNCH-091), they would otherwise
//     never reach docker. A group-wide one reaches docker twice; docker
//     proxies both, which the container's init absorbs.
//   - SIGINT and SIGQUIT are dropped when the launcher is the terminal's
//     foreground group: the terminal sent them to the whole group, docker
//     included. Otherwise — no terminal, or a background launcher — they
//     were sent to the launcher alone (kill -INT <pid>, a supervisor's
//     interrupt) and are forwarded. They are caught, never SIG_IGN'd: an
//     ignored disposition survives exec, and docker would inherit it.
//   - A signal the launcher inherited as ignored (nohup's SIGHUP, a
//     background job's SIGINT) stays ignored: it is not caught at all.
//   - Pdeathsig SIGKILL, with the forking OS thread locked for the child's
//     lifetime (Linux delivers it when that THREAD exits, not the process):
//     a launcher killed outright takes the docker client with it, exactly
//     as killing the exec'd client used to.
//   - The handlers stay installed after the child exits, until Release:
//     signals then arrive on Late instead of killing the launcher mid-report
//     (CS-LNCH-097).
func (s System) RunSession(c Cmd) (SessionResult, error) {
	cmd := s.build(c)
	// exec.Cmd reads a nil stream as /dev/null; a session needs the terminal.
	if cmd.Stdin == nil {
		cmd.Stdin = os.Stdin
	}
	if cmd.Stdout == nil {
		cmd.Stdout = os.Stdout
	}
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}
	cmd.SysProcAttr = sessionAttr()

	// Installed before the fork, so no signal can land in between with the
	// default action and kill the launcher before it can report.
	var handled []os.Signal
	for _, sig := range sessionSignals {
		if !signal.Ignored(sig) {
			handled = append(handled, sig)
		}
	}
	sigs := make(chan os.Signal, 8)
	if len(handled) > 0 {
		signal.Notify(sigs, handled...)
	}
	var once sync.Once
	release := func() { once.Do(func() { signal.Stop(sigs) }) }

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := cmd.Start(); err != nil {
		release()
		return SessionResult{Code: -1}, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	res := SessionResult{Late: sigs, Release: release}
	for {
		select {
		case sig := <-sigs:
			switch sig {
			case syscall.SIGTERM, syscall.SIGHUP:
				res.Forwarded = sig
				cmd.Process.Signal(sig)
			case syscall.SIGINT, syscall.SIGQUIT:
				if !inForeground() {
					cmd.Process.Signal(sig)
				}
			}
		case <-done:
			res.Code = statusCode(cmd.ProcessState)
			return res, nil
		}
	}
}

// statusCode renders a finished process's status as a shell would: the exit
// code, or 128+n for death by signal n.
func statusCode(ps *os.ProcessState) int {
	if ps == nil {
		return -1
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ps.ExitCode()
}
