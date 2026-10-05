package main

// Spec: spec/launch.feature CS-LNCH-178..181 and spec/sessions.feature
// CS-SESS-090..092 — the terminal identity (TERMINAL_EMULATOR) end to end:
// the create argv and label of every launch path, the join's exec argv, and
// the attach note. The restore paths are in tmux_restore_act_cli_test.go.

import (
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
)

const jediTerm = "JetBrains-JediTerm"

// psRowTerm is a discovery row carried through the terminal label and Mounts.
func psRowTerm(name, project, instance, terminal string) string {
	return strings.Join([]string{name, "Up 1 hour", project, "claude", instance, "v1", "", "", "", "3", "",
		"running", "2026-09-30 12:00:00 +0000 UTC", "", "", "",
		"", "", "", "", "", "none", terminal, ""}, psSep)
}

var _ = Describe("terminal identity (CS-LNCH-178..181, CS-SESS-090..092)", func() {
	var f *cliFixture
	BeforeEach(func() { f = newCLIFixture() })

	envFile := func() string { return filepath.Join(f.proj, ".claude-sandbox", "env") }
	createEnv := func() []string { return argPairsCLI(f.launched().Args, "-e") }
	createLabel := func() string { return labelsOf(f.launched().Args)["claude-sandbox.terminal"] }
	noTerminalEnv := func(es []string) {
		for _, e := range es {
			Expect(e).NotTo(HavePrefix("TERMINAL_EMULATOR"))
			Expect(e).NotTo(HavePrefix("TERM="))
			Expect(e).NotTo(HavePrefix("TMUX"))
		}
	}

	Describe("create (CS-LNCH-178..181)", func() {
		It("CS-LNCH-178, CS-LNCH-181: a new interactive launch passes -e TERMINAL_EMULATOR=<value> and labels it", func() {
			f.envmap["TERMINAL_EMULATOR"] = jediTerm
			Expect(f.run("--new")).To(Equal(0), f.errw.String())
			Expect(createEnv()).To(ContainElement("TERMINAL_EMULATOR=" + jediTerm))
			Expect(createLabel()).To(Equal("TERMINAL_EMULATOR=" + jediTerm))
		})

		It("CS-LNCH-178, CS-LNCH-181: unset passes nothing and labels none", func() {
			Expect(f.run("--new")).To(Equal(0), f.errw.String())
			noTerminalEnv(createEnv())
			Expect(createLabel()).To(Equal("none"))
		})

		It("CS-LNCH-178: --branch passes it too", func() {
			f.envmap["TERMINAL_EMULATOR"] = jediTerm
			f.fake.On("docker ps", "", nil)
			Expect(f.run("--branch")).To(Equal(0), f.errw.String())
			Expect(createEnv()).To(ContainElement("TERMINAL_EMULATOR=" + jediTerm))
			Expect(createLabel()).To(Equal("TERMINAL_EMULATOR=" + jediTerm))
		})

		It("CS-LNCH-178: --detach passes it too (the launcher's terminal is the best evidence)", func() {
			saved := detachedSettle
			detachedSettle = 20 * time.Millisecond
			DeferCleanup(func() { detachedSettle = saved })
			f.envmap["TERMINAL_EMULATOR"] = jediTerm
			f.fake.On("docker inspect --type container", "running 2026-09-24T00:00:00Z\n", nil)
			Expect(f.run("--detach")).To(Equal(0), f.errw.String())
			Expect(createEnv()).To(ContainElement("TERMINAL_EMULATOR=" + jediTerm))
			Expect(createLabel()).To(Equal("TERMINAL_EMULATOR=" + jediTerm))
		})

		It("CS-LNCH-179: an assigning env-file line wins: no -e, the label is the file's value", func() {
			f.envmap["TERMINAL_EMULATOR"] = "xterm.js"
			writeFile(envFile(), "TERMINAL_EMULATOR="+jediTerm+"\n")
			Expect(f.run("--new")).To(Equal(0), f.errw.String())
			noTerminalEnv(createEnv())
			Expect(createLabel()).To(Equal("TERMINAL_EMULATOR=" + jediTerm))
		})

		It("CS-LNCH-179: a bare env-file line (the old workaround) passes the launcher's value as before", func() {
			f.envmap["TERMINAL_EMULATOR"] = jediTerm
			writeFile(envFile(), "TERMINAL_EMULATOR\n")
			Expect(f.run("--new")).To(Equal(0), f.errw.String())
			Expect(createEnv()).To(ContainElement("TERMINAL_EMULATOR=" + jediTerm))
			Expect(createLabel()).To(Equal("TERMINAL_EMULATOR=" + jediTerm))
		})

		It("CS-LNCH-180: headless passes none of its own; its label is the env-file walk", func() {
			f.envmap["TERMINAL_EMULATOR"] = jediTerm
			writeFile(envFile(), "TERMINAL_EMULATOR\n")
			Expect(f.run("headless", "--", "-p", "hi")).To(Equal(0), f.errw.String())
			noTerminalEnv(createEnv())
			Expect(createLabel()).To(Equal("TERMINAL_EMULATOR=" + jediTerm))
		})

		It("CS-LNCH-180: ralph passes none; with no env-file line its label is none", func() {
			f.envmap["TERMINAL_EMULATOR"] = jediTerm
			Expect(f.run("--ralph")).To(Equal(0), f.errw.String())
			noTerminalEnv(createEnv())
			Expect(createLabel()).To(Equal("none"))
		})
	})

	Describe("join (CS-SESS-090, CS-SESS-092)", func() {
		join := func(terminal string) string {
			f.fake.On("docker ps", psRowTerm("cs-a", f.proj, "otter", terminal)+"\n", nil)
			f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
			Expect(f.run("--join=otter")).To(Equal(0), f.errw.String())
			return f.sessionLine()
		}
		const cmd = " cs-a /opt/claude-sandbox/bin/claude-sandbox pidslot -- claude"

		It("CS-SESS-090: the joiner's value is set on the exec", func() {
			f.envmap["TERMINAL_EMULATOR"] = jediTerm
			line := join("none")
			Expect(line).To(ContainSubstring(" -e TERMINAL_EMULATOR=" + jediTerm + " -w "))
			Expect(line).To(ContainSubstring(cmd))
		})

		It("CS-SESS-090: an unset joiner removes the creator's value with /usr/bin/env -u, before pidslot", func() {
			line := join("TERMINAL_EMULATOR=" + jediTerm)
			Expect(line).NotTo(ContainSubstring("-e TERMINAL_EMULATOR"))
			Expect(line).To(ContainSubstring(" cs-a /usr/bin/env -u TERMINAL_EMULATOR /opt/claude-sandbox/bin/claude-sandbox pidslot -- claude"))
		})

		It("CS-SESS-092: a winning assigning env-file line sets the file's value (the container may hold the creator's bare-line value)", func() {
			f.envmap["TERMINAL_EMULATOR"] = "xterm.js"
			writeFile(envFile(), "TERMINAL_EMULATOR="+jediTerm+"\n")
			line := join("TERMINAL_EMULATOR=xterm.js")
			Expect(line).To(ContainSubstring(" -e TERMINAL_EMULATOR=" + jediTerm + " -w "))
			Expect(line).To(ContainSubstring(cmd))
		})

		It("CS-SESS-092: an empty assignment removes it", func() {
			f.envmap["TERMINAL_EMULATOR"] = jediTerm
			writeFile(envFile(), "TERMINAL_EMULATOR=\n")
			line := join("none")
			Expect(line).NotTo(ContainSubstring("-e TERMINAL_EMULATOR"))
			Expect(line).To(ContainSubstring("/usr/bin/env -u TERMINAL_EMULATOR "))
		})

		It("CS-SESS-092: a bare env-file line acts as no line", func() {
			f.envmap["TERMINAL_EMULATOR"] = jediTerm
			writeFile(envFile(), "TERMINAL_EMULATOR\n")
			Expect(join("none")).To(ContainSubstring(" -e TERMINAL_EMULATOR=" + jediTerm + " -w "))
		})

		It("CS-SESS-092: an unreadable env file touches no name, warns once, and the join still runs", func() {
			f.envmap["TERMINAL_EMULATOR"] = jediTerm
			plantLaunchFIFO(envFile())
			f.fake.On("docker ps", psRowTerm("cs-a", f.proj, "otter", "none")+"\n", nil)
			f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
			Expect(runBounded(f, "--join=otter", "--allow-config-drift")).To(Equal(0), f.errw.String())
			line := f.sessionLine()
			Expect(line).NotTo(ContainSubstring("TERMINAL_EMULATOR"))
			Expect(line).To(ContainSubstring(cmd))
			Expect(strings.Count(f.errw.String(), "WARNING")).To(Equal(1))
			Expect(f.errw.String()).To(ContainSubstring("WARNING: cannot compute the current configuration for the drift check: reading env file:"))
			Expect(f.errw.String()).To(ContainSubstring("; the session keeps the container's terminal identity."))
		})
	})

	Describe("attach (CS-SESS-091)", func() {
		attach := func(terminal string) (errAtAttach string) {
			f.fake.On("docker ps", psRowTerm("cs-a", f.proj, "otter", terminal)+"\n", nil)
			f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
			f.fake.OnFunc("docker attach", func(execx.Cmd) (string, error) { errAtAttach = f.errw.String(); return "", nil })
			Expect(runBounded(f, "--attach=otter", "--allow-config-drift")).To(Equal(0), f.errw.String())
			Expect(f.sessionLine()).To(HavePrefix("docker attach "))
			return errAtAttach
		}
		const started = "was started"

		It("CS-SESS-091: a mismatch prints one note before docker attach, naming --join and a relaunch", func() {
			got := attach("TERMINAL_EMULATOR=" + jediTerm)
			Expect(got).To(ContainSubstring("Note: session 'otter' was started in a terminal with TERMINAL_EMULATOR=" + jediTerm + "; this terminal has TERMINAL_EMULATOR unset."))
			Expect(got).To(ContainSubstring("--join (a new claude), or exit and relaunch with --resume."))
			Expect(strings.Count(f.errw.String(), started)).To(Equal(1))
		})

		It("CS-SESS-091: none against a set variable prints the note", func() {
			f.envmap["TERMINAL_EMULATOR"] = jediTerm
			Expect(attach("none")).To(ContainSubstring("was started in a terminal with TERMINAL_EMULATOR unset; this terminal has TERMINAL_EMULATOR=" + jediTerm + "."))
		})

		It("CS-SESS-091: an equal value prints nothing", func() {
			f.envmap["TERMINAL_EMULATOR"] = jediTerm
			Expect(attach("TERMINAL_EMULATOR=" + jediTerm)).NotTo(ContainSubstring(started))
		})

		It("CS-SESS-091: an empty label prints nothing", func() {
			f.envmap["TERMINAL_EMULATOR"] = jediTerm
			Expect(attach("")).NotTo(ContainSubstring(started))
		})

		It("CS-SESS-091: a row without the label (a container from before it) prints nothing", func() {
			f.envmap["TERMINAL_EMULATOR"] = jediTerm
			f.fake.On("docker ps", psRow("cs-a", "Up 1 hour", f.proj, "otter")+"\n", nil)
			f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
			Expect(f.run("--attach=otter")).To(Equal(0), f.errw.String())
			Expect(f.errw.String()).NotTo(ContainSubstring(started))
		})

		It("CS-SESS-091: an env file assigning another value names the file instead of --join", func() {
			writeFile(envFile(), "TERMINAL_EMULATOR=xterm.js\n")
			got := attach("TERMINAL_EMULATOR=" + jediTerm)
			Expect(got).To(ContainSubstring("Note: session 'otter' was started with TERMINAL_EMULATOR=" + jediTerm + "; " + envFile() + " now gives TERMINAL_EMULATOR=xterm.js."))
			Expect(got).To(ContainSubstring("relaunch (exit, then claude-sandbox --resume) to apply it."))
			Expect(got).NotTo(ContainSubstring("--join"))
		})

		It("CS-SESS-091: an env file assigning the label's value prints nothing, whatever this terminal is", func() {
			f.envmap["TERMINAL_EMULATOR"] = "xterm.js"
			writeFile(envFile(), "TERMINAL_EMULATOR="+jediTerm+"\n")
			Expect(attach("TERMINAL_EMULATOR=" + jediTerm)).NotTo(ContainSubstring(started))
		})

		It("CS-SESS-091: an unreadable env file prints no terminal note; the attach goes on", func() {
			plantLaunchFIFO(envFile())
			Expect(attach("TERMINAL_EMULATOR=" + jediTerm)).NotTo(ContainSubstring(started))
		})
	})
})
