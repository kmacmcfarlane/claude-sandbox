package main

// Spec: spec/tmux.feature (CS-TMUX-010..019) and spec/launch.feature
// (CS-LNCH-109) — the tmux pane mark, end to end through MainWithEnv (and
// runSession directly for the restore-only paths) with tmux faked through
// execx.Fake: no real tmux, no real docker.

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
	"github.com/kmacmcfarlane/claude-sandbox/internal/oomreport"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

const (
	markConv = "0b5e9c3a-1f2d-4e5f-8a9b-0c1d2e3f4a5b"
	markID   = "4f1c2a9b8e7d6c5b4a39281706f5e4d3c2b1a09f8e7d6c5b4a3928170615e4d3"
)

// psRowMark is a docker ps row carrying every optional trailing field, the
// CS-LNCH-109 labels included.
func psRowMark(name, project, mode, instance, model, class, worktree, created, id, configDir, registry, flags string) string {
	return strings.Join([]string{name, "Up 1 hour", project, mode, instance, "v1", model, "", "", class, worktree,
		"running", created, "8g", "default", "", id, configDir, registry, flags}, psSep)
}

var _ = Describe("tmux pane mark (CS-TMUX-010..019)", func() {
	var f *cliFixture
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

	BeforeEach(func() {
		f = newCLIFixture()
		f.envmap["TMUX"] = "/tmp/tmux-1000/default,4242,0"
		f.envmap["TMUX_PANE"] = "%7"
		f.env.Now = func() time.Time { return now }
	})

	// tmuxLines is every recorded tmux command, in order.
	tmuxLines := func() []string {
		var out []string
		for _, l := range f.fake.CommandLines() {
			if strings.HasPrefix(l, "tmux ") {
				out = append(out, l)
			}
		}
		return out
	}
	// indexOf is the position of the first command line with prefix p.
	indexOf := func(p string) int {
		for i, l := range f.fake.CommandLines() {
			if strings.HasPrefix(l, p) {
				return i
			}
		}
		return -1
	}
	// setMarks is every mark set on the pane, parsed.
	setMarks := func() []tmuxpane.Mark {
		var out []tmuxpane.Mark
		for _, c := range f.fake.Calls {
			if c.Name == "tmux" && len(c.Args) == 6 && c.Args[0] == "set-option" && c.Args[4] == tmuxpane.Option {
				m, ok := tmuxpane.ParseMark(c.Args[5])
				Expect(ok).To(BeTrue(), c.Args[5])
				out = append(out, m)
			}
		}
		return out
	}
	theMark := func() tmuxpane.Mark {
		ms := setMarks()
		Expect(ms).To(HaveLen(1), "one mark set; tmux calls: %v", tmuxLines())
		return ms[0]
	}
	const unset = "tmux set-option -p -u -t %7 @claude-sandbox"
	const show = "tmux show-options -p -q -v -t %7 @claude-sandbox"

	label := func(key string) string {
		for _, l := range argPairsCLI(f.launched().Args, "--label") {
			if k, v, ok := strings.Cut(l, "="); ok && k == key {
				return v
			}
		}
		Fail("no label " + key)
		return ""
	}

	It("CS-TMUX-010: a launch inside tmux marks its pane before the session and unmarks it after", func() {
		// A clean exit: the container's die, with no stop from outside
		// (CS-TMUX-071 row 4).
		streamEvents(f.fake, dockerEvent("die", "0"))
		Expect(f.run()).To(Equal(0), f.errw.String())
		lines := tmuxLines()
		Expect(lines).To(HaveLen(3))
		Expect(lines[0]).To(Equal(show))
		Expect(lines[1]).To(HavePrefix("tmux set-option -p -t %7 @claude-sandbox {"))
		Expect(lines[2]).To(Equal(unset))
		start := indexOf("docker start -ai")
		Expect(start).To(BeNumerically(">", indexOf("tmux set-option -p -t %7")))
		Expect(indexOf(unset)).To(BeNumerically(">", start))
	})

	It("CS-TMUX-011: the mark of a new container", func() {
		f.fake.On("docker create", markID+"\n", nil)
		Expect(f.run("--model", "opus", "--docker-socket", "--",
			"--add-dir", "/x", "--permission-mode", "plan", "--name", "fix")).To(Equal(0), f.errw.String())
		m := theMark()
		Expect(m.V).To(Equal(1))
		Expect(m.State).To(Equal(tmuxpane.StateActive))
		Expect(m.Mode).To(Equal(tmuxpane.ModeClaude))
		Expect(m.Container).To(Equal(nameOf(f.launched().Args)))
		Expect(m.ContainerID).To(Equal(markID))
		Expect(m.Instance).NotTo(BeEmpty())
		Expect(m.Container).To(HaveSuffix("-" + m.Instance))
		Expect(m.Project).To(Equal(f.proj))
		Expect(m.CwdRoot).To(Equal(f.proj))
		Expect(m.Class).To(Equal(label("claude-sandbox.pidclass")))
		Expect(m.Since).To(Equal(now.UnixMilli()))
		Expect(m.ConfigDir).To(Equal(filepath.Join(f.home, ".claude")))
		Expect(m.ConfigDirEnv).NotTo(BeNil())
		Expect(*m.ConfigDirEnv).To(Equal(""))
		Expect(m.RegistryDir).To(Equal(filepath.Join(f.home, ".claude", "sessions")))
		Expect(m.Worktree).To(Equal(""))
		Expect(m.Model).To(Equal("opus"))
		Expect(m.Replay).To(Equal([]string{"--add-dir", "/x"}))
		// The fixture's CLAUDE_SANDBOX_BASE_ONLY=1 is a launch switch set in
		// the environment: named, never its value.
		Expect(m.Unreplayed).To(Equal([]string{"--docker-socket", "CLAUDE_SANDBOX_BASE_ONLY", "--permission-mode"}))
		Expect(m.Conversation + m.Name + m.NameSource).To(BeEmpty())
	})

	It("CS-TMUX-011: model only from the command line; a cascade model is not recorded", func() {
		writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "model: sonnet\n")
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(theMark().Model).To(BeEmpty())
		Expect(label("claude-sandbox.model")).To(Equal("sonnet"))
		Expect(label(launch.LabelLaunchFlags)).To(Equal("CLAUDE_SANDBOX_BASE_ONLY"), "the fixture's switch; no --model")
	})

	It("CS-TMUX-011: a worktree session's mark names the worktree and runs under it", func() {
		f.fake.On("rev-parse --show-toplevel", f.proj+"\n", nil)
		Expect(f.run("--worktree")).To(Equal(0), f.errw.String())
		m := theMark()
		Expect(m.Worktree).To(Equal(m.Instance))
		Expect(m.CwdRoot).To(Equal(filepath.Join(f.proj, ".claude", "worktrees", m.Instance)))
	})

	It("CS-TMUX-011: a host --ralph launch is marked mode ralph, without an instance", func() {
		Expect(f.run("--ralph")).To(Equal(0), f.errw.String())
		m := theMark()
		Expect(m.Mode).To(Equal(tmuxpane.ModeRalph))
		Expect(m.Instance).To(BeEmpty())
	})

	It("CS-TMUX-011: the raw CLAUDE_CONFIG_DIR is recorded as given", func() {
		cfg := filepath.Join(f.home, "work-claude")
		Expect(os.MkdirAll(cfg, 0o755)).To(Succeed())
		f.envmap["CLAUDE_CONFIG_DIR"] = cfg
		Expect(f.run()).To(Equal(0), f.errw.String())
		m := theMark()
		Expect(*m.ConfigDirEnv).To(Equal(cfg))
		Expect(m.ConfigDir).To(Equal(cfg))
		Expect(m.RegistryDir).To(Equal(filepath.Join(cfg, "sessions")))
	})

	It("CS-LNCH-109: the create carries configdir, registry and names-only launchflags", func() {
		Expect(f.run("--model", "opus", "--ssh", "--", "--effort", "max", "--settings", `{"apiKeyHelper":"s3cret"}`)).To(Equal(0), f.errw.String())
		Expect(label(launch.LabelConfigDir)).To(Equal(""))
		Expect(label(launch.LabelRegistry)).To(Equal(filepath.Join(f.home, ".claude", "sessions")))
		Expect(label(launch.LabelLaunchFlags)).To(Equal("--model,--effort,--ssh,CLAUDE_SANDBOX_BASE_ONLY,--settings"))
		for _, l := range argPairsCLI(f.launched().Args, "--label") {
			Expect(l).NotTo(ContainSubstring("s3cret"))
		}
	})

	Describe("attach and join (CS-TMUX-012)", func() {
		created := "2026-09-18 12:34:56 +0000 UTC"
		createdAt := time.Date(2026, 9, 18, 12, 34, 56, 0, time.UTC)
		running := func(row string) {
			f.fake.On("docker ps", row+"\n", nil)
			f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
		}

		It("CS-TMUX-012: an attach's mark comes from the container's labels", func() {
			cfg := "/srv/claude-work"
			running(psRowMark("cs-otter", f.proj, "claude", "otter", "opus", "37", "", created, markID,
				cfg, cfg+"/sessions", "--model,--add-dir,--docker-socket"))
			Expect(f.run("--attach=otter")).To(Equal(0), f.errw.String())
			Expect(f.sessionLine()).To(HavePrefix("docker attach"))
			m := theMark()
			Expect(m.Mode).To(Equal(tmuxpane.ModeClaude))
			Expect(m.Container).To(Equal("cs-otter"))
			Expect(m.ContainerID).To(Equal(markID))
			Expect(m.Since).To(Equal(createdAt.UnixMilli()))
			Expect(m.Class).To(Equal("37"))
			Expect(m.Instance).To(Equal("otter"))
			Expect(*m.ConfigDirEnv).To(Equal(cfg))
			Expect(m.ConfigDir).To(Equal(cfg))
			Expect(m.RegistryDir).To(Equal(cfg + "/sessions"))
			Expect(m.Model).To(Equal("opus"), "--model was given on the command line")
			Expect(m.Replay).To(BeEmpty())
			Expect(m.Unreplayed).To(Equal([]string{"--add-dir", "--docker-socket"}))
			Expect(m.FlagsUnknown).To(BeFalse())
			Expect(tmuxLines()[len(tmuxLines())-1]).To(Equal(unset))
		})

		It("CS-TMUX-012: an attach records the model label only when launchflags names --model", func() {
			running(psRowMark("cs-otter", f.proj, "claude", "otter", "sonnet", "37", "otter", created, "short",
				"", f.home+"/.claude/sessions", ""))
			f.fake.On("rev-parse --show-toplevel", f.proj+"\n", nil)
			Expect(f.run("--attach=otter")).To(Equal(0), f.errw.String())
			m := theMark()
			Expect(m.Model).To(BeEmpty())
			Expect(m.ContainerID).To(BeEmpty(), "a short id is never recorded")
			Expect(m.ConfigDir).To(Equal(filepath.Join(f.home, ".claude")))
			Expect(m.Worktree).To(Equal("otter"))
			Expect(m.CwdRoot).To(Equal(filepath.Join(f.proj, ".claude", "worktrees", "otter")))
		})

		It("CS-TMUX-012: an attach to a launch with claude's own --model names --model and takes no model", func() {
			running(psRowMark("cs-otter", f.proj, "claude", "otter", "sonnet", "37", "", created, markID,
				"", f.home+"/.claude/sessions", tmuxpane.LabelClaudeModel+",--bare"))
			Expect(f.run("--attach=otter")).To(Equal(0), f.errw.String())
			m := theMark()
			Expect(m.Model).To(BeEmpty(), "the model label holds the launcher's model, not the session's")
			Expect(m.Unreplayed).To(Equal([]string{"--model", "--bare"}))
		})

		It("CS-LNCH-109: launchflags says --model:claude when claude's own --model was given", func() {
			Expect(f.run("--model", "sonnet", "--", "--model", "opus")).To(Equal(0), f.errw.String())
			Expect(label(launch.LabelLaunchFlags)).To(Equal(tmuxpane.LabelClaudeModel + ",CLAUDE_SANDBOX_BASE_ONLY"))
			Expect(theMark().Model).To(Equal("opus"))
		})

		It("CS-TMUX-012: a join in worktree mode without a name marks the worktree as generated, never the shared checkout", func() {
			running(psRowMark("cs-otter", f.proj, "claude", "otter", "", "37", "", created, markID,
				"", f.home+"/.claude/sessions", ""))
			f.fake.On("rev-parse --show-toplevel", f.proj+"\n", nil)
			Expect(f.run("--join=otter", "--worktree")).To(Equal(0), f.errw.String())
			Expect(f.sessionLine()).To(ContainSubstring(" --worktree"))
			m := theMark()
			Expect(m.Worktree).To(Equal(""))
			Expect(m.WorktreeGenerated).To(BeTrue())

			f.fake.Calls = nil
			Expect(f.run("--join=otter", "--worktree=named")).To(Equal(0), f.errw.String())
			m = theMark()
			Expect(m.Worktree).To(Equal("named"))
			Expect(m.WorktreeGenerated).To(BeFalse())
			Expect(m.CwdRoot).To(Equal(filepath.Join(f.proj, ".claude", "worktrees", "named")))
		})

		It("CS-TMUX-012: a join whose --worktree=NAME stood down (not a git repo) records no worktree", func() {
			running(psRowMark("cs-otter", f.proj, "claude", "otter", "", "37", "", created, markID,
				"", f.home+"/.claude/sessions", ""))
			Expect(f.run("--join=otter", "--worktree=named")).To(Equal(0), f.errw.String())
			m := theMark()
			Expect(m.Worktree).To(Equal(""))
			Expect(m.WorktreeGenerated).To(BeFalse())
			Expect(m.CwdRoot).To(Equal(f.proj))
		})

		It("CS-TMUX-012: a container that predates the labels leaves them unknown", func() {
			running(psRow("cs-otter", "Up 1 hour", f.proj, "otter"))
			Expect(f.run("--attach=otter")).To(Equal(0), f.errw.String())
			m := theMark()
			Expect(m.ConfigDirEnv).To(BeNil())
			Expect(m.RegistryDir).To(BeEmpty())
			Expect(m.FlagsUnknown).To(BeTrue())
		})

		It("CS-TMUX-012: a join is mode join, since the exec, with its own command line", func() {
			running(psRowMark("cs-otter", f.proj, "claude", "otter", "opus", "37", "", created, markID,
				"", f.home+"/.claude/sessions", "--model,--docker-socket"))
			Expect(f.run("--join=otter", "--", "--bare", "--verbose")).To(Equal(0), f.errw.String())
			Expect(f.sessionLine()).To(HavePrefix("docker exec"))
			m := theMark()
			Expect(m.Mode).To(Equal(tmuxpane.ModeJoin))
			Expect(m.ContainerID).To(Equal(markID))
			Expect(m.Since).To(Equal(now.UnixMilli()))
			Expect(m.Model).To(BeEmpty())
			Expect(m.Replay).To(Equal([]string{"--bare"}))
			Expect(m.Unreplayed).To(Equal([]string{"CLAUDE_SANDBOX_BASE_ONLY", "--verbose"}))
			Expect(tmuxLines()[len(tmuxLines())-1]).To(Equal(unset))
		})
	})

	Describe("CS-TMUX-014: no tmux call", func() {
		It("CS-TMUX-014: outside tmux", func() {
			delete(f.envmap, "TMUX")
			Expect(f.run()).To(Equal(0), f.errw.String())
			Expect(tmuxLines()).To(BeEmpty())
			f.envmap["TMUX"] = "x"
			delete(f.envmap, "TMUX_PANE")
			Expect(f.run()).To(Equal(0), f.errw.String())
			Expect(tmuxLines()).To(BeEmpty())
		})

		It("CS-TMUX-014: for a headless launch, even with TMUX set", func() {
			Expect(f.run("headless", "--")).To(Equal(0), f.errw.String())
			Expect(f.fake.Session).NotTo(BeNil())
			Expect(tmuxLines()).To(BeEmpty())
		})

		It("CS-TMUX-014: inside a sandbox", func() {
			Expect(runSession(f.env, execx.Cmd{Name: "docker", Args: []string{"attach", "x"}}, "x",
				sessionOpts{kind: primarySession, mark: &paneMark{next: tmuxpane.Mark{V: 1}}})).Error().NotTo(HaveOccurred())
			Expect(tmuxLines()).NotTo(BeEmpty(), "control: marked on the host")
			f.fake.Calls = nil
			f.envmap["CLAUDE_SANDBOX_PROJECT_DIR"] = f.proj
			Expect(runSession(f.env, execx.Cmd{Name: "docker", Args: []string{"attach", "x"}}, "x",
				sessionOpts{kind: primarySession, mark: &paneMark{next: tmuxpane.Mark{V: 1}}})).Error().NotTo(HaveOccurred())
			Expect(tmuxLines()).To(BeEmpty())
		})

		It("CS-TMUX-014: for a --detach launch", func() {
			saved := detachedSettle
			detachedSettle = 10 * time.Millisecond
			DeferCleanup(func() { detachedSettle = saved })
			f.fake.On("docker inspect --type container", "running 2026-09-24T00:00:00Z\n", nil)
			Expect(f.run("--detach")).To(Equal(0), f.errw.String())
			Expect(tmuxLines()).To(BeEmpty())
		})
	})

	Describe("CS-TMUX-015: unmarked on a clean exit, a crash, an OOM kill, a detach or the launcher's own signal", func() {
		var p *paneSim
		BeforeEach(func() { p = simulatePane(f.fake) })

		It("CS-TMUX-015: a non-zero exit (a crash) with its die", func() {
			streamEvents(f.fake, dockerEvent("die", "3"))
			f.fake.On("docker start -ai", "", execx.Fail(3))
			Expect(f.run()).To(Equal(3))
			Expect(tmuxLines()[len(tmuxLines())-1]).To(Equal(unset))
			Expect(p.get()).To(BeEmpty())
		})

		It("CS-TMUX-015: an OOM kill", func() {
			streamEvents(f.fake, dockerEvent("oom", ""), dockerEvent("die", "137"))
			f.fake.On("docker start -ai", "", execx.Fail(137))
			Expect(f.run()).To(Equal(137))
			Expect(p.get()).To(BeEmpty())
			Expect(f.errw.String()).To(ContainSubstring("killed by the OOM killer"))
		})

		It("CS-TMUX-015: a forwarded signal", func() {
			f.fake.SessionSignal = syscall.SIGTERM
			f.run()
			Expect(tmuxLines()[len(tmuxLines())-1]).To(Equal(unset))
			Expect(p.get()).To(BeEmpty())
		})

		It("CS-TMUX-015: a signal during the die wait", func() {
			f.fake.LateSignal = syscall.SIGTERM
			f.run()
			Expect(tmuxLines()[len(tmuxLines())-1]).To(Equal(unset))
			Expect(tmuxLines()).To(HaveLen(3))
			Expect(p.get()).To(BeEmpty())
		})
	})

	It("CS-TMUX-016: tmux failures never change the launch", func() {
		f.fake.On("tmux ", "", execx.Fail(1))
		Expect(f.run()).To(Equal(0))
		Expect(f.fake.Session).NotTo(BeNil())
		Expect(f.errw.String()).NotTo(ContainSubstring("tmux"))
		Expect(f.out.String()).NotTo(ContainSubstring("tmux"))
	})

	Describe("a pending mark (CS-TMUX-017/018)", func() {
		cde := "/srv/claude-work"
		pending := tmuxpane.Mark{V: 1, State: tmuxpane.StatePending, Mode: "claude", Container: "cs-old",
			Instance: "heron", Project: "/srv/proj", ConfigDirEnv: &cde, Conversation: markConv,
			Name: "fix the build", NameSource: "user"}
		BeforeEach(func() { f.fake.On("tmux show-options", pending.JSON()+"\n", nil) })

		It("CS-TMUX-017: a launch over it prints one note with the exact resume command", func() {
			Expect(f.run()).To(Equal(0), f.errw.String())
			Expect(f.errw.String()).To(ContainSubstring("Note: this pane was waiting to restore 'fix the build' (" + markConv +
				"); resume it with: cd /srv/proj && CLAUDE_CONFIG_DIR=/srv/claude-work claude-sandbox --new --no-worktree -- --resume " +
				markConv + " --name 'fix the build'\n"))
			Expect(strings.Count(f.errw.String(), "Note: this pane was waiting")).To(Equal(1))
			Expect(theMark().State).To(Equal(tmuxpane.StateActive))
		})

		It("CS-TMUX-017: no note for a launch that resumes that conversation", func() {
			Expect(f.run("--resume", markConv)).To(Equal(0), f.errw.String())
			Expect(f.errw.String()).NotTo(ContainSubstring("waiting to restore"))
		})

		It("CS-TMUX-018: a docker start that could not run puts the prior mark back", func() {
			f.fake.On("docker start -ai", "", errors.New("exec: docker: not found"))
			Expect(f.run()).NotTo(Equal(0))
			lines := tmuxLines()
			Expect(lines[len(lines)-1]).To(Equal("tmux set-option -p -t %7 @claude-sandbox " + pending.JSON()))
			Expect(lines).NotTo(ContainElement(unset))
		})

		It("CS-TMUX-018: a reservation that never ran puts the prior mark back", func() {
			f.fake.On("docker inspect --type container -f", "created 2026-09-24T00:00:00Z\n", nil)
			f.fake.On("docker start -ai", "", execx.Fail(1))
			f.run()
			lines := tmuxLines()
			Expect(lines[len(lines)-1]).To(Equal("tmux set-option -p -t %7 @claude-sandbox " + pending.JSON()))
		})
	})

	It("CS-TMUX-018: with no prior mark a failed start unsets", func() {
		f.fake.On("tmux show-options", "not a mark\n", nil)
		f.fake.On("docker start -ai", "", errors.New("exec: docker: not found"))
		f.run()
		lines := tmuxLines()
		Expect(lines[len(lines)-1]).To(Equal(unset))
	})

	Describe("CS-TMUX-019: a restore attach to a container that vanished", func() {
		prior := tmuxpane.Mark{V: 1, State: tmuxpane.StatePending, Mode: "claude", Container: "cs-otter",
			ContainerID: markID, Conversation: markConv}.JSON()
		attach := func(restore bool) {
			next := tmuxpane.Mark{V: 1, State: tmuxpane.StateActive, Mode: "claude", Container: "cs-otter", ContainerID: markID}
			p := prior
			_, err := runSession(f.env, execx.Cmd{Name: "docker", Args: []string{"attach", markID}}, "cs-otter",
				sessionOpts{kind: primarySession, mark: &paneMark{next: next, prior: &p, restoreAttach: restore}})
			Expect(err).NotTo(HaveOccurred())
		}

		It("CS-TMUX-019: puts the prior mark back when the attach fails at once and the id is gone", func() {
			f.fake.On("docker attach", "", execx.Fail(1))
			f.fake.On("docker inspect --type container -f {{.State.Status}} {{.Created}} "+markID, "", execx.Fail(1))
			attach(true)
			lines := tmuxLines()
			Expect(lines[0]).To(HavePrefix("tmux set-option -p -t %7 @claude-sandbox "))
			Expect(lines[len(lines)-1]).To(Equal("tmux set-option -p -t %7 @claude-sandbox " + prior))
			Expect(lines).NotTo(ContainElement(show), "a restore hands its prior in; nothing is read")
		})

		It("CS-TMUX-019: unmarks when the container is still there, or for a hand attach", func() {
			f.fake.On("docker attach", "", execx.Fail(1))
			f.fake.On("docker inspect --type container -f {{.State.Status}} {{.Created}} "+markID, "running 2026-09-24T00:00:00Z\n", nil)
			attach(true)
			Expect(tmuxLines()[len(tmuxLines())-1]).To(Equal(unset))

			f.fake.Calls = nil
			f.fake = &execx.Fake{}
			f.env.Runner = f.fake
			f.fake.On("docker attach", "", execx.Fail(1))
			f.fake.On("docker inspect", "", execx.Fail(1))
			attach(false)
			Expect(tmuxLines()[len(tmuxLines())-1]).To(Equal(unset))
		})
	})

	Describe("CS-TMUX-071: a session stopped from outside leaves its pane pending", func() {
		var (
			p         *paneSim
			state     string // what the end's docker inspect reports; "" fails it
			startCode int    // the session child's status
		)
		const probe = "systemctl is-system-running"
		BeforeEach(func() {
			p = simulatePane(f.fake)
			state, startCode = "running", 0
			f.fake.OnFunc("docker inspect --type container -f {{.State.Status}} ", func(c execx.Cmd) (string, error) {
				if c.Args[4] != "{{.State.Status}}" {
					return "", nil // CS-LNCH-096's created check: not created
				}
				if state == "" {
					return "", execx.Fail(1)
				}
				return state + "\n", nil
			})
			// The save hook writes the conversation into the mark while the
			// session runs (CS-TMUX-035).
			f.fake.OnFunc("docker start -ai", func(execx.Cmd) (string, error) {
				p.update(func(m *tmuxpane.Mark) { m.Conversation = markConv })
				// The session lasts a moment: events published before it
				// ends have been read, as a real session's would be (the
				// forwarded path reads what has arrived without waiting).
				time.Sleep(20 * time.Millisecond)
				if startCode != 0 {
					return "", execx.Fail(startCode)
				}
				return "", nil
			})
			saved := oomreport.DieWait
			oomreport.DieWait = 300 * time.Millisecond
			DeferCleanup(func() { oomreport.DieWait = saved })
		})
		stopping := func() { f.fake.On(probe, "stopping\n", execx.Fail(1)) }
		probes := func() int {
			n := 0
			for _, l := range f.fake.CommandLines() {
				if l == probe {
					n++
				}
			}
			return n
		}
		// pendingNow is the pane's mark, which must be this session's own,
		// pending, with the conversation the save hook wrote.
		pendingNow := func() tmuxpane.Mark {
			m, ok := tmuxpane.ParseMark(p.get())
			Expect(ok).To(BeTrue(), "a mark is left; tmux: %v", tmuxLines())
			Expect(m.State).To(Equal(tmuxpane.StatePending))
			Expect(m.Conversation).To(Equal(markConv))
			Expect(m.Container).To(Equal(nameOf(f.launched().Args)))
			return m
		}

		for name, stream := range map[string][]string{
			"docker stop (kill, die, stop)": {dockerEvent("kill", ""), dockerEvent("die", "143"), dockerEvent("stop", "")},
			"a stop before a clean die":     {dockerEvent("stop", ""), dockerEvent("die", "0")},
		} {
			It("CS-TMUX-071 row 1: a kill or stop event before the die keeps the pane pending, with no probe: "+name, func() {
				streamEvents(f.fake, stream...)
				f.run()
				pendingNow()
				Expect(probes()).To(Equal(0))
				Expect(tmuxLines()).NotTo(ContainElement(unset))
			})
		}

		It("CS-TMUX-071 row 1: a stop event after the die changes nothing", func() {
			streamEvents(f.fake, dockerEvent("die", "0"), dockerEvent("stop", ""))
			Expect(f.run()).To(Equal(0))
			Expect(p.get()).To(BeEmpty())
			Expect(probes()).To(Equal(1), "a clean end asks whether the host is stopping")
		})

		It("CS-TMUX-071 row 2: systemd reporting stopping (exit 1) keeps a clean-looking end pending", func() {
			streamEvents(f.fake, dockerEvent("die", "0"))
			stopping()
			Expect(f.run()).To(Equal(0))
			pendingNow()
			Expect(probes()).To(Equal(1))
		})

		It("CS-TMUX-071 row 2: degraded (exit 1) or no systemd is not stopping", func() {
			streamEvents(f.fake, dockerEvent("die", "0"))
			f.fake.On(probe, "degraded\n", execx.Fail(1))
			Expect(f.run()).To(Equal(0))
			Expect(p.get()).To(BeEmpty())
		})

		It("CS-TMUX-071 rows 2 and 3: shutdown evidence outranks a forwarded signal", func() {
			f.fake.SessionSignal = syscall.SIGTERM
			stopping()
			f.run()
			pendingNow()
		})

		It("CS-TMUX-071 rows 1 and 3: a kill seen before the forwarded signal keeps it pending", func() {
			f.fake.SessionSignal = syscall.SIGTERM
			streamEvents(f.fake, dockerEvent("kill", ""))
			f.run()
			Expect(probes()).To(Equal(0))
			pendingNow()
		})

		It("CS-TMUX-071 row 3: a forwarded signal with no outside evidence unsets, after one probe", func() {
			f.fake.SessionSignal = syscall.SIGTERM
			f.run()
			Expect(p.get()).To(BeEmpty())
			Expect(probes()).To(Equal(1))
		})

		It("CS-TMUX-071 row 4: a crash or an OOM kill unsets (the narrow default)", func() {
			streamEvents(f.fake, dockerEvent("oom", ""), dockerEvent("die", "137"))
			f.run()
			Expect(p.get()).To(BeEmpty())
		})

		It("CS-TMUX-071 row 6: a stream that ended with no die is inconclusive: pending, no probe", func() {
			f.run() // the fake's stream ends at once, with nothing
			pendingNow()
			Expect(probes()).To(Equal(0))
		})

		It("CS-TMUX-071 rows 6 and 7: a detach needs an open stream, exit 0 and a running container", func() {
			r := &eventsRunner{Fake: f.fake, open: true}
			f.env.Runner = r
			f.run()
			Expect(p.get()).To(BeEmpty(), "a detach")
			Expect(f.fake.CommandLines()).To(ContainElement(HavePrefix("docker inspect --type container -f {{.State.Status}} claude-sandbox-")))

			for _, st := range []string{"", "exited"} {
				state = st
				f.run()
				pendingNow()
				p.set("")
			}
		})

		It("CS-TMUX-071 row 9: a primary whose child exited non-zero while its container runs on is pending", func() {
			f.env.Runner = &eventsRunner{Fake: f.fake, open: true}
			startCode = 1
			Expect(f.run()).To(Equal(1))
			pendingNow()
		})

		It("CS-TMUX-071: the write-back flips only this session's own mark", func() {
			streamEvents(f.fake, dockerEvent("kill", ""), dockerEvent("die", "143"))
			other := tmuxpane.Mark{V: 1, State: tmuxpane.StateActive, Mode: "claude", Container: "cs-other"}.JSON()
			p.onRead = func(cur string) string {
				if strings.Contains(cur, `"state":"active"`) && strings.Contains(cur, markConv) {
					return other // relaunched in between
				}
				return cur
			}
			f.run()
			Expect(p.get()).To(BeEmpty(), "another mark: unset")
		})

		It("CS-TMUX-071 / CS-TMUX-017: the next hand launch in a pending pane prints the resume command", func() {
			streamEvents(f.fake, dockerEvent("kill", ""), dockerEvent("die", "143"))
			f.run()
			pend := pendingNow()
			Expect(tmuxpane.PendingNote(pend.JSON(), "")).To(ContainSubstring("--resume " + markConv))
			f.errw.Reset()
			f.run()
			Expect(f.errw.String()).To(ContainSubstring("Note: this pane was waiting to restore '"))
			Expect(f.errw.String()).To(ContainSubstring("--resume " + markConv))
		})

		It("CS-TMUX-071: no probe, inspect or extra tmux call for a headless or an unmarked session", func() {
			stopping()
			f.fake.SessionSignal = syscall.SIGTERM
			Expect(f.run("headless", "--")).To(Equal(0))
			Expect(probes()).To(Equal(0))
			Expect(tmuxLines()).To(BeEmpty())

			f.fake.Calls = nil
			f.fake.SessionSignal = nil
			delete(f.envmap, "TMUX")
			f.run()
			Expect(probes()).To(Equal(0))
			Expect(tmuxLines()).To(BeEmpty())
			for _, l := range f.fake.CommandLines() {
				Expect(l).NotTo(HavePrefix("docker inspect --type container -f {{.State.Status}} claude-"))
			}
		})

		Describe("joins", func() {
			created := "2026-09-18 12:34:56 +0000 UTC"
			BeforeEach(func() {
				f.fake.On("docker ps", psRowMark("cs-otter", f.proj, "claude", "otter", "", "37", "", created, markID,
					"", f.home+"/.claude/sessions", "")+"\n", nil)
				f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
			})
			joinExit := func(code int) {
				f.fake.OnFunc("docker exec", func(execx.Cmd) (string, error) {
					p.update(func(m *tmuxpane.Mark) { m.Conversation = markConv })
					if code == 0 {
						return "", nil
					}
					return "", execx.Fail(code)
				})
			}
			joinPending := func() {
				m, ok := tmuxpane.ParseMark(p.get())
				Expect(ok).To(BeTrue(), "tmux: %v", tmuxLines())
				Expect(m.State).To(Equal(tmuxpane.StatePending))
				Expect(m.Mode).To(Equal(tmuxpane.ModeJoin))
				Expect(m.Conversation).To(Equal(markConv))
			}

			It("CS-TMUX-071: a marked join that exits 130 sees the container's clean die moments later and unsets", func() {
				joinExit(130)
				state = "" // without the die, the end would be inconclusive (pending)
				// Later than OOMGrace: only the die wait sees it.
				f.env.Runner = &eventsRunner{Fake: f.fake, open: true, later: []string{dockerEvent("die", "0")}, delay: 200 * time.Millisecond}
				Expect(f.run("--join=otter")).To(Equal(130))
				Expect(p.get()).To(BeEmpty())
			})

			It("CS-TMUX-071: the same join with a kill before the die is pending", func() {
				joinExit(137)
				f.env.Runner = &eventsRunner{Fake: f.fake, open: true,
					later: []string{dockerEvent("kill", ""), dockerEvent("die", "137")}, delay: 200 * time.Millisecond}
				f.run("--join=otter")
				joinPending()
			})

			It("CS-TMUX-071 row 8: a join that ends non-zero while its container runs on unsets (a Ctrl-C)", func() {
				joinExit(130)
				f.env.Runner = &eventsRunner{Fake: f.fake, open: true}
				f.run("--join=otter")
				Expect(p.get()).To(BeEmpty())
				Expect(f.fake.CommandLines()).To(ContainElement("docker inspect --type container -f {{.State.Status}} " + markID))
			})

			It("CS-TMUX-071 row 5: a join that exits 0 unsets without an inspect", func() {
				joinExit(0)
				f.run("--join=otter")
				Expect(p.get()).To(BeEmpty())
				Expect(f.fake.CommandLines()).NotTo(ContainElement(HavePrefix("docker inspect --type container -f {{.State.Status}} " + markID)))
			})

			It("CS-TMUX-071 row 6: a join whose stream ended with no die is pending", func() {
				joinExit(1)
				f.run("--join=otter")
				joinPending()
			})
		})
	})

	It("CS-TMUX-011: sessions.Session carries the full id and the CS-LNCH-109 labels", func() {
		f.fake.On("docker ps", psRowMark("cs-a", f.proj, "claude", "a", "", "1", "", "", markID, "/c", "/c/sessions", "--bare")+"\n", nil)
		all, err := sessions.DiscoverAllUncounted(f.fake)
		Expect(err).NotTo(HaveOccurred())
		Expect(all).To(HaveLen(1))
		Expect(all[0].ID).To(Equal(markID))
		Expect(all[0].ConfigDirEnv).To(Equal("/c"))
		Expect(all[0].RegistryDir).To(Equal("/c/sessions"))
		Expect(all[0].LaunchFlags).To(Equal("--bare"))
		Expect(f.fake.CommandLines()[0]).To(HaveSuffix(" --no-trunc"))
	})
})

