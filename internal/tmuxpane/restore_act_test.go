package tmuxpane_test

// Spec: spec/tmux.feature (CS-TMUX-050 claiming, CS-TMUX-060 the start lock,
// CS-TMUX-061/062 the readiness watcher) — the F4b pieces of internal/tmuxpane,
// with tmux faked through execx.Fake and a scratch registry dir.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

var _ = Describe("tmux restore: the start lock (CS-TMUX-060)", func() {
	var path string
	BeforeEach(func() {
		path = filepath.Join(GinkgoT().TempDir(), "cache", tmuxpane.StartLockFile)
		DeferCleanup(func(old time.Duration) { tmuxpane.StartLockPoll = old }, tmuxpane.StartLockPoll)
		tmuxpane.StartLockPoll = 5 * time.Millisecond
	})

	It("CS-TMUX-060: the holder writes its text through the lock, a waiter shows it, and the release truncates it", func() {
		l, err := tmuxpane.AcquireStartLock(context.Background(), path, tmuxpane.StartLockHolder("main:2.0", "heron", 4242), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(tmuxpane.ReadStartLockHolder(path)).To(Equal("main:2.0 (heron), pid 4242"))
		fi, err := os.Stat(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))

		var told atomic.Value
		got := make(chan *tmuxpane.StartLock, 1)
		go func() {
			l2, err := tmuxpane.AcquireStartLock(context.Background(), path, "main:3.0, pid 1", func(h string) { told.Store(h) })
			Expect(err).NotTo(HaveOccurred())
			got <- l2
		}()
		Eventually(func() any { return told.Load() }).Should(Equal("main:2.0 (heron), pid 4242"))
		Consistently(got, 100*time.Millisecond).ShouldNot(Receive(), "no deadline: it waits")
		l.Release()
		l.Release() // idempotent
		var l2 *tmuxpane.StartLock
		Eventually(got).Should(Receive(&l2))
		Expect(tmuxpane.ReadStartLockHolder(path)).To(Equal("main:3.0, pid 1"))
		l2.Release()
		Expect(tmuxpane.ReadStartLockHolder(path)).To(BeEmpty(), "truncated before the unlock")
	})

	It("CS-TMUX-060: Ctrl-C (the context) ends the wait", func() {
		l, err := tmuxpane.AcquireStartLock(context.Background(), path, "a", nil)
		Expect(err).NotTo(HaveOccurred())
		defer l.Release()
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(50*time.Millisecond, cancel)
		_, err = tmuxpane.AcquireStartLock(ctx, path, "b", nil)
		Expect(err).To(MatchError(context.Canceled))
	})

	It("CS-TMUX-060: a symlink in the lock's place is refused; the holder text shown is printable only", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
		Expect(os.Symlink(filepath.Join(filepath.Dir(path), "elsewhere"), path)).To(Succeed())
		_, err := tmuxpane.AcquireStartLock(context.Background(), path, "a", nil)
		Expect(err).To(HaveOccurred())
		Expect(os.Remove(path)).To(Succeed())
		Expect(os.WriteFile(path, []byte("main:1.0\x1b[2J (x)"+strings.Repeat("y", 400)), 0o600)).To(Succeed())
		h := tmuxpane.ReadStartLockHolder(path)
		Expect(h).NotTo(ContainSubstring("\x1b"))
		Expect(len(h)).To(BeNumerically("<=", 256))
	})

	It("CS-TMUX-060: a FIFO in the lock's place never blocks the waiter's read of the holder", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
		Expect(syscall.Mkfifo(path, 0o600)).To(Succeed())
		done := make(chan string, 1)
		go func() { done <- tmuxpane.ReadStartLockHolder(path) }()
		Eventually(done).Should(Receive(Equal("")))
	})
})

