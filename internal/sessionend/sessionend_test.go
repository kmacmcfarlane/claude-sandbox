package sessionend_test

// Spec: spec/launch.feature CS-LNCH-208 (the helper), CS-LNCH-209 (several
// clients), CS-LNCH-210 (its stand-downs) and CS-LNCH-212 (the notice it
// writes). Docker and systemctl run through execx.Fake; the registry, the
// leases, the end lock and /proc are scratch directories; the clock is fake
// (After advances it), so a 30-minute cap takes no time.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/clients"
	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
	"github.com/kmacmcfarlane/claude-sandbox/internal/registry"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessionend"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions/sessionstest"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

const (
	cid      = "c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00"
	cname    = "claude-sandbox-work-proj-abc123-otter"
	conv1    = "0b5e9c3a-1f2d-4e5f-8a9b-0c1d2e3f4a5b"
	conv2    = "1c6f0d4b-2a3e-4f60-9b0c-1d2e3f4a5b6c"
	inspectP = "docker inspect --type container"
	stopP    = "docker stop"
	topP     = "docker top"
)

var t0 = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// fakeClock: After advances Now by d and fires at once; hook runs after.
type fakeClock struct {
	now    time.Time
	afters int
	hook   func(n int)
}

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.now = c.now.Add(d)
	c.afters++
	if c.afters > 5000 {
		panic("the helper never finished")
	}
	if c.hook != nil {
		c.hook(c.afters)
	}
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}

type fixture struct {
	fake    *execx.Fake
	clock   *fakeClock
	state   string
	proc    string
	reg     string
	proj    string
	sigs    chan os.Signal
	opts    sessionend.Options
	log     *bytes.Buffer
	inspect []string // states answered in order; the last one repeats
	nInsp   int
	// onInspect runs before the n-th inspect (1-based) is answered.
	onInspect func(n int)
}

func newFixture() *fixture {
	base, err := filepath.EvalSymlinks(GinkgoT().TempDir())
	Expect(err).NotTo(HaveOccurred())
	f := &fixture{
		fake:  &execx.Fake{},
		clock: &fakeClock{now: t0.Add(20 * time.Second)},
		state: filepath.Join(base, "state"),
		proc:  filepath.Join(base, "proc"),
		reg:   filepath.Join(base, "peers", "sessions"),
		proj:  filepath.Join(base, "work", "proj"),
		sigs:  make(chan os.Signal, 2),
		log:   &bytes.Buffer{},
	}
	for _, d := range []string{f.reg, f.proj, filepath.Join(f.proc, "self", "ns")} {
		Expect(os.MkdirAll(d, 0o755)).To(Succeed())
	}
	Expect(os.Symlink("pid:[4026531836]", filepath.Join(f.proc, "self", "ns", "pid"))).To(Succeed())
	Expect(os.WriteFile(filepath.Join(f.proc, "stat"), []byte(fmt.Sprintf("cpu 1 2 3\nbtime %d\n", t0.Add(-time.Hour).Unix())), 0o644)).To(Succeed())
	mid := filepath.Join(base, "machine-id")
	Expect(os.WriteFile(mid, []byte("abc123\n"), 0o644)).To(Succeed())
	f.inspect = []string{"running"}
	f.fake.OnFunc(inspectP, func(c execx.Cmd) (string, error) {
		i := f.nInsp
		if f.onInspect != nil {
			f.onInspect(i + 1)
		}
		if i >= len(f.inspect) {
			i = len(f.inspect) - 1
		}
		f.nInsp++
		st := f.inspect[i]
		switch st {
		case "gone":
			if c.Stderr != nil {
				c.Stderr.Write([]byte("Error: No such container: " + cid + "\n"))
			}
			return "", execx.Fail(1)
		case "error":
			if c.Stderr != nil {
				c.Stderr.Write([]byte("Cannot connect to the Docker daemon\n"))
			}
			return "", execx.Fail(1)
		}
		return sessionstest.InspectRow("/"+cname, st, "2026-10-28T12:00:00Z", f.proj, "otter", "claude") + "\n", nil
	})
	f.opts = sessionend.Options{
		Mark: tmuxpane.Mark{V: tmuxpane.MarkVersion, State: tmuxpane.StateActive, Mode: tmuxpane.ModeClaude,
			Container: cname, ContainerID: cid, Instance: "otter", Project: f.proj, CwdRoot: f.proj,
			Class: "7", Since: t0.UnixMilli(), RegistryDir: f.reg, Worktree: ""},
		HangupAt: t0.Add(10 * time.Second), StateRoot: f.state,
		ProcRoot: f.proc, MachineIDPath: mid, Runner: f.fake, Clock: f.clock,
		Signals: f.sigs, UID: os.Getuid(), GOOS: "linux", Log: f.log,
	}
	return f
}

// record writes <pid>.json in the registry dir.
func (f *fixture) record(pid int, conv, status, procStart string, startedAt time.Time) {
	body := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":%q,"startedAt":%d,"procStart":%q,"pidDomain":"linux::pid:[4026532001]","status":%q,"name":"fix the tests","nameSource":"user"}`,
		pid, conv, f.proj, startedAt.UnixMilli(), procStart, status)
	Expect(os.WriteFile(filepath.Join(f.reg, strconv.Itoa(pid)+".json"), []byte(body), 0o600)).To(Succeed())
}

