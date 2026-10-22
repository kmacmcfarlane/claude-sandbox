// Package clients answers "does anyone still hold container C?"
// (CS-LNCH-204, CS-SESS-098, CS-DIR-029; plan detached-sandboxes 01 F1 as
// amended by 02 § 7).
//
// Two sources, read together by Present:
//
//   - a LEASE per client the launcher runs (a new container's start, an
//     attach, a join, a headless session): a file under
//     <StateRoot>/clients/<64-hex id>/ the launcher holds flock(LOCK_EX) on
//     while it lives. The kernel drops the lock however the launcher dies, so
//     a reader tests the lock and never trusts a written "attached";
//   - a SCAN of the host's /proc for docker CLI processes attached to the
//     container, which also sees clients the launcher did not start (a hand
//     "docker attach", a launcher that predates leases, a nested launcher).
//
// The package imports nothing of the repo but hostdirs. Its consumers are
// the session-end helper (F2), which must not end a container another client
// still uses, and the sessions listing (F4).
package clients

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
)

// Kind is who holds a lease, or what a scanned docker client is.
type Kind string

const (
	// KindPrimary is a new container's own client ("docker start -ai").
	KindPrimary Kind = "primary"
	// KindAttach is "docker attach" to a running container.
	KindAttach Kind = "attach"
	// KindJoin is a join's "docker exec": its own claude in the container.
	KindJoin Kind = "join"
	// KindSDK is a headless session's client (listing only).
	KindSDK Kind = "sdk"
)

// Terminal reports whether k is a client of the container's own terminal
// (the primary claude's TTY): a primary or an attach. A join runs its own
// claude; an SDK client has no terminal.
func (k Kind) Terminal() bool { return k == KindPrimary || k == KindAttach }

// LeaseSuffix and TempSuffix name a lease file and one being taken.
const (
	LeaseSuffix = ".lease"
	TempSuffix  = ".tmp"
	// LockSuffix names the lock files other features keep in a container's
	// directory: the session-end helper's EndLockName (F2), a join helper's
	// "join-<token>.lock" (F3). The prune never touches a directory where
	// one of them is held (CS-DIR-029).
	LockSuffix = ".lock"
	// EndLockName is the session-end helper's lock (one helper per
	// container, F2).
	EndLockName = "end.lock"
)

// TempMinAge is how old a temp file must be before the prune may remove it
// (CS-DIR-029): a lease being taken goes from create to rename in
// microseconds, so this leaves orders of magnitude of slack.
const TempMinAge = time.Minute

// maxRecord bounds a lease file read.
const maxRecord = 4096

// takeAttempts bounds Take's retries after a concurrent prune removed its
// temp file or directory.
const takeAttempts = 3

var fullID = regexp.MustCompile(`^[0-9a-f]{64}$`)

// IsFullID reports whether id is a 64-hex container id, the only key a
// lease directory has.
func IsFullID(id string) bool { return fullID.MatchString(id) }

// Record is a lease file's content.
type Record struct {
	V        int    `json:"v"`
	Kind     Kind   `json:"kind"`
	Since    int64  `json:"since"`
	TmuxPane string `json:"tmuxPane,omitempty"`
}

// Lease is one held client lease. The open file is kept in the struct, and
// the struct by its holder, until Release: a finalizer closing an
// unreferenced *os.File would drop the lock while the client still runs.
type Lease struct {
	// Path is the lease file.
	Path string
	f    *os.File
	once sync.Once
}

// ContainerDir is a container's lease directory.
func ContainerDir(stateRoot, id string) string {
	return filepath.Join(hostdirs.ClientsDir(stateRoot), id)
}

// testHook, when set by a test, runs at the named step of Take: "created"
// right after the temp file is created (before the flock), "locked" right
// after the flock (before the rename).
var testHook func(step, tmp string)

