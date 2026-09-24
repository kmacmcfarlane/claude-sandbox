package oomreport_test

// Spec: spec/launch.feature CS-LNCH-087..093 and spec/sessions.feature
// CS-SESS-059/060 — the events subscription, the verdicts and the report
// text. The subscription runs through execx.Fake: a "docker events" stub's
// output is the event stream, which ends when the stub's output does.

import (
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/oomreport"
)

// ev renders one docker event line as "docker events --format {{json .}}"
// prints it.
func ev(action, exit string, labels ...string) string {
	attrs := `"name":"cs-x","image":"img"`
	if exit != "" {
		attrs += `,"exitCode":"` + exit + `"`
	}
	for i := 0; i+1 < len(labels); i += 2 {
		attrs += `,"` + labels[i] + `":"` + labels[i+1] + `"`
	}
	return `{"status":"` + action + `","id":"abc","from":"img","Type":"container","Action":"` + action +
		`","Actor":{"ID":"abc","Attributes":{` + attrs + `}},"scope":"local","time":1,"timeNano":1}` + "\n"
}

var limitLabels = []string{
	oomreport.LabelMemoryLimit, "16g",
	oomreport.LabelMemoryLimitSource, "/ws/.claude-sandbox/config.yaml",
}

func watch(stream string) (*oomreport.Watch, *execx.Fake) {
	fake := &execx.Fake{}
	fake.On("docker events", stream, nil)
	return oomreport.Start(fake, "cs-x", time.Unix(1700000000, 5)), fake
}

