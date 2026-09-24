package main

// Spec: spec/launch.feature CS-LNCH-085..094 and spec/sessions.feature
// CS-SESS-059/060 — the session child and the OOM report, end to end through
// MainWithEnv. The "docker events" subscription runs through the fake runner:
// a stub's output is the event stream, which ends where the output does, so a
// stream without die reads as a detach at once rather than after 2 s.

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/oomreport"
)

// thisContainer stands for the subscribed container's name in dockerEvent
// lines; events() substitutes it (the watcher matches names exactly,
// CS-LNCH-095).
const thisContainer = "{{CONTAINER}}"

// dockerEvent renders one "docker events --format {{json .}}" line for the
// subscribed container.
func dockerEvent(action, exit string, labels ...string) string {
	return namedEvent(thisContainer, action, exit, labels...)
}

// namedEvent renders one event line for the container called name.
func namedEvent(name, action, exit string, labels ...string) string {
	attrs := `"name":"` + name + `"`
	if exit != "" {
		attrs += `,"exitCode":"` + exit + `"`
	}
	for i := 0; i+1 < len(labels); i += 2 {
		attrs += `,"` + labels[i] + `":"` + labels[i+1] + `"`
	}
	return `{"status":"` + action + `","Type":"container","Action":"` + action +
		`","Actor":{"ID":"abc","Attributes":{` + attrs + `}}}` + "\n"
}

// shadowDirOf reads the shadow directory label from a create argv.
func shadowDirOf(args []string) string {
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "claude-sandbox.shadowdir="); ok {
			return v
		}
	}
	return ""
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

