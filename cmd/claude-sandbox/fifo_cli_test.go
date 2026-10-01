package main

// Spec: spec/config-cascade.feature CS-CASC-047 and spec/launch.feature
// CS-LNCH-172 — end to end through MainWithEnv: a FIFO planted at a cascade
// config.yaml or env file fails the launch at once (exit 2, naming the file),
// never hangs it, and nothing is built or created.

import (
	"os"
	"path/filepath"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
)

// plantLaunchFIFO makes a FIFO at path; cleanup opens it for writing
// (non-blocking) and closes it, so a read a regression left blocked returns.
func plantLaunchFIFO(path string) {
	Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
	Expect(syscall.Mkfifo(path, 0o600)).To(Succeed())
	DeferCleanup(func() {
		if w, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = w.Close()
		}
	})
}

// runBounded runs the launcher and fails unless it returns within 5 s.
func runBounded(f *cliFixture, args ...string) int {
	done := make(chan int, 1)
	go func() {
		defer GinkgoRecover()
		done <- f.run(args...)
	}()
	var code int
	Eventually(done, 5*time.Second).Should(Receive(&code), "the launch must not block on a FIFO")
	return code
}

var _ = Describe("a FIFO at a launch-config file never hangs a launch (CS-CASC-047, CS-LNCH-172)", func() {
	var f *cliFixture
	BeforeEach(func() { f = newCLIFixture() })

	It("CS-CASC-047: a FIFO at config.yaml fails the launch at once (exit 2), naming it, before anything is created", func() {
		cfg := filepath.Join(f.proj, ".claude-sandbox", "config.yaml")
		plantLaunchFIFO(cfg)
		Expect(runBounded(f, "--new")).To(Equal(2))
		Expect(f.errw.String()).To(ContainSubstring(cfg))
		Expect(f.errw.String()).To(ContainSubstring("not a regular file"))
		Expect(createdAny(f)).To(BeFalse())
		for _, c := range f.fake.Calls {
			Expect(c.Name).NotTo(Equal("docker"), "no docker call before the config read failed")
		}
	})

	It("CS-LNCH-172: a FIFO at an env file fails the launch at once (exit 2), naming it, before any image work", func() {
		env := filepath.Join(f.proj, ".claude-sandbox", "env")
		plantLaunchFIFO(env)
		Expect(runBounded(f, "--new")).To(Equal(2))
		Expect(f.errw.String()).To(ContainSubstring("Error: reading env file:"))
		Expect(f.errw.String()).To(ContainSubstring(env))
		Expect(f.errw.String()).To(ContainSubstring("not a regular file"))
		Expect(createdAny(f)).To(BeFalse())
		for _, c := range f.fake.Calls {
			Expect(c.Args).NotTo(ContainElement("build"), "no image work")
			Expect(c.Args).NotTo(ContainElement("buildx"), "no image work")
		}
	})

	Describe("CS-LNCH-172: attach and join with a FIFO env file", func() {
		var env string
		BeforeEach(func() {
			env = filepath.Join(f.proj, ".claude-sandbox", "env")
			plantLaunchFIFO(env)
			f.fake.On("docker ps", psRowFull("cs-a", "Up 1 hour", f.proj, "otter", "", "stalehash1234", "[]")+"\n", nil)
			f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
			f.env.Prompter = &prompt.Scripted{IsTTY: false}
		})

		DescribeTable("the drift check returns at once and names the file, then reports drift as for any uncomputable hash",
			func(flag string) {
				Expect(runBounded(f, flag)).To(Equal(3), "drift needs a decision; no terminal")
				Expect(f.errw.String()).To(ContainSubstring("WARNING: cannot compute the current configuration for the drift check:"))
				Expect(f.errw.String()).To(ContainSubstring(env))
				Expect(f.errw.String()).To(ContainSubstring("not a regular file"))
				Expect(f.errw.String()).To(ContainSubstring("different configuration"))
				Expect(createdAny(f)).To(BeFalse())
			},
			Entry("CS-LNCH-172: --attach", "--attach=otter"),
			Entry("CS-LNCH-172: --join", "--join=otter"),
		)

		DescribeTable("with --allow-config-drift the session is reached, the FIFO never waited on",
			func(flag, verb string) {
				Expect(runBounded(f, flag, "--allow-config-drift")).To(Equal(0))
				Expect(f.sessionLine()).To(HavePrefix("docker " + verb))
				Expect(createdAny(f)).To(BeFalse())
			},
			Entry("CS-LNCH-172: --attach", "--attach=otter", "attach"),
			Entry("CS-LNCH-172: --join", "--join=otter", "exec"),
		)
	})
})
