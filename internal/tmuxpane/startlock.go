package tmuxpane

// The restore start lock (CS-TMUX-060; plan sandbox-reboot-restore 11 § 6):
// restores that start a session run one at a time, host-wide, so one image
// build serves every pane and resumed sessions come up spaced. It is NOT the
// launch lock (launch.FileLock): that one always has a deadline and only
// returns a release, while this one waits without a deadline — its holder is
// itself bounded (ReadyCap plus the gap) except while it builds images — and
// its holder writes who it is through the lock's own fd, so a waiter can say
// whom it waits for. Ctrl-C skips the wait.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
)

// StartLockFile is the lock's name in the cache root.
const StartLockFile = "restore-start.lock"

// StartLockPoll is how often a contended start lock is retried.
var StartLockPoll = 100 * time.Millisecond

// holderMax caps the holder text a waiter reads and prints.
const holderMax = 256

// StartLock is a held restore start lock.
type StartLock struct {
	mu sync.Mutex
	f  *os.File
}

// AcquireStartLock takes the lock at path, waiting without a deadline until
// ctx is cancelled (Ctrl-C), then returns ctx.Err(). holder is what this
// restore writes into the file once it holds it ("s:w.p, noun, pid").
// waiting is called once, with the current holder's text, when the lock is
// contended. The directory is created 0700 as the invoking user; the file is
// opened O_NOFOLLOW and 0600, close-on-exec (os.OpenFile), so a session
// child never inherits it.
func AcquireStartLock(ctx context.Context, path, holder string, waiting func(holder string)) (*StartLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("opening %s: not a regular file", path)
	}
	told := false
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, fmt.Errorf("locking %s: %w", path, err)
		}
		if !told && waiting != nil {
			told = true
			waiting(ReadStartLockHolder(path))
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(StartLockPoll):
		}
	}
	// The holder text goes through the locked fd: truncate, then write.
	if err := f.Truncate(0); err == nil {
		f.WriteAt([]byte(holder), 0)
	}
	return &StartLock{f: f}, nil
}

// Release truncates the holder text and unlocks. Safe to call more than once
// and from more than one goroutine (the readiness watcher and the restore's
// deferred release).
func (l *StartLock) Release() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return
	}
	l.f.Truncate(0)
	syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	l.f.Close()
	l.f = nil
}

// Held reports whether the lock is still held by this restore.
func (l *StartLock) Held() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f != nil
}

// ReadStartLockHolder is the holder text a waiter prints: a separate
// O_RDONLY|O_NOFOLLOW|O_NONBLOCK open of a regular file, at most 256 bytes,
// printable characters only.
func ReadStartLockHolder(path string) string {
	// O_NONBLOCK: a FIFO swapped in must not block the waiter in open(),
	// where Ctrl-C could not end it.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return ""
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	b, _ := io.ReadAll(io.LimitReader(f, holderMax))
	s := strings.Map(func(r rune) rune {
		if r == unicode.ReplacementChar || !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, string(b))
	return strings.TrimSpace(s)
}

// StartLockHolder is the text a restore writes as the holder.
func StartLockHolder(coords, noun string, pid int) string {
	who := coords
	if noun != "" {
		who += " (" + noun + ")"
	}
	return fmt.Sprintf("%s, pid %d", who, pid)
}
