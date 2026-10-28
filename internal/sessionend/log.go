package sessionend

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
)

// LogMax is the session-end log's size past which it is renamed to
// <log>.old before the next line (one generation kept).
const LogMax = 1 << 20

// Entry is one line of <StateRoot>/session-ends.jsonl (CS-LNCH-208).
type Entry struct {
	At          string    `json:"at"`
	Container   string    `json:"container"`
	ContainerID string    `json:"containerId"`
	Project     string    `json:"project"`
	Instance    string    `json:"instance,omitempty"`
	Worktree    string    `json:"worktree"`
	CwdRoot     string    `json:"cwdRoot,omitempty"`
	ConfigDir   string    `json:"configDir,omitempty"`
	Sessions    []Session `json:"sessions"`
	Resume      []string  `json:"resume"`
	Reason      string    `json:"reason"`
	WaitedMs    int64     `json:"waitedMs"`
	Capped      bool      `json:"capped"`
	Outcome     string    `json:"outcome"`
}

// finish logs a final outcome: one line in the session-end log, and for an
// end the helper caused, a notice for the next launch in the project.
func (r *run) finish(res Result) Result {
	res.Sessions = r.sessionsOf()
	now := r.clock.Now()
	m := r.o.Mark
	e := Entry{
		At: now.UTC().Format(time.RFC3339), Container: m.Container, ContainerID: m.ContainerID,
		Project: m.Project, Instance: m.Instance, Worktree: m.Worktree, CwdRoot: m.CwdRoot,
		ConfigDir: m.ConfigDir, Sessions: res.Sessions, Resume: []string{}, Reason: "hangup",
		WaitedMs: now.Sub(r.o.HangupAt).Milliseconds(), Capped: res.Capped, Outcome: res.Outcome,
	}
	if e.Sessions == nil {
		e.Sessions = []Session{}
	}
	for _, s := range res.Sessions {
		e.Resume = append(e.Resume, s.Resume)
	}
	r.logf("%s", res.Outcome)
	if err := AppendLog(r.o.StateRoot, e); err != nil {
		r.logf("session-end log: %v", err)
	} else {
		res.Logged = true
	}
	if res.Outcome == OutcomeEnded {
		if err := WriteNotice(r.o.StateRoot, noticeOf(m, res, now)); err != nil {
			r.logf("notice: %v", err)
		}
	}
	return res
}

// AppendLog appends e to the session-end log (0600, O_APPEND|O_NOFOLLOW),
// renaming a log past LogMax to <log>.old first.
func AppendLog(stateRoot string, e Entry) error {
	if err := hostdirs.EnsureOwnedDir(stateRoot, hostdirs.OwnedDirMode, nil); err != nil {
		return err
	}
	p := hostdirs.SessionEndsLog(stateRoot)
	if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() && fi.Size() > LogMax {
		os.Rename(p, p+".old")
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("%s: not a regular file", p)
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

// ProjectKey is a project's notice key: the first 12 hex of the sha256 of
// its physical path (CS-LNCH-212).
func ProjectKey(project string) string {
	sum := sha256.Sum256([]byte(project))
	return hex.EncodeToString(sum[:])[:12]
}

func randHex() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// writeAtomic writes data to dir/name through a 0600 temp file and a rename.
func writeAtomic(dir, name string, data []byte) error {
	tmp := filepath.Join(dir, "."+name+".tmp-"+randHex())
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
