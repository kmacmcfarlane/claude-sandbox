package main

// Spec: spec/global-config.feature CS-GCFG-031/032 and 038..040 — every
// launch path carries the linked layout, attach sees a layout change as
// drift, and a session that exits 78 gets the host-side explanation.
// execx.Fake throughout; HOME is the fixture's scratch home.

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/globalcfg"
	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
)

var _ = Describe("global config (CS-GCFG)", func() {
	var f *cliFixture
	var link, target string
	BeforeEach(func() {
		f = newCLIFixture()
		link = filepath.Join(f.home, ".claude.json")
		target = filepath.Join(f.home, ".claude", ".claude.json")
	})
	makeLinked := func() {
		writeFile(target, "{}")
		Expect(os.Symlink(".claude/.claude.json", link)).To(Succeed())
	}
	expectLinkedCreate := func(what string) {
		line := f.launchLine()
		Expect(line).To(ContainSubstring("-e "+globalcfg.EnvVar+"="+target+" "), what)
		Expect(line).NotTo(ContainSubstring(".claude.json:"), what)
	}

	Describe("CS-GCFG-032: every launch path", func() {
		BeforeEach(makeLinked)

		It("CS-GCFG-032: interactive, --branch and ralph create with the variable and no mount", func() {
			for _, args := range [][]string{{}, {"--branch"}, {"--ralph"}} {
				g := newCLIFixture()
				f = g
				link = filepath.Join(g.home, ".claude.json")
				target = filepath.Join(g.home, ".claude", ".claude.json")
				makeLinked()
				Expect(g.run(args...)).To(Equal(0), g.errw.String())
				expectLinkedCreate(strings.Join(args, " "))
			}
		})

		It("CS-GCFG-032: headless creates with the variable and no mount", func() {
			Expect(f.run("headless", "--")).To(Equal(0), f.errw.String())
			expectLinkedCreate("headless")
		})

		It("CS-GCFG-032: --detach creates with the variable and no mount", func() {
			saved := detachedSettle
			detachedSettle = 20 * time.Millisecond
			DeferCleanup(func() { detachedSettle = saved })
			f.fake.On("docker inspect --type container", "running 2026-09-24T00:00:00Z\n", nil)
			Expect(f.run("--detach")).To(Equal(0), f.errw.String())
			expectLinkedCreate("detached")
		})

		It("CS-GCFG-032: a join runs claude through pidslot, which inherits the variable from the container", func() {
			f.fake.On("docker ps", psRow("cs-a", "Up 1 hour", f.proj, "otter")+"\n", nil)
			f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
			Expect(f.run("--join=otter", "--allow-config-drift")).To(Equal(0), f.errw.String())
			line := f.sessionLine()
			Expect(line).To(ContainSubstring("/opt/claude-sandbox/bin/claude-sandbox pidslot -- claude"))
			Expect(line).NotTo(ContainSubstring(globalcfg.EnvVar), "inherited via Config.Env, not re-set")
		})
	})

	Describe("CS-GCFG-031: attach after a layout change", func() {
		It("CS-GCFG-031: a legacy container is drift after a migrate, and a linked one after a revert", func() {
			writeFile(link, "{}")
			legacyHash := currentHash(f)
			// migrate by hand
			Expect(os.MkdirAll(filepath.Dir(target), 0o755)).To(Succeed())
			Expect(os.Rename(link, target)).To(Succeed())
			Expect(os.Symlink(".claude/.claude.json", link)).To(Succeed())
			linkedHash := currentHash(f)
			Expect(linkedHash).NotTo(Equal(legacyHash))

			f.fake.On("docker ps", psRowFull("cs-a", "Up 1 hour", f.proj, "otter", "", legacyHash, "[]")+"\n", nil)
			f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
			f.env.Prompter = &prompt.Scripted{IsTTY: false}
			Expect(f.run("--attach=otter")).To(Equal(3))
			Expect(f.errw.String()).To(ContainSubstring("different configuration"))

			// revert by hand: the linked container is now the stale one
			Expect(os.Remove(link)).To(Succeed())
			Expect(os.Rename(target, link)).To(Succeed())
			Expect(currentHash(f)).NotTo(Equal(linkedHash))
			g := newCLIFixture()
			g.home, g.envmap["HOME"] = f.home, f.home
			g.fake.On("docker ps", psRowFull("cs-a", "Up 1 hour", g.proj, "otter", "", linkedHash, "[]")+"\n", nil)
			g.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
			g.env.Prompter = &prompt.Scripted{IsTTY: false}
			Expect(g.run("--attach=otter")).To(Equal(3))
			Expect(g.errw.String()).To(ContainSubstring("different configuration"))
		})
	})

	Describe("exit 78 (CS-GCFG-038..040)", func() {
		expectMessage := func(w string, container string) {
			Expect(w).To(ContainSubstring("The session exited with 78, likely the global-config link check"))
			Expect(w).NotTo(ContainSubstring("docker rm "+container), "every container is --rm")
			Expect(strings.Count(w, "likely the global-config link check")).To(Equal(1))
		}

		It("CS-GCFG-038, CS-GCFG-039: a new container's session exiting 78, with no output, gets the explanation and exit 78", func() {
			f.fake.On("docker start", "", execx.Fail(78))
			f.fake.On("docker inspect --type container", "exited 2026-09-24T00:00:00Z\n", nil)
			Expect(f.run()).To(Equal(78))
			expectMessage(f.errw.String(), nameOf(f.launched().Args))
		})

		It("CS-GCFG-038: headless puts it on stderr, never stdout", func() {
			f.fake.On("docker start", "", execx.Fail(78))
			f.fake.On("docker inspect --type container", "exited 2026-09-24T00:00:00Z\n", nil)
			Expect(f.run("headless", "--")).To(Equal(78))
			expectMessage(f.errw.String(), nameOf(f.launched().Args))
			Expect(f.out.String()).NotTo(ContainSubstring("likely the global-config"))
		})

		It("CS-GCFG-038: a join exiting 78 gets it too", func() {
			f.fake.On("docker ps", psRow("cs-a", "Up 1 hour", f.proj, "otter")+"\n", nil)
			f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
			f.fake.On("docker exec", "", execx.Fail(78))
			Expect(f.run("--join=otter", "--allow-config-drift")).To(Equal(78))
			expectMessage(f.errw.String(), "cs-a")
		})

		It("CS-GCFG-038: an attach whose session ends 78 gets it too", func() {
			f.fake.On("docker ps", psRow("cs-a", "Up 1 hour", f.proj, "otter")+"\n", nil)
			f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
			f.fake.On("docker attach", "", execx.Fail(78))
			Expect(f.run("--attach=otter", "--allow-config-drift")).To(Equal(78))
			expectMessage(f.errw.String(), "cs-a")
		})

		It("CS-GCFG-038: any other status prints nothing of the kind", func() {
			f.fake.On("docker start", "", execx.Fail(1))
			f.fake.On("docker inspect --type container", "exited 2026-09-24T00:00:00Z\n", nil)
			Expect(f.run()).To(Equal(1))
			Expect(f.errw.String()).NotTo(ContainSubstring("global-config link"))
		})

		It("CS-GCFG-040: a detached container dying 78 in the settle explains it before the error, exit 1", func() {
			saved := detachedSettle
			detachedSettle = 200 * time.Millisecond
			DeferCleanup(func() { detachedSettle = saved })
			f.fake.OnFunc("docker events", func(c execx.Cmd) (string, error) {
				name := ""
				for _, a := range c.Args {
					if v, ok := strings.CutPrefix(a, "container="); ok {
						name = v
					}
				}
				return strings.ReplaceAll(dockerEvent("die", "78"), thisContainer, name), nil
			})
			Expect(f.run("--detach")).To(Equal(1))
			w := f.errw.String()
			expectMessage(w, nameOf(f.launched().Args))
			Expect(strings.Index(w, "likely the global-config")).To(BeNumerically("<", strings.Index(w, "stopped right after it started (exit 78)")))
		})
	})
})