var _ = Describe("oomreport", func() {
	It("CS-LNCH-087: subscribes to the container's oom and die events from a given moment", func() {
		w, fake := watch("")
		defer w.Stop()
		Expect(fake.CommandLines()).To(ConsistOf(
			"docker events --since 1700000000.000000005 --filter container=cs-x --filter event=oom --filter event=die --format {{json .}}"))
	})

	It("CS-LNCH-087: a subscription that cannot start has seen nothing and never blocks", func() {
		w := oomreport.Start(failStart{&execx.Fake{}}, "cs-x", time.Now())
		start := time.Now()
		o, _ := w.Await(time.Minute, oomreport.Died, nil)
		Expect(time.Since(start)).To(BeNumerically("<", time.Second))
		Expect(o).To(Equal(oomreport.Outcome{}))
		Expect(oomreport.Primary(o)).To(Equal(oomreport.Quiet))
		w.Stop()
	})

	It("CS-LNCH-089, CS-LNCH-093: counts oom events and reads the die's exit code and the limit labels", func() {
		w, _ := watch(ev("oom", "", limitLabels...) + ev("oom", "", limitLabels...) + ev("die", "137", limitLabels...))
		o, _ := w.Await(time.Second, oomreport.Died, nil)
		Expect(o.Died).To(BeTrue())
		Expect(o.ExitCode).To(Equal(137))
		Expect(o.OOMKills).To(Equal(2))
		Expect(o.Limit).To(Equal(oomreport.Limit{Value: "16g", Source: "/ws/.claude-sandbox/config.yaml"}))
		Expect(oomreport.Primary(o)).To(Equal(oomreport.Killed))
	})

	It("ignores lines that are not oom or die events", func() {
		w, _ := watch("not json\n" + `{"Action":"start","Actor":{"Attributes":{}}}` + "\n" + ev("die", "0"))
		o, _ := w.Await(time.Second, oomreport.Died, nil)
		Expect(o).To(Equal(oomreport.Outcome{Died: true}))
	})

	It("CS-LNCH-088: a stream that ends without die is a detach, whatever ooms it saw", func() {
		w, _ := watch(ev("oom", ""))
		start := time.Now()
		o, _ := w.Await(time.Minute, oomreport.Died, nil)
		Expect(time.Since(start)).To(BeNumerically("<", time.Second), "the stream's end stops the wait")
		Expect(o.Died).To(BeFalse())
		Expect(oomreport.Primary(o)).To(Equal(oomreport.Quiet))
	})

	It("CS-LNCH-088: the wait for die is bounded while the stream stays open", func() {
		w := oomreport.Start(openStream{&execx.Fake{}}, "cs-x", time.Now())
		defer w.Stop()
		start := time.Now()
		o, _ := w.Await(50*time.Millisecond, oomreport.Died, nil)
		Expect(time.Since(start)).To(BeNumerically(">=", 50*time.Millisecond))
		Expect(o.Died).To(BeFalse())
	})

	It("CS-LNCH-088: a die with 137 and no oom waits only the short grace for a trailing oom", func() {
		w := oomreport.Start(&scripted{lines: []string{ev("die", "137")}}, "cs-x", time.Now())
		defer w.Stop()
		start := time.Now()
		o, stopped := w.AwaitDeath(nil)
		Expect(stopped).To(BeFalse())
		Expect(o).To(Equal(oomreport.Outcome{Died: true, ExitCode: 137}))
		el := time.Since(start)
		Expect(el).To(BeNumerically(">=", oomreport.OOMGrace))
		Expect(el).To(BeNumerically("<", oomreport.DieWait), "never the full die wait")
		Expect(oomreport.Primary(o)).To(Equal(oomreport.Quiet))
	})

	It("CS-LNCH-088: an oom that trails the die inside the grace still counts", func() {
		w := oomreport.Start(&scripted{lines: []string{ev("die", "137")}, later: []string{ev("oom", "")}, delay: 30 * time.Millisecond}, "cs-x", time.Now())
		defer w.Stop()
		o, _ := w.AwaitDeath(nil)
		Expect(o.OOMKills).To(Equal(1))
		Expect(oomreport.Primary(o)).To(Equal(oomreport.Killed))
	})

	It("CS-LNCH-095: events of a container whose name merely starts with this one's are ignored", func() {
		fake := &execx.Fake{}
		other := strings.ReplaceAll(ev("oom", ""), `"name":"cs-x"`, `"name":"cs-x0"`) +
			strings.ReplaceAll(ev("die", "137"), `"name":"cs-x"`, `"name":"cs-x0"`)
		fake.On("docker events", other+ev("die", "0"), nil)
		w := oomreport.Start(fake, "cs-x", time.Now())
		o, _ := w.Await(time.Second, oomreport.Died, nil)
		Expect(o).To(Equal(oomreport.Outcome{Died: true}))
	})

	It("CS-LNCH-098: the watcher is started to die with the launcher", func() {
		_, fake := watch("")
		Expect(fake.Calls[0].DieWithParent).To(BeTrue())
	})

	It("CS-LNCH-097: a signal on stop ends the wait at once and says so", func() {
		w := oomreport.Start(openStream{&execx.Fake{}}, "cs-x", time.Now())
		defer w.Stop()
		stop := make(chan os.Signal, 1)
		stop <- syscall.SIGTERM
		start := time.Now()
		_, stopped := w.AwaitDeath(stop)
		Expect(stopped).To(BeTrue())
		Expect(time.Since(start)).To(BeNumerically("<", 100*time.Millisecond))
	})

	It("CS-SESS-060: a joined 137 stops waiting at the container's die, then only the grace", func() {
		w := oomreport.Start(&scripted{lines: []string{ev("die", "137")}}, "cs-x", time.Now())
		defer w.Stop()
		start := time.Now()
		o, _ := w.AwaitJoined(137, nil)
		Expect(time.Since(start)).To(BeNumerically("<", oomreport.DieWait))
		Expect(oomreport.Joined(137, o)).To(Equal(oomreport.Quiet))
	})

	It("CS-SESS-060: a joined exit other than 137 waits the grace for ooms in flight", func() {
		w := oomreport.Start(&scripted{later: []string{ev("oom", "")}, delay: 30 * time.Millisecond}, "cs-x", time.Now())
		defer w.Stop()
		o, _ := w.AwaitJoined(0, nil)
		Expect(oomreport.Joined(0, o)).To(Equal(oomreport.Survived))
	})

	It("CS-LNCH-090: a die with another code after ooms is a survived kill; a die alone is quiet", func() {
		Expect(oomreport.Primary(oomreport.Outcome{Died: true, ExitCode: 0, OOMKills: 3})).To(Equal(oomreport.Survived))
		Expect(oomreport.Primary(oomreport.Outcome{Died: true, ExitCode: 1, OOMKills: 1})).To(Equal(oomreport.Survived))
		Expect(oomreport.Primary(oomreport.Outcome{Died: true, ExitCode: 137})).To(Equal(oomreport.Quiet))
		Expect(oomreport.Primary(oomreport.Outcome{Died: true, ExitCode: 0})).To(Equal(oomreport.Quiet))
	})

	It("CS-SESS-060: a joined session is judged by its own status and the ooms in its window", func() {
		Expect(oomreport.Joined(137, oomreport.Outcome{OOMKills: 1})).To(Equal(oomreport.Killed))
		Expect(oomreport.Joined(0, oomreport.Outcome{OOMKills: 2})).To(Equal(oomreport.Survived))
		Expect(oomreport.Joined(137, oomreport.Outcome{})).To(Equal(oomreport.Quiet), "killed some other way")
		Expect(oomreport.Joined(0, oomreport.Outcome{})).To(Equal(oomreport.Quiet))
		Expect(oomreport.SawOOM(oomreport.Outcome{OOMKills: 1})).To(BeTrue())
		Expect(oomreport.Never(oomreport.Outcome{OOMKills: 1})).To(BeFalse())
	})

	Describe("CS-LNCH-089: the report", func() {
		It("names the kills, both causes, the limit, its source, swap and both remedies", func() {
			Expect(oomreport.KilledReport(2, oomreport.Limit{Value: "16g", Source: "/ws/.claude-sandbox/config.yaml"})).To(Equal(
				"claude-sandbox: this session was killed by the OOM killer (exit 137; 2 OOM kills) — the container's memoryLimit or the host running out of memory.\n" +
					"  memoryLimit: 16g (from /ws/.claude-sandbox/config.yaml); swap is off by design.\n" +
					"  At memoryLimit: raise memoryLimit in that file, or cap build/test parallelism (e.g. ginkgo --procs=N, go test -p N, make -jN).\n" +
					"  Host out of memory: run fewer sandboxes at once or cap their parallelism; see \"When the host runs out of memory\" in the claude-sandbox README.\n" +
					"  To tell which: the host's kernel log (journalctl -k or sudo dmesg; may need sudo) says \"Memory cgroup out of memory\" for a limit, plain \"Out of memory\" for the host.\n"))
		})

		It("is singular for one kill and names the default", func() {
			r := oomreport.KilledReport(1, oomreport.Limit{Value: "8g", Source: oomreport.SourceDefault})
			Expect(r).To(ContainSubstring("(exit 137; 1 OOM kill) — the container's memoryLimit or the host running out of memory."))
			Expect(r).To(ContainSubstring("memoryLimit: 8g (the default; no config.yaml in the cascade sets it); swap is off by design."))
			Expect(r).To(ContainSubstring("At memoryLimit: set a higher memoryLimit in .claude-sandbox/config.yaml, or cap"))
		})

		It("says so when the container recorded no limit", func() {
			Expect(oomreport.KilledReport(1, oomreport.Limit{})).To(ContainSubstring("memoryLimit: not recorded on this container;"))
		})
	})

	It("CS-LNCH-090: the survived note is one line", func() {
		r := oomreport.SurvivedReport(3, oomreport.Limit{Value: "16g", Source: "/ws/.claude-sandbox/config.yaml"})
		Expect(r).To(Equal("claude-sandbox: note: the OOM killer killed 3 processes in this container during this session " +
			"(the container's memoryLimit 16g (from /ws/.claude-sandbox/config.yaml) or the host running out of memory); the session itself was not killed.\n"))
		Expect(oomreport.SurvivedReport(1, oomreport.Limit{Value: "8g", Source: oomreport.SourceDefault})).To(ContainSubstring("killed 1 process in this container during"))
	})

	It("CS-LNCH-092: the terminal reset undoes Claude Code's modes and never moves the cursor", func() {
		r := oomreport.TerminalReset
		for _, want := range []string{"\x1b[?2026l", "\x1b[?2004l", "\x1b[?1004l", "\x1b[?2031l",
			"\x1b[?1006l", "\x1b[?1003l", "\x1b[?1002l", "\x1b[?1000l", "\x1b[<u", "\x1b[>4m", "\x1b[0m", "\x1b[?25h"} {
			Expect(r).To(ContainSubstring(want), "%q", want)
		}
		for _, never := range []string{"?1049", "?47", "?1047", "\x1b[r", "\x1b8", "\x1bc", "\x1b[H", "\x1b[2J"} {
			Expect(r).NotTo(ContainSubstring(never), "%q", never)
		}
		Expect(strings.Count(r, "\x1b")).To(Equal(12))
	})
})

