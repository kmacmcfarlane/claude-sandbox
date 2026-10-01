package tmuxpane

// The lifetimes index (CS-TMUX-047, plan sandbox-reboot-restore 11 § 2,
// 12 § 4, 13 § 3): one small file in the resurrect dir with one entry per tmux
// server lifetime — which save it first and last wrote, and how many rows the
// last one had. "--from previous", the sparse baseline and (F4c) the pin read
// it instead of scanning weeks of sidecars. It is derived data: only the save
// hook writes it, and every reader falls back to a bounded sidecar scan when
// it is missing or unreadable.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

// LifetimesFile is the index's name in the resurrect dir. It falls outside
// resurrect's prune glob (tmux_resurrect_*.txt) and the sidecar pattern.
const LifetimesFile = "claude-sandbox-lifetimes.json"

// lifetimesLock serialises index updates between overlapping saves.
const lifetimesLock = ".claude-sandbox-lifetimes.lock"

// LifetimesVersion is the index's schema version ("v").
const LifetimesVersion = 1

// MaxLifetimes caps the index (the newest by "last" are kept).
const MaxLifetimes = 64

// IndexLockWait bounds the wait for the index lock (13 § 3): long enough for
// an overlapping save to finish (saves take well under a second), so the
// final save of a lifetime — continuum's ExecStop save at shutdown — is not
// dropped behind the last autosave. The hook's deadline caps it further.
var IndexLockWait = 500 * time.Millisecond

// indexLockPoll is how often the lock is tried.
const indexLockPoll = 20 * time.Millisecond

// Lifetime is one tmux server's entry: First and Last are save stamps, Rows
// the row count of Last's sidecar, SavedAt unix ms of the update.
type Lifetime struct {
	Server  Server `json:"server"`
	First   string `json:"first"`
	Last    string `json:"last"`
	Rows    int    `json:"rows"`
	SavedAt int64  `json:"savedAt"`
}

// Lifetimes is the index file.
type Lifetimes struct {
	V         int        `json:"v"`
	Lifetimes []Lifetime `json:"lifetimes"`
}

// ReadLifetimes reads dir's index with CS-TMUX-046's checks. Entries come
// back newest first by Last; one without a stamp or a server is dropped.
func ReadLifetimes(dir string) ([]Lifetime, error) {
	b, err := readOwned(filepath.Join(dir, LifetimesFile), maxRecordFile)
	if err != nil {
		return nil, err
	}
	var lf Lifetimes
	if err := json.Unmarshal(b, &lf); err != nil {
		return nil, fmt.Errorf("does not parse: %v", err)
	}
	if lf.V != LifetimesVersion {
		return nil, fmt.Errorf("version %d, not %d", lf.V, LifetimesVersion)
	}
	out := lf.Lifetimes[:0]
	for _, l := range lf.Lifetimes {
		if IsStamp(l.First) && IsStamp(l.Last) && l.Server.PID > 0 && l.Server.Start > 0 {
			out = append(out, l)
		}
	}
	sortLifetimes(out)
	return out, nil
}

func sortLifetimes(ls []Lifetime) {
	sort.SliceStable(ls, func(i, j int) bool { return ls[i].Last > ls[j].Last })
}

// errIndexBusy is a lock still held when the wait ran out.
var errIndexBusy = errors.New("the lifetimes index is locked by another save")

// UpdateLifetimes records that srv saved stamp with rows rows (CS-TMUX-047),
// under the index lock waited on for at most wait. A new server is added; an
// existing entry moves only forwards (a stamp at or after its Last) and its
// First is never rewritten; entries whose Last state file is gone are pruned.
// An unreadable index is rebuilt from this save: it is derived data.
func UpdateLifetimes(dir string, srv Server, stamp string, rows int, now time.Time, wait time.Duration) error {
	if !IsStamp(stamp) {
		return fmt.Errorf("%q is not a save stamp", stamp)
	}
	unlock, err := lockIndex(dir, wait)
	if err != nil {
		return err
	}
	defer unlock()

	ls, rerr := ReadLifetimes(dir)
	if rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
		ls = nil // rebuilt; the caller logs nothing for derived data
	}
	found := false
	for i := range ls {
		if ls[i].Server == srv {
			found = true
			if stamp >= ls[i].Last {
				ls[i].Last, ls[i].Rows, ls[i].SavedAt = stamp, rows, now.UnixMilli()
			}
		}
	}
	if !found {
		ls = append([]Lifetime{{Server: srv, First: stamp, Last: stamp, Rows: rows, SavedAt: now.UnixMilli()}}, ls...)
	}
	kept := ls[:0]
	for _, l := range ls {
		if _, err := os.Lstat(filepath.Join(dir, StateFileName(l.Last))); errors.Is(err, os.ErrNotExist) {
			continue
		}
		kept = append(kept, l)
	}
	sortLifetimes(kept)
	if len(kept) > MaxLifetimes {
		kept = kept[:MaxLifetimes]
	}
	b, err := json.Marshal(Lifetimes{V: LifetimesVersion, Lifetimes: append([]Lifetime{}, kept...)})
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, LifetimesFile), b)
}

// lockIndex takes the index lock: an flock on a 0600 file opened
// O_CREAT|O_NOFOLLOW|O_CLOEXEC, tried every 20 ms until wait runs out
// (CS-TMUX-047).
func lockIndex(dir string, wait time.Duration) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, lifetimesLock), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, err
		}
		if !time.Now().Before(deadline) {
			f.Close()
			return nil, errIndexBusy
		}
		time.Sleep(min(indexLockPoll, time.Until(deadline)))
	}
}
