package main

// Spec: spec/launch.feature CS-LNCH-206 (the key, the env and the label),
// CS-LNCH-207 (telling a hang-up from a detach), CS-LNCH-210 (where the end
// does not apply) and CS-LNCH-212 (the notice a launch claims, and an attach
// to a container already ended). The helper itself is tested in
// internal/sessionend (CS-LNCH-208/209). newCLIFixture pins
// CLAUDE_SANDBOX_END_ON_HANGUP=0; these tests delete the pin.

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/clients"
	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessionend"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions/sessionstest"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

// psRowHangup is psRowMark with the labels up to onhangup (CS-LNCH-206) and
// the mounts.
func psRowHangup(name, project, mode, instance, created, id, onhangup string) string {
	return sessionstest.Row(name, "Up 1 hour", project, mode, instance, "v1", "", "", "", "37", "",
		"running", created, "8g", "default", "", id, "", "/reg/sessions", "", "", "none", "none", "", "none", "", onhangup)
}

var _ = Describe("end the session on hang-up (CS-LNCH-206..212)", func() {
	var f *cliFixture
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	BeforeEach(func() {
		f = newCLIFixture()
		delete(f.envmap, "CLAUDE_SANDBOX_END_ON_HANGUP")
		f.env.Now = func() time.Time { return now }
		f.env.GOOS = "linux"
		f.fake.On("docker create", markID+"\n", nil)
	})

	// helpers is every session-end helper start.
	helpers := func() []execx.Cmd {
		var out []execx.Cmd
		for _, c := range f.fake.Calls {
			if len(c.Args) > 0 && c.Args[0] == sessionend.Subcommand {
				out = append(out, c)
			}
		}
		return out
	}
	label := func() string {
		for i, a := range f.launched().Args {
			if a == "--label" && strings.HasPrefix(f.launched().Args[i+1], "claude-sandbox.onhangup=") {
				return strings.TrimPrefix(f.launched().Args[i+1], "claude-sandbox.onhangup=")
			}
		}
		return "<none>"
	}

	It("CS-LNCH-206: the shipped default is on: the label reads end and a hang-up starts the helper", func() {
		f.fake.SessionSignal = syscall.SIGHUP
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(label()).To(Equal("end"))
		hs := helpers()
		Expect(hs).To(HaveLen(1))
		h := hs[0]
		Expect(h.Name).To(Equal("/fake/claude-sandbox"))
		Expect(h.Detach).To(BeTrue())
		Expect(h.DieWithParent).To(BeFalse())
		Expect(h.Dir).To(Equal("/"))
		Expect(h.Args[:11]).To(Equal([]string{"session-end", "--state-dir", f.state, "--cache-dir", f.cache,
			"--home", f.home, "--config-dir", filepath.Join(f.home, ".claude"), "--hangup-at", "1793188800000"}))
		Expect(h.Args[11]).To(Equal("--mark"))
		m, ok := tmuxpane.ParseMark(h.Args[12])
		Expect(ok).To(BeTrue())
		Expect(m.ContainerID).To(Equal(markID))
		Expect(m.Project).To(Equal(f.proj))
		Expect(m.Instance).NotTo(BeEmpty())
		Expect(m.RegistryDir).To(Equal(filepath.Join(f.home, ".claude", "sessions")))
		Expect(f.errw.String()).NotTo(ContainSubstring("session-end"))
	})

	It("CS-LNCH-207: the lease is released before the helper starts", func() {
		var held []clients.Kind
		f.fake.OnFunc(sessionend.Subcommand, func(execx.Cmd) (string, error) {
			held = heldKinds(f.state, markID)
			return "", nil
		})
		f.fake.SessionSignal = syscall.SIGHUP
		f.run()
		Expect(helpers()).To(HaveLen(1))
		Expect(held).To(BeEmpty())
	})

	DescribeTable("CS-LNCH-207: the first of SIGHUP and SIGTERM decides",
		func(during, late []os.Signal, child syscall.Signal, code int, want bool) {
			f.fake.SessionSignals, f.fake.LateSignals, f.fake.ChildSignal = during, late, child
			if code != 0 {
				f.fake.On("docker start -ai", "", execx.Fail(code))
			}
			f.run()
			Expect(helpers()).To(HaveLen(map[bool]int{true: 1, false: 0}[want]))
		},
		Entry("SIGHUP during the child", []os.Signal{syscall.SIGHUP}, nil, syscall.Signal(0), 0, true),
		Entry("SIGHUP during the die wait", nil, []os.Signal{syscall.SIGHUP}, syscall.Signal(0), 0, true),
		Entry("SIGHUP then SIGTERM in the die wait", nil, []os.Signal{syscall.SIGHUP, syscall.SIGTERM}, syscall.Signal(0), 0, true),
		Entry("SIGHUP in the child, then SIGTERM in the die wait", []os.Signal{syscall.SIGHUP}, []os.Signal{syscall.SIGTERM}, syscall.Signal(0), 0, true),
		Entry("SIGTERM then SIGHUP", []os.Signal{syscall.SIGTERM, syscall.SIGHUP}, nil, syscall.Signal(0), 0, false),
		Entry("SIGTERM in the child, then SIGHUP in the die wait", []os.Signal{syscall.SIGTERM}, []os.Signal{syscall.SIGHUP}, syscall.Signal(0), 0, false),
		Entry("SIGTERM only", []os.Signal{syscall.SIGTERM}, nil, syscall.Signal(0), 0, false),
		Entry("SIGINT then SIGHUP (not foreground)", []os.Signal{syscall.SIGINT, syscall.SIGHUP}, nil, syscall.Signal(0), 0, true),
		Entry("docker died of SIGHUP, nothing caught", nil, nil, syscall.SIGHUP, 129, true),
		Entry("exit code 129 without a signal", nil, nil, syscall.Signal(0), 129, false),
		Entry("the detach keys: docker exited by itself, no die", nil, nil, syscall.Signal(0), 0, false),
	)

	It("CS-LNCH-207: docker dying of SIGHUP while SIGHUP is inherited as ignored is no hang-up", func() {
		defer func(old func() bool) { hupIgnored = old }(hupIgnored)
		hupIgnored = func() bool { return true }
		f.fake.ChildSignal = syscall.SIGHUP
		f.fake.On("docker start -ai", "", execx.Fail(129))
		f.run()
		Expect(helpers()).To(BeEmpty())
	})

	It("CS-LNCH-207: the helper starts before the pane mark's checks", func() {
		f.envmap["TMUX"] = "/tmp/tmux-1000/default,1,0"
		f.envmap["TMUX_PANE"] = "%3"
		f.fake.SessionSignal = syscall.SIGHUP
		f.run()
		lines := f.fake.CommandLines()
		hi := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "/fake/claude-sandbox session-end") })
		si := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "systemctl is-system-running") })
		Expect(hi).To(BeNumerically(">=", 0))
		Expect(si).To(BeNumerically(">", hi))
	})

	It("CS-LNCH-207: a helper that cannot start is one line in the cache's session-end.log", func() {
		f.env.Executable = func() (string, error) { return "", os.ErrNotExist }
		f.fake.SessionSignal = syscall.SIGHUP
		Expect(f.run()).To(Equal(0))
		data, err := os.ReadFile(filepath.Join(f.cache, sessionend.LogFile))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(ContainSubstring("cannot start the session-end helper"))
		Expect(f.errw.String()).NotTo(ContainSubstring("session-end"))
	})

	It("CS-LNCH-206: the key off (or the env off) labels keep and starts no helper", func() {
		writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "endOnHangup: false\n")
		f.fake.SessionSignal = syscall.SIGHUP
		f.run()
		Expect(label()).To(Equal("keep"))
		Expect(helpers()).To(BeEmpty())

		f = newCLIFixture()
		f.fake.On("docker create", markID+"\n", nil)
		f.fake.SessionSignal = syscall.SIGHUP
		f.run()
		Expect(label()).To(Equal("keep"))
		Expect(helpers()).To(BeEmpty())
	})

	It("CS-LNCH-210: --detach labels keep", func() {
		f.fake.On("docker inspect --type container", "running 2026-10-22T00:00:00Z\n", nil)
		Expect(f.run("--detach")).To(Equal(0), f.errw.String())
		Expect(label()).To(Equal("keep"))
	})

	It("CS-LNCH-210: headless labels keep and its SIGHUP starts no helper", func() {
		f.fake.SessionSignal = syscall.SIGHUP
		f.run("headless", "--")
		Expect(label()).To(Equal("keep"))
		Expect(helpers()).To(BeEmpty())
	})

	It("CS-LNCH-210: a launcher in a sandbox, or on a host other than Linux, starts no helper", func() {
		f.env.GOOS = "darwin"
		f.fake.SessionSignal = syscall.SIGHUP
		f.run()
		Expect(label()).To(Equal("end"))
		Expect(helpers()).To(BeEmpty())

		f = newCLIFixture()
		delete(f.envmap, "CLAUDE_SANDBOX_END_ON_HANGUP")
		f.env.GOOS = "linux"
		f.envmap["CLAUDE_SANDBOX_PROJECT_DIR"] = f.proj
		f.envmap["TMPDIR"] = f.home
		f.fake.On("docker create", markID+"\n", nil)
		f.fake.SessionSignal = syscall.SIGHUP
		f.run()
		Expect(helpers()).To(BeEmpty())
	})

	Describe("attach and join", func() {
		created := "2026-10-28 11:00:00 +0000 UTC"
		running := func(mode, onhangup string) {
			f.fake.On("docker ps", psRowHangup("cs-otter", f.proj, mode, "otter", created, markID, onhangup)+"\n", nil)
			f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
		}

		DescribeTable("CS-LNCH-206: an attach's hang-up follows its env, else the label, else its cascade",
			func(env, labelValue, mode string, want bool) {
				if env != "" {
					f.envmap["CLAUDE_SANDBOX_END_ON_HANGUP"] = env
				}
				running(mode, labelValue)
				f.fake.SessionSignal = syscall.SIGHUP
				f.run("--attach=otter")
				Expect(f.sessionLine()).To(ContainSubstring("docker attach"))
				Expect(helpers()).To(HaveLen(map[bool]int{true: 1, false: 0}[want]))
				if want {
					m, _ := tmuxpane.ParseMark(helpers()[0].Args[12])
					Expect(m.ContainerID).To(Equal(markID))
					Expect(m.Instance).To(Equal("otter"))
					Expect(m.RegistryDir).To(Equal("/reg/sessions"))
				}
			},
			Entry("label end", "", "end", "claude", true),
			Entry("label keep", "", "keep", "claude", false),
			Entry("env on over label keep", "1", "keep", "claude", true),
			Entry("env off over label end", "0", "end", "claude", false),
			Entry("no label: the default", "", "", "claude", true),
		)

		It("CS-LNCH-206: a ralph or headless container is never ended, whatever the env", func() {
			f.envmap["CLAUDE_SANDBOX_END_ON_HANGUP"] = "1"
			Expect(endOnHangupFor(f.env, sessions.Session{Mode: "ralph", OnHangup: "end"}, nil)).To(BeFalse())
			Expect(endOnHangupFor(f.env, sessions.Session{Mode: "headless", OnHangup: "end"}, nil)).To(BeFalse())
			Expect(endOnHangupFor(f.env, sessions.Session{Mode: "claude", OnHangup: "keep"}, nil)).To(BeTrue())
		})

		It("CS-LNCH-210: a join's hang-up starts no helper", func() {
			running("claude", "end")
			f.fake.SessionSignal = syscall.SIGHUP
			f.run("--join=otter")
			Expect(f.sessionLine()).To(ContainSubstring("docker exec"))
			Expect(helpers()).To(BeEmpty())
		})

		It("CS-LNCH-212: an attach to a container already ended prints the notice and exits 1, running no docker attach", func() {
			running("claude", "end")
			Expect(sessionend.WriteNotice(f.state, sessionend.Notice{V: 1, Project: f.proj, At: now.UnixMilli(),
				Entries: []sessionend.NoticeEntry{{Instance: "otter", Resume: "cd x && claude-sandbox --new"}}})).To(Succeed())
			f.fake.On("docker inspect --type container", "", &execx.CodeError{Code: 1, Msg: "Error: No such container: " + markID})
			Expect(f.run("--attach=otter")).To(Equal(1))
			Expect(f.errw.String()).To(ContainSubstring("Note: 'otter' ended when its terminal closed"))
			Expect(f.errw.String()).To(ContainSubstring("'otter' is no longer running.\n"))
			Expect(f.fake.Session).To(BeNil())
			Expect(heldKinds(f.state, markID)).To(BeEmpty())
			ents, _ := os.ReadDir(hostdirs.NoticesDir(f.state))
			Expect(ents).To(BeEmpty())
		})

		It("CS-LNCH-212: a join to a removing container is gone too", func() {
			running("claude", "end")
			f.fake.On("docker inspect --type container", sessionstest.InspectRow("/cs-otter", "removing", "2026-10-28T11:00:00Z", f.proj, "otter", "claude")+"\n", nil)
			Expect(f.run("--join=otter")).To(Equal(1))
			Expect(f.errw.String()).To(ContainSubstring("'otter' is no longer running."))
			Expect(f.fake.Session).To(BeNil())
		})

		It("CS-LNCH-212: any other inspect error goes on, and docker attach runs", func() {
			running("claude", "end")
			f.fake.On("docker inspect --type container", "", &execx.CodeError{Code: 1, Msg: "Cannot connect to the Docker daemon"})
			Expect(f.run("--attach=otter")).To(Equal(0), f.errw.String())
			Expect(f.sessionLine()).To(ContainSubstring("docker attach"))
			Expect(f.errw.String()).NotTo(ContainSubstring("no longer running"))
		})
	})

	It("CS-LNCH-212: an interactive launch in a terminal claims this project's notices once; a scripted one does not", func() {
		Expect(sessionend.WriteNotice(f.state, sessionend.Notice{V: 1, Project: f.proj, At: now.UnixMilli(),
			Entries: []sessionend.NoticeEntry{{Instance: "heron", Resume: "cd " + f.proj + " && claude-sandbox --new --no-worktree -- --resume x"}}})).To(Succeed())
		f.run("--new")
		Expect(f.errw.String()).NotTo(ContainSubstring("heron"))

		f.env.IsTerminal = func(io.Writer) bool { return true }
		f.errw.Reset()
		f.run("--new")
		Expect(f.errw.String()).To(ContainSubstring("Note: 'heron' ended when its terminal closed (after its turn, "))
		Expect(f.errw.String()).To(ContainSubstring("All sessions ended this way: " + hostdirs.SessionEndsLog(f.state)))
		f.errw.Reset()
		f.run("--new")
		Expect(f.errw.String()).NotTo(ContainSubstring("heron"))
	})

	It("CS-LNCH-212: headless claims nothing", func() {
		f.env.IsTerminal = func(io.Writer) bool { return true }
		Expect(sessionend.WriteNotice(f.state, sessionend.Notice{V: 1, Project: f.proj, At: now.UnixMilli(),
			Entries: []sessionend.NoticeEntry{{Instance: "heron"}}})).To(Succeed())
		f.run("headless", "--")
		Expect(f.errw.String()).NotTo(ContainSubstring("heron"))
	})

	It("CS-LNCH-208: the hidden session-end subcommand runs the helper with the launcher's dirs and logs its outcome", func() {
		m := tmuxpane.Mark{V: tmuxpane.MarkVersion, State: tmuxpane.StateActive, Mode: tmuxpane.ModeClaude,
			Container: "cs-otter", ContainerID: markID, Instance: "otter", Project: f.proj, Class: "37"}
		f.fake.On("docker inspect --type container", "", &execx.CodeError{Code: 1, Msg: "Error: No such container: " + markID})
		code := f.run(sessionend.Subcommand, "--state-dir", f.state, "--cache-dir", f.cache, "--home", f.home,
			"--config-dir", filepath.Join(f.home, ".claude"), "--hangup-at", "1793188800000", "--mark", m.JSON())
		Expect(code).To(Equal(0), f.errw.String())
		Expect(f.out.String() + f.errw.String()).To(BeEmpty())
		data, err := os.ReadFile(hostdirs.SessionEndsLog(f.state))
		Expect(err).NotTo(HaveOccurred())
		var e sessionend.Entry
		Expect(json.Unmarshal(bytes.TrimSpace(data), &e)).To(Succeed())
		Expect(e.Outcome).To(Equal("ended on its own"))
		Expect(e.ContainerID).To(Equal(markID))
		log, err := os.ReadFile(filepath.Join(f.cache, sessionend.LogFile))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(log)).To(ContainSubstring("ended on its own"))

		Expect(f.run(sessionend.Subcommand, "--state-dir", f.state)).To(Equal(2))
	})

	It("CS-LNCH-206: the fixture pin keeps every other test helper-free", func() {
		g := newCLIFixture()
		Expect(g.envmap["CLAUDE_SANDBOX_END_ON_HANGUP"]).To(Equal("0"))
		g.env.Prompter = &prompt.Scripted{}
		g.fake.On("docker create", markID+"\n", nil)
		g.fake.SessionSignal = syscall.SIGHUP
		g.run()
		for _, c := range g.fake.Calls {
			Expect(c.Args).NotTo(ContainElement(sessionend.Subcommand))
		}
	})
})
