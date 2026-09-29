package main

// Spec: spec/tmux.feature (CS-TMUX-010..019) and spec/launch.feature
// (CS-LNCH-109) — the tmux pane mark, end to end through MainWithEnv (and
// runSession directly for the restore-only paths) with tmux faked through
// execx.Fake: no real tmux, no real docker.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
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

	Describe("CS-TMUX-015: unmarked on every ordinary return path", func() {
		It("CS-TMUX-015: a non-zero exit", func() {
			f.fake.On("docker start -ai", "", execx.Fail(3))
			Expect(f.run()).To(Equal(3))
			Expect(tmuxLines()[len(tmuxLines())-1]).To(Equal(unset))
		})

		It("CS-TMUX-015: a forwarded signal", func() {
			f.fake.SessionSignal = syscall.SIGTERM
			f.run()
			Expect(tmuxLines()[len(tmuxLines())-1]).To(Equal(unset))
		})

		It("CS-TMUX-015: a signal during the die wait", func() {
			f.fake.LateSignal = syscall.SIGTERM
			f.run()
			Expect(tmuxLines()[len(tmuxLines())-1]).To(Equal(unset))
			Expect(tmuxLines()).To(HaveLen(3))
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
