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
	// on (docker start/attach/exec, CS-LNCH-085): the terminal is inherited,
	// SIGTERM and SIGHUP are forwarded, SIGINT and SIGQUIT are swallowed by
	// the launcher (CS-LNCH-086). The error is non-nil only when c could not
	// be started; the child's own outcome is in the result.
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
	cmd := s.build(c)
	// Own process group so the whole tree can be signalled together.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &sysProcess{cmd: cmd}, nil
}

// RunSession starts c as a child in the launcher's own process group, so the
// terminal keeps delivering its keys and job-control signals to docker
// directly, and waits for it (CS-LNCH-085/086).
//
//   - SIGTERM and SIGHUP are forwarded: sent to the launcher's pid alone (an
//     SDK client stopping its session, CS-LNCH-091), they would otherwise
//     never reach docker. A group-wide one reaches docker twice; docker
//     proxies both, which the container's init absorbs.
//   - SIGINT and SIGQUIT are caught and dropped, not SIG_IGN'd: an ignored
//     disposition survives exec, and docker would inherit it. Caught ones
//     reset to the default in the child.
//   - Pdeathsig SIGKILL, with the forking OS thread locked for the child's
//     lifetime (Linux delivers it when that THREAD exits, not the process):
//     a launcher killed outright takes the docker client with it, exactly
//     as killing the exec'd client used to.
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
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT)
	defer signal.Stop(sigs)

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := cmd.Start(); err != nil {
		return SessionResult{Code: -1}, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var res SessionResult
	for {
		select {
		case sig := <-sigs:
			switch sig {
			case syscall.SIGTERM, syscall.SIGHUP:
				res.Forwarded = sig
				cmd.Process.Signal(sig)
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
