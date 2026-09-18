package launch

// The host launch lock (CS-SESS-048): one flock, host-wide, held only across
// discovery, the noun/class picks and "docker create". Host-wide rather than
// per project because pid classes are allocated across every sandbox on the
// host.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// LaunchLockFile is the lock's path relative to $HOME, under the sandbox-only
// tree like every other fixed host path the launcher owns.
const LaunchLockFile = SandboxHomeRoot + "/launch.lock"

// DefaultLockTimeout bounds the wait for the lock. The critical section takes
// milliseconds; a lock held this long belongs to a wedged launcher, and waiting
// forever behind it would be worse than launching unserialized (the create
// conflict retry still protects names, CS-SESS-053).
const DefaultLockTimeout = 30 * time.Second

// LaunchLockPath is the lock file for a home directory.
func LaunchLockPath(home string) string {
	return filepath.Join(home, LaunchLockFile)
}

// HostLock is the seam over the launch lock, so tests can record when it is
// taken and released relative to the docker calls.
type HostLock interface {
	// Acquire blocks until the lock is held and returns its release.
	Acquire() (release func(), err error)
}

// FileLock is the real HostLock: an exclusive flock on Path.
type FileLock struct {
	Path    string
	Timeout time.Duration // 0 = DefaultLockTimeout
}

// lockPoll is how often a contended lock is retried.
const lockPoll = 20 * time.Millisecond

// Acquire takes the flock. The directory and file are created as the invoking
// user, like the package-cache and peer-registry directories: nothing else
// would create them, and a root-owned lock would lock every later launch out.
//
// The file is opened by os.OpenFile, which always sets O_CLOEXEC. That is
// load-bearing: the launcher ends by exec'ing "docker start", and a lock fd
// inherited across that exec would be held for the whole session. The caller
// releases explicitly after "docker create"; close-on-exec is the backstop.
func (l FileLock) Acquire() (func(), error) {
	if err := os.MkdirAll(filepath.Dir(l.Path), 0o755); err != nil {
		return nil, fmt.Errorf("creating %s: %w", filepath.Dir(l.Path), err)
	}
	f, err := os.OpenFile(l.Path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", l.Path, err)
	}
	timeout := l.Timeout
	if timeout <= 0 {
		timeout = DefaultLockTimeout
	}
	deadline := time.Now().Add(timeout)
	fd := int(f.Fd())
	for {
		err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, fmt.Errorf("locking %s: %w", l.Path, err)
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("%s is still held after %s", l.Path, timeout)
		}
		time.Sleep(lockPoll)
	}
	return func() {
		syscall.Flock(fd, syscall.LOCK_UN)
		f.Close()
	}, nil
}
