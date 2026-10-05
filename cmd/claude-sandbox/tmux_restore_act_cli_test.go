package main

// Spec: spec/tmux.feature (CS-TMUX-050 claiming, CS-TMUX-051 row 15 for a
// reservation, CS-TMUX-052..063) — "claude-sandbox tmux restore" acting in
// one pane, end to end through MainWithEnv with tmux and docker faked through
// execx.Fake: the pending mark first, the decision's effects, the attach by
// id, the resume through the normal launch path, the start lock, readiness,
// the early-end put-back and --drop. The pieces are unit-tested in
// internal/tmuxpane (startlock, notice, ready, restore).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

// lockFree reports whether nobody holds the restore start lock at path.
func lockFree(path string) bool {
	fd, err := syscall.Open(path, syscall.O_RDONLY, 0)
	if err != nil {
		return true
	}
	defer syscall.Close(fd)
	if syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return false
	}
	syscall.Flock(fd, syscall.LOCK_UN)
	return true
}

var _ = Describe("tmux restore, one pane (CS-TMUX-052..063)", func() {
	const other = "9b5e9c3a-1f2d-4e5f-8a9b-0c1d2e3f4a5b"
	var (
		f        *cliFixture
		dir      string
		lockPath string
		setenv   map[string]*string
		cfgDir   string
		dockerUp bool
		base     = time.Date(2026, 9, 29, 12, 0, 0, 0, time.Local)
	)

	BeforeEach(func() {
		f = newCLIFixture()
		f.envmap["TMUX"] = "/tmp/tmux-1000/default,4242,0"
		f.envmap["TMUX_PANE"] = "%7"
		dir = filepath.Join(f.home, ".local", "share", "tmux", "resurrect")
		Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
		f.env.ResurrectDir = dir
		f.env.Now = func() time.Time { return base.Add(time.Hour) }
		lockPath = filepath.Join(f.cache, tmuxpane.StartLockFile)
		// Never the real SIGINT handler under go test (it panics unset).
		f.env.interrupt = func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }
		// The notice is a terminal's (CS-TMUX-050).
		f.env.IsTerminal = func(io.Writer) bool { return true }
		// A relocated config dir: no gap after up (answer 50 d).
		cfgDir = filepath.Join(f.home, "work-claude")
		Expect(os.MkdirAll(filepath.Join(cfgDir, "sessions"), 0o700)).To(Succeed())

		wd, err := os.Getwd()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { os.Chdir(wd) })
		setenv = map[string]*string{}
		var mu sync.Mutex
		DeferCleanup(func(old func(string, *string)) { restoreSetenv = old }, restoreSetenv)
		restoreSetenv = func(k string, v *string) { mu.Lock(); setenv[k] = v; mu.Unlock() }
		for _, v := range []*time.Duration{&restoreDockerWait, &restoreDockerPoll, &tmuxpane.StartLockPoll,
			&tmuxpane.ReadyPoll, &tmuxpane.ResumedPoll, &tmuxpane.UpFallback, &tmuxpane.ReadyCap, &tmuxpane.EarlyEnd} {
			DeferCleanup(func(p *time.Duration, old time.Duration) { *p = old }, v, *v)
		}
		restoreDockerWait, restoreDockerPoll, tmuxpane.StartLockPoll = 2*time.Second, 10*time.Millisecond, 5*time.Millisecond
		tmuxpane.ReadyPoll, tmuxpane.ResumedPoll = 10*time.Millisecond, 10*time.Millisecond
		tmuxpane.UpFallback, tmuxpane.ReadyCap, tmuxpane.EarlyEnd = 5*time.Second, time.Minute, time.Minute
		dockerUp = true
		f.fake.OnFunc("docker version", func(execx.Cmd) (string, error) {
			if dockerUp {
				return "29.3.0\n", nil
			}
			return "", execx.Fail(1)
		})
	})

	// row is a restorable mark of this project.
	row := func(mut func(*tmuxpane.Mark)) tmuxpane.Mark {
		cde := cfgDir
		m := tmuxpane.Mark{V: 1, State: tmuxpane.StatePending, Mode: tmuxpane.ModeClaude,
			Container: "claude-sandbox-x-proj-abc123-heron", Instance: "heron", Project: f.proj,
			ConfigDir: cfgDir, ConfigDirEnv: &cde, RegistryDir: filepath.Join(cfgDir, "sessions"),
			Conversation: markConv, Name: "fix the build", NameSource: "user",
			Replay: []string{"--add-dir", "/x"}}
		if mut != nil {
			mut(&m)
		}
		return m
	}
	// pane scripts this pane (main:2.0) and its mark.
	pane := func(mark string) {
		f.fake.On("tmux display-message -p -t %7", "main\t2\t0\t4242\t1727000000\t"+mark+"\n", nil)
	}
	tmuxLines := func() []string {
		var out []string
		for _, l := range f.fake.CommandLines() {
			if strings.HasPrefix(l, "tmux ") {
				out = append(out, l)
			}
		}
		return out
	}
	// marksSet is every mark set on the pane, parsed, in order.
	marksSet := func() []tmuxpane.Mark {
		var out []tmuxpane.Mark
		for _, c := range f.fake.Calls {
			if c.Name == "tmux" && len(c.Args) == 6 && c.Args[0] == "set-option" && c.Args[4] == tmuxpane.Option {
				m, ok := tmuxpane.ParseMark(c.Args[5])
				Expect(ok).To(BeTrue())
				out = append(out, m)
			}
		}
		return out
	}
	const unset = "tmux set-option -p -u -t %7 @claude-sandbox"
	unsets := func() int {
		n := 0
		for _, l := range tmuxLines() {
			if l == unset {
				n++
			}
		}
		return n
	}
	indexOf := func(p string) int {
		for i, l := range f.fake.CommandLines() {
			if strings.HasPrefix(l, p) {
				return i
			}
		}
		return -1
	}
	inspectRunning := func(state string) {
		f.fake.OnFunc("docker inspect", func(c execx.Cmd) (string, error) {
			if strings.Contains(strings.Join(c.Args, " "), "{{.Name}}") {
				return "/claude-sandbox-x-proj-abc123-heron\x1f" + state + "\x1f2026-09-29T12:00:00Z\x1f" + f.proj + "\x1fheron\x1fclaude\n", nil
			}
			return state + "\n", nil
		})
	}
	// record writes a registry record into the row's registry dir.
	record := func(pid int, session string) {
		b, _ := json.Marshal(map[string]any{"pid": pid, "sessionId": session, "cwd": f.proj, "startedAt": time.Now().UnixMilli()})
		Expect(os.WriteFile(filepath.Join(cfgDir, "sessions", fmt.Sprintf("%d.json", pid)), b, 0o600)).To(Succeed())
	}

	Describe("CS-TMUX-052: refusals and the row", func() {
		It("CS-TMUX-052: outside a pane, a silent tmux, and bad combinations exit 2", func() {
			delete(f.envmap, "TMUX_PANE")
			Expect(f.run("tmux", "restore")).To(Equal(2))
			Expect(f.errw.String()).To(ContainSubstring("TMUX_PANE is not set"))
			Expect(f.run("tmux", "restore", "--drop")).To(Equal(2))
			f.envmap["TMUX_PANE"] = "%7"
			f.fake.On("tmux display-message", "", execx.Fail(1))
			f.errw.Reset()
			Expect(f.run("tmux", "restore")).To(Equal(2))
			Expect(f.errw.String()).To(ContainSubstring("tmux did not answer for pane %7"))
		})

		It("CS-TMUX-052: no row at the coordinates leaves the pane alone; a bad row is cleared, naming the field", func() {
			pane("")
			stamp := base.Format("20060102T150405")
			Expect(os.WriteFile(filepath.Join(dir, tmuxpane.StateFileName(stamp)), []byte("pane\tmain\t1\t1\t:*\t0\tt\t:/p\t1\tzsh\t:\n"), 0o600)).To(Succeed())
			Expect(tmuxpane.WriteSidecar(filepath.Join(dir, tmuxpane.SidecarName(stamp)), tmuxpane.Sidecar{V: 1, StateFile: tmuxpane.StateFileName(stamp),
				Panes: []tmuxpane.Row{{Session: "main", Window: 3, Pane: 0, Mark: row(nil)}}})).To(Succeed())
			Expect(os.Symlink(tmuxpane.StateFileName(stamp), filepath.Join(dir, "last"))).To(Succeed())
			Expect(f.run("tmux", "restore")).To(Equal(0), f.errw.String())
			Expect(f.out.String()).To(ContainSubstring("nothing recorded for this pane (main:2.0) — the shell is yours"))
			Expect(tmuxLines()).To(HaveLen(1), "only the display-message")

			// --from that save, with a row at these coordinates that fails the checks.
			Expect(tmuxpane.WriteSidecar(filepath.Join(dir, tmuxpane.SidecarName(stamp)), tmuxpane.Sidecar{V: 1, StateFile: tmuxpane.StateFileName(stamp),
				Panes: []tmuxpane.Row{{Session: "main", Window: 2, Pane: 0, Mark: row(func(m *tmuxpane.Mark) { m.Project = "rel" })}}})).To(Succeed())
			f.out.Reset()
			Expect(f.run("tmux", "restore", "--from", stamp)).To(Equal(0), f.errw.String())
			Expect(f.out.String()).To(ContainSubstring("its project is not valid"))
			Expect(unsets()).To(Equal(1))
			Expect(marksSet()).To(BeEmpty())
		})

		It("CS-TMUX-052: --from reads that save's row even over the pane's pending mark", func() {
			pane(row(func(m *tmuxpane.Mark) { m.Conversation = other }).JSON())
			stamp := base.Format("20060102T150405")
			Expect(os.WriteFile(filepath.Join(dir, tmuxpane.StateFileName(stamp)), []byte("pane\tmain\t1\t1\t:*\t0\tt\t:/p\t1\tzsh\t:\n"), 0o600)).To(Succeed())
			Expect(tmuxpane.WriteSidecar(filepath.Join(dir, tmuxpane.SidecarName(stamp)), tmuxpane.Sidecar{V: 1, StateFile: tmuxpane.StateFileName(stamp),
				Panes: []tmuxpane.Row{{Session: "main", Window: 2, Pane: 0, Mark: row(func(m *tmuxpane.Mark) { m.Mode = tmuxpane.ModeRalph; m.State = tmuxpane.StateActive })}}})).To(Succeed())
			Expect(f.run("tmux", "restore", "--from", stamp)).To(Equal(0), f.errw.String())
			Expect(f.out.String()).To(ContainSubstring("a ralph run was here"))
			ms := marksSet()
			Expect(ms).To(HaveLen(1))
			Expect(ms[0].Mode).To(Equal(tmuxpane.ModeRalph), "the save's row, not the pane's")
			Expect(ms[0].State).To(Equal(tmuxpane.StatePending))
		})
	})

	It("CS-TMUX-053, CS-TMUX-054: the pane is pending before any wait; a final outcome then clears it", func() {
		pane(row(func(m *tmuxpane.Mark) { m.Mode = tmuxpane.ModeJoin }).JSON())
		Expect(f.run("tmux", "restore")).To(Equal(0), f.errw.String())
		Expect(f.out.String()).To(ContainSubstring("claude-sandbox: a joined session was here (fix the build); joins are not restored"))
		Expect(f.out.String()).To(ContainSubstring("claude-sandbox: by hand: cd " + f.proj))
		Expect(marksSet()).To(HaveLen(1))
		Expect(indexOf("tmux set-option -p -t %7")).To(BeNumerically("<", indexOf(unset)))
		Expect(unsets()).To(Equal(1))
		Expect(f.fake.CommandLines()).NotTo(ContainElement(HavePrefix("docker create")))
	})

	It("CS-TMUX-053, CS-TMUX-055: docker that never answers is waited for, then stays pending with the retry and manual commands", func() {
		pane(row(nil).JSON())
		restoreDockerWait = 60 * time.Millisecond
		dockerUp = false
		Expect(f.run("tmux", "restore")).To(Equal(0), f.errw.String())
		Expect(f.errw.String()).To(ContainSubstring("waiting for docker… (Ctrl-C to skip)"))
		Expect(strings.Count(f.errw.String(), "waiting for docker")).To(Equal(1))
		out := f.out.String()
		Expect(out).To(ContainSubstring("docker does not answer"))
		Expect(out).To(ContainSubstring("The pane stays pending. Retry: claude-sandbox tmux restore"))
		Expect(out).To(ContainSubstring("By hand:  cd " + f.proj + " && CLAUDE_CONFIG_DIR=" + cfgDir + " claude-sandbox --new --no-worktree -- --resume " + markConv))
		Expect(out).To(ContainSubstring("claude-sandbox tmux restore --drop"))
		Expect(unsets()).To(BeZero())
		Expect(marksSet()).To(HaveLen(1))
		Expect(marksSet()[0].State).To(Equal(tmuxpane.StatePending))
		n := 0
		for _, l := range f.fake.CommandLines() {
			if strings.HasPrefix(l, "docker version") {
				n++
			}
		}
		Expect(n).To(BeNumerically(">", 1), "polled")
	})

	Describe("attach (CS-TMUX-056, CS-TMUX-057)", func() {
		withID := func() tmuxpane.Mark { return row(func(m *tmuxpane.Mark) { m.ContainerID = markID }) }

		It("CS-TMUX-056, CS-TMUX-060: attaches by the 64-hex id, the events by name, after releasing the lock once the pane is active", func() {
			pane(withID().JSON())
			inspectRunning("running")
			f.fake.On("docker ps", psRowMark("claude-sandbox-x-proj-abc123-heron", f.proj, "claude", "heron", "", "37", "",
				"2026-09-18 12:34:56 +0000 UTC", markID, cfgDir, cfgDir+"/sessions", "--add-dir")+"\n", nil)
			streamEvents(f.fake, dockerEvent("die", "0"))
			freeAtAttach := false
			f.fake.OnFunc("docker attach", func(execx.Cmd) (string, error) { freeAtAttach = lockFree(lockPath); return "", nil })
			Expect(f.run("tmux", "restore")).To(Equal(0), f.errw.String())
			Expect(f.sessionLine()).To(Equal("docker attach --detach-keys=ctrl-q,ctrl-q " + markID))
			Expect(freeAtAttach).To(BeTrue(), "released from onChild")
			Expect(f.fake.CommandLines()).To(ContainElement(ContainSubstring("container=claude-sandbox-x-proj-abc123-heron")))
			ms := marksSet()
			Expect(ms).To(HaveLen(2))
			Expect(ms[0].State).To(Equal(tmuxpane.StatePending))
			Expect(ms[1].State).To(Equal(tmuxpane.StateActive))
			Expect(ms[1].ContainerID).To(Equal(markID))
			Expect(indexOf("tmux set-option -p -t %7")).To(BeNumerically("<", indexOf("docker attach")))
			Expect(f.out.String()).To(ContainSubstring("'fix the build' still runs in claude-sandbox-x-proj-abc123-heron; attach to it"))
			Expect(f.fake.CommandLines()).NotTo(ContainElement(HavePrefix("docker create")))
		})

		It("CS-LNCH-176, CS-TMUX-056: a hung git (restoreCascade's linked-worktree probe, the mark's GitRoot) is bounded and silent; the attach goes on", func() {
			f.fake.GitBound = 50 * time.Millisecond
			f.fake.OnHang("git ")
			pane(withID().JSON())
			inspectRunning("running")
			f.fake.On("docker ps", psRowMark("claude-sandbox-x-proj-abc123-heron", f.proj, "claude", "heron", "", "37", "",
				"2026-09-18 12:34:56 +0000 UTC", markID, cfgDir, cfgDir+"/sessions", "--add-dir")+"\n", nil)
			streamEvents(f.fake, dockerEvent("die", "0"))
			done := make(chan int, 1)
			go func() { defer GinkgoRecover(); done <- f.run("tmux", "restore") }()
			var code int
			Eventually(done, 10*time.Second).Should(Receive(&code))
			Expect(code).To(Equal(0), f.errw.String())
			Expect(f.sessionLine()).To(Equal("docker attach --detach-keys=ctrl-q,ctrl-q " + markID))
			Expect(f.fake.CommandLines()).To(ContainElement(ContainSubstring("rev-parse --git-dir --git-common-dir --show-toplevel")))
			Expect(f.fake.CommandLines()).To(ContainElement(ContainSubstring("rev-parse --show-toplevel")))
			Expect(f.fake.Killed).To(BeNumerically(">=", 2))
			Expect(f.errw.String()).NotTo(ContainSubstring("did not finish within"), "both timeouts are silent here")
			ms := marksSet()
			Expect(ms[len(ms)-1].State).To(Equal(tmuxpane.StateActive))
		})

		It("CS-TMUX-056: a hand attach to a container discovered with its full id attaches by the id", func() {
			delete(f.envmap, "TMUX")
			f.fake.On("docker ps", psRowMark("cs-otter", f.proj, "claude", "otter", "", "37", "",
				"2026-09-18 12:34:56 +0000 UTC", markID, "", "/x/sessions", "")+"\n", nil)
			f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
			Expect(f.run("--attach=otter")).To(Equal(0), f.errw.String())
			Expect(f.sessionLine()).To(Equal("docker attach --detach-keys=ctrl-q,ctrl-q " + markID))
		})

		Describe("CS-SESS-091: the terminal note before a restore attach", func() {
			// termRow is psRowMark carried through the terminal label and Mounts.
			termRow := func(terminal string) string {
				return psRowMark("claude-sandbox-x-proj-abc123-heron", f.proj, "claude", "heron", "", "37", "",
					"2026-09-18 12:34:56 +0000 UTC", markID, cfgDir, cfgDir+"/sessions", "") +
					psSep + "" + psSep + "none" + psSep + terminal + psSep + ""
			}
			attachWith := func(terminal string) (errAtAttach string) {
				pane(withID().JSON())
				inspectRunning("running")
				f.fake.On("docker ps", termRow(terminal)+"\n", nil)
				streamEvents(f.fake, dockerEvent("die", "0"))
				f.fake.OnFunc("docker attach", func(execx.Cmd) (string, error) { errAtAttach = f.errw.String(); return "", nil })
				Expect(f.run("tmux", "restore")).To(Equal(0), f.errw.String())
				Expect(f.sessionLine()).To(Equal("docker attach --detach-keys=ctrl-q,ctrl-q " + markID))
				return errAtAttach
			}

			It("CS-SESS-091: a mismatch prints the note before docker attach", func() {
				got := attachWith("TERMINAL_EMULATOR=JetBrains-JediTerm")
				Expect(got).To(ContainSubstring("Note: session 'heron' was started in a terminal with TERMINAL_EMULATOR=JetBrains-JediTerm; this terminal has TERMINAL_EMULATOR unset."))
				Expect(got).To(ContainSubstring("--join"))
			})

			It("CS-SESS-091: an equal identity prints nothing", func() {
				f.envmap["TERMINAL_EMULATOR"] = "JetBrains-JediTerm"
				Expect(attachWith("TERMINAL_EMULATOR=JetBrains-JediTerm")).NotTo(ContainSubstring("was started in a terminal"))
			})

			It("CS-SESS-091: an unreadable env file prints no note, and the attach goes on", func() {
				plantLaunchFIFO(filepath.Join(f.proj, ".claude-sandbox", "env"))
				Expect(attachWith("TERMINAL_EMULATOR=JetBrains-JediTerm")).NotTo(ContainSubstring("was started"))
			})

			It("CS-SESS-091: the session built when discovery misses the id (named by the restore's Inspect probe) carries no label: no note", func() {
				pane(withID().JSON())
				inspectRunning("running")
				f.fake.On("docker ps", "", nil)
				streamEvents(f.fake, dockerEvent("die", "0"))
				Expect(f.run("tmux", "restore")).To(Equal(0), f.errw.String())
				Expect(f.sessionLine()).To(Equal("docker attach --detach-keys=ctrl-q,ctrl-q " + markID))
				Expect(f.errw.String()).NotTo(ContainSubstring("was started in a terminal"))
			})
		})

		It("CS-TMUX-056: paused stays pending", func() {
			pane(withID().JSON())
			inspectRunning("paused")
			Expect(f.run("tmux", "restore")).To(Equal(0), f.errw.String())
			Expect(f.out.String()).To(ContainSubstring("'fix the build' is paused: docker unpause claude-sandbox-x-proj-abc123-heron, then claude-sandbox tmux restore"))
			Expect(unsets()).To(BeZero())
			Expect(f.fake.Session).To(BeNil())
			Expect(lockFree(lockPath)).To(BeTrue())
		})

		It("CS-TMUX-057: on screen in another pane by containerId clears the mark, no attach", func() {
			pane(withID().JSON())
			inspectRunning("running")
			active := withID()
			active.State = tmuxpane.StateActive
			f.fake.On("tmux list-panes", "%9\twork\t4\t0\tclaude-sandbox\t"+active.JSON()+"\n", nil)
			Expect(f.run("tmux", "restore")).To(Equal(0), f.errw.String())
			Expect(f.out.String()).To(ContainSubstring("'fix the build' is already on screen in work:4.0"))
			Expect(unsets()).To(Equal(1))
			Expect(f.fake.Session).To(BeNil())
		})
	})

	Describe("resume (CS-TMUX-058..062)", func() {
		// pidClass is the class the restore's create got.
		var (
			mu     sync.Mutex
			create []string
		)
		BeforeEach(func() {
			create = nil
			f.fake.OnFunc("docker create", func(c execx.Cmd) (string, error) {
				mu.Lock()
				create = c.Args
				mu.Unlock()
				return markID + "\n", nil
			})
		})
		classOf := func() int {
			mu.Lock()
			defer mu.Unlock()
			var n int
			fmt.Sscan(labelsOf(create)["claude-sandbox.pidclass"], &n)
			return n
		}
		streamEvents2 := func() { streamEvents(f.fake, dockerEvent("die", "0")) }

		It("CS-TMUX-058, CS-TMUX-059, CS-TMUX-061: resumes with the row's identity and replay, its config dir, PROJECT_DIR cleared, and releases the lock once up", func() {
			pane(row(func(m *tmuxpane.Mark) { m.Unreplayed = []string{"--docker-socket"}; m.Model = "opus" }).JSON())
			f.envmap["PROJECT_DIR"] = f.repo // the restoring shell exports another project
			f.envmap["CLAUDE_CONFIG_DIR"] = "/elsewhere"
			streamEvents2()
			var freeDuring, heldBefore bool
			f.fake.OnFunc("docker start -ai", func(execx.Cmd) (string, error) {
				heldBefore = !lockFree(lockPath)
				record(classOf()+256, markConv)
				deadline := time.Now().Add(2 * time.Second)
				for time.Now().Before(deadline) && !lockFree(lockPath) {
					time.Sleep(5 * time.Millisecond)
				}
				freeDuring = lockFree(lockPath)
				time.Sleep(50 * time.Millisecond)
				return "", nil
			})
			Expect(f.run("tmux", "restore")).To(Equal(0), f.errw.String())
			out := f.out.String()
			Expect(out).To(ContainSubstring("claude-sandbox: note: restored without flags given at launch: --docker-socket — relaunch by hand to use them"))
			Expect(strings.Index(out, "note: restored without")).To(BeNumerically("<", strings.Index(out, "claude-sandbox: resume 'fix the build'")))
			c := f.launched()
			line := strings.Join(c.Args, " ")
			Expect(line).To(ContainSubstring("-e CLAUDE_CONFIG_DIR=" + cfgDir))
			Expect(line).NotTo(ContainSubstring("/elsewhere"))
			Expect(argPairsCLI(c.Args, "-w")).To(Equal([]string{f.proj}))
			Expect(labelsOf(c.Args)["claude-sandbox.project"]).To(Equal(f.proj))
			Expect(labelsOf(c.Args)["claude-sandbox.resume"]).To(Equal(markConv))
			Expect(line).To(MatchRegexp(`--model opus .*--resume ` + markConv + ` --name fix the build --add-dir /x$`))
			Expect(setenv).To(HaveKeyWithValue("PROJECT_DIR", BeNil()))
			Expect(setenv).To(HaveKey("CLAUDE_CONFIG_DIR"))
			Expect(*setenv["CLAUDE_CONFIG_DIR"]).To(Equal(cfgDir))
			Expect(heldBefore).To(BeTrue(), "held across the launch until up")
			Expect(freeDuring).To(BeTrue(), "released at up (no gap on a relocated config dir)")
			Expect(f.errw.String()).NotTo(ContainSubstring("waiting to restore"), "no CS-TMUX-017 note for the restore's own prior")
			ms := marksSet()
			Expect(ms).To(HaveLen(2))
			Expect(ms[0].State).To(Equal(tmuxpane.StatePending))
			Expect(ms[1].State).To(Equal(tmuxpane.StateActive))
			Expect(tmuxLines()[len(tmuxLines())-1]).To(Equal(unset), "resumed, then a clean exit: CS-TMUX-071")
			Expect(lockFree(lockPath)).To(BeTrue())
		})

		It("CS-TMUX-058: a row recorded with no CLAUDE_CONFIG_DIR unsets it; one from before the record takes the shell's", func() {
			pane(row(func(m *tmuxpane.Mark) { e := ""; m.ConfigDirEnv = &e; m.ConfigDir = "" }).JSON())
			f.envmap["CLAUDE_CONFIG_DIR"] = cfgDir
			streamEvents2()
			f.fake.On("docker start -ai", "", nil)
			f.run("tmux", "restore")
			Expect(strings.Join(f.launched().Args, " ")).NotTo(ContainSubstring("CLAUDE_CONFIG_DIR="))
			Expect(setenv).To(HaveKeyWithValue("CLAUDE_CONFIG_DIR", BeNil()))

			g := newCLIFixture()
			g.envmap["TMUX"], g.envmap["TMUX_PANE"] = "x", "%7"
			g.envmap["CLAUDE_CONFIG_DIR"] = cfgDir
			g.env.ResurrectDir = dir
			g.env.interrupt = f.env.interrupt
			g.fake.On("tmux display-message -p -t %7", "main\t2\t0\t1\t2\t"+row(func(m *tmuxpane.Mark) { m.ConfigDirEnv = nil }).JSON()+"\n", nil)
			g.fake.On("docker version", "29.3.0\n", nil)
			streamEvents(g.fake, dockerEvent("die", "0"))
			setenv = map[string]*string{}
			g.run("tmux", "restore")
			Expect(g.out.String()).To(ContainSubstring("note: its CLAUDE_CONFIG_DIR was not recorded"))
			Expect(strings.Join(g.launched().Args, " ")).To(ContainSubstring("-e CLAUDE_CONFIG_DIR=" + cfgDir))
			Expect(setenv).NotTo(HaveKey("CLAUDE_CONFIG_DIR"), "left as the shell has it")
		})

		It("CS-LNCH-178: the resume's create takes the terminal identity from the pane's environment, never from the save", func() {
			pane(row(nil).JSON())
			f.envmap["TERMINAL_EMULATOR"] = "JetBrains-JediTerm"
			streamEvents2()
			f.fake.On("docker start -ai", "", nil)
			f.run("tmux", "restore")
			args := f.launched().Args
			Expect(argPairsCLI(args, "-e")).To(ContainElement("TERMINAL_EMULATOR=JetBrains-JediTerm"))
			Expect(labelsOf(args)["claude-sandbox.terminal"]).To(Equal("TERMINAL_EMULATOR=JetBrains-JediTerm"))
		})

		It("CS-TMUX-062: a resume that ends before it was resumed puts the pending row back and says so", func() {
			pend := row(nil)
			pane(pend.JSON())
			streamEvents(f.fake, dockerEvent("die", "1"))
			f.fake.On("docker start -ai", "", execx.Fail(1))
			Expect(f.run("tmux", "restore")).To(Equal(1))
			ms := marksSet()
			Expect(ms).To(HaveLen(3))
			Expect(ms[1].State).To(Equal(tmuxpane.StateActive))
			Expect(ms[2]).To(Equal(ms[0]), "the pending row went back")
			Expect(ms[2].Conversation).To(Equal(markConv))
			Expect(unsets()).To(BeZero())
			Expect(f.out.String()).To(ContainSubstring("the resume of 'fix the build' (" + markConv + ") ended before it was up (exit 1) — the conversation may be missing from " + cfgDir))
			Expect(lockFree(lockPath)).To(BeTrue())
		})

		It("CS-TMUX-062: an end past EarlyEnd follows CS-TMUX-071 (a clean exit unsets)", func() {
			tmuxpane.EarlyEnd = 20 * time.Millisecond
			pane(row(nil).JSON())
			streamEvents2()
			f.fake.OnFunc("docker start -ai", func(execx.Cmd) (string, error) { time.Sleep(60 * time.Millisecond); return "", nil })
			Expect(f.run("tmux", "restore")).To(Equal(0), f.errw.String())
			Expect(tmuxLines()[len(tmuxLines())-1]).To(Equal(unset))
			Expect(f.out.String()).NotTo(ContainSubstring("ended before it was up"))
		})

		It("CS-TMUX-058: a reservation that never started puts the row back with the retry and manual commands", func() {
			pane(row(nil).JSON())
			f.fake.On("docker inspect", "created 2026-09-29T12:00:00Z\n", nil)
			f.fake.On("docker start -ai", "", execx.Fail(1))
			Expect(f.run("tmux", "restore")).To(Equal(1))
			ms := marksSet()
			Expect(ms[len(ms)-1]).To(Equal(ms[0]), "CS-TMUX-018: the pending row went back")
			Expect(unsets()).To(BeZero())
			out := f.out.String()
			Expect(out).To(ContainSubstring("did not start (see above)"))
			Expect(out).To(ContainSubstring("The pane stays pending. Retry: claude-sandbox tmux restore"))
			Expect(out).To(ContainSubstring("By hand:  cd " + f.proj))
			Expect(out).NotTo(ContainSubstring("ended before it was up"))
		})

		It("CS-TMUX-061: the ReadyCap line is printed after the session, never into it", func() {
			tmuxpane.UpFallback, tmuxpane.ReadyCap = time.Hour, 20*time.Millisecond
			pane(row(nil).JSON())
			streamEvents2()
			var during string
			f.fake.OnFunc("docker start -ai", func(execx.Cmd) (string, error) {
				time.Sleep(80 * time.Millisecond)
				during = f.errw.String() + f.out.String()
				return "", nil
			})
			f.run("tmux", "restore")
			Expect(during).NotTo(ContainSubstring("was not up after"))
			Expect(f.out.String()).To(ContainSubstring("'fix the build' was not up after 0 s; the next restore was let start then"))
		})

		It("CS-TMUX-058: the launch's resume guard refusing (exit 4) keeps the row pending", func() {
			pane(row(nil).JSON())
			n := 0
			holder := psRowResume("claude-sandbox-x-proj-abc123-murre", f.proj, "claude", "murre", "12", "running", time.Now(), cfgDir+"/sessions", markConv)
			f.fake.OnFunc("docker ps", func(execx.Cmd) (string, error) {
				n++
				if n == 1 {
					return "", nil // the restore's own check: nobody holds it
				}
				return holder + "\n", nil // a hand launch raced the restore
			})
			Expect(f.run("tmux", "restore")).To(Equal(4))
			Expect(f.errw.String()).To(ContainSubstring("murre"))
			out := f.out.String()
			Expect(out).To(ContainSubstring("did not start (see above)"))
			Expect(out).To(ContainSubstring("The pane stays pending."))
			Expect(f.fake.CommandLines()).NotTo(ContainElement(HavePrefix("docker create")))
			Expect(unsets()).To(BeZero())
			Expect(marksSet()).To(HaveLen(1))
			Expect(lockFree(lockPath)).To(BeTrue())
		})

		It("CS-TMUX-060: a held start lock is waited on, naming its holder; Ctrl-C skips it and leaves the row pending", func() {
			pane(row(nil).JSON())
			l, err := tmuxpane.AcquireStartLock(context.Background(), lockPath, "main:9.0 (murre), pid 1", nil)
			Expect(err).NotTo(HaveOccurred())
			defer l.Release()
			f.env.interrupt = func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				time.AfterFunc(100*time.Millisecond, cancel)
				return ctx, cancel
			}
			Expect(f.run("tmux", "restore")).To(Equal(130))
			Expect(f.errw.String()).To(ContainSubstring("waiting for main:9.0 (murre), pid 1 to start (Ctrl-C to skip)…"))
			Expect(f.out.String()).To(ContainSubstring("skipped; pane main:2.0 stays pending"))
			Expect(unsets()).To(BeZero())
			Expect(f.fake.CommandLines()).NotTo(ContainElement(HavePrefix("docker ps")), "nothing checked without the lock")
			Expect(tmuxpane.ReadStartLockHolder(lockPath)).To(Equal("main:9.0 (murre), pid 1"))
		})
	})

	It("CS-TMUX-051, CS-TMUX-055: a reservation holding the conversation keeps the row pending (the F4a review's note)", func() {
		pane(row(nil).JSON())
		f.fake.On("docker ps", psRowResume("claude-sandbox-x-proj-abc123-murre", f.proj, "claude", "murre", "12", "created",
			time.Now(), cfgDir+"/sessions", markConv)+"\n", nil)
		Expect(f.run("tmux", "restore")).To(Equal(0), f.errw.String())
		Expect(f.out.String()).To(ContainSubstring("'fix the build' is being started in 'murre' (claude-sandbox-x-proj-abc123-murre) (created, not started yet)"))
		Expect(f.out.String()).To(ContainSubstring("The pane stays pending."))
		Expect(unsets()).To(BeZero())
		Expect(f.fake.CommandLines()).NotTo(ContainElement(HavePrefix("docker create")))
	})

	It("CS-TMUX-051, CS-TMUX-058: an orphaned reservation older than 60 s does not hold the row; the resume's launch removes it", func() {
		pane(row(nil).JSON())
		orphan := "claude-sandbox-x-proj-abc123-murre"
		f.fake.On("docker ps", psRowResume(orphan, f.proj, "claude", "murre", "12", "created",
			base, cfgDir+"/sessions", markConv)+"\n", nil) // an hour before the clock
		streamEvents(f.fake, dockerEvent("die", "0"))
		f.fake.On("docker start -ai", "", nil)
		Expect(f.run("tmux", "restore")).To(Equal(0), f.errw.String())
		Expect(f.out.String()).To(ContainSubstring("note: " + orphan + " was created for this conversation but never started (an interrupted launch); the resume's launch removes it"))
		Expect(f.fake.CommandLines()).To(ContainElement("docker rm " + orphan))
		Expect(indexOf("docker rm " + orphan)).To(BeNumerically("<", indexOf("docker create")))
		Expect(f.sessionLine()).To(HavePrefix("docker start -ai"))
	})

	Describe("CS-TMUX-063: --drop", func() {
		It("CS-TMUX-063: unsets a pending mark and names it", func() {
			pane(row(nil).JSON())
			Expect(f.run("tmux", "restore", "--drop")).To(Equal(0), f.errw.String())
			Expect(f.out.String()).To(Equal("claude-sandbox: dropped the pending mark of pane main:2.0: 'fix the build' (" + markConv + "); the shell is yours\n"))
			Expect(tmuxLines()).To(Equal([]string{"tmux display-message -p -t %7 " + strings.Join(f.fake.Calls[0].Args[4:], " "), unset}))
			Expect(f.fake.CommandLines()).NotTo(ContainElement(HavePrefix("docker")))
		})

		It("CS-TMUX-063: leaves an active mark or none alone; an unusable row is dropped without its values", func() {
			active := row(nil)
			active.State = tmuxpane.StateActive
			pane(active.JSON())
			Expect(f.run("tmux", "restore", "--drop")).To(Equal(0))
			Expect(f.out.String()).To(ContainSubstring("holds a running session"))
			Expect(unsets()).To(BeZero())

			g := newCLIFixture()
			g.envmap["TMUX"], g.envmap["TMUX_PANE"] = "x", "%7"
			g.fake.On("tmux display-message", "main\t2\t0\t1\t2\t\n", nil)
			Expect(g.run("tmux", "restore", "--drop")).To(Equal(0))
			Expect(g.out.String()).To(ContainSubstring("has no claude-sandbox mark"))

			h := newCLIFixture()
			h.envmap["TMUX"], h.envmap["TMUX_PANE"] = "x", "%7"
			bad := row(func(m *tmuxpane.Mark) { m.Project = "rel"; m.Name = "evil\x1b[2J" })
			h.fake.On("tmux display-message", "main\t2\t0\t1\t2\t"+bad.JSON()+"\n", nil)
			Expect(h.run("tmux", "restore", "--drop")).To(Equal(0))
			Expect(h.out.String()).To(Equal("claude-sandbox: dropped the pending mark of pane main:2.0 (it could not be used)\n"))
		})
	})

	Describe("CS-TMUX-050: the sparse notice is claimed by commands the operator types", func() {
		notice := func(fx *cliFixture) {
			Expect(tmuxpane.WriteNotice(fx.cache, tmuxpane.Notice{Stamp: "20260929T110000", N: 1, M: 9, K: 3, Lifetimes: true,
				At: base.Add(30 * time.Minute).UnixMilli()})).To(Succeed())
		}
		const text = "claude-sandbox: this save (20260929T110000) has 1 sandbox panes"

		It("CS-TMUX-050: a typed restore inside tmux prints and claims it before acting", func() {
			notice(f)
			pane(row(func(m *tmuxpane.Mark) { m.Mode = tmuxpane.ModeRalph }).JSON())
			Expect(f.run("tmux", "restore")).To(Equal(0), f.errw.String())
			Expect(strings.Count(f.errw.String(), text)).To(Equal(1))
			Expect(filepath.Join(f.cache, tmuxpane.NoticeFile)).NotTo(BeAnExistingFile())
			Expect(indexOf("tmux set -gu @claude-sandbox-notice")).To(BeNumerically("<", indexOf("tmux display-message")))
		})

		It("CS-TMUX-050: --list and --dry-run --all outside tmux print it and leave it", func() {
			notice(f)
			delete(f.envmap, "TMUX")
			delete(f.envmap, "TMUX_PANE")
			Expect(f.run("tmux", "restore", "--list")).To(Equal(0), f.errw.String())
			Expect(f.errw.String()).To(ContainSubstring(text))
			Expect(filepath.Join(f.cache, tmuxpane.NoticeFile)).To(BeAnExistingFile())
			Expect(f.fake.CommandLines()).NotTo(ContainElement(HavePrefix("tmux set")))
		})

		It("CS-TMUX-050: a hand launch claims it inside tmux and only prints it outside", func() {
			notice(f)
			streamEvents(f.fake, dockerEvent("die", "0"))
			Expect(f.run()).To(Equal(0), f.errw.String())
			Expect(f.errw.String()).To(ContainSubstring(text))
			Expect(f.fake.CommandLines()).To(ContainElement("tmux set -gu @claude-sandbox-notice"))
			Expect(indexOf("tmux set -gu")).To(BeNumerically("<", indexOf("docker create")))
			Expect(filepath.Join(f.cache, tmuxpane.NoticeFile)).NotTo(BeAnExistingFile())

			g := newCLIFixture()
			g.env.IsTerminal = func(io.Writer) bool { return true }
			notice(g)
			streamEvents(g.fake, dockerEvent("die", "0"))
			Expect(g.run()).To(Equal(0), g.errw.String())
			Expect(g.errw.String()).To(ContainSubstring(text))
			Expect(filepath.Join(g.cache, tmuxpane.NoticeFile)).To(BeAnExistingFile())
			Expect(g.fake.CommandLines()).NotTo(ContainElement(HavePrefix("tmux")))
		})

		It("CS-TMUX-050: a scripted launch whose stderr is no terminal neither prints nor claims it", func() {
			notice(f)
			f.env.IsTerminal = func(io.Writer) bool { return false }
			streamEvents(f.fake, dockerEvent("die", "0"))
			Expect(f.run()).To(Equal(0), f.errw.String())
			Expect(f.errw.String()).NotTo(ContainSubstring(text))
			Expect(f.fake.CommandLines()).NotTo(ContainElement("tmux set -gu @claude-sandbox-notice"))
			Expect(filepath.Join(f.cache, tmuxpane.NoticeFile)).To(BeAnExistingFile())
		})

		It("CS-TMUX-050: a resume a restore started prints nothing more; --detach and headless never print it", func() {
			notice(f)
			f.fake.On("tmux set -gu", "", execx.Fail(1)) // the claim fails: the notice stays
			pane(row(nil).JSON())
			streamEvents(f.fake, dockerEvent("die", "0"))
			f.run("tmux", "restore")
			Expect(f.launched()).NotTo(BeNil())
			Expect(strings.Count(f.errw.String(), text)).To(Equal(1), "only the typed restore printed it")
			Expect(filepath.Join(f.cache, tmuxpane.NoticeFile)).To(BeAnExistingFile())

			for _, args := range [][]string{{"--detach"}, {"headless", "--"}} {
				g := newCLIFixture()
				notice(g)
				g.fake.On("docker inspect", "running 2026-09-29T12:00:00Z\n", nil)
				g.run(args...)
				Expect(g.errw.String()).NotTo(ContainSubstring(text), strings.Join(args, " "))
				Expect(g.out.String()).NotTo(ContainSubstring(text), strings.Join(args, " "))
			}
		})
	})
})
