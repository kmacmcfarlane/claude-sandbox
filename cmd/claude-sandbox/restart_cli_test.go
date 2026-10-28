package main

// Spec: spec/sessions.feature CS-SESS-105..108 (the restart's grammar, its
// conversation, its launch and its questions), spec/launch.feature
// CS-LNCH-218 (the sequence), CS-LNCH-220 (every failure after the stop) and
// CS-LNCH-221 (the stand-downs), spec/tmux.feature CS-TMUX-080 (the old
// container's pane marks). Docker and tmux run through execx.Fake; the
// registry, the leases, the end lock and /proc are scratch directories.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

const (
	rsOldID = "0a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9"
	rsNewID = "f9e8d7c6b5a40392817f6e5d4c3b2a10f9e8d7c6b5a40392817f6e5d4c3b2a10"
	rsConv  = "3f2a9c1e-7b4d-4e8f-9a0b-1c2d3e4f5a6b"
	rsOther = "7c8d9e0f-1a2b-4c3d-8e4f-5a6b7c8d9e0f"
	rsJoin  = "9e8d7c6b-5a49-4382-a716-f5e4d3c2b1a0"
	rsName  = "claude-sandbox-work-proj-abc123-dunlin"
)

// restartFix is a project with one running sandbox, 'dunlin', to restart.
type restartFix struct {
	*cliFixture
	reg      string
	created  time.Time
	now      time.Time
	stopped  bool
	gone     bool
	removing bool // the removal never finishes
	worktree string
	model    string
	flags    string
	resume   string
	class    string
	cfgDir   string
	cmd      []string
	// access is the claude-sandbox.access label (CS-LNCH-223); "" = absent.
	access  string
	extra   []string // more docker ps rows
	onStop  func()
	stopErr error
	// failPS makes docker ps fail.
	failPS bool
	// onInspect runs before every InspectContainer answer.
	onInspect func()
	// create answers the n-th docker create (1-based); nil succeeds.
	create  func(c execx.Cmd, n int) (string, error)
	creates int
}

func newRestartFix() *restartFix {
	f := &restartFix{cliFixture: newCLIFixture()}
	f.now = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	f.created = f.now.Add(-time.Hour)
	f.env.Now = func() time.Time { return f.now }
	f.env.GOOS = "linux"
	f.reg = filepath.Join(f.home, ".claude", "sessions")
	Expect(os.MkdirAll(f.reg, 0o700)).To(Succeed())
	f.class = "7"
	f.flags = ""
	f.cmd = []string{"claude", "--resume", rsConv}
	f.access = "none"
	f.fake.OnFunc("docker ps", func(execx.Cmd) (string, error) {
		if f.failPS {
			return "", execx.Fail(1)
		}
		var rows []string
		switch {
		case !f.stopped:
			rows = append(rows, f.row("running", "Up 1 hour"))
		case f.removing:
			rows = append(rows, f.row("exited", "Exited (143) 1 second ago"))
		}
		rows = append(rows, f.extra...)
		return strings.Join(rows, "\n") + "\n", nil
	})
	f.fake.OnFunc("json .Config.Cmd", func(execx.Cmd) (string, error) {
		c, _ := json.Marshal(f.cmd)
		return string(c) + "\n", nil
	})
	f.fake.OnFunc("{{.Name}}", func(c execx.Cmd) (string, error) {
		if f.onInspect != nil {
			f.onInspect()
		}
		if f.gone || (f.stopped && !f.removing) {
			if c.Stderr != nil {
				c.Stderr.Write([]byte("Error: No such container: x\n"))
			}
			return "", execx.Fail(1)
		}
		state := "running"
		if f.stopped {
			state = "exited"
		}
		return strings.Join([]string{"/" + rsName, state, f.created.Format(time.RFC3339Nano), f.proj, "dunlin", "claude"}, "\x1f") + "\n", nil
	})
	f.fake.OnFunc("docker stop", func(execx.Cmd) (string, error) {
		if f.onStop != nil {
			f.onStop()
		}
		if f.stopErr != nil {
			return "", f.stopErr
		}
		f.stopped = true
		return "", nil
	})
	f.fake.OnFunc("docker create", func(c execx.Cmd) (string, error) {
		f.creates++
		if f.create != nil {
			return f.create(c, f.creates)
		}
		return rsNewID + "\n", nil
	})
	return f
}

// row is the target's docker ps row, every label up to the resume label.
func (f *restartFix) row(state, status string) string {
	return strings.Join([]string{rsName, status, f.proj, "claude", "dunlin", "v1", f.model, "", "", f.class, f.worktree,
		state, f.created.Format("2006-01-02 15:04:05 -0700 MST"), "8g", "default", "", rsOldID,
		f.cfgDir, f.reg, f.flags, f.resume, "none", "none", "", "none", "", "keep", f.access, ""}, psSep)
}

// otherRow is another sandbox's docker ps row.
func (f *restartFix) otherRow(name, instance, mode, state, resume string) string {
	return strings.Join([]string{name, "Up 1 hour", f.proj, mode, instance, "v1", "", "", "", "9", "",
		state, f.created.Format("2006-01-02 15:04:05 -0700 MST"), "8g", "default", "", strings.Repeat("b", 64),
		"", f.reg, "", resume}, psSep)
}

