package main

// Spec: spec/launch.feature CS-LNCH-176, spec/image-build.feature CS-IMG-076,
// spec/layout.feature CS-LAY-024 — a launch whose every git call hangs (a FIFO
// in .git) still launches, within the bounds, with one warning per call and
// each call's ordinary failure outcome.

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("bounded launch-path git calls (CS-LNCH-176)", func() {
	It("CS-LNCH-176, CS-IMG-076, CS-LAY-024: every git call hangs — the launch still starts, worktree stood down, one warning each", func() {
		f := newCLIFixture()
		f.fake.GitBound = 50 * time.Millisecond
		f.fake.OnHang("git ")
		// .claude-sandbox/ exists, so the layout setup runs (CS-LAY-015).
		Expect(os.MkdirAll(filepath.Join(f.proj, ".claude-sandbox"), 0o755)).To(Succeed())

		done := make(chan int, 1)
		go func() {
			defer GinkgoRecover()
			done <- f.run("--worktree")
		}()
		var code int
		Eventually(done, 10*time.Second).Should(Receive(&code), "a hung git must not hang the launch")
		Expect(code).To(Equal(0), f.errw.String())

		errs := f.errw.String()
		Expect(errs).To(ContainSubstring(" describe --tags --always --dirty did not finish within 50ms"))
		Expect(errs).To(ContainSubstring(`using the version stamp "unknown".`))
		Expect(errs).To(ContainSubstring("rev-parse --git-dir --git-common-dir --show-toplevel did not finish within 50ms"))
		Expect(errs).To(ContainSubstring("launching as a plain project, without the linked-worktree check."))
		Expect(errs).To(ContainSubstring("rev-parse --show-toplevel did not finish within 50ms"))
		Expect(errs).To(ContainSubstring("treating " + f.proj + " as not a git repository."))
		Expect(errs).To(ContainSubstring("rev-parse --is-inside-work-tree did not finish within 50ms"))
		Expect(f.out.String()).To(ContainSubstring("Worktree: off (not a git repository)"), "the CS-LNCH-046 stand-down")
		Expect(f.launchLine()).NotTo(ContainSubstring("--worktree"))
		Expect(f.fake.Killed).To(BeNumerically(">=", 4))
		for _, l := range f.fake.CommandLines() {
			if strings.HasPrefix(l, "git ") {
				Expect(l).NotTo(ContainSubstring("check-ignore"), "no probe after an unknown answer")
			}
		}
	})
})
