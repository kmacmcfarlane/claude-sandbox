package ralphloop

// White-box tests for the unexported prompt/argv/backoff helpers and the real
// pipeline runner. Spec: CS-RLP-011/012/013/015, CS-RQT-009, plus the
// CS-RLP-001 numeric defaults not visible in the banner.
//
// CS-RLP-013 (node stage ordering), CS-RLP-014 (raw-log naming on disk), and
// the process-group TERM half of CS-RLP-017 need the real node logstream
// stages / signal delivery and are covered by the manual smoke checklist; the
// interactive-mode pipeline test below exercises the real subprocess wiring
// (prompt to stdin, stderr capture, exit code) without node.

import (
	"bytes"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func indexOf(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}

var _ = Describe("defaults (white-box)", func() {
	It("CS-RLP-001: zero-value options take the documented numeric defaults", func() {
		o := Options{WorkDir: GinkgoT().TempDir()}
		Expect(o.withDefaults()).To(Succeed())
		Expect(o.RepoRoot).To(Equal("/opt/claude-sandbox"))
		Expect(o.Limit).To(Equal(30))
		Expect(o.ClaudeBin).To(Equal("claude"))
		Expect(o.WatchdogTimeout).To(Equal(15))
		Expect(o.IterationTimeout).To(Equal(7200))
		Expect(o.MaxRetries).To(Equal(5))
		Expect(o.RetryDelay).To(Equal(30))
		Expect(o.QuotaPause).To(Equal(300))
		Expect(o.QuotaMaxWait).To(Equal(18000))
	})
})

var _ = Describe("claudeArgs", func() {
	It("CS-RLP-012: non-interactive with --dangerous, --model opus, --worktree ralph, --resume builds the full argv", func() {
		l := &Loop{Options: Options{SkipPermissions: true, Model: "opus", Worktree: "ralph", Resume: true}}
		Expect(l.claudeArgs(true)).To(Equal([]string{
			"-p", "--dangerously-skip-permissions", "--worktree", "ralph", "--model", "opus", "--resume",
			"--verbose", "--output-format", "stream-json",
		}))
	})

	It("CS-RLP-012: subsequent iterations omit --resume but keep --worktree", func() {
		l := &Loop{Options: Options{SkipPermissions: true, Model: "opus", Worktree: "ralph", Resume: true}}
		Expect(l.claudeArgs(false)).To(Equal([]string{
			"-p", "--dangerously-skip-permissions", "--worktree", "ralph", "--model", "opus",
			"--verbose", "--output-format", "stream-json",
		}))
	})

	It("CS-RLP-012 / CS-RLP-021: without --worktree the argv carries no worktree flag at all", func() {
		l := &Loop{Options: Options{SkipPermissions: true, Model: "opus", Resume: true}}
		Expect(l.claudeArgs(true)).To(Equal([]string{
			"-p", "--dangerously-skip-permissions", "--model", "opus", "--resume",
			"--verbose", "--output-format", "stream-json",
		}))
		Expect(l.claudeArgs(false)).NotTo(ContainElement("--worktree"))
	})

	It("CS-RLP-019: --worktree is forwarded to EVERY iteration, not first-only like --resume", func() {
		l := &Loop{Options: Options{Worktree: "ralph", Resume: true}}
		for iter, resume := range []bool{true, false, false} {
			args := l.claudeArgs(resume)
			Expect(args).To(ContainElements("--worktree", "ralph"), "iteration %d", iter+1)
			idx := indexOf(args, "--worktree")
			Expect(args[idx+1]).To(Equal("ralph"))
		}
		// Interactive mode too: the worktree is where the run lives, not a
		// stream-json concern.
		l = &Loop{Options: Options{Interactive: true, Worktree: "ralph"}}
		Expect(l.claudeArgs(false)).To(Equal([]string{"--worktree", "ralph"}))
	})

	It("CS-RLP-012: interactive mode has no -p and no stream flags", func() {
		l := &Loop{Options: Options{Interactive: true}}
		Expect(l.claudeArgs(false)).To(BeEmpty())

		l = &Loop{Options: Options{Interactive: true, Model: "opus"}}
		Expect(l.claudeArgs(false)).To(Equal([]string{"--model", "opus"}))
	})
})

var _ = Describe("promptData", func() {
	var tmp, prompt, addendum string

	BeforeEach(func() {
		tmp = GinkgoT().TempDir()
		prompt = filepath.Join(tmp, "PROMPT.md")
		addendum = filepath.Join(tmp, "PROMPT_AUTO.md")
		Expect(os.WriteFile(prompt, []byte("the prompt"), 0o644)).To(Succeed())
		Expect(os.WriteFile(addendum, []byte("the addendum"), 0o644)).To(Succeed())
	})

	It("CS-RLP-011: concatenates base prompt, prompt file, and addendum with blank lines", func() {
		l := &Loop{Options: Options{PromptRalph: []byte("ralph base")}}
		l.PromptFile = prompt
		l.Addendum = addendum
		data, err := l.promptData()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("ralph base\n\nthe prompt\n\nthe addendum"))
	})

	It("CS-RLP-011: falls back to the repo-root PROMPT_RALPH.md copy", func() {
		Expect(os.WriteFile(filepath.Join(tmp, "PROMPT_RALPH.md"), []byte("repo ralph"), 0o644)).To(Succeed())
		l := &Loop{Options: Options{RepoRoot: tmp}}
		l.PromptFile = prompt
		l.Addendum = addendum
		data, err := l.promptData()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("repo ralph\n\nthe prompt\n\nthe addendum"))
	})

	It("CS-RLP-022: in worktree mode a generated 'Where you are' block follows the base prompt", func() {
		l := &Loop{Options: Options{PromptRalph: []byte("ralph base"), Worktree: "ralph", WorkDir: "/srv/proj"}}
		l.PromptFile = prompt
		l.Addendum = addendum
		data, err := l.promptData()
		Expect(err).NotTo(HaveOccurred())
		s := string(data)
		Expect(s).To(HavePrefix("ralph base\n\n## Where you are\n"))
		Expect(s).To(HaveSuffix("\n\nthe prompt\n\nthe addendum"))
		for _, want := range []string{
			"`--worktree ralph`",
			"`.claude/worktrees/ralph`",
			"branch `worktree-ralph`",
			"reopens the SAME worktree",
			"Never merge into `main`",
			"a human fast-forwards `main`",
			"`$CLAUDE_SANDBOX_PROJECT_DIR` (`/srv/proj`)",
			"`$CLAUDE_SANDBOX_PROJECT_DIR/.claude-sandbox/ralph/stop`",
			"blocks Edit/Write to the main checkout",
			"`backlog.py`",
			"`$BACKLOG_REPO_ROOT`",
		} {
			Expect(s).To(ContainSubstring(want))
		}
	})

	It("CS-RLP-021: without --worktree the prompt is exactly the three files, no generated block", func() {
		l := &Loop{Options: Options{PromptRalph: []byte("ralph base"), WorkDir: "/srv/proj"}}
		l.PromptFile = prompt
		l.Addendum = addendum
		data, err := l.promptData()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("ralph base\n\nthe prompt\n\nthe addendum"))
		Expect(string(data)).NotTo(ContainSubstring("Where you are"))
	})

	It("CS-RLP-011: a missing prompt file is an error", func() {
		l := &Loop{Options: Options{PromptRalph: []byte("x")}}
		l.PromptFile = filepath.Join(tmp, "missing.md")
		l.Addendum = addendum
		_, err := l.promptData()
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("backoff", func() {
	It("CS-RQT-009: doubles from --retry-delay, caps at 300s, scales by jitter in [0.75, 1.0]", func() {
		l := &Loop{Options: Options{RetryDelay: 30, Rand: func(int) int { return 25 }}}
		// Jitter factor exactly 1.0: raw exponential values, capped.
		Expect(l.backoff(1)).To(Equal(30))
		Expect(l.backoff(2)).To(Equal(60))
		Expect(l.backoff(3)).To(Equal(120))
		Expect(l.backoff(4)).To(Equal(240))
		Expect(l.backoff(5)).To(Equal(300)) // 480 capped to 300
		Expect(l.backoff(6)).To(Equal(300))

		// Jitter lower bound 0.75.
		l.Rand = func(int) int { return 0 }
		Expect(l.backoff(1)).To(Equal(22)) // 30 * 75 / 100
		Expect(l.backoff(5)).To(Equal(225))
	})
})

var _ = Describe("runIterationReal", func() {
	newLoop := func(tmp string) *Loop {
		prompt := filepath.Join(tmp, "PROMPT.md")
		addendum := filepath.Join(tmp, "PROMPT_INTERACTIVE.md")
		Expect(os.WriteFile(prompt, []byte("the prompt"), 0o644)).To(Succeed())
		Expect(os.WriteFile(addendum, []byte("the addendum"), 0o644)).To(Succeed())
		l := &Loop{Options: Options{
			WorkDir:          tmp,
			RepoRoot:         tmp,
			Interactive:      true,
			IterationTimeout: 3600,
			PromptRalph:      []byte("ralph base"),
			Out:              &bytes.Buffer{},
			Err:              &bytes.Buffer{},
		}}
		l.PromptFile = prompt
		l.Addendum = addendum
		l.StderrFile = filepath.Join(tmp, "stderr")
		l.RawLogBase = filepath.Join(tmp, "rawlog")
		return l
	}

	It("CS-PID-006: the pid-class burn runs once, immediately before claude is started", func() {
		tmp := GinkgoT().TempDir()
		l := newLoop(tmp)
		l.ClaudeBin = "/bin/cat"
		calls := 0
		l.ReserveClass = func() { calls++ }
		Expect(l.runIterationReal(1, false)).To(Equal(0))
		Expect(calls).To(Equal(1))
	})

	It("CS-RLP-013: interactive mode pipes the assembled prompt to the claude binary's stdin", func() {
		// /bin/cat stands in for claude: what it prints is what arrived on
		// stdin. Node logstream stage ordering is covered by the manual
		// smoke checklist (needs node).
		tmp := GinkgoT().TempDir()
		l := newLoop(tmp)
		l.ClaudeBin = "/bin/cat"
		Expect(l.runIterationReal(1, false)).To(Equal(0))
		Expect(l.Out.(*bytes.Buffer).String()).To(ContainSubstring("ralph base\n\nthe prompt\n\nthe addendum"))
		Expect(l.StderrFile).To(BeAnExistingFile())
	})

	It("CS-RLP-020: claude's environment carries BACKLOG_REPO_ROOT and CLAUDE_SANDBOX_PROJECT_DIR = the work dir", func() {
		// A shell stands in for claude and prints the two variables; the
		// loop's own process does NOT have them set, so anything printed
		// came from the loop.
		GinkgoT().Setenv("BACKLOG_REPO_ROOT", "")
		GinkgoT().Setenv("CLAUDE_SANDBOX_PROJECT_DIR", "")
		tmp := GinkgoT().TempDir()
		script := filepath.Join(tmp, "env-claude")
		Expect(os.WriteFile(script, []byte("#!/bin/sh\necho \"root=$BACKLOG_REPO_ROOT dir=$CLAUDE_SANDBOX_PROJECT_DIR\"\n"), 0o755)).To(Succeed())
		for _, worktree := range []string{"ralph", ""} { // worktree mode and shared checkout alike
			l := newLoop(tmp)
			l.Worktree = worktree
			l.ClaudeBin = script
			Expect(l.runIterationReal(1, false)).To(Equal(0))
			Expect(l.Out.(*bytes.Buffer).String()).To(ContainSubstring("root=" + tmp + " dir=" + tmp))
		}
		l := newLoop(tmp)
		Expect(l.childEnv()).To(ConsistOf("BACKLOG_REPO_ROOT="+tmp, "CLAUDE_SANDBOX_PROJECT_DIR="+tmp))
	})

	It("CS-RLP-015: the hard iteration timeout kills the iteration and yields exit 124", func() {
		tmp := GinkgoT().TempDir()
		script := filepath.Join(tmp, "slow-claude")
		Expect(os.WriteFile(script, []byte("#!/bin/sh\nsleep 30\n"), 0o755)).To(Succeed())
		l := newLoop(tmp)
		l.ClaudeBin = script
		l.IterationTimeout = 1
		start := time.Now()
		Expect(l.runIterationReal(1, false)).To(Equal(124))
		Expect(time.Since(start)).To(BeNumerically("<", 10*time.Second))
	})
})
