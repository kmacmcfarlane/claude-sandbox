package tmuxpane_test

// Spec: spec/tmux.feature (CS-TMUX-020..026, CS-TMUX-041..044) — the window
// label: the label rule, the launch's cases, the end's hand-back and the save
// hook's refresh, with tmux faked through execx.Fake (a small window
// simulator) — never a real tmux. The launcher wiring is in
// cmd/claude-sandbox/pane_mark_cli_test.go.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

// windowSim is one tmux window as the label code sees it: the effective
// automatic-rename, its name, the two user options and its panes.
type windowSim struct {
	mu    sync.Mutex
	auto  bool
	name  string
	label string
	owner string
	panes []string
	// readFails makes the window read fail; garbage makes it unparsable.
	readFails bool
	garbage   bool
}

func (w *windowSim) install(fake *execx.Fake) {
	fake.OnFunc("tmux display-message -p -t ", func(c execx.Cmd) (string, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.readFails {
			return "", execx.Fail(1)
		}
		if w.garbage {
			return "no such format\n", nil
		}
		a := "0"
		if w.auto {
			a = "1"
		}
		return fmt.Sprintf("@3\t%s\t%s\t%s\t%s\n", a, w.owner, w.name, w.label), nil
	})
	fake.OnFunc("tmux list-panes -t ", func(c execx.Cmd) (string, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		return strings.Join(w.panes, "\n") + "\n", nil
	})
	fake.OnFunc("tmux set-option -w ", func(c execx.Cmd) (string, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		unset := c.Args[2] == "-u"
		opt := c.Args[len(c.Args)-1]
		if !unset {
			opt = c.Args[len(c.Args)-2]
		}
		val := c.Args[len(c.Args)-1]
		switch opt {
		case tmuxpane.LabelOption:
			if unset {
				w.label = ""
			} else {
				w.label = val
			}
		case tmuxpane.LabelPaneOption:
			if unset {
				w.owner = ""
			} else {
				w.owner = val
			}
		case "automatic-rename":
			if unset {
				w.auto = true // the global "on"
			}
		}
		return "", nil
	})
	fake.OnFunc("tmux rename-window ", func(c execx.Cmd) (string, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.name, w.auto = c.Args[len(c.Args)-1], false // tmux: rename-window turns it off
		return "", nil
	})
}

// tmuxCalls is every recorded tmux command line.
func tmuxCalls(fake *execx.Fake) []string {
	var out []string
	for _, l := range fake.CommandLines() {
		if strings.HasPrefix(l, "tmux ") {
			out = append(out, l)
		}
	}
	return out
}

const readLine = "tmux display-message -p -t %7 #{window_id}\t#{automatic-rename}\t#{@claude-sandbox-label-pane}\t#{window_name}\t#{@claude-sandbox-label}"