var _ = Describe("tmux restore: claiming the sparse notice (CS-TMUX-050)", func() {
	var (
		dir  string
		fake *execx.Fake
		out  *bytes.Buffer
		now  = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	)
	BeforeEach(func() {
		dir = GinkgoT().TempDir()
		fake = &execx.Fake{}
		out = &bytes.Buffer{}
		Expect(tmuxpane.WriteNotice(dir, tmuxpane.Notice{Stamp: "20261001T085900", N: 1, M: 9, K: 3, Lifetimes: true, At: now.Add(-time.Hour).UnixMilli()})).To(Succeed())
	})
	claim := func(inTmux bool) bool {
		return tmuxpane.ClaimNotice(tmuxpane.ClaimOptions{CacheDir: dir, Runner: fake, InTmux: inTmux, Now: now, Out: out})
	}
	noticePath := func() string { return filepath.Join(dir, tmuxpane.NoticeFile) }
	line := "claude-sandbox: this save (20261001T085900) has 1 sandbox panes; the saves before the last 3 tmux restarts had 9. " +
		"If sessions are missing, list earlier saves: claude-sandbox tmux restore --list (ignore this if you closed them on purpose).\n"

	It("CS-TMUX-050: inside tmux the first claimant prints it once, unsets the option and removes the file", func() {
		Expect(claim(true)).To(BeTrue())
		Expect(out.String()).To(Equal(line))
		Expect(fake.CommandLines()).To(Equal([]string{"tmux set -gu @claude-sandbox-notice"}))
		Expect(noticePath()).NotTo(BeAnExistingFile())
		entries, _ := os.ReadDir(dir)
		Expect(entries).To(BeEmpty(), "the renamed file is gone too")
		out.Reset()
		Expect(claim(true)).To(BeFalse())
		Expect(out.String()).To(BeEmpty())
	})

	It("CS-TMUX-050: outside tmux it only prints, leaving the file and the option", func() {
		Expect(claim(false)).To(BeTrue())
		Expect(out.String()).To(Equal(line))
		Expect(fake.Calls).To(BeEmpty())
		Expect(noticePath()).To(BeAnExistingFile())
	})

	It("CS-TMUX-050: a file a killed claimant left aside is pruned; a live claimant's is kept", func() {
		dead := filepath.Join(dir, tmuxpane.NoticeFile+".claimed-999999999")
		live := filepath.Join(dir, fmt.Sprintf("%s.claimed-%d", tmuxpane.NoticeFile, os.Getppid()))
		Expect(os.WriteFile(dead, []byte("{}"), 0o600)).To(Succeed())
		Expect(os.WriteFile(live, []byte("{}"), 0o600)).To(Succeed())
		claim(false)
		Expect(dead).NotTo(BeAnExistingFile())
		Expect(live).To(BeAnExistingFile())
	})

	It("CS-TMUX-050: a failed unset renames the file back", func() {
		fake.On("tmux set -gu", "", execx.Fail(1))
		Expect(claim(true)).To(BeTrue())
		Expect(noticePath()).To(BeAnExistingFile())
	})

	It("CS-TMUX-050: a notice older than 7 days is removed unprinted; a bad or symlinked one is ignored", func() {
		Expect(tmuxpane.WriteNotice(dir, tmuxpane.Notice{Stamp: "20260901T000000", N: 0, M: 4, K: 3, At: now.Add(-8 * 24 * time.Hour).UnixMilli()})).To(Succeed())
		Expect(claim(true)).To(BeFalse())
		Expect(noticePath()).NotTo(BeAnExistingFile())
		Expect(out.String()).To(BeEmpty())

		b, _ := json.Marshal(map[string]any{"v": 1, "stamp": "x#{pwn}", "n": 1, "m": 5, "at": now.UnixMilli()})
		Expect(os.WriteFile(noticePath(), b, 0o600)).To(Succeed())
		Expect(claim(true)).To(BeFalse())
		Expect(os.Remove(noticePath())).To(Succeed())

		real := filepath.Join(dir, "real.json")
		Expect(tmuxpane.WriteNotice(dir, tmuxpane.Notice{Stamp: "20261001T085900", N: 1, M: 9, K: 3, At: now.UnixMilli()})).To(Succeed())
		Expect(os.Rename(noticePath(), real)).To(Succeed())
		Expect(os.Symlink(real, noticePath())).To(Succeed())
		Expect(claim(true)).To(BeFalse())
		Expect(fake.Calls).To(BeEmpty())
	})
})

