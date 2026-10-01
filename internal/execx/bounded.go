package execx

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
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
// launcher (DieWithParent), and SIGKILLs the whole group after timeout. It is
// the bounded-call helper of the tmux pane mark and save hook
// (CS-TMUX-016/032/040) and, through Git, of the launch-path git calls
// (CS-LNCH-176); tmuxpane.BoundedRunner and the resume guard's docker top
// keep their own loops (stderr captured until exit; a shorter kill grace).
// c.Stdout is replaced by a buffer whose content is returned whatever the
// exit status; a nil c.Stderr is /dev/null.
//
// The error is nil on success; a *StartError when c could not be started; a
// *TimeoutError (errors.Is ErrTimedOut) when it was killed at its bound, or
// when timeout is not positive (nothing is started: the deadline has passed),
// with "" as output; otherwise the exit status from Wait.
func Bounded(r Runner, timeout time.Duration, c Cmd) (string, error) {
	return bounded(r, timeout, 0, c)
}

// GroupSignaler is a Process that leads its own process group and can
// signal all of it. Fake processes do not implement it.
type GroupSignaler interface {
	SignalGroup(sig syscall.Signal) error
}

// bounded is Bounded with a termGrace: when positive, the group gets SIGTERM
// at the bound and SIGKILL only if it is still there termGrace later (git
// removes its lock files on SIGTERM, not on SIGKILL).
func bounded(r Runner, timeout, termGrace time.Duration, c Cmd) (string, error) {
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
		if termGrace > 0 {
			if g, ok := proc.(GroupSignaler); ok {
				g.SignalGroup(syscall.SIGTERM)
			} else {
				proc.Signal(syscall.SIGTERM)
			}
			select {
			case <-done:
				return "", &TimeoutError{Command: line, After: timeout}
			case <-time.After(termGrace):
			}
		}
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

// GitTermGrace is how long a git killed at GitTimeout gets between SIGTERM
// and SIGKILL: git's signal handler removes its lock files (index.lock) on
// SIGTERM, which SIGKILL would leave behind.
const GitTermGrace = 500 * time.Millisecond

// GitSafeArgs come before every launch-path git command (CS-LNCH-177): the
// repository's .git/config is session-writable, and these keys make git run
// programs from it. core.fsmonitor runs a hook on any index read
// (describe --dirty, ls-files, check-ignore without --no-index);
// core.hooksPath=/dev/null finds no hook, so the post-index-change hook an
// index refresh runs (describe --dirty) never starts; --no-optional-locks
// keeps describe from rewriting the index. Filter drivers (clean/process,
// run by an index refresh that must re-hash a file) are named per
// repository, so GitFilterOverrides blanks them for the one call that
// refreshes, the version stamp's describe.
var GitSafeArgs = []string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "--no-optional-locks"}

// GitSafePrefix is "git" plus GitSafeArgs, as a recorded command line starts
// (a test pins the two together).
const GitSafePrefix = "git -c core.fsmonitor=false -c core.hooksPath=/dev/null --no-optional-locks"

// GitFilterOverrides returns the -c pairs that blank every named filter
// driver (clean, smudge, process; required off), so no driver program runs;
// ok is false when a name cannot be passed on a -c (it holds '=' or a
// newline), and the caller must not run the command.
func GitFilterOverrides(names []string) (args []string, ok bool) {
	for _, n := range names {
		if strings.ContainsAny(n, "=\n\x00") {
			return nil, false
		}
		for _, k := range []string{"clean", "smudge", "process"} {
			args = append(args, "-c", "filter."+n+"."+k+"=")
		}
		args = append(args, "-c", "filter."+n+".required=false")
	}
	return args, true
}

// boundOverrider is implemented by test runners (Fake) to shorten
// GitTimeout, so a test of a hung git need not wait the real bound.
type boundOverrider interface {
	gitTimeout() time.Duration
}

// Git runs one git command under GitTimeout (CS-LNCH-176), SIGTERM then
// SIGKILL at the bound, with GitSafeArgs before c.Args (CS-LNCH-177): its
// stdout, and Bounded's error. A timeout's message names the command as the
// caller wrote it.
func Git(r Runner, c Cmd) (string, error) {
	t := GitTimeout
	grace := GitTermGrace
	if o, ok := r.(boundOverrider); ok {
		if d := o.gitTimeout(); d > 0 {
			t, grace = d, d
		}
	}
	orig := strings.TrimSpace(c.Name + " " + strings.Join(c.Args, " "))
	c.Args = append(append([]string(nil), GitSafeArgs...), c.Args...)
	out, err := bounded(r, t, grace, c)
	var te *TimeoutError
	if errors.As(err, &te) {
		te.Command = orig
	}
	return out, err
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