var _ = Describe("window labels (CS-TMUX-020..026, 041..044)", func() {
	var (
		fake *execx.Fake
		win  *windowSim
		pane tmuxpane.Pane
	)
	BeforeEach(func() {
		fake = &execx.Fake{}
		win = &windowSim{auto: true, name: "bash", panes: []string{"%7"}}
		win.install(fake)
		pane = tmuxpane.Pane{Runner: fake, ID: "%7"}
	})

	Describe("CS-TMUX-021: the label", func() {
		It("CS-TMUX-021: cleaned of control characters, #, \\ and ;, trimmed and capped at 40", func() {
			Expect(tmuxpane.Label("  fix\t#12;  the\x1b[31m bug\\ ​ ")).To(Equal("fix 12 the [31m bug"))
			Expect(tmuxpane.Label("--rm -rf")).To(Equal("rm -rf"))
			Expect(tmuxpane.Label("a;")).To(Equal("a"))
			Expect(tmuxpane.Label(" ;#\\ ")).To(Equal(""))
			long := strings.Repeat("é", 39) + " xyz"
			Expect([]rune(tmuxpane.Label(long))).To(HaveLen(39), "cut to 40, then the trailing space trimmed")
			Expect([]rune(tmuxpane.Label(strings.Repeat("ab", 30)))).To(HaveLen(tmuxpane.LabelMax))
			for _, s := range []string{"  fix\t#12;  the bug ", long, "--x", strings.Repeat("ab", 30)} {
				l := tmuxpane.Label(s)
				Expect(tmuxpane.Label(l)).To(Equal(l), "idempotent: %q", s)
			}
		})

		It("CS-TMUX-021: the user-given name, else the project folder's base name", func() {
			Expect(tmuxpane.LaunchLabel("my task", "/w/proj")).To(Equal("my task"))
			Expect(tmuxpane.LaunchLabel("", "/w/proj/")).To(Equal("proj"))
			Expect(tmuxpane.LaunchLabel(" #; ", "/w/proj")).To(Equal("proj"))
			Expect(tmuxpane.LaunchLabel("", "/")).To(Equal(""))
			Expect(tmuxpane.LaunchLabel("", "")).To(Equal(""))
		})

		It("CS-TMUX-021: --name, --name=, -n and -n<v> before the scan's stop; the last wins", func() {
			Expect(tmuxpane.NameArg([]string{"--name", "a"})).To(Equal("a"))
			Expect(tmuxpane.NameArg([]string{"--name=a b"})).To(Equal("a b"))
			Expect(tmuxpane.NameArg([]string{"-n", "a", "--effort", "high", "-nb"})).To(Equal("b"))
			Expect(tmuxpane.NameArg([]string{"--resume", convID, "--name", "x"})).To(Equal("x"))
			Expect(tmuxpane.NameArg([]string{"--", "--name", "x"})).To(Equal(""))
			Expect(tmuxpane.NameArg([]string{"fix it", "--name", "x"})).To(Equal(""))
			Expect(tmuxpane.NameArg([]string{"--frobnicate", "v", "--name", "x"})).To(Equal(""), "unknown arity stops")
			Expect(tmuxpane.NameArg(nil)).To(Equal(""))
			r := tmuxpane.Record(nil, "", []string{"--name", "task"}, nil)
			Expect(r.Name).To(Equal("task"))
			Expect(r.LabelValue()).To(Equal(""), "the name is never a launchflags entry")
		})

		It("CS-TMUX-021: an empty label writes nothing", func() {
			Expect(pane.BeginLabel("")).To(BeFalse())
			Expect(fake.Calls).To(BeEmpty())
		})
	})

	It("CS-TMUX-020: an automatic window is named: one read, the options, then rename-window", func() {
		Expect(pane.BeginLabel("proj")).To(BeTrue())
		Expect(tmuxCalls(fake)).To(Equal([]string{
			readLine,
			"tmux set-option -w -t %7 @claude-sandbox-label proj",
			"tmux set-option -w -t %7 @claude-sandbox-label-pane %7",
			"tmux rename-window -t %7 -- proj",
		}))
		Expect(win.name).To(Equal("proj"))
		Expect(win.auto).To(BeFalse())
		for _, c := range fake.Calls {
			Expect(c.DieWithParent).To(BeTrue(), "bounded like the mark's calls (CS-TMUX-025)")
		}
	})

	It("CS-TMUX-020: the rename runs even over an equal name — it is what turns automatic-rename off", func() {
		win.name = "proj"
		Expect(pane.BeginLabel("proj")).To(BeTrue())
		Expect(tmuxCalls(fake)).To(ContainElement("tmux rename-window -t %7 -- proj"))
	})

	It("CS-TMUX-020: a failed option write stops before the rename", func() {
		fake2 := &execx.Fake{}
		fake2.On("tmux set-option -w", "", execx.Fail(1))
		win.install(fake2)
		p := tmuxpane.Pane{Runner: fake2, ID: "%7"}
		Expect(p.BeginLabel("proj")).To(BeFalse())
		for _, l := range tmuxCalls(fake2) {
			Expect(l).NotTo(HavePrefix("tmux rename-window"))
		}
	})

	Describe("CS-TMUX-022: a window that is not automatic", func() {
		It("CS-TMUX-022: a name the operator gave is left alone", func() {
			win.auto, win.name = false, "editor"
			Expect(pane.BeginLabel("proj")).To(BeFalse())
			Expect(tmuxCalls(fake)).To(Equal([]string{readLine}))
		})

		It("CS-TMUX-022: claude-sandbox's own label, owned by this pane, is taken over and renamed", func() {
			win.auto, win.name, win.label, win.owner = false, "old", "old", "%7"
			Expect(pane.BeginLabel("proj")).To(BeTrue())
			Expect(win.name).To(Equal("proj"))
			Expect(tmuxCalls(fake)).NotTo(ContainElement(HavePrefix("tmux list-panes")), "own pane: no listing")
		})

		It("CS-TMUX-022: claude-sandbox's own label whose owner pane is gone is taken over", func() {
			win.auto, win.name, win.label, win.owner = false, "proj", "proj", "%9"
			win.panes = []string{"%7", "%8"}
			Expect(pane.BeginLabel("proj")).To(BeTrue())
			Expect(win.owner).To(Equal("%7"))
			Expect(tmuxCalls(fake)).To(ContainElement("tmux list-panes -t %7 -F #{pane_id}"))
			Expect(tmuxCalls(fake)).NotTo(ContainElement(HavePrefix("tmux rename-window")), "same name: no rename")
		})

		It("CS-TMUX-022: a window resurrect restored with this launch's label is reclaimed, not renamed", func() {
			win.auto, win.name = false, "my task"
			Expect(pane.BeginLabel("my task")).To(BeTrue())
			Expect(tmuxCalls(fake)).To(Equal([]string{
				readLine,
				"tmux set-option -w -t %7 @claude-sandbox-label my task",
				"tmux set-option -w -t %7 @claude-sandbox-label-pane %7",
			}))
		})

		It("CS-TMUX-022: a restored window whose name is another label is a hand name", func() {
			win.auto, win.name = false, "other task"
			Expect(pane.BeginLabel("proj")).To(BeFalse())
			Expect(tmuxCalls(fake)).To(Equal([]string{readLine}))
		})
	})

	It("CS-TMUX-023: one pane owns a shared window's name; the other writes nothing", func() {
		win.auto, win.name, win.label, win.owner = false, "proj", "proj", "%8"
		win.panes = []string{"%7", "%8"}
		Expect(pane.BeginLabel("other")).To(BeFalse())
		Expect(win.name).To(Equal("proj"))
		Expect(win.owner).To(Equal("%8"))
		// A failed listing counts as present: never fight.
		fake2 := &execx.Fake{}
		fake2.On("tmux list-panes", "", execx.Fail(1))
		win.install(fake2)
		Expect(tmuxpane.Pane{Runner: fake2, ID: "%7"}.BeginLabel("other")).To(BeFalse())
		// And the non-owner's end hands nothing back.
		fake.Calls = nil
		pane.EndLabel()
		Expect(tmuxCalls(fake)).To(Equal([]string{readLine}))
		Expect(win.label).To(Equal("proj"))
	})

	Describe("CS-TMUX-024: the end hands the window back", func() {
		It("CS-TMUX-024: still ours: automatic-rename unset at the window level, then both options", func() {
			Expect(pane.BeginLabel("proj")).To(BeTrue())
			fake.Calls = nil
			pane.EndLabel()
			Expect(tmuxCalls(fake)).To(Equal([]string{
				readLine,
				"tmux set-option -w -u -t %7 automatic-rename",
				"tmux set-option -w -u -t %7 @claude-sandbox-label",
				"tmux set-option -w -u -t %7 @claude-sandbox-label-pane",
			}))
			Expect(win.auto).To(BeTrue())
			Expect(win.label).To(BeEmpty())
			Expect(win.owner).To(BeEmpty())
		})

		It("CS-TMUX-042: renamed by hand since: the name and automatic-rename stay, only the options go", func() {
			Expect(pane.BeginLabel("proj")).To(BeTrue())
			win.name = "mine" // prefix + ,
			fake.Calls = nil
			pane.EndLabel()
			Expect(tmuxCalls(fake)).To(Equal([]string{
				readLine,
				"tmux set-option -w -u -t %7 @claude-sandbox-label",
				"tmux set-option -w -u -t %7 @claude-sandbox-label-pane",
			}))
			Expect(win.name).To(Equal("mine"))
			Expect(win.auto).To(BeFalse())
		})
	})

	Describe("CS-TMUX-025: bounded and silent", func() {
		It("CS-TMUX-025: a failed or unparsable read writes nothing", func() {
			win.readFails = true
			Expect(pane.BeginLabel("proj")).To(BeFalse())
			pane.EndLabel()
			Expect(tmuxCalls(fake)).To(Equal([]string{readLine, readLine}))
			win.readFails, win.garbage = false, true
			fake.Calls = nil
			Expect(pane.BeginLabel("proj")).To(BeFalse())
			Expect(tmuxCalls(fake)).To(Equal([]string{readLine}))
		})

		It("CS-TMUX-025: a tmux that never answers is killed after CallTimeout", func() {
			DeferCleanup(func(d time.Duration) { tmuxpane.CallTimeout = d }, tmuxpane.CallTimeout)
			tmuxpane.CallTimeout = 50 * time.Millisecond
			h := &stallRunner{Fake: &execx.Fake{}, hang: []string{"display-message"}}
			start := time.Now()
			Expect(tmuxpane.Pane{Runner: h, ID: "%7"}.BeginLabel("proj")).To(BeFalse())
			Expect(time.Since(start)).To(BeNumerically("<", time.Second))
			Expect(h.Fake.CommandLines()).To(HaveLen(1))
		})
	})

	It("CS-TMUX-026: no tmux.conf line: the label is a plain rename-window, nothing in the mark", func() {
		Expect(pane.BeginLabel("proj")).To(BeTrue())
		for _, l := range tmuxCalls(fake) {
			Expect(l).NotTo(ContainSubstring("automatic-rename-format"))
			Expect(l).NotTo(HavePrefix("tmux set-option -g"))
		}
		// tmux-resurrect restores the name with automatic-rename off (the
		// window-level value rename-window left); the next launch reclaims
		// it (CS-TMUX-022) without a rename.
		restored := &windowSim{auto: false, name: win.name, panes: []string{"%7"}}
		fake2 := &execx.Fake{}
		restored.install(fake2)
		Expect(tmuxpane.Pane{Runner: fake2, ID: "%7"}.BeginLabel("proj")).To(BeTrue())
		Expect(tmuxCalls(fake2)).NotTo(ContainElement(HavePrefix("tmux rename-window")))
		Expect(tmuxpane.Mark{}.JSON()).NotTo(ContainSubstring("label"))
	})
})

