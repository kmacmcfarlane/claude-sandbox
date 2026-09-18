package main

// Spec: spec/launch.feature CS-LNCH-058..067 (headless mode for SDK clients)
// and spec/sessions.feature CS-SESS-055 (headless containers are never
// session candidates), end to end through MainWithEnv with the fake runner
// and the recording launch lock.

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
)

// trapPrompter records every use. A headless launch must never reach the
// Env's prompter: runHeadless installs a non-interactive one in its place, so
// the TTY prompter (and with it /dev/tty) is never consulted.
type trapPrompter struct{ calls []string }

func (p *trapPrompter) Interactive() bool {
	p.calls = append(p.calls, "Interactive")
	return true
}

func (p *trapPrompter) Confirm(_, q string, def bool, _ time.Duration) bool {
	p.calls = append(p.calls, "Confirm "+q)
	return def
}

func (p *trapPrompter) Ask(_, q string, _ time.Duration) string {
	p.calls = append(p.calls, "Ask "+q)
	return "q"
}

// psRowMode is a running docker ps row with an explicit mode label.
func psRowMode(name, project, mode, instance, class string) string {
	return strings.Join([]string{name, "Up 1 hour", project, mode, instance, "v1", "", "", "", class, "", "running", ""}, psSep)
}

// createTail returns the create argv after "--name <name> <image>": the
// container command.
func createTail(args []string) []string {
	for i, a := range args {
		if a == "--name" && i+2 < len(args) {
			return args[i+3:]
		}
	}
	Fail("no --name in create argv")
	return nil
}

