package tmuxpane_test

// Spec: spec/tmux.feature (CS-TMUX-045..050) — reading resurrect's saves for
// `tmux restore`: the dir, the hardened reader, the lifetimes index, --from,
// the sparse rule, the runs --list prints and its procedures. Scratch dirs
// only; tmux is faked through execx.Fake.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

// stampAt renders a local time as a resurrect stamp.
func stampAt(t time.Time) string { return t.Local().Format("20060102T150405") }

var _ = Describe("tmux restore: saves (CS-TMUX-045..050)", func() {
	var (
		dir  string
		base = time.Date(2026, 9, 29, 12, 0, 0, 0, time.Local)
	)

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
	})

	srv := func(pid int) *tmuxpane.Server { return &tmuxpane.Server{PID: pid, Start: int64(1790000000 + pid)} }
	rows := func(n int, state string) []tmuxpane.Row {
		var out []tmuxpane.Row
		for i := 0; i < n; i++ {
			out = append(out, tmuxpane.Row{Session: "main", Window: i, Pane: 0, Mark: tmuxpane.Mark{
				V: 1, State: state, Mode: tmuxpane.ModeClaude, Container: fmt.Sprintf("c%d", i), Project: "/p",
				Conversation: fmt.Sprintf("%08d-1f2d-4e5f-8a9b-0c1d2e3f4a5b", i),
			}})
		}
		return out
	}
	// save writes a state file and, unless sc is nil, its sidecar.
	save := func(stamp string, sc *tmuxpane.Sidecar) {
		Expect(os.WriteFile(filepath.Join(dir, tmuxpane.StateFileName(stamp)), []byte("pane\tmain\t0\t1\t:*\t0\tt\t:/p\t1\tzsh\t:\n"), 0o600)).To(Succeed())
		if sc != nil {
			sc.V = 1
			sc.StateFile = tmuxpane.StateFileName(stamp)
			Expect(tmuxpane.WriteSidecar(filepath.Join(dir, tmuxpane.SidecarName(stamp)), *sc)).To(Succeed())
		}
	}
	at := func(min int) string { return stampAt(base.Add(time.Duration(min) * time.Minute)) }
	link := func(stamp string) {
		Expect(os.Symlink(tmuxpane.StateFileName(stamp), filepath.Join(dir, "last"))).To(Succeed())
	}
	open := func() *tmuxpane.Saves {
		s, err := tmuxpane.OpenSaves(dir)
		Expect(err).NotTo(HaveOccurred())
		return s
	}

	Describe("CS-TMUX-045: the resurrect dir", func() {
		var (
			fake *execx.Fake
			home string
		)
		BeforeEach(func() {
			fake = &execx.Fake{}
			home = GinkgoT().TempDir()
		})
		resolve := func(env map[string]string) string {
			return tmuxpane.ResolveResurrectDir(tmuxpane.DirOptions{
				Runner: fake, Home: home, Getenv: func(k string) string { return env[k] },
				Hostname: func() (string, error) { return "hooper", nil },
			})
		}

		It("CS-TMUX-045: @resurrect-dir wins, with $HOME, $HOSTNAME and every ~ expanded", func() {
			fake.On("tmux show-option -gqv @resurrect-dir", "~/saves/$HOSTNAME/x~y\n", nil)
			Expect(resolve(nil)).To(Equal(filepath.Join(home, "saves", "hooper", "x"+home+"y")))
			Expect(fake.CommandLines()).To(Equal([]string{"tmux show-option -gqv @resurrect-dir"}))
			fake2 := &execx.Fake{}
			fake2.On("tmux show-option", "$HOME/r\n", nil)
			fake = fake2
			Expect(resolve(nil)).To(Equal(filepath.Join(home, "r")))
		})

		It("CS-TMUX-045: else ~/.tmux/resurrect when it exists, else the XDG data dir", func() {
			Expect(resolve(nil)).To(Equal(filepath.Join(home, ".local", "share", "tmux", "resurrect")))
			Expect(resolve(map[string]string{"XDG_DATA_HOME": "/data"})).To(Equal("/data/tmux/resurrect"))
			Expect(os.MkdirAll(filepath.Join(home, ".tmux", "resurrect"), 0o700)).To(Succeed())
			Expect(resolve(map[string]string{"XDG_DATA_HOME": "/data"})).To(Equal(filepath.Join(home, ".tmux", "resurrect")))
		})

		It("CS-TMUX-045: a tmux that fails (no server) or a relative value skips the option", func() {
			fake.On("tmux show-option", "", execx.Fail(1))
			Expect(resolve(nil)).To(Equal(filepath.Join(home, ".local", "share", "tmux", "resurrect")))
			fake2 := &execx.Fake{}
			fake2.On("tmux show-option", "relative/dir\n", nil)
			fake = fake2
			Expect(resolve(nil)).To(Equal(filepath.Join(home, ".local", "share", "tmux", "resurrect")))
		})
	})

	Describe("CS-TMUX-046: defensive reads", func() {
		It("CS-TMUX-046: a sidecar is read only when regular, the user's, not group/world-writable, <= 1 MiB, v 1", func() {
			save(at(0), &tmuxpane.Sidecar{Panes: rows(1, tmuxpane.StateActive)})
			p := filepath.Join(dir, tmuxpane.SidecarName(at(0)))
			sc, err := tmuxpane.ReadSidecar(p)
			Expect(err).NotTo(HaveOccurred())
			Expect(sc.Panes).To(HaveLen(1))

			Expect(os.Chmod(p, 0o620)).To(Succeed())
			_, err = tmuxpane.ReadSidecar(p)
			Expect(err).To(MatchError(ContainSubstring("writable")))
			Expect(os.Chmod(p, 0o600)).To(Succeed())

			sl := filepath.Join(dir, tmuxpane.SidecarName(at(1)))
			Expect(os.Symlink(p, sl)).To(Succeed())
			_, err = tmuxpane.ReadSidecar(sl)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Or(ContainSubstring("symbolic link"), ContainSubstring("too many levels")))

			big := filepath.Join(dir, tmuxpane.SidecarName(at(2)))
			Expect(os.WriteFile(big, []byte(`{"v":1,"x":"`+strings.Repeat("a", 1<<20)+`"}`), 0o600)).To(Succeed())
			_, err = tmuxpane.ReadSidecar(big)
			Expect(err).To(MatchError(ContainSubstring("larger than")))

			v2 := filepath.Join(dir, tmuxpane.SidecarName(at(3)))
			Expect(os.WriteFile(v2, []byte(`{"v":2,"panes":[]}`), 0o600)).To(Succeed())
			_, err = tmuxpane.ReadSidecar(v2)
			Expect(err).To(MatchError(ContainSubstring("version 2")))

			Expect(os.Mkdir(filepath.Join(dir, tmuxpane.SidecarName(at(4))), 0o700)).To(Succeed())
			_, err = tmuxpane.ReadSidecar(filepath.Join(dir, tmuxpane.SidecarName(at(4))))
			Expect(err).To(HaveOccurred())
		})

		It("CS-TMUX-046: each row is checked field by field before use", func() {
			cde, rel := "/cfg", "cfg"
			good := tmuxpane.Mark{V: 1, State: tmuxpane.StateActive, Mode: tmuxpane.ModeClaude, Container: "c", Project: "/p",
				ConfigDir: "/home/u/.claude", CwdRoot: "/p", ConfigDirEnv: &cde, Worktree: "heron", Model: "claude-opus-4-1[1m]",
				ContainerID: strings.Repeat("ab", 32), Conversation: convID}
			Expect(tmuxpane.ValidateRow(good)).To(BeEmpty())
			for field, mut := range map[string]func(*tmuxpane.Mark){
				"v":            func(m *tmuxpane.Mark) { m.V = 2 },
				"state":        func(m *tmuxpane.Mark) { m.State = "gone" },
				"mode":         func(m *tmuxpane.Mark) { m.Mode = "headless" },
				"project":      func(m *tmuxpane.Mark) { m.Project = "p" },
				"configDir":    func(m *tmuxpane.Mark) { m.ConfigDir = "~/.claude" },
				"cwdRoot":      func(m *tmuxpane.Mark) { m.CwdRoot = "rel" },
				"configDirEnv": func(m *tmuxpane.Mark) { m.ConfigDirEnv = &rel },
				"worktree":     func(m *tmuxpane.Mark) { m.Worktree = "a b" },
				"model":        func(m *tmuxpane.Mark) { m.Model = "-x" },
				"containerId":  func(m *tmuxpane.Mark) { m.ContainerID = "abc" },
				"conversation": func(m *tmuxpane.Mark) { m.Conversation = "not-a-uuid" },
				"a control character": func(m *tmuxpane.Mark) {
					m.Name = "fix\x1b[2Jit"
				},
				"a control character ": func(m *tmuxpane.Mark) {
					m.Unreplayed = []string{"--docker-socket", "\x1b]0;x\x07\x1b[2J"}
				},
			} {
				m := good
				mut(&m)
				Expect(tmuxpane.ValidateRow(m)).To(Equal(strings.TrimSpace(field)), field)
			}
			empty := "" // configDirEnv "" (unset at launch) is fine
			m := good
			m.ConfigDirEnv, m.ConfigDir, m.CwdRoot, m.Worktree, m.Model, m.ContainerID, m.Conversation = &empty, "", "", "", "", "", ""
			Expect(tmuxpane.ValidateRow(m)).To(BeEmpty())
		})
	})

	Describe("CS-TMUX-047: the lifetimes index", func() {
		now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
		read := func() []tmuxpane.Lifetime {
			ls, err := tmuxpane.ReadLifetimes(dir)
			Expect(err).NotTo(HaveOccurred())
			return ls
		}

		It("CS-TMUX-047: adds a server, moves only forwards, never rewrites first, 0600", func() {
			for _, m := range []int{0, 1, 2} {
				save(at(m), nil)
			}
			Expect(tmuxpane.UpdateLifetimes(dir, *srv(1), at(1), 3, now, time.Second)).To(Succeed())
			Expect(read()).To(Equal([]tmuxpane.Lifetime{{Server: *srv(1), First: at(1), Last: at(1), Rows: 3, SavedAt: now.UnixMilli()}}))
			Expect(tmuxpane.UpdateLifetimes(dir, *srv(1), at(2), 4, now, time.Second)).To(Succeed())
			// A late save for an older state file leaves last and rows alone.
			Expect(tmuxpane.UpdateLifetimes(dir, *srv(1), at(0), 9, now, time.Second)).To(Succeed())
			ls := read()
			Expect(ls).To(HaveLen(1))
			Expect(ls[0].First).To(Equal(at(1)))
			Expect(ls[0].Last).To(Equal(at(2)))
			Expect(ls[0].Rows).To(Equal(4))
			fi, err := os.Stat(filepath.Join(dir, tmuxpane.LifetimesFile))
			Expect(err).NotTo(HaveOccurred())
			Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))

			// A new server is prepended (newest first by last).
			save(at(5), nil)
			Expect(tmuxpane.UpdateLifetimes(dir, *srv(2), at(5), 1, now, time.Second)).To(Succeed())
			ls = read()
			Expect(ls).To(HaveLen(2))
			Expect(ls[0].Server).To(Equal(*srv(2)))
		})

		It("CS-TMUX-047: prunes entries whose last state file is gone, and caps at 64", func() {
			for i := 0; i < 70; i++ {
				save(at(i), nil)
				Expect(tmuxpane.UpdateLifetimes(dir, *srv(i + 1), at(i), i, now, time.Second)).To(Succeed())
			}
			ls := read()
			Expect(ls).To(HaveLen(tmuxpane.MaxLifetimes))
			Expect(ls[0].Last).To(Equal(at(69)))
			Expect(os.Remove(filepath.Join(dir, tmuxpane.StateFileName(at(69))))).To(Succeed())
			save(at(70), nil)
			Expect(tmuxpane.UpdateLifetimes(dir, *srv(200), at(70), 0, now, time.Second)).To(Succeed())
			for _, l := range read() {
				Expect(l.Last).NotTo(Equal(at(69)))
			}
		})

		It("CS-TMUX-047: an unreadable index is rebuilt from the save (derived data)", func() {
			save(at(0), nil)
			Expect(os.WriteFile(filepath.Join(dir, tmuxpane.LifetimesFile), []byte("garbage"), 0o600)).To(Succeed())
			Expect(tmuxpane.UpdateLifetimes(dir, *srv(1), at(0), 2, now, time.Second)).To(Succeed())
			Expect(read()).To(HaveLen(1))
		})

		It("CS-TMUX-047: the lock is waited on: released at 200 ms the update applies; held past the wait it is skipped", func() {
			save(at(0), nil)
			lock := func() *os.File {
				f, err := os.OpenFile(filepath.Join(dir, ".claude-sandbox-lifetimes.lock"), os.O_RDWR|os.O_CREATE, 0o600)
				Expect(err).NotTo(HaveOccurred())
				Expect(syscall.Flock(int(f.Fd()), syscall.LOCK_EX)).To(Succeed())
				return f
			}
			f := lock()
			go func(f *os.File) {
				time.Sleep(200 * time.Millisecond)
				syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				f.Close()
			}(f)
			start := time.Now()
			Expect(tmuxpane.UpdateLifetimes(dir, *srv(1), at(0), 2, now, tmuxpane.IndexLockWait)).To(Succeed())
			Expect(time.Since(start)).To(BeNumerically(">=", 150*time.Millisecond))
			Expect(read()).To(HaveLen(1))

			f = lock()
			defer f.Close()
			start = time.Now()
			err := tmuxpane.UpdateLifetimes(dir, *srv(2), at(0), 5, now, tmuxpane.IndexLockWait)
			Expect(err).To(MatchError(ContainSubstring("locked")))
			Expect(time.Since(start)).To(BeNumerically(">=", tmuxpane.IndexLockWait))
			Expect(time.Since(start)).To(BeNumerically("<", tmuxpane.IndexLockWait+300*time.Millisecond))
			Expect(read()).To(HaveLen(1), "the held lock skipped the update")
			fi, err := os.Lstat(filepath.Join(dir, ".claude-sandbox-lifetimes.lock"))
			Expect(err).NotTo(HaveOccurred())
			Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		})

		It("CS-TMUX-047: a symlinked lock file is never followed", func() {
			save(at(0), nil)
			target := filepath.Join(GinkgoT().TempDir(), "elsewhere")
			Expect(os.Symlink(target, filepath.Join(dir, ".claude-sandbox-lifetimes.lock"))).To(Succeed())
			Expect(tmuxpane.UpdateLifetimes(dir, *srv(1), at(0), 2, now, time.Second)).NotTo(Succeed())
			Expect(target).NotTo(BeAnExistingFile())
		})
	})

	Describe("CS-TMUX-049: --from", func() {
		It("CS-TMUX-049: last, a stamp and a base name in the dir; nothing else", func() {
			save(at(0), &tmuxpane.Sidecar{})
			save(at(1), &tmuxpane.Sidecar{})
			link(at(0))
			s := open()
			for from, want := range map[string]string{
				"": at(0), "last": at(0), at(1): at(1),
				tmuxpane.StateFileName(at(1)): at(1), tmuxpane.SidecarName(at(1)): at(1),
			} {
				st, err := s.Resolve(from, nil, nil)
				Expect(err).NotTo(HaveOccurred(), from)
				Expect(st).To(Equal(want), from)
			}
			for _, bad := range []string{filepath.Join(dir, tmuxpane.StateFileName(at(1))), "../" + tmuxpane.StateFileName(at(1)),
				"20260101T000000", "yesterday", "3", tmuxpane.StateFileName("20260101T000000")} {
				_, err := s.Resolve(bad, nil, nil)
				Expect(err).To(MatchError(tmuxpane.ErrBadSave), bad)
			}
		})

		It("CS-TMUX-049: a missing or foreign last link names no save", func() {
			save(at(0), &tmuxpane.Sidecar{})
			Expect(os.Symlink("/etc/passwd", filepath.Join(dir, "last"))).To(Succeed())
			_, err := open().Resolve("last", nil, nil)
			Expect(err).To(MatchError(tmuxpane.ErrBadSave))
		})

		It("CS-TMUX-049: previous comes from the index; without one, from a scan; else it refuses", func() {
			save(at(0), &tmuxpane.Sidecar{Server: srv(1)})
			save(at(1), &tmuxpane.Sidecar{Server: srv(1)})
			save(at(2), &tmuxpane.Sidecar{Server: srv(2)})
			save(at(3), &tmuxpane.Sidecar{Server: srv(3)})
			s := open()
			// The scan: the newest sidecar of another server.
			st, err := s.Resolve("previous", srv(3), nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(st).To(Equal(at(2)))
			// The index wins over the scan.
			idx := []tmuxpane.Lifetime{{Server: *srv(3), First: at(3), Last: at(3)}, {Server: *srv(1), First: at(0), Last: at(1)}}
			st, err = s.Resolve("previous", srv(3), idx)
			Expect(err).NotTo(HaveOccurred())
			Expect(st).To(Equal(at(1)))
			// Outside tmux, or no other server recorded.
			_, err = s.Resolve("previous", nil, idx)
			Expect(err).To(MatchError(tmuxpane.ErrNoPrevious))
			_, err = s.Resolve("previous", srv(9), nil)
			Expect(err).NotTo(HaveOccurred())
			dir = GinkgoT().TempDir()
			save(at(0), &tmuxpane.Sidecar{})
			_, err = open().Resolve("previous", srv(3), nil)
			Expect(err).To(MatchError(tmuxpane.ErrNoPrevious))
		})
	})

	Describe("CS-TMUX-049/050: the sidecar scan is capped at ScanMax", func() {
		// One save of server 1, then ScanMax+1 newer saves of server 2: the
		// other server's save lies just past the cap.
		BeforeEach(func() {
			save(at(0), &tmuxpane.Sidecar{Server: srv(1), Panes: rows(9, tmuxpane.StateActive)})
			for i := 1; i <= tmuxpane.ScanMax+1; i++ {
				save(at(i), &tmuxpane.Sidecar{Server: srv(2), Panes: rows(1, tmuxpane.StateActive)})
			}
		})

		It("CS-TMUX-049: previous without an index scans at most ScanMax sidecars", func() {
			_, err := open().Resolve("previous", srv(2), nil)
			Expect(err).To(MatchError(tmuxpane.ErrNoPrevious))
		})

		It("CS-TMUX-050: the sparse baseline's scan stops at ScanMax and falls back to the saves right before", func() {
			v := open().SparseOf(at(tmuxpane.ScanMax+1), 1, srv(2), nil)
			Expect(v.Lifetimes).To(BeFalse())
			Expect(v.K).To(Equal(tmuxpane.SparseFallbackSaves))
			Expect(v.M).To(Equal(1))
		})
	})

	Describe("CS-TMUX-050: the sparse rule", func() {
		It("CS-TMUX-050: at least 2 fewer AND at least a third fewer", func() {
			for _, c := range []struct{ m, maxWarn int }{{2, 0}, {3, 1}, {6, 4}, {13, 8}} {
				Expect(tmuxpane.IsSparse(c.maxWarn, c.m)).To(BeTrue(), "m=%d n=%d", c.m, c.maxWarn)
				Expect(tmuxpane.IsSparse(c.maxWarn+1, c.m)).To(BeFalse(), "m=%d n=%d", c.m, c.maxWarn+1)
			}
			Expect(tmuxpane.IsSparse(0, 1)).To(BeFalse())
			Expect(tmuxpane.IsSparse(0, 0)).To(BeFalse())
		})

		It("CS-TMUX-050: the baseline is the median of the last saves of up to 3 earlier lifetimes (index)", func() {
			s := open()
			idx := []tmuxpane.Lifetime{
				{Server: *srv(9), First: at(50), Last: at(60), Rows: 2}, // S's own server: ignored
				{Server: *srv(4), First: at(40), Last: at(45), Rows: 13},
				{Server: *srv(3), First: at(30), Last: at(35), Rows: 12},
				{Server: *srv(2), First: at(20), Last: at(25), Rows: 1},
				{Server: *srv(1), First: at(10), Last: at(15), Rows: 0}, // a 4th: ignored
			}
			v := s.SparseOf(at(60), 2, srv(9), idx)
			Expect(v.Known).To(BeTrue())
			Expect(v.Lifetimes).To(BeTrue())
			Expect(v.K).To(Equal(3))
			Expect(v.M).To(Equal(12))
			Expect(v.Sparse).To(BeTrue())
			Expect(v.Line()).To(Equal("claude-sandbox: this save (" + at(60) + ") has 2 sandbox panes; the saves before the last 3 tmux restarts had 12. " +
				"If sessions are missing, list earlier saves: claude-sandbox tmux restore --list (ignore this if you closed them on purpose)."))
			// Two values: the larger.
			v = s.SparseOf(at(60), 1, srv(9), idx[2:4])
			Expect(v.M).To(Equal(12))
			// Lifetimes ending after S do not count.
			v = s.SparseOf(at(32), 2, srv(3), idx)
			Expect(v.K).To(Equal(2))
			Expect(v.M).To(Equal(1))
			Expect(v.Sparse).To(BeFalse())
		})

		It("CS-TMUX-050: saves piling up after a bad restore still warn: only earlier servers count", func() {
			save(at(0), &tmuxpane.Sidecar{Server: srv(1), Panes: rows(13, tmuxpane.StateActive)})
			for m := 1; m <= 8; m++ {
				save(at(m), &tmuxpane.Sidecar{Server: srv(2), Panes: rows(1, tmuxpane.StateActive)})
			}
			v := open().SparseOf(at(8), 1, srv(2), nil) // no index: the scan
			Expect(v.Lifetimes).To(BeTrue())
			Expect(v.M).To(Equal(13))
			Expect(v.Sparse).To(BeTrue())
		})

		It("CS-TMUX-050: with no earlier lifetime known, the up to 5 saves right before; none at all is unknown", func() {
			for m := 0; m < 7; m++ {
				save(at(m), &tmuxpane.Sidecar{Panes: rows(6, tmuxpane.StateActive)})
			}
			save(at(7), &tmuxpane.Sidecar{Panes: rows(1, tmuxpane.StateActive)})
			v := open().SparseOf(at(7), 1, nil, nil)
			Expect(v.Lifetimes).To(BeFalse())
			Expect(v.K).To(Equal(5))
			Expect(v.M).To(Equal(6))
			Expect(v.Sparse).To(BeTrue())
			Expect(v.Line()).To(ContainSubstring("the 5 saves before it had 6"))
			v = open().SparseOf(at(0), 0, nil, nil)
			Expect(v.Known).To(BeFalse())
			Expect(v.Sparse).To(BeFalse())
			Expect(v.Line()).To(BeEmpty())
		})
	})

	Describe("CS-TMUX-048: runs", func() {
		It("CS-TMUX-048: consecutive saves of one server with the same sessions collapse; a change, a server change or no record splits", func() {
			save(at(0), nil) // no record
			save(at(1), &tmuxpane.Sidecar{Server: srv(1), Panes: rows(3, tmuxpane.StateActive)})
			save(at(2), &tmuxpane.Sidecar{Server: srv(1), Panes: rows(3, tmuxpane.StateActive)})
			pend := rows(3, tmuxpane.StateActive)
			pend[2].Mark.State = tmuxpane.StatePending
			save(at(3), &tmuxpane.Sidecar{Server: srv(1), Panes: pend})
			moved := rows(3, tmuxpane.StateActive) // renumbered windows: the same run
			moved[0].Window = 9
			save(at(4), &tmuxpane.Sidecar{Server: srv(2), Panes: moved})
			save(at(5), &tmuxpane.Sidecar{Server: srv(2), Panes: rows(3, tmuxpane.StateActive)})
			conv := rows(3, tmuxpane.StateActive)
			conv[1].Mark.Conversation = conv2
			save(at(6), &tmuxpane.Sidecar{Server: srv(2), Panes: conv})
			save(at(7), &tmuxpane.Sidecar{Server: srv(2), Panes: rows(0, tmuxpane.StateActive)})
			link(at(7))
			runs := open().Runs(base.Add(-time.Hour), nil)
			var got []string
			for _, r := range runs {
				got = append(got, fmt.Sprintf("%s..%s x%d rec=%v rows=%d a=%d p=%d last=%v sparse=%v",
					r.Newest, r.Oldest, r.Count, r.Record, r.Rows, r.Active, r.Pending, r.Last, r.Sparse.Sparse))
			}
			Expect(got).To(Equal([]string{
				fmt.Sprintf("%s..%s x1 rec=true rows=0 a=0 p=0 last=true sparse=true", at(7), at(7)),
				fmt.Sprintf("%s..%s x1 rec=true rows=3 a=3 p=0 last=false sparse=false", at(6), at(6)),
				fmt.Sprintf("%s..%s x2 rec=true rows=3 a=3 p=0 last=false sparse=false", at(5), at(4)),
				fmt.Sprintf("%s..%s x1 rec=true rows=3 a=2 p=1 last=false sparse=false", at(3), at(3)),
				fmt.Sprintf("%s..%s x2 rec=true rows=3 a=3 p=0 last=false sparse=false", at(2), at(1)),
				fmt.Sprintf("%s..%s x1 rec=false rows=0 a=0 p=0 last=false sparse=false", at(0), at(0)),
			}))
			Expect(runs[0].Server).To(Equal(srv(2)))
		})

		It("CS-TMUX-048: the window is by stamp, and an unreadable sidecar is its own line", func() {
			save(at(0), &tmuxpane.Sidecar{Panes: rows(1, tmuxpane.StateActive)})
			save(at(1), &tmuxpane.Sidecar{Panes: rows(1, tmuxpane.StateActive)})
			Expect(os.Chmod(filepath.Join(dir, tmuxpane.SidecarName(at(1))), 0o666)).To(Succeed())
			save(stampAt(base.Add(-10*24*time.Hour)), &tmuxpane.Sidecar{})
			runs := open().Runs(base.Add(-7*24*time.Hour), nil)
			Expect(runs).To(HaveLen(2))
			Expect(runs[0].Err).To(HaveOccurred())
			Expect(runs[1].Newest).To(Equal(at(0)))
			Expect(open().Runs(base.Add(-30*24*time.Hour), nil)).To(HaveLen(3))
		})

		It("CS-TMUX-048: names that are not resurrect stamps are ignored", func() {
			Expect(os.WriteFile(filepath.Join(dir, "tmux_resurrect_weird.txt"), nil, 0o600)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(dir, tmuxpane.LifetimesFile), []byte("{}"), 0o600)).To(Succeed())
			Expect(open().Stamps).To(BeEmpty())
		})
	})

	Describe("CS-TMUX-048: the whole-layout procedures", func() {
		It("CS-TMUX-048: A repoints and puts the interval back; B stops, repoints, starts; a reboot is not one", func() {
			d := "/home/u/.local/share/tmux/resurrect"
			p := strings.Join(tmuxpane.Procedures(d, "20260929T120000", "1"), "\n")
			Expect(p).To(ContainSubstring("tmux set -g @continuum-save-interval 0"))
			Expect(p).To(ContainSubstring("ln -sf tmux_resurrect_20260929T120000.txt " + d + "/last"))
			Expect(p).To(ContainSubstring("prefix + C-r"))
			Expect(p).To(ContainSubstring("tmux set -g @continuum-save-interval 1 "))
			Expect(strings.Index(p, "systemctl --user stop tmux.service")).To(BeNumerically("<", strings.Index(p, "systemctl --user start tmux.service")))
			Expect(p).To(ContainSubstring("A reboot never restores a chosen save"))
			Expect(strings.Join(tmuxpane.Procedures("/a b", "20260929T120000", ""), "\n")).To(And(
				ContainSubstring("tmux set -gu @continuum-save-interval"), ContainSubstring("'/a b/last'")))
		})

		It("CS-TMUX-048: the interval is read with one bounded show and kept only when numeric", func() {
			for out, want := range map[string]string{"5\n": "5", "": "", "1m\n": "", "-1\n": ""} {
				fake := &execx.Fake{}
				fake.On("tmux show -gqv @continuum-save-interval", out, nil)
				Expect(tmuxpane.SaveInterval(fake)).To(Equal(want), out)
				Expect(fake.CommandLines()).To(Equal([]string{"tmux show -gqv @continuum-save-interval"}))
			}
			fake := &execx.Fake{}
			fake.On("tmux show", "5\n", execx.Fail(1))
			Expect(tmuxpane.SaveInterval(fake)).To(BeEmpty())
		})
	})

	It("CS-TMUX-047: the index JSON shape", func() {
		save(at(0), nil)
		Expect(tmuxpane.UpdateLifetimes(dir, *srv(1), at(0), 2, base, time.Second)).To(Succeed())
		b, err := os.ReadFile(filepath.Join(dir, tmuxpane.LifetimesFile))
		Expect(err).NotTo(HaveOccurred())
		var raw map[string]any
		Expect(json.Unmarshal(b, &raw)).To(Succeed())
		Expect(raw["v"]).To(BeEquivalentTo(1))
		l := raw["lifetimes"].([]any)[0].(map[string]any)
		Expect(l).To(HaveKey("server"))
		Expect(l["first"]).To(Equal(at(0)))
		Expect(l["last"]).To(Equal(at(0)))
		Expect(l["rows"]).To(BeEquivalentTo(2))
		Expect(l).To(HaveKey("savedAt"))
	})
})