// paneSim is a tmux pane option the fake tmux reads and writes, so a test
// sees what the launcher left in the pane.
type paneSim struct {
	mu   sync.Mutex
	mark string
	// onRead, when set, rewrites what show-options returns.
	onRead func(cur string) string
}

func (p *paneSim) get() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.mark
}

func (p *paneSim) set(raw string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mark = raw
}

// update changes the pane's mark as the save hook would.
func (p *paneSim) update(fn func(*tmuxpane.Mark)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if m, ok := tmuxpane.ParseMark(p.mark); ok {
		fn(&m)
		p.mark = m.JSON()
	}
}

// simulatePane makes the fake's tmux keep the pane's mark. Registered before
// any other tmux stub, it answers every tmux call.
func simulatePane(fake *execx.Fake) *paneSim {
	p := &paneSim{}
	fake.OnFunc("tmux ", func(c execx.Cmd) (string, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		switch {
		case len(c.Args) > 0 && c.Args[0] == "show-options":
			cur := p.mark
			if p.onRead != nil {
				cur = p.onRead(cur)
			}
			if cur == "" {
				return "", nil
			}
			return cur + "\n", nil
		case len(c.Args) > 2 && c.Args[0] == "set-option" && c.Args[2] == "-u":
			p.mark = ""
		case len(c.Args) == 6 && c.Args[0] == "set-option":
			p.mark = c.Args[5]
		}
		return "", nil
	})
	return p
}

