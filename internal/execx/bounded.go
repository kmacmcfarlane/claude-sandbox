package execx

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// GitTimeout bounds every git subprocess the launcher runs on the launch path
// (CS-LNCH-176, CS-LAY-024, CS-IMG-076): git reads .git/config, HEAD, the
// commondir file, info/exclude and every .gitignore with blocking opens, all
// of them session-writable, so a FIFO planted there would otherwise hang the
// next launch forever. Every bounded git call is a local metadata lookup
// that answers in milliseconds; 5 s leaves a wide margin for a cold page
// cache, a large index or a network filesystem, while a launch with every
// call stuck still ends in well under a minute.
const GitTimeout = 5 * time.Second

// ErrTimedOut is matched (errors.Is) by the error Bounded returns when the
// command did not finish within its bound.
var ErrTimedOut = errors.New("timed out")

// TimeoutError is Bounded's error for a command it killed at its bound.
type TimeoutError struct {
	// Command is the command line, "name arg...".
	Command string
	After   time.Duration
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("%s did not finish within %s", e.Command, e.After)
}

// Is makes errors.Is(err, ErrTimedOut) true.
func (e *TimeoutError) Is(target error) bool { return target == ErrTimedOut }

// StartError is Bounded's error for a command that could not be started
// (not installed, not executable).
type StartError struct{ Err error }

func (e *StartError) Error() string { return e.Err.Error() }
func (e *StartError) Unwrap() error { return e.Err }

// Bounded runs c through r.Start in its own process group, killed with the
// launcher (DieWithParent), and kills the whole group after timeout — the one
// bounded-call helper, shared by the tmux pane mark and save hook
// (CS-TMUX-016/032/040) and the launch-path git calls (CS-LNCH-176). c.Stdout
// is replaced by a buffer whose content is returned whatever the exit status;
// a nil c.Stderr is /dev/null.
//
// The error is nil on success; a *StartError when c could not be started; a
// *TimeoutError (errors.Is ErrTimedOut) when it was killed at its bound, or
// when timeout is not positive (nothing is started: the deadline has passed),
// with "" as output; otherwise the exit status from Wait.
func Bounded(r Runner, timeout time.Duration, c Cmd) (string, error) {
	line := strings.TrimSpace(c.Name + " " + strings.Join(c.Args, " "))
	if timeout <= 0 {
		return "", &TimeoutError{Command: line, After: 0}
	}
	var buf syncBuffer
	c.Stdout = &buf
	c.DieWithParent = true
	proc, err := r.Start(c)
	if err != nil {
		return "", &StartError{Err: err}
	}
	done := make(chan error, 1)
	go func() { done <- proc.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case werr := <-done:
		return buf.String(), werr
	case <-timer.C:
		// The whole group: a grandchild holding the stdout pipe would
		// otherwise keep Wait (and so this call) waiting (CS-TMUX-040).
		if g, ok := proc.(GroupKiller); ok {
			g.KillGroup()
		} else {
			proc.Signal(os.Kill)
		}
		select {
		case <-done:
		case <-time.After(timeout):
		}
		return "", &TimeoutError{Command: line, After: timeout}
	}
}

// boundOverrider is implemented by test runners (Fake) to shorten
// GitTimeout, so a test of a hung git need not wait the real bound.
type boundOverrider interface {
	gitTimeout() time.Duration
}

// Git runs one git command with Bounded under GitTimeout (CS-LNCH-176): its
// stdout, and Bounded's error.
func Git(r Runner, c Cmd) (string, error) {
	t := GitTimeout
	if o, ok := r.(boundOverrider); ok {
		if d := o.gitTimeout(); d > 0 {
			t = d
		}
	}
	return Bounded(r, t, c)
}

// GitTimeoutWarning is the one warning line for a launch-path git call killed
// at its bound (CS-LNCH-176, CS-LAY-024, CS-IMG-076): what timed out, the
// usual cause, and what the launcher does instead.
func GitTimeoutWarning(err error, instead string) string {
	return fmt.Sprintf("WARNING: %v (a FIFO or other blocking file in its .git or a .gitignore can do this); %s.", err, instead)
}

// syncBuffer is a bytes.Buffer safe for the writer goroutine exec starts and
// the reader that returns its content.
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
