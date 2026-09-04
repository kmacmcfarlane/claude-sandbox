// Package pidslot lands a process on a pid congruent to a launcher-assigned
// class, so sessions in separate sandbox containers never share a pid.
// Spec: spec/pidslot.feature (CS-PID).
//
// Claude Code names its peer-registry record ~/.claude/sessions/<pid>.json.
// Every sandbox container is its own pid namespace and the entrypoint execs
// claude, so it is PID 7 in all of them and the records collide. Namespaces
// stay private; instead the launcher assigns each container a class k and this
// package advances the namespace's pid counter to k-1 (mod Modulus) right
// before a FORK. exec keeps the caller's pid, so the fork is what matters: the
// helper execs tini, whose fork lands the command on the class.
package pidslot

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
)

const (
	// Modulus is the number of classes. 256 bounds the burn at ~100 ms
	// (≈0.4 ms/fork) and gives a background-forked session, whose pid is not
	// slotted, a 1/256 chance of even sharing a residue with a sibling.
	Modulus = 256
	// EnvVar carries the class from the launcher into the container; docker
	// exec inherits it, so joined sessions land on the same class.
	EnvVar = "CLAUDE_SANDBOX_PID_CLASS"
	// MaxBurn caps the loop. Reaching the residue needs at most Modulus-1
	// forks; the cap only matters when the counter is misread (CS-PID-002/003).
	MaxBurn = 2 * Modulus

	lastPIDPath = "/proc/sys/kernel/ns_last_pid"
)

// Ops are the process-level seams; Real returns the live ones.
type Ops struct {
	// ReadLastPID returns the namespace's last-allocated pid from one whole
	// read (CS-PID-002).
	ReadLastPID func() (int, error)
	// Fork consumes exactly one pid (a throwaway child that exits at once).
	Fork func() error
	// Exec replaces the process; it returns only on failure.
	Exec func(path string, argv []string) error
	// LookPath resolves a binary on PATH.
	LookPath func(string) (string, error)
	Getenv   func(string) string
	Stderr   io.Writer
}

// Real returns the operating-system implementations.
func Real() Ops {
	return Ops{
		ReadLastPID: readLastPID,
		Fork:        forkTrue,
		Exec: func(path string, argv []string) error {
			return syscall.Exec(path, argv, os.Environ())
		},
		LookPath: exec.LookPath,
		Getenv:   os.Getenv,
		Stderr:   os.Stderr,
	}
}

// readLastPID reads the counter with a single read(2): a sysctl file returns
// EOF at any non-zero offset, so byte-at-a-time readers see one digit.
func readLastPID() (int, error) {
	b, err := os.ReadFile(lastPIDPath)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

// forkTrue spawns and reaps one throwaway child.
func forkTrue() error {
	for _, p := range []string{"/bin/true", "/usr/bin/true"} {
		if _, err := os.Stat(p); err == nil {
			return exec.Command(p).Run()
		}
	}
	return errors.New("no true(1) binary to fork")
}

// ParseClass validates an EnvVar value: an integer in [0, Modulus).
func ParseClass(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	k, err := strconv.Atoi(s)
	if err != nil || k < 0 || k >= Modulus {
		return 0, false
	}
	return k, true
}

// Burn advances the counter until it is ≡ class-1 (mod Modulus) and returns
// the number of forks it took (CS-PID-001). Zero when already there.
func Burn(ops Ops, class int) (int, error) {
	want := ((class-1)%Modulus + Modulus) % Modulus
	n := 0
	for {
		last, err := ops.ReadLastPID()
		if err != nil {
			return n, fmt.Errorf("reading %s: %w", lastPIDPath, err)
		}
		if last < 0 {
			return n, fmt.Errorf("reading %s: negative value %d", lastPIDPath, last)
		}
		if last%Modulus == want {
			return n, nil
		}
		if n >= MaxBurn {
			return n, fmt.Errorf("pid counter never reached residue %d after %d forks", want, n)
		}
		if err := ops.Fork(); err != nil {
			return n, fmt.Errorf("forking a throwaway process: %w", err)
		}
		n++
	}
}

// BurnFromEnv is Burn for a parent that will fork the real child itself (the
// ralph loop, CS-PID-006). It is silent when the class is unset and warns,
// without failing, when the burn does not complete.
func BurnFromEnv(ops Ops) {
	k, ok := ParseClass(ops.Getenv(EnvVar))
	if !ok {
		return
	}
	if _, err := Burn(ops, k); err != nil {
		fmt.Fprintf(ops.Stderr, "Warning: pid class %d not applied (%v); this session may be invisible to sibling sandboxes.\n", k, err)
	}
}

// Run is the `pidslot -- <cmd...>` helper (CS-PID-001..003). It exits only
// when every exec fails; the returned code is then the process exit status.
func Run(ops Ops, argv []string) int {
	if len(argv) == 0 {
		fmt.Fprintln(ops.Stderr, "Error: pidslot needs a command: claude-sandbox pidslot -- <cmd...>")
		return 2
	}
	direct := func(reason string) int {
		fmt.Fprintf(ops.Stderr, "Warning: pid class not applied (%s); this session may be invisible to sibling sandboxes.\n", reason)
		return execDirect(ops, argv)
	}
	k, ok := ParseClass(ops.Getenv(EnvVar))
	if !ok {
		return direct(EnvVar + " is unset or not an integer in [0," + strconv.Itoa(Modulus) + ")")
	}
	tini, err := ops.LookPath("tini")
	if err != nil {
		return direct("tini not found on PATH")
	}
	// Shrink the window between the last read and tini's fork: no GC worker
	// thread may spawn (threads share the pid counter), and stay on one M.
	runtime.LockOSThread()
	debug.SetGCPercent(-1)
	if _, err := Burn(ops, k); err != nil {
		return direct(err.Error())
	}
	if err := ops.Exec(tini, append([]string{"tini", "-s", "--"}, argv...)); err != nil {
		return direct("exec tini: " + err.Error())
	}
	return 0
}

func execDirect(ops Ops, argv []string) int {
	path, err := ops.LookPath(argv[0])
	if err != nil {
		fmt.Fprintf(ops.Stderr, "Error: %s: %v\n", argv[0], err)
		return 127
	}
	if err := ops.Exec(path, argv); err != nil {
		fmt.Fprintf(ops.Stderr, "Error: exec %s: %v\n", path, err)
		return 126
	}
	return 0
}
