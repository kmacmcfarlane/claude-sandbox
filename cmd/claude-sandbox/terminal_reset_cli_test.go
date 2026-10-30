package main

// Spec: spec/launch.feature CS-LNCH-227/228 — the TUI's terminal modes are
// switched off on every end of an interactive session child (a detach
// included: claude keeps running and never turns them off), and turned back
// on before an attach for the ones Claude Code does not re-send on a resize.

import (
	"io"
	"strings"
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/oomreport"
)

var _ = Describe("terminal modes across a detach (CS-LNCH-227/228)", func() {
	var f *cliFixture
	BeforeEach(func() { f = newCLIFixture() })

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
	// onTerminal makes stdout (and, with both, stderr) a terminal.
	onTerminal := func(both bool) {
		f.env.IsTerminal = func(w io.Writer) bool { return w == f.env.Out || (both && w == f.env.Err) }
	}
	all := func() string { return f.out.String() + f.errw.String() }
	// neverCursorMoves: the reset leaves the alternate screen and the scroll
	// region alone.
	neverCursorMoves := func() {
		Expect(all()).NotTo(ContainSubstring("\x1b[?1049l"))
		Expect(all()).NotTo(MatchRegexp(`\x1b\[[0-9;]*r`))
	}
	existing := func() {
		f.fake.On("docker ps", psRow("cs-a", "Up 1 hour", f.proj, "otter")+"\n", nil)
		f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
	}

	It("CS-LNCH-227: a detach from a new session (start -ai exits 0, no die) resets stdout's terminal once", func() {
		onTerminal(false)
		f.fake.On("docker inspect --type container", "running 2026-09-19T00:00:00Z\n", nil)
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(strings.Count(f.out.String(), oomreport.TerminalReset)).To(Equal(1))
		Expect(f.errw.String()).NotTo(ContainSubstring("\x1b"))
		neverCursorMoves()
	})

	It("CS-LNCH-227: a session that ended on its own is reset too", func() {
		onTerminal(false)
		events(dockerEvent("die", "0"))
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(strings.Count(f.out.String(), oomreport.TerminalReset)).To(Equal(1))
	})

	It("CS-LNCH-227: with only stderr a terminal, the reset goes there", func() {
		f.env.IsTerminal = func(w io.Writer) bool { return w == f.env.Err }
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(strings.Count(f.errw.String(), oomreport.TerminalReset)).To(Equal(1))
		Expect(f.out.String()).NotTo(ContainSubstring("\x1b"))
	})

	It("CS-LNCH-227: nothing is written when neither stdout nor stderr is a terminal", func() {
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(all()).NotTo(ContainSubstring("\x1b"))
	})

	It("CS-LNCH-227: nothing is written for a headless launch, even on a terminal", func() {
		f.env.IsTerminal = func(io.Writer) bool { return true }
		events(dockerEvent("die", "0"))
		Expect(f.run("headless", "--")).To(Equal(0))
		Expect(all()).NotTo(ContainSubstring("\x1b"))
	})

	It("CS-LNCH-227: nothing is written for a reservation that never started", func() {
		onTerminal(true)
		f.fake.On("docker start", "", execx.Fail(1))
		f.fake.On("docker inspect --type container", "created 2026-09-19T00:00:00Z\n", nil)
		Expect(f.run()).To(Equal(1))
		Expect(all()).NotTo(ContainSubstring("\x1b"))
	})

	It("CS-LNCH-227: nothing is written after a hang-up: the terminal is gone", func() {
		onTerminal(true)
		f.fake.SessionSignal = syscall.SIGHUP
		f.fake.On("docker start", "", execx.Fail(129))
		f.run()
		Expect(all()).NotTo(ContainSubstring(oomreport.TerminalReset))
	})

	It("CS-LNCH-227, CS-LNCH-092: an OOM report follows the one reset, never a second copy", func() {
		onTerminal(true)
		f.fake.On("docker start", "", execx.Fail(137))
		events(dockerEvent("oom", ""), dockerEvent("die", "137"))
		Expect(f.run()).To(Equal(137))
		Expect(strings.Count(all(), oomreport.TerminalReset)).To(Equal(1))
		Expect(strings.Count(all(), "\x1b")).To(Equal(strings.Count(oomreport.TerminalReset, "\x1b")))
		Expect(f.errw.String()).To(ContainSubstring("claude-sandbox: this session was killed by the OOM killer"))
	})

	It("CS-LNCH-227, CS-LNCH-228: an attach turns the modes back on first and resets them on its detach", func() {
		existing()
		onTerminal(false)
		f.fake.On("docker attach", "", execx.Fail(1)) // docker attach exits 1 on the detach keys
		Expect(f.run("--attach=otter")).To(Equal(1))
		out := f.out.String()
		on := strings.Index(out, oomreport.AttachModes)
		off := strings.Index(out, oomreport.TerminalReset)
		Expect(on).To(BeNumerically(">=", 0), "the enables are written")
		Expect(off).To(BeNumerically(">", on), "the reset follows the attach")
		Expect(strings.Count(out, oomreport.TerminalReset)).To(Equal(1))
		Expect(out).NotTo(ContainSubstring("\x1b[?1000h"), "mouse tracking is claude's to re-send on the resize")
		neverCursorMoves()
	})

	It("CS-LNCH-228: an attach off a terminal writes no enables", func() {
		existing()
		f.fake.On("docker attach", "", execx.Fail(1))
		f.run("--attach=otter")
		Expect(all()).NotTo(ContainSubstring("\x1b"))
	})

	It("CS-LNCH-227, CS-LNCH-228: a join's end resets the terminal and writes no enables", func() {
		existing()
		onTerminal(false)
		Expect(f.run("--join=otter")).To(Equal(0), f.errw.String())
		Expect(f.sessionLine()).To(HavePrefix("docker exec -it "))
		Expect(strings.Count(f.out.String(), oomreport.TerminalReset)).To(Equal(1))
		Expect(f.out.String()).NotTo(ContainSubstring(oomreport.AttachModes))
	})
})
