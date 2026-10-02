package execx_test

// Spec: spec/launch.feature CS-LNCH-176 — execx.Bounded is the one bounded-call
// helper (the tmux mark's and the launch-path git calls'): Runner.Start with
// DieWithParent, the whole process group killed at the bound, a timeout told
// apart from a start failure and an exit status. execx.Git applies
// GitTimeout, which a Fake's GitBound shortens for tests.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
)

var _ = Describe("Bounded (CS-LNCH-176)", func() {
	It("CS-LNCH-176: a real command that never ends is killed at the bound, with its grandchild", func() {
		start := time.Now()
		out, err := execx.Bounded(execx.System{}, 300*time.Millisecond,
			execx.Cmd{Name: "sh", Args: []string{"-c", "echo partial; sleep 30 & exec sleep 30"}})
		Expect(time.Since(start)).To(BeNumerically("<", 3*time.Second))
		Expect(errors.Is(err, execx.ErrTimedOut)).To(BeTrue(), "%v", err)
		Expect(err.Error()).To(Equal("sh -c echo partial; sleep 30 & exec sleep 30 did not finish within 300ms"))
		Expect(out).To(BeEmpty(), "a timed-out call's output is never used")
	})

	It("CS-LNCH-176: stdout is returned whatever the exit status, with that status", func() {
		out, err := execx.Bounded(execx.System{}, 5*time.Second, execx.Cmd{Name: "sh", Args: []string{"-c", "echo hi; exit 3"}})
		Expect(out).To(Equal("hi\n"))
		Expect(execx.ExitCode(err)).To(Equal(3))
		Expect(errors.Is(err, execx.ErrTimedOut)).To(BeFalse())

		out, err = execx.Bounded(execx.System{}, 5*time.Second, execx.Cmd{Name: "sh", Args: []string{"-c", "echo ok"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal("ok\n"))
	})

	It("CS-LNCH-176: a command that cannot start is a StartError, not a timeout", func() {
		_, err := execx.Bounded(execx.System{}, time.Second, execx.Cmd{Name: "/nonexistent/claude-sandbox-no-such-git"})
		var se *execx.StartError
		Expect(errors.As(err, &se)).To(BeTrue(), "%v", err)
		Expect(errors.Is(err, execx.ErrTimedOut)).To(BeFalse())
	})

	It("CS-LNCH-176: a bound already spent starts nothing and reads as a timeout", func() {
		f := &execx.Fake{}
		_, err := execx.Bounded(f, 0, execx.Cmd{Name: "git", Args: []string{"status"}})
		Expect(errors.Is(err, execx.ErrTimedOut)).To(BeTrue())
		Expect(f.Calls).To(BeEmpty())
	})

	It("CS-LNCH-176: Git starts with DieWithParent under GitTimeout; a Fake's GitBound shortens it and OnHang hangs it", func() {
		Expect(execx.GitTimeout).To(Equal(5 * time.Second))
		f := &execx.Fake{GitBound: 50 * time.Millisecond}
		f.OnHang("rev-parse")
		start := time.Now()
		_, err := execx.Git(f, execx.Cmd{Name: "git", Args: []string{"-C", "/p", "rev-parse", "--show-toplevel"}})
		Expect(time.Since(start)).To(BeNumerically("<", 2*time.Second))
		Expect(err).To(MatchError("git -C /p rev-parse --show-toplevel did not finish within 50ms"))
		Expect(f.Killed).To(Equal(1), "the hung process was signalled")
		Expect(f.Calls).To(HaveLen(1))
		Expect(f.Calls[0].DieWithParent).To(BeTrue())

		f.On("ls-files", "a\x00", nil)
		out, err := execx.Git(f, execx.Cmd{Name: "git", Args: []string{"ls-files"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal("a\x00"))
	})

	It("CS-LNCH-176: the warning names the call, the usual cause and the fallback", func() {
		err := &execx.TimeoutError{Command: "git -C /p rev-parse --show-toplevel", After: 5 * time.Second}
		Expect(execx.GitTimeoutWarning(err, "doing X")).To(Equal(
			"WARNING: git -C /p rev-parse --show-toplevel did not finish within 5s (a FIFO or other blocking file in its .git or a .gitignore can do this); doing X."))
	})

	It("CS-LNCH-176: Git's path sends the group SIGTERM first, so git can remove its locks; SIGKILL only after the grace", func() {
		dir := GinkgoT().TempDir()
		mark := filepath.Join(dir, "term")
		script := "trap 'echo term > " + mark + "; exit 0' TERM; sleep 30 & wait"
		start := time.Now()
		_, err := execx.BoundedTerm(execx.System{}, 300*time.Millisecond, 2*time.Second, execx.Cmd{Name: "sh", Args: []string{"-c", script}})
		Expect(errors.Is(err, execx.ErrTimedOut)).To(BeTrue())
		Expect(time.Since(start)).To(BeNumerically("<", 2*time.Second), "the trap exited at once: no wait for the grace")
		Expect(mark).To(BeAnExistingFile(), "SIGTERM reached it")

		// Ignoring SIGTERM: SIGKILL after the grace.
		start = time.Now()
		_, err = execx.BoundedTerm(execx.System{}, 200*time.Millisecond, 300*time.Millisecond,
			execx.Cmd{Name: "sh", Args: []string{"-c", "trap '' TERM; sleep 30 & wait"}})
		Expect(errors.Is(err, execx.ErrTimedOut)).To(BeTrue())
		Expect(time.Since(start)).To(BeNumerically("<", 3*time.Second))
	})

	It("CS-LNCH-176: Bounded (tmux's helper) keeps the immediate SIGKILL: no SIGTERM", func() {
		dir := GinkgoT().TempDir()
		mark := filepath.Join(dir, "term")
		_, err := execx.Bounded(execx.System{}, 300*time.Millisecond,
			execx.Cmd{Name: "sh", Args: []string{"-c", "trap 'echo term > " + mark + "; exit 0' TERM; sleep 30 & wait"}})
		Expect(errors.Is(err, execx.ErrTimedOut)).To(BeTrue())
		_, statErr := os.Stat(mark)
		Expect(os.IsNotExist(statErr)).To(BeTrue())
	})

	It("CS-LNCH-177: Git puts GitSafeArgs before the caller's args; the prefix constant matches them", func() {
		Expect(execx.GitSafePrefix).To(Equal("git " + strings.Join(execx.GitSafeArgs, " ")))
		Expect(execx.GitSafeArgs).To(Equal([]string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "--no-optional-locks"}))
		f := &execx.Fake{}
		_, err := execx.Git(f, execx.Cmd{Name: "git", Args: []string{"-C", "/p", "ls-files"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(f.CommandLines()).To(Equal([]string{execx.GitSafePrefix + " -C /p ls-files"}))
	})

	It("CS-LNCH-177: GitFilterOverrides blanks every named driver; a name a -c cannot carry is refused", func() {
		args, ok := execx.GitFilterOverrides([]string{"lfs", "a.b"})
		Expect(ok).To(BeTrue())
		Expect(args).To(Equal([]string{
			"-c", "filter.lfs.clean=", "-c", "filter.lfs.smudge=", "-c", "filter.lfs.process=", "-c", "filter.lfs.required=false",
			"-c", "filter.a.b.clean=", "-c", "filter.a.b.smudge=", "-c", "filter.a.b.process=", "-c", "filter.a.b.required=false",
		}))
		_, ok = execx.GitFilterOverrides([]string{"x=y"})
		Expect(ok).To(BeFalse())
		_, ok = execx.GitFilterOverrides([]string{"x\ny"})
		Expect(ok).To(BeFalse())
	})
})