// streamEvents scripts the subscribed container's event stream, which ends
// with the lines (thisContainer stands for its name).
func streamEvents(fake *execx.Fake, lines ...string) {
	fake.OnFunc("docker events", func(c execx.Cmd) (string, error) {
		return strings.ReplaceAll(strings.Join(lines, ""), thisContainer, eventsTarget(c)), nil
	})
}

// eventsTarget is the container a "docker events" call subscribes to.
func eventsTarget(c execx.Cmd) string {
	for _, a := range c.Args {
		if v, ok := strings.CutPrefix(a, "container="); ok {
			return v
		}
	}
	return ""
}

// eventsRunner is the fake whose "docker events" publishes now, then later
// after delay, and stays open until signalled when open is set — a live
// subscription, which execx.Fake's ending stream cannot be.
type eventsRunner struct {
	*execx.Fake
	now, later []string
	delay      time.Duration
	open       bool
}

type eventsProc struct {
	once sync.Once
	done chan struct{}
}

func (p *eventsProc) Signal(os.Signal) error { p.once.Do(func() { close(p.done) }); return nil }
func (p *eventsProc) Wait() error            { <-p.done; return nil }
func (p *eventsProc) Pid() int               { return 4545 }

func (r *eventsRunner) Start(c execx.Cmd) (execx.Process, error) {
	if c.Name != "docker" || len(c.Args) == 0 || c.Args[0] != "events" {
		return r.Fake.Start(c)
	}
	r.Fake.Calls = append(r.Fake.Calls, c)
	name := eventsTarget(c)
	p := &eventsProc{done: make(chan struct{})}
	go func() {
		for _, l := range r.now {
			io.WriteString(c.Stdout, strings.ReplaceAll(l, thisContainer, name))
		}
		time.Sleep(r.delay)
		for _, l := range r.later {
			io.WriteString(c.Stdout, strings.ReplaceAll(l, thisContainer, name))
		}
		if !r.open {
			p.Signal(nil)
		}
	}()
	return p, nil
}