func (f *fixture) run() sessionend.Result { return sessionend.Run(f.opts) }

func (f *fixture) stops() []string {
	var out []string
	for _, l := range f.fake.CommandLines() {
		if strings.HasPrefix(l, stopP) {
			out = append(out, l)
		}
	}
	return out
}

func (f *fixture) calls(prefix string) int {
	n := 0
	for _, l := range f.fake.CommandLines() {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

func (f *fixture) entries() []sessionend.Entry {
	data, err := os.ReadFile(hostdirs.SessionEndsLog(f.state))
	if os.IsNotExist(err) {
		return nil
	}
	Expect(err).NotTo(HaveOccurred())
	var out []sessionend.Entry
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		var e sessionend.Entry
		Expect(json.Unmarshal(sc.Bytes(), &e)).To(Succeed())
		out = append(out, e)
	}
	return out
}

func (f *fixture) notices() []string {
	ents, _ := os.ReadDir(hostdirs.NoticesDir(f.state))
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

// proc writes a process into the scratch /proc: its stat starttime, and for
// a docker client its cmdline and status.
func (f *fixture) procEntry(pid int, start uint64, argv ...string) {
	dir := filepath.Join(f.proc, strconv.Itoa(pid))
	Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
	stat := fmt.Sprintf("%d (x) S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 %d 0 0", pid, start)
	Expect(os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644)).To(Succeed())
	if len(argv) > 0 {
		Expect(os.WriteFile(filepath.Join(dir, "cmdline"), []byte(strings.Join(argv, "\x00")+"\x00"), 0o644)).To(Succeed())
		uid := strconv.Itoa(os.Getuid())
		Expect(os.WriteFile(filepath.Join(dir, "status"), []byte("Name:\tdocker\nUid:\t"+uid+"\t"+uid+"\t"+uid+"\t"+uid+"\n"), 0o644)).To(Succeed())
	}
}

var _ = Describe("the session-end helper", func() {
	var f *fixture
	saved := []time.Duration{sessionend.TurnMax, sessionend.RecordGrace}
	BeforeEach(func() {
		f = newFixture()
		sessionend.TurnMax = 30 * time.Minute
		sessionend.RecordGrace = 30 * time.Second
	})
	AfterEach(func() { sessionend.TurnMax, sessionend.RecordGrace = saved[0], saved[1] })

	It("CS-LNCH-208: an idle session is stopped at once with SIGTERM explicit, logged with its resume command, and a notice written", func() {
		f.record(7, conv1, "idle", "", t0.Add(time.Second))
		res := f.run()
		Expect(res.Outcome).To(Equal(sessionend.OutcomeEnded))
		Expect(f.stops()).To(Equal([]string{"docker stop --signal SIGTERM --time 30 " + cid}))
		es := f.entries()
		Expect(es).To(HaveLen(1))
		Expect(es[0].Outcome).To(Equal("ended"))
		Expect(es[0].Reason).To(Equal("hangup"))
		Expect(es[0].ContainerID).To(Equal(cid))
		Expect(es[0].Instance).To(Equal("otter"))
		Expect(es[0].Capped).To(BeFalse())
		Expect(es[0].Sessions).To(HaveLen(1))
		Expect(es[0].Sessions[0].SessionID).To(Equal(conv1))
		Expect(es[0].Sessions[0].Kind).To(Equal("primary"))
		Expect(es[0].Resume).To(Equal([]string{"cd " + f.proj + " && claude-sandbox --new --no-worktree -- --resume " + conv1 + " --name 'fix the tests'"}))
		Expect(f.notices()).To(HaveLen(1))
		Expect(f.notices()[0]).To(MatchRegexp(`^ended-` + sessionend.ProjectKey(f.proj) + `-[0-9]+-[0-9a-f]+\.json$`))
		fi, err := os.Stat(hostdirs.SessionEndsLog(f.state))
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))
	})

	It("CS-LNCH-208: a busy turn is waited for; the stop comes once it is idle", func() {
		f.record(7, conv1, "busy", "", t0.Add(time.Second))
		f.clock.hook = func(n int) {
			if n == 5 {
				f.record(7, conv1, "idle", "", t0.Add(time.Second))
			}
		}
		res := f.run()
		Expect(res.Outcome).To(Equal(sessionend.OutcomeEnded))
		Expect(f.clock.afters).To(Equal(5))
		Expect(f.stops()).To(HaveLen(1))
	})

	It("CS-LNCH-208: waiting (a prompt nobody can answer) ends; shell, an unknown and an empty status wait", func() {
		f.record(7, conv1, "waiting", "", t0.Add(time.Second))
		Expect(f.run().Outcome).To(Equal(sessionend.OutcomeEnded))
		for _, st := range []string{"shell", "compacting", ""} {
			f = newFixture()
			f.record(7, conv1, st, "", t0.Add(time.Second))
			f.clock.hook = func(n int) {
				if n == 3 {
					f.record(7, conv1, "idle", "", t0.Add(time.Second))
				}
			}
			Expect(f.run().Outcome).To(Equal(sessionend.OutcomeEnded), st)
			Expect(f.clock.afters).To(Equal(3), st)
		}
	})

	It("CS-LNCH-208: any live busy record waits, even beside an idle one", func() {
		f.record(7, conv1, "idle", "", t0.Add(time.Second))
		f.record(263, conv2, "busy", "", t0.Add(2*time.Second))
		f.clock.hook = func(n int) {
			if n == 4 {
				f.record(263, conv2, "idle", "", t0.Add(2*time.Second))
			}
		}
		res := f.run()
		Expect(res.Outcome).To(Equal(sessionend.OutcomeEnded))
		Expect(f.clock.afters).To(Equal(4))
		Expect(res.Sessions).To(HaveLen(2))
		Expect(res.Sessions[1].Kind).To(Equal("join"))
	})

	It("CS-LNCH-208: the turn wait is capped at 30 minutes; the session is then ended anyway, logged capped", func() {
		Expect(sessionend.TurnMax).To(Equal(30 * time.Minute))
		f.record(7, conv1, "busy", "", t0.Add(time.Second))
		res := f.run()
		Expect(res.Outcome).To(Equal(sessionend.OutcomeEnded))
		Expect(res.Capped).To(BeTrue())
		Expect(res.Waited).To(Equal(30 * time.Minute))
		Expect(f.stops()).To(HaveLen(1))
		Expect(f.entries()[0].Capped).To(BeTrue())
	})

	It("CS-LNCH-208: a cap reached, then deferred by a client, then an idle turn ends uncapped", func() {
		sessionend.TurnMax = 4 * time.Second
		f.record(7, conv1, "busy", "", t0.Add(time.Second))
		var l *clients.Lease
		f.onInspect = func(n int) {
			if n == 4 { // the pre-stop re-check of the poll that reached the cap
				var err error
				l, err = clients.Take(f.state, cid, clients.KindJoin, t0, "")
				Expect(err).NotTo(HaveOccurred())
				f.record(7, conv1, "idle", "", t0.Add(time.Second))
			}
		}
		f.clock.hook = func(n int) {
			if n == 3 {
				l.Release()
			}
		}
		res := f.run()
		Expect(res.Outcome).To(Equal(sessionend.OutcomeEnded))
		Expect(res.Capped).To(BeFalse())
		Expect(f.entries()[0].Capped).To(BeFalse())
		Expect(f.stops()).To(HaveLen(1))
	})

	It("CS-LNCH-208: a cap reached whose pre-stop re-check finds the turn idle ends uncapped", func() {
		sessionend.TurnMax = 4 * time.Second
		f.record(7, conv1, "busy", "", t0.Add(time.Second))
		f.onInspect = func(n int) {
			if n == 4 { // the pre-stop re-check of the poll that reached the cap
				f.record(7, conv1, "idle", "", t0.Add(time.Second))
			}
		}
		res := f.run()
		Expect(res.Outcome).To(Equal(sessionend.OutcomeEnded))
		Expect(res.Capped).To(BeFalse())
		Expect(f.entries()[0].Capped).To(BeFalse())
		Expect(f.clock.afters).To(Equal(2))
	})

	It("CS-LNCH-208: a join's resume command names the worktree its record's cwd is in; an unknown place prints none", func() {
		m := tmuxpane.Mark{V: tmuxpane.MarkVersion, Project: "/r", CwdRoot: "/r", Worktree: ""}
		rec := func(cwd string) registry.Record {
			return registry.Record{SessionID: conv2, Cwd: cwd}
		}
		Expect(sessionend.ResumeCommand(m, rec("/r/.claude/worktrees/gen-x/sub"))).To(Equal("cd /r && claude-sandbox --new --worktree=gen-x -- --resume " + conv2))
		Expect(sessionend.ResumeCommand(m, rec("/r/sub"))).To(Equal("cd /r && claude-sandbox --new --no-worktree -- --resume " + conv2))
		Expect(sessionend.ResumeCommand(m, rec("/elsewhere"))).To(BeEmpty())
		Expect(sessionend.ResumeCommand(m, rec("/r/.claude/worktrees/a b"))).To(BeEmpty())
		Expect(sessionend.ResumeCommand(m, rec("/r/.claude/worktrees/feat+x/sub"))).To(BeEmpty())
		Expect(sessionend.ResumeCommand(m, rec("/r/.claude/worktrees/a/.claude/worktrees/b"))).To(BeEmpty())
		Expect(sessionend.ResumeCommand(m, rec("/r/.claude/worktrees/a/.claude/worktrees/b/sub"))).To(BeEmpty())
		Expect(sessionend.ResumeCommand(m, rec(""))).To(BeEmpty())

		// A primary in its own worktree; a join of it in the shared checkout.
		m = tmuxpane.Mark{V: tmuxpane.MarkVersion, Project: "/r/p", CwdRoot: "/r/.claude/worktrees/otter", Worktree: "otter"}
		Expect(sessionend.ResumeCommand(m, rec("/r/.claude/worktrees/otter"))).To(Equal("cd /r/p && claude-sandbox --new --worktree=otter -- --resume " + conv2))
		Expect(sessionend.ResumeCommand(m, rec("/r/.claude/worktrees/gen-y"))).To(Equal("cd /r/p && claude-sandbox --new --worktree=gen-y -- --resume " + conv2))
		Expect(sessionend.ResumeCommand(m, rec("/r/p"))).To(Equal("cd /r/p && claude-sandbox --new --no-worktree -- --resume " + conv2))
		Expect(sessionend.ResumeCommand(m, rec("/r/other"))).To(BeEmpty())

		// A project launched inside a worktree: its own cwd is no worktree flag.
		m = tmuxpane.Mark{V: tmuxpane.MarkVersion, Project: "/r/.claude/worktrees/x", CwdRoot: "/r/.claude/worktrees/x"}
		Expect(sessionend.ResumeCommand(m, rec("/r/.claude/worktrees/x/sub"))).To(Equal("cd /r/.claude/worktrees/x && claude-sandbox --new --no-worktree -- --resume " + conv2))
		Expect(sessionend.ResumeCommand(m, rec("/r/.claude/worktrees/x/.claude/worktrees/gen"))).To(Equal("cd /r/.claude/worktrees/x && claude-sandbox --new --worktree=gen -- --resume " + conv2))
	})

	It("CS-LNCH-208: the real state root is refused under go test", func() {
		home, err := os.UserHomeDir()
		Expect(err).NotTo(HaveOccurred())
		real := hostdirs.StateRoot(home, nil)
		Expect(func() { sessionend.AppendLog(real, sessionend.Entry{}) }).To(Panic())
		Expect(func() { sessionend.WriteNotice(real, sessionend.Notice{}) }).To(Panic())
		Expect(func() { sessionend.ClaimNotices(sessionend.ClaimOptions{StateRoot: real}) }).To(Panic())
		f.opts.StateRoot = real
		Expect(func() { f.run() }).To(Panic())
	})

	It("CS-LNCH-208: no live record keeps the container after 30 s, never stopping it", func() {
		res := f.run()
		Expect(res.Outcome).To(Equal(sessionend.OutcomeNoRecord))
		Expect(f.clock.now.Sub(t0.Add(20 * time.Second))).To(BeNumerically(">=", 30*time.Second))
		Expect(f.stops()).To(BeEmpty())
		Expect(f.entries()[0].Outcome).To(Equal("kept: no live record"))
		Expect(f.notices()).To(BeEmpty())
	})

	It("CS-LNCH-208: records of another class, from before the session, or in the helper's own pid domain are not this container's", func() {
		f.record(8, conv1, "busy", "", t0.Add(time.Second))  // class 8
		f.record(7, conv1, "busy", "", t0.Add(-time.Minute)) // before since
		body := fmt.Sprintf(`{"pid":263,"sessionId":%q,"startedAt":%d,"pidDomain":"linux:abc123:pid:[4026531836]","status":"busy"}`, conv2, t0.Add(time.Second).UnixMilli())
		Expect(os.WriteFile(filepath.Join(f.reg, "263.json"), []byte(body), 0o600)).To(Succeed())
		Expect(f.run().Outcome).To(Equal(sessionend.OutcomeNoRecord))
	})

	It("CS-LNCH-208: a registry dir that exists but cannot be opened keeps the container", func() {
		Expect(os.RemoveAll(f.reg)).To(Succeed())
		Expect(os.WriteFile(f.reg, []byte("x"), 0o600)).To(Succeed())
		Expect(f.run().Outcome).To(Equal(sessionend.OutcomeRegistry))
		Expect(f.stops()).To(BeEmpty())
	})

	It("CS-LNCH-208: a container without a registry label is read in the config dir and the legacy peers root", func() {
		home := filepath.Join(filepath.Dir(f.state), "home")
		cfgDir := filepath.Join(home, ".claude")
		legacy := filepath.Join(home, ".cache", "claude-sandbox", "peers", "sessions")
		Expect(os.MkdirAll(filepath.Join(cfgDir, "sessions"), 0o700)).To(Succeed())
		Expect(os.MkdirAll(legacy, 0o700)).To(Succeed())
		f.opts.Mark.RegistryDir = ""
		f.opts.Home, f.opts.ConfigDir = home, cfgDir
		f.reg = legacy
		f.record(7, conv1, "busy", "", t0.Add(time.Second))
		f.clock.hook = func(n int) {
			if n == 2 {
				f.record(7, conv1, "idle", "", t0.Add(time.Second))
			}
		}
		Expect(f.run().Outcome).To(Equal(sessionend.OutcomeEnded))
		Expect(f.clock.afters).To(Equal(2))
	})

	It("CS-LNCH-208: a malformed record never seen parsing waits; one that stops parsing keeps its last state", func() {
		Expect(os.WriteFile(filepath.Join(f.reg, "7.json"), []byte(`{"pid":7,`), 0o600)).To(Succeed())
		f.clock.hook = func(n int) {
			if n == 2 {
				f.record(7, conv1, "idle", "", t0.Add(time.Second))
			}
		}
		Expect(f.run().Outcome).To(Equal(sessionend.OutcomeEnded))
		Expect(f.clock.afters).To(Equal(2))

		f = newFixture()
		f.record(7, conv1, "busy", "", t0.Add(time.Second))
		f.clock.hook = func(n int) {
			switch n {
			case 1:
				Expect(os.WriteFile(filepath.Join(f.reg, "7.json"), []byte(`{"pid":7,`), 0o600)).To(Succeed())
			case 4:
				f.record(7, conv1, "idle", "", t0.Add(time.Second))
			}
		}
		Expect(f.run().Outcome).To(Equal(sessionend.OutcomeEnded))
		Expect(f.clock.afters).To(Equal(4))
	})

	It("CS-LNCH-208: a busy record whose process is dead in the container no longer holds it", func() {
		f.record(7, conv1, "idle", "100", t0.Add(time.Second))
		f.record(263, conv2, "busy", "999", t0.Add(2*time.Second))
		f.fake.On(topP, "PID\n4242\n", nil)
		f.procEntry(4242, 100)
		res := f.run()
		Expect(res.Outcome).To(Equal(sessionend.OutcomeEnded))
		Expect(f.clock.afters).To(BeZero())
		Expect(f.fake.CommandLines()).To(ContainElement("docker top " + cid + " -o pid"))
		Expect(res.Sessions).To(HaveLen(1))
		Expect(res.Sessions[0].SessionID).To(Equal(conv1))
	})

	It("CS-LNCH-208: a failed docker top keeps a busy record counting", func() {
		f.record(7, conv1, "busy", "999", t0.Add(time.Second))
		f.fake.On(topP, "", execx.Fail(1))
		f.clock.hook = func(n int) {
			if n == 3 {
				f.record(7, conv1, "idle", "999", t0.Add(time.Second))
			}
		}
		Expect(f.run().Outcome).To(Equal(sessionend.OutcomeEnded))
		Expect(f.clock.afters).To(Equal(3))
	})

	It("CS-LNCH-208: gone (No such container; exited, dead, removing) is ended on its own; paused keeps", func() {
		for _, st := range []string{"gone", "exited", "dead", "removing"} {
			f = newFixture()
			f.inspect = []string{st}
			Expect(f.run().Outcome).To(Equal(sessionend.OutcomeEndedOnItsOwn), st)
			Expect(f.stops()).To(BeEmpty())
			Expect(f.notices()).To(BeEmpty())
		}
		f = newFixture()
		f.inspect = []string{"paused"}
		Expect(f.run().Outcome).To(Equal(sessionend.OutcomePaused))
	})

	It("CS-LNCH-208: a docker error on a poll is no verdict; the next poll that reads running goes on to the stop", func() {
		f.inspect = []string{"error", "error", "running"}
		f.record(7, conv1, "idle", "", t0.Add(time.Second))
		Expect(f.run().Outcome).To(Equal(sessionend.OutcomeEnded))
		Expect(f.clock.afters).To(Equal(2))
		Expect(f.stops()).To(HaveLen(1))
	})

	It("CS-LNCH-208: the pre-stop re-check sees the container gone: ended on its own, no stop", func() {
		f.inspect = []string{"running", "gone"}
		f.record(7, conv1, "idle", "", t0.Add(time.Second))
		Expect(f.run().Outcome).To(Equal(sessionend.OutcomeEndedOnItsOwn))
		Expect(f.stops()).To(BeEmpty())
	})

	It("CS-LNCH-208: No such container from docker stop is ended on its own; another failure is retried once, then kept", func() {
		f.record(7, conv1, "idle", "", t0.Add(time.Second))
		f.fake.On(stopP, "", &execx.CodeError{Code: 1, Msg: "Error response from daemon: No such container: " + cid})
		Expect(f.run().Outcome).To(Equal(sessionend.OutcomeEndedOnItsOwn))

		f = newFixture()
		f.record(7, conv1, "idle", "", t0.Add(time.Second))
		f.fake.On(stopP, "", execx.Fail(1))
		Expect(f.run().Outcome).To(Equal(sessionend.OutcomeStopFailed))
		Expect(f.stops()).To(HaveLen(2))
	})

	It("CS-LNCH-208: its own SIGTERM or SIGHUP keeps the container, never stopping it", func() {
		for _, sig := range []os.Signal{syscall.SIGTERM, syscall.SIGHUP} {
			f = newFixture()
			f.record(7, conv1, "busy", "", t0.Add(time.Second))
			f.clock.hook = func(n int) {
				if n == 2 {
					f.sigs <- sig
				}
			}
			Expect(f.run().Outcome).To(Equal(sessionend.OutcomeSignalled))
			Expect(f.stops()).To(BeEmpty())
			Expect(f.entries()[0].Outcome).To(Equal("kept: signalled"))
		}
	})

	It("CS-LNCH-208: a user unit deactivating, inactive or failed keeps; the argv is exact", func() {
		Expect(os.WriteFile(filepath.Join(f.proc, "self", "cgroup"), []byte("0::/user.slice/user-1000.slice/user@1000.service/app.slice/tmux.service\n"), 0o644)).To(Succeed())
		for _, st := range []string{"deactivating", "inactive", "failed"} {
			fx := newFixture()
			Expect(os.WriteFile(filepath.Join(fx.proc, "self", "cgroup"), []byte("0::/user.slice/user-1000.slice/user@1000.service/app.slice/tmux.service\n"), 0o644)).To(Succeed())
			fx.fake.On("systemctl --user show", st+"\n", nil)
			fx.record(7, conv1, "idle", "", t0.Add(time.Second))
			Expect(fx.run().Outcome).To(Equal(sessionend.OutcomeUnitStopping), st)
			Expect(fx.fake.CommandLines()).To(ContainElement("systemctl --user show -p ActiveState --value tmux.service"))
			Expect(fx.stops()).To(BeEmpty())
		}
	})

	It("CS-LNCH-208: a system scope is asked without --user; active, a failure or no unit is no verdict", func() {
		Expect(os.WriteFile(filepath.Join(f.proc, "self", "cgroup"), []byte("0::/user.slice/user-1000.slice/session-3.scope\n"), 0o644)).To(Succeed())
		f.fake.On("systemctl show", "active\n", nil)
		f.record(7, conv1, "idle", "", t0.Add(time.Second))
		Expect(f.run().Outcome).To(Equal(sessionend.OutcomeEnded))
		Expect(f.fake.CommandLines()).To(ContainElement("systemctl show -p ActiveState --value session-3.scope"))

		f = newFixture()
		Expect(os.WriteFile(filepath.Join(f.proc, "self", "cgroup"), []byte("0::/user.slice/user-1000.slice/session-3.scope\n"), 0o644)).To(Succeed())
		f.fake.On("systemctl show", "", execx.Fail(1))
		f.record(7, conv1, "idle", "", t0.Add(time.Second))
		Expect(f.run().Outcome).To(Equal(sessionend.OutcomeEnded))

		f = newFixture()
		Expect(os.WriteFile(filepath.Join(f.proc, "self", "cgroup"), []byte("0::/\n"), 0o644)).To(Succeed())
		f.record(7, conv1, "idle", "", t0.Add(time.Second))
		Expect(f.run().Outcome).To(Equal(sessionend.OutcomeEnded))
		Expect(f.calls("systemctl show")).To(BeZero())
		Expect(f.calls("systemctl --user")).To(BeZero())
	})

	It("CS-LNCH-208: a host shutting down keeps", func() {
		f.fake.On("systemctl is-system-running", "stopping\n", nil)
		f.record(7, conv1, "idle", "", t0.Add(time.Second))
		Expect(f.run().Outcome).To(Equal(sessionend.OutcomeUnitStopping))
		Expect(f.stops()).To(BeEmpty())
	})

	It("CS-LNCH-208: Unit reads the v2 line's last .service or .scope", func() {
		u, user := sessionend.Unit("12:cpu:/x\n0::/user.slice/user-1000.slice/user@1000.service/app.slice/app-gnome-goland-1.scope\n")
		Expect(u).To(Equal("app-gnome-goland-1.scope"))
		Expect(user).To(BeTrue())
		u, user = sessionend.Unit("0::/system.slice/docker.service\n")
		Expect([]any{u, user}).To(Equal([]any{"docker.service", false}))
		u, _ = sessionend.Unit("0::/user.slice/user-1000.slice/user@1000.service\n")
		Expect(u).To(Equal("user@1000.service"))
		u, _ = sessionend.Unit("1:name=systemd:/x.scope\n")
		Expect(u).To(BeEmpty())
	})

	It("CS-LNCH-208: a held end lock is retried every poll; the loser takes over when the winner lets go", func() {
		dir := clients.ContainerDir(f.state, cid)
		Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
		lf, err := os.OpenFile(filepath.Join(dir, clients.EndLockName), os.O_RDWR|os.O_CREATE, 0o600)
		Expect(err).NotTo(HaveOccurred())
		Expect(syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)).To(Succeed())
		f.clock.hook = func(n int) {
			if n == 3 {
				lf.Close()
			}
		}
		f.record(7, conv1, "idle", "", t0.Add(time.Second))
		Expect(f.run().Outcome).To(Equal(sessionend.OutcomeEnded))
		Expect(f.clock.afters).To(Equal(3))
	})

	It("CS-LNCH-208: a lock loser whose container goes exits without logging", func() {
		dir := clients.ContainerDir(f.state, cid)
		Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
		lf, err := os.OpenFile(filepath.Join(dir, clients.EndLockName), os.O_RDWR|os.O_CREATE, 0o600)
		Expect(err).NotTo(HaveOccurred())
		defer lf.Close()
		Expect(syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)).To(Succeed())
		f.inspect = []string{"running", "gone"}
		Expect(f.run().Outcome).To(Equal(sessionend.OutcomeLostLock))
		Expect(f.entries()).To(BeEmpty())
	})

	It("CS-LNCH-210: in a sandbox or on a host other than Linux it keeps at once and logs nothing", func() {
		f.opts.InSandbox = true
		Expect(f.run().Outcome).To(Equal(sessionend.OutcomeStoodDown))
		f.opts.InSandbox, f.opts.GOOS = false, "darwin"
		Expect(f.run().Outcome).To(Equal(sessionend.OutcomeStoodDown))
		Expect(f.fake.CommandLines()).To(BeEmpty())
		Expect(f.entries()).To(BeEmpty())
	})

	Describe("several clients (CS-LNCH-209)", func() {
		BeforeEach(func() { f.record(7, conv1, "idle", "", t0.Add(time.Second)) })

		It("CS-LNCH-209: a terminal lease taken after the hang-up keeps the container at once", func() {
			l, err := clients.Take(f.state, cid, clients.KindAttach, f.opts.HangupAt.Add(time.Second), "")
			Expect(err).NotTo(HaveOccurred())
			defer l.Release()
			Expect(f.run().Outcome).To(Equal(sessionend.OutcomeReattached))
			Expect(f.clock.afters).To(BeZero())
			Expect(f.stops()).To(BeEmpty())
		})

		It("CS-LNCH-209: an older terminal lease keeps it only when seen on two polls 3 s apart", func() {
			l, err := clients.Take(f.state, cid, clients.KindAttach, t0, "")
			Expect(err).NotTo(HaveOccurred())
			defer l.Release()
			Expect(f.run().Outcome).To(Equal(sessionend.OutcomeReattached))
			Expect(f.clock.afters).To(Equal(2))
		})

		It("CS-LNCH-209: a sibling that hung up in the same moment releases by the next poll: the session ends", func() {
			l, err := clients.Take(f.state, cid, clients.KindPrimary, t0, "")
			Expect(err).NotTo(HaveOccurred())
			f.clock.hook = func(n int) {
				if n == 1 {
					l.Release()
				}
			}
			Expect(f.run().Outcome).To(Equal(sessionend.OutcomeEnded))
			Expect(f.clock.afters).To(Equal(1))
		})

		It("CS-LNCH-209: a join holds the container with no cap, then the session ends when it goes", func() {
			sessionend.TurnMax = 4 * time.Second
			l, err := clients.Take(f.state, cid, clients.KindJoin, t0, "")
			Expect(err).NotTo(HaveOccurred())
			f.clock.hook = func(n int) {
				if n == 10 {
					l.Release()
				}
			}
			res := f.run()
			Expect(res.Outcome).To(Equal(sessionend.OutcomeEnded))
			Expect(res.Capped).To(BeFalse())
			Expect(f.clock.afters).To(Equal(10))
		})

		It("CS-LNCH-209: a scanned docker attach started after the hang-up keeps it; one started before only defers", func() {
			// starttime in ticks since boot (btime = t0 - 1h): hang-up + 2 s.
			after := uint64((time.Hour + 12*time.Second) / time.Second * clients.ClockTicks)
			f.procEntry(5001, after, "docker", "attach", "--detach-keys=ctrl-q,ctrl-q", cid)
			Expect(f.run().Outcome).To(Equal(sessionend.OutcomeReattached))
			Expect(f.clock.afters).To(BeZero())

			f = newFixture()
			f.record(7, conv1, "idle", "", t0.Add(time.Second))
			before := uint64((time.Hour + 5*time.Second) / time.Second * clients.ClockTicks)
			f.procEntry(5002, before, "docker", "start", "-ai", cname)
			f.clock.hook = func(n int) {
				if n == 1 {
					Expect(os.RemoveAll(filepath.Join(f.proc, "5002"))).To(Succeed())
				}
			}
			Expect(f.run().Outcome).To(Equal(sessionend.OutcomeEnded))
			Expect(f.clock.afters).To(Equal(1))
		})

		It("CS-LNCH-209: presence that cannot be told keeps the container", func() {
			f.opts.ProcRoot = filepath.Join(f.proc, "missing")
			Expect(os.MkdirAll(filepath.Join(f.opts.ProcRoot, "self", "ns"), 0o755)).To(Succeed())
			Expect(os.Chmod(f.opts.ProcRoot, 0o300)).To(Succeed())
			defer os.Chmod(f.opts.ProcRoot, 0o755)
			Expect(f.run().Outcome).To(Equal(sessionend.OutcomeClientsUnknown))
			Expect(f.stops()).To(BeEmpty())
		})
	})

	It("CS-LNCH-212: the stop writes a notice the next launch in the project claims once", func() {
		f.record(7, conv1, "idle", "", t0.Add(time.Second))
		f.run()
		var out bytes.Buffer
		n := sessionend.ClaimNotices(sessionend.ClaimOptions{StateRoot: f.state, Project: f.proj, Out: &out, Now: f.clock.now})
		Expect(n).To(Equal(1))
		at := f.clock.now.Local().Format("2006-01-02 15:04")
		Expect(out.String()).To(Equal("Note: 'otter' ended when its terminal closed (after its turn, " + at + "). Resume it with:\n" +
			"  cd " + f.proj + " && claude-sandbox --new --no-worktree -- --resume " + conv1 + " --name 'fix the tests'\n" +
			"All sessions ended this way: " + hostdirs.SessionEndsLog(f.state) + "\n"))
		out.Reset()
		Expect(sessionend.ClaimNotices(sessionend.ClaimOptions{StateRoot: f.state, Project: f.proj, Out: &out, Now: f.clock.now})).To(BeZero())
		Expect(out.String()).To(BeEmpty())
		Expect(f.notices()).To(BeEmpty())
	})
})

