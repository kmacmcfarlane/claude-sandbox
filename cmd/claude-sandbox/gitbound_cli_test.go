package main

// Spec: spec/launch.feature CS-LNCH-176, spec/image-build.feature CS-IMG-076,
// spec/layout.feature CS-LAY-024 — a launch whose every git call hangs (a FIFO
// in .git) still launches, within the bounds, with one warning per call and
// each call's ordinary failure outcome — unless a worktree was requested,
// which refuses (exit 2) rather than run in the shared checkout.

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
)

// hungGitFixture is a CLI fixture whose every git call hangs, bounded at 50 ms.
func hungGitFixture() *cliFixture {
	f := newCLIFixture()
	f.fake.GitBound = 50 * time.Millisecond
	f.fake.OnHang("git ")
	// .claude-sandbox/ exists, so the layout setup runs (CS-LAY-015).
	Expect(os.MkdirAll(filepath.Join(f.proj, ".claude-sandbox"), 0o755)).To(Succeed())
	return f
}

// runWithin runs the launcher and fails unless it returns within 10 s.
func runWithin(f *cliFixture, args ...string) int {
	done := make(chan int, 1)
	go func() {
		defer GinkgoRecover()
		done <- f.run(args...)
	}()
	var code int
	Eventually(done, 10*time.Second).Should(Receive(&code), "a hung git must not hang the launch")
	return code
}

func created(f *cliFixture) bool {
	for _, l := range f.fake.CommandLines() {
		if strings.HasPrefix(l, "docker create") {
			return true
		}
	}
	return false
}

var _ = Describe("bounded launch-path git calls (CS-LNCH-176)", func() {
	It("CS-LNCH-176, CS-IMG-076, CS-LAY-024: every git call hangs, no worktree requested — the launch still starts, one warning each", func() {
		f := hungGitFixture()
		Expect(runWithin(f)).To(Equal(0), f.errw.String())

		errs := f.errw.String()
		Expect(errs).To(ContainSubstring(" config -z --name-only --get-regexp ^filter\\. did not finish within 50ms"))
		Expect(errs).To(ContainSubstring(`using the version stamp "unknown".`))
		Expect(errs).To(ContainSubstring("rev-parse --git-dir --git-common-dir --show-toplevel did not finish within 50ms"))
		Expect(errs).To(ContainSubstring("launching as a plain project, without the linked-worktree check."))
		Expect(errs).To(ContainSubstring("rev-parse --show-toplevel did not finish within 50ms"))
		Expect(errs).To(ContainSubstring("treating " + f.proj + " as not a git repository."))
		Expect(errs).To(ContainSubstring("rev-parse --is-inside-work-tree did not finish within 50ms"))
		Expect(f.out.String()).NotTo(ContainSubstring("Worktree:"), "nothing was requested")
		Expect(created(f)).To(BeTrue())
		Expect(f.fake.Killed).To(BeNumerically(">=", 4))
		for _, l := range f.fake.CommandLines() {
			if strings.HasPrefix(l, "git ") {
				Expect(l).To(HavePrefix(execx.GitSafePrefix+" "), "CS-LNCH-177: every git call is hardened")
				Expect(l).NotTo(ContainSubstring("check-ignore"), "no probe after an unknown answer")
			}
		}
	})

	It("CS-LNCH-176: a requested worktree whose git pre-check times out refuses (exit 2) — --worktree, ralph's default, the env var", func() {
		for _, c := range []struct {
			args []string
			env  string
		}{{args: []string{"--worktree"}}, {args: []string{"--ralph"}}, {env: "1"}} {
			f := hungGitFixture()
			if c.env != "" {
				f.envmap["CLAUDE_SANDBOX_WORKTREE"] = c.env
			}
			Expect(runWithin(f, c.args...)).To(Equal(2), "%v", c.args)
			Expect(f.errw.String()).To(ContainSubstring("Error: a worktree was requested, but git -C " + f.proj +
				" rev-parse --show-toplevel did not finish within 50ms"))
			Expect(f.errw.String()).To(ContainSubstring("not launching in the shared checkout instead. Remove the blocking file, or pass --no-worktree to launch there."))
			Expect(f.errw.String()).NotTo(ContainSubstring("treating " + f.proj + " as not a git repository"))
			Expect(f.out.String()).NotTo(ContainSubstring("Worktree: off"))
			Expect(created(f)).To(BeFalse(), "%v", c.args)
		}
	})

	It("CS-LNCH-176: --no-worktree over ralph's default launches with the warning", func() {
		f := hungGitFixture()
		Expect(runWithin(f, "--ralph", "--no-worktree")).To(Equal(0), f.errw.String())
		Expect(f.errw.String()).To(ContainSubstring("treating " + f.proj + " as not a git repository."))
		Expect(created(f)).To(BeTrue())
	})
})
