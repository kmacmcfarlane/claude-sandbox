package main

// Spec: spec/tmux.feature (CS-TMUX-069) — "claude-sandbox tmux restore --all
// [--from SAVE]" through MainWithEnv: a chosen save armed into the panes of
// the running tmux server. The per-row rules are unit-tested in
// internal/tmuxpane/arm_test.go; this covers the form, the save it reads,
// what it prints, the notice it claims, and that it locks and starts nothing.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

var _ = Describe("tmux restore --all: arming a save into existing panes (CS-TMUX-069)", func() {
	var (
		f    *cliFixture
		dir  string
		proc string
		base = time.Date(2026, 9, 29, 12, 0, 0, 0, time.Local)
		now  = base.Add(time.Hour)
		srv  = tmuxpane.Server{PID: 4242, Start: 1727000000}
	)
	at := func(min int) string { return base.Add(time.Duration(min) * time.Minute).Format("20060102T150405") }

	BeforeEach(func() {
		f = newCLIFixture()
		f.envmap["TMUX"] = "/tmp/tmux-1000/default,4242,0"
		f.envmap["TMUX_PANE"] = "%9"
		dir = filepath.Join(f.home, ".local", "share", "tmux", "resurrect")
		Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
		f.env.ResurrectDir = dir
		f.env.Now = func() time.Time { return now }
		f.fake.On("docker version", "29.3.0\n", nil)
		f.fake.On("tmux show -gv default-shell", "/bin/zsh\n", nil)
		proc = GinkgoT().TempDir()
		f.env.ProcRoot = proc
		// The resume guard's host check reads the same /proc seam.
		Expect(os.MkdirAll(filepath.Join(proc, "self", "ns"), 0o755)).To(Succeed())
		Expect(os.Symlink("pid:[4026531836]", filepath.Join(proc, "self", "ns", "pid"))).To(Succeed())
	})

	mark := func(w int, mut func(*tmuxpane.Mark)) tmuxpane.Mark {
		empty := ""
		m := tmuxpane.Mark{V: 1, State: tmuxpane.StateActive, Mode: tmuxpane.ModeClaude,
			Container: fmt.Sprintf("claude-sandbox-x-proj-abc123-n%d", w), Instance: fmt.Sprintf("n%d", w), Project: f.proj,
			ConfigDirEnv: &empty, Conversation: fmt.Sprintf("%08d-1f2d-4e5f-8a9b-0c1d2e3f4a5b", w),
			Name: fmt.Sprintf("task %d", w), NameSource: "user"}
		if mut != nil {
			mut(&m)
		}
		return m
	}
	// save writes a save whose rows are main:<w>.0, each saved in the project.
	save := func(st string, s *tmuxpane.Server, marks map[int]tmuxpane.Mark) {
		var lines []string
		var rows []tmuxpane.Row
		for w := 1; w <= 9; w++ {
			m, ok := marks[w]
			if !ok {
				continue
			}
			lines = append(lines, fmt.Sprintf("pane\tmain\t%d\t1\t:*\t0\tt\t:%s\t1\tclaude-sandbox\t:claude-sandbox", w, f.proj))
			rows = append(rows, tmuxpane.Row{Session: "main", Window: w, Pane: 0, Mark: m})
		}
		Expect(os.WriteFile(filepath.Join(dir, tmuxpane.StateFileName(st)), []byte(strings.Join(lines, "\n")+"\n"), 0o600)).To(Succeed())
		Expect(tmuxpane.WriteSidecar(filepath.Join(dir, tmuxpane.SidecarName(st)), tmuxpane.Sidecar{
			V: 1, StateFile: tmuxpane.StateFileName(st), Server: s, Panes: rows})).To(Succeed())
	}
	// panes scripts the server's panes: main:<w>.0 is %<w> (pane pid
	// 2000+w, its shell leading the foreground group), in the project,
	// running cmd, no client looking at it; %9 is where the command runs.
	panes := func(cmds map[int]string) {
		var lines []string
		add := func(id, sess string, w int, cmd, path string) {
			pid := 2000 + w
			own := strings.Join([]string{cmd, path, "0", "0", "0", fmt.Sprint(pid)}, "\t")
			lines = append(lines, fmt.Sprintf("%s\t%s\t%d\t0\t%d\t%d\t/tmp/tmux-1000/default\t%s\t1\t0\t1\t", id, sess, w, srv.PID, srv.Start, own))
			f.fake.On("tmux display-message -p -t "+id+" ", own+"\t\n", nil)
			Expect(os.MkdirAll(filepath.Join(proc, fmt.Sprint(pid)), 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(proc, fmt.Sprint(pid), "stat"),
				[]byte(fmt.Sprintf("%d (%s) S 1 %d %d 34816 %d 0\n", pid, cmd, pid, pid, pid)), 0o644)).To(Succeed())
		}
		var views []string
		for w := 1; w <= 8; w++ {
			if cmd, ok := cmds[w]; ok {
				add(fmt.Sprintf("%%%d", w), "main", w, cmd, f.proj)
				views = append(views, fmt.Sprintf("%%%d\t1\t0\t1", w))
			}
		}
		add("%9", "ops", 1, "claude-sandbox", f.home)
		f.fake.On("list-panes -a -F #{pane_id}\t#{pane_active}", strings.Join(views, "\n")+"\n", nil)
		f.fake.On("tmux list-panes -a", strings.Join(lines, "\n")+"\n", nil)
	}
	linkLast := func(st string) {
		Expect(os.Symlink(tmuxpane.StateFileName(st), filepath.Join(dir, "last"))).To(Succeed())
	}
	noStarts := func() {
		for _, l := range f.fake.CommandLines() {
			Expect(l).NotTo(MatchRegexp(`^docker (create|start|attach|exec|rm)`), "nothing started")
		}
		Expect(f.lock.acquiredAt).To(BeEmpty(), "no launch lock")
		Expect(filepath.Join(f.cache, tmuxpane.StartLockFile)).NotTo(BeAnExistingFile(), "no restore start lock")
		Expect(f.fake.Session).To(BeNil())
	}

	It("CS-TMUX-069: arms the chosen save into its idle panes, types --resurrected, skips the rest, and starts nothing", func() {
		other := tmuxpane.Server{PID: 100, Start: 1726000000}
		save(at(0), &other, map[int]tmuxpane.Mark{
			1: mark(1, func(m *tmuxpane.Mark) { m.Labelled = true }),
			2: mark(2, nil),
			3: mark(3, nil),
			4: mark(4, func(m *tmuxpane.Mark) { m.Mode = tmuxpane.ModeRalph }),
		})
		save(at(30), &srv, map[int]tmuxpane.Mark{})
		linkLast(at(30))
		panes(map[int]string{1: "zsh", 2: "vim", 4: "zsh"})
		Expect(f.run("tmux", "restore", "--all", "--from", at(0))).To(Equal(0), f.errw.String())
		out := f.out.String()
		Expect(out).To(ContainSubstring("Arming save " + at(0) + " in " + dir + " (4 sandbox panes) into the panes of the tmux server pid 4242 (socket /tmp/tmux-1000/default):\n"))
		Expect(out).To(ContainSubstring("  main:1.0  'task 1'  n1  00000001-1f2d-4e5f-8a9b-0c1d2e3f4a5b  [claude, active]\n" +
			"      armed: marked pending and typed claude-sandbox tmux restore --resurrected into it, after clearing its command line (resume the conversation"))
		Expect(out).To(ContainSubstring("  main:2.0  'task 2'  n2  00000002-1f2d-4e5f-8a9b-0c1d2e3f4a5b  [claude, active]\n" +
			"      armed: marked pending, not typed (it runs vim, not the shell); type claude-sandbox tmux restore in it, or forget it with claude-sandbox tmux restore --drop there\n"))
		Expect(out).To(ContainSubstring("  main:3.0  'task 3'  n3  00000003-1f2d-4e5f-8a9b-0c1d2e3f4a5b  [claude, active]\n      skipped: no pane at main:3.0\n"))
		Expect(out).To(ContainSubstring("      not armed: a ralph run was here"))
		Expect(out).To(ContainSubstring("Typed the restore into 1, marked 1 only, skipped 2. Each armed pane restores itself, one start at a time.\n"))
		Expect(out).To(ContainSubstring("For panes that are gone or moved, restore the whole layout from this save:"))
		Expect(out).To(ContainSubstring("ln -sf tmux_resurrect_" + at(0) + ".txt " + dir + "/last"))

		lines := f.fake.CommandLines()
		Expect(lines).To(ContainElement("tmux send-keys -t %1 C-e C-u claude-sandbox tmux restore --resurrected C-m"))
		Expect(lines).NotTo(ContainElement(HavePrefix("tmux send-keys -t %2")), "vim is never typed into")
		var set1 string
		for _, l := range lines {
			if strings.HasPrefix(l, "tmux set-option -p -t %1 @claude-sandbox ") {
				set1 = strings.TrimPrefix(l, "tmux set-option -p -t %1 @claude-sandbox ")
			}
			Expect(l).NotTo(HavePrefix("tmux set-option -p -t %4"), "a final row arms nothing")
			Expect(l).NotTo(ContainSubstring("-t %9 @claude-sandbox"), "the pane the command runs in is never touched")
		}
		m, ok := tmuxpane.ParseMark(set1)
		Expect(ok).To(BeTrue())
		Expect(m.State).To(Equal(tmuxpane.StatePending))
		Expect(m.Labelled).To(BeTrue(), "the row's labelled is kept for CS-TMUX-022's reclaim")
		Expect(m.Conversation).To(Equal("00000001-1f2d-4e5f-8a9b-0c1d2e3f4a5b"))
		noStarts()
	})

	It("CS-TMUX-069: last by default, previous against the listed server; the sparse line on stderr; the notice claimed", func() {
		other := tmuxpane.Server{PID: 100, Start: 1726000000}
		save(at(0), &other, map[int]tmuxpane.Mark{1: mark(1, nil), 2: mark(2, nil), 3: mark(3, nil), 5: mark(5, nil)})
		save(at(30), &srv, map[int]tmuxpane.Mark{1: mark(1, nil)})
		linkLast(at(30))
		Expect(tmuxpane.WriteNotice(f.cache, tmuxpane.Notice{Stamp: at(30), N: 1, M: 4, K: 1, Lifetimes: true,
			At: now.Add(-time.Minute).UnixMilli()})).To(Succeed())
		panes(map[int]string{1: "zsh"})
		Expect(f.run("tmux", "restore", "--all")).To(Equal(0), f.errw.String())
		Expect(f.out.String()).To(ContainSubstring("Arming save " + at(30) + " (last) in "))
		Expect(f.errw.String()).To(ContainSubstring("this save (" + at(30) + ") has 1 sandbox panes"))
		Expect(f.fake.CommandLines()).To(ContainElement("tmux set -gu @claude-sandbox-notice"), "typed by the operator: claims")
		Expect(filepath.Join(f.cache, tmuxpane.NoticeFile)).NotTo(BeAnExistingFile())

		f.out.Reset()
		Expect(f.run("tmux", "restore", "--all", "--from", "previous")).To(Equal(0), f.errw.String())
		Expect(f.out.String()).To(ContainSubstring("Arming save " + at(0) + " (previous) in "))
		noStarts()
	})

	It("CS-TMUX-069: no tmux server exits 2; a save without a record is one line; bad combinations exit 2", func() {
		f.fake.On("tmux list-panes", "", execx.Fail(1))
		Expect(f.run("tmux", "restore", "--all")).To(Equal(2))
		Expect(f.errw.String()).To(ContainSubstring("no tmux server answered"))

		g := newCLIFixture()
		g.env.ResurrectDir = dir
		g.envmap["TMUX"] = "x"
		g.fake.On("tmux list-panes -a", "", nil)
		Expect(os.WriteFile(filepath.Join(dir, tmuxpane.StateFileName(at(5))), nil, 0o600)).To(Succeed())
		Expect(g.run("tmux", "restore", "--all", "--from", at(5))).To(Equal(0), g.errw.String())
		Expect(g.out.String()).To(ContainSubstring("has no claude-sandbox record (the save hook was not wired then): nothing to arm."))

		for _, args := range [][]string{
			{"tmux", "restore", "--all", "--drop"},
			{"tmux", "restore", "--all", "--resurrected"},
			{"tmux", "restore", "--all", "--rearm"},
			{"tmux", "restore", "--all", "--list", "--from", "last"},
		} {
			h := newCLIFixture()
			h.env.ResurrectDir = dir
			Expect(h.run(args...)).To(Equal(2), strings.Join(args, " "))
			Expect(h.fake.Calls).To(BeEmpty())
		}
		h := newCLIFixture()
		h.envmap["CLAUDE_SANDBOX_PROJECT_DIR"] = h.proj
		Expect(h.run("tmux", "restore", "--all")).To(Equal(2))
		Expect(h.errw.String()).To(ContainSubstring("host only"))
	})

	It("CS-TMUX-069: --dry-run --all still only previews", func() {
		save(at(0), &srv, map[int]tmuxpane.Mark{1: mark(1, nil)})
		linkLast(at(0))
		panes(map[int]string{1: "zsh"})
		Expect(f.run("tmux", "restore", "--dry-run", "--all")).To(Equal(0), f.errw.String())
		Expect(f.out.String()).To(ContainSubstring("Dry run: nothing is changed."))
		for _, l := range f.fake.CommandLines() {
			Expect(l).NotTo(MatchRegexp(`^tmux (set-option|send-keys)`))
		}
	})

	It("CS-TMUX-048: --list's footer names --all --from", func() {
		save(at(0), &srv, map[int]tmuxpane.Mark{1: mark(1, nil)})
		Expect(f.run("tmux", "restore", "--list")).To(Equal(0), f.errw.String())
		Expect(f.out.String()).To(ContainSubstring("then arm the panes that exist:  claude-sandbox tmux restore --all --from " + at(0)))
		Expect(f.out.String()).NotTo(ContainSubstring("still to come"))
	})
})
