package tmuxpane_test

// Spec: spec/tmux.feature (CS-TMUX-069) — "tmux restore --all": a chosen
// save armed into the panes that exist. Each row goes through the steps in
// order (usable, a pane in its saved place, idle, not on screen, a decision
// that is not final, unchanged since the list), and only then is the pane
// marked pending — and typed into only when it is provably idle at a shell.
// tmux is faked through execx.Fake; the decision's probes are scripted.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

var _ = Describe("tmux restore --all: arming a save into existing panes (CS-TMUX-069)", func() {
	const cid = "4f1c2a9b8e7d6c5b4a39281706f5e4d3c2b1a09f8e7d6c5b4a3928170615e4d3"
	var (
		proj string
		proc string
		fake *execx.Fake
		p    *probes
	)

	BeforeEach(func() {
		var err error
		proj, err = filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		proc = GinkgoT().TempDir()
		fake = &execx.Fake{}
		p = &probes{dirs: map[string]bool{proj: true}}
	})

	mark := func(mut func(*tmuxpane.Mark)) tmuxpane.Mark {
		empty := ""
		m := tmuxpane.Mark{V: 1, State: tmuxpane.StatePending, Mode: tmuxpane.ModeClaude, Container: "claude-sandbox-x-proj-abc123-heron",
			Instance: "heron", Project: proj, ConfigDirEnv: &empty, Conversation: convID, Name: "fix the build", NameSource: "user"}
		if mut != nil {
			mut(&m)
		}
		return m
	}
	// pane is one listed pane (main:<w>.0, id %<w>, pane pid 1000+w).
	// tpgid is its terminal's foreground group: 0 = the pane's own shell
	// leads it, -1 = no /proc entry, else that pid.
	type pn struct {
		w                                        int
		cmd, path, mark                          string
		inMode, sync, dead, active, wactive, att string
		tpgid                                    int
		sess                                     string
	}
	def := func(s, d string) string {
		if s == "" {
			return d
		}
		return s
	}
	own := func(x pn) string {
		return strings.Join([]string{x.cmd, x.path, def(x.inMode, "0"), def(x.sync, "0"), def(x.dead, "0"),
			fmt.Sprint(1000 + x.w)}, "\t")
	}
	statLine := func(pid, tpgid int) string {
		// pid (comm) state ppid pgrp session tty_nr tpgid ...; a comm with
		// a space and a ")" checks the split after the LAST ")".
		return fmt.Sprintf("%d (ba)sh x) S 1 %d %d 34816 %d 4194560 0 0\n", pid, pid, pid, tpgid)
	}
	// list scripts the one list-panes, each pane's re-check (the same own
	// fields and mark, so nothing changed since the list) and its /proc stat.
	list := func(ps ...pn) []tmuxpane.ArmPane {
		var lines []string
		for _, x := range ps {
			pid := 1000 + x.w
			lines = append(lines, fmt.Sprintf("%%%d\t%s\t%d\t0\t4242\t1790000000\t/tmp/tmux-1000/default\t%s\t%s\t%s\t%s\t%s",
				x.w, def(x.sess, "main"), x.w, own(x), def(x.active, "1"), def(x.wactive, "0"), def(x.att, "1"), x.mark))
			fake.On(fmt.Sprintf("tmux display-message -p -t %%%d ", x.w), own(x)+"\t"+x.mark+"\n", nil)
			if x.tpgid >= 0 {
				tp := x.tpgid
				if tp == 0 {
					tp = pid
				}
				Expect(os.MkdirAll(filepath.Join(proc, fmt.Sprint(pid)), 0o755)).To(Succeed())
				Expect(os.WriteFile(filepath.Join(proc, fmt.Sprint(pid), "stat"), []byte(statLine(pid, tp)), 0o644)).To(Succeed())
			}
		}
		fake.On("tmux list-panes -a", strings.Join(lines, "\n")+"\n", nil)
		panes, srv, ok := tmuxpane.ListArmPanes(fake)
		Expect(ok).To(BeTrue())
		Expect(srv.Server).To(Equal(tmuxpane.Server{PID: 4242, Start: 1790000000}))
		Expect(srv.Socket).To(Equal("/tmp/tmux-1000/default"))
		return panes
	}
	row := func(w int, m tmuxpane.Mark) tmuxpane.Row {
		return tmuxpane.Row{Session: "main", Window: w, Pane: 0, Mark: m}
	}
	state := func(ws ...int) []tmuxpane.StatePane {
		var out []tmuxpane.StatePane
		for _, w := range ws {
			out = append(out, tmuxpane.StatePane{Session: "main", Window: w, Pane: 0, Dir: proj, FullCommandSaved: true})
		}
		return out
	}
	arm := func(panes []tmuxpane.ArmPane, st []tmuxpane.StatePane, shell string, rows ...tmuxpane.Row) []tmuxpane.ArmRow {
		return tmuxpane.Arm(tmuxpane.ArmOptions{Runner: fake, Rows: rows, State: st, Panes: panes, Shell: shell, Self: "%99", Probes: p,
			Proc: tmuxpane.ProcOptions{Runner: fake, ProcRoot: proc, GOOS: "linux"}})
	}
	writes := func() []string {
		var w []string
		for _, l := range fake.CommandLines() {
			if strings.HasPrefix(l, "tmux set-option") || strings.HasPrefix(l, "tmux send-keys") {
				w = append(w, l)
			}
		}
		return w
	}
	setJSON := func(id string) tmuxpane.Mark {
		for _, l := range fake.CommandLines() {
			prefix := "tmux set-option -p -t " + id + " @claude-sandbox "
			if strings.HasPrefix(l, prefix) {
				m, ok := tmuxpane.ParseMark(strings.TrimPrefix(l, prefix))
				Expect(ok).To(BeTrue())
				return m
			}
		}
		Fail("no set-option for " + id)
		return tmuxpane.Mark{}
	}
	keys := func(id string) string { return "tmux send-keys -t " + id + " C-e C-u " + tmuxpane.RetypeKeys + " C-m" }

	It("CS-TMUX-069: an idle shell in its saved place is marked pending from the row and --resurrected is typed", func() {
		panes := list(pn{w: 1, cmd: "zsh", path: proj})
		labelled := mark(func(m *tmuxpane.Mark) { m.Labelled = true; m.State = tmuxpane.StateActive })
		res := arm(panes, state(1), "zsh", row(1, labelled))
		Expect(res).To(HaveLen(1))
		Expect(res[0].Verdict).To(Equal(tmuxpane.ArmTyped), res[0].Why)
		Expect(res[0].Decision.Outcome).To(Equal(tmuxpane.OutcomeResume))
		got := setJSON("%1")
		Expect(got.State).To(Equal(tmuxpane.StatePending), "an active row is armed as pending")
		Expect(got.Labelled).To(BeTrue(), "labelled is kept: only a labelled row reclaims its window (CS-TMUX-022)")
		want := labelled
		want.State = tmuxpane.StatePending
		Expect(got).To(Equal(want), "every other field kept")
		Expect(writes()).To(Equal([]string{"tmux set-option -p -t %1 @claude-sandbox " + want.JSON(), keys("%1")}),
			"the mark first, then the keys")
		Expect(tmuxpane.RetypeKeys).To(Equal("claude-sandbox tmux restore --resurrected"), "never the plain restore (12 § 1)")
	})

	It("CS-TMUX-069: an unlabelled row's armed mark carries no labelled", func() {
		panes := list(pn{w: 1, cmd: "zsh", path: proj})
		arm(panes, state(1), "zsh", row(1, mark(nil)))
		Expect(setJSON("%1").Labelled).To(BeFalse())
		Expect(fake.CommandLines()).NotTo(ContainElement(ContainSubstring(`"labelled"`)))
	})

	DescribeTable("CS-TMUX-069: a pane not provably idle at a shell is marked only, never typed into",
		func(x pn, shell, why string) {
			x.w = 1
			if x.path == "" {
				x.path = proj
			}
			st := state(1)
			if strings.Contains(x.path, "  ") {
				st[0].Dir = x.path
			}
			res := arm(list(x), st, shell, row(1, mark(nil)))
			Expect(res[0].Verdict).To(Equal(tmuxpane.ArmMarked))
			Expect(res[0].Why).To(ContainSubstring(why))
			Expect(writes()).To(HaveLen(1))
			Expect(writes()[0]).To(HavePrefix("tmux set-option -p -t %1 @claude-sandbox "))
		},
		Entry("another program runs", pn{cmd: "vim"}, "zsh", "it runs vim, not the shell"),
		Entry("copy mode", pn{cmd: "zsh", inMode: "1"}, "zsh", "copy mode"),
		Entry("synchronize-panes", pn{cmd: "zsh", sync: "1"}, "zsh", "synchronize-panes"),
		Entry("a client looks at it", pn{cmd: "zsh", wactive: "1"}, "zsh", "a client is looking at it"),
		Entry("the default shell is unknown", pn{cmd: "zsh"}, "", "default shell is unknown"),
		Entry("a lossy saved dir", pn{cmd: "zsh", path: "/tmp/two  spaces"}, "zsh", "cannot be compared"),
	)

	It("CS-TMUX-069: an empty saved dir cannot be compared either", func() {
		st := state(1)
		st[0].Dir = ""
		res := arm(list(pn{w: 1, cmd: "zsh", path: proj}), st, "zsh", row(1, mark(nil)))
		Expect(res[0].Verdict).To(Equal(tmuxpane.ArmMarked))
		Expect(writes()).To(HaveLen(1))
	})

	It("CS-TMUX-069: a window not focused in an attached session, or the session detached, is no reason to hold back", func() {
		res := arm(list(pn{w: 1, cmd: "zsh", path: proj, wactive: "1", att: "0"}), state(1), "zsh", row(1, mark(nil)))
		Expect(res[0].Verdict).To(Equal(tmuxpane.ArmTyped), res[0].Why)
	})

	DescribeTable("CS-TMUX-069 step 2: missing or moved panes are skipped and touched by nothing",
		func(setup func() ([]tmuxpane.ArmPane, []tmuxpane.StatePane, tmuxpane.Mark), why string) {
			panes, st, m := setup()
			res := tmuxpane.Arm(tmuxpane.ArmOptions{Runner: fake, Rows: []tmuxpane.Row{row(1, m)}, State: st, Panes: panes, Shell: "zsh", Probes: p})
			Expect(res[0].Verdict).To(Equal(tmuxpane.ArmMissing))
			Expect(res[0].Why).To(ContainSubstring(why))
			Expect(writes()).To(BeEmpty())
			Expect(p.asked).To(BeEmpty(), "no probe for a row that is skipped earlier")
		},
		Entry("no pane at the coordinates", func() ([]tmuxpane.ArmPane, []tmuxpane.StatePane, tmuxpane.Mark) {
			return list(pn{w: 2, cmd: "zsh", path: proj}), state(1), mark(nil)
		}, "no pane at main:1.0"),
		Entry("no state-file line", func() ([]tmuxpane.ArmPane, []tmuxpane.StatePane, tmuxpane.Mark) {
			return list(pn{w: 1, cmd: "zsh", path: proj}), state(2), mark(nil)
		}, "no line for main:1.0"),
		Entry("another directory", func() ([]tmuxpane.ArmPane, []tmuxpane.StatePane, tmuxpane.Mark) {
			return list(pn{w: 1, cmd: "zsh", path: GinkgoT().TempDir()}), state(1), mark(nil)
		}, "not in its saved directory"),
		Entry("an active row saved outside its project", func() ([]tmuxpane.ArmPane, []tmuxpane.StatePane, tmuxpane.Mark) {
			return list(pn{w: 1, cmd: "zsh", path: proj}), state(1), mark(func(m *tmuxpane.Mark) {
				m.State = tmuxpane.StateActive
				m.Project = "/elsewhere"
			})
		}, "not the session's project"),
	)

	It("CS-TMUX-069 step 2: an unreadable state file proves no place, so nothing is touched", func() {
		panes := list(pn{w: 1, cmd: "zsh", path: proj})
		res := tmuxpane.Arm(tmuxpane.ArmOptions{Runner: fake, Rows: []tmuxpane.Row{row(1, mark(nil))}, Panes: panes, Shell: "zsh",
			StateErr: errors.New("not a regular file"), Probes: p})
		Expect(res[0].Verdict).To(Equal(tmuxpane.ArmMissing))
		Expect(res[0].Why).To(ContainSubstring("not a regular file"))
		Expect(writes()).To(BeEmpty())
	})

	DescribeTable("CS-TMUX-069 step 3: a busy pane is skipped and touched by nothing",
		func(x pn, why string) {
			x.w = 1
			if x.path == "" {
				x.path = proj
			}
			res := arm(list(x), state(1), "zsh", row(1, mark(nil)))
			Expect(res[0].Verdict).To(Equal(tmuxpane.ArmBusy))
			Expect(res[0].Why).To(ContainSubstring(why))
			Expect(writes()).To(BeEmpty())
		},
		Entry("it runs the launcher", pn{cmd: "claude-sandbox"}, "it runs claude-sandbox"),
		Entry("its program exited", pn{cmd: "zsh", dead: "1"}, "remain-on-exit"),
		Entry("an active mark", pn{cmd: "zsh", mark: `{"v":1,"state":"active","mode":"claude","container":"c","project":"/p","worktree":""}`}, "running session's mark"),
		Entry("a mark that does not parse", pn{cmd: "zsh", mark: "{not json"}, "does not parse"),
		Entry("a pending mark for another session", pn{cmd: "zsh",
			mark: `{"v":1,"state":"pending","mode":"claude","container":"c","project":"/p","worktree":"","conversation":"1b5e9c3a-1f2d-4e5f-8a9b-0c1d2e3f4a5b"}`},
			"pending mark for another session"),
	)

	It("CS-TMUX-069 step 3: the pane the command runs in is busy", func() {
		res := tmuxpane.Arm(tmuxpane.ArmOptions{Runner: fake, Rows: []tmuxpane.Row{row(1, mark(nil))}, State: state(1),
			Panes: list(pn{w: 1, cmd: "zsh", path: proj}), Shell: "zsh", Self: "%1", Probes: p})
		Expect(res[0].Verdict).To(Equal(tmuxpane.ArmBusy))
		Expect(res[0].Why).To(ContainSubstring("this command runs in"))
		Expect(writes()).To(BeEmpty())
	})

	It("CS-TMUX-069 step 3: a pending mark for the same session is re-armed with the chosen row", func() {
		same := mark(func(m *tmuxpane.Mark) { m.Name = "an older name" })
		res := arm(list(pn{w: 1, cmd: "zsh", path: proj, mark: same.JSON()}), state(1), "zsh", row(1, mark(nil)))
		Expect(res[0].Verdict).To(Equal(tmuxpane.ArmTyped), res[0].Why)
		Expect(setJSON("%1").Name).To(Equal("fix the build"))
	})

	It("CS-TMUX-069 step 4: a session already on screen in another pane (by container id or conversation) is skipped", func() {
		on := mark(func(m *tmuxpane.Mark) { m.State = tmuxpane.StateActive })
		panes := list(pn{w: 1, cmd: "zsh", path: proj}, pn{w: 2, cmd: "claude-sandbox", path: proj, mark: on.JSON()})
		res := arm(panes, state(1), "zsh", row(1, mark(nil)))
		Expect(res[0].Verdict).To(Equal(tmuxpane.ArmOnScreen))
		Expect(res[0].Why).To(Equal("already on screen in main:2.0"))
		Expect(writes()).To(BeEmpty())

		fake = &execx.Fake{}
		byID := mark(func(m *tmuxpane.Mark) { m.State = tmuxpane.StateActive; m.ContainerID = cid; m.Conversation = "" })
		panes = list(pn{w: 1, cmd: "zsh", path: proj}, pn{w: 2, cmd: "claude-sandbox", path: proj, mark: byID.JSON()})
		res = arm(panes, state(1), "zsh", row(1, mark(func(m *tmuxpane.Mark) { m.ContainerID = cid })))
		Expect(res[0].Verdict).To(Equal(tmuxpane.ArmOnScreen))
	})

	It("CS-TMUX-069 step 5: a final decision arms nothing; a pending one (docker down) is armed", func() {
		panes := list(pn{w: 1, cmd: "zsh", path: proj}, pn{w: 2, cmd: "zsh", path: proj})
		res := arm(panes, state(1, 2), "zsh",
			row(1, mark(func(m *tmuxpane.Mark) { m.Mode = tmuxpane.ModeRalph })),
			row(2, mark(nil)))
		Expect(res[0].Verdict).To(Equal(tmuxpane.ArmFinal))
		Expect(res[0].Why).To(ContainSubstring("a ralph run was here"))
		Expect(res[1].Verdict).To(Equal(tmuxpane.ArmTyped))
		Expect(writes()).NotTo(ContainElement(ContainSubstring("-t %1 ")))

		fake = &execx.Fake{}
		p.dockerErr = errors.New("docker version did not finish")
		res = arm(list(pn{w: 1, cmd: "zsh", path: proj}), state(1), "zsh", row(1, mark(nil)))
		Expect(res[0].Decision.Outcome).To(Equal(tmuxpane.OutcomePending))
		Expect(res[0].Verdict).To(Equal(tmuxpane.ArmTyped), "the typed restore waits for docker itself")
	})

	It("CS-TMUX-069 step 1: a row failing the checks is skipped before any probe or tmux call", func() {
		panes := list(pn{w: 1, cmd: "zsh", path: proj})
		n := len(fake.Calls)
		res := arm(panes, state(1), "zsh", row(1, mark(func(m *tmuxpane.Mark) { m.Project = "rel" })))
		Expect(res[0].Verdict).To(Equal(tmuxpane.ArmUnusable))
		Expect(res[0].Why).To(Equal("its project cannot be used"))
		Expect(fake.Calls).To(HaveLen(n))
		Expect(p.asked).To(BeEmpty())
	})

	It("CS-TMUX-069 step 6: a pane that changed since the list, or a re-check tmux does not answer, is skipped", func() {
		fake.On("tmux display-message -p -t %1 ", "vim\t"+proj+"\t0\t0\t0\t1\t0\t1\t\n", nil)
		res := arm(list(pn{w: 1, cmd: "zsh", path: proj}), state(1), "zsh", row(1, mark(nil)))
		Expect(res[0].Verdict).To(Equal(tmuxpane.ArmBusy))
		Expect(res[0].Why).To(ContainSubstring("changed since it was listed"))
		Expect(writes()).To(BeEmpty())

		fake = &execx.Fake{}
		fake.On("tmux display-message -p -t %1 ", "", execx.Fail(1))
		res = arm(list(pn{w: 1, cmd: "zsh", path: proj}), state(1), "zsh", row(1, mark(nil)))
		Expect(res[0].Verdict).To(Equal(tmuxpane.ArmFailed))
		Expect(writes()).To(BeEmpty())
	})

	It("CS-TMUX-069: a failed set-option types nothing; a failed send-keys leaves the pane marked", func() {
		fake.On("tmux set-option", "", execx.Fail(1))
		res := arm(list(pn{w: 1, cmd: "zsh", path: proj}), state(1), "zsh", row(1, mark(nil)))
		Expect(res[0].Verdict).To(Equal(tmuxpane.ArmFailed))
		Expect(fake.CommandLines()).NotTo(ContainElement(HavePrefix("tmux send-keys")))

		fake = &execx.Fake{}
		fake.On("tmux send-keys", "", execx.Fail(1))
		res = arm(list(pn{w: 1, cmd: "zsh", path: proj}), state(1), "zsh", row(1, mark(nil)))
		Expect(res[0].Verdict).To(Equal(tmuxpane.ArmMarked))
		Expect(res[0].Why).To(Equal("tmux send-keys failed"))
	})

	It("CS-TMUX-069: the listing is one bounded list-panes; a path holding a tab reads as marked; the shell is a basename", func() {
		fake.On("tmux list-panes -a", "%1\tmain\t1\t0\t4242\t1790000000\t/sock\tzsh\t/a\tb\t0\t0\t0\t1001\t1\t1\t1\t\n", nil)
		panes, _, ok := tmuxpane.ListArmPanes(fake)
		Expect(ok).To(BeTrue())
		Expect(panes).To(HaveLen(1))
		Expect(panes[0].Raw).NotTo(BeEmpty(), "the shifted fields read as a mark: --all leaves the pane alone")
		Expect(panes[0].Marked).To(BeFalse())
		Expect(fake.Calls[0].DieWithParent).To(BeTrue())

		fake.On("tmux show -gv default-shell", "/usr/bin/zsh\n", nil)
		Expect(tmuxpane.DefaultShell(fake)).To(Equal("zsh"))
		f2 := &execx.Fake{}
		f2.On("tmux show -gv default-shell", "", execx.Fail(1))
		Expect(tmuxpane.DefaultShell(f2)).To(Equal(""))
		f3 := &execx.Fake{}
		f3.On("tmux list-panes", "", execx.Fail(1))
		_, _, ok = tmuxpane.ListArmPanes(f3)
		Expect(ok).To(BeFalse())
	})

	Describe("CS-TMUX-069: the pane's own shell must lead its terminal's foreground process group", func() {
		It("CS-TMUX-069: a script, a program started from a wrapper or a root shell reads as the shell; only the pane's own process leading the group is typed into", func() {
			res := arm(list(pn{w: 1, cmd: "zsh", path: proj, tpgid: 4321}), state(1), "zsh", row(1, mark(nil)))
			Expect(res[0].Verdict).To(Equal(tmuxpane.ArmMarked))
			Expect(res[0].Why).To(Equal("a program runs in the foreground of its shell"))
			Expect(writes()).To(HaveLen(1), "marked, never typed into")

			fake = &execx.Fake{}
			res = arm(list(pn{w: 1, cmd: "zsh", path: proj}), state(1), "zsh", row(1, mark(nil)))
			Expect(res[0].Verdict).To(Equal(tmuxpane.ArmTyped), res[0].Why)
		})

		It("CS-TMUX-069: a foreground group that cannot be read fails safe: marked only", func() {
			res := arm(list(pn{w: 1, cmd: "zsh", path: proj, tpgid: -1}), state(1), "zsh", row(1, mark(nil)))
			Expect(res[0].Verdict).To(Equal(tmuxpane.ArmMarked))
			Expect(res[0].Why).To(ContainSubstring("cannot tell whether its shell is at its prompt"))
			Expect(fake.CommandLines()).NotTo(ContainElement(HavePrefix("tmux send-keys")))

			Expect(os.MkdirAll(filepath.Join(proc, "1001"), 0o755)).To(Succeed())
			for _, stat := range []string{"", "1001 (zsh S 1", "1001 (zsh) S 1 1001 1001 34816 x 0", "1001 (zsh) S 1 1001 1001 0 -1 0"} {
				Expect(os.WriteFile(filepath.Join(proc, "1001", "stat"), []byte(stat), 0o644)).To(Succeed())
				fg, known := tmuxpane.Foreground(tmuxpane.ProcOptions{ProcRoot: proc, GOOS: "linux"}, 1001)
				Expect(known).To(BeFalse(), stat)
				Expect(fg).To(BeFalse(), stat)
			}
			_, known := tmuxpane.Foreground(tmuxpane.ProcOptions{ProcRoot: proc, GOOS: "linux"}, 0)
			Expect(known).To(BeFalse(), "no pane pid")
			_, known = tmuxpane.Foreground(tmuxpane.ProcOptions{ProcRoot: proc, GOOS: "freebsd"}, 1001)
			Expect(known).To(BeFalse(), "another OS")
		})

		It("CS-TMUX-069: on darwin one bounded ps -o tpgid= reads the group; a failing ps is unknown", func() {
			f := &execx.Fake{}
			f.On("ps -o tpgid= -p 1001", "  1001\n", nil)
			fg, known := tmuxpane.Foreground(tmuxpane.ProcOptions{Runner: f, GOOS: "darwin"}, 1001)
			Expect(fg && known).To(BeTrue())
			Expect(f.Calls[0].DieWithParent).To(BeTrue())
			f = &execx.Fake{}
			f.On("ps -o tpgid= -p 1001", " 77\n", nil)
			fg, known = tmuxpane.Foreground(tmuxpane.ProcOptions{Runner: f, GOOS: "darwin"}, 1001)
			Expect(known).To(BeTrue())
			Expect(fg).To(BeFalse())
			f = &execx.Fake{}
			f.On("ps", "", execx.Fail(1))
			_, known = tmuxpane.Foreground(tmuxpane.ProcOptions{Runner: f, GOOS: "darwin"}, 1001)
			Expect(known).To(BeFalse())
		})

		It("CS-TMUX-069: under go test the real /proc panics", func() {
			Expect(func() { tmuxpane.Foreground(tmuxpane.ProcOptions{GOOS: "linux"}, 1) }).To(PanicWith(ContainSubstring("real /proc")))
		})
	})

	It("CS-TMUX-069: the keys clear the command line first (C-e C-u as key names), then the restore, then Enter", func() {
		Expect(tmuxpane.TypeKeys()).To(Equal([]string{"C-e", "C-u", "claude-sandbox tmux restore --resurrected", "C-m"}))
		Expect(tmuxpane.TypeRestore(fake, tmuxpane.CallTimeout, "%3")).To(BeTrue())
		Expect(fake.Calls[0].Args).To(Equal([]string{"send-keys", "-t", "%3", "C-e", "C-u", "claude-sandbox tmux restore --resurrected", "C-m"}))
	})

	It("CS-TMUX-069: a pane listed under several sessions is focused when any of its lines is", func() {
		panes := list(pn{w: 1, cmd: "zsh", path: proj}, pn{w: 1, cmd: "zsh", path: proj, sess: "grp", wactive: "1"})
		Expect(panes).To(HaveLen(2))
		Expect(panes[0].Focused && panes[1].Focused).To(BeTrue())
		res := arm(panes, state(1), "zsh", row(1, mark(nil)))
		Expect(res[0].Verdict).To(Equal(tmuxpane.ArmMarked))
		Expect(res[0].Why).To(Equal("a client is looking at it"))
	})

	It("CS-TMUX-069: one pane reached through two rows (a linked window, grouped sessions) is armed once: the first row wins", func() {
		panes := list(pn{w: 1, cmd: "zsh", path: proj}, pn{w: 1, cmd: "zsh", path: proj, sess: "grp"})
		st := append(state(1), tmuxpane.StatePane{Session: "grp", Window: 1, Pane: 0, Dir: proj, FullCommandSaved: true})
		other := mark(func(m *tmuxpane.Mark) { m.Conversation = "1b5e9c3a-1f2d-4e5f-8a9b-0c1d2e3f4a5b" })
		res := arm(panes, st, "zsh", row(1, mark(nil)), tmuxpane.Row{Session: "grp", Window: 1, Pane: 0, Mark: other})
		Expect(res[0].Verdict).To(Equal(tmuxpane.ArmTyped), res[0].Why)
		Expect(res[1].Verdict).To(Equal(tmuxpane.ArmBusy))
		Expect(res[1].Why).To(Equal("pane %1 is also at main:1.0, which an earlier row of this save took"))
		Expect(writes()).To(HaveLen(2), "one set-option and one send-keys")
	})
})
