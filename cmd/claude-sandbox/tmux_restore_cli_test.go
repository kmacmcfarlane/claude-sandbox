package main

// Spec: spec/tmux.feature (CS-TMUX-045..051) — "claude-sandbox tmux restore"
// through MainWithEnv: the refusals, --list, --dry-run in a pane and
// --dry-run --all, and that none of them writes, locks or starts anything.
// The pure parts are tested in internal/tmuxpane/{saves,restore}_test.go.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

var _ = Describe("tmux restore, read-only (CS-TMUX-045..051)", func() {
	const (
		cid   = "4f1c2a9b8e7d6c5b4a39281706f5e4d3c2b1a09f8e7d6c5b4a3928170615e4d3"
		conv2 = "1b5e9c3a-1f2d-4e5f-8a9b-0c1d2e3f4a5b"
	)
	var (
		f    *cliFixture
		dir  string
		base = time.Date(2026, 9, 29, 12, 0, 0, 0, time.Local)
	)
	at := func(min int) string { return base.Add(time.Duration(min) * time.Minute).Format("20060102T150405") }
	srv := func(pid int) *tmuxpane.Server {
		return &tmuxpane.Server{PID: pid, Start: base.Add(-time.Hour).Unix() + int64(pid)}
	}

	BeforeEach(func() {
		f = newCLIFixture()
		dir = filepath.Join(f.home, ".local", "share", "tmux", "resurrect")
		Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
		f.env.ResurrectDir = dir
		f.env.Now = func() time.Time { return base.Add(time.Hour) }
		f.fake.On("docker version", "29.3.0\n", nil)
	})

	mark := func(mut func(*tmuxpane.Mark)) tmuxpane.Mark {
		empty := ""
		m := tmuxpane.Mark{V: 1, State: tmuxpane.StateActive, Mode: tmuxpane.ModeClaude,
			Container: "claude-sandbox-x-proj-abc123-heron", ContainerID: cid, Instance: "heron", Project: f.proj,
			ConfigDir: filepath.Join(f.home, ".claude"), ConfigDirEnv: &empty, Conversation: markConv,
			Name: "fix the build", NameSource: "user"}
		if mut != nil {
			mut(&m)
		}
		return m
	}
	save := func(stamp string, sc *tmuxpane.Sidecar) {
		Expect(os.WriteFile(filepath.Join(dir, tmuxpane.StateFileName(stamp)), []byte("pane\tmain\t1\t1\t:*\t0\tt\t:/p\t1\tzsh\t:\n"), 0o600)).To(Succeed())
		if sc != nil {
			sc.V, sc.StateFile = 1, tmuxpane.StateFileName(stamp)
			Expect(tmuxpane.WriteSidecar(filepath.Join(dir, tmuxpane.SidecarName(stamp)), *sc)).To(Succeed())
		}
	}
	rowsOf := func(n int) []tmuxpane.Row {
		var out []tmuxpane.Row
		for i := 0; i < n; i++ {
			m := mark(func(m *tmuxpane.Mark) {
				m.ContainerID = ""
				m.Container = fmt.Sprintf("c%d", i)
				m.Conversation = fmt.Sprintf("%08d-1f2d-4e5f-8a9b-0c1d2e3f4a5b", i)
			})
			out = append(out, tmuxpane.Row{Session: "main", Window: i + 2, Pane: 0, Mark: m})
		}
		return out
	}
	// snapshot is every file under the resurrect dir and the cache root,
	// with its size and mtime: a dry run or a list must change none.
	snapshot := func() []string {
		var out []string
		for _, root := range []string{dir, f.cache, f.state} {
			filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
				if err == nil {
					out = append(out, fmt.Sprintf("%s %d %d", p, fi.Size(), fi.ModTime().UnixNano()))
				}
				return nil
			})
		}
		sort.Strings(out)
		return out
	}
	noWrites := func(before []string) {
		Expect(snapshot()).To(Equal(before), "nothing written")
		for _, l := range f.fake.CommandLines() {
			Expect(l).NotTo(MatchRegexp(`^tmux (set-option|set |send-keys)`), "no tmux option or key written")
			Expect(l).NotTo(MatchRegexp(`^docker (create|start|attach|exec|rm)`), "nothing started")
		}
		Expect(f.lock.acquiredAt).To(BeEmpty(), "no lock taken")
		Expect(f.fake.Session).To(BeNil())
	}

	Describe("CS-TMUX-051 row 1 and CS-TMUX-049: refusals exit 2", func() {
		It("CS-TMUX-051: inside a sandbox, without a form, with a form F4b owns, or a bad combination", func() {
			f.envmap["CLAUDE_SANDBOX_PROJECT_DIR"] = f.proj
			Expect(f.run("tmux", "restore", "--list")).To(Equal(2))
			Expect(f.errw.String()).To(ContainSubstring("host only"))
			delete(f.envmap, "CLAUDE_SANDBOX_PROJECT_DIR")
			for _, args := range [][]string{
				{"tmux", "restore"},
				{"tmux", "restore", "--from", "last"},
				{"tmux", "restore", "--list", "--dry-run"},
				{"tmux", "restore", "--list", "--from", "last"},
				{"tmux", "restore", "--pin"},
				{"tmux", "restore", "extra"},
			} {
				f.errw.Reset()
				Expect(f.run(args...)).To(Equal(2), strings.Join(args, " "))
				Expect(f.errw.String()).NotTo(BeEmpty())
			}
			f.errw.Reset()
			Expect(f.run("tmux", "restore")).To(Equal(2))
			Expect(f.errw.String()).To(ContainSubstring("restoring is not built yet"))
			Expect(f.fake.Calls).To(BeEmpty())
		})

		It("CS-TMUX-045: the resurrect dir seam panics under go test when unset", func() {
			f.env.ResurrectDir = ""
			Expect(func() { f.run("tmux", "restore", "--list") }).To(PanicWith(ContainSubstring("real resurrect dir")))
		})

		It("CS-TMUX-051: a per-pane dry run needs TMUX_PANE", func() {
			Expect(f.run("tmux", "restore", "--dry-run")).To(Equal(2))
			Expect(f.errw.String()).To(ContainSubstring("TMUX_PANE"))
		})

		It("CS-TMUX-049: a path, a name that is no save, and previous outside tmux exit 2", func() {
			save(at(0), &tmuxpane.Sidecar{Server: srv(1)})
			save(at(1), &tmuxpane.Sidecar{Server: srv(2)})
			for _, from := range []string{filepath.Join(dir, tmuxpane.StateFileName(at(0))), "20200101T000000", "nonsense"} {
				f.errw.Reset()
				Expect(f.run("tmux", "restore", "--dry-run", "--all", "--from", from)).To(Equal(2), from)
				Expect(f.errw.String()).To(ContainSubstring("not a save"), from)
			}
			f.errw.Reset()
			Expect(f.run("tmux", "restore", "--dry-run", "--all", "--from", "previous")).To(Equal(2))
			Expect(f.errw.String()).To(ContainSubstring("claude-sandbox tmux restore --list"))
		})
	})

	Describe("CS-TMUX-048: --list", func() {
		It("CS-TMUX-048: runs newest first under one heading per server, with last, sparse and no record; then how to use a line", func() {
			save(at(-30), nil)
			save(at(0), &tmuxpane.Sidecar{Server: srv(1), Panes: rowsOf(6)})
			save(at(1), &tmuxpane.Sidecar{Server: srv(1), Panes: rowsOf(6)})
			pend := rowsOf(6)
			pend[0].Mark.State = tmuxpane.StatePending
			save(at(2), &tmuxpane.Sidecar{Server: srv(1), Panes: pend})
			save(at(10), &tmuxpane.Sidecar{Server: srv(2), Panes: rowsOf(1)})
			save(at(11), &tmuxpane.Sidecar{Server: srv(2), Panes: rowsOf(1)})
			Expect(os.Symlink(tmuxpane.StateFileName(at(11)), filepath.Join(dir, "last"))).To(Succeed())
			f.fake.On("tmux show -gqv @continuum-save-interval", "1\n", nil)
			before := snapshot()
			Expect(f.run("tmux", "restore", "--list")).To(Equal(0), f.errw.String())
			out := f.out.String()
			srvTime := func(s *tmuxpane.Server) string { return time.Unix(s.Start, 0).Local().Format("2006-01-02 15:04") }
			hm := func(min int) string { return base.Add(time.Duration(min) * time.Minute).Format("2006-01-02 15:04") }
			Expect(out).To(ContainSubstring("tmux-resurrect saves in " + dir + " (newest first, last 7 days; --all for 30):"))
			Expect(out).To(ContainSubstring(
				"\ntmux server started " + srvTime(srv(2)) + " (pid 2):\n" +
					"  " + at(11) + "  " + hm(11) + "  2 saves since " + hm(10) + "  1 sandbox pane (1 active)  last  sparse (had 6)\n" +
					"\ntmux server started " + srvTime(srv(1)) + " (pid 1):\n" +
					"  " + at(2) + "  " + hm(2) + "  1 save  6 sandbox panes (5 active, 1 pending)\n" +
					"  " + at(1) + "  " + hm(1) + "  2 saves since " + hm(0) + "  6 sandbox panes (6 active)\n" +
					"  " + at(-30) + "  " + hm(-30) + "  1 save  no record\n"))
			// The footer: the previous server's newest save, the dir, and
			// both procedures with the interval as it was.
			Expect(out).To(ContainSubstring("Use a save by its stamp (" + at(2) + " here):"))
			Expect(out).To(ContainSubstring("claude-sandbox tmux restore --dry-run --from " + at(2)))
			Expect(out).To(ContainSubstring("claude-sandbox tmux restore --dry-run --all --from " + at(2)))
			Expect(out).To(ContainSubstring("ln -sf tmux_resurrect_" + at(2) + ".txt " + dir + "/last"))
			Expect(out).To(ContainSubstring("tmux set -g @continuum-save-interval 1 "))
			Expect(out).To(ContainSubstring("systemctl --user stop tmux.service"))
			Expect(out).To(ContainSubstring("A reboot never restores a chosen save"))
			Expect(f.fake.CommandLines()).To(Equal([]string{"tmux show -gqv @continuum-save-interval"}))
			noWrites(before)
		})

		It("CS-TMUX-048: --all reaches 30 days; an empty window says so", func() {
			save(base.Add(-10*24*time.Hour).Format("20060102T150405"), &tmuxpane.Sidecar{})
			Expect(f.run("tmux", "restore", "--list")).To(Equal(0))
			Expect(f.out.String()).To(ContainSubstring("No tmux-resurrect saves in " + dir + " in the last 7 days."))
			f.out.Reset()
			Expect(f.run("tmux", "restore", "--list", "--all")).To(Equal(0))
			Expect(f.out.String()).To(ContainSubstring("(newest first, last 30 days):"))
			Expect(f.out.String()).To(ContainSubstring("saves that do not record their tmux server:"))
			Expect(f.out.String()).To(ContainSubstring("tmux set -gu @continuum-save-interval"), "no server answered: the -u form")
		})
	})

	Describe("CS-TMUX-051: --dry-run in a pane", func() {
		BeforeEach(func() {
			f.envmap["TMUX"] = "/tmp/tmux-1000/default,1,0"
			f.envmap["TMUX_PANE"] = "%5"
		})
		thisPane := func(raw string) {
			f.fake.On("tmux display-message -p -t %5", fmt.Sprintf("main\t1\t0\t%d\t%d\t%s\n", srv(2).PID, srv(2).Start, raw), nil)
		}

		It("CS-TMUX-051: last's row at the pane's coordinates; the sparse line first; the decision, what it would do and the manual command", func() {
			save(at(0), &tmuxpane.Sidecar{Server: srv(1), Panes: rowsOf(6)})
			gone := mark(nil)
			save(at(5), &tmuxpane.Sidecar{Server: srv(2), Panes: []tmuxpane.Row{{Session: "main", Window: 1, Pane: 0, Mark: gone}}})
			Expect(os.Symlink(tmuxpane.StateFileName(at(5)), filepath.Join(dir, "last"))).To(Succeed())
			Expect(os.WriteFile(filepath.Join(f.home, ".claude.json"), []byte("{}"), 0o600)).To(Succeed()) // legacy: a gap
			thisPane("")
			f.fake.OnFunc("docker inspect", func(c execx.Cmd) (string, error) {
				c.Stderr.Write([]byte("Error: No such container: " + cid))
				return "", execx.Fail(1)
			})
			before := snapshot()
			Expect(f.run("tmux", "restore", "--dry-run")).To(Equal(0), f.errw.String())
			out := f.out.String()
			Expect(out).To(ContainSubstring("Dry run: nothing is changed.\nPane main:1.0, from save " + at(5) + " (last) in " + dir + ":\n"))
			Expect(out).To(ContainSubstring("claude-sandbox: this save (" + at(5) + ") has 1 sandbox panes; the saves before the last 1 tmux restarts had 6."))
			Expect(out).To(ContainSubstring("  decision (row 18): resume 'fix the build' (" + markConv + ") in a new container\n"))
			Expect(out).To(ContainSubstring("and 10 s more"))
			Expect(out).To(ContainSubstring("  manual: cd " + f.proj + " && claude-sandbox --new --no-worktree -- --resume " + markConv + " --name 'fix the build'\n"))
			Expect(strings.Index(out, "this save (")).To(BeNumerically("<", strings.Index(out, "decision")))
			noWrites(before)
		})

		It("CS-TMUX-051: the pane's own pending mark comes first; --from reads that save only", func() {
			pending := mark(func(m *tmuxpane.Mark) { m.State = tmuxpane.StatePending; m.Mode = tmuxpane.ModeRalph })
			thisPane(pending.JSON())
			save(at(0), &tmuxpane.Sidecar{Server: srv(1), Panes: []tmuxpane.Row{{Session: "main", Window: 1, Pane: 0, Mark: mark(nil)}}})
			Expect(os.Symlink(tmuxpane.StateFileName(at(0)), filepath.Join(dir, "last"))).To(Succeed())
			Expect(f.run("tmux", "restore", "--dry-run")).To(Equal(0), f.errw.String())
			Expect(f.out.String()).To(ContainSubstring("Pane main:1.0, from its own pending mark:\n  decision (row 4): a ralph run was here"))

			f.out.Reset()
			f.fake.On("docker inspect", "/"+mark(nil).Container+"\x1frunning\x1f2026-09-29T12:00:00Z\x1f"+f.proj+"\x1fheron\x1fclaude\n", nil)
			Expect(f.run("tmux", "restore", "--dry-run", "--from", at(0))).To(Equal(0), f.errw.String())
			Expect(f.out.String()).To(ContainSubstring("Pane main:1.0, from save " + at(0) + " in " + dir))
			Expect(f.out.String()).To(ContainSubstring("decision (row 9): 'fix the build' still runs in " + mark(nil).Container + "; attach to it"))
			Expect(f.out.String()).To(ContainSubstring("would: attach to the running container " + cid))
		})

		It("CS-TMUX-049: previous is the newest save of another tmux server; no record and no row say so", func() {
			thisPane("")
			save(at(0), &tmuxpane.Sidecar{Server: srv(1)})
			save(at(1), &tmuxpane.Sidecar{Server: srv(1)})
			save(at(2), &tmuxpane.Sidecar{Server: srv(2)})
			Expect(f.run("tmux", "restore", "--dry-run", "--from", "previous")).To(Equal(0), f.errw.String())
			Expect(f.out.String()).To(ContainSubstring("from save " + at(1) + " (previous)"))
			Expect(f.out.String()).To(ContainSubstring("decision (row 2): nothing recorded for this pane (main:1.0) — the shell is yours"))
			f.out.Reset()
			save(at(3), nil)
			Expect(f.run("tmux", "restore", "--dry-run", "--from", at(3))).To(Equal(0))
			Expect(f.out.String()).To(ContainSubstring("this save has no claude-sandbox record"))
		})
	})

	Describe("CS-TMUX-051: --dry-run --all", func() {
		It("CS-TMUX-051: the running server's pending marks first, then every row with its decision and manual command", func() {
			pend := mark(func(m *tmuxpane.Mark) {
				m.State = tmuxpane.StatePending
				m.Conversation = conv2
				m.Name = "other"
				m.ContainerID = ""
			})
			active := mark(nil)
			f.fake.On("tmux list-panes", strings.Join([]string{
				"%1\tmain\t1\t0\tzsh\t" + pend.JSON(),
				"%2\twork\t4\t0\tclaude-sandbox\t" + active.JSON(),
			}, "\n")+"\n", nil)
			f.fake.On("docker inspect", "/"+active.Container+"\x1frunning\x1f2026-09-29T12:00:00Z\x1f"+f.proj+"\x1fheron\x1fclaude\n", nil)
			bad := mark(func(m *tmuxpane.Mark) { m.Project = "relative"; m.Name = "evil\x1b[2J" })
			save(at(0), &tmuxpane.Sidecar{Server: srv(1), Panes: []tmuxpane.Row{
				{Session: "main", Window: 1, Pane: 0, Mark: mark(nil)},
				{Session: "main", Window: 2, Pane: 0, Mark: mark(func(m *tmuxpane.Mark) { m.Mode = tmuxpane.ModeJoin })},
				{Session: "main", Window: 3, Pane: 0, Mark: bad},
			}})
			Expect(os.Symlink(tmuxpane.StateFileName(at(0)), filepath.Join(dir, "last"))).To(Succeed())
			before := snapshot()
			Expect(f.run("tmux", "restore", "--dry-run", "--all")).To(Equal(0), f.errw.String())
			out := f.out.String()
			Expect(out).To(ContainSubstring("Pending marks in the running tmux server:\n  main:1.0  'other'  heron  " + conv2 + "  [claude, pending]\n"))
			Expect(out).To(ContainSubstring("Save " + at(0) + " (last) in " + dir + ": 3 sandbox panes:\n"))
			Expect(strings.Index(out, "Pending marks")).To(BeNumerically("<", strings.Index(out, "Save ")))
			Expect(out).To(ContainSubstring("  main:1.0  'fix the build'  heron  " + markConv + "  [claude, active]\n      decision (row 9): 'fix the build' is already on screen in work:4.0\n"))
			Expect(out).To(ContainSubstring("  main:2.0  'fix the build'  heron  " + markConv + "  [join, active]\n      decision (row 5): a joined session was here (fix the build); joins are not restored\n"))
			Expect(out).To(ContainSubstring("      manual: cd " + f.proj + " && claude-sandbox --new --no-worktree -- --resume " + markConv))
			Expect(out).To(ContainSubstring("  main:3.0  (a row that cannot be used)\n      decision (row 3): cannot use the recorded row for main:3.0: its project is not valid\n"))
			Expect(out).NotTo(ContainSubstring("\x1b"))
			Expect(f.fake.CommandLines()).To(ContainElement(HavePrefix("tmux list-panes -a -F")))
			noWrites(before)
		})

		It("CS-TMUX-051: the resume guard runs over one discovery and fails closed when it fails", func() {
			f.fake.On("tmux list-panes", "", execx.Fail(1))
			f.fake.On("docker inspect", "", execx.Fail(1))
			f.fake.On("docker ps", "", execx.Fail(1)) // discovery fails: the guard fails closed
			save(at(0), &tmuxpane.Sidecar{Panes: []tmuxpane.Row{
				{Session: "main", Window: 1, Pane: 0, Mark: mark(func(m *tmuxpane.Mark) { m.ContainerID = "" })},
				{Session: "main", Window: 2, Pane: 0, Mark: mark(func(m *tmuxpane.Mark) { m.ContainerID = ""; m.Conversation = conv2 })},
			}})
			Expect(os.Symlink(tmuxpane.StateFileName(at(0)), filepath.Join(dir, "last"))).To(Succeed())
			Expect(f.run("tmux", "restore", "--dry-run", "--all")).To(Equal(0), f.errw.String())
			out := f.out.String()
			Expect(out).To(ContainSubstring("  (no tmux server answered)"))
			Expect(strings.Count(out, "decision (row 17): cannot tell whether")).To(Equal(2))
			n := 0
			for _, l := range f.fake.CommandLines() {
				if strings.HasPrefix(l, "docker ps") {
					n++
				}
			}
			Expect(n).To(Equal(1), "one discovery for every row")
		})
	})
})
