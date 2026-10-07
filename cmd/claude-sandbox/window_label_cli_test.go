package main

// Spec: spec/tmux.feature (CS-TMUX-020..024) — the window label end to end
// through MainWithEnv and runSession, with tmux faked through execx.Fake: the
// launch names its window, a clean end hands it back, a pending end keeps it.

import (
	"fmt"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

// winSim is the pane's window: effective automatic-rename, name, and the two
// label options.
type winSim struct {
	mu                 sync.Mutex
	auto               bool
	name, label, owner string
}

// install answers the window's tmux calls; registered before simulatePane,
// whose catch-all would otherwise take them.
func (w *winSim) install(fake *execx.Fake) {
	fake.OnFunc("tmux display-message -p -t %7 #{window_id}", func(execx.Cmd) (string, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		a := "0"
		if w.auto {
			a = "1"
		}
		return fmt.Sprintf("@1\t%s\t%s\t%s\t%s\n", a, w.owner, w.name, w.label), nil
	})
	fake.OnFunc("tmux set-option -w ", func(c execx.Cmd) (string, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if c.Args[2] == "-u" {
			switch c.Args[len(c.Args)-1] {
			case "automatic-rename":
				w.auto = true
			case tmuxpane.LabelOption:
				w.label = ""
			case tmuxpane.LabelPaneOption:
				w.owner = ""
			}
			return "", nil
		}
		switch c.Args[len(c.Args)-2] {
		case tmuxpane.LabelOption:
			w.label = c.Args[len(c.Args)-1]
		case tmuxpane.LabelPaneOption:
			w.owner = c.Args[len(c.Args)-1]
		}
		return "", nil
	})
	fake.OnFunc("tmux rename-window ", func(c execx.Cmd) (string, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.name, w.auto = c.Args[len(c.Args)-1], false
		return "", nil
	})
}

func (w *winSim) state() (bool, string, string, string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.auto, w.name, w.label, w.owner
}

var _ = Describe("tmux window label (CS-TMUX-020..024)", func() {
	var (
		f   *cliFixture
		win *winSim
		p   *paneSim
	)
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

	BeforeEach(func() {
		f = newCLIFixture()
		f.envmap["TMUX"] = "/tmp/tmux-1000/default,4242,0"
		f.envmap["TMUX_PANE"] = "%7"
		f.env.Now = func() time.Time { return now }
		win = &winSim{auto: true, name: "bash"}
		win.install(f.fake)
		p = simulatePane(f.fake)
	})

	renames := func() []string {
		var out []string
		for _, l := range f.fake.CommandLines() {
			if strings.HasPrefix(l, "tmux rename-window") {
				out = append(out, l)
			}
		}
		return out
	}
	index := func(prefix string) int {
		for i, l := range f.fake.CommandLines() {
			if strings.HasPrefix(l, prefix) {
				return i
			}
		}
		return -1
	}

	It("CS-TMUX-020/021: a launch names its window by the project folder before the session", func() {
		var during string
		f.fake.OnFunc("docker start -ai", func(execx.Cmd) (string, error) {
			_, during, _, _ = win.state()
			return "", nil
		})
		streamEvents(f.fake, dockerEvent("die", "0"))
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(during).To(Equal("proj"))
		Expect(renames()).To(Equal([]string{"tmux rename-window -t %7 -- proj"}))
		Expect(index("tmux rename-window")).To(BeNumerically("<", index("docker start -ai")))
		Expect(f.errw.String()).NotTo(ContainSubstring("tmux"))
	})

	It("CS-TMUX-021: --name in the passthrough is the label", func() {
		streamEvents(f.fake, dockerEvent("die", "0"))
		Expect(f.run("--", "--name", "fix #12; now")).To(Equal(0), f.errw.String())
		Expect(renames()).To(Equal([]string{"tmux rename-window -t %7 -- fix 12 now"}))
	})

	It("CS-TMUX-024: a clean end hands the window back to automatic-rename", func() {
		streamEvents(f.fake, dockerEvent("die", "0"))
		Expect(f.run()).To(Equal(0), f.errw.String())
		auto, _, label, owner := win.state()
		Expect(auto).To(BeTrue())
		Expect(label).To(BeEmpty())
		Expect(owner).To(BeEmpty())
		Expect(index("tmux set-option -w -u -t %7 automatic-rename")).To(BeNumerically(">", index("tmux set-option -p -u -t %7 @claude-sandbox")))
		Expect(p.get()).To(BeEmpty())
	})

	It("CS-TMUX-024: a pane kept pending (a stop from outside) keeps its label", func() {
		f.fake.OnFunc("docker start -ai", func(execx.Cmd) (string, error) {
			p.update(func(m *tmuxpane.Mark) { m.Conversation = markConv })
			time.Sleep(20 * time.Millisecond)
			return "", nil
		})
		streamEvents(f.fake, dockerEvent("kill", ""), dockerEvent("die", "143"), dockerEvent("stop", ""))
		f.run()
		m, ok := tmuxpane.ParseMark(p.get())
		Expect(ok).To(BeTrue())
		Expect(m.State).To(Equal(tmuxpane.StatePending))
		auto, name, label, owner := win.state()
		Expect(auto).To(BeFalse())
		Expect(name).To(Equal("proj"))
		Expect(label).To(Equal("proj"))
		Expect(owner).To(Equal("%7"))
		Expect(f.fake.CommandLines()).NotTo(ContainElement(HavePrefix("tmux set-option -w -u")))
	})

	It("CS-TMUX-024, CS-TMUX-075: a crashed end keeps its label; one that falls back to an unset hands it back", func() {
		withConv := true
		f.fake.OnFunc("docker start -ai", func(execx.Cmd) (string, error) {
			if withConv {
				p.update(func(m *tmuxpane.Mark) { m.Conversation = markConv })
			}
			time.Sleep(20 * time.Millisecond)
			return "", execx.Fail(1)
		})
		streamEvents(f.fake, dockerEvent("die", "1"))
		f.run()
		m, ok := tmuxpane.ParseMark(p.get())
		Expect(ok).To(BeTrue())
		Expect(m.State).To(Equal(tmuxpane.StateCrashed))
		auto, name, label, owner := win.state()
		Expect(auto).To(BeFalse())
		Expect(name).To(Equal("proj"))
		Expect(label).To(Equal("proj"))
		Expect(owner).To(Equal("%7"))
		Expect(f.fake.CommandLines()).NotTo(ContainElement(HavePrefix("tmux set-option -w -u")))

		// No conversation known: the crashed end falls back to an unset,
		// which hands the window back as any unset does.
		f.fake.Calls = nil
		p.set("")
		win.auto, win.name, win.label, win.owner = true, "bash", "", ""
		withConv = false
		f.run()
		Expect(p.get()).To(BeEmpty())
		auto, _, label, owner = win.state()
		Expect(auto).To(BeTrue())
		Expect(label).To(BeEmpty())
		Expect(owner).To(BeEmpty())
	})

	It("CS-TMUX-022/024: a window the operator named is never renamed nor handed back", func() {
		win.auto, win.name = false, "editor"
		streamEvents(f.fake, dockerEvent("die", "0"))
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(renames()).To(BeEmpty())
		Expect(f.fake.CommandLines()).NotTo(ContainElement(HavePrefix("tmux set-option -w")))
		// The end made no window read: the launch did not own the label.
		n := 0
		for _, l := range f.fake.CommandLines() {
			if strings.HasPrefix(l, "tmux display-message") {
				n++
			}
		}
		Expect(n).To(Equal(1))
	})

	It("CS-TMUX-022: the mark records that its launch owns the label", func() {
		streamEvents(f.fake, dockerEvent("die", "0"))
		var during string
		f.fake.OnFunc("docker start -ai", func(execx.Cmd) (string, error) {
			during = p.get()
			return "", nil
		})
		Expect(f.run()).To(Equal(0), f.errw.String())
		m, ok := tmuxpane.ParseMark(during)
		Expect(ok).To(BeTrue())
		Expect(m.Labelled).To(BeTrue())
	})

	It("CS-TMUX-022: a window named by hand after the project folder keeps its name and its automatic-rename", func() {
		win.auto, win.name = false, "proj" // the same text as the label, no options
		streamEvents(f.fake, dockerEvent("die", "0"))
		var during string
		f.fake.OnFunc("docker start -ai", func(execx.Cmd) (string, error) {
			during = p.get()
			return "", nil
		})
		Expect(f.run()).To(Equal(0), f.errw.String())
		auto, name, label, _ := win.state()
		Expect(auto).To(BeFalse())
		Expect(name).To(Equal("proj"))
		Expect(label).To(BeEmpty())
		Expect(f.fake.CommandLines()).NotTo(ContainElement(HavePrefix("tmux set-option -w")))
		m, _ := tmuxpane.ParseMark(during)
		Expect(m.Labelled).To(BeFalse())
	})

	It("CS-TMUX-022: only a restore whose row was labelled may reclaim", func() {
		labelled := tmuxpane.Mark{V: 1, State: tmuxpane.StatePending, Labelled: true}.JSON()
		plain := tmuxpane.Mark{V: 1, State: tmuxpane.StatePending}.JSON()
		Expect(reclaims(&paneMark{prior: &labelled})).To(BeTrue())
		Expect(reclaims(&paneMark{prior: &plain})).To(BeFalse())
		Expect(reclaims(&paneMark{})).To(BeFalse(), "a hand launch hands in no prior")
	})

	It("CS-TMUX-021: a restore attach with no --name takes its pending row's user-given name", func() {
		prior := tmuxpane.Mark{V: 1, State: tmuxpane.StatePending, Project: "/w/proj",
			Conversation: markConv, Name: "fix the restore", NameSource: tmuxpane.NameSourceUser}.JSON()
		pm := &paneMark{prior: &prior}
		Expect(labelName(pm)).To(Equal("fix the restore"))
		pm.name = "given"
		Expect(labelName(pm)).To(Equal("given"))
		derived := tmuxpane.Mark{V: 1, State: tmuxpane.StatePending, Name: "topic", NameSource: "derived"}.JSON()
		Expect(labelName(&paneMark{prior: &derived})).To(Equal(""))
		Expect(labelName(&paneMark{})).To(Equal(""))
	})
})
