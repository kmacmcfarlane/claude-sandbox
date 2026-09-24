package main

// Spec: spec/launch.feature CS-LNCH-113..118 and spec/sessions.feature
// CS-SESS-028/036 — "--detach": the container is created as for an attached
// launch, started with a plain "docker start", and the launcher exits 0 with
// the command that attaches. Everything runs through execx.Fake; the fixture's
// TempRoot and CacheDir are scratch directories.

import (
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
)

// startCalls returns every recorded "docker start" argv.
func startCalls(f *cliFixture) [][]string {
	var out [][]string
	for _, c := range f.fake.Calls {
		if c.Name == "docker" && len(c.Args) > 0 && c.Args[0] == "start" {
			out = append(out, c.Args)
		}
	}
	return out
}

var _ = Describe("detached launch (CS-LNCH-113..118)", func() {
	var f *cliFixture
	BeforeEach(func() { f = newCLIFixture() })

	It("CS-LNCH-113: creates as an attached launch does, then starts without attaching and prints the attach command", func() {
		Expect(f.run("--detach")).To(Equal(0), f.errw.String())

		Expect(f.launchLine()).To(HavePrefix("docker create -it --rm --init "), "-t kept for the later attach")
		name := nameOf(f.launched().Args)
		Expect(startCalls(f)).To(Equal([][]string{{"start", name}}), "no -a, no -i, no --detach-keys")
		Expect(f.fake.Session).To(BeNil(), "no session child")
		Expect(f.fake.CommandLines()).NotTo(ContainElement(HavePrefix("docker events ")), "no OOM watcher")

		instance := labelValue(f.launched().Args, "claude-sandbox.instance")
		Expect(instance).NotTo(BeEmpty())
		Expect(f.out.String()).To(HaveSuffix(
			"Started '" + instance + "' (" + name + ") in the background.\n" +
				"Attach: claude-sandbox --attach=" + instance + "   (from " + f.proj + "; detach again with ctrl-q,ctrl-q)\n"))
	})

	It("CS-LNCH-113: the hint names the configured detach keys; docker's own start output is not shown", func() {
		writeFile(f.proj+"/.claude-sandbox/config.yaml", "detachKeys: ctrl-^\n")
		f.fake.OnFunc("docker start", func(c execx.Cmd) (string, error) { return c.Args[len(c.Args)-1] + "\n", nil })
		Expect(f.run("--detach")).To(Equal(0), f.errw.String())
		Expect(f.out.String()).To(ContainSubstring("detach again with ctrl-^)"))
		Expect(strings.Count(f.out.String(), nameOf(f.launched().Args))).To(Equal(1), "only in the Started line")
	})

	It("CS-LNCH-113: an initial prompt reaches the container's claude command", func() {
		Expect(f.run("--detach", "--", "/librarian-mode start")).To(Equal(0), f.errw.String())
		Expect(createTail(f.launched().Args)).To(ContainElement("/librarian-mode start"))
	})

	It("CS-LNCH-114, CS-SESS-028: needs no terminal and implies --new with sessions running", func() {
		f.fake.On("docker ps", psRow("cs-a", "Up 1 hour", f.proj, "otter")+"\n", nil)
		f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
		f.env.Prompter = &prompt.Scripted{IsTTY: false}
		Expect(f.run("--detach")).To(Equal(0), f.errw.String())
		Expect(f.launchLine()).To(HavePrefix("docker create "))
		Expect(nameOf(f.launched().Args)).NotTo(HaveSuffix("-otter"), "a new container, not the running one")
		Expect(f.errw.String()).NotTo(ContainSubstring("already running"))
	})

	DescribeTable("CS-LNCH-115: refused combinations exit 2 before any docker command",
		func(args []string, msg string) {
			Expect(f.run(args...)).To(Equal(2))
			Expect(f.errw.String()).To(ContainSubstring(msg))
			Expect(f.fake.CommandLines()).NotTo(ContainElement(HavePrefix("docker ")))
		},
		Entry("--ralph", []string{"--detach", "--ralph"}, "--detach is not valid with --ralph"),
		Entry("--attach", []string{"--detach", "--attach"}, "--detach conflicts with --attach"),
		Entry("--attach=N", []string{"--attach=otter", "--detach"}, "--detach conflicts with --attach"),
		Entry("--join=N", []string{"--detach", "--join=otter"}, "--detach conflicts with --join"),
		Entry("--branch", []string{"--detach", "--branch"}, "--detach is not valid with --branch"),
		Entry("headless", []string{"headless", "--detach", "--"}, "--detach is not valid with headless"),
	)

	It("CS-LNCH-116: a docker start that fails removes the reservation, then the shadow directory, and exits with docker's status", func() {
		f.fake.On("docker start", "", execx.Fail(125))
		Expect(f.run("--detach")).To(Equal(125))
		name := nameOf(f.launched().Args)
		Expect(f.fake.CommandLines()).To(ContainElement("docker rm " + name))
		Expect(exists(shadowDirOf(f.launched().Args))).To(BeFalse())
		Expect(f.out.String()).NotTo(ContainSubstring("Attach:"))
	})

	It("CS-LNCH-116: a start that never ran the container is cleaned up and exits 1", func() {
		f.fake.On("docker inspect --type container", "created 2026-09-24T00:00:00Z\n", nil)
		Expect(f.run("--detach")).To(Equal(1))
		name := nameOf(f.launched().Args)
		Expect(f.fake.CommandLines()).To(ContainElement("docker rm " + name))
		Expect(exists(shadowDirOf(f.launched().Args))).To(BeFalse())
		Expect(f.errw.String()).To(ContainSubstring("never ran"))
		Expect(f.out.String()).NotTo(ContainSubstring("Attach:"))
	})

	It("CS-LNCH-116: the shadow directory stays when the reservation cannot be removed", func() {
		f.fake.On("docker start", "", execx.Fail(1))
		f.fake.On("docker rm", "", execx.Fail(1))
		Expect(f.run("--detach")).To(Equal(1))
		Expect(exists(shadowDirOf(f.launched().Args))).To(BeTrue(), "a container that still exists may mount it")
	})

	It("CS-LNCH-117: the shadow directory is kept at launch and swept by a later launch once the container is gone", func() {
		f.fake.On("docker inspect --type container", "running 2026-09-24T00:00:00Z\n", nil)
		Expect(f.run("--detach")).To(Equal(0), f.errw.String())
		dir := shadowDirOf(f.launched().Args)
		Expect(dir).NotTo(BeEmpty())
		Expect(exists(dir)).To(BeTrue(), "the running container mounts it")
		Expect(f.errw.String()).NotTo(ContainSubstring("OOM"))

		// An hour later the --rm container has exited and been removed: no
		// container names the directory (the fake's docker ps lists none).
		f.env.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
		Expect(f.run("--detach")).To(Equal(0), f.errw.String())
		Expect(exists(dir)).To(BeFalse(), "a later launch's sweep removed it")
	})

	It("CS-LNCH-118: the detached label is set only on detached launches; the mode stays claude and the config hash is unchanged", func() {
		Expect(f.run("--detach")).To(Equal(0), f.errw.String())
		detached := f.launched().Args
		Expect(labelValue(detached, launch.LabelDetached)).To(Equal("1"))
		Expect(labelValue(detached, "claude-sandbox.mode")).To(Equal("claude"))

		g := newCLIFixture()
		g.proj, g.envmap["PROJECT_DIR"] = f.proj, f.proj
		g.home, g.envmap["HOME"] = f.home, f.home
		Expect(g.run()).To(Equal(0), g.errw.String())
		attached := g.launched().Args
		Expect(attached).NotTo(ContainElement(HavePrefix(launch.LabelDetached + "=")))
		Expect(labelValue(detached, "claude-sandbox.confighash")).To(Equal(labelValue(attached, "claude-sandbox.confighash")))
	})

	It("CS-SESS-036: a detached start carries no detach keys; the later attach does", func() {
		Expect(f.run("--detach")).To(Equal(0), f.errw.String())
		for _, a := range startCalls(f) {
			Expect(strings.Join(a, " ")).NotTo(ContainSubstring("--detach-keys"))
		}
		g := newCLIFixture()
		g.fake.On("docker ps", psRow("cs-a", "Up 1 hour", g.proj, "otter")+"\n", nil)
		g.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
		Expect(g.run("--attach=otter")).To(Equal(0), g.errw.String())
		Expect(g.sessionLine()).To(Equal("docker attach --detach-keys=ctrl-q,ctrl-q cs-a"))
	})
})