var _ = Describe("headless mode (CS-LNCH-058..067)", func() {
	var f *cliFixture
	BeforeEach(func() { f = newCLIFixture() })

	It("CS-LNCH-059, CS-LNCH-064: create is -i without -t and labelled headless; start has no detach keys", func() {
		writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "detachKeys: ctrl-^\n")
		Expect(f.run("headless", "--")).To(Equal(0), f.errw.String())
		args := f.launched().Args
		Expect(args[0:4]).To(Equal([]string{"create", "-i", "--rm", "--init"}))
		Expect(args).NotTo(ContainElement("-it"))
		Expect(args).NotTo(ContainElement("-t"))
		Expect(args).To(ContainElement("claude-sandbox.mode=headless"))
		Expect(args).NotTo(ContainElement("claude-sandbox.mode=claude"))
		name := nameOf(args)
		Expect(args).To(ContainElement(HavePrefix("claude-sandbox.instance=")))
		Expect(args).To(ContainElement(HavePrefix("claude-sandbox.pidclass=")))
		Expect(f.execLine()).To(Equal("docker start -ai " + name))
		// Reserved under the lock like every launch (CS-LNCH-057).
		Expect(f.lock.held()).To(ContainElement(HavePrefix("docker create -i --rm --init ")))
	})

	It("CS-LNCH-063: forwards the set allowlisted names as bare -e NAME and nothing else", func() {
		f.envmap["CLAUDE_CODE_ENTRYPOINT"] = "sdk-ts"
		f.envmap["PASEO_AGENT_ID"] = "x"
		f.envmap["PASEO_PASSWORD"] = "hunter2"
		f.envmap["PASEO_HOME"] = "/srv/paseo"
		Expect(f.run("headless", "--")).To(Equal(0), f.errw.String())
		args := f.launched().Args
		es := argPairsCLI(args, "-e")
		Expect(es).To(ContainElements("CLAUDE_CODE_ENTRYPOINT", "PASEO_AGENT_ID"))
		for _, e := range es {
			Expect(e).NotTo(HavePrefix("PASEO_PASSWORD"))
			Expect(e).NotTo(HavePrefix("PASEO_HOME"))
			Expect(e).NotTo(Equal("PASEO_AGENT_CWD"), "unset: not forwarded")
			Expect(e).NotTo(Equal("CLAUDE_AGENT_SDK_VERSION"), "unset: not forwarded")
		}
		Expect(strings.Join(args, " ")).NotTo(ContainSubstring("hunter2"))
		Expect(strings.Join(args, " ")).NotTo(ContainSubstring("sdk-ts"), "values stay out of argv")

		// The live-verification inputs: the exact create and start argv.
		GinkgoWriter.Printf("HEADLESS CREATE: %s\n", f.launchLine())
		GinkgoWriter.Printf("HEADLESS START: %s\n", f.execLine())
	})

	It("CS-LNCH-063: an interactive launch forwards none of the allowlist", func() {
		f.envmap["CLAUDE_CODE_ENTRYPOINT"] = "sdk-ts"
		f.envmap["PASEO_AGENT_ID"] = "x"
		Expect(f.run()).To(Equal(0), f.errw.String())
		es := argPairsCLI(f.launched().Args, "-e")
		Expect(es).NotTo(ContainElement("CLAUDE_CODE_ENTRYPOINT"))
		Expect(es).NotTo(ContainElement("PASEO_AGENT_ID"))
	})

	It("CS-LNCH-060: writes nothing to stdout; the launcher's messages go to stderr", func() {
		// Force images to build and a banner to print, so there is plenty of
		// launcher output to misroute.
		f.fake.On("image inspect", "", execx.Fail(1))
		f.fake.On("rev-parse --show-toplevel", f.proj+"\n", nil)
		Expect(f.run("headless", "--worktree", "--", "-p", "--output-format", "stream-json")).To(Equal(0), f.errw.String())
		Expect(f.out.String()).To(BeEmpty())
		Expect(f.errw.String()).To(ContainSubstring("Worktree: "))
		Expect(f.errw.Len()).To(BeNumerically(">", 0))

		// The same launch without headless does print on stdout, so the
		// assertion above is not vacuous.
		g := newCLIFixture()
		g.fake.On("image inspect", "", execx.Fail(1))
		g.fake.On("rev-parse --show-toplevel", g.proj+"\n", nil)
		Expect(g.run("--worktree")).To(Equal(0), g.errw.String())
		Expect(g.out.String()).To(ContainSubstring("Worktree: "))
	})

	It("CS-LNCH-061: never prompts and implies --new, even with sessions running and a terminal", func() {
		trap := &trapPrompter{}
		f.env.Prompter = trap
		f.fake.On("docker ps", psRowMode("cs-a", f.proj, "claude", "otter", "1")+"\n", nil)
		f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
		Expect(f.run("headless", "--")).To(Equal(0), f.errw.String())
		Expect(trap.calls).To(BeEmpty(), "the Env's prompter (the /dev/tty one in production) is never used")
		Expect(f.launched()).NotTo(BeNil(), "a new container, no session decision")
		Expect(f.errw.String()).NotTo(ContainSubstring("[n] new session"))

		// Without a terminal, the same launch without headless needs a decision.
		g := newCLIFixture()
		g.fake.On("docker ps", psRowMode("cs-a", g.proj, "claude", "otter", "1")+"\n", nil)
		g.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
		Expect(g.run()).To(Equal(exitDecisionRequired))
	})

	It("CS-LNCH-061: the new-layout .gitignore prompt is skipped as with no terminal", func() {
		trap := &trapPrompter{}
		f.env.Prompter = trap
		Expect(os.MkdirAll(filepath.Join(f.proj, ".claude-sandbox"), 0o755)).To(Succeed())
		Expect(f.run("headless", "--")).To(Equal(0), f.errw.String())
		Expect(trap.calls).To(BeEmpty())
		Expect(f.out.String()).To(BeEmpty())
	})

	Describe("CS-LNCH-062: update check", func() {
		stub := func(g *cliFixture) {
			g.fake.On("claude-sandbox.claude-version", "1.2.3\n", nil)
			g.fake.On("npm view @anthropic-ai/claude-code version", "1.2.4\n", nil)
		}
		npmCalled := func(g *cliFixture) bool {
			for _, l := range g.fake.CommandLines() {
				if strings.Contains(l, "npm view") {
					return true
				}
			}
			return false
		}

		It("the post-build cache-budget check never runs; an interactive build still runs it", func() {
			f.fake.On("image inspect claude-sandbox:run", "", execx.Fail(1))
			Expect(f.run("headless", "--", "--version")).To(Equal(0), f.errw.String())
			lines := strings.Join(f.fake.CommandLines(), "\n")
			Expect(lines).To(ContainSubstring("docker build "), "the test must exercise a build")
			Expect(lines).NotTo(ContainSubstring("system df"))

			g := newCLIFixture()
			g.fake.On("image inspect claude-sandbox:run", "", execx.Fail(1))
			Expect(g.run()).To(Equal(0), g.errw.String())
			Expect(g.fake.CommandLines()).To(ContainElement("docker system df --format {{json .}}"))
		})

		It("is off by default", func() {
			stub(f)
			Expect(f.run("headless", "--")).To(Equal(0), f.errw.String())
			Expect(npmCalled(f)).To(BeFalse())
		})

		It("runs with --update and rebuilds without asking", func() {
			trap := &trapPrompter{}
			f.env.Prompter = trap
			stub(f)
			Expect(f.run("headless", "--update", "--")).To(Equal(0), f.errw.String())
			Expect(npmCalled(f)).To(BeTrue())
			Expect(trap.calls).To(BeEmpty())
			Expect(f.out.String()).To(BeEmpty())
		})
	})

	Describe("CS-LNCH-058: grammar", func() {
		expectTail := func(g *cliFixture, want ...string) {
			args := g.launched().Args
			tail := createTail(args)
			Expect(tail).To(Equal(append([]string{"claude"}, want...)))
		}

		It("passes everything after -- to claude verbatim", func() {
			cases := [][]string{
				{"--version"},
				{"auth", "status"},
				{"--resume=abc"},
				{"--session-id=0b6f", "--setting-sources=user,project,local"},
				{"--output-format", "stream-json", "--verbose", "--input-format", "stream-json",
					"--mcp-config", `{"mcpServers":{"a b":{"command":"x \"y\"","args":["--flag", "two words"]}}}`,
					"--settings", `{"permissions": {"allow": ["Bash(git log:*)"]}, "env": {"K": "v w"}}`,
					"--help", "--", "tail"},
			}
			for _, c := range cases {
				g := newCLIFixture()
				Expect(g.run(append([]string{"headless", "--"}, c...)...)).To(Equal(0), "%v: %s", c, g.errw.String())
				expectTail(g, c...)
				Expect(g.out.String()).To(BeEmpty(), "%v", c)
			}
		})

		It("--version and --help after headless are claude's", func() {
			Expect(f.run("headless", "--version")).To(Equal(0), f.errw.String())
			expectTail(f, "--version")
			g := newCLIFixture()
			Expect(g.run("headless", "--help")).To(Equal(0), g.errw.String())
			expectTail(g, "--help")
			Expect(g.out.String()).NotTo(ContainSubstring("Usage:"))
		})

		It("launcher flags before -- keep their meaning", func() {
			Expect(f.run("headless", "--model", "opus", "--dangerous", "--", "--version")).To(Equal(0), f.errw.String())
			expectTail(f, "--dangerously-skip-permissions", "--model", "opus", "--version")
		})

		It("rejects the flags that contradict one new non-ralph container", func() {
			for _, a := range [][]string{
				{"--ralph"}, {"--limit", "3"}, {"--attach"}, {"--attach=otter"},
				{"--join"}, {"--join=otter"}, {"--branch"},
			} {
				g := newCLIFixture()
				Expect(g.run(append([]string{"headless"}, a...)...)).To(Equal(2), "%v", a)
				Expect(g.fake.Execed).To(BeNil(), "%v", a)
				Expect(g.errw.String()).To(ContainSubstring("not valid with headless"), "%v", a)
			}
		})

		It("is recognized only as the first argument", func() {
			Expect(f.run("--rebuild", "headless")).To(Equal(2))
			Expect(f.errw.String()).To(ContainSubstring("'headless' must be the first argument"))
			Expect(f.fake.Execed).To(BeNil())
		})

		It("the launcher's own --version still applies before headless", func() {
			Expect(f.run("--version")).To(Equal(0))
			Expect(f.fake.Execed).To(BeNil())
		})

		It("is registered with cobra, so help lists it", func() {
			Expect(f.run("help")).To(Equal(0))
			Expect(f.out.String()).To(ContainSubstring("headless"))
			g := newCLIFixture()
			Expect(g.run("help", "headless")).To(Equal(0))
			Expect(g.out.String()).To(ContainSubstring("claude-sandbox headless"))
		})
	})

	Describe("CS-LNCH-066: worktree only on an explicit flag", func() {
		gitRepo := func(g *cliFixture) { g.fake.On("rev-parse --show-toplevel", g.proj+"\n", nil) }
		hasWorktree := func(g *cliFixture) bool {
			return strings.Contains(" "+strings.Join(createTail(g.launched().Args), " ")+" ", " --worktree ")
		}

		It("ignores worktree: true in the cascade", func() {
			gitRepo(f)
			writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "worktree: true\n")
			Expect(f.run("headless", "--", "--resume=abc")).To(Equal(0), f.errw.String())
			Expect(hasWorktree(f)).To(BeFalse())
			Expect(createTail(f.launched().Args)).To(Equal([]string{"claude", "--resume=abc"}))
			Expect(f.errw.String()).NotTo(ContainSubstring("Worktree:"))
		})

		It("ignores CLAUDE_SANDBOX_WORKTREE=1", func() {
			gitRepo(f)
			f.envmap["CLAUDE_SANDBOX_WORKTREE"] = "1"
			Expect(f.run("headless", "--")).To(Equal(0), f.errw.String())
			Expect(hasWorktree(f)).To(BeFalse())
		})

		It("uses one when --worktree or --worktree=NAME precedes --", func() {
			gitRepo(f)
			Expect(f.run("headless", "--worktree", "--")).To(Equal(0), f.errw.String())
			Expect(hasWorktree(f)).To(BeTrue())
			g := newCLIFixture()
			gitRepo(g)
			Expect(g.run("headless", "--worktree=paseo", "--")).To(Equal(0), g.errw.String())
			Expect(createTail(g.launched().Args)).To(Equal([]string{"claude", "--worktree", "paseo"}))
		})

		It("an interactive launch still honours the cascade key", func() {
			gitRepo(f)
			writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "worktree: true\n")
			Expect(f.run()).To(Equal(0), f.errw.String())
			Expect(hasWorktree(f)).To(BeTrue())
		})
	})

	Describe("CS-LNCH-067: the cascade's dangerous mode applies to headless", func() {
		It("dangerous: true adds --dangerously-skip-permissions ahead of the client's --permission-mode", func() {
			writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "dangerous: true\n")
			Expect(f.run("headless", "--", "--permission-mode", "plan")).To(Equal(0), f.errw.String())
			Expect(createTail(f.launched().Args)).To(Equal([]string{"claude", "--dangerously-skip-permissions", "--permission-mode", "plan"}))
		})

		It("a more-local dangerous: false turns it off", func() {
			writeFile(filepath.Join(filepath.Dir(f.proj), ".claude-sandbox", "config.yaml"), "dangerous: true\n")
			writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "dangerous: false\n")
			Expect(f.run("headless", "--", "--permission-mode", "plan")).To(Equal(0), f.errw.String())
			Expect(createTail(f.launched().Args)).To(Equal([]string{"claude", "--permission-mode", "plan"}))
		})

		It("CLAUDE_SANDBOX_DANGEROUS=0 does not turn it off", func() {
			writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "dangerous: true\n")
			f.envmap["CLAUDE_SANDBOX_DANGEROUS"] = "0"
			Expect(f.run("headless", "--")).To(Equal(0), f.errw.String())
			Expect(createTail(f.launched().Args)).To(ContainElement("--dangerously-skip-permissions"))
		})
	})

	It("CS-LNCH-065: without PROJECT_DIR the project is the physical working directory", func() {
		link := filepath.Join(filepath.Dir(f.proj), "link")
		Expect(os.Symlink(f.proj, link)).To(Succeed())
		delete(f.envmap, "PROJECT_DIR")
		GinkgoT().Chdir(link)
		GinkgoT().Setenv("PWD", link)
		Expect(f.run("headless", "--")).To(Equal(0), f.errw.String())
		args := f.launched().Args
		Expect(args).To(ContainElements("-w", f.proj))
		Expect(args).To(ContainElement(f.proj + ":" + f.proj))
		Expect(strings.Join(args, " ")).NotTo(ContainSubstring(link))
		Expect(f.out.String()).To(BeEmpty(), "the Project: redirect line goes to stderr")
		Expect(f.errw.String()).To(ContainSubstring("Project: " + f.proj + " (resolved from " + link + ")"))
	})
})

