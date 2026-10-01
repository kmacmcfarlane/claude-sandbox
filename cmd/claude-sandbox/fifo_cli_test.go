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
})
