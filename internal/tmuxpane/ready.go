package tmuxpane

// The readiness watcher of a restore resume (CS-TMUX-061/062; plan
// sandbox-reboot-restore 11 § 8, 12 § 2). A resume holds the restore start
// lock until its new session is "up", plus the layout's gap; it is "resumed"
// once its claude registers the conversation it was asked to resume.
//
//   - up: a registry record at the new container's pid class (pid % 256),
//     started at or after the reservation (to the second), whatever its
//     sessionId — the new claude registered. Whether Claude Code writes its
//     record before any interactive screen of --resume is NOT verified (plan
//     11 § 8, a host check owed), so up also comes when the session child has
//     run UpFallback (5 s) without returning: the plan's named fallback.
//   - resumed: such a record names the conversation.
//
// The lock is released at up plus the gap, or at ReadyCap. The watcher then
// keeps polling until resumed, the child's return, or EarlyEnd; a session
// that ended before resumed and within EarlyEnd of its start gets the pane's
// pending row back (EndedEarly).

import (
	"strings"
	"sync"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/registry"
)

// The watcher's timings (11 § 8, 12 § 2). Variables so tests can shorten
// them.
var (
	// ReadyPoll is the registry poll before up; ResumedPoll after it.
	ReadyPoll   = 500 * time.Millisecond
	ResumedPoll = time.Second
	// UpFallback: up without a record, once the child has run this long
	// (11 § 8's fallback for a claude that writes its record late).
	UpFallback = 5 * time.Second
	// ReadyCap releases the lock however the session is doing.
	ReadyCap = 60 * time.Second
	// EarlyEnd: an end before resumed within this long of the start puts
	// the pending row back; later ends follow CS-TMUX-071.
	EarlyEnd = 60 * time.Second
)

// ReadyOptions configure a watcher.
type ReadyOptions struct {
	// RegistryDir is the new container's registry (Plan.RegistryDir); Class
	// its pid class; Since the moment the container was reserved — records
	// older than it (to the second) are an earlier container's.
	RegistryDir string
	Class       int
	Since       time.Time
	// ID is the conversation being resumed.
	ID string
	// Gap is waited after up before the release (answer 50 d).
	Gap time.Duration
	// Release releases the start lock; called once.
	Release func()
	// OnCap is called when ReadyCap passed before up (one line in the pane).
	OnCap func()
}

// Watcher watches one resume.
type Watcher struct {
	o     ReadyOptions
	start time.Time
	done  chan struct{}
	exit  chan struct{}

	mu       sync.Mutex
	up       bool
	upByRec  bool
	resumed  bool
	released bool
	endedAt  time.Time
	stopOnce sync.Once
}

// StartWatcher starts watching; call it right before the session child
// starts (sessionOpts.onChild).
func StartWatcher(o ReadyOptions) *Watcher {
	w := &Watcher{o: o, start: time.Now(), done: make(chan struct{}), exit: make(chan struct{})}
	go w.run()
	return w
}

// ChildReturned stops the watcher (the session child returned) and waits
// for it; the main path then releases the lock itself.
func (w *Watcher) ChildReturned() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() {
		w.mu.Lock()
		w.endedAt = time.Now()
		w.mu.Unlock()
		close(w.done)
	})
	<-w.exit
}

// Up and Resumed report what was seen.
func (w *Watcher) Up() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.up
}

func (w *Watcher) Resumed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.resumed
}

// EndedEarly is keepUnlessReady (12 § 2): the session ended before resumed
// was seen and within EarlyEnd of its start. It stops the watcher first.
func (w *Watcher) EndedEarly() bool {
	if w == nil {
		return false
	}
	w.ChildReturned()
	w.mu.Lock()
	defer w.mu.Unlock()
	return !w.resumed && w.endedAt.Sub(w.start) < EarlyEnd
}

func (w *Watcher) release() {
	w.mu.Lock()
	if w.released {
		w.mu.Unlock()
		return
	}
	w.released = true
	w.mu.Unlock()
	if w.o.Release != nil {
		w.o.Release()
	}
}

// sleep waits d or until the child returned; false when it returned.
func (w *Watcher) sleep(d time.Duration) bool {
	if d <= 0 {
		select {
		case <-w.done:
			return false
		default:
			return true
		}
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-w.done:
		return false
	case <-t.C:
		return true
	}
}

func (w *Watcher) run() {
	defer close(w.exit)
	sinceMs := w.o.Since.Truncate(time.Second).UnixMilli()
	for {
		elapsed := time.Since(w.start)
		at, named := w.look(sinceMs)
		w.mu.Lock()
		if named {
			w.resumed = true
		}
		upNow := !w.up && (at || named || elapsed >= UpFallback)
		if upNow {
			w.up, w.upByRec = true, at || named
		}
		released, resumed := w.released, w.resumed
		w.mu.Unlock()
		if upNow && !released {
			if !w.sleep(w.o.Gap) {
				return
			}
			w.release()
			released = true
		}
		if !released && elapsed >= ReadyCap {
			w.release()
			released = true
			if w.o.OnCap != nil {
				w.o.OnCap()
			}
		}
		if resumed && released {
			return
		}
		if released && time.Since(w.start) >= EarlyEnd {
			return
		}
		poll := ReadyPoll
		if released {
			poll = ResumedPoll
		}
		if !w.sleep(poll) {
			return
		}
	}
}

// look reads the registry once: at is a record at the class since the
// reservation; named one that also names the conversation.
func (w *Watcher) look(sinceMs int64) (at, named bool) {
	d, err := registry.OpenDir(w.o.RegistryDir)
	if err != nil {
		return false, false
	}
	defer d.Close()
	es, err := d.Entries()
	if err != nil {
		return false, false
	}
	for _, e := range es {
		if e.PID%256 != w.o.Class {
			continue
		}
		r, err := d.Read(e)
		if err != nil {
			continue // vanished, or a partial write: the next poll reads it again
		}
		if r.StartedAt < sinceMs {
			continue
		}
		at = true
		if strings.EqualFold(r.SessionID, w.o.ID) {
			return true, true
		}
	}
	return at, false
}
