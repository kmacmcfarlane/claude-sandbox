package imagebuild

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

// DetachedCmd is a process started to outlive the launcher (CS-IMG-045).
type DetachedCmd struct {
	Path string
	Args []string
	Env  []string // KEY=VAL entries added to the launcher's environment
	Log  string   // stdout and stderr are appended here
}

// Detacher starts a DetachedCmd and returns without waiting for it.
type Detacher func(DetachedCmd) error

// StartDetached is the real Detacher. Deliberately not execx's Start with
// DieWithParent: this process must survive the launcher (which may be killed
// with its terminal) and the terminal itself, so it gets its own session
// (setsid: no controlling terminal, no SIGHUP or job-control signals from
// ours), stdin from /dev/null and the log as stdout and stderr — real fds
// handed to the child, not pipes the launcher would have to drain. It is
// never waited on; when it ends first, it is reaped with the launcher.
//
// Refused under go test: a test that forgot to inject Options.Detach would
// otherwise start the test binary itself as a background "build".
func StartDetached(c DetachedCmd) error {
	if testing.Testing() {
		return errors.New("imagebuild: StartDetached under go test; inject Options.Detach")
	}
	return startDetached(c)
}

func startDetached(c DetachedCmd) error {
	log, err := os.OpenFile(c.Log, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	null, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer null.Close()
	cmd := exec.Command(c.Path, c.Args...)
	cmd.Env = append(os.Environ(), c.Env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
