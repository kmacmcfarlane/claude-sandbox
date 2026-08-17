package main

// Spec: spec/sessions.feature (CS-SESS) — the `sessions` subcommand and the
// launch-time multi-session decision, end to end through MainWithEnv with a
// scripted execx.Fake and prompt.Scripted.
//
// CS-SESS-037 is @manual: these tests assert the argv handed to docker, but
// whether a detach sequence actually reaches the docker client past the Claude
// Code TUI, the terminal's raw mode and any multiplexer is real-tty behavior.
// The detach/reattach/detach-again round trip was verified by hand; CS-SESS-036
// covers the part that can be automated, namely that every interactive docker
// path is given the keys in the first place.

import (
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/paths"
	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
)

const psSep = "\x1f"

// psRow builds one line of scripted `docker ps --format` output.
func psRow(name, status, project, instance string) string {
	return strings.Join([]string{name, status, project, "claude", instance, "v1", "", "", ""}, psSep)
}

// psRowFull additionally sets the model, config hash and inputs label.
func psRowFull(name, status, project, instance, model, hash, inputs string) string {
	return strings.Join([]string{name, status, project, "claude", instance, "v1", model, hash, inputs}, psSep)
}

var _ = Describe("sessions (CS-SESS)", func() {
	var f *cliFixture

	BeforeEach(func() { f = newCLIFixture() })

	// running scripts docker ps to return the given rows.
	running := func(rows ...string) {
		f.fake.On("docker ps", strings.Join(rows, "\n")+"\n", nil)
		f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
	}

	// tty makes prompts available and queues answers.
	tty := func(answers ...string) {
		f.env.Prompter = &prompt.Scripted{IsTTY: true, Answers: answers}
	}

	Describe("the sessions subcommand", func() {
		It("CS-SESS-010: lists the current project by default", func() {
			running(psRow("cs-a", "Up 2 hours", f.proj, "otter"))
			Expect(f.run("sessions")).To(Equal(0))
			Expect(f.out.String()).To(ContainSubstring("INSTANCE"))
			Expect(f.out.String()).To(ContainSubstring("otter"))
			Expect(f.out.String()).To(ContainSubstring("2 hours"))
			Expect(f.fake.CommandLines()[0]).To(ContainSubstring("label=claude-sandbox.project=" + f.proj))
		})

		It("CS-SESS-011: --all widens the scope and marks the current project", func() {
			running(
				psRow("cs-a", "Up 1 hour", f.proj, "otter"),
				psRow("cs-b", "Up 3 days", "/elsewhere", "heron"),
			)
			Expect(f.run("sessions", "--all")).To(Equal(0))
			out := f.out.String()
			Expect(out).To(ContainSubstring("PROJECT"))
			Expect(out).To(ContainSubstring("/elsewhere"))
			Expect(out).To(ContainSubstring("* otter"), "the current project's row is marked")
			Expect(out).NotTo(ContainSubstring("* heron"))
			// Filtering on the bare label key, with no project value.
			Expect(f.fake.CommandLines()[0]).To(ContainSubstring("label=claude-sandbox.project "))
		})

		It("CS-SESS-012: --json emits a JSON array", func() {
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"))
			Expect(f.run("sessions", "--json")).To(Equal(0))
			Expect(strings.TrimSpace(f.out.String())).To(HavePrefix("["))
			Expect(f.out.String()).To(ContainSubstring(`"instance": "otter"`))
		})

		It("CS-SESS-013: exits 0 and says so when nothing is running", func() {
			running()
			Expect(f.run("sessions")).To(Equal(0))
			Expect(f.out.String()).To(ContainSubstring("No running sandbox sessions for this project"))
		})
	})

	Describe("launch-time discovery", func() {
		It("CS-SESS-014: a clean launch is unchanged and needs no terminal", func() {
			running()
			Expect(f.run()).To(Equal(0))
			Expect(f.fake.Execed).NotTo(BeNil())
			Expect(f.execLine()).To(ContainSubstring("docker run"))
		})

		It("CS-SESS-015: discovery runs before any image build", func() {
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"))
			tty("q")
			Expect(f.run()).To(Equal(0))
			lines := f.fake.CommandLines()
			Expect(lines).NotTo(BeEmpty())
			psAt, buildAt := -1, -1
			for i, l := range lines {
				if psAt < 0 && strings.Contains(l, "docker ps") {
					psAt = i
				}
				if buildAt < 0 && strings.Contains(l, "docker build") {
					buildAt = i
				}
			}
			Expect(psAt).To(BeNumerically(">=", 0))
			if buildAt >= 0 {
				Expect(psAt).To(BeNumerically("<", buildAt))
			}
		})

		It("CS-SESS-019: with sessions running and no terminal, exits 3 without launching", func() {
			// The pre-existing hard failure was useful signal; it is preserved
			// rather than silently defaulting to some branch.
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"))
			f.env.Prompter = &prompt.Scripted{IsTTY: false}
			Expect(f.run()).To(Equal(3))
			Expect(f.errw.String()).To(ContainSubstring("no terminal is attached"))
			Expect(f.errw.String()).To(ContainSubstring("otter"), "the sessions found are reported before failing")
			Expect(f.fake.Execed).To(BeNil(), "nothing may be launched")
		})

		It("CS-SESS-016: [q] quits without launching", func() {
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"))
			tty("q")
			Expect(f.run()).To(Equal(0))
			Expect(f.fake.Execed).To(BeNil())
		})

		It("CS-SESS-016: an empty answer quits rather than launching or attaching", func() {
			// Ask returns "" on Enter, EOF and timeout; none of those is consent.
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"))
			tty("")
			Expect(f.run()).To(Equal(0))
			Expect(f.fake.Execed).To(BeNil())
		})

		It("CS-SESS-016: [n] launches a new container alongside the existing one", func() {
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"))
			tty("n")
			Expect(f.run()).To(Equal(0))
			line := f.execLine()
			Expect(line).To(ContainSubstring("docker run"))
			// A different instance noun than the one already in use.
			Expect(line).NotTo(ContainSubstring("-otter claude-sandbox"))
		})

		It("CS-SESS-016, CS-SESS-017, CS-SESS-031: [a] attaches, skipping tier 2 for a single candidate", func() {
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"))
			tty("a")
			Expect(f.run()).To(Equal(0))
			Expect(f.execLine()).To(Equal("docker attach --detach-keys=ctrl-q,ctrl-q cs-a"))
			Expect(f.out.String()).To(ContainSubstring("ctrl-q,ctrl-q"), "the detach sequence is printed")
		})

		It("CS-SESS-016, CS-SESS-032: [j] joins the container as the host user", func() {
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"))
			tty("j")
			Expect(f.run()).To(Equal(0))
			line := f.execLine()
			Expect(line).To(ContainSubstring("docker exec -it --detach-keys=ctrl-q,ctrl-q -u "))
			Expect(line).To(ContainSubstring(" -w " + f.proj + " cs-a claude"))
			Expect(f.out.String()).To(ContainSubstring("cannot be reattached"))
		})

		It("CS-SESS-036: all three docker paths carry the same detach keys", func() {
			// Docker applies its own ctrl-p,ctrl-q to any invocation that omits
			// the flag, so a path missing it is silently wrong rather than
			// obviously broken.
			byPath := map[string]string{}

			running()
			Expect(f.run()).To(Equal(0))
			byPath["run"] = f.execLine()

			for _, choice := range []string{"a", "j"} {
				g := newCLIFixture()
				g.fake.On("docker ps", psRow("cs-a", "Up 1 hour", g.proj, "otter")+"\n", nil)
				g.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
				g.env.Prompter = &prompt.Scripted{IsTTY: true, Answers: []string{choice}}
				Expect(g.run()).To(Equal(0))
				byPath[choice] = g.fake.Execed.Name + " " + strings.Join(g.fake.Execed.Args, " ")
			}

			for path, line := range byPath {
				Expect(line).To(ContainSubstring("--detach-keys=ctrl-q,ctrl-q"),
					"the %s path omits detach keys, so docker's ctrl-p,ctrl-q applies", path)
			}
			Expect(byPath["run"]).To(ContainSubstring("docker run"))
			Expect(byPath["a"]).To(ContainSubstring("docker attach"))
			Expect(byPath["j"]).To(ContainSubstring("docker exec"))
		})

		It("CS-SESS-036: detachKeys overrides every path together", func() {
			writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "detachKeys: ctrl-^\n")
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"))
			tty("a")
			Expect(f.run()).To(Equal(0))
			Expect(f.execLine()).To(ContainSubstring("--detach-keys=ctrl-^"))
			Expect(f.out.String()).To(ContainSubstring("ctrl-^"))
		})

		It("CS-SESS-018: tier 2 selects an instance when several are running", func() {
			running(
				psRow("cs-a", "Up 1 hour", f.proj, "otter"),
				psRow("cs-b", "Up 2 hours", f.proj, "heron"),
			)
			tty("a", "heron")
			Expect(f.run()).To(Equal(0))
			Expect(f.execLine()).To(HaveSuffix("cs-b"))
		})

		It("CS-SESS-018: an unknown instance at tier 2 fails and lists the choices", func() {
			running(
				psRow("cs-a", "Up 1 hour", f.proj, "otter"),
				psRow("cs-b", "Up 2 hours", f.proj, "heron"),
			)
			tty("a", "nosuch")
			Expect(f.run()).To(Equal(2))
			Expect(f.errw.String()).To(ContainSubstring("otter"))
			Expect(f.errw.String()).To(ContainSubstring("heron"))
		})

		It("CS-SESS-033: attach runs no image build or mount assembly", func() {
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"))
			tty("a")
			Expect(f.run()).To(Equal(0))
			Expect(strings.Join(f.fake.CommandLines(), "\n")).NotTo(ContainSubstring("docker build"))
		})
	})

	Describe("bypass flags (CS-SESS-028)", func() {
		BeforeEach(func() {
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"))
			f.env.Prompter = &prompt.Scripted{IsTTY: false}
		})

		It("--new launches without prompting and without a terminal", func() {
			Expect(f.run("--new")).To(Equal(0))
			Expect(f.execLine()).To(ContainSubstring("docker run"))
		})

		It("--no-session-check skips the decision but still names the container safely", func() {
			// The instance noun must not collide with a running session, so the
			// in-use nouns are still looked up. Skipping that would reintroduce
			// the container-name collisions this feature exists to fix.
			Expect(f.run("--no-session-check")).To(Equal(0))
			Expect(f.execLine()).To(ContainSubstring("docker run"))
			Expect(f.errw.String()).NotTo(ContainSubstring("Found 1 running session"))
			Expect(f.execLine()).NotTo(ContainSubstring("-otter claude-sandbox"))
		})

		It("--attach=INSTANCE attaches with no terminal", func() {
			Expect(f.run("--attach=otter")).To(Equal(0))
			Expect(f.execLine()).To(HaveSuffix("cs-a"))
		})

		It("--join=INSTANCE joins with no terminal", func() {
			Expect(f.run("--join=otter")).To(Equal(0))
			Expect(f.execLine()).To(ContainSubstring("docker exec"))
		})

		It("bare --attach is unambiguous with a single candidate", func() {
			Expect(f.run("--attach")).To(Equal(0))
			Expect(f.execLine()).To(HaveSuffix("cs-a"))
		})

		It("CS-SESS-030: an unknown instance name fails and lists what is available", func() {
			Expect(f.run("--attach=nosuchnoun")).To(Equal(2))
			Expect(f.errw.String()).To(ContainSubstring("no running session named 'nosuchnoun'"))
			Expect(f.errw.String()).To(ContainSubstring("otter"))
		})
	})

	It("CS-SESS-029: bare --attach with several candidates and no terminal exits 3", func() {
		running(
			psRow("cs-a", "Up 1 hour", f.proj, "otter"),
			psRow("cs-b", "Up 2 hours", f.proj, "heron"),
		)
		f.env.Prompter = &prompt.Scripted{IsTTY: false}
		Expect(f.run("--attach")).To(Equal(3))
		// The hint must name the flag actually in use, not a generic verb.
		Expect(f.errw.String()).To(ContainSubstring("--attach=otter"))
	})

	It("CS-SESS-029: bare --attach with several candidates prompts when a terminal exists", func() {
		running(
			psRow("cs-a", "Up 1 hour", f.proj, "otter"),
			psRow("cs-b", "Up 2 hours", f.proj, "heron"),
		)
		f.env.Prompter = &prompt.Scripted{IsTTY: true, Answers: []string{"heron"}}
		Expect(f.run("--attach")).To(Equal(0))
		Expect(f.execLine()).To(HaveSuffix("cs-b"))
	})

	Describe("config drift (CS-SESS-025..027)", func() {
		It("CS-SESS-022: a matching hash attaches with no drift prompt", func() {
			// Compute what this launch's hash would be, then claim the running
			// container carries it.
			hash := currentHash(f)
			running(psRowFull("cs-a", "Up 1 hour", f.proj, "otter", "", hash, "[]"))
			f.env.Prompter = &prompt.Scripted{IsTTY: false}
			Expect(f.run("--attach=otter")).To(Equal(0))
			Expect(f.errw.String()).NotTo(ContainSubstring("different configuration"))
		})

		It("CS-SESS-025: a differing hash prompts, and [c] continues", func() {
			running(psRowFull("cs-a", "Up 1 hour", f.proj, "otter", "", "stalehash1234",
				`[{"p":"/w/env","d":"aaaaaaaa","k":"env"}]`))
			f.env.Prompter = &prompt.Scripted{IsTTY: true, Answers: []string{"c"}}
			Expect(f.run("--attach=otter")).To(Equal(0))
			Expect(f.errw.String()).To(ContainSubstring("different configuration"))
			Expect(f.errw.String()).To(ContainSubstring("will NOT apply"))
			Expect(f.execLine()).To(ContainSubstring("docker attach"))
		})

		It("CS-SESS-025: [n] launches a new container with the current config instead", func() {
			running(psRowFull("cs-a", "Up 1 hour", f.proj, "otter", "", "stalehash1234", "[]"))
			f.env.Prompter = &prompt.Scripted{IsTTY: true, Answers: []string{"n"}}
			Expect(f.run("--attach=otter")).To(Equal(0))
			Expect(f.execLine()).To(ContainSubstring("docker run"))
		})

		It("CS-SESS-025: [q] quits", func() {
			running(psRowFull("cs-a", "Up 1 hour", f.proj, "otter", "", "stalehash1234", "[]"))
			f.env.Prompter = &prompt.Scripted{IsTTY: true, Answers: []string{"q"}}
			Expect(f.run("--attach=otter")).To(Equal(0))
			Expect(f.fake.Execed).To(BeNil())
		})

		It("CS-SESS-025: names the drifted files", func() {
			// The container recorded a digest for a file whose current digest
			// differs, so it must be reported as changed by name.
			running(psRowFull("cs-a", "Up 1 hour", f.proj, "otter", "", "stalehash1234",
				`[{"p":"<merged config>","d":"00000000","k":"config"}]`))
			f.env.Prompter = &prompt.Scripted{IsTTY: true, Answers: []string{"q"}}
			Expect(f.run("--attach=otter")).To(Equal(0))
			Expect(f.errw.String()).To(ContainSubstring("<merged config>"))
			Expect(f.errw.String()).To(MatchRegexp(`changed|added|removed`))
		})

		It("CS-SESS-026: --allow-config-drift skips the prompt", func() {
			running(psRowFull("cs-a", "Up 1 hour", f.proj, "otter", "", "stalehash1234", "[]"))
			f.env.Prompter = &prompt.Scripted{IsTTY: false}
			Expect(f.run("--attach=otter", "--allow-config-drift")).To(Equal(0))
			Expect(f.errw.String()).NotTo(ContainSubstring("different configuration"))
			Expect(f.execLine()).To(ContainSubstring("docker attach"))
		})

		It("CS-SESS-019: drift with no terminal exits 3", func() {
			running(psRowFull("cs-a", "Up 1 hour", f.proj, "otter", "", "stalehash1234", "[]"))
			f.env.Prompter = &prompt.Scripted{IsTTY: false}
			Expect(f.run("--attach=otter")).To(Equal(3))
			Expect(f.errw.String()).To(ContainSubstring("--allow-config-drift"))
			Expect(f.fake.Execed).To(BeNil())
		})

		It("CS-SESS-025: an absent hash label does not invent drift", func() {
			// A container from an older version carries no hash; there is nothing
			// to compare, so attaching must not be blocked.
			running(psRowFull("cs-a", "Up 1 hour", f.proj, "otter", "", "", ""))
			f.env.Prompter = &prompt.Scripted{IsTTY: false}
			Expect(f.run("--attach=otter")).To(Equal(0))
			Expect(f.execLine()).To(ContainSubstring("docker attach"))
		})

		It("CS-SESS-027: a model mismatch warns on attach", func() {
			hash := currentHash(f)
			running(psRowFull("cs-a", "Up 1 hour", f.proj, "otter", "sonnet", hash, "[]"))
			f.env.Prompter = &prompt.Scripted{IsTTY: false}
			Expect(f.run("--attach=otter", "--model", "opus")).To(Equal(0))
			Expect(f.errw.String()).To(ContainSubstring("cannot change a running session"))
		})

		It("CS-SESS-027: join passes the requested model to the new process", func() {
			hash := currentHash(f)
			running(psRowFull("cs-a", "Up 1 hour", f.proj, "otter", "sonnet", hash, "[]"))
			f.env.Prompter = &prompt.Scripted{IsTTY: false}
			Expect(f.run("--join=otter", "--model", "opus")).To(Equal(0))
			Expect(f.execLine()).To(HaveSuffix("claude --model opus"))
		})
	})

	Describe("ralph (CS-SESS-034/035)", func() {
		It("CS-SESS-034: reports running sessions but never prompts", func() {
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"))
			// No TTY: a prompt here would exit 3, so reaching docker run proves
			// ralph does not treat this as a decision.
			f.env.Prompter = &prompt.Scripted{IsTTY: false}
			Expect(f.run("--ralph")).To(Equal(0))
			Expect(f.errw.String()).To(ContainSubstring("otter"))
			Expect(f.execLine()).To(ContainSubstring("/opt/claude-sandbox/bin/ralph"))
		})

		It("CS-SESS-035: a ralph container carries no instance noun", func() {
			running()
			Expect(f.run("--ralph")).To(Equal(0))
			line := f.execLine()
			Expect(line).To(ContainSubstring("-ralph claude-sandbox"))
			Expect(line).NotTo(ContainSubstring("claude-sandbox.instance="))
		})

		It("CS-LNCH-032: labels reach docker run", func() {
			running()
			Expect(f.run()).To(Equal(0))
			line := f.execLine()
			Expect(line).To(ContainSubstring("--label claude-sandbox.project=" + f.proj))
			Expect(line).To(ContainSubstring("--label claude-sandbox.mode=claude"))
			Expect(line).To(ContainSubstring("--label claude-sandbox.instance="))
			Expect(line).To(ContainSubstring("--label claude-sandbox.confighash="))
		})
	})

	Describe("discovery robustness", func() {
		It("a docker ps failure surfaces rather than being ignored", func() {
			f.fake.On("docker ps", "", execx.Fail(1))
			Expect(f.run()).NotTo(Equal(0))
		})

		It("CS-SESS-034: a ralph container is not offered as an attach candidate", func() {
			f.fake.On("docker ps", strings.Join([]string{"cs-r", "Up 1 hour", f.proj, "ralph", "", "v1", "", "", ""}, psSep)+"\n", nil)
			f.fake.On("docker top", "PID  COMMAND\n1  /opt/claude-sandbox/bin/ralph\n", nil)
			f.env.Prompter = &prompt.Scripted{IsTTY: false}
			// Only a ralph container is running, so there is no decision to make
			// and an interactive launch proceeds without exiting 3.
			Expect(f.run()).To(Equal(0))
			Expect(f.execLine()).To(ContainSubstring("docker run"))
		})
	})
})

// currentHash returns the config hash the fixture's launch would produce, so a
// test can pretend a running container carries it. It resolves the cascade the
// same way runLaunch does.
func currentHash(f *cliFixture) string {
	fl, err := scanLaunchArgs(nil)
	Expect(err).NotTo(HaveOccurred())

	configFiles, err := paths.CollectUp(f.proj, paths.Config)
	Expect(err).NotTo(HaveOccurred())
	envFiles, err := paths.CollectUp(f.proj, paths.Env)
	Expect(err).NotTo(HaveOccurred())
	cfg, err := cascade.Load(configFiles)
	Expect(err).NotTo(HaveOccurred())

	hash, _ := wouldBeFingerprint(f.env, f.proj, fl, cfg, envFiles)
	Expect(hash).NotTo(BeEmpty())
	return hash
}