var _ = Describe("tmux restore: readiness (CS-TMUX-061/062)", func() {
	var (
		reg      string
		released atomic.Int32
		capped   atomic.Int32
		since    time.Time
	)
	const class = 37
	BeforeEach(func() {
		reg = GinkgoT().TempDir()
		released.Store(0)
		capped.Store(0)
		since = time.Now().Add(-time.Second)
		for _, v := range []*time.Duration{&tmuxpane.ReadyPoll, &tmuxpane.ResumedPoll, &tmuxpane.UpFallback, &tmuxpane.ReadyCap, &tmuxpane.EarlyEnd} {
			DeferCleanup(func(p *time.Duration, old time.Duration) { *p = old }, v, *v)
		}
		tmuxpane.ReadyPoll, tmuxpane.ResumedPoll = 10*time.Millisecond, 10*time.Millisecond
		tmuxpane.UpFallback, tmuxpane.ReadyCap, tmuxpane.EarlyEnd = time.Hour, time.Hour, time.Hour
	})
	record := func(pid int, session string, started time.Time) {
		b, _ := json.Marshal(map[string]any{"pid": pid, "sessionId": session, "cwd": "/p", "startedAt": started.UnixMilli()})
		Expect(os.WriteFile(filepath.Join(reg, fmt.Sprintf("%d.json", pid)), b, 0o600)).To(Succeed())
	}
	start := func(gap time.Duration) *tmuxpane.Watcher {
		return tmuxpane.StartWatcher(tmuxpane.ReadyOptions{
			RegistryDir: reg, Class: class, Since: since, ID: convID, Gap: gap,
			Release: func() { released.Add(1) }, OnCap: func() { capped.Add(1) },
		})
	}
	other := "9b5e9c3a-1f2d-4e5f-8a9b-0c1d2e3f4a5b"

	It("CS-TMUX-061: up is any record at the class since the reservation; the lock goes after the gap", func() {
		record(256+class+1, convID, time.Now()) // another class
		record(class, convID, since.Add(-time.Hour))  // an earlier container's
		w := start(150 * time.Millisecond)
		defer w.ChildReturned()
		Consistently(released.Load, 80*time.Millisecond).Should(BeZero())
		Expect(w.Up()).To(BeFalse())
		record(512+class, other, time.Now()) // a fresh id: up, not resumed
		Eventually(w.Up).Should(BeTrue())
		Expect(released.Load()).To(BeZero(), "the gap first")
		Eventually(released.Load).Should(Equal(int32(1)))
		Expect(w.Resumed()).To(BeFalse())
		record(768+class, convID, time.Now())
		Eventually(w.Resumed).Should(BeTrue(), "it keeps polling after the release until resumed")
		Expect(w.EndedEarly()).To(BeFalse())
		Expect(released.Load()).To(Equal(int32(1)))
	})

	It("CS-TMUX-061: without a record, up comes from the 5 s fallback; ReadyCap releases with one line", func() {
		tmuxpane.UpFallback = 50 * time.Millisecond
		w := start(0)
		Eventually(released.Load).Should(Equal(int32(1)))
		Expect(w.Up()).To(BeTrue())
		w.ChildReturned()
		Expect(capped.Load()).To(BeZero())

		released.Store(0)
		tmuxpane.UpFallback, tmuxpane.ReadyCap = time.Hour, 50*time.Millisecond
		w = start(0)
		Eventually(released.Load).Should(Equal(int32(1)))
		Expect(capped.Load()).To(Equal(int32(1)))
		Expect(w.Up()).To(BeFalse())
		w.ChildReturned()
	})

	It("CS-TMUX-062: an end before resumed within EarlyEnd is early; after resumed or past EarlyEnd it is not", func() {
		w := start(0)
		Expect(w.EndedEarly()).To(BeTrue())
		Expect(released.Load()).To(BeZero(), "the main path releases at the return")

		record(class, convID, time.Now())
		w = start(0)
		Eventually(w.Resumed).Should(BeTrue())
		Expect(w.EndedEarly()).To(BeFalse())

		Expect(os.Remove(filepath.Join(reg, fmt.Sprintf("%d.json", class)))).To(Succeed())
		tmuxpane.EarlyEnd = 30 * time.Millisecond
		w = start(0)
		time.Sleep(60 * time.Millisecond)
		Expect(w.Resumed()).To(BeFalse())
		Expect(w.EndedEarly()).To(BeFalse(), "past EarlyEnd: CS-TMUX-071 decides")
	})
})
