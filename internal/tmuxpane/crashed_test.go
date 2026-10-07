package tmuxpane_test

// Spec: spec/tmux.feature (CS-TMUX-075, CS-TMUX-077, CS-TMUX-079, with
// CS-TMUX-011/017/046/051) — a crashed pane: the mark's state and fields,
// decision row 19 and its hint line, the CS-TMUX-017 note over a crashed
// prior, and the hint's reclaim-only window label. tmux is faked through
// execx.Fake (label_test.go's window simulator); the launcher's end rules
// are in cmd/claude-sandbox/pane_mark_cli_test.go.

import (
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

var _ = Describe("a crashed pane (CS-TMUX-075..079)", func() {
	cde := "/home/u/work claude"
	ended := time.Date(2026, 10, 5, 14, 2, 0, 0, time.Local)
	crashed := func(mut func(*tmuxpane.Mark)) tmuxpane.Mark {
		m := tmuxpane.Mark{V: 1, State: tmuxpane.StateCrashed, Mode: tmuxpane.ModeClaude, Container: "c",
			Instance: "otter", Project: "/home/u/myproj", ConfigDirEnv: &cde, Conversation: convID,
			Name: "fix it", NameSource: "user", EndedAt: ended.UnixMilli(), ExitCode: 137, OOMKilled: true}
		if mut != nil {
			mut(&m)
		}
		return m
	}
	cmd := "cd /home/u/myproj && CLAUDE_CONFIG_DIR='/home/u/work claude' claude-sandbox --new --no-worktree -- --resume " + convID + " --name 'fix it'"

	It("CS-TMUX-011, CS-TMUX-075: a crashed mark is a v1 mark with endedAt, exitCode and oomKilled; old fields untouched", func() {
		m := crashed(nil)
		raw := m.JSON()
		Expect(raw).To(ContainSubstring(`"v":1,"state":"crashed"`))
		Expect(raw).To(ContainSubstring(`"endedAt":` + fmt.Sprint(ended.UnixMilli()) + `,"exitCode":137,"oomKilled":true`))
		back, ok := tmuxpane.ParseMark(raw)
		Expect(ok).To(BeTrue(), "ParseMark accepts it with v 1: an old reader drops or clears it, never launches it")
		Expect(back).To(Equal(m))
		Expect(tmuxpane.Mark{V: 1, State: tmuxpane.StateActive}.JSON()).NotTo(ContainSubstring("exitCode"), "omitted unless crashed")
	})

	It("CS-TMUX-075: the clean exit codes are 0 and 78 only", func() {
		Expect(tmuxpane.CleanExitCodes).To(Equal([]int{0, 78}))
		for _, c := range []int{0, 78} {
			Expect(tmuxpane.CleanExit(c)).To(BeTrue(), "%d", c)
		}
		for _, c := range []int{1, 2, 130, 137, 143, 255} {
			Expect(tmuxpane.CleanExit(c)).To(BeFalse(), "%d", c)
		}
	})

	Describe("CS-TMUX-077: decision row 19", func() {
		It("CS-TMUX-077, CS-TMUX-051: a crashed row is row 19, decided right after row 3 with NO probe (a nil Probes would panic)", func() {
			r := &tmuxpane.Row{Session: "main", Window: 1, Pane: 0, Mark: crashed(nil)}
			var d tmuxpane.Decision
			Expect(func() { d = tmuxpane.Decide(r, "main:1.0", nil) }).NotTo(Panic())
			Expect(d.Row).To(Equal(19))
			Expect(d.Outcome).To(Equal(tmuxpane.OutcomeHint))
			Expect(d.Manual).To(Equal(cmd))
			Expect(d.Would()).To(Equal("print the crash hint and forget the row; nothing is started"))
			p := &probes{}
			tmuxpane.Decide(r, "main:1.0", p)
			Expect(p.asked).To(BeEmpty())

			// Row 3 still comes first: an unusable crashed row is cleared.
			bad := *r
			bad.Mark.Mode = tmuxpane.ModeJoin
			Expect(tmuxpane.Decide(&bad, "main:1.0", nil).Row).To(Equal(3))
		})

		It("CS-TMUX-077: the hint's exact shape, with and without the date, the OOM words and the launch flags", func() {
			Expect(tmuxpane.CrashHint(crashed(nil))).To(Equal("'fix it' crashed in this pane on 2026-10-05 14:02 (exit 137, killed by the OOM killer); not restarted. Resume it with: " + cmd))
			Expect(tmuxpane.CrashHint(crashed(func(m *tmuxpane.Mark) { m.EndedAt, m.OOMKilled, m.ExitCode = 0, false, 1 }))).
				To(Equal("'fix it' crashed in this pane (exit 1); not restarted. Resume it with: " + cmd))
			Expect(tmuxpane.CrashHint(crashed(func(m *tmuxpane.Mark) { m.Unreplayed = []string{"--docker-socket", "--ssh"} }))).
				To(HaveSuffix("; not restarted. Resume it with: " + cmd + "; it was also launched with --docker-socket, --ssh"))
			// The name is cleaned for display (a zero-width space goes); a
			// derived name is not replayed with --name.
			h := tmuxpane.CrashHint(crashed(func(m *tmuxpane.Mark) { m.Name = "fix​it"; m.NameSource = "derived" }))
			Expect(h).To(HavePrefix("'fixit' crashed in this pane"))
			Expect(h).NotTo(ContainSubstring("--name"))
			Expect(tmuxpane.CrashHint(crashed(func(m *tmuxpane.Mark) { m.Name = "" }))).To(HavePrefix("'otter' crashed"))
		})

		It("CS-TMUX-017: a hand launch over a crashed prior naming another conversation gets the crash note; silent when it resumes that id", func() {
			raw := crashed(nil).JSON()
			Expect(tmuxpane.PendingNote(raw, "")).To(Equal("Note: 'fix it' (" + convID + ") crashed in this pane (exit 137); resume it with: " + cmd))
			Expect(tmuxpane.PendingNote(raw, strings.ToUpper(convID))).To(BeEmpty())
			bidi := crashed(func(m *tmuxpane.Mark) { m.Project = "/home/u/‮projym" })
			Expect(tmuxpane.PendingNote(bidi.JSON(), "")).To(Equal("Note: a session that crashed in this pane (" + convID +
				") left a mark that holds unprintable values; no resume command is shown"))
		})
	})

	Describe("CS-TMUX-079: the hint reclaims a labelled restored window, and never renames", func() {
		var (
			fake *execx.Fake
			win  *windowSim
			pane tmuxpane.Pane
		)
		BeforeEach(func() {
			fake = &execx.Fake{}
			// What resurrect restores: the saved name, automatic-rename
			// off, and no user options.
			win = &windowSim{auto: false, name: "fix it", panes: []string{"%7"}}
			win.install(fake)
			pane = tmuxpane.Pane{Runner: fake, ID: "%7"}
		})
		labelled := func(mut func(*tmuxpane.Mark)) tmuxpane.Mark {
			return crashed(func(m *tmuxpane.Mark) {
				m.Labelled = true
				if mut != nil {
					mut(m)
				}
			})
		}
		renames := func() int {
			n := 0
			for _, l := range tmuxCalls(fake) {
				if strings.HasPrefix(l, "tmux rename-window") {
					n++
				}
			}
			return n
		}

		It("CS-TMUX-079: off, no option, named the label: both options to this pane, the confirming read, no rename", func() {
			Expect(pane.ReclaimLabel(labelled(nil))).To(BeTrue())
			Expect(win.label).To(Equal("fix it"))
			Expect(win.owner).To(Equal("%7"))
			Expect(win.name).To(Equal("fix it"))
			Expect(renames()).To(BeZero())
			Expect(tmuxCalls(fake)).To(Equal([]string{readLine,
				"tmux set-option -w -t %7 @claude-sandbox-label fix it",
				"tmux set-option -w -t %7 @claude-sandbox-label-pane %7", readLine}))
		})

		It("CS-TMUX-079: the folder name when the conversation's name was not given by the user", func() {
			win.name = "myproj"
			Expect(pane.ReclaimLabel(labelled(func(m *tmuxpane.Mark) { m.NameSource = "derived" }))).To(BeTrue())
			Expect(win.label).To(Equal("myproj"))
		})

		It("CS-TMUX-079: an automatic window, one still carrying options, another name, or an unlabelled row: no write", func() {
			for name, setup := range map[string]func() tmuxpane.Mark{
				"automatic":    func() tmuxpane.Mark { win.auto = true; return labelled(nil) },
				"live options": func() tmuxpane.Mark { win.label, win.owner = "fix it", "%3"; return labelled(nil) },
				"another name": func() tmuxpane.Mark { win.name = "notes"; return labelled(nil) },
				"not labelled": func() tmuxpane.Mark { return crashed(nil) },
			} {
				fake.Calls = nil
				win.auto, win.name, win.label, win.owner = false, "fix it", "", ""
				m := setup()
				Expect(pane.ReclaimLabel(m)).To(BeFalse(), name)
				for _, l := range tmuxCalls(fake) {
					Expect(l).NotTo(HavePrefix("tmux set-option"), name)
					Expect(l).NotTo(HavePrefix("tmux rename-window"), name)
				}
				if name == "not labelled" {
					Expect(tmuxCalls(fake)).To(BeEmpty(), "an unlabelled row makes no label call")
				}
			}
		})
	})
})
