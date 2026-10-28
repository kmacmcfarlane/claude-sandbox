package sessionend

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
	"github.com/kmacmcfarlane/claude-sandbox/internal/registry"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

// Notice is one ended container's notice (CS-LNCH-212).
type Notice struct {
	V       int           `json:"v"`
	Project string        `json:"project"`
	At      int64         `json:"at"`
	Entries []NoticeEntry `json:"entries"`
}

// NoticeEntry is one session in a notice.
type NoticeEntry struct {
	Instance  string `json:"instance"`
	Name      string `json:"name,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	Resume    string `json:"resume,omitempty"`
	Capped    bool   `json:"capped,omitempty"`
}

// NoticeMax is a notice file's largest size; a larger one is removed
// unprinted.
const NoticeMax = 4096

// NoticeMaxAge is how long a notice waits for a launch to print it.
const NoticeMaxAge = 7 * 24 * time.Hour

// MaxPrinted is how many sessions one claim prints before a count.
const MaxPrinted = 3

func noticeOf(m tmuxpane.Mark, res Result, now time.Time) Notice {
	inst := m.Instance
	if inst == "" {
		inst = m.Container
	}
	n := Notice{V: 1, Project: m.Project, At: now.UnixMilli()}
	for _, s := range res.Sessions {
		n.Entries = append(n.Entries, NoticeEntry{Instance: inst, Name: s.Name, SessionID: s.SessionID, Resume: s.Resume, Capped: res.Capped})
	}
	if len(n.Entries) == 0 {
		n.Entries = []NoticeEntry{{Instance: inst, Capped: res.Capped}}
	}
	return n
}

// WriteNotice writes n under <StateRoot>/notices/ (0700) as
// ended-<project key>-<ms>-<rand>.json, 0600, through a temp file and a
// rename. Entries are dropped from the end until it fits NoticeMax.
func WriteNotice(stateRoot string, n Notice) error {
	dir := hostdirs.NoticesDir(stateRoot)
	for _, d := range []string{stateRoot, dir} {
		if err := hostdirs.EnsureOwnedDir(d, hostdirs.OwnedDirMode, nil); err != nil {
			return err
		}
	}
	var b []byte
	for {
		var err error
		if b, err = json.Marshal(n); err != nil {
			return err
		}
		if len(b) <= NoticeMax || len(n.Entries) <= 1 {
			break
		}
		n.Entries = n.Entries[:len(n.Entries)-1]
	}
	if len(b) > NoticeMax {
		n.Entries[0].Resume = ""
		b, _ = json.Marshal(n)
	}
	name := fmt.Sprintf("ended-%s-%d-%s.json", ProjectKey(n.Project), n.At, randHex())
	return writeAtomic(dir, name, b)
}

// ClaimOptions is one claim of a project's notices.
type ClaimOptions struct {
	StateRoot string
	Project   string
	Out       io.Writer
	Now       time.Time
	// PID is the claimant's pid; Alive reports whether a pid lives (nil:
	// kill(pid, 0)).
	PID   int
	Alive func(pid int) bool
}

// ClaimNotices prints and removes every notice of the project (CS-LNCH-212):
// unlike the sparse-restore notice it always claims, with no tmux step. A
// ".claimed-<pid>" left by a dead claimant is removed first, a notice older
// than NoticeMaxAge is removed unprinted, and each notice is renamed aside
// before it is printed, so two launches never print one notice twice.
// Returns how many sessions it printed.
func ClaimNotices(o ClaimOptions) int {
	dir := hostdirs.NoticesDir(checkStateRoot(o.StateRoot))
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	if o.PID == 0 {
		o.PID = os.Getpid()
	}
	alive := o.Alive
	if alive == nil {
		alive = func(pid int) bool { return !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }
	}
	prefix := "ended-" + ProjectKey(o.Project) + "-"
	var names []string
	for _, e := range ents {
		n := e.Name()
		if !strings.HasPrefix(n, prefix) {
			continue
		}
		if base, pidText, ok := strings.Cut(n, ".json.claimed-"); ok && base != "" {
			if pid, err := strconv.Atoi(pidText); err == nil && pid != o.PID && !alive(pid) {
				os.Remove(filepath.Join(dir, n))
			}
			continue
		}
		if strings.HasSuffix(n, ".json") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	var entries []NoticeEntry
	var ats []int64
	for _, n := range names {
		p := filepath.Join(dir, n)
		claimed := p + ".claimed-" + strconv.Itoa(o.PID)
		if os.Rename(p, claimed) != nil {
			continue // another launch claimed it
		}
		nt, ok := readNotice(claimed)
		os.Remove(claimed)
		if !ok || nt.Project != o.Project || o.Now.Sub(time.UnixMilli(nt.At)) > NoticeMaxAge {
			continue
		}
		for _, e := range nt.Entries {
			entries = append(entries, e)
			ats = append(ats, nt.At)
		}
	}
	if len(entries) == 0 {
		return 0
	}
	for i, e := range entries {
		if i == MaxPrinted {
			fmt.Fprintf(o.Out, "Note: %d more sessions here ended the same way.\n", len(entries)-MaxPrinted)
			break
		}
		fmt.Fprint(o.Out, NoticeText(e, time.UnixMilli(ats[i])))
	}
	fmt.Fprintf(o.Out, "All sessions ended this way: %s\n", hostdirs.SessionEndsLog(o.StateRoot))
	return len(entries)
}

// NoticeText is one session's lines of the note.
func NoticeText(e NoticeEntry, at time.Time) string {
	when := "after its turn"
	if e.Capped {
		when = fmt.Sprintf("it ended at the %d min cap while busy", int(TurnMax.Minutes()))
	}
	inst := registry.Printable(e.Instance)
	head := fmt.Sprintf("Note: '%s' ended when its terminal closed (%s, %s).", inst, when, at.Local().Format("2006-01-02 15:04"))
	resume := e.Resume
	if strings.IndexFunc(resume, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
		resume = "" // the file is the user's, but never print an escape
	}
	if resume == "" {
		return head + "\n"
	}
	return head + " Resume it with:\n  " + resume + "\n"
}

// readNotice reads a notice: O_NOFOLLOW, a regular file of the user, at most
// NoticeMax bytes, v 1.
func readNotice(p string) (Notice, bool) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return Notice{}, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > NoticeMax {
		return Notice{}, false
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
		return Notice{}, false
	}
	b, err := io.ReadAll(io.LimitReader(f, NoticeMax+1))
	if err != nil || len(b) > NoticeMax {
		return Notice{}, false
	}
	var n Notice
	if json.Unmarshal(b, &n) != nil || n.V != 1 {
		return Notice{}, false
	}
	return n, true
}
