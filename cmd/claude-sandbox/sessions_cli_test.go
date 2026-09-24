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
	"os"
	"path/filepath"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/paths"
	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
)

const psSep = "\x1f"

// psRow builds one line of scripted `docker ps --format` output.
func psRow(name, status, project, instance string) string {
	return strings.Join([]string{name, status, project, "claude", instance, "v1", "", "", "", "", ""}, psSep)
}

// psRowFull additionally sets the model, config hash and inputs label.
func psRowFull(name, status, project, instance, model, hash, inputs string) string {
	return strings.Join([]string{name, status, project, "claude", instance, "v1", model, hash, inputs, "", ""}, psSep)
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
			Expect(f.out.String()).To(ContainSubstring("No sandbox sessions for this project"))
		})
	})

	Describe("launch-time discovery", func() {
		It("CS-SESS-014: a clean launch is unchanged and needs no terminal", func() {
			running()
			Expect(f.run()).To(Equal(0))
			Expect(f.fake.Session).NotTo(BeNil())
			Expect(f.launchLine()).To(HavePrefix("docker create "))
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
			Expect(f.fake.Session).To(BeNil(), "nothing may be launched")
		})

		It("CS-SESS-016: [q] quits without launching", func() {
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"))
			tty("q")
			Expect(f.run()).To(Equal(0))
			Expect(f.fake.Session).To(BeNil())
		})

		It("CS-SESS-016: an empty answer quits rather than launching or attaching", func() {
			// Ask returns "" on Enter, EOF and timeout; none of those is consent.
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"))
			tty("")
			Expect(f.run()).To(Equal(0))
			Expect(f.fake.Session).To(BeNil())
		})

		It("CS-SESS-016: [n] launches a new container alongside the existing one", func() {
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"))
			tty("n")
			Expect(f.run()).To(Equal(0))
			line := f.launchLine()
			Expect(line).To(HavePrefix("docker create "))
			// A different instance noun than the one already in use.
			Expect(line).NotTo(ContainSubstring("-otter claude-sandbox"))
		})

		It("CS-SESS-016, CS-SESS-017, CS-SESS-031: [a] attaches, skipping tier 2 for a single candidate", func() {
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"))
			tty("a")
			Expect(f.run()).To(Equal(0))
			Expect(f.sessionLine()).To(Equal("docker attach --detach-keys=ctrl-q,ctrl-q cs-a"))
			Expect(f.out.String()).To(ContainSubstring("ctrl-q,ctrl-q"), "the detach sequence is printed")
		})

		It("CS-SESS-016, CS-SESS-032, CS-SESS-044, CS-PID-005: [j] joins the container as the host user, through the pid-class helper", func() {
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"))
			tty("j")
			Expect(f.run()).To(Equal(0))
			line := f.sessionLine()
			Expect(line).To(ContainSubstring("docker exec -it --detach-keys=ctrl-q,ctrl-q -u "))
			Expect(line).To(ContainSubstring(" -w " + f.proj + " cs-a /opt/claude-sandbox/bin/claude-sandbox pidslot -- claude"))
			Expect(f.out.String()).To(ContainSubstring("cannot be reattached"))
		})

		It("CS-SESS-075: [j] marks the joined claude as not the primary, on the exec only", func() {
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"))
			tty("j")
			Expect(f.run()).To(Equal(0))
			Expect(f.sessionLine()).To(MatchRegexp(` -u \S+ -e CLAUDE_SANDBOX_JOINED=1 -w `))
		})

		It("CS-SESS-075: a new container never carries the join marker", func() {
			running()
			Expect(f.run()).To(Equal(0))
			Expect(f.launchLine()).To(HavePrefix("docker create "))
			Expect(f.launchLine()).NotTo(ContainSubstring("CLAUDE_SANDBOX_JOINED"))
		})

		It("CS-SESS-064: [j] honours a cascade dangerous: true", func() {
			writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "dangerous: true\n")
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"))
			tty("j")
			Expect(f.run()).To(Equal(0))
			Expect(f.sessionLine()).To(HaveSuffix(" pidslot -- claude --dangerously-skip-permissions"))
		})

		It("CS-SESS-036: all three docker paths carry the same detach keys", func() {
			// Docker applies its own ctrl-p,ctrl-q to any invocation that omits
			// the flag, so a path missing it is silently wrong rather than
			// obviously broken.
			byPath := map[string]string{}

			running()
			Expect(f.run()).To(Equal(0))
			byPath["start"] = f.sessionLine()
			// CS-LNCH-057: the keys belong to the attaching client, never to create.
			Expect(f.launchLine()).NotTo(ContainSubstring("--detach-keys"))

			for _, choice := range []string{"a", "j"} {
				g := newCLIFixture()
				g.fake.On("docker ps", psRow("cs-a", "Up 1 hour", g.proj, "otter")+"\n", nil)
				g.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
				g.env.Prompter = &prompt.Scripted{IsTTY: true, Answers: []string{choice}}
				Expect(g.run()).To(Equal(0))
				byPath[choice] = g.fake.Session.Name + " " + strings.Join(g.fake.Session.Args, " ")
			}

			for path, line := range byPath {
				Expect(line).To(ContainSubstring("--detach-keys=ctrl-q,ctrl-q"),
					"the %s path omits detach keys, so docker's ctrl-p,ctrl-q applies", path)
			}
			Expect(byPath["start"]).To(HavePrefix("docker start -ai "))
			Expect(byPath["a"]).To(ContainSubstring("docker attach"))
			Expect(byPath["j"]).To(ContainSubstring("docker exec"))
		})

		It("CS-SESS-036: detachKeys overrides every path together", func() {
			writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "detachKeys: ctrl-^\n")
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"))
			tty("a")
			Expect(f.run()).To(Equal(0))
			Expect(f.sessionLine()).To(ContainSubstring("--detach-keys=ctrl-^"))
			Expect(f.out.String()).To(ContainSubstring("ctrl-^"))
		})

		It("CS-SESS-018: tier 2 selects an instance when several are running", func() {
			running(
				psRow("cs-a", "Up 1 hour", f.proj, "otter"),
				psRow("cs-b", "Up 2 hours", f.proj, "heron"),
			)
			tty("a", "heron")
			Expect(f.run()).To(Equal(0))
			Expect(f.sessionLine()).To(HaveSuffix("cs-b"))
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
			Expect(f.launchLine()).To(HavePrefix("docker create "))
		})

		It("--no-session-check skips the decision but still names the container safely", func() {
			// The instance noun must not collide with a running session, so the
			// in-use nouns are still looked up. Skipping that would reintroduce
			// the container-name collisions this feature exists to fix.
			Expect(f.run("--no-session-check")).To(Equal(0))
			Expect(f.launchLine()).To(HavePrefix("docker create "))
			Expect(f.errw.String()).NotTo(ContainSubstring("Found 1 running session"))
			Expect(f.launchLine()).NotTo(ContainSubstring("-otter claude-sandbox"))
		})

		It("--attach=INSTANCE attaches with no terminal", func() {
			Expect(f.run("--attach=otter")).To(Equal(0))
			Expect(f.sessionLine()).To(HaveSuffix("cs-a"))
		})

		It("--join=INSTANCE joins with no terminal", func() {
			Expect(f.run("--join=otter")).To(Equal(0))
			Expect(f.sessionLine()).To(ContainSubstring("docker exec"))
		})

		It("CS-SESS-064: --join honours a cascade dangerous: true", func() {
			writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "dangerous: true\n")
			Expect(f.run("--join=otter")).To(Equal(0))
			Expect(f.sessionLine()).To(HaveSuffix(" pidslot -- claude --dangerously-skip-permissions"))
		})

		It("CS-SESS-064: --join honours CLAUDE_SANDBOX_DANGEROUS=1", func() {
			f.envmap["CLAUDE_SANDBOX_DANGEROUS"] = "1"
			Expect(f.run("--join=otter")).To(Equal(0))
			Expect(f.sessionLine()).To(HaveSuffix(" pidslot -- claude --dangerously-skip-permissions"))
		})

		It("CS-SESS-064: --join still honours the --dangerous flag", func() {
			Expect(f.run("--join=otter", "--dangerous")).To(Equal(0))
			Expect(f.sessionLine()).To(HaveSuffix(" pidslot -- claude --dangerously-skip-permissions"))
		})

		It("CS-SESS-064: a more-local dangerous: false keeps a join out of dangerous mode", func() {
			writeFile(filepath.Join(filepath.Dir(f.proj), ".claude-sandbox", "config.yaml"), "dangerous: true\n")
			writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "dangerous: false\n")
			Expect(f.run("--join=otter")).To(Equal(0))
			Expect(f.sessionLine()).NotTo(ContainSubstring("--dangerously-skip-permissions"))
		})

		It("bare --attach is unambiguous with a single candidate", func() {
			Expect(f.run("--attach")).To(Equal(0))
			Expect(f.sessionLine()).To(HaveSuffix("cs-a"))
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
		Expect(f.sessionLine()).To(HaveSuffix("cs-b"))
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

		It("CS-SESS-038: the would-be fingerprint resolves the cap image's ID, not its parent's", func() {
			running(psRowFull("cs-a", "Up 1 hour", f.proj, "otter", "", "stalehash1234", "[]"))
			f.env.Prompter = &prompt.Scripted{IsTTY: true, Answers: []string{"c"}}
			Expect(f.run("--attach=otter")).To(Equal(0))
			lines := f.fake.CommandLines()
			Expect(lines).To(ContainElement("docker image inspect -f {{.Id}} claude-sandbox:run"))
			Expect(lines).NotTo(ContainElement("docker image inspect -f {{.Id}} claude-sandbox"))
		})

		It("CS-SESS-025: a differing hash prompts, and [c] continues", func() {
			running(psRowFull("cs-a", "Up 1 hour", f.proj, "otter", "", "stalehash1234",
				`[{"p":"/w/env","d":"aaaaaaaa","k":"env"}]`))
			f.env.Prompter = &prompt.Scripted{IsTTY: true, Answers: []string{"c"}}
			Expect(f.run("--attach=otter")).To(Equal(0))
			Expect(f.errw.String()).To(ContainSubstring("different configuration"))
			Expect(f.errw.String()).To(ContainSubstring("will NOT apply"))
			Expect(f.sessionLine()).To(ContainSubstring("docker attach"))
		})

		It("CS-SESS-025: a container without an instance label is named by its mode", func() {
			running(psRowFull("cs-a", "Up 1 hour", f.proj, "", "", "stalehash1234", "[]"))
			f.env.Prompter = &prompt.Scripted{IsTTY: true, Answers: []string{"q"}}
			Expect(f.run("--attach")).To(Equal(0))
			Expect(f.errw.String()).To(ContainSubstring("Session 'claude' was started with different configuration"))
		})

		It("CS-SESS-025: [n] launches a new container with the current config instead", func() {
			running(psRowFull("cs-a", "Up 1 hour", f.proj, "otter", "", "stalehash1234", "[]"))
			f.env.Prompter = &prompt.Scripted{IsTTY: true, Answers: []string{"n"}}
			Expect(f.run("--attach=otter")).To(Equal(0))
			Expect(f.launchLine()).To(HavePrefix("docker create "))
		})

		It("CS-SESS-025: [q] quits", func() {
			running(psRowFull("cs-a", "Up 1 hour", f.proj, "otter", "", "stalehash1234", "[]"))
			f.env.Prompter = &prompt.Scripted{IsTTY: true, Answers: []string{"q"}}
			Expect(f.run("--attach=otter")).To(Equal(0))
			Expect(f.fake.Session).To(BeNil())
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
			Expect(f.sessionLine()).To(ContainSubstring("docker attach"))
		})

		It("CS-SESS-019: drift with no terminal exits 3", func() {
			running(psRowFull("cs-a", "Up 1 hour", f.proj, "otter", "", "stalehash1234", "[]"))
			f.env.Prompter = &prompt.Scripted{IsTTY: false}
			Expect(f.run("--attach=otter")).To(Equal(3))
			Expect(f.errw.String()).To(ContainSubstring("--allow-config-drift"))
			Expect(f.fake.Session).To(BeNil())
		})

		It("CS-SESS-025: an absent hash label does not invent drift", func() {
			// A container from an older version carries no hash; there is nothing
			// to compare, so attaching must not be blocked.
			running(psRowFull("cs-a", "Up 1 hour", f.proj, "otter", "", "", ""))
			f.env.Prompter = &prompt.Scripted{IsTTY: false}
			Expect(f.run("--attach=otter")).To(Equal(0))
			Expect(f.sessionLine()).To(ContainSubstring("docker attach"))
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
			Expect(f.sessionLine()).To(HaveSuffix("claude --model opus"))
		})
	})

	Describe("ralph (CS-SESS-034/035)", func() {
		It("CS-SESS-034: reports running sessions but never prompts", func() {
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"))
			// No TTY: a prompt here would exit 3, so reaching docker create proves
			// ralph does not treat this as a decision.
			f.env.Prompter = &prompt.Scripted{IsTTY: false}
			Expect(f.run("--ralph")).To(Equal(0))
			Expect(f.errw.String()).To(ContainSubstring("otter"))
			Expect(f.launchLine()).To(ContainSubstring("/opt/claude-sandbox/bin/ralph"))
		})

		It("CS-SESS-035: a ralph container carries no instance noun", func() {
			running()
			Expect(f.run("--ralph")).To(Equal(0))
			line := f.launchLine()
			Expect(line).To(ContainSubstring("-ralph claude-sandbox"))
			Expect(line).NotTo(ContainSubstring("claude-sandbox.instance="))
		})

		It("CS-LNCH-032: labels reach docker create", func() {
			running()
			Expect(f.run()).To(Equal(0))
			line := f.launchLine()
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
			Expect(f.launchLine()).To(HavePrefix("docker create "))
		})
	})

	Describe("branching a conversation (CS-SESS-039..042)", func() {
		It("CS-SESS-016, CS-SESS-039: [b] launches a new container forking the newest conversation", func() {
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"))
			tty("b")
			Expect(f.run()).To(Equal(0))
			line := f.launchLine()
			Expect(line).To(HavePrefix("docker create "))
			Expect(line).To(HaveSuffix(" claude --continue --fork-session"))
			// A fresh container with its own instance noun; the running one is untouched.
			Expect(line).NotTo(ContainSubstring("-otter claude-sandbox"))
		})

		It("CS-SESS-039: [b] puts the fork flags before the user's passthrough", func() {
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"))
			tty("b")
			Expect(f.run("--verbose")).To(Equal(0))
			Expect(f.launchLine()).To(HaveSuffix(" claude --continue --fork-session --verbose"))
		})

		It("CS-SESS-040, CS-SESS-041: --branch bypasses the session prompt and uses claude's picker", func() {
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"))
			// No terminal: reaching docker create proves the decision was removed
			// rather than exiting 3 (CS-SESS-028/041).
			f.env.Prompter = &prompt.Scripted{IsTTY: false}
			Expect(f.run("--branch")).To(Equal(0))
			line := f.launchLine()
			Expect(line).To(HavePrefix("docker create "))
			Expect(line).To(HaveSuffix(" claude --resume --fork-session"))
			Expect(f.errw.String()).NotTo(ContainSubstring("Choice ["))
		})

		It("CS-SESS-040: --branch needs no running session — a past conversation can be branched", func() {
			running()
			Expect(f.run("--branch")).To(Equal(0))
			Expect(f.launchLine()).To(HaveSuffix(" claude --resume --fork-session"))
		})

		It("CS-SESS-040: --branch keeps the fork flags ahead of passthrough args", func() {
			running()
			Expect(f.run("--branch", "--verbose")).To(Equal(0))
			Expect(f.launchLine()).To(HaveSuffix(" claude --resume --fork-session --verbose"))
		})

		It("CS-SESS-040: --branch with --no-session-check still forks", func() {
			running()
			Expect(f.run("--branch", "--no-session-check")).To(Equal(0))
			Expect(f.launchLine()).To(HaveSuffix(" claude --resume --fork-session"))
		})

		DescribeTable("CS-SESS-042: contradictory flags are rejected",
			func(flag string) {
				Expect(f.run("--branch", flag)).To(Equal(2))
				Expect(f.errw.String()).To(ContainSubstring("--branch"))
				Expect(f.fake.Session).To(BeNil())
			},
			Entry("--ralph", "--ralph"),
			Entry("--attach", "--attach"),
			Entry("--join", "--join"),
			Entry("--resume", "--resume"),
			Entry("--continue", "--continue"),
			Entry("--resume=ID (CS-LNCH-100)", "--resume=abc"),
		)

		It("CS-SESS-043: --branch composes with claude's own --name to name the fork", func() {
			running()
			Expect(f.run("--branch", "--name", "sidequest")).To(Equal(0))
			Expect(f.launchLine()).To(HaveSuffix(" claude --resume --fork-session --name sidequest"))
		})

		It("CS-SESS-043, CS-LNCH-002: --name alone passes through to name any new session", func() {
			running()
			Expect(f.run("--name", "something sidequest")).To(Equal(0))
			Expect(f.launched().Args).To(ContainElement("something sidequest"), "the name stays one argument")
			Expect(f.launchLine()).To(HaveSuffix(" claude --name something sidequest"))
		})

		It("CS-SESS-043: there is no --branch=NAME form — the '=' value would name the result while --attach=/--join= pick a target", func() {
			Expect(f.run("--branch=sidequest")).To(Equal(2))
			Expect(f.errw.String()).To(ContainSubstring("unknown flag"))
			Expect(f.fake.Session).To(BeNil())
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

	hash, _ := wouldBeFingerprint(f.env, f.proj, fl, cfg, envFiles, nil)
	Expect(hash).NotTo(BeEmpty())
	return hash
}

var _ = Describe("would-be fingerprint vs the launch (CS-LNCH-108)", func() {
	It("CS-LNCH-108, CS-SESS-020: a bare env-file XDG_RUNTIME_DIR the host sets to \"\" stands down in both, so the hashes agree", func() {
		f := newCLIFixture()
		// A short fixed-root home: past 103 bytes the bridge stands down for
		// length (CS-LNCH-055) and this test would pass vacuously.
		short, err := os.MkdirTemp("/tmp", "cs")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(os.RemoveAll, short)
		short, err = filepath.EvalSymlinks(short)
		Expect(err).NotTo(HaveOccurred())
		f.envmap["HOME"] = filepath.Join(short, "h")
		Expect(os.MkdirAll(filepath.Join(short, "h", ".claude"), 0o755)).To(Succeed())
		writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "sharedPeerRegistry: true\n")
		envFile := filepath.Join(f.proj, ".claude-sandbox", "env")

		launchHash := func() string {
			f.fake.Calls = nil
			f.out.Reset()
			Expect(f.run("--new")).To(Equal(0))
			for _, l := range argPairsCLI(f.launched().Args, "--label") {
				if v, ok := strings.CutPrefix(l, "claude-sandbox.confighash="); ok {
					return v
				}
			}
			Fail("no confighash label")
			return ""
		}

		// Control: bridged without the bare line, so the stand-down below is
		// caused by it and not by the socket-path length.
		writeFile(envFile, "OTHER=1\n")
		launchHash()
		Expect(f.out.String()).To(ContainSubstring("Peer registry: shared ("))

		writeFile(envFile, "XDG_RUNTIME_DIR\n")
		f.envmap["XDG_RUNTIME_DIR"] = "" // set-but-empty: Getenv alone reads it as unset
		got := launchHash()
		Expect(f.out.String()).To(ContainSubstring("an env file sets XDG_RUNTIME_DIR"))
		Expect(currentHash(f)).To(Equal(got))
	})
})

// keptPsRow is a ps row carrying every trailing field through the
// claude-sandbox.keep label (CS-SESS-070).
func keptPsRow(name, project, instance, class, state, keep string) string {
	status := map[string]string{
		"running":    "Up 1 hour",
		"exited":     "Exited (0) 3 hours ago",
		"restarting": "Restarting (1) 5 seconds ago",
	}[state]
	return strings.Join([]string{name, status, project, "claude", instance, "v1", "", "", "", class, "",
		state, "", "", "", keep}, psSep)
}

var _ = Describe("kept containers in discovery (CS-SESS-070..074)", func() {
	var f *cliFixture
	BeforeEach(func() { f = newCLIFixture() })

	exitedKept := func(g *cliFixture, instance string) string {
		return keptPsRow("cs-k", g.proj, instance, "5", sessions.StateExited, "unless-stopped")
	}

	It("CS-SESS-070, CS-SESS-074: sessions lists an exited kept container with its state; an unlabelled one stays out", func() {
		f.fake.On("docker ps", strings.Join([]string{
			keptPsRow("cs-a", f.proj, "heron", "1", "running", ""),
			exitedKept(f, "otter"),
			keptPsRow("cs-gone", f.proj, "wren", "2", sessions.StateExited, ""),
		}, "\n")+"\n", nil)
		f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
		Expect(f.run("sessions")).To(Equal(0), f.errw.String())
		out := f.out.String()
		Expect(out).To(MatchRegexp(`INSTANCE\s+WORKTREE\s+NAME\s+MODE\s+STATE\s+UP\s+SESSIONS`))
		Expect(out).To(MatchRegexp(`heron\s+-\s+cs-a\s+claude\s+running\s+1 hour\s+1`))
		Expect(out).To(MatchRegexp(`otter\s+-\s+cs-k\s+claude\s+exited\s+-\s+0`))
		Expect(out).NotTo(ContainSubstring("wren"))
		for _, l := range f.fake.CommandLines() {
			Expect(l).NotTo(HavePrefix("docker top cs-k"), "CS-SESS-071: nothing to count in a stopped container")
		}
	})

	It("CS-SESS-074: an older row with no state shows '-' in STATE", func() {
		f.fake.On("docker ps", psRow("cs-a", "Up 2 hours", f.proj, "otter")+"\n", nil)
		f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
		Expect(f.run("sessions")).To(Equal(0))
		Expect(f.out.String()).To(MatchRegexp(`otter\s+-\s+cs-a\s+claude\s+-\s+2 hours\s+1`))
	})

	It("CS-SESS-074: --json carries state and keep", func() {
		f.fake.On("docker ps", keptPsRow("cs-a", f.proj, "heron", "1", "running", "")+"\n"+exitedKept(f, "otter")+"\n", nil)
		f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
		Expect(f.run("sessions", "--json")).To(Equal(0))
		out := f.out.String()
		Expect(out).To(ContainSubstring(`"state": "running"`))
		Expect(out).To(ContainSubstring(`"state": "exited"`))
		Expect(out).To(ContainSubstring(`"keep": "unless-stopped"`))
	})

	It("CS-SESS-072: a stopped kept container's noun and pid class are never re-issued", func() {
		// Every noun but one, and every class but one, is held by an exited or
		// restarting kept container: the launch must get the remaining pair.
		var rows []string
		free := sessions.Nouns[len(sessions.Nouns)-1]
		for i, n := range sessions.Nouns[:len(sessions.Nouns)-1] {
			state := sessions.StateExited
			if i%2 == 1 {
				state = sessions.StateRestarting
			}
			rows = append(rows, keptPsRow("k-"+n, f.proj, n, strconv.Itoa(i), state, "unless-stopped"))
		}
		for k := len(sessions.Nouns) - 1; k < 255; k++ {
			rows = append(rows, keptPsRow("e-"+strconv.Itoa(k), "/elsewhere", "", strconv.Itoa(k), sessions.StateExited, "always"))
		}
		f.fake.On("docker ps", strings.Join(rows, "\n")+"\n", nil)
		f.env.Prompter = &prompt.Scripted{IsTTY: false}
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(nameOf(f.launched().Args)).To(HaveSuffix("-" + free))
		Expect(f.launched().Args).To(ContainElement("claude-sandbox.pidclass=255"))
	})

	It("CS-SESS-073: alone it never triggers the session decision, and ralph does not report it", func() {
		f.fake.On("docker ps", exitedKept(f, "otter")+"\n", nil)
		f.env.Prompter = &prompt.Scripted{IsTTY: false}
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(f.errw.String()).NotTo(ContainSubstring("running session"))
		Expect(nameOf(f.launched().Args)).NotTo(HaveSuffix("-otter"))

		g := newCLIFixture()
		g.fake.On("docker ps", exitedKept(g, "otter")+"\n", nil)
		g.env.Prompter = &prompt.Scripted{IsTTY: false}
		Expect(g.run("--ralph")).To(Equal(0), g.errw.String())
		Expect(g.errw.String()).NotTo(ContainSubstring("otter"))
	})

	It("CS-SESS-073: --attach, --join and the completion do not offer it", func() {
		f.fake.On("docker ps", exitedKept(f, "otter")+"\n", nil)
		Expect(f.run("--attach=otter")).To(Equal(2))
		Expect(f.errw.String()).To(ContainSubstring("no running sessions"))
		Expect(f.fake.Session).To(BeNil())

		g := newCLIFixture()
		g.fake.On("docker ps", keptPsRow("cs-k", g.proj, "otter", "5", sessions.StateRestarting, "unless-stopped")+"\n", nil)
		Expect(g.run("--join=otter")).To(Equal(2))
		Expect(g.fake.Session).To(BeNil())

		h := newCLIFixture()
		h.fake.On("docker ps", exitedKept(h, "otter")+"\n", nil)
		Expect(h.complete("--attach=").names).To(BeEmpty())
	})
})