var _ = Describe("session child and OOM report (CS-LNCH-085..098)", func() {
	var f *cliFixture
	BeforeEach(func() { f = newCLIFixture() })

	// events scripts the container's event stream.
	events := func(lines ...string) {
		f.fake.OnFunc("docker events", func(c execx.Cmd) (string, error) {
			name := ""
			for _, a := range c.Args {
				if v, ok := strings.CutPrefix(a, "container="); ok {
					name = v
				}
			}
			return strings.ReplaceAll(strings.Join(lines, ""), thisContainer, name), nil
		})
	}
	// exits scripts the session child's status.
	exits := func(pattern string, code int) {
		f.fake.On(pattern, "", execx.Fail(code))
	}
	// withLimit writes a project config setting memoryLimit and returns its path.
	withLimit := func(v string) string {
		p := filepath.Join(f.proj, ".claude-sandbox", "config.yaml")
		writeFile(p, "memoryLimit: "+v+"\n")
		return p
	}

	It("CS-LNCH-085, CS-LNCH-087: a new session subscribes to its events, then runs docker start as a child", func() {
		Expect(f.run()).To(Equal(0), f.errw.String())
		name := nameOf(f.launched().Args)
		lines := f.fake.CommandLines()
		Expect(lines[len(lines)-3]).To(MatchRegexp(`^docker events --since \d+\.\d{9} --filter container=` + name +
			` --filter event=oom --filter event=die --format \{\{json \.\}\}$`))
		Expect(lines[len(lines)-2]).To(Equal("docker start -ai --detach-keys=ctrl-q,ctrl-q " + name))
		// CS-LNCH-096: then whether the start ever ran the container.
		Expect(lines[len(lines)-1]).To(HavePrefix("docker inspect --type container "))
		Expect(f.fake.Session).NotTo(BeNil())
	})

	It("CS-LNCH-085: the child's status is the launcher's, silently", func() {
		exits("docker start", 7)
		events(dockerEvent("die", "7"))
		Expect(f.run()).To(Equal(7))
		Expect(f.errw.String()).NotTo(ContainSubstring("exit 7"))
		Expect(f.errw.String()).NotTo(ContainSubstring("OOM"))
	})

	It("CS-LNCH-085: a child that cannot start is an error", func() {
		g := newCLIFixture()
		g.env.Runner = &execFailRunner{Fake: g.fake}
		Expect(g.run()).To(Equal(2))
		Expect(g.errw.String()).To(ContainSubstring("docker: not found"))
	})

	It("CS-LNCH-089, CS-LNCH-093: an OOM-killed session names the limit and the file it came from", func() {
		src := withLimit("16g")
		exits("docker start", 137)
		events(dockerEvent("oom", ""), dockerEvent("oom", ""), dockerEvent("die", "137"))
		Expect(f.run()).To(Equal(137))
		args := f.launched().Args
		Expect(args).To(ContainElements("claude-sandbox.memorylimit=16g", "claude-sandbox.memorylimitsource="+src))
		Expect(args).To(ContainElements("CLAUDE_SANDBOX_MEMORY_LIMIT=16g", "CLAUDE_SANDBOX_MEMORY_LIMIT_SOURCE="+src))
		Expect(f.errw.String()).To(HaveSuffix(
			"claude-sandbox: this session was killed by the OOM killer (exit 137; 2 OOM kills) — the container's memoryLimit or the host running out of memory.\n" +
				"  memoryLimit: 16g (from " + src + "); swap is off by design.\n" +
				"  At memoryLimit: raise memoryLimit in that file, or cap build/test parallelism (e.g. ginkgo --procs=N, go test -p N, make -jN).\n" +
				"  Host out of memory: run fewer sandboxes at once or cap their parallelism; see \"When the host runs out of memory\" in the claude-sandbox README.\n" +
				"  To tell which: the host's kernel log (journalctl -k or sudo dmesg; may need sudo) says \"Memory cgroup out of memory\" for a limit, plain \"Out of memory\" for the host.\n"))
		Expect(f.out.String()).NotTo(ContainSubstring("OOM"), "the report is on stderr")
	})

	It("CS-LNCH-089: names the default when no config sets memoryLimit", func() {
		exits("docker start", 137)
		events(dockerEvent("oom", ""), dockerEvent("die", "137"))
		Expect(f.run()).To(Equal(137))
		Expect(f.launched().Args).To(ContainElement("claude-sandbox.memorylimitsource=default"))
		Expect(f.errw.String()).To(ContainSubstring("(exit 137; 1 OOM kill) — "))
		Expect(f.errw.String()).To(ContainSubstring("memoryLimit: 8g (the default; no config.yaml in the cascade sets it); swap is off by design."))
	})

	It("CS-LNCH-089: the limit on the container's events wins over what the launch resolved", func() {
		withLimit("16g")
		exits("docker start", 137)
		events(dockerEvent("oom", "", "claude-sandbox.memorylimit", "4g", "claude-sandbox.memorylimitsource", "/x/config.yaml"),
			dockerEvent("die", "137"))
		Expect(f.run()).To(Equal(137))
		Expect(f.errw.String()).To(ContainSubstring("memoryLimit: 4g (from /x/config.yaml)"))
	})

	It("CS-LNCH-092: a report on a stderr that is not a terminal is plain lines", func() {
		exits("docker start", 137)
		events(dockerEvent("oom", ""), dockerEvent("die", "137"))
		Expect(f.run()).To(Equal(137))
		Expect(f.errw.String()).NotTo(ContainSubstring("\x1b"))
	})

	It("CS-LNCH-090: an OOM kill the session survived is one softer line", func() {
		withLimit("16g")
		events(dockerEvent("oom", ""), dockerEvent("oom", ""), dockerEvent("oom", ""), dockerEvent("die", "0"))
		Expect(f.run()).To(Equal(0))
		report := f.errw.String()[strings.Index(f.errw.String(), "claude-sandbox: "):]
		Expect(report).To(HavePrefix("claude-sandbox: note: the OOM killer killed 3 processes in this container during this session (the container's memoryLimit 16g"))
		Expect(strings.Count(report, "\n")).To(Equal(1))
		Expect(report).NotTo(ContainSubstring("\x1b"), "never a terminal reset for the soft note")
	})

	It("CS-LNCH-090: a death without oom events prints nothing", func() {
		exits("docker start", 1)
		events(dockerEvent("die", "1"))
		Expect(f.run()).To(Equal(1))
		Expect(f.errw.String()).NotTo(ContainSubstring("OOM"))
	})

	It("CS-LNCH-088, CS-LNCH-094: a detach (no die) prints nothing and keeps the shadow directory", func() {
		events(dockerEvent("oom", ""))
		Expect(f.run()).To(Equal(0))
		Expect(f.errw.String()).NotTo(ContainSubstring("OOM"))
		dir := shadowDirOf(f.launched().Args)
		Expect(dir).NotTo(BeEmpty())
		Expect(exists(dir)).To(BeTrue(), "the container may still mount it")
	})

	It("CS-LNCH-094: the shadow directory is removed once the container died", func() {
		events(dockerEvent("die", "0"))
		Expect(f.run()).To(Equal(0))
		dir := shadowDirOf(f.launched().Args)
		Expect(dir).NotTo(BeEmpty())
		Expect(exists(dir)).To(BeFalse())
	})

	It("CS-LNCH-091: a signal-initiated exit is silent, passes the status through and keeps the shadow directory", func() {
		f.fake.SessionSignal = syscall.SIGTERM
		exits("docker start", 143)
		events(dockerEvent("oom", ""), dockerEvent("die", "137"))
		Expect(f.run()).To(Equal(143))
		Expect(f.errw.String()).NotTo(ContainSubstring("OOM"))
		Expect(exists(shadowDirOf(f.launched().Args))).To(BeTrue(), "left to a later launch's sweep")
	})

	It("CS-LNCH-092: on a terminal, the reset precedes the report and nothing else is reset", func() {
		f.env.IsTerminal = func(w io.Writer) bool { return w == f.env.Err }
		exits("docker start", 137)
		events(dockerEvent("oom", ""), dockerEvent("die", "137"))
		Expect(f.run()).To(Equal(137))
		errs := f.errw.String()
		reset := strings.Index(errs, oomreport.TerminalReset)
		report := strings.Index(errs, "claude-sandbox: this session was killed")
		Expect(reset).To(BeNumerically(">=", 0), "the reset is written on a terminal")
		Expect(report).To(BeNumerically(">", reset), "before the report")
		Expect(strings.Count(errs, "\x1b")).To(Equal(strings.Count(oomreport.TerminalReset, "\x1b")))
	})

	It("CS-LNCH-092: the soft note never resets the terminal, even on one", func() {
		f.env.IsTerminal = func(io.Writer) bool { return true }
		events(dockerEvent("oom", ""), dockerEvent("die", "0"))
		Expect(f.run()).To(Equal(0))
		Expect(f.errw.String()).To(ContainSubstring("claude-sandbox: note:"))
		Expect(f.errw.String()).NotTo(ContainSubstring("\x1b"))
	})

	It("CS-LNCH-095: events of a container whose name only starts with this one's are ignored", func() {
		exits("docker start", 0)
		events(namedEvent(thisContainer+"0", "oom", ""), namedEvent(thisContainer+"0", "die", "137"), dockerEvent("die", "0"))
		Expect(f.run()).To(Equal(0))
		Expect(f.errw.String()).NotTo(ContainSubstring("OOM"))
		Expect(exists(shadowDirOf(f.launched().Args))).To(BeFalse(), "our own die removed our directory")
	})

	It("CS-LNCH-095: another container's die never counts as ours, so the shadow directory stays", func() {
		events(namedEvent(thisContainer+"0", "die", "0"))
		Expect(f.run()).To(Equal(0))
		Expect(exists(shadowDirOf(f.launched().Args))).To(BeTrue(), "our container may still run")
	})

	It("CS-LNCH-096: a start that never ran the container removes the reservation and the shadow directory at once", func() {
		exits("docker start", 1)
		f.fake.On("docker inspect --type container", "created 2026-09-19T00:00:00Z\n", nil)
		start := time.Now()
		Expect(f.run()).To(Equal(1))
		Expect(time.Since(start)).To(BeNumerically("<", oomreport.DieWait), "no die wait")
		name := nameOf(f.launched().Args)
		Expect(f.fake.CommandLines()).To(ContainElement("docker rm " + name))
		Expect(exists(shadowDirOf(f.launched().Args))).To(BeFalse())
	})

	It("CS-LNCH-096: a start that ran is not treated as never started", func() {
		f.fake.On("docker inspect --type container", "running 2026-09-19T00:00:00Z\n", nil)
		events()
		Expect(f.run()).To(Equal(0))
		Expect(f.fake.CommandLines()).NotTo(ContainElement(HavePrefix("docker rm ")))
	})

	It("CS-LNCH-097: a signal after the child returned ends the wait silently with the child's status", func() {
		f.fake.LateSignal = syscall.SIGTERM
		exits("docker start", 137)
		events(dockerEvent("oom", ""))
		start := time.Now()
		Expect(f.run()).To(Equal(137))
		Expect(time.Since(start)).To(BeNumerically("<", oomreport.DieWait))
		Expect(f.errw.String()).NotTo(ContainSubstring("OOM"))
		Expect(f.fake.Released).To(Equal(1), "handlers released once the launcher is done")
	})

	It("CS-LNCH-097: the handlers are released only after the report is written", func() {
		exits("docker start", 137)
		events(dockerEvent("oom", ""), dockerEvent("die", "137"))
		Expect(f.run()).To(Equal(137))
		Expect(f.fake.Released).To(Equal(1))
	})

	Describe("headless (CS-LNCH-060, CS-LNCH-091, CS-LNCH-092)", func() {
		It("CS-LNCH-091: SIGTERM from an SDK client ends the launch at once with docker's status", func() {
			f.fake.SessionSignal = syscall.SIGTERM
			exits("docker start", 143)
			Expect(f.run("headless", "--")).To(Equal(143))
			Expect(f.out.String()).To(BeEmpty())
			Expect(f.sessionLine()).To(HavePrefix("docker start -ai "))
		})

		It("CS-LNCH-092, CS-LNCH-060: an OOM report is plain lines on stderr, stdout stays claude's", func() {
			// Even were stderr a terminal: headless never resets it.
			f.env.IsTerminal = func(io.Writer) bool { return true }
			exits("docker start", 137)
			events(dockerEvent("oom", ""), dockerEvent("die", "137"))
			Expect(f.run("headless", "--")).To(Equal(137))
			Expect(f.out.String()).To(BeEmpty())
			Expect(f.errw.String()).To(ContainSubstring("claude-sandbox: this session was killed by the OOM killer"))
			Expect(f.errw.String()).NotTo(ContainSubstring("\x1b"))
		})

		It("CS-LNCH-085: a clean headless session exits 0 and prints nothing", func() {
			events(dockerEvent("die", "0"))
			Expect(f.run("headless", "--")).To(Equal(0))
			Expect(f.out.String()).To(BeEmpty())
			Expect(f.errw.String()).NotTo(ContainSubstring("claude-sandbox: "))
		})
	})

	Describe("attach and join (CS-SESS-059/060)", func() {
		limitLabels := []string{"claude-sandbox.memorylimit", "16g", "claude-sandbox.memorylimitsource", "/ws/.claude-sandbox/config.yaml"}
		BeforeEach(func() {
			f.fake.On("docker ps", psRow("cs-a", "Up 1 hour", f.proj, "otter")+"\n", nil)
			f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
		})

		It("CS-SESS-059: attach runs docker attach as a child and reports an OOM kill from the container's labels", func() {
			exits("docker attach", 137)
			events(dockerEvent("oom", "", limitLabels...), dockerEvent("die", "137", limitLabels...))
			Expect(f.run("--attach=otter")).To(Equal(137))
			Expect(f.fake.CommandLines()).To(ContainElement(ContainSubstring("docker events --since ")))
			Expect(f.sessionLine()).To(Equal("docker attach --detach-keys=ctrl-q,ctrl-q cs-a"))
			Expect(f.errw.String()).To(ContainSubstring("killed by the OOM killer (exit 137; 1 OOM kill) — the container's memoryLimit or the host running out of memory."))
			Expect(f.errw.String()).To(ContainSubstring("memoryLimit: 16g (from /ws/.claude-sandbox/config.yaml)"))
		})

		It("CS-SESS-059: a detach from an attach prints nothing", func() {
			events(dockerEvent("oom", "", limitLabels...))
			Expect(f.run("--attach=otter")).To(Equal(0))
			Expect(f.errw.String()).NotTo(ContainSubstring("OOM"))
		})

		It("CS-SESS-060: a joined session with exit 137 and an oom in its window is reported", func() {
			exits("docker exec", 137)
			events(dockerEvent("oom", "", limitLabels...))
			Expect(f.run("--join=otter")).To(Equal(137))
			Expect(f.sessionLine()).To(HavePrefix("docker exec -it "))
			Expect(f.errw.String()).To(ContainSubstring("this session was killed by the OOM killer (exit 137; 1 OOM kill) — "))
			Expect(f.errw.String()).To(ContainSubstring("memoryLimit: 16g (from /ws/.claude-sandbox/config.yaml)"))
		})

		It("CS-SESS-060: an oom during a join that ended otherwise is the softer line", func() {
			events(dockerEvent("oom", "", limitLabels...))
			Expect(f.run("--join=otter")).To(Equal(0))
			Expect(f.errw.String()).To(ContainSubstring("claude-sandbox: note: the OOM killer killed 1 process in this container during this session"))
		})

		It("CS-SESS-060: exit 137 without an oom event prints nothing", func() {
			exits("docker exec", 137)
			Expect(f.run("--join=otter")).To(Equal(137))
			Expect(f.errw.String()).NotTo(ContainSubstring("OOM"))
		})
	})
})