// Take takes a lease of kind on container id (CS-LNCH-204): a temp file
// ".<pid>.<rand>.tmp" opened O_CREAT|O_EXCL|O_NOFOLLOW|O_CLOEXEC 0600,
// flock(LOCK_EX), the record written, then renamed to "<pid>.<rand>.lease",
// so a reader never sees a lease file that is not yet locked. A temp file or
// directory a concurrent prune removed is retried. stateRoot "" panics under
// go test (the real state root).
func Take(stateRoot, id string, kind Kind, since time.Time, tmuxPane string) (*Lease, error) {
	if !IsFullID(id) {
		return nil, fmt.Errorf("container id %q is not a 64-hex id", id)
	}
	root := checkRoot(stateRoot)
	dir := ContainerDir(root, id)
	rec, err := json.Marshal(Record{V: 1, Kind: kind, Since: since.UnixMilli(), TmuxPane: tmuxPane})
	if err != nil {
		return nil, err
	}
	var last error
	for attempt := 0; attempt < takeAttempts; attempt++ {
		l, err := takeOnce(root, dir, rec)
		if err == nil {
			return l, nil
		}
		last = err
		if !errors.Is(err, errRetry) {
			return nil, err
		}
	}
	return nil, last
}

// errRetry marks a race with a concurrent prune.
var errRetry = errors.New("removed by a concurrent prune")

func takeOnce(root, dir string, rec []byte) (*Lease, error) {
	for _, d := range []string{root, hostdirs.ClientsDir(root), dir} {
		if err := hostdirs.EnsureOwnedDir(d, hostdirs.OwnedDirMode, nil); err != nil {
			return nil, fmt.Errorf("%s: %w", d, err)
		}
	}
	base := fmt.Sprintf("%d.%s", os.Getpid(), randHex())
	tmp := filepath.Join(dir, "."+base+TempSuffix)
	final := filepath.Join(dir, base+LeaseSuffix)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errRetry // the directory went under a prune
		}
		return nil, err
	}
	fail := func(err error) (*Lease, error) {
		os.Remove(tmp)
		f.Close()
		return nil, err
	}
	if testHook != nil {
		testHook("created", tmp)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			// A prune holds it on its way to unlinking it.
			f.Close()
			return nil, errRetry
		}
		return fail(err)
	}
	if testHook != nil {
		testHook("locked", tmp)
	}
	if _, err := f.Write(rec); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp, final); err != nil {
		f.Close()
		if errors.Is(err, os.ErrNotExist) {
			return nil, errRetry // pruned before our flock
		}
		os.Remove(tmp)
		return nil, err
	}
	return &Lease{Path: final, f: f}, nil
}

// Release drops the lease: the file is unlinked while still locked, then
// closed, which drops the lock — so a prune never locks a file its holder is
// about to remove. Safe to call more than once and on a nil lease.
func (l *Lease) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		os.Remove(l.Path)
		l.f.Close()
	})
	runtime.KeepAlive(l)
}

// checkRoot refuses an empty state root: under go test that would be the
// real one (CS-DIR-007).
func checkRoot(stateRoot string) string {
	if stateRoot == "" {
		if testing.Testing() {
			panic("clients: an empty state root under go test would be the real one; pass a scratch directory")
		}
		panic("clients: empty state root")
	}
	return stateRoot
}

func randHex() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
}

// LeaseInfo is one lease found in a container's directory.
type LeaseInfo struct {
	Path   string
	Record Record
	// Held is true while its holder lives.
	Held bool
}

// ReadLeases tests every lease of container id with LOCK_SH|LOCK_NB: held
// (EWOULDBLOCK) = its holder lives. A missing directory is no lease; any
// other failure to list it is returned ("cannot tell"). A lease whose record
// cannot be read still counts by its lock, with an empty kind.
func ReadLeases(stateRoot, id string) ([]LeaseInfo, error) {
	dir := ContainerDir(checkRoot(stateRoot), id)
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []LeaseInfo
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), LeaseSuffix) || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		held, rec, ok := testLease(p)
		if !ok {
			continue
		}
		out = append(out, LeaseInfo{Path: p, Record: rec, Held: held})
	}
	return out, nil
}

// testLease opens p (O_NOFOLLOW, regular files only) and tests its lock with
// LOCK_SH|LOCK_NB. ok is false when p vanished or is not a regular file.
func testLease(p string) (held bool, rec Record, ok bool) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return false, rec, false
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return false, rec, false
	}
	b, _ := io.ReadAll(io.LimitReader(f, maxRecord))
	_ = json.Unmarshal(b, &rec)
	switch err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); {
	case err == nil:
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return false, rec, true
	case errors.Is(err, syscall.EWOULDBLOCK):
		return true, rec, true
	default:
		// Cannot tell: count it, the conservative direction.
		return true, rec, true
	}
}