var _ = Describe("the save hook's window-label refresh (CS-TMUX-041..044)", func() {
	var (
		dir, reg, proj, state string
		fake                  *execx.Fake
		win                   *windowSim
		logs                  []string
		now                   = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
		since                 = now.Add(-time.Hour).UnixMilli()
	)
	mark := func(mut func(*tmuxpane.Mark)) tmuxpane.Mark {
		m := tmuxpane.Mark{
			V: 1, State: tmuxpane.StateActive, Mode: tmuxpane.ModeClaude,
			Container: "claude-sandbox-work-proj-abc123-murre", ContainerID: saveID,
			Instance: "murre", Project: proj, CwdRoot: proj, Class: "7", Since: since,
			RegistryDir: reg,
		}
		if mut != nil {
			mut(&m)
		}
		return m
	}
	record := func(name, source string) {
		b := fmt.Sprintf(`{"pid":7,"sessionId":%q,"cwd":%q,"startedAt":%d,"name":%q,"nameSource":%q}`, convID, proj, since, name, source)
		Expect(os.WriteFile(filepath.Join(reg, "7.json"), []byte(b), 0o600)).To(Succeed())
	}
	run := func(m tmuxpane.Mark, cmd string) tmuxpane.SaveResult {
		fake.On("tmux list-panes -a", fmt.Sprintf("main\t1\t0\t%%7\t%s\t%d\t%d\t%s\n", cmd, srvPID, srvStart, m.JSON()), nil)
		fake.On("tmux show-options", m.JSON()+"\n", nil)
		fake.On("docker ps", saveID+"\t"+m.Container+"\trunning\n", nil)
		res, err := tmuxpane.Save(state, tmuxpane.SaveOptions{
			Runner: fake, Now: func() time.Time { return now },
			Logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) },
		})
		Expect(err).NotTo(HaveOccurred())
		return res
	}
	BeforeEach(func() {
		base, err := filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		dir, reg, proj = filepath.Join(base, "resurrect"), filepath.Join(base, "sessions"), filepath.Join(base, "proj")
		for _, d := range []string{dir, reg, proj} {
			Expect(os.MkdirAll(d, 0o700)).To(Succeed())
		}
		state = filepath.Join(dir, "tmux_resurrect_20260929T120000.txt")
		line := strings.Join([]string{"pane", "main", "1", "1", ":*", "0", "title", ":" + proj, "1", "claude-sandbox", ":claude-sandbox"}, "\t")
		Expect(os.WriteFile(state, []byte(line+"\n"), 0o600)).To(Succeed())
		fake = &execx.Fake{}
		// The window the launch named "proj" and owns from pane %7.
		win = &windowSim{auto: false, name: "proj", label: "proj", owner: "%7", panes: []string{"%7"}}
		win.install(fake)
		logs = nil
		DeferCleanup(func(d, c time.Duration) { tmuxpane.SaveDeadline, tmuxpane.CallTimeout = d, c },
			tmuxpane.SaveDeadline, tmuxpane.CallTimeout)
	})

	It("CS-TMUX-041: a /rename (name source user) renames the owned window: option, then rename", func() {
		record("fix the restore", "user")
		res := run(mark(nil), "claude-sandbox")
		Expect(res.Relabeled).To(Equal(1))
		Expect(win.name).To(Equal("fix the restore"))
		Expect(win.label).To(Equal("fix the restore"))
		Expect(win.auto).To(BeFalse())
		calls := tmuxCalls(fake)
		n := len(calls)
		Expect(calls[n-2]).To(Equal("tmux set-option -w -t %7 @claude-sandbox-label fix the restore"))
		Expect(calls[n-1]).To(Equal("tmux rename-window -t %7 -- fix the restore"))
		// Already the name: one read, no write.
		fake = &execx.Fake{}
		win.install(fake)
		res = run(mark(func(m *tmuxpane.Mark) {
			m.Conversation, m.Name, m.NameSource = convID, "fix the restore", "user"
		}), "claude-sandbox")
		Expect(res.Relabeled).To(Equal(0))
		Expect(tmuxCalls(fake)).NotTo(ContainElement(HavePrefix("tmux rename-window")))
	})

	It("CS-TMUX-042: a window renamed by hand is never renamed again", func() {
		record("fix the restore", "user")
		win.name = "mine"
		res := run(mark(nil), "claude-sandbox")
		Expect(res.Relabeled).To(Equal(0))
		Expect(win.name).To(Equal("mine"))
		Expect(tmuxCalls(fake)).NotTo(ContainElement(HavePrefix("tmux rename-window")))
	})

	It("CS-TMUX-043: after the write-backs; a failed read is logged; the deadline leaves the rest", func() {
		record("fix the restore", "user")
		run(mark(nil), "claude-sandbox")
		calls := tmuxCalls(fake)
		lastWB, read := -1, -1
		for i, l := range calls {
			if strings.HasPrefix(l, "tmux set-option -p -t %7 @claude-sandbox ") {
				lastWB = i
			}
			if strings.HasPrefix(l, "tmux display-message") {
				read = i
			}
		}
		Expect(lastWB).To(BeNumerically(">=", 0))
		Expect(read).To(BeNumerically(">", lastWB))

		fake = &execx.Fake{}
		win.readFails = true
		win.install(fake)
		logs = nil
		Expect(run(mark(nil), "claude-sandbox").Relabeled).To(Equal(0))
		Expect(logs).To(ContainElement(ContainSubstring("window label not refreshed")))

		// A refresh read that never answers is cut at the deadline.
		tmuxpane.SaveDeadline, tmuxpane.CallTimeout = 300*time.Millisecond, 100*time.Millisecond
		h := &stallRunner{Fake: &execx.Fake{}, hang: []string{"display-message"}}
		m := mark(nil)
		h.Fake.On("tmux list-panes -a", fmt.Sprintf("main\t1\t0\t%%7\tclaude-sandbox\t%d\t%d\t%s\n", srvPID, srvStart, m.JSON()), nil)
		h.Fake.On("tmux show-options", m.JSON()+"\n", nil)
		h.Fake.On("docker ps", saveID+"\t"+m.Container+"\trunning\n", nil)
		start := time.Now()
		_, err := tmuxpane.Save(state, tmuxpane.SaveOptions{Runner: h, Now: func() time.Time { return now }})
		Expect(err).NotTo(HaveOccurred())
		Expect(time.Since(start)).To(BeNumerically("<", 2*time.Second))
	})

	Describe("CS-TMUX-044: what the refresh never touches", func() {
		It("CS-TMUX-044: a name the user did not give (never back to the folder)", func() {
			record("auto topic", "derived")
			Expect(run(mark(nil), "claude-sandbox").Relabeled).To(Equal(0))
			Expect(tmuxCalls(fake)).NotTo(ContainElement(HavePrefix("tmux display-message")))
		})

		It("CS-TMUX-044: a pending mark", func() {
			record("fix the restore", "user")
			m := mark(func(m *tmuxpane.Mark) {
				m.State, m.Conversation, m.Name, m.NameSource = tmuxpane.StatePending, convID, "fix the restore", "user"
			})
			Expect(run(m, "bash").Relabeled).To(Equal(0))
			Expect(win.name).To(Equal("proj"))
		})

		It("CS-TMUX-044: a window owned by another pane", func() {
			record("fix the restore", "user")
			win.owner = "%8"
			Expect(run(mark(nil), "claude-sandbox").Relabeled).To(Equal(0))
			Expect(win.name).To(Equal("proj"))
		})

		It("CS-TMUX-044: a pane whose mark changed since the list", func() {
			record("fix the restore", "user")
			m := mark(nil)
			fake.On("tmux show-options", mark(func(m *tmuxpane.Mark) { m.Container = "other" }).JSON()+"\n", nil)
			Expect(run(m, "claude-sandbox").Relabeled).To(Equal(0))
			Expect(win.name).To(Equal("proj"))
		})
	})
})
