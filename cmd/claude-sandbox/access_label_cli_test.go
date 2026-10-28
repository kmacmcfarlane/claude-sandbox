package main

// Spec: spec/launch.feature CS-LNCH-223 — every create records the access
// options it applied, and the source that turned each on, in the unhashed
// claude-sandbox.access label: from the launch's own resolution, so a falsy
// switch is never "env". A restart reads only it (CS-SESS-107).

import (
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
)

var _ = Describe("the access label (CS-LNCH-223)", func() {
	var f *cliFixture
	BeforeEach(func() {
		saved := detachedSettle
		detachedSettle = time.Millisecond
		DeferCleanup(func() { detachedSettle = saved })
		f = newCLIFixture()
	})

	config := func(yaml string) {
		d := filepath.Join(f.proj, ".claude-sandbox")
		Expect(os.MkdirAll(d, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(d, "config.yaml"), []byte(yaml), 0o644)).To(Succeed())
	}
	access := func() string {
		v, ok := labelsOf(f.launched().Args)[launch.LabelAccess]
		Expect(ok).To(BeTrue(), "no %s label", launch.LabelAccess)
		return v
	}

	It("CS-LNCH-223: each option with its source, in a fixed order; none applied is \"none\"", func() {
		f.envmap["CLAUDE_SANDBOX_HOST_ACCESS_GIT_ENABLED"] = "1"
		config("hostAccess:\n  packageCaches:\n    enabled: true\n")
		Expect(f.run("--ssh", "--dangerous")).To(Equal(0), f.errw.String())
		Expect(access()).To(Equal("dangerous=flag,ssh=flag,git=env,package-caches=config"))

		f = newCLIFixture()
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(access()).To(Equal("none"))
	})

	It("CS-LNCH-223: a falsy switch is never env: the config decides, or the option is off", func() {
		f.envmap["CLAUDE_SANDBOX_HOST_ACCESS_SSH_ENABLED"] = "0"
		f.envmap["CLAUDE_SANDBOX_HOST_ACCESS_DOCKER_SOCKET_ENABLED"] = "false"
		f.envmap["CLAUDE_SANDBOX_DANGEROUS"] = "0"
		config("hostAccess:\n  ssh:\n    enabled: true\n")
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(access()).To(Equal("ssh=config"))
	})

	It("CS-LNCH-223: dangerous from its switch and from the config", func() {
		f.envmap["CLAUDE_SANDBOX_DANGEROUS"] = "1"
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(access()).To(Equal("dangerous=env"))

		f = newCLIFixture()
		config("dangerous: true\n")
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(access()).To(Equal("dangerous=config"))
	})

	DescribeTable("CS-LNCH-223: every create path writes it",
		func(args ...string) {
			f.run(args...)
			Expect(access()).To(Equal("aws=flag"))
		},
		Entry("interactive", "--aws"),
		Entry("--detach", "--detach", "--aws"),
		Entry("--branch", "--branch", "--aws"),
		Entry("headless", "headless", "--aws", "--"),
		Entry("ralph", "--ralph", "--aws"),
	)

	It("CS-LNCH-223: the tier-1 [b] path writes it too", func() {
		f.fake.On("docker ps", psRow("cs-a", "Up 1 hour", f.proj, "otter")+"\n", nil)
		f.env.Prompter = &prompt.Scripted{IsTTY: true, Answers: []string{"b"}}
		f.run("--git")
		Expect(access()).To(Equal("git=flag"))
	})

})