// record writes a registry record at the class.
func (f *restartFix) record(pid int, conv, status, name, nameSource, cwd string, started time.Time) {
	b, err := json.Marshal(map[string]any{
		"pid": pid, "sessionId": conv, "cwd": cwd, "startedAt": started.UnixMilli(),
		"pidDomain": "linux::pid:[4026532001]", "status": status, "name": name, "nameSource": nameSource,
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(os.WriteFile(filepath.Join(f.reg, strconv.Itoa(pid)+".json"), b, 0o600)).To(Succeed())
}

// primary writes the primary claude's record: idle, named by the user.
func (f *restartFix) primary(status string) {
	f.record(263, rsConv, status, "fix the tests", "user", f.proj, f.created.Add(3*time.Second))
}

func (f *restartFix) tty(answers ...string) {
	f.env.Prompter = &prompt.Scripted{IsTTY: true, Answers: answers}
}

// asked is every question the restart put.
func (f *restartFix) asked() []string {
	if s, ok := f.env.Prompter.(*prompt.Scripted); ok {
		return s.Asked
	}
	return nil
}

// index is the position of the first command line with prefix p, -1 if none.
func (f *restartFix) index(p string) int {
	for i, l := range f.fake.CommandLines() {
		if strings.HasPrefix(l, p) {
			return i
		}
	}
	return -1
}

func (f *restartFix) count(p string) int {
	n := 0
	for _, l := range f.fake.CommandLines() {
		if strings.HasPrefix(l, p) {
			n++
		}
	}
	return n
}

// createdCmd is the new container's claude command line.
func (f *restartFix) createdCmd() []string {
	args := f.launched().Args
	for i, a := range args {
		if a == "claude" && i > 0 && strings.Contains(args[i-1], "claude-sandbox") {
			return args[i:]
		}
	}
	i := slices.Index(args, "claude")
	Expect(i).To(BeNumerically(">", 0), "no claude command in %v", args)
	return args[i:]
}

func (f *restartFix) logEntries() []sessionend.Entry {
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

func (f *restartFix) noDocker() {
	for _, l := range f.fake.CommandLines() {
		Expect(l).NotTo(HavePrefix("docker "), "a docker call before the refusal")
	}
}

var _ = Describe("restart a session in place (CS-SESS-105..108, CS-LNCH-218..221, CS-TMUX-080)", func() {
	var f *restartFix
	BeforeEach(func() { f = newRestartFix() })

	Describe("CS-SESS-105: the grammar and the target", func() {
		DescribeTable("CS-SESS-105: refused with exit 2 before any docker call",
			func(want string, args ...string) {
				Expect(f.run(args...)).To(Equal(2))
				Expect(f.errw.String()).To(ContainSubstring(want))
				f.noDocker()
			},
			Entry("--attach", "conflicts with --attach", "--restart", "--attach"),
			Entry("--join", "conflicts with --join", "--restart=dunlin", "--join"),
			Entry("--branch", "conflicts with --branch", "--restart", "--branch"),
			Entry("--new", "conflicts with --new", "--restart", "--new"),
			Entry("--no-session-check", "conflicts with --no-session-check", "--restart", "--no-session-check"),
			Entry("--detach", "not valid with --detach", "--restart", "--detach"),
			Entry("--ralph", "not valid with --ralph", "--restart", "--ralph"),
			Entry("--worktree", "no --worktree", "--restart", "--worktree"),
			Entry("--worktree=NAME", "no --worktree", "--restart", "--worktree=x"),
			Entry("--no-worktree", "no --worktree", "--restart", "--no-worktree"),
			Entry("--continue", "drop --continue", "--restart", "--", "--continue"),
			Entry("--fork-session", "to fork it", "--restart", "--", "--resume", rsConv, "--fork-session"),
			Entry("--session-id", "--session-id is not valid", "--restart", "--", "--session-id", rsConv),
			Entry("claude's -w", "claude's -w", "--restart", "--", "-w", "x"),
			Entry("a --resume without an id", "without an id", "--restart", "--", "--resume"),
		)

		It("CS-SESS-105: a named noun among this project's sessions, the clean case: no question, one line", func() {
			f.primary("idle")
			Expect(f.run("--restart=dunlin")).To(Equal(0), f.errw.String())
			Expect(f.errw.String()).To(ContainSubstring("Restarting 'dunlin' ('fix the tests', " + rsConv + "): its container is stopped"))
			Expect(f.errw.String()).To(ContainSubstring("anything else running in that container ends with it"))
			Expect(f.index("docker stop --signal SIGTERM --time 30 " + rsOldID)).To(BeNumerically(">=", 0))
		})

		It("CS-SESS-105: a bare --restart uses the only candidate, named in the restart line", func() {
			f.primary("idle")
			Expect(f.run("--restart")).To(Equal(0), f.errw.String())
			Expect(f.errw.String()).To(ContainSubstring("Restarting 'dunlin' ('fix the tests'"))
			Expect(f.asked()).To(BeEmpty())
		})

		It("CS-SESS-105: several candidates and no terminal: exit 3; with one, the prompt names the verb", func() {
			f.extra = append(f.extra, f.otherRow("claude-sandbox-work-proj-abc123-heron", "heron", "claude", "running", ""))
			Expect(f.run("--restart")).To(Equal(3))
			Expect(f.errw.String()).To(ContainSubstring("--restart=dunlin"))
			Expect(f.count("docker stop")).To(BeZero())
		})

		It("CS-SESS-105: an unknown noun exits 2 naming the available ones", func() {
			Expect(f.run("--restart=heron")).To(Equal(2))
			Expect(f.errw.String()).To(ContainSubstring("no running session named 'heron'. Available: dunlin"))
		})

		DescribeTable("CS-LNCH-221: headless, ralph, reserved and paused containers are refused by name",
			func(mode, state, want string) {
				f.extra = append(f.extra, f.otherRow("claude-sandbox-work-proj-abc123-heron", "heron", mode, state, ""))
				Expect(f.run("--restart=heron")).To(Equal(2))
				Expect(f.errw.String()).To(ContainSubstring(want))
				Expect(f.count("docker stop")).To(BeZero())
			},
			Entry("headless", "headless", "running", "headless container"),
			Entry("ralph", "ralph", "running", "ralph loop"),
			Entry("reserved", "claude", "created", "still being started"),
			Entry("paused", "claude", "paused", "is paused"),
		)

		It("CS-SESS-016: the tier-1 menu offers [r]; choosing it restarts the picked session", func() {
			f.primary("idle")
			f.extra = append(f.extra, f.otherRow("claude-sandbox-work-proj-abc123-heron", "heron", "claude", "running", ""))
			f.tty("r", "dunlin")
			Expect(f.run()).To(Equal(0), f.errw.String())
			Expect(f.errw.String()).To(ContainSubstring("[r] restart a session in a new container"))
			Expect(f.asked()).To(Equal([]string{"Choice [n/b/j/a/r/q]:", "Which session to restart? [dunlin/heron]:"}))
			Expect(f.index("docker stop --signal SIGTERM --time 30 " + rsOldID)).To(BeNumerically(">=", 0))
			Expect(labelsOf(f.launched().Args)).To(HaveKeyWithValue("claude-sandbox.instance", "dunlin"))
		})

		It("CS-SESS-105: the tier-1 [r] drops a worktree flag, with a note when it named another place", func() {
			f.primary("idle")
			f.extra = append(f.extra, f.otherRow("claude-sandbox-work-proj-abc123-heron", "heron", "claude", "running", ""))
			f.tty("r", "dunlin")
			Expect(f.run("--worktree=elsewhere")).To(Equal(0), f.errw.String())
			Expect(f.errw.String()).To(ContainSubstring("Note: you asked for --worktree=elsewhere; 'dunlin' runs in the shared checkout"))
			Expect(f.createdCmd()).NotTo(ContainElement("--worktree"))
		})

		It("CS-SESS-105: a --resume naming another conversation is refused; one a join holds names the container", func() {
			f.primary("idle")
			Expect(f.run("--restart=dunlin", "--", "--resume", rsOther)).To(Equal(2))
			Expect(f.errw.String()).To(ContainSubstring("has conversation " + rsConv + " open, not " + rsOther))

			f2 := newRestartFix()
			f2.primary("idle")
			f2.record(519, rsJoin, "idle", "side quest", "user", f2.proj, f2.created.Add(10*time.Minute))
			Expect(f2.run("--restart=dunlin", "--", "--resume", rsJoin)).To(Equal(2))
			Expect(f2.errw.String()).To(ContainSubstring("conversation " + rsJoin + " is open in a session joined to 'dunlin' (" + rsName + ")"))
			Expect(f2.count("docker stop")).To(BeZero())
		})
	})

	Describe("CS-SESS-106: the conversation and its place", func() {
		It("CS-SESS-106: the record's conversation wins over the resume label (a /resume inside the session)", func() {
			f.resume = rsOther
			f.primary("idle")
			Expect(f.run("--restart=dunlin")).To(Equal(0), f.errw.String())
			cmd := f.createdCmd()
			Expect(cmd).To(ContainElements("--resume", rsConv))
			Expect(labelsOf(f.launched().Args)).To(HaveKeyWithValue("claude-sandbox.resume", rsConv))
		})

		It("CS-SESS-106: a primary record that stays malformed exits 2, never resuming a join's", func() {
			Expect(os.WriteFile(filepath.Join(f.reg, "263.json"), []byte(`{"pid":263,`), 0o600)).To(Succeed())
			f.record(519, rsJoin, "idle", "side quest", "user", f.proj, f.created.Add(10*time.Minute))
			Expect(f.run("--restart=dunlin")).To(Equal(2))
			Expect(f.errw.String()).To(ContainSubstring("cannot tell which conversation 'dunlin' has open"))
			Expect(f.count("docker stop")).To(BeZero())
			Expect(f.count("docker create")).To(BeZero())
		})

		It("CS-SESS-106: a record outside the primary window is not the primary (it may be a join's)", func() {
			f.record(519, rsJoin, "idle", "side quest", "user", f.proj, f.created.Add(10*time.Minute))
			Expect(f.run("--restart=dunlin")).To(Equal(2))
			Expect(f.errw.String()).To(ContainSubstring("no registry record at its pid class 7"))
			Expect(f.count("docker stop")).To(BeZero())
		})

		It("CS-SESS-106: a record cwd outside the session's place exits 2", func() {
			f.record(263, rsConv, "idle", "fix the tests", "user", "/elsewhere", f.created.Add(3*time.Second))
			Expect(f.run("--restart=dunlin")).To(Equal(2))
			Expect(f.errw.String()).To(ContainSubstring("runs in /elsewhere"))
			Expect(f.count("docker stop")).To(BeZero())
		})

		It("CS-SESS-106: a config dir other than the launcher's exits 2 with the prefixed command", func() {
			f.cfgDir = "/other/claude"
			f.primary("idle")
			Expect(f.run("--restart=dunlin")).To(Equal(2))
			Expect(f.errw.String()).To(ContainSubstring("CLAUDE_CONFIG_DIR=/other/claude claude-sandbox --restart=dunlin"))
			Expect(f.count("docker stop")).To(BeZero())
		})

		It("CS-SESS-106: a container from before the labels exits 2", func() {
			f.reg = ""
			Expect(f.run("--restart=dunlin")).To(Equal(2))
			Expect(f.errw.String()).To(ContainSubstring("older launcher"))
		})

		It("CS-SESS-106: no record, a container younger than 30 s: exit 1, not up yet", func() {
			f.created = f.now.Add(-10 * time.Second)
			f.resume = rsConv
			Expect(f.run("--restart=dunlin")).To(Equal(1))
			Expect(f.errw.String()).To(ContainSubstring("not up yet"))
			Expect(f.count("docker stop")).To(BeZero())
		})

		It("CS-SESS-106: no record and neither a label nor a mark: exit 2", func() {
			Expect(f.run("--restart=dunlin")).To(Equal(2))
			Expect(f.count("docker stop")).To(BeZero())
		})
	})

	Describe("CS-SESS-108: when the restart asks", func() {
		It("CS-SESS-108: no record but a resume label: asks, default No, with the picker command; No stops nothing", func() {
			f.resume = rsConv
			f.tty("")
			Expect(f.run("--restart=dunlin")).To(Equal(1))
			out := f.errw.String()
			Expect(out).To(ContainSubstring("has no registry record"))
			Expect(out).To(ContainSubstring("it was launched with conversation " + rsConv))
			Expect(out).To(ContainSubstring("claude-sandbox --new --no-worktree -- --resume"))
			Expect(f.asked()).To(Equal([]string{"Restart 'dunlin'? [y/N]:"}))
			Expect(f.count("docker stop")).To(BeZero())
		})

		It("CS-SESS-108: no record but a resume label, answered yes: stopped and resumed, logged with its source", func() {
			f.resume = rsConv
			f.tty("y")
			Expect(f.run("--restart=dunlin")).To(Equal(0), f.errw.String())
			Expect(f.createdCmd()).To(ContainElements("--resume", rsConv))
			es := f.logEntries()
			Expect(es).To(HaveLen(1))
			Expect(es[0].ConversationSource).To(Equal("label"))
			Expect(es[0].TurnState).To(Equal("unknown"))
			Expect(es[0].Picker).To(ContainSubstring("-- --resume"))
		})

		It("CS-SESS-108: no record but a pane mark naming the conversation: the mark is the source", func() {
			m := tmuxpane.Mark{V: tmuxpane.MarkVersion, State: tmuxpane.StateActive, Mode: tmuxpane.ModeClaude,
				Container: rsName, ContainerID: rsOldID, Instance: "dunlin", Project: f.proj, Worktree: "",
				Conversation: rsOther, Name: "mark name", NameSource: "user"}
			f.fake.On("list-panes", "%12\t"+m.JSON()+"\n", nil)
			f.tty("")
			Expect(f.run("--restart=dunlin")).To(Equal(1))
			Expect(f.errw.String()).To(ContainSubstring("its tmux pane last recorded conversation " + rsOther + " ('mark name')"))
		})

		It("CS-SESS-108: no terminal for a question: exit 3, nothing stopped", func() {
			f.resume = rsConv
			Expect(f.run("--restart=dunlin")).To(Equal(3))
			Expect(f.count("docker stop")).To(BeZero())
		})

		It("CS-SESS-108: a running turn refuses, exit 1", func() {
			f.primary("busy")
			Expect(f.run("--restart=dunlin")).To(Equal(1))
			Expect(f.errw.String()).To(ContainSubstring("'dunlin' is busy"))
			Expect(f.count("docker stop")).To(BeZero())
			Expect(f.count("docker create")).To(BeZero())
		})

		It("CS-SESS-108: an attached terminal is named (kind, tmux pane, since) and asked about; No stops nothing", func() {
			f.primary("idle")
			l, err := clients.Take(f.state, rsOldID, clients.KindAttach, f.now.Add(-5*time.Minute), "%12")
			Expect(err).NotTo(HaveOccurred())
			defer l.Release()
			f.tty("n")
			Expect(f.run("--restart=dunlin")).To(Equal(1))
			Expect(f.errw.String()).To(ContainSubstring("'dunlin' is attached in another terminal (attach, tmux pane %12, since "))
			Expect(f.count("docker stop")).To(BeZero())
		})

		It("CS-SESS-108: a joined session is named, said to be SIGKILLed; yes prints and logs its resume command", func() {
			f.primary("idle")
			f.record(519, rsJoin, "idle", "side quest", "user", f.proj, f.created.Add(10*time.Minute))
			f.tty("y")
			Expect(f.run("--restart=dunlin")).To(Equal(0), f.errw.String())
			out := f.errw.String()
			Expect(out).To(ContainSubstring("A joined session runs there too: 'side quest' (" + rsJoin + "). It is killed (SIGKILL)"))
			Expect(out).To(ContainSubstring("Its joined sessions were killed with it; resume them with:"))
			Expect(out).To(ContainSubstring("-- --resume " + rsJoin))
			es := f.logEntries()
			Expect(es).To(HaveLen(1))
			Expect(es[0].Outcome).To(Equal(sessionend.OutcomeRestarted))
			Expect(es[0].Sessions).To(HaveLen(2))
			Expect(es[0].Sessions[1].Kind).To(Equal("join"))
			Expect(es[0].Sessions[1].Resume).To(ContainSubstring(rsJoin))
			ents, _ := os.ReadDir(hostdirs.NoticesDir(f.state))
			Expect(ents).To(HaveLen(1), "the killed join's notice")
		})

		It("CS-SESS-108: a waiting prompt asks, and says the prompt is dropped", func() {
			f.primary("waiting")
			f.tty("y")
			Expect(f.run("--restart=dunlin")).To(Equal(0), f.errw.String())
			Expect(f.errw.String()).To(ContainSubstring("is waiting for an answer to a prompt; restarting drops that prompt"))
		})

		It("CS-LNCH-221: off Linux presence cannot be told, so the restart always asks", func() {
			f.primary("idle")
			f.env.GOOS = "darwin"
			f.tty("n")
			Expect(f.run("--restart=dunlin")).To(Equal(1))
			Expect(f.errw.String()).To(ContainSubstring("Cannot tell whether a terminal is attached to 'dunlin'"))
		})

		It("CS-SESS-108: the re-check: a turn started after the decision refuses with no stop and no mark touched", func() {
			f.primary("idle")
			f.fake.OnFunc("buildx", func(execx.Cmd) (string, error) {
				f.primary("busy")
				return "", nil
			})
			Expect(f.run("--restart=dunlin")).To(Equal(1))
			Expect(f.errw.String()).To(ContainSubstring("started a turn since you chose it"))
			Expect(f.count("docker stop")).To(BeZero())
			Expect(f.count("tmux set-option")).To(BeZero())
		})

		It("CS-SESS-108: the re-check: a prompt waiting since the decision asks again", func() {
			f.primary("idle")
			f.fake.OnFunc("buildx", func(execx.Cmd) (string, error) {
				f.primary("waiting")
				return "", nil
			})
			f.tty("n")
			Expect(f.run("--restart=dunlin")).To(Equal(1))
			Expect(f.errw.String()).To(ContainSubstring("is waiting for an answer to a prompt now"))
			Expect(f.count("docker stop")).To(BeZero())
		})

		It("CS-SESS-108: the re-check: a malformed record exits 2 before the turn rule (no docker top)", func() {
			f.primary("idle")
			f.fake.OnFunc("buildx", func(execx.Cmd) (string, error) {
				os.WriteFile(filepath.Join(f.reg, "263.json"), []byte(`{"pid":263,`), 0o600)
				return "", nil
			})
			Expect(f.run("--restart=dunlin")).To(Equal(2))
			Expect(f.count("docker stop")).To(BeZero())
			Expect(f.count("docker top " + rsOldID)).To(BeZero())
		})
	})

	Describe("CS-SESS-107: the new launch", func() {
		It("CS-SESS-107: the old command line's flags, the model and the worktree; the noun kept though its worktree exists", func() {
			f.worktree, f.model, f.flags = "feature-x", "opus", "--model,--effort"
			f.fake.On("rev-parse --show-toplevel", f.proj+"\n", nil)
			Expect(os.MkdirAll(filepath.Join(f.proj, ".claude", "worktrees", "feature-x"), 0o755)).To(Succeed())
			f.cmd = []string{"claude", "--worktree", "feature-x", "--model", "opus", "--resume", rsOther, "--name", "old", "--effort", "high", "--verbose"}
			f.record(263, rsConv, "idle", "fix the tests", "user", filepath.Join(f.proj, ".claude", "worktrees", "feature-x"), f.created.Add(3*time.Second))
			Expect(f.run("--restart=dunlin")).To(Equal(0), f.errw.String())
			cmd := f.createdCmd()
			Expect(strings.Join(cmd, " ")).To(Equal("claude --worktree feature-x --model opus --resume " + rsConv + " --name fix the tests --effort high"))
			Expect(labelsOf(f.launched().Args)).To(HaveKeyWithValue("claude-sandbox.instance", "dunlin"))
			Expect(f.errw.String()).To(ContainSubstring("was also launched with --verbose"))
		})

		It("CS-SESS-107: this launch's --model wins; a user --name sets the name once", func() {
			f.model, f.flags = "opus", "--model"
			f.cmd = []string{"claude", "--model", "opus", "--resume", rsConv}
			f.record(263, rsConv, "idle", "derived", "derived", f.proj, f.created.Add(3*time.Second))
			Expect(f.run("--restart=dunlin", "--model", "sonnet", "--", "--name", "x")).To(Equal(0), f.errw.String())
			s := strings.Join(f.createdCmd(), " ")
			Expect(s).To(Equal("claude --model sonnet --resume " + rsConv + " --name x"))
		})

		It("CS-SESS-107: a command line not in the launcher's shape replays nothing, with a note", func() {
			f.cmd = []string{"bash", "-c", "claude"}
			f.primary("idle")
			Expect(f.run("--restart=dunlin")).To(Equal(0), f.errw.String())
			Expect(f.errw.String()).To(ContainSubstring("not in the shape this launcher writes"))
		})

		It("CS-SESS-107: the new container is in the target's project, its -w the project", func() {
			f.primary("idle")
			Expect(f.run("--restart=dunlin")).To(Equal(0), f.errw.String())
			args := f.launched().Args
			Expect(args[slices.Index(args, "-w")+1]).To(Equal(f.proj))
			Expect(labelsOf(args)).To(HaveKeyWithValue("claude-sandbox.project", f.proj))
		})

		DescribeTable("CS-SESS-107: an option the access label records from a flag or a switch is carried, named with its source",
			func(setup func(f *restartFix), wantArg, wantLine string) {
				f.primary("idle")
				setup(f)
				Expect(f.run("--restart=dunlin")).To(Equal(0), f.errw.String())
				Expect(f.errw.String()).To(ContainSubstring("Carried over from 'dunlin': " + wantLine))
				Expect(strings.Join(f.launched().Args, " ")).To(ContainSubstring(wantArg))
			},
			Entry("a flag source", func(f *restartFix) { f.access = "docker-socket=flag" },
				"/var/run/docker.sock:/var/run/docker.sock", "--docker-socket (given as a flag at its launch)"),
			Entry("a truthy switch source", func(f *restartFix) {
				Expect(os.MkdirAll(filepath.Join(f.home, ".ssh"), 0o700)).To(Succeed())
				f.access = "ssh=env"
			}, "/.ssh:", "--ssh (CLAUDE_SANDBOX_HOST_ACCESS_SSH_ENABLED was on at its launch)"),
			Entry("dangerous from a flag", func(f *restartFix) {
				f.access = "dangerous=flag"
				f.cmd = []string{"claude", "--dangerously-skip-permissions", "--resume", rsConv}
			}, "claude --dangerously-skip-permissions", "--dangerous (given as a flag at its launch)"),
		)

		It("CS-SESS-107: a falsy switch with a planted mount is not carried (the label says nothing was applied)", func() {
			f.primary("idle")
			f.flags = "CLAUDE_SANDBOX_HOST_ACCESS_DOCKER_SOCKET_ENABLED"
			f.access = "none"
			// A session-writable cascade mount at the socket's path: never read for access.
			cfgDir := filepath.Join(f.proj, ".claude-sandbox")
			Expect(os.MkdirAll(cfgDir, 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte("mounts:\n  - host: "+f.proj+"/x\n    container: /var/run/docker.sock\n"), 0o644)).To(Succeed())
			Expect(f.run("--restart=dunlin")).To(Equal(0), f.errw.String())
			Expect(f.errw.String()).NotTo(ContainSubstring("Carried over"))
			Expect(f.errw.String()).NotTo(ContainSubstring("from its config"))
			Expect(strings.Join(f.launched().Args, " ")).NotTo(ContainSubstring("/var/run/docker.sock:/var/run/docker.sock"))
		})

		It("CS-SESS-107: a falsy switch with a planted env-file variable is not carried", func() {
			f.primary("idle")
			f.flags = "CLAUDE_SANDBOX_HOST_ACCESS_PACKAGE_CACHES_ENABLED"
			f.access = "none"
			cfgDir := filepath.Join(f.proj, ".claude-sandbox")
			Expect(os.MkdirAll(cfgDir, 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(cfgDir, "env"), []byte("GOMODCACHE="+filepath.Join(f.home, ".cache", "claude-sandbox", "go-mod")+"\n"), 0o600)).To(Succeed())
			Expect(f.run("--restart=dunlin")).To(Equal(0), f.errw.String())
			Expect(f.errw.String()).NotTo(ContainSubstring("Carried over"))
			Expect(strings.Join(f.launched().Args, " ")).NotTo(ContainSubstring("go-mod:"))
		})

		It("CS-SESS-107: a passthrough --dangerously-skip-permissions first is noted, never carried", func() {
			f.primary("idle")
			f.access = "none"
			f.cmd = []string{"claude", "--dangerously-skip-permissions", "--resume", rsConv}
			Expect(f.run("--restart=dunlin")).To(Equal(0), f.errw.String())
			Expect(f.createdCmd()).NotTo(ContainElement("--dangerously-skip-permissions"))
			Expect(f.errw.String()).To(ContainSubstring("was also launched with --dangerously-skip-permissions"))
			Expect(f.errw.String()).NotTo(ContainSubstring("Carried over"))
		})

		It("CS-SESS-107: a config source today's config dropped gets the note only", func() {
			f.primary("idle")
			f.access = "docker-socket=config"
			Expect(f.run("--restart=dunlin")).To(Equal(0), f.errw.String())
			Expect(f.errw.String()).To(ContainSubstring("Note: 'dunlin' had --docker-socket from its config, which no longer gives it; pass it again to keep it."))
			Expect(f.errw.String()).NotTo(ContainSubstring("Carried over"))
			Expect(strings.Join(f.launched().Args, " ")).NotTo(ContainSubstring("docker.sock"))
		})

		It("CS-SESS-107: a container without the access label carries nothing and says so once", func() {
			f.primary("idle")
			f.access = ""
			f.flags = "--docker-socket"
			Expect(f.run("--restart=dunlin")).To(Equal(0), f.errw.String())
			Expect(f.errw.String()).To(ContainSubstring("Note: 'dunlin' predates access tracking; pass any access options again."))
			Expect(f.errw.String()).NotTo(ContainSubstring("Carried over"))
			Expect(strings.Join(f.launched().Args, " ")).NotTo(ContainSubstring("docker.sock"))
		})

		It("CS-SESS-107: an option this launch gives on its own is not carried, and nothing is said", func() {
			f.primary("idle")
			f.access = "docker-socket=flag"
			Expect(f.run("--restart=dunlin", "--docker-socket")).To(Equal(0), f.errw.String())
			Expect(f.errw.String()).NotTo(ContainSubstring("Carried over"))
		})
	})

	Describe("CS-LNCH-218: the sequence", func() {
		It("CS-LNCH-218: image work, then the stop, then the removal wait, then the lock, the guard and the create", func() {
			f.primary("idle")
			Expect(f.run("--restart=dunlin")).To(Equal(0), f.errw.String())
			stop := f.index("docker stop --signal SIGTERM --time 30 " + rsOldID)
			Expect(stop).To(BeNumerically(">", f.index("docker buildx")), "the image work runs first")
			Expect(f.lock.acquiredAt).To(HaveLen(1))
			Expect(f.lock.acquiredAt[0]).To(BeNumerically(">", stop), "the stop is outside the lock")
			removal := -1
			for i, l := range f.fake.CommandLines() {
				if i > stop && strings.Contains(l, "{{.Name}}") {
					removal = i
					break
				}
			}
			Expect(removal).To(BeNumerically(">", stop))
			Expect(f.lock.acquiredAt[0]).To(BeNumerically(">", removal))
			Expect(f.index("docker create")).To(BeNumerically(">", f.lock.acquiredAt[0]))
			// No lease on the old container.
			ls, _ := clients.ReadLeases(f.state, rsOldID)
			Expect(ls).To(BeEmpty())
		})

		It("CS-LNCH-218: an end.lock another process holds is polled; gone meanwhile skips the stop", func() {
			f.primary("idle")
			defer func(o, w time.Duration) { sessionend.EndPoll, restartLockWait = o, w }(sessionend.EndPoll, restartLockWait)
			sessionend.EndPoll, restartLockWait = time.Millisecond, time.Hour
			l, held, err := sessionend.TryEndLock(f.state, rsOldID)
			Expect(err).NotTo(HaveOccurred())
			Expect(held).To(BeFalse())
			defer l.Close()
			f.onInspect = func() { f.gone, f.stopped = true, true }
			Expect(f.run("--restart=dunlin")).To(Equal(0), f.errw.String())
			Expect(f.errw.String()).To(ContainSubstring("is being ended by another process"))
			Expect(f.errw.String()).To(ContainSubstring("ended meanwhile"))
			Expect(f.count("docker stop")).To(BeZero())
			Expect(f.count("docker create")).To(Equal(1))
		})

		It("CS-LNCH-218: an end.lock held past the bound: exit 1, nothing stopped", func() {
			f.primary("idle")
			defer func(o, w time.Duration) { sessionend.EndPoll, restartLockWait = o, w }(sessionend.EndPoll, restartLockWait)
			sessionend.EndPoll, restartLockWait = time.Millisecond, 0
			l, _, err := sessionend.TryEndLock(f.state, rsOldID)
			Expect(err).NotTo(HaveOccurred())
			defer l.Close()
			Expect(f.run("--restart=dunlin")).To(Equal(1))
			Expect(f.errw.String()).To(ContainSubstring("still held by another process ending it"))
			Expect(f.count("docker stop")).To(BeZero())
		})

		It("CS-LNCH-218: the removal not done in time: a new noun, with one line", func() {
			f.primary("idle")
			defer func(w, p time.Duration) { restartRemovalWait, restartRemovalPoll = w, p }(restartRemovalWait, restartRemovalPoll)
			restartRemovalWait, restartRemovalPoll = 5*time.Millisecond, time.Millisecond
			f.removing = true
			Expect(f.run("--restart=dunlin")).To(Equal(0), f.errw.String())
			Expect(f.out.String()).To(MatchRegexp(`'dunlin' is still being removed; using '[a-z-]+'\.`))
			Expect(labelsOf(f.launched().Args)["claude-sandbox.instance"]).NotTo(Equal("dunlin"))
			// The worktree stays the session's (shared checkout here).
			Expect(f.createdCmd()).NotTo(ContainElement("--worktree"))
		})

		It("CS-LNCH-218: a create name conflict on the kept noun re-picks with the restart's line", func() {
			f.primary("idle")
			f.create = func(c execx.Cmd, n int) (string, error) {
				if n == 1 {
					c.Stderr.Write([]byte(fmt.Sprintf(`docker: Error response from daemon: Conflict. The container name "/%s" is already in use by container "abc".`, rsName)))
					return "", execx.Fail(125)
				}
				return rsNewID + "\n", nil
			}
			Expect(f.run("--restart=dunlin")).To(Equal(0), f.errw.String())
			Expect(f.out.String()).To(ContainSubstring("'dunlin' is still being removed; using '"))
			Expect(f.errw.String()).NotTo(ContainSubstring("was taken during launch"))
		})
	})

	Describe("CS-LNCH-220: after the stop", func() {
		It("CS-LNCH-220: a launch that resumed the id meanwhile wins: exit 4, the holder, and the way back", func() {
			f.primary("idle")
			f.onStop = func() {
				f.extra = append(f.extra, f.otherRow("claude-sandbox-work-proj-abc123-heron", "heron", "claude", "running", rsConv))
			}
			Expect(f.run("--restart=dunlin")).To(Equal(4))
			out := f.errw.String()
			Expect(out).To(ContainSubstring("is already open in 'heron'"))
			Expect(out).To(ContainSubstring("'dunlin' was stopped; its conversation is now open in the session named above."))
			Expect(out).To(ContainSubstring("Resume it by hand: cd "))
			Expect(f.count("docker create")).To(BeZero())
			es := f.logEntries()
			Expect(es).To(HaveLen(1))
			Expect(es[0].Outcome).To(Equal(sessionend.OutcomeRefusedAfterStop))
			Expect(es[0].Reason).To(Equal(sessionend.ReasonRestart))
		})

		It("CS-LNCH-220: a docker create that fails after the stop prints and logs the resume command, and leaves a notice", func() {
			f.primary("idle")
			f.create = func(execx.Cmd, int) (string, error) { return "", fmt.Errorf("boom") }
			Expect(f.run("--restart=dunlin")).To(Equal(2))
			Expect(f.errw.String()).To(ContainSubstring("Resume it by hand: cd " + f.proj + " && claude-sandbox --new --no-worktree -- --resume " + rsConv))
			es := f.logEntries()
			Expect(es).To(HaveLen(1))
			Expect(es[0].Outcome).To(Equal(sessionend.OutcomeRefusedAfterStop))
			Expect(es[0].Container).To(Equal(rsName))
			ents, _ := os.ReadDir(hostdirs.NoticesDir(f.state))
			Expect(ents).To(HaveLen(1))
			// The next launch in the project prints it once.
			g := newCLIFixture()
			g.env.Prompter = f.env.Prompter
			n := sessionend.ClaimNotices(sessionend.ClaimOptions{StateRoot: f.state, Project: f.proj, Out: g.out, Now: f.now})
			Expect(n).To(Equal(1))
			Expect(g.out.String()).To(ContainSubstring("a restart of 'dunlin' stopped its container but did not resume its conversation"))
		})

		It("CS-LNCH-220: a stop that fails while the container runs creates nothing and puts the marks back", func() {
			f.primary("idle")
			m := tmuxpane.Mark{V: tmuxpane.MarkVersion, State: tmuxpane.StateActive, Mode: tmuxpane.ModeClaude,
				Container: rsName, ContainerID: rsOldID, Instance: "dunlin", Project: f.proj, Worktree: ""}
			f.fake.On("list-panes", "%12\t"+m.JSON()+"\n%13\t\n", nil)
			f.stopErr = execx.Fail(1)
			Expect(f.run("--restart=dunlin")).To(Equal(1))
			Expect(f.errw.String()).To(ContainSubstring("may still be running (state: running)"))
			Expect(f.count("docker create")).To(BeZero())
			Expect(f.fake.CommandLines()).To(ContainElement("tmux set-option -p -t %12 @claude-sandbox " + m.JSON()))
			Expect(f.logEntries()).To(BeEmpty())
		})

		It("CS-LNCH-220: a signal during the stop: the stop and removal finish, no session starts, exit 130", func() {
			f.primary("idle")
			ch := make(chan os.Signal, 2)
			f.env.restartSignals = func() (<-chan os.Signal, func()) { return ch, func() {} }
			f.onStop = func() { ch <- syscall.SIGINT }
			Expect(f.run("--restart=dunlin")).To(Equal(130))
			Expect(f.count("docker create")).To(BeZero())
			Expect(f.fake.Session).To(BeNil())
			Expect(f.errw.String()).To(ContainSubstring("Resume it by hand: "))
			es := f.logEntries()
			Expect(es).To(HaveLen(1))
			Expect(es[0].Outcome).To(Equal(sessionend.OutcomeInterrupted))
		})

		It("CS-LNCH-220: a signal before the stop aborts with nothing stopped", func() {
			f.primary("idle")
			ch := make(chan os.Signal, 2)
			ch <- syscall.SIGTERM
			f.env.restartSignals = func() (<-chan os.Signal, func()) { return ch, func() {} }
			Expect(f.run("--restart=dunlin")).To(Equal(143))
			Expect(f.errw.String()).To(ContainSubstring("nothing was stopped"))
			Expect(f.count("docker stop")).To(BeZero())
		})

		It("CS-LNCH-220: a new session that ends early with a crash code prints the way back and logs start-failed", func() {
			f.primary("idle")
			f.fake.On("docker start -ai", "", execx.Fail(1))
			Expect(f.run("--restart=dunlin")).To(Equal(1))
			out := f.errw.String()
			Expect(out).To(ContainSubstring("the restarted session of 'dunlin' ('fix the tests', " + rsConv + ") ended before it was up (exit 1)"))
			Expect(out).To(ContainSubstring("Resume it by hand: "))
			es := f.logEntries()
			Expect(es).To(HaveLen(2))
			Expect(es[0].Outcome).To(Equal(sessionend.OutcomeRestarted))
			Expect(es[0].NewContainer).To(ContainSubstring("dunlin"))
			Expect(es[1].Outcome).To(Equal(sessionend.OutcomeStartFailed))
		})

		It("CS-LNCH-220: every way back passes the carried access flags: printed, logged and noticed", func() {
			f.primary("idle")
			f.access = "docker-socket=flag"
			f.record(519, rsJoin, "idle", "side quest", "user", f.proj, f.created.Add(10*time.Minute))
			f.tty("y")
			f.create = func(execx.Cmd, int) (string, error) { return "", fmt.Errorf("boom") }
			Expect(f.run("--restart=dunlin")).To(Equal(2))
			want := "claude-sandbox --docker-socket --new --no-worktree -- --resume "
			Expect(f.errw.String()).To(ContainSubstring("Resume it by hand: cd " + f.proj + " && " + want + rsConv))
			Expect(f.errw.String()).To(ContainSubstring(want + rsJoin))
			es := f.logEntries()
			Expect(es).To(HaveLen(1))
			for _, r := range es[0].Resume {
				Expect(r).To(ContainSubstring(want))
			}
			ents, _ := os.ReadDir(hostdirs.NoticesDir(f.state))
			Expect(ents).To(HaveLen(1))
			b, err := os.ReadFile(filepath.Join(hostdirs.NoticesDir(f.state), ents[0].Name()))
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.Count(string(b), "--docker-socket --new")).To(Equal(2))
		})

		It("CS-LNCH-220: a new session that exits 78 before it is up is a start failure", func() {
			f.primary("idle")
			f.fake.On("docker start -ai", "", execx.Fail(78))
			Expect(f.run("--restart=dunlin")).To(Equal(78))
			Expect(f.errw.String()).To(ContainSubstring("ended before it was up (exit 78)"))
			Expect(f.logEntries()[len(f.logEntries())-1].Outcome).To(Equal(sessionend.OutcomeStartFailed))
		})

		It("CS-LNCH-220: a session child docker could not run is a start failure", func() {
			f.primary("idle")
			f.fake.On("docker start -ai", "", fmt.Errorf("exec: docker: not found"))
			Expect(f.run("--restart=dunlin")).To(Equal(2))
			Expect(f.errw.String()).To(ContainSubstring("could not be started"))
			Expect(f.errw.String()).To(ContainSubstring("Resume it by hand: "))
			Expect(f.logEntries()[len(f.logEntries())-1].Outcome).To(Equal(sessionend.OutcomeStartFailed))
		})

		It("CS-LNCH-220: a guard that cannot tell after the stop says so, never 'open in the session named above'", func() {
			f.primary("idle")
			f.onStop = func() { f.failPS = true }
			Expect(f.run("--restart=dunlin")).To(Equal(4))
			Expect(f.errw.String()).To(ContainSubstring("whether its conversation is open elsewhere now cannot be told"))
			Expect(f.errw.String()).NotTo(ContainSubstring("now open in the session named above"))
		})

		It("CS-SESS-108: on the no-record path the re-check does not count the decision's joins again", func() {
			f.resume = rsConv
			f.record(519, rsJoin, "idle", "side quest", "user", f.proj, f.created.Add(10*time.Minute))
			f.tty("y")
			Expect(f.run("--restart=dunlin")).To(Equal(0), f.errw.String())
			Expect(f.asked()).To(HaveLen(1))
		})

		It("CS-LNCH-220: a clean restart logs restarted, with the new container and the turn state", func() {
			f.primary("idle")
			Expect(f.run("--restart=dunlin")).To(Equal(0), f.errw.String())
			es := f.logEntries()
			Expect(es).To(HaveLen(1))
			Expect(es[0].Outcome).To(Equal(sessionend.OutcomeRestarted))
			Expect(es[0].ConversationSource).To(Equal("record"))
			Expect(es[0].TurnState).To(Equal("idle"))
			Expect(es[0].NewInstance).To(Equal("dunlin"))
			ents, _ := os.ReadDir(hostdirs.NoticesDir(f.state))
			Expect(ents).To(BeEmpty())
		})
	})

	Describe("CS-LNCH-221: stand-downs", func() {
		It("CS-LNCH-221: inside a sandbox --restart exits 2 before any docker call", func() {
			f.envmap["CLAUDE_SANDBOX_PROJECT_DIR"] = f.proj
			Expect(f.run("--restart=dunlin")).To(Equal(2))
			Expect(f.errw.String()).To(ContainSubstring("not available inside a sandbox"))
			f.noDocker()
		})

		It("CS-LNCH-221: headless refuses --restart", func() {
			Expect(f.run("headless", "--restart", "--")).To(Equal(2))
			Expect(f.errw.String()).To(ContainSubstring("--restart is not valid with headless"))
			f.noDocker()
		})
	})

	Describe("CS-TMUX-080: the old container's pane marks", func() {
		It("CS-TMUX-080: every mark naming the old container is unset right before the stop; others stay", func() {
			f.primary("idle")
			mine := tmuxpane.Mark{V: tmuxpane.MarkVersion, State: tmuxpane.StateActive, Mode: tmuxpane.ModeClaude,
				Container: rsName, ContainerID: rsOldID, Instance: "dunlin", Project: f.proj, Worktree: ""}
			other := mine
			other.ContainerID, other.Container = strings.Repeat("c", 64), "other"
			f.fake.On("list-panes", "%12\t"+mine.JSON()+"\n%14\t"+other.JSON()+"\n", nil)
			Expect(f.run("--restart=dunlin")).To(Equal(0), f.errw.String())
			unset := f.index("tmux set-option -p -u -t %12 @claude-sandbox")
			Expect(unset).To(BeNumerically(">=", 0))
			Expect(unset).To(BeNumerically("<", f.index("docker stop")))
			Expect(f.index("tmux set-option -p -u -t %14")).To(Equal(-1))
		})

		It("CS-TMUX-080: the new pane's mark resumes the conversation: a pending mark naming it gets no note", func() {
			f.primary("idle")
			f.envmap["TMUX"] = "/tmp/tmux-1000/default,1,0"
			f.envmap["TMUX_PANE"] = "%7"
			pending := tmuxpane.Mark{V: tmuxpane.MarkVersion, State: tmuxpane.StatePending, Mode: tmuxpane.ModeClaude,
				Container: rsName, ContainerID: rsOldID, Instance: "dunlin", Project: f.proj, Worktree: "",
				Conversation: rsConv, Name: "fix the tests", NameSource: "user"}
			f.fake.On("show-options -p -q -v -t %7", pending.JSON()+"\n", nil)
			Expect(f.run("--restart=dunlin")).To(Equal(0), f.errw.String())
			Expect(f.errw.String()).NotTo(ContainSubstring("waiting to restore"))
			Expect(f.index("tmux set-option -p -t %7 @claude-sandbox")).To(BeNumerically(">", f.index("docker create")))
		})

		It("CS-TMUX-080: a failed stop puts a mark back only on a pane still unset", func() {
			f.primary("idle")
			m := tmuxpane.Mark{V: tmuxpane.MarkVersion, State: tmuxpane.StateActive, Mode: tmuxpane.ModeClaude,
				Container: rsName, ContainerID: rsOldID, Instance: "dunlin", Project: f.proj, Worktree: ""}
			f.fake.On("list-panes", "%12\t"+m.JSON()+"\n", nil)
			f.fake.On("show-options -p -q -v -t %12", `{"v":1,"state":"active","mode":"claude","container":"new","project":"/x","since":1,"worktree":""}`+"\n", nil)
			f.stopErr = execx.Fail(1)
			Expect(f.run("--restart=dunlin")).To(Equal(1))
			Expect(f.index("tmux set-option -p -t %12")).To(Equal(-1))
		})
	})
})