var _ = Describe("ended-session notices (CS-LNCH-212)", func() {
	var state, proj string
	now := time.Date(2026, 10, 28, 14, 3, 0, 0, time.Local)
	BeforeEach(func() {
		base := GinkgoT().TempDir()
		state, proj = filepath.Join(base, "state"), "/home/user/work/my project"
	})
	write := func(project string, at time.Time, entries ...sessionend.NoticeEntry) {
		Expect(sessionend.WriteNotice(state, sessionend.Notice{V: 1, Project: project, At: at.UnixMilli(), Entries: entries})).To(Succeed())
	}
	claim := func() string {
		var out bytes.Buffer
		sessionend.ClaimNotices(sessionend.ClaimOptions{StateRoot: state, Project: proj, Out: &out, Now: now})
		return out.String()
	}

	It("CS-LNCH-212: notices are 0600 in a 0700 directory, named by the project key", func() {
		write(proj, now, sessionend.NoticeEntry{Instance: "heron"})
		fi, err := os.Stat(hostdirs.NoticesDir(state))
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o700)))
		ents, _ := os.ReadDir(hostdirs.NoticesDir(state))
		Expect(ents).To(HaveLen(1))
		info, _ := ents[0].Info()
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		Expect(sessionend.ProjectKey(proj)).To(MatchRegexp(`^[0-9a-f]{12}$`))
	})

	It("CS-LNCH-212: the cap wording, a session without a resume command, and at most 3 sessions plus a count", func() {
		write(proj, now, sessionend.NoticeEntry{Instance: "heron", Resume: "cd '/home/user/work/my project' && claude-sandbox --new --no-worktree -- --resume x", Capped: true})
		write(proj, now.Add(time.Second), sessionend.NoticeEntry{Instance: "otter"}, sessionend.NoticeEntry{Instance: "otter"}, sessionend.NoticeEntry{Instance: "otter"})
		out := claim()
		Expect(out).To(HavePrefix("Note: 'heron' ended when its terminal closed (it ended at the 30 min cap while busy, 2026-10-28 14:03). Resume it with:\n  cd '/home/user/work/my project' && "))
		Expect(out).To(ContainSubstring("Note: 'otter' ended when its terminal closed (after its turn, 2026-10-28 14:03).\n"))
		Expect(out).To(ContainSubstring("Note: 1 more sessions here ended the same way.\n"))
		Expect(strings.Count(out, "All sessions ended this way: ")).To(Equal(1))
	})

	It("CS-LNCH-212: another project's notice is left for it; an expired one is removed unprinted", func() {
		write("/other", now, sessionend.NoticeEntry{Instance: "wren"})
		write(proj, now.Add(-8*24*time.Hour), sessionend.NoticeEntry{Instance: "old"})
		Expect(claim()).To(BeEmpty())
		ents, _ := os.ReadDir(hostdirs.NoticesDir(state))
		Expect(ents).To(HaveLen(1))
		Expect(ents[0].Name()).To(HavePrefix("ended-" + sessionend.ProjectKey("/other")))
	})

	It("CS-LNCH-212: a dead claimant's .claimed file is removed; a live one's is left", func() {
		dir := hostdirs.NoticesDir(state)
		write(proj, now, sessionend.NoticeEntry{Instance: "heron"})
		dead := filepath.Join(dir, "ended-"+sessionend.ProjectKey(proj)+"-1-aa.json.claimed-999999")
		live := filepath.Join(dir, "ended-"+sessionend.ProjectKey(proj)+"-2-bb.json.claimed-1")
		Expect(os.WriteFile(dead, []byte("{}"), 0o600)).To(Succeed())
		Expect(os.WriteFile(live, []byte("{}"), 0o600)).To(Succeed())
		var out bytes.Buffer
		sessionend.ClaimNotices(sessionend.ClaimOptions{StateRoot: state, Project: proj, Out: &out, Now: now,
			Alive: func(pid int) bool { return pid == 1 }})
		Expect(out.String()).To(ContainSubstring("'heron'"))
		_, err := os.Stat(dead)
		Expect(os.IsNotExist(err)).To(BeTrue())
		_, err = os.Stat(live)
		Expect(err).NotTo(HaveOccurred())
	})

	It("CS-LNCH-212: a notice that is a symlink, not the user's, too large or not v1 is never printed", func() {
		dir := hostdirs.NoticesDir(state)
		Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
		key := sessionend.ProjectKey(proj)
		target := filepath.Join(filepath.Dir(state), "elsewhere.json")
		Expect(os.WriteFile(target, []byte(`{"v":1,"project":"`+proj+`","at":1,"entries":[{"instance":"x"}]}`), 0o600)).To(Succeed())
		Expect(os.Symlink(target, filepath.Join(dir, "ended-"+key+"-1-aa.json"))).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "ended-"+key+"-2-bb.json"), bytes.Repeat([]byte(" "), sessionend.NoticeMax+1), 0o600)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "ended-"+key+"-3-cc.json"), []byte(`{"v":2,"project":"`+proj+`"}`), 0o600)).To(Succeed())
		Expect(claim()).To(BeEmpty())
	})
})
