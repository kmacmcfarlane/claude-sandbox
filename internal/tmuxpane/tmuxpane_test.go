package tmuxpane_test

// Spec: spec/tmux.feature (CS-TMUX-010..017) — the pane mark's pure parts:
// the replay allowlist and names-only scan (decision 49), the mark JSON, the
// tmux argv, and the pending-mark note. The launcher paths are covered end to
// end in cmd/claude-sandbox/pane_mark_cli_test.go.

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

const convID = "0b5e9c3a-1f2d-4e5f-8a9b-0c1d2e3f4a5b"

var _ = Describe("tmuxpane", func() {
	Describe("CS-TMUX-013: replayed flags and names-only unreplayed flags", func() {
		env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

		It("CS-TMUX-013: allowlisted claude flags are replayed with their values, as given", func() {
			r := tmuxpane.Record(nil, "", []string{
				"--add-dir", "/a", "/b", "--effort=high", "--append-system-prompt", "be brief",
				"--agent", "reviewer", "--disallowedTools", "Bash", "--tools", "Read", "Edit",
				"--strict-mcp-config", "--bare", "--restricted", "--safe-mode",
				"--append-system-prompt-file", "/p.md", "--disallowed-tools=Write",
			}, nil)
			Expect(r.Replay).To(Equal([]string{
				"--add-dir", "/a", "/b", "--effort=high", "--append-system-prompt", "be brief",
				"--agent", "reviewer", "--disallowedTools", "Bash", "--tools", "Read", "Edit",
				"--strict-mcp-config", "--bare", "--restricted", "--safe-mode",
				"--append-system-prompt-file", "/p.md", "--disallowed-tools=Write",
			}))
			Expect(r.Unreplayed).To(BeEmpty())
		})

		It("CS-TMUX-013: every other flag given is named, never with its value", func() {
			r := tmuxpane.Record(
				[]string{"--dangerous", "--docker-socket", "--ssh", "--git", "--aws", "--package-caches", "--rebuild", "--new"},
				"", []string{
					"--permission-mode", "plan", "--mcp-config", "/m.json", "/n.json",
					"--settings={\"apiKeyHelper\":\"secret\"}", "--allowedTools", "Bash(rm *)", "--verbose",
				},
				env(map[string]string{
					"CLAUDE_SANDBOX_DANGEROUS":                          "1",
					"CLAUDE_SANDBOX_HOST_ACCESS_AWS_ENABLED":            "true",
					"CLAUDE_SANDBOX_WORKTREE":                           "1",
					"CLAUDE_SANDBOX_REPO_ROOT":                          "/repo",
					"CLAUDE_SANDBOX_OOM_SCORE_ADJ":                      "",
					"CLAUDE_SANDBOX_HOST_ACCESS_PACKAGE_CACHES_ENABLED": "1",
				}))
			Expect(r.Replay).To(BeEmpty())
			Expect(r.Unreplayed).To(Equal([]string{
				"--dangerous", "--docker-socket", "--ssh", "--git", "--aws", "--package-caches",
				"CLAUDE_SANDBOX_DANGEROUS", "CLAUDE_SANDBOX_HOST_ACCESS_AWS_ENABLED",
				"CLAUDE_SANDBOX_HOST_ACCESS_PACKAGE_CACHES_ENABLED",
				"--permission-mode", "--mcp-config", "--settings", "--allowedTools", "--verbose",
			}))
			for _, n := range r.Unreplayed {
				Expect(n).NotTo(ContainSubstring("secret"))
				Expect(n).NotTo(ContainSubstring("plan"))
			}
		})

		It("CS-TMUX-013: the session-selection family is never named, and --model is the model", func() {
			r := tmuxpane.Record(nil, "opus", []string{
				"--resume", convID, "--fork-session", "-n", "fix it", "--name=x", "-c", "--session-id", convID,
			}, nil)
			Expect(r.Unreplayed).To(BeEmpty())
			Expect(r.Model).To(Equal("opus"))
			r = tmuxpane.Record(nil, "opus", []string{"--model", "sonnet"}, nil)
			Expect(r.Model).To(Equal("sonnet"), "claude's own --model comes after the launcher's and wins")
			Expect(r.Unreplayed).To(BeEmpty())
		})

		It("CS-TMUX-013: the scan stops at -- or the first positional, and after an unknown flag with a word", func() {
			_, named, _ := tmuxpane.ScanPassthrough([]string{"--verbose", "fix", "--bare", "--agent", "x"})
			Expect(named).To(Equal([]string{"--verbose"}))
			replay, named, _ := tmuxpane.ScanPassthrough([]string{"--bare", "--", "--agent", "x"})
			Expect(replay).To(Equal([]string{"--bare"}))
			Expect(named).To(BeEmpty())
			// A known flag's value is not a positional.
			replay, named, _ = tmuxpane.ScanPassthrough([]string{"--permission-mode", "plan", "--bare"})
			Expect(replay).To(Equal([]string{"--bare"}))
			Expect(named).To(Equal([]string{"--permission-mode"}))
			// Unknown arity: named, then the scan stops.
			replay, named, _ = tmuxpane.ScanPassthrough([]string{"--frobnicate", "x", "--bare"})
			Expect(replay).To(BeEmpty())
			Expect(named).To(Equal([]string{"--frobnicate"}))
			// Unknown but followed by a flag: it takes no word, the scan goes on.
			replay, named, _ = tmuxpane.ScanPassthrough([]string{"--frobnicate", "--bare"})
			Expect(replay).To(Equal([]string{"--bare"}))
			Expect(named).To(Equal([]string{"--frobnicate"}))
		})

		It("CS-TMUX-013: the label holds names only and reads back for an attach", func() {
			r := tmuxpane.Record([]string{"--docker-socket"}, "opus",
				[]string{"--add-dir", "/secret/dir", "--effort", "max", "--permission-mode", "plan"}, nil)
			Expect(r.LabelValue()).To(Equal("--model,--add-dir,--effort,--docker-socket,--permission-mode"))
			Expect(r.LabelValue()).NotTo(ContainSubstring("/secret"))
			model, unreplayed := tmuxpane.FromLabel(r.LabelValue())
			Expect(model).To(BeTrue())
			Expect(unreplayed).To(Equal([]string{"--add-dir", "--effort", "--docker-socket", "--permission-mode"}))
			// claude's own --model after "--": the model label is not the
			// session's model, so an attach names --model instead.
			r = tmuxpane.Record(nil, "sonnet", []string{"--model", "opus"}, nil)
			Expect(r.Model).To(Equal("opus"))
			Expect(r.LabelValue()).To(Equal("--model:claude"))
			model, unreplayed = tmuxpane.FromLabel(r.LabelValue())
			Expect(model).To(BeFalse())
			Expect(unreplayed).To(Equal([]string{"--model"}))
			model, unreplayed = tmuxpane.FromLabel("--bare,rm -rf /,,CLAUDE_SANDBOX_DANGEROUS")
			Expect(model).To(BeFalse())
			Expect(unreplayed).To(Equal([]string{"--bare", "CLAUDE_SANDBOX_DANGEROUS"}))
		})

		It("CS-TMUX-017: ResumeID reads an explicit resume in every spelling, with the scan's stop rules", func() {
			for _, args := range [][]string{
				{"--resume", convID}, {"--resume=" + convID}, {"-r", convID}, {"-r" + convID},
				{"--verbose", "--permission-mode", "plan", "--resume", strings.ToUpper(convID)},
			} {
				Expect(tmuxpane.ResumeID(args)).To(Equal(convID), "%v", args)
			}
			for _, args := range [][]string{
				{"--resume"}, {"--resume", "not-a-uuid"}, {"fix", "--resume", convID},
				{"--", "--resume", convID}, {"--frobnicate", "x", "--resume", convID},
			} {
				Expect(tmuxpane.ResumeID(args)).To(BeEmpty(), "%v", args)
			}
		})
	})

	Describe("the mark", func() {
		It("CS-TMUX-011: compact JSON, worktree always present, no conversation or label fields at launch", func() {
			cde := ""
			m := tmuxpane.Mark{V: 1, State: tmuxpane.StateActive, Mode: "claude", Container: "c",
				Project: "/p", Since: 5, ConfigDirEnv: &cde}
			raw := m.JSON()
			Expect(raw).NotTo(ContainSubstring(" "))
			var got map[string]any
			Expect(json.Unmarshal([]byte(raw), &got)).To(Succeed())
			Expect(got).To(HaveKeyWithValue("worktree", ""))
			Expect(got).To(HaveKeyWithValue("configDirEnv", ""))
			for _, k := range []string{"conversation", "name", "nameSource", "windowLabel", "labelPane"} {
				Expect(got).NotTo(HaveKey(k))
			}
			back, ok := tmuxpane.ParseMark(raw)
			Expect(ok).To(BeTrue())
			Expect(back.Container).To(Equal("c"))
		})

		It("CS-TMUX-016: a value that is not a v1 mark does not parse", func() {
			for _, raw := range []string{"", "garbage", `{"v":2}`, "[]"} {
				_, ok := tmuxpane.ParseMark(raw)
				Expect(ok).To(BeFalse(), raw)
			}
		})

		It("CS-TMUX-010: the tmux argv", func() {
			fake := &execx.Fake{}
			fake.On("tmux show-options", `{"v":1}`+"\n", nil)
			p := tmuxpane.Pane{Runner: fake, ID: "%3"}
			Expect(p.Read()).To(Equal(`{"v":1}`))
			p.Set(`{"v":1}`)
			p.Unset()
			Expect(fake.CommandLines()).To(Equal([]string{
				"tmux show-options -p -q -v -t %3 @claude-sandbox",
				`tmux set-option -p -t %3 @claude-sandbox {"v":1}`,
				"tmux set-option -p -u -t %3 @claude-sandbox",
			}))
		})

		It("CS-TMUX-016: every tmux call is killed after CallTimeout", func() {
			saved := tmuxpane.CallTimeout
			tmuxpane.CallTimeout = 50 * time.Millisecond
			DeferCleanup(func() { tmuxpane.CallTimeout = saved })
			r := &hangRunner{}
			p := tmuxpane.Pane{Runner: r, ID: "%3"}
			start := time.Now()
			Expect(p.Read()).To(BeEmpty())
			p.Set(`{"v":1}`)
			p.Unset()
			Expect(time.Since(start)).To(BeNumerically("<", 2*time.Second))
			Expect(r.killed).To(Equal(3))
			Expect(r.dieWithParent).To(Equal(3), "own process group, killed with the launcher")
		})

		It("CS-TMUX-014: no pane outside tmux or inside a sandbox", func() {
			get := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
			_, ok := tmuxpane.FromEnv(get(map[string]string{"TMUX_PANE": "%1"}))
			Expect(ok).To(BeFalse())
			_, ok = tmuxpane.FromEnv(get(map[string]string{"TMUX": "/tmp/tmux-1/default,1,0"}))
			Expect(ok).To(BeFalse())
			_, ok = tmuxpane.FromEnv(get(map[string]string{"TMUX": "x", "TMUX_PANE": "%1", "CLAUDE_SANDBOX_PROJECT_DIR": "/p"}))
			Expect(ok).To(BeFalse())
			_, ok = tmuxpane.FromEnv(get(map[string]string{"TMUX": "x", "TMUX_PANE": "-t"}))
			Expect(ok).To(BeFalse(), "only a pane id")
			id, ok := tmuxpane.FromEnv(get(map[string]string{"TMUX": "x", "TMUX_PANE": "%12"}))
			Expect(ok).To(BeTrue())
			Expect(id).To(Equal("%12"))
		})
	})

	Describe("CS-TMUX-017: the pending-mark note", func() {
		cde := "/home/u/work claude"
		pending := tmuxpane.Mark{V: 1, State: tmuxpane.StatePending, Mode: "claude", Container: "c",
			Instance: "otter", Project: "/home/u/proj", ConfigDirEnv: &cde, Worktree: "",
			Model: "opus", Replay: []string{"--add-dir", "/x y"},
			Conversation: convID, Name: "fix it", NameSource: "user"}

		It("CS-TMUX-017: names the conversation and the exact, quoted resume command", func() {
			note := tmuxpane.PendingNote(pending.JSON(), "")
			Expect(note).To(Equal("Note: this pane was waiting to restore 'fix it' (" + convID + "); resume it with: " +
				"cd /home/u/proj && CLAUDE_CONFIG_DIR='/home/u/work claude' claude-sandbox --new --no-worktree --model opus -- " +
				"--add-dir '/x y' --resume " + convID + " --name 'fix it'"))
		})

		It("CS-TMUX-017: a control character in any printed value prints no command", func() {
			esc := pending
			esc.Replay = []string{"--append-system-prompt", "hi\x1b]0;pwned\x07"}
			want := "Note: this pane was waiting to restore a conversation (" + convID +
				"), but its mark holds unprintable values; no resume command is shown"
			Expect(tmuxpane.PendingNote(esc.JSON(), "")).To(Equal(want))
			for _, mut := range []func(m *tmuxpane.Mark){
				func(m *tmuxpane.Mark) { m.Project = "/p\x1b[2J" },
				func(m *tmuxpane.Mark) { v := "/c\n"; m.ConfigDirEnv = &v },
				func(m *tmuxpane.Mark) { m.Model = "opus\u009b" }, // C1 CSI
				func(m *tmuxpane.Mark) { m.Worktree = "w\x1b" },
				func(m *tmuxpane.Mark) { m.Name = "fix\x1b[31m" },
			} {
				m := pending
				mut(&m)
				Expect(tmuxpane.PendingNote(m.JSON(), "")).To(Equal(want))
			}
		})

		It("CS-TMUX-017: a worktree whose generated name is not recorded gets no command", func() {
			gen := pending
			gen.WorktreeGenerated = true
			Expect(tmuxpane.PendingNote(gen.JSON(), "")).To(Equal("Note: this pane was waiting to restore 'fix it' (" + convID +
				") in a worktree whose name is not recorded yet; no resume command is shown"))
			Expect(tmuxpane.ResumeCommand(gen)).To(BeEmpty())
		})

		It("CS-TMUX-017: --worktree when recorded, --name only for a user name, no CLAUDE_CONFIG_DIR when unset", func() {
			m := pending
			empty := ""
			m.ConfigDirEnv, m.Worktree, m.NameSource, m.Replay, m.Model = &empty, "otter", "derived", nil, ""
			Expect(tmuxpane.ResumeCommand(m)).To(Equal("cd /home/u/proj && claude-sandbox --new --worktree=otter -- --resume " + convID))
		})

		It("CS-TMUX-017: silent for an active mark, a pending one without an id, or the same conversation", func() {
			active := pending
			active.State = tmuxpane.StateActive
			Expect(tmuxpane.PendingNote(active.JSON(), "")).To(BeEmpty())
			noID := pending
			noID.Conversation = ""
			Expect(tmuxpane.PendingNote(noID.JSON(), "")).To(BeEmpty())
			Expect(tmuxpane.PendingNote(pending.JSON(), convID)).To(BeEmpty())
			Expect(tmuxpane.PendingNote("garbage", "")).To(BeEmpty())
		})
	})
})

// hangRunner starts tmux processes that never exit until killed: a hung tmux
// server (CS-TMUX-016).
type hangRunner struct {
	execx.Fake
	killed, dieWithParent int
}

type hungProc struct {
	r    *hangRunner
	done chan struct{}
}

func (p *hungProc) Signal(os.Signal) error { p.r.killed++; close(p.done); return nil }
func (p *hungProc) Wait() error            { <-p.done; return errors.New("killed") }
func (p *hungProc) Pid() int               { return 1 }

func (r *hangRunner) Start(c execx.Cmd) (execx.Process, error) {
	if c.DieWithParent {
		r.dieWithParent++
	}
	return &hungProc{r: r, done: make(chan struct{})}, nil
}
