package main

// Spec: spec/sessions.feature (CS-SESS-048..054) and spec/launch.feature
// (CS-LNCH-057) — every new container is reserved with docker create inside
// the host launch lock, then started with docker start -ai. The fixture's
// fakeLock records where in the fake runner's call log the lock was taken and
// released, so these tests assert what ran inside the critical section.

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
)

// reservedRow is a ps row carrying the optional State and CreatedAt fields.
func reservedRow(name, project, instance, class, state string, created time.Time) string {
	status := "Up 1 hour"
	if state == "created" {
		status = "Created"
	}
	return strings.Join([]string{name, status, project, "claude", instance, "v1", "", "", "", class, "",
		state, created.Format("2006-01-02 15:04:05 -0700 MST")}, psSep)
}

// conflictOn makes "docker create" fail with docker's name-conflict message for
// the first n calls (n < 0: every call), and counts the attempts.
func conflictOn(f *cliFixture, n int) *int {
	calls := 0
	f.fake.OnFunc("docker create", func(c execx.Cmd) (string, error) {
		calls++
		if n < 0 || calls <= n {
			c.Stderr.Write([]byte(`docker: Error response from daemon: Conflict. The container name "/x" is already in use by container "abc".`))
			return "", execx.Fail(125)
		}
		return "abc123\n", nil
	})
	return &calls
}

