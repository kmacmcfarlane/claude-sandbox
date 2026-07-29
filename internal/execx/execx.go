// Package execx is the single seam through which claude-sandbox runs external
// commands (docker, git, claude, node). Tests inject a recording fake; the
// real implementation shells out.
package execx

import (
	"errors"
	"io"
	"os"
	"os/exec"
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
	// Exec replaces the current process with c (docker run hand-off).
	Exec(c Cmd) error
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

func (s System) Exec(c Cmd) error {
	path, err := exec.LookPath(c.Name)
	if err != nil {
		return err
	}
	env := os.Environ()
	env = append(env, c.Env...)
	return syscall.Exec(path, append([]string{c.Name}, c.Args...), env)
}
