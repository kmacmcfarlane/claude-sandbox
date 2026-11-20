package main

// Spec: spec/launch.feature CS-LNCH-241 — end to end through MainWithEnv: a
// cascade mount over the container's home is refused before any image work;
// the home as a host side under another container path launches; an attach
// is not refused.

import (
	"path/filepath"

	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("a cascade mount over the home (CS-LNCH-241)", func() {
	var f *cliFixture
	BeforeEach(func() { f = newCLIFixture() })

	noImageWork := func() {
		Expect(createdAny(f)).To(BeFalse())
		for _, l := range f.fake.CommandLines() {
			Expect(l).NotTo(HavePrefix("docker build"))
			Expect(l).NotTo(HavePrefix("docker image inspect"))
		}
	}

	DescribeTable("CS-LNCH-241: refused with exit 2, naming the entry and the file",
		func(entry func(home string) string, container func(home string) string) {
			cfg := filepath.Join(f.proj, ".claude-sandbox", "config.yaml")
			writeFile(cfg, "mounts:\n"+entry(f.home))
			Expect(f.run()).To(Equal(2))
			Expect(f.errw.String()).To(ContainSubstring("Error: mounts[0] (" + cfg + ") mounts over the container's home (container: " + container(f.home) + "; the home is " + f.home + ")"))
			Expect(f.errw.String()).To(ContainSubstring(`"container: /mnt/host-home"`))
			noImageWork()
		},
		Entry("the home, both sides",
			func(h string) string { return "  - host: " + h + "\n    container: " + h + "\n" },
			func(h string) string { return h }),
		Entry("the home, container defaulted to host (CS-CASC-048)",
			func(h string) string { return "  - host: " + h + "\n" },
			func(h string) string { return h }),
		Entry("the home with a trailing slash",
			func(h string) string { return "  - host: /srv/x\n    container: " + h + "/\n" },
			func(h string) string { return h + "/" }),
		Entry("a parent of the home",
			func(h string) string { return "  - host: /srv/x\n    container: " + filepath.Dir(h) + "\n" },
			func(h string) string { return filepath.Dir(h) }),
		Entry("the root",
			func(string) string { return "  - host: /srv/x\n    container: /\n" },
			func(string) string { return "/" }),
	)

	It("CS-LNCH-241: names the upstream config.yaml that holds the entry", func() {
		upstream := filepath.Join(filepath.Dir(f.proj), ".claude-sandbox", "config.yaml")
		writeFile(upstream, "mounts:\n  - host: "+f.home+"\n    container: "+f.home+"\n")
		Expect(f.run()).To(Equal(2))
		Expect(f.errw.String()).To(ContainSubstring("Error: mounts[0] (" + upstream + ")"))
		noImageWork()
	})

	It("CS-LNCH-241: the home as the host side under another container path launches", func() {
		writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"),
			"mounts:\n  - host: "+f.home+"\n    container: /mnt/host-home\n  - host: "+f.home+"/.ssh\n")
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(f.launchLine()).To(ContainSubstring(f.home + ":/mnt/host-home:ro"))
	})

	It("CS-LNCH-241: an attach is not refused", func() {
		writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"),
			"mounts:\n  - host: "+f.home+"\n    container: "+f.home+"\n")
		hash := currentHash(f)
		f.fake.On("docker ps", psRowFull("cs-a", "Up 1 hour", f.proj, "otter", "", hash, "[]")+"\n", nil)
		f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
		f.env.Prompter = &prompt.Scripted{IsTTY: false}
		Expect(f.run("--attach=otter")).To(Equal(0), f.errw.String())
		Expect(f.errw.String()).NotTo(ContainSubstring("mounts over the container's home"))
		Expect(f.errw.String()).NotTo(ContainSubstring("different configuration"))
	})
})