// failStart is a runner whose Start fails.
type failStart struct{ *execx.Fake }

func (failStart) Start(execx.Cmd) (execx.Process, error) { return nil, errFail }

var errFail = execx.Fail(1)

// openStream is a runner whose "docker events" stays open, publishing
// nothing, until it is signalled.
type openStream struct{ *execx.Fake }

func (openStream) Start(execx.Cmd) (execx.Process, error) {
	return &blockingProc{done: make(chan struct{})}, nil
}

type blockingProc struct{ done chan struct{} }

func (p *blockingProc) Signal(os.Signal) error { return nil }
func (p *blockingProc) Wait() error            { <-p.done; return nil }
func (p *blockingProc) Pid() int               { return 1 }

// scripted is a runner whose "docker events" publishes lines at once, then
// later after delay, and stays open until signalled.
type scripted struct {
	execx.Fake
	lines, later []string
	delay        time.Duration
}

func (r *scripted) Start(c execx.Cmd) (execx.Process, error) {
	p := &blockingProc{done: make(chan struct{})}
	go func() {
		for _, l := range r.lines {
			io.WriteString(c.Stdout, l)
		}
		time.Sleep(r.delay)
		for _, l := range r.later {
			io.WriteString(c.Stdout, l)
		}
	}()
	return p, nil
}