var _ = Describe("headless containers are not candidates (CS-SESS-055)", func() {
	var f *cliFixture
	BeforeEach(func() {
		f = newCLIFixture()
		f.fake.On("docker ps", psRowMode("cs-h", f.proj, sessions.ModeHeadless, "heron", "4")+"\n", nil)
		f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
	})

	It("CS-SESS-055: alone it never triggers the session decision", func() {
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(f.launched()).NotTo(BeNil())
		Expect(nameOf(f.launched().Args)).NotTo(HaveSuffix("-heron"), "its noun stays taken")
	})

	It("CS-SESS-055: --attach and --join do not offer it", func() {
		Expect(f.run("--attach")).To(Equal(2))
		Expect(f.errw.String()).To(ContainSubstring("no running sessions"))
		g := newCLIFixture()
		g.fake.On("docker ps", psRowMode("cs-h", g.proj, sessions.ModeHeadless, "heron", "4")+"\n", nil)
		Expect(g.run("--join=heron")).To(Equal(2))
		Expect(g.fake.Execed).To(BeNil())
	})

	It("CS-SESS-055: the --attach= completion does not offer it", func() {
		Expect(f.complete("--attach=").names).To(BeEmpty())
	})

	It("CS-SESS-055: sessions lists it, marked headless", func() {
		Expect(f.run("sessions")).To(Equal(0), f.errw.String())
		Expect(f.out.String()).To(MatchRegexp(`heron\s+-\s+cs-h\s+headless\s`))
		g := newCLIFixture()
		g.fake.On("docker ps", psRowMode("cs-h", g.proj, sessions.ModeHeadless, "heron", "4")+"\n", nil)
		Expect(g.run("sessions", "--json")).To(Equal(0))
		Expect(g.out.String()).To(ContainSubstring(`"mode": "headless"`))
	})
})

// argPairsCLI collects the values following each occurrence of flag.
func argPairsCLI(args []string, flag string) []string {
	var out []string
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}
