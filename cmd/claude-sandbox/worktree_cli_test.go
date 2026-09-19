package main

// Spec: spec/launch.feature (CS-LNCH-041..047) and spec/sessions.feature
// (CS-SESS-045..047) — worktree mode end to end through MainWithEnv. The
// fixture's project directory is not a git work tree, so every test that wants
// a worktree scripts `git rev-parse --show-toplevel` (gitProject); the others
// exercise the stand-down (CS-LNCH-046). The interactive default is OFF, so
// tests that want a worktree ask for one (--worktree, the env var or the key).

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/imagebuild"
	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
)

// gitProject makes the fixture's project directory count as a git work tree.
func gitProject(f *cliFixture) {
	f.fake.On("rev-parse --show-toplevel", f.proj+"\n", nil)
}

// psRowWorktree builds a ps row for a session running in the named worktree.
func psRowWorktree(name, status, project, instance, worktree string) string {
	return strings.Join([]string{name, status, project, "claude", instance, "v1", "", "", "", "", worktree}, psSep)
}

var _ = Describe("worktree mode (CS-LNCH-041..047, CS-SESS-045..047)", func() {
	var f *cliFixture
	BeforeEach(func() { f = newCLIFixture() })

	nounRe := `[a-z]+(-[0-9]+)?`

	It("CS-LNCH-041, CS-LNCH-026: off by default — a plain launch is plain claude, no banner", func() {
		gitProject(f)
		Expect(f.run("--dangerous", "--model", "opus", "--resume")).To(Equal(0), f.errw.String())
		Expect(f.launchLine()).To(HaveSuffix(" claude-sandbox:run claude --dangerously-skip-permissions --model opus --resume"))
		Expect(f.launchLine()).NotTo(ContainSubstring("--worktree"))
		Expect(f.out.String()).NotTo(ContainSubstring("Worktree:"), "the shared-checkout default does not narrate itself")
	})

	It("CS-LNCH-041, CS-LNCH-026: --worktree — claude --worktree <instance> precedes --model and passthrough, and names the container", func() {
		gitProject(f)
		Expect(f.run("--worktree", "--dangerous", "--model", "opus", "--resume")).To(Equal(0), f.errw.String())
		line := f.launchLine()
		re := regexp.MustCompile(`--name claude-sandbox-` + regexp.QuoteMeta(imagebuild.ProjectSlug(f.proj)) +
			`-(` + nounRe + `) claude-sandbox:run claude --dangerously-skip-permissions --worktree (` + nounRe + `) --model opus --resume$`)
		m := re.FindStringSubmatch(line)
		Expect(m).NotTo(BeNil(), line)
		Expect(m[1]).To(Equal(m[3]), "container, worktree and branch share the instance noun")
		Expect(f.out.String()).To(ContainSubstring(
			"Worktree: " + m[1] + " (.claude/worktrees/" + m[1] + ", branch worktree-" + m[1] + ")"))
	})

	It("CS-LNCH-041: --no-worktree is accepted and launches plain claude, silently", func() {
		gitProject(f)
		Expect(f.run("--no-worktree")).To(Equal(0))
		Expect(f.launchLine()).To(HaveSuffix(" claude-sandbox:run claude"))
		Expect(f.out.String()).NotTo(ContainSubstring("Worktree:"))
	})

	It("CS-LNCH-041: --worktree is launcher-owned, never a passthrough boundary", func() {
		gitProject(f)
		Expect(f.run("--worktree", "--dangerous")).To(Equal(0), "--dangerous after --worktree is still consumed")
		Expect(f.launchLine()).To(MatchRegexp(` claude --dangerously-skip-permissions --worktree ` + nounRe + `$`))
	})

	DescribeTable("CS-LNCH-042: precedence is tri-state — CLI > env (falsy is an explicit off) > merged config > default (off interactive, on ralph)",
		func(yaml, envVal, cli string, ralph, want bool) {
			gitProject(f)
			if yaml != "" {
				writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "worktree: "+yaml+"\n")
			}
			if envVal != "" {
				f.envmap["CLAUDE_SANDBOX_WORKTREE"] = envVal
			}
			var args []string
			if ralph {
				args = append(args, "--ralph")
			}
			if cli != "" {
				args = append(args, cli)
			}
			Expect(f.run(args...)).To(Equal(0), f.errw.String())
			if want {
				Expect(f.launchLine()).To(ContainSubstring(" --worktree "))
			} else {
				Expect(f.launchLine()).NotTo(ContainSubstring("--worktree"))
			}
		},
		Entry("unset/unset/absent interactive -> off", "", "", "", false, false),
		Entry("unset/unset/absent ralph -> on", "", "", "", true, true),
		Entry("true/unset/absent interactive -> on", "true", "", "", false, true),
		Entry("false/unset/absent ralph -> off", "false", "", "", true, false),
		Entry("false/1/absent interactive -> on", "false", "1", "", false, true),
		Entry("true/0/absent ralph -> off", "true", "0", "", true, false),
		Entry("true/no/absent interactive -> off", "true", "no", "", false, false),
		Entry("unset/maybe/absent interactive -> off (unrecognized env is unset)", "", "maybe", "", false, false),
		Entry("unset/maybe/absent ralph -> on (unrecognized env is unset)", "", "maybe", "", true, true),
		Entry("true/unset/--no-worktree interactive -> off", "true", "", "--no-worktree", false, false),
		Entry("false/0/--worktree ralph -> on", "false", "0", "--worktree", true, true),
	)

	It("CS-LNCH-042: the cascade merges the key like any scalar — a local true overrides an upstream false", func() {
		gitProject(f)
		writeFile(filepath.Join(filepath.Dir(f.proj), ".claude-sandbox", "config.yaml"), "worktree: false\n")
		writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "worktree: true\n")
		Expect(f.run()).To(Equal(0))
		Expect(f.launchLine()).To(ContainSubstring(" claude --worktree "))

		g := newCLIFixture()
		gitProject(g)
		writeFile(filepath.Join(filepath.Dir(g.proj), ".claude-sandbox", "config.yaml"), "worktree: true\n")
		writeFile(filepath.Join(g.proj, ".claude-sandbox", "config.yaml"), "worktree: false\n")
		Expect(g.run()).To(Equal(0))
		Expect(g.launchLine()).NotTo(ContainSubstring("--worktree"))
	})

	It("CS-LNCH-043: --worktree=NAME names the worktree while the noun still names the container", func() {
		gitProject(f)
		Expect(f.run("--worktree=feature-x")).To(Equal(0))
		Expect(f.launchLine()).To(MatchRegexp(`--name claude-sandbox-\S+-` + nounRe + ` claude-sandbox:run claude --worktree feature-x$`))
		Expect(f.launchLine()).NotTo(ContainSubstring("-feature-x claude-sandbox:run"))
		Expect(f.out.String()).To(ContainSubstring("Worktree: feature-x (.claude/worktrees/feature-x, branch worktree-feature-x)"))
	})

	It("CS-LNCH-043: an invalid name exits 2 before any docker command runs", func() {
		for _, bad := range []string{"--worktree=has space", "--worktree=a/b", "--worktree=" + strings.Repeat("x", 65), "--worktree=.git"} {
			g := newCLIFixture()
			Expect(g.run(bad)).To(Equal(2), bad)
			Expect(g.errw.String()).To(ContainSubstring("--worktree"), bad)
			Expect(g.fake.Calls).To(BeEmpty(), "no command may run for %s", bad)
			Expect(g.fake.Session).To(BeNil())
		}
	})

	It("CS-LNCH-044: the worktree label records the name, empty when off", func() {
		gitProject(f)
		Expect(f.run("--worktree=feature-x")).To(Equal(0))
		Expect(f.launched().Args).To(ContainElement("claude-sandbox.worktree=feature-x"))

		g := newCLIFixture()
		Expect(g.run()).To(Equal(0))
		Expect(g.launched().Args).To(ContainElement("claude-sandbox.worktree="))
	})

	It("CS-LNCH-044: the key, flag, env var and name never register as drift", func() {
		bare := currentHash(f)
		writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "worktree: false\n")
		Expect(currentHash(f)).To(Equal(bare), "the config key is excluded from the merged-config digest")

		gitProject(f)
		f.envmap["CLAUDE_SANDBOX_WORKTREE"] = "1"
		Expect(f.run("--worktree=feature-x")).To(Equal(0))
		Expect(f.launched().Args).To(ContainElement("claude-sandbox.confighash=" + bare))
	})

	It("CS-LNCH-045, CS-LNCH-027: ralph launches carry --worktree ralph by default, before passthrough", func() {
		gitProject(f)
		Expect(f.run("--ralph", "--limit", "5", "--dangerous", "--verbose")).To(Equal(0))
		Expect(f.launchLine()).To(HaveSuffix(
			"-ralph claude-sandbox:run /opt/claude-sandbox/bin/ralph --limit 5 --dangerously-skip-permissions --worktree ralph --verbose"))
		Expect(f.out.String()).To(ContainSubstring("Worktree: ralph (.claude/worktrees/ralph, branch worktree-ralph)"))
	})

	It("CS-LNCH-045: ralph's worktree can be renamed or turned off", func() {
		gitProject(f)
		Expect(f.run("--ralph", "--worktree=nightly")).To(Equal(0))
		Expect(f.launchLine()).To(HaveSuffix("/opt/claude-sandbox/bin/ralph --worktree nightly"))

		g := newCLIFixture()
		gitProject(g)
		Expect(g.run("--ralph", "--no-worktree")).To(Equal(0))
		Expect(g.launchLine()).To(HaveSuffix("/opt/claude-sandbox/bin/ralph"))
	})

	It("CS-LNCH-046: outside a git work tree a requested worktree stands down with a banner, however it was requested", func() {
		for _, args := range [][]string{{}, {"--worktree"}, {"--worktree=feature-x"}, {"--ralph"}} {
			g := newCLIFixture() // no gitProject: rev-parse returns nothing
			g.envmap["CLAUDE_SANDBOX_WORKTREE"] = "1"
			Expect(g.run(args...)).To(Equal(0), "%v: %s", args, g.errw.String())
			Expect(g.launchLine()).NotTo(ContainSubstring("--worktree"), "%v", args)
			Expect(g.out.String()).To(ContainSubstring("Worktree: off (not a git repository)"), "%v", args)
			Expect(g.launched().Args).To(ContainElement("claude-sandbox.worktree="))
		}
		// --ralph asks by default; a plain interactive launch never asked.
		g := newCLIFixture()
		Expect(g.run("--ralph")).To(Equal(0))
		Expect(g.out.String()).To(ContainSubstring("Worktree: off (not a git repository)"))

		h := newCLIFixture()
		Expect(h.run()).To(Equal(0))
		Expect(h.launchLine()).NotTo(ContainSubstring("--worktree"))
		Expect(h.out.String()).NotTo(ContainSubstring("Worktree:"), "nothing was requested, nothing stood down")
	})

	It("CS-LNCH-047, CS-LNCH-029: the container always receives CLAUDE_SANDBOX_PROJECT_DIR", func() {
		for _, args := range [][]string{{}, {"--ralph"}, {"--no-worktree"}} {
			g := newCLIFixture()
			if len(args) == 0 {
				gitProject(g)
			}
			Expect(g.run(args...)).To(Equal(0))
			Expect(g.launched().Args).To(ContainElements("-e", "CLAUDE_SANDBOX_PROJECT_DIR="+g.proj), "%v", args)
		}
	})

	Describe("sessions (CS-SESS-045..047)", func() {
		running := func(rows ...string) {
			f.fake.On("docker ps", strings.Join(rows, "\n")+"\n", nil)
			f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
		}
		noTTY := func() { f.env.Prompter = &prompt.Scripted{IsTTY: false} }

		It("CS-SESS-045: the instance noun skips nouns whose worktree already exists, --no-session-check included", func() {
			// Every worktree but one already exists, so the pick is forced.
			for _, n := range sessions.Nouns {
				if n != "zenith" {
					Expect(os.MkdirAll(filepath.Join(f.proj, ".claude/worktrees", n), 0o755)).To(Succeed())
				}
			}
			gitProject(f)
			running()
			Expect(f.run("--worktree", "--no-session-check")).To(Equal(0))
			Expect(f.launchLine()).To(ContainSubstring("-zenith claude-sandbox:run claude --worktree zenith"))
		})

		It("CS-SESS-045: an explicit --worktree=NAME reopens an existing worktree on purpose", func() {
			Expect(os.MkdirAll(filepath.Join(f.proj, ".claude/worktrees/otter"), 0o755)).To(Succeed())
			gitProject(f)
			running()
			Expect(f.run("--worktree=otter")).To(Equal(0))
			Expect(f.launchLine()).To(HaveSuffix(" claude --worktree otter"))
		})

		It("CS-SESS-046: join enters its own worktree with a bare --worktree before --model when the mode resolves on", func() {
			gitProject(f)
			running(psRowWorktree("cs-a", "Up 1 hour", f.proj, "otter", "otter"))
			noTTY()
			f.envmap["CLAUDE_SANDBOX_WORKTREE"] = "1"
			Expect(f.run("--join=otter", "--model", "opus")).To(Equal(0), f.errw.String())
			Expect(f.sessionLine()).To(HaveSuffix("pidslot -- claude --worktree --model opus"))
			Expect(f.sessionLine()).NotTo(ContainSubstring("--worktree otter"), "never the primary's worktree")
		})

		It("CS-SESS-046: --worktree=NAME names the joined session's worktree", func() {
			gitProject(f)
			running(psRowWorktree("cs-a", "Up 1 hour", f.proj, "otter", "otter"))
			noTTY()
			Expect(f.run("--join=otter", "--worktree=side")).To(Equal(0))
			Expect(f.sessionLine()).To(HaveSuffix("pidslot -- claude --worktree side"))
		})

		It("CS-SESS-046: the default, --no-worktree, or a non-git project joins the shared checkout", func() {
			gitProject(f)
			running(psRowWorktree("cs-a", "Up 1 hour", f.proj, "otter", "otter"))
			noTTY()
			Expect(f.run("--join=otter")).To(Equal(0))
			Expect(f.sessionLine()).To(HaveSuffix("pidslot -- claude"), "the interactive default is the shared checkout")

			f.fake.Session = nil
			Expect(f.run("--join=otter", "--no-worktree")).To(Equal(0))
			Expect(f.sessionLine()).To(HaveSuffix("pidslot -- claude"))

			g := newCLIFixture() // not a git work tree
			g.fake.On("docker ps", psRow("cs-a", "Up 1 hour", g.proj, "otter")+"\n", nil)
			g.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
			g.env.Prompter = &prompt.Scripted{IsTTY: false}
			Expect(g.run("--join=otter")).To(Equal(0))
			Expect(g.sessionLine()).To(HaveSuffix("pidslot -- claude"))
		})

		It("CS-SESS-047: attach reports the session's worktree without blocking", func() {
			gitProject(f)
			running(psRowWorktree("cs-a", "Up 1 hour", f.proj, "otter", "otter"))
			noTTY()
			Expect(f.run("--attach=otter", "--worktree")).To(Equal(0), f.errw.String())
			Expect(f.sessionLine()).To(HavePrefix("docker attach "))
			Expect(f.errw.String()).To(ContainSubstring("Note: session 'otter' runs in worktree 'otter' (branch worktree-otter)."))
			Expect(f.errw.String()).NotTo(ContainSubstring("cannot change"))
		})

		It("CS-SESS-047: attach states the running session cannot be changed when the request differs", func() {
			gitProject(f)
			running(psRowWorktree("cs-a", "Up 1 hour", f.proj, "otter", "otter"))
			noTTY()
			Expect(f.run("--attach=otter", "--no-worktree")).To(Equal(0))
			Expect(f.errw.String()).To(ContainSubstring("runs in worktree 'otter'"))
			Expect(f.errw.String()).To(ContainSubstring("--worktree/--no-worktree cannot change a running session"))

			g := newCLIFixture()
			gitProject(g)
			g.fake.On("docker ps", psRow("cs-a", "Up 1 hour", g.proj, "otter")+"\n", nil)
			g.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
			g.env.Prompter = &prompt.Scripted{IsTTY: false}
			Expect(g.run("--attach=otter", "--worktree")).To(Equal(0))
			Expect(g.errw.String()).To(ContainSubstring("runs in the shared checkout; --worktree/--no-worktree cannot change a running session."))

			// The default request matches a shared-checkout session: a plain note.
			h := newCLIFixture()
			gitProject(h)
			h.fake.On("docker ps", psRow("cs-a", "Up 1 hour", h.proj, "otter")+"\n", nil)
			h.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
			h.env.Prompter = &prompt.Scripted{IsTTY: false}
			Expect(h.run("--attach=otter")).To(Equal(0))
			Expect(h.errw.String()).To(ContainSubstring("runs in the shared checkout."))
			Expect(h.errw.String()).NotTo(ContainSubstring("cannot change"))
		})

		It("CS-SESS-039: [b] composes --worktree <new-noun> with --continue --fork-session when the mode resolves on", func() {
			gitProject(f)
			running(psRowWorktree("cs-a", "Up 1 hour", f.proj, "otter", "otter"))
			f.env.Prompter = &prompt.Scripted{IsTTY: true, Answers: []string{"b"}}
			Expect(f.run("--worktree", "--verbose")).To(Equal(0))
			line := f.launchLine()
			Expect(line).To(MatchRegexp(` claude --worktree (` + nounRe + `) --continue --fork-session --verbose$`))
			Expect(line).NotTo(ContainSubstring("--worktree otter "), "the fork gets its own worktree")

			g := newCLIFixture()
			gitProject(g)
			g.fake.On("docker ps", psRowWorktree("cs-a", "Up 1 hour", g.proj, "otter", "otter")+"\n", nil)
			g.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
			g.env.Prompter = &prompt.Scripted{IsTTY: true, Answers: []string{"b"}}
			Expect(g.run("--verbose")).To(Equal(0))
			Expect(g.launchLine()).To(HaveSuffix(" claude --continue --fork-session --verbose"), "by default the fork shares the checkout")
		})

		It("CS-SESS-040: --branch composes --worktree <new-noun> with --resume --fork-session when the mode resolves on", func() {
			gitProject(f)
			running()
			Expect(f.run("--worktree", "--branch", "--name", "sidequest")).To(Equal(0))
			Expect(f.launchLine()).To(MatchRegexp(` claude --worktree ` + nounRe + ` --resume --fork-session --name sidequest$`))

			g := newCLIFixture()
			gitProject(g)
			g.fake.On("docker ps", "\n", nil)
			g.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
			Expect(g.run("--branch", "--name", "sidequest")).To(Equal(0))
			Expect(g.launchLine()).To(HaveSuffix(" claude --resume --fork-session --name sidequest"), "by default the fork shares the checkout")
		})

		It("CS-SESS-010, CS-SESS-012: the listing and JSON carry the worktree", func() {
			running(
				psRowWorktree("cs-a", "Up 1 hour", f.proj, "otter", "otter"),
				psRow("cs-b", "Up 2 hours", f.proj, "heron"),
			)
			Expect(f.run("sessions")).To(Equal(0))
			out := f.out.String()
			Expect(out).To(ContainSubstring("WORKTREE"))
			Expect(out).To(MatchRegexp(`(?m)^otter\s+otter\s+cs-a`))
			Expect(out).To(MatchRegexp(`(?m)^heron\s+-\s+cs-b`))

			f.out.Reset()
			Expect(f.run("sessions", "--json")).To(Equal(0))
			Expect(f.out.String()).To(ContainSubstring(`"worktree": "otter"`))
		})
	})
})