func nameOf(args []string) string {
	for i, a := range args {
		if a == "--name" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func createCount(f *cliFixture) int {
	n := 0
	for _, l := range f.fake.CommandLines() {
		if strings.HasPrefix(l, "docker create ") {
			n++
		}
	}
	return n
}

var _ = Describe("launch reservation (CS-SESS-048..054, CS-LNCH-057)", func() {
	var f *cliFixture
	BeforeEach(func() { f = newCLIFixture() })

	It("CS-LNCH-057, CS-SESS-048, CS-LNCH-087: discovery, pick and create run under the lock; events and start come after release", func() {
		Expect(f.run()).To(Equal(0), f.errw.String())

		held := f.lock.held()
		Expect(held).NotTo(BeEmpty())
		Expect(held[0]).To(HavePrefix("docker ps -a "), "discovery is the first thing under the lock")
		Expect(held[len(held)-1]).To(HavePrefix("docker create -it --rm --init "), "create is the last")

		// The session child comes after the release, and names the created
		// container; the events subscription comes between the two.
		Expect(f.fake.Session).NotTo(BeNil())
		lines := f.fake.CommandLines()
		Expect(lines[len(lines)-1]).To(HavePrefix("docker start "))
		Expect(lines[len(lines)-2]).To(HavePrefix("docker events "))
		Expect(f.lock.releasedAt[0]).To(Equal(len(lines)-2), "released after the create, before the subscription and the start")
		name := nameOf(f.launched().Args)
		Expect(name).NotTo(BeEmpty())
		Expect(f.sessionLine()).To(Equal("docker start -ai --detach-keys=ctrl-q,ctrl-q " + name))
	})

	It("CS-LNCH-057: ralph, --branch and the [b] fork are reserved and started the same way", func() {
		cases := []struct {
			args []string
			tty  []string
		}{
			{args: []string{"--ralph"}},
			{args: []string{"--branch"}},
			{tty: []string{"b"}},
		}
		for _, c := range cases {
			g := newCLIFixture()
			g.fake.On("docker ps", psRow("cs-a", "Up 1 hour", g.proj, "otter")+"\n", nil)
			g.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
			g.env.Prompter = &prompt.Scripted{IsTTY: c.tty != nil, Answers: c.tty}
			Expect(g.run(c.args...)).To(Equal(0), "%v: %s", c.args, g.errw.String())
			Expect(g.lock.held()).To(ContainElement(HavePrefix("docker create -it --rm --init ")))
			Expect(g.sessionLine()).To(Equal("docker start -ai --detach-keys=ctrl-q,ctrl-q "+nameOf(g.launched().Args)), "%v", c.args)
		}
	})

	It("CS-LNCH-057: a failed start exec removes the reservation before reporting", func() {
		g := newCLIFixture()
		failing := &execFailRunner{Fake: g.fake}
		g.env.Runner = failing
		Expect(g.run()).NotTo(Equal(0))
		name := nameOf(g.launched().Args)
		Expect(g.fake.CommandLines()).To(ContainElement("docker rm " + name))
	})

	It("CS-SESS-049: the lock is never held across image checks or builds", func() {
		// Force every image to be missing, so the base, CLI and cap all build.
		f.fake.On("image inspect", "", execx.Fail(1))
		// Running sandboxes on the host, so a per-container docker top under
		// the lock (CS-SESS-048: discovery there is uncounted) would show up.
		f.fake.On("docker ps", strings.Join([]string{
			reservedRow("cs-a", f.proj, "otter", "1", "running", time.Now()),
			reservedRow("cs-b", "/elsewhere", "heron", "2", "running", time.Now()),
		}, "\n")+"\n", nil)
		f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
		Expect(f.run("--new")).To(Equal(0), f.errw.String())
		lines := f.fake.CommandLines()
		sawBuild := false
		for i, l := range lines {
			if strings.Contains(l, "docker build") || strings.Contains(l, "buildx") || strings.Contains(l, "image inspect") {
				sawBuild = sawBuild || strings.Contains(l, "build")
				Expect(i).To(BeNumerically("<", f.lock.acquiredAt[0]), "image work at %d is inside the lock: %s", i, l)
			}
		}
		Expect(sawBuild).To(BeTrue(), "the test must actually exercise a build")
		for _, l := range f.lock.held() {
			Expect(l).To(Or(HavePrefix("docker ps "), HavePrefix("docker rm "), HavePrefix("docker create ")),
				"only discovery, stale cleanup and create run under the lock")
		}
	})

	It("CS-SESS-050: discovery under the lock sees created reservations across the host", func() {
		Expect(f.run()).To(Equal(0))
		held := strings.Join(f.lock.held(), "\n")
		Expect(held).To(ContainSubstring("docker ps -a --filter label=claude-sandbox.project --filter status=created --filter status=running "))
	})

	It("CS-SESS-050, CS-PID-004: a reservation's pid class is in use for the next launch", func() {
		rows := make([]string, 0, 255)
		for k := 0; k < 255; k++ {
			rows = append(rows, reservedRow("other", "/elsewhere", "", strconv.Itoa(k), "created", time.Now()))
		}
		f.fake.On("docker ps", strings.Join(rows, "\n")+"\n", nil)
		Expect(f.run("--no-session-check")).To(Equal(0), f.errw.String())
		Expect(f.launched().Args).To(ContainElement("claude-sandbox.pidclass=255"))
	})

	It("CS-SESS-051: a reservation is never a session candidate or a listed session", func() {
		row := reservedRow("cs-r", f.proj, "otter", "4", "created", time.Now())
		f.fake.On("docker ps", row+"\n", nil)
		f.env.Prompter = &prompt.Scripted{IsTTY: false}

		// No decision is needed (it would exit 3 without a terminal): the only
		// container is a reservation.
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(f.errw.String()).NotTo(ContainSubstring("running session"))
		Expect(nameOf(f.launched().Args)).NotTo(HaveSuffix("-otter"), "its noun is still in use")
		for _, l := range f.fake.CommandLines() {
			Expect(l).NotTo(HavePrefix("docker top cs-r"), "nothing to count in a container that never started")
		}

		g := newCLIFixture()
		g.fake.On("docker ps", strings.Replace(row, f.proj, g.proj, 1)+"\n", nil)
		g.env.Prompter = &prompt.Scripted{IsTTY: false}
		Expect(g.run("--attach=otter")).To(Equal(2))
		Expect(g.errw.String()).To(ContainSubstring("no running sessions"))
		Expect(g.fake.Session).To(BeNil())

		h := newCLIFixture()
		h.fake.On("docker ps", strings.Replace(row, f.proj, h.proj, 1)+"\n", nil)
		Expect(h.run("sessions")).To(Equal(0))
		Expect(h.out.String()).To(ContainSubstring("No running sandbox sessions for this project"))
	})

	It("CS-SESS-052: stale reservations anywhere on the host are removed under the lock; young ones are left", func() {
		now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
		f.env.Now = func() time.Time { return now }
		f.fake.On("docker ps", strings.Join([]string{
			reservedRow("orphan", "/elsewhere", "heron", "7", "created", now.Add(-61*time.Second)),
			reservedRow("inflight", "/elsewhere", "wren", "8", "created", now.Add(-2*time.Second)),
			reservedRow("old-running", "/elsewhere", "lynx", "9", "running", now.Add(-time.Hour)),
		}, "\n")+"\n", nil)
		f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
		Expect(f.run("--no-session-check")).To(Equal(0), f.errw.String())

		held := f.lock.held()
		Expect(held).To(ContainElement("docker rm orphan"))
		Expect(held).NotTo(ContainElement("docker rm inflight"))
		Expect(held).NotTo(ContainElement("docker rm old-running"))
		Expect(f.errw.String()).To(ContainSubstring("Removed stale reservation orphan"))
	})

	It("CS-SESS-053: a create name conflict re-picks the noun and retries", func() {
		calls := conflictOn(f, 1)
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(*calls).To(Equal(2))

		var names []string
		for _, c := range f.fake.Calls {
			if len(c.Args) > 0 && c.Args[0] == "create" {
				names = append(names, nameOf(c.Args))
			}
		}
		Expect(names).To(HaveLen(2))
		Expect(names[1]).NotTo(Equal(names[0]), "the lost noun is not picked again")
		Expect(f.sessionLine()).To(HaveSuffix(" " + names[1]))
		Expect(f.errw.String()).To(ContainSubstring("was taken during launch; picking another instance"))
		// Every attempt runs inside the one critical section.
		Expect(f.lock.acquiredAt).To(HaveLen(1))
	})

	It("CS-SESS-053: gives up after 3 attempts with a clear error", func() {
		conflictOn(f, -1)
		Expect(f.run()).To(Equal(2))
		Expect(createCount(f)).To(Equal(3))
		Expect(f.errw.String()).To(ContainSubstring("gave up after 3 attempts"))
		Expect(f.fake.Session).To(BeNil(), "nothing is started")
		Expect(f.lock.releasedAt).To(HaveLen(1), "the lock is released on failure too")
	})

	It("CS-SESS-053: ralph's fixed name fails on the first conflict", func() {
		conflictOn(f, -1)
		Expect(f.run("--ralph")).To(Equal(2))
		Expect(createCount(f)).To(Equal(1))
		Expect(f.errw.String()).To(ContainSubstring("a ralph container"))
		Expect(f.errw.String()).To(ContainSubstring("already exists for this project"))
	})

	It("CS-SESS-053: ralph reclaims its name from a reservation that never started, once", func() {
		now := time.Date(2026, 9, 18, 18, 3, 30, 0, time.UTC)
		inspect := "docker inspect --type container -f {{.State.Status}} {{.Created}}"
		oldCreated := func(state string) string { return state + " 2026-09-18T18:03:16.123456789Z\n" } // 14 s before now
		calls := conflictOn(f, 1)
		f.env.Now = func() time.Time { return now }
		f.fake.On(inspect, oldCreated("created"), nil)
		Expect(f.run("--ralph")).To(Equal(0), f.errw.String())
		Expect(*calls).To(Equal(2))
		name := nameOf(f.launched().Args)
		held := f.lock.held()
		Expect(held).To(ContainElement(inspect + " " + name))
		Expect(held).To(ContainElement("docker rm " + name))
		Expect(f.errw.String()).To(ContainSubstring("it never started"))
		Expect(f.sessionLine()).To(HaveSuffix(" " + name))

		// A holder that is really running is never removed.
		g := newCLIFixture()
		conflictOn(g, -1)
		g.env.Now = func() time.Time { return now }
		g.fake.On(inspect, oldCreated("running"), nil)
		Expect(g.run("--ralph")).To(Equal(2))
		Expect(createCount(g)).To(Equal(1))
		Expect(g.fake.CommandLines()).NotTo(ContainElement(HavePrefix("docker rm ")))

		// Reclaiming happens once: a second conflict is a real owner.
		h := newCLIFixture()
		conflictOn(h, -1)
		h.env.Now = func() time.Time { return now }
		h.fake.On(inspect, oldCreated("created"), nil)
		Expect(h.run("--ralph")).To(Equal(2))
		Expect(createCount(h)).To(Equal(2))
		Expect(h.errw.String()).To(ContainSubstring("a ralph container"))
	})

	It("CS-SESS-053: a young created ralph holder is never reclaimed — it may be a launch about to start", func() {
		now := time.Date(2026, 9, 18, 18, 3, 20, 0, time.UTC)
		conflictOn(f, -1)
		f.env.Now = func() time.Time { return now }
		// 3.9 s old: the other launcher released the lock and is exec'ing docker start.
		f.fake.On("docker inspect --type container -f {{.State.Status}} {{.Created}}",
			"created 2026-09-18T18:03:16.123456789Z\n", nil)
		Expect(f.run("--ralph")).To(Equal(2))
		Expect(createCount(f)).To(Equal(1))
		Expect(f.fake.CommandLines()).NotTo(ContainElement(HavePrefix("docker rm ")))
		Expect(f.errw.String()).To(ContainSubstring("a ralph container"))
		Expect(f.errw.String()).To(ContainSubstring("already exists for this project"))
		// The message also covers the caller's own failed start, which is
		// not running and clears by itself.
		Expect(f.errw.String()).To(ContainSubstring("reclaimed automatically once it is 10s old"))

		// An unreadable creation time is treated as young, never as old.
		g := newCLIFixture()
		conflictOn(g, -1)
		g.fake.On("docker inspect --type container -f {{.State.Status}} {{.Created}}", "created\n", nil)
		Expect(g.run("--ralph")).To(Equal(2))
		Expect(g.fake.CommandLines()).NotTo(ContainElement(HavePrefix("docker rm ")))
	})

	It("CS-SESS-053: a Conflicting options error is not a name conflict and is not retried", func() {
		f.fake.OnFunc("docker create", func(c execx.Cmd) (string, error) {
			c.Stderr.Write([]byte("docker: Conflicting options: --rm and --restart."))
			return "", execx.Fail(125)
		})
		Expect(f.run()).To(Equal(2))
		Expect(createCount(f)).To(Equal(1))
		Expect(f.errw.String()).To(ContainSubstring("Conflicting options"))
		Expect(f.errw.String()).NotTo(ContainSubstring("already in use"))
	})

	It("CS-SESS-053: any other create failure is reported, not retried", func() {
		f.fake.OnFunc("docker create", func(c execx.Cmd) (string, error) {
			c.Stderr.Write([]byte("docker: Error response from daemon: invalid mount config"))
			return "", execx.Fail(125)
		})
		Expect(f.run()).To(Equal(2))
		Expect(createCount(f)).To(Equal(1))
		Expect(f.errw.String()).To(ContainSubstring("invalid mount config"))
	})

	It("CS-SESS-048: a lock that cannot be taken warns and still launches", func() {
		f.lock.err = errors.New("permission denied")
		Expect(f.run()).To(Equal(0))
		Expect(f.errw.String()).To(ContainSubstring("could not take the launch lock (permission denied)"))
		// CS-SESS-048: the fallback covers names only, and says so.
		Expect(f.errw.String()).To(ContainSubstring("Container names stay unique"))
		Expect(f.errw.String()).To(ContainSubstring("may get the same pid class"))
		Expect(f.fake.Session).NotTo(BeNil())
	})

	Describe("CS-SESS-054: the early noun is re-validated under the lock", func() {
		banner := regexp.MustCompile(`Worktree: (\S+) `)

		// takeEarlyNoun scripts discovery so that, once the lock is held, a
		// concurrent launch appears to own the noun this launch picked early
		// (read back from the worktree banner printed before the build).
		takeEarlyNoun := func(asWorktreeDir bool) *string {
			early := new(string)
			f.fake.OnFunc("docker ps", func(c execx.Cmd) (string, error) {
				if len(f.lock.acquiredAt) == 0 {
					return "", nil
				}
				m := banner.FindStringSubmatch(f.out.String())
				Expect(m).NotTo(BeNil(), "the early banner is printed before the lock")
				*early = m[1]
				if asWorktreeDir {
					Expect(os.MkdirAll(filepath.Join(f.proj, ".claude/worktrees", *early), 0o755)).To(Succeed())
					return "", nil
				}
				return reservedRow("cs-other", f.proj, *early, "3", "created", time.Now()) + "\n", nil
			})
			return early
		}

		It("re-picks a noun a concurrent launch reserved, and re-derives name and worktree", func() {
			gitProject(f)
			early := takeEarlyNoun(false)
			Expect(f.run("--worktree")).To(Equal(0), f.errw.String())

			args := f.launched().Args
			name := nameOf(args)
			Expect(*early).NotTo(BeEmpty())
			Expect(name).NotTo(HaveSuffix("-" + *early))
			picked := name[strings.LastIndex(name, "-")+1:]
			Expect(args).To(ContainElement("claude-sandbox.instance=" + picked))
			Expect(args).To(ContainElement("claude-sandbox.worktree=" + picked))
			Expect(f.launchLine()).To(HaveSuffix(" claude --worktree " + picked))
			Expect(f.out.String()).To(ContainSubstring("Instance '" + *early + "' was taken by a concurrent launch; using '" + picked + "'."))
			Expect(f.out.String()).To(ContainSubstring("Worktree: "+picked+" "), "the banner is printed again")
		})

		It("re-picks when the noun's worktree directory appeared meanwhile", func() {
			gitProject(f)
			early := takeEarlyNoun(true)
			Expect(f.run("--worktree")).To(Equal(0), f.errw.String())
			Expect(nameOf(f.launched().Args)).NotTo(HaveSuffix("-" + *early))
		})

		It("keeps an explicit --worktree=NAME, and ralph keeps ralph", func() {
			gitProject(f)
			early := takeEarlyNoun(false)
			Expect(f.run("--worktree=feature-x")).To(Equal(0), f.errw.String())
			Expect(nameOf(f.launched().Args)).NotTo(HaveSuffix("-" + *early))
			Expect(f.launchLine()).To(HaveSuffix(" claude --worktree feature-x"))

			g := newCLIFixture()
			g.fake.On("rev-parse --show-toplevel", g.proj+"\n", nil)
			g.fake.On("docker ps", reservedRow("cs-other", g.proj, "ralph", "3", "created", time.Now())+"\n", nil)
			Expect(g.run("--ralph")).To(Equal(0), g.errw.String())
			Expect(nameOf(g.launched().Args)).To(HaveSuffix("-ralph"))
			Expect(g.launchLine()).To(HaveSuffix("/opt/claude-sandbox/bin/ralph --worktree ralph"))
		})
	})
})

// execFailRunner wraps the fake so the session child cannot be started, as
// when the docker binary vanished between create and start.
type execFailRunner struct{ *execx.Fake }

func (r *execFailRunner) RunSession(c execx.Cmd) (execx.SessionResult, error) {
	r.Fake.RunSession(c)
	return execx.SessionResult{Code: -1}, errors.New("exec: docker: not found")
}
