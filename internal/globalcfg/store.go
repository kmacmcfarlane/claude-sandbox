package globalcfg

// The copies the host commands keep (CS-GCFG-053): StateRoot/global-config/
// <key>/, owner-only, never mounted into a container. <key> is derived from
// the LEXICAL global-file path Claude Code resolves, so it survives a
// migrate and a host "mv" over the link. The launcher's health check
// (CS-GCFG-001..015, health.go) reads and extends the same store: accept's snapshot-*
// files are its baselines.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
)

const (
	// StoreDirName is the store's directory under StateRoot.
	StoreDirName = "global-config"
	// PreMigratePrefix, RevertedPrefix and SnapshotPrefix name the kept
	// copies: migrate's pre-migration copy, revert's parked linked file and
	// accept's baseline.
	PreMigratePrefix = "pre-migrate-"
	RevertedPrefix   = "reverted-"
	SnapshotPrefix   = "snapshot-"
	// KeepPreMigrate, KeepReverted and KeepSnapshots are the caps, applied
	// after each successful write.
	KeepPreMigrate = 3
	KeepReverted   = 3
	KeepSnapshots  = 5
)

// StoreKey is the store key of a global file: the first 12 hex digits of
// sha256 of its cleaned lexical path.
func StoreKey(globalFile string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(globalFile)))
	return hex.EncodeToString(sum[:])[:12]
}

// Store is one global file's directory of kept copies.
type Store struct {
	Dir string
}

// OpenStore makes StateRoot, StateRoot/global-config and the key directory
// owner-only directories (hostdirs.EnsureOwnedDir, 0700) and returns the
// store. A symlink or a directory another uid owns at any level is an error.
// Under go test it panics for the real state root.
func OpenStore(stateRoot, globalFile string, ops *hostdirs.Ops) (*Store, error) {
	if stateRoot == "" {
		return nil, errors.New("the state directory is not known")
	}
	if testing.Testing() && isRealStateRoot(stateRoot) {
		panic(fmt.Sprintf("globalcfg: a test would write under the real state root %s; use a scratch Env.StateDir", stateRoot))
	}
	dir := filepath.Join(stateRoot, StoreDirName, StoreKey(globalFile))
	for _, d := range []string{stateRoot, filepath.Dir(dir), dir} {
		if err := hostdirs.EnsureOwnedDir(d, hostdirs.OwnedDirMode, ops); err != nil {
			return nil, fmt.Errorf("%s: %v", d, err)
		}
	}
	st := &Store{Dir: dir}
	st.removeStaleTemps(time.Now())
	return st, nil
}

// staleTempAge is how old a .tmp-* file must be before OpenStore removes it
// as the leftover of a writer killed mid-write (CS-GCFG-006).
const staleTempAge = time.Hour

// removeStaleTemps removes .tmp-* files older than staleTempAge. Best effort.
func (s *Store) removeStaleTemps(now time.Time) {
	ents, err := os.ReadDir(s.Dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), ".tmp-") || !e.Type().IsRegular() {
			continue
		}
		if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) >= staleTempAge {
			os.Remove(filepath.Join(s.Dir, e.Name()))
		}
	}
}

// SnapshotLockName is the store's snapshot lock file (CS-GCFG-006).
const SnapshotLockName = ".snapshot.lock"

// TryLock takes a non-blocking exclusive flock on the store's snapshot lock.
// ok is false when another process holds it, or the lock cannot be taken;
// the caller then skips the snapshot. unlock releases it.
func (s *Store) TryLock() (unlock func(), ok bool) {
	// O_NOFOLLOW: a symlink planted at the lock's name is never followed
	// (no file is created or opened at its target); the snapshot is then
	// skipped like any lock that cannot be taken.
	f, err := os.OpenFile(filepath.Join(s.Dir, SnapshotLockName), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return func() {}, false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return func() {}, false
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, true
}

// Write keeps data as <prefix><ms>: a 0600 temp file in the store's
// directory, fsynced, renamed into place, then read back and compared.
func (s *Store) Write(prefix string, data []byte, now time.Time) (string, error) {
	name := filepath.Join(s.Dir, prefix+strconv.FormatInt(now.UnixMilli(), 10))
	if _, err := os.Lstat(name); err == nil {
		name += "-" + randHex()
	}
	tmp, err := os.CreateTemp(s.Dir, ".tmp-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	fail := func(err error) (string, error) {
		tmp.Close()
		os.Remove(tmpName)
		return "", err
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	if err := os.Rename(tmpName, name); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	syncDir(s.Dir)
	back, err := os.ReadFile(name)
	if err != nil {
		return "", err
	}
	if !bytes.Equal(back, data) {
		return "", fmt.Errorf("%s: the copy does not match what was written", name)
	}
	return name, nil
}

// List returns the store's <prefix>* entries, newest first. The names carry
// the millisecond timestamp, so a name sort is a time sort.
func (s *Store) List(prefix string) []string {
	ents, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), prefix) && e.Type().IsRegular() {
			out = append(out, filepath.Join(s.Dir, e.Name()))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

// Newest is the newest <prefix>* entry, "" when there is none.
func (s *Store) Newest(prefix string) string {
	if l := s.List(prefix); len(l) > 0 {
		return l[0]
	}
	return ""
}

// Prune keeps the newest keep <prefix>* entries and removes the rest.
func (s *Store) Prune(prefix string, keep int) error {
	var errs []error
	for i, p := range s.List(prefix) {
		if i >= keep {
			// Another launch may have pruned it first (CS-GCFG-006).
			if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// syncDir fsyncs a directory so a rename in it is durable. Best effort.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}

// isRealStateRoot reports whether root is, or lies inside, the invoking
// user's real state root (the default or the XDG one), compared with
// symlinks resolved, for the go test guard.
func isRealStateRoot(root string) bool {
	var homes []string
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		homes = append(homes, h)
	}
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		homes = append(homes, u.HomeDir)
	}
	root = resolvePath(root)
	for _, h := range homes {
		for _, r := range []string{hostdirs.StateRoot(h, os.Getenv), hostdirs.StateRoot(h, nil)} {
			r = resolvePath(r)
			if root == r || strings.HasPrefix(root, r+string(filepath.Separator)) {
				return true
			}
		}
	}
	return false
}

// resolvePath is p with symlinks resolved as far as it exists: the deepest
// existing ancestor is resolved and the rest appended.
func resolvePath(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(p, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}
