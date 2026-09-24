package main

// Spec: spec/sessions.feature CS-SESS-061..063 — the OOM marker in
// `claude-sandbox sessions` and the note before attach or join, end to end
// through MainWithEnv. State.OOMKilled comes from a scripted batched
// "docker inspect".

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
)

// oomInspect is the pattern of the batched OOM inspect.
const oomInspect = "{{.State.OOMKilled}}"

// psRowLimit is psRow plus the create-time memoryLimit labels (CS-LNCH-093).
func psRowLimit(name, project, instance, limit, source string) string {
	return strings.Join([]string{name, "Up 1 hour", project, "claude", instance, "v1", "", "", "", "", "",
		"running", "", limit, source}, psSep)
}

var _ = Describe("the OOM marker (CS-SESS-061..063)", func() {
	var f *cliFixture

	BeforeEach(func() { f = newCLIFixture() })

	running := func(rows ...string) {
		f.fake.On("docker ps", strings.Join(rows, "\n")+"\n", nil)
		f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
	}
	inspects := func() []string {
		var out []string
		for _, l := range f.fake.CommandLines() {
			if strings.HasPrefix(l, "docker inspect") && strings.Contains(l, oomInspect) {
				out = append(out, l)
			}
		}
		return out
	}

	Describe("the sessions subcommand", func() {
		It("CS-SESS-061: one batched inspect marks the OOM-killed container's SESSIONS column", func() {
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"), psRow("cs-b", "Up 2 hours", f.proj, "heron"))
			f.fake.On(oomInspect, "/cs-a true\n/cs-b false\n", nil)
			Expect(f.run("sessions")).To(Equal(0))
			Expect(inspects()).To(Equal([]string{
				"docker inspect --type container --format {{.Name}} {{.State.OOMKilled}} cs-a cs-b",
			}))
			var otter, heron string
			for _, l := range strings.Split(f.out.String(), "\n") {
				if strings.HasPrefix(l, "otter") {
					otter = l
				}
				if strings.HasPrefix(l, "heron") {
					heron = l
				}
			}
			Expect(otter).To(HaveSuffix("1 (OOM)"))
			Expect(heron).NotTo(ContainSubstring("(OOM)"))
			Expect(f.out.String()).To(ContainSubstring("(OOM) = a process in this container was killed by the OOM killer: its memoryLimit or the host running out of memory"))
		})

		It("CS-SESS-061: --all marks too, and the legend is absent when nothing is marked", func() {
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"), psRow("cs-b", "Up 3 days", "/elsewhere", "heron"))
			f.fake.On(oomInspect, "/cs-a false\n/cs-b false\n", nil)
			Expect(f.run("sessions", "--all")).To(Equal(0))
			Expect(inspects()).To(HaveLen(1))
			Expect(f.out.String()).NotTo(ContainSubstring("OOM"))
		})

		It("CS-SESS-061: --json carries oomKilled", func() {
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"))
			f.fake.On(oomInspect, "/cs-a true\n", nil)
			Expect(f.run("sessions", "--json")).To(Equal(0))
			Expect(f.out.String()).To(ContainSubstring(`"oomKilled": true`))
		})

		It("CS-SESS-061: nothing listed runs no inspect", func() {
			running()
			Expect(f.run("sessions")).To(Equal(0))
			Expect(inspects()).To(BeEmpty())
		})

		It("CS-SESS-062: a failing inspect leaves the listing unmarked, with no error", func() {
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"))
			f.fake.On(oomInspect, "", execx.Fail(1))
			Expect(f.run("sessions")).To(Equal(0))
			Expect(f.out.String()).To(ContainSubstring("otter"))
			Expect(f.out.String()).NotTo(ContainSubstring("OOM"))
			Expect(f.errw.String()).To(BeEmpty())
		})

		It("CS-SESS-062: a partial failure keeps the marks docker printed", func() {
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"), psRow("cs-b", "Up 2 hours", f.proj, "heron"))
			f.fake.On(oomInspect, "/cs-a true\n", execx.Fail(1))
			Expect(f.run("sessions")).To(Equal(0))
			Expect(f.out.String()).To(ContainSubstring("1 (OOM)"))
			Expect(strings.Count(f.out.String(), "1 (OOM)")).To(Equal(1))
			Expect(f.errw.String()).To(BeEmpty())
		})
	})

	Describe("the note before attach or join", func() {
		const note = "Note: an earlier process in this container (session 'otter') was killed by the OOM killer (the container's memoryLimit or the host running out of memory); memoryLimit: "

		It("CS-SESS-063: --attach notes the kill with the create-time limit and proceeds", func() {
			running(psRowLimit("cs-a", f.proj, "otter", "16g", "/ws/.claude-sandbox/config.yaml"))
			f.fake.On(oomInspect, "/cs-a true\n", nil)
			Expect(f.run("--attach=otter")).To(Equal(0))
			Expect(f.errw.String()).To(ContainSubstring(note + "16g (from /ws/.claude-sandbox/config.yaml).\n"))
			Expect(strings.Count(f.errw.String(), "killed by the OOM killer")).To(Equal(1))
			Expect(inspects()).To(Equal([]string{"docker inspect --type container --format {{.Name}} {{.State.OOMKilled}} cs-a"}))
			Expect(f.sessionLine()).To(Equal("docker attach --detach-keys=ctrl-q,ctrl-q cs-a"))
		})

		It("CS-SESS-063: --join notes it too, and a container without the labels says so", func() {
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"))
			f.fake.On(oomInspect, "/cs-a true\n", nil)
			Expect(f.run("--join=otter")).To(Equal(0))
			Expect(f.errw.String()).To(ContainSubstring(note + "not recorded on this container.\n"))
			Expect(f.sessionLine()).To(HavePrefix("docker exec -it "))
		})

		It("CS-SESS-063: a container without an instance label is named by its mode", func() {
			running(psRow("cs-a", "Up 1 hour", f.proj, ""))
			f.fake.On(oomInspect, "/cs-a true\n", nil)
			Expect(f.run("--attach")).To(Equal(0))
			Expect(f.errw.String()).To(ContainSubstring("(session 'claude') was killed by the OOM killer"))
			Expect(f.errw.String()).NotTo(ContainSubstring("(session '')"))
		})

		It("CS-SESS-063: the default limit is named as the default", func() {
			running(psRowLimit("cs-a", f.proj, "otter", "8g", "default"))
			f.fake.On(oomInspect, "/cs-a true\n", nil)
			Expect(f.run("--attach")).To(Equal(0))
			Expect(f.errw.String()).To(ContainSubstring(note + "8g (the default; no config.yaml in the cascade sets it).\n"))
		})

		It("CS-SESS-063: the session decision's [a] notes it without an extra prompt", func() {
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"))
			f.fake.On(oomInspect, "/cs-a true\n", nil)
			scripted := &prompt.Scripted{IsTTY: true, Answers: []string{"a"}}
			f.env.Prompter = scripted
			Expect(f.run()).To(Equal(0))
			Expect(f.errw.String()).To(ContainSubstring(note))
			Expect(f.sessionLine()).To(HavePrefix("docker attach "))
		})

		It("CS-SESS-063: nothing is printed when OOMKilled is false or the inspect fails", func() {
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"))
			f.fake.On(oomInspect, "/cs-a false\n", nil)
			Expect(f.run("--attach=otter")).To(Equal(0))
			Expect(f.errw.String()).NotTo(ContainSubstring("OOM killer"))

			g := newCLIFixture()
			g.fake.On("docker ps", psRow("cs-a", "Up 1 hour", g.proj, "otter")+"\n", nil)
			g.fake.On(oomInspect, "", execx.Fail(1))
			Expect(g.run("--join=otter")).To(Equal(0))
			Expect(g.errw.String()).NotTo(ContainSubstring("OOM killer"))
			Expect(g.sessionLine()).To(HavePrefix("docker exec -it "))
		})

		It("CS-SESS-063: without a terminal the decision still exits 3, and nothing is inspected", func() {
			running(psRow("cs-a", "Up 1 hour", f.proj, "otter"))
			f.fake.On(oomInspect, "/cs-a true\n", nil)
			f.env.Prompter = &prompt.Scripted{IsTTY: false}
			Expect(f.run()).To(Equal(3))
			Expect(f.fake.Session).To(BeNil())
			Expect(inspects()).To(BeEmpty())
			Expect(f.errw.String()).NotTo(ContainSubstring("OOM killer"))
		})
	})
})
