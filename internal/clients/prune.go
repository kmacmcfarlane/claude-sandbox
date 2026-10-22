package clients

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
)

// Prune removes what dead clients left under <stateRoot>/clients
// (CS-DIR-029). It runs under the launch lock, from the launch's first
// discovery: known are the 64-hex ids of every row it returned (running,
// paused, created and exited). A directory no row names is removed whole; in
// a named one only dead leases go. A file is removed only once
// LOCK_EX|LOCK_NB on it succeeds and the path still names the locked file;
// a temp file younger than TempMinAge is left alone (a lease being taken);
// a directory where any *.lock is held (the session-end helper's) is never
// touched.
//
// When discovery failed (discoveryErr != nil), or a row's id is not a full
// id (an empty known entry or anything else), nothing is removed: a missing
// row cannot be told from a gone container. The error is the listing's
// (ENOENT is none); per-entry failures are skipped silently.
func Prune(stateRoot string, known []string, discoveryErr error, now time.Time) error {
	if discoveryErr != nil {
		return nil
	}
	keep := map[string]bool{}
	for _, id := range known {
		if !IsFullID(id) {
			return nil
		}
		keep[id] = true
	}
	root := hostdirs.ClientsDir(checkRoot(stateRoot))
	fi, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() || !ownedByMe(fi) {
		// Never through a symlink, never another user's directory.
		return nil
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if !IsFullID(e.Name()) || !e.IsDir() {
			continue
		}
		pruneDir(filepath.Join(root, e.Name()), !keep[e.Name()], now)
	}
	return nil
}

// pruneDir removes dead leases from dir, and with whole also every other
// file and the directory itself — unless a lock file there is held.
func pruneDir(dir string, whole bool, now time.Time) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	// Locks first: a held one keeps the whole directory as it is.
	var locked []*os.File
	defer func() {
		for _, f := range locked {
			f.Close()
		}
	}()
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), LockSuffix) {
			continue
		}
		f, ok := lockExclusive(filepath.Join(dir, e.Name()))
		if !ok {
			return
		}
		locked = append(locked, f)
	}
	removedAll := true
	for _, e := range ents {
		name := e.Name()
		p := filepath.Join(dir, name)
		switch {
		case strings.HasSuffix(name, LockSuffix):
			if !whole {
				continue
			}
			// Held by us since above: unlink before the deferred close.
			if os.Remove(p) != nil {
				removedAll = false
			}
		case strings.HasSuffix(name, LeaseSuffix), strings.HasSuffix(name, TempSuffix):
			if strings.HasSuffix(name, TempSuffix) {
				if fi, err := e.Info(); err != nil || now.Sub(fi.ModTime()) < TempMinAge {
					removedAll = false
					continue
				}
			}
			if !removeIfDead(p) {
				removedAll = false
			}
		default:
			if !whole {
				continue
			}
			// Another feature's plain file (F4's left.json): goes with the
			// directory. Never through a symlink: Remove unlinks the link.
			if os.Remove(p) != nil {
				removedAll = false
			}
		}
	}
	if whole && removedAll {
		os.Remove(dir)
	}
}

// removeIfDead unlinks p when LOCK_EX|LOCK_NB on it succeeds (its holder is
// dead) and p still names the locked file. It reports whether p is gone.
func removeIfDead(p string) bool {
	f, ok := lockExclusive(p)
	if !ok {
		_, err := os.Lstat(p)
		return errors.Is(err, os.ErrNotExist)
	}
	defer f.Close()
	if !sameFile(f, p) {
		return false
	}
	return os.Remove(p) == nil
}

// lockExclusive opens p without following a link and takes LOCK_EX|LOCK_NB.
func lockExclusive(p string) (*os.File, bool) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, false
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, false
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		f.Close()
		return nil, false
	}
	return f, true
}

// sameFile reports whether path still names f's file.
func sameFile(f *os.File, p string) bool {
	a, err := f.Stat()
	if err != nil {
		return false
	}
	b, err := os.Lstat(p)
	return err == nil && os.SameFile(a, b)
}

func ownedByMe(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
