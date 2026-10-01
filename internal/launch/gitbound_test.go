package launch_test

// Spec: spec/launch.feature CS-LNCH-176 — the launch package's git calls
// (GitRoot, DetectLinkedWorktree) are bounded by execx.GitTimeout; a git that
// never answers (a FIFO in .git) is killed and each call falls back to its
// existing failure outcome with one warning.

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
)

// hungGit is a Fake whose every git call hangs until killed, bounded at
// 50 ms instead of the real 5 s.
func hungGit() *execx.Fake {
	f := &execx.Fake{GitBound: 50 * time.Millisecond}
	f.OnHang("git ")
	return f
}

// within2s runs f and fails unless it returns within 2 s.
func within2s(f func()) {
	done := make(chan struct{})
	go func() {
		defer GinkgoRecover()
		defer close(done)
		f()
	}()
	Eventually(done, 2*time.Second).Should(BeClosed(), "a hung git must not hang the launch")
}

var _ = Describe("bounded launch-path git calls (CS-LNCH-176)", func() {
	It("CS-LNCH-176: a hung GitRoot is killed and reads as not a git repository, with one warning", func() {
		f := hungGit()
		var root, warn string
		within2s(func() { root, warn = launch.GitRoot(f, "/p") })
		Expect(root).To(BeEmpty())
		Expect(warn).To(Equal("WARNING: git -C /p rev-parse --show-toplevel did not finish within 50ms " +
			"(a FIFO or other blocking file in its .git or a .gitignore can do this); treating /p as not a git repository."))
		Expect(f.Killed).To(Equal(1))
		Expect(f.Calls[0].DieWithParent).To(BeTrue())
	})

	It("CS-LNCH-176: a GitRoot that merely fails stays silent", func() {
		f := &execx.Fake{}
		f.On("rev-parse --show-toplevel", "", execx.Fail(128))
		root, warn := launch.GitRoot(f, "/p")
		Expect(root).To(BeEmpty())
		Expect(warn).To(BeEmpty())
	})

	It("CS-LNCH-176: a hung linked-worktree probe is killed and the project launches plain, with one warning", func() {
		f := hungGit()
		var lw *launch.LinkedWorktree
		var warn string
		within2s(func() { lw, warn = launch.DetectLinkedWorktree(f, "/p") })
		Expect(lw).To(BeNil())
		Expect(warn).To(HavePrefix("WARNING: git -C /p rev-parse --git-dir --git-common-dir --show-toplevel did not finish within 50ms"))
		Expect(warn).To(HaveSuffix("; launching as a plain project, without the linked-worktree check."))
		Expect(f.Killed).To(Equal(1))
	})
})
