package tmuxpane

// The sparse-restore notice (CS-TMUX-050; plan sandbox-reboot-restore
// 11 § 4, 12 § 1, 13 § 4). When a resurrect restore reads a sparse save
// (F4c's --pin), the verdict is kept in the cache root and in a tmux global
// option, so it outlives an unattended boot where nobody saw the pane lines.
// F4b claims it: the first command the operator types inside tmux renames
// the file aside, prints the notice once and unsets the option. Outside tmux
// a claimant only prints, leaving both in place (it cannot reach the server
// that holds the option).

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
)

// NoticeFile is the notice's name in the cache root.
const NoticeFile = "restore-notice.json"

// NoticeOption is the tmux global user option that shows the notice in a
// status line (11 § 4.3).
const NoticeOption = "@claude-sandbox-notice"

// NoticeMaxAge is the age past which a notice is removed unprinted.
const NoticeMaxAge = 7 * 24 * time.Hour

// noticeMax caps the notice file read.
const noticeMax = 4 << 10

// Notice is the stored sparse verdict: {v, stamp, n, m, k, lifetimes, at}.
// At is unix milliseconds.
type Notice struct {
	V         int    `json:"v"`
	Stamp     string `json:"stamp"`
	N         int    `json:"n"`
	M         int    `json:"m"`
	K         int    `json:"k"`
	Lifetimes bool   `json:"lifetimes"`
	At        int64  `json:"at"`
}

// Line is the notice as printed: the sparse line of CS-TMUX-050, built only
// from digits, the validated stamp and fixed words.
func (n Notice) Line() string {
	return Sparse{Stamp: n.Stamp, N: n.N, M: n.M, K: n.K, Lifetimes: n.Lifetimes, Known: true, Sparse: true}.Line()
}

// WriteNotice writes the notice file, 0600, by temp file and rename (F4c's
// writer; tests use it to set one up).
func WriteNotice(cacheDir string, n Notice) error {
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return err
	}
	n.V = 1
	b, err := json.Marshal(n)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(cacheDir, ".restore-notice-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(cacheDir, NoticeFile))
}

// readNotice reads path defensively: O_NOFOLLOW, a regular file owned by the
// user, at most 4 KiB, v 1, a stamp of the save-stamp shape and sane counts.
func readNotice(path string) (Notice, bool) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return Notice{}, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > noticeMax || !ownedByMe(fi) {
		return Notice{}, false
	}
	b, err := io.ReadAll(io.LimitReader(f, noticeMax+1))
	if err != nil || len(b) > noticeMax {
		return Notice{}, false
	}
	var n Notice
	if json.Unmarshal(b, &n) != nil || n.V != 1 || !stampRE.MatchString(n.Stamp) ||
		n.N < 0 || n.M < 0 || n.K < 0 || n.N > 100000 || n.M > 100000 || n.K > 100 {
		return Notice{}, false
	}
	return n, true
}

// ownedByMe reports whether fi belongs to the invoking user.
func ownedByMe(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}

// ClaimOptions are ClaimNotice's seams.
type ClaimOptions struct {
	// CacheDir holds the notice file.
	CacheDir string
	// Runner runs the one bounded "tmux set -gu".
	Runner execx.Runner
	// InTmux: TMUX is set (and not in a sandbox). Only then is the notice
	// claimed; otherwise it is only printed.
	InTmux bool
	Now    time.Time
	// Out receives the notice line.
	Out io.Writer
}

// ClaimNotice prints the pending notice, if any, and claims it when the
// claimant runs inside tmux (CS-TMUX-050): rename the file aside, print once,
// one bounded "tmux set -gu @claude-sandbox-notice", then remove the renamed
// file — or rename it back when the unset fails, so the claim completes or
// does not happen. Outside tmux it only prints. A notice older than 7 days
// is removed unprinted. Every failure is silent. It returns whether a notice
// was printed.
func ClaimNotice(o ClaimOptions) bool {
	if o.CacheDir == "" {
		return false
	}
	path := filepath.Join(o.CacheDir, NoticeFile)
	n, ok := readNotice(path)
	if !ok {
		return false
	}
	if at := time.UnixMilli(n.At); o.Now.Sub(at) > NoticeMaxAge {
		os.Remove(path)
		return false
	}
	if !o.InTmux {
		fmt.Fprintln(o.Out, n.Line())
		return true
	}
	aside := fmt.Sprintf("%s.claimed-%d", path, os.Getpid())
	if os.Rename(path, aside) != nil {
		return false // another claimant won
	}
	fmt.Fprintln(o.Out, n.Line())
	if _, ok := bounded(o.Runner, CallTimeout, "tmux", "set", "-gu", NoticeOption); !ok {
		os.Rename(aside, path)
		return true
	}
	os.Remove(aside)
	return true
}
