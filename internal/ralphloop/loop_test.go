package ralphloop_test

// Spec: spec/ralph-loop.feature (CS-RLP) and spec/ralph-quota.feature
// (CS-RQT). Run() is driven with a scripted RunIter seam that writes the
// quota-status/stderr/watchdog-marker files an iteration would leave behind;
// Sleep and Notify are recorders, so no real time passes and no network is
// touched.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/ralphloop"
)

// step is one scripted iteration outcome for RunIter.
type step func(l *ralphloop.Loop, iter int) int

func stepOK() step { return func(*ralphloop.Loop, int) int { return 0 } }

func stepStatus(status string) step {
	return func(l *ralphloop.Loop, _ int) int {
		Expect(os.WriteFile(l.QuotaFile, []byte(status+"\n"), 0o644)).To(Succeed())
		return 0
	}
}

func stepExit(code int) step { return func(*ralphloop.Loop, int) int { return code } }

func stepWatchdog() step {
	return func(l *ralphloop.Loop, _ int) int {
		Expect(os.WriteFile(l.MarkerFile, []byte("1\n"), 0o644)).To(Succeed())
		return 124
	}
}

var _ = Describe("ralph loop", func() {
	var (
		work     string
		out      *bytes.Buffer
		errBuf   *bytes.Buffer
		fake     *execx.Fake
		sleeps   []time.Duration
		notes    []string
		iters    []int
		opts     ralphloop.Options
		agentDir string
		ralphDir string
	)

	BeforeEach(func() {
		work = GinkgoT().TempDir()
		agentDir = filepath.Join(work, ".claude-sandbox", "agent")
		ralphDir = filepath.Join(work, ".claude-sandbox", "ralph")
		Expect(os.MkdirAll(agentDir, 0o755)).To(Succeed())
		for name, body := range map[string]string{
			"PROMPT.md":             "the prompt",
			"PROMPT_AUTO.md":        "auto addendum",
			"PROMPT_INTERACTIVE.md": "interactive addendum",
		} {
			Expect(os.WriteFile(filepath.Join(agentDir, name), []byte(body), 0o644)).To(Succeed())
		}
		// Neutralize any ambient overrides (empty == unset for envOr).
		for _, k := range []string{"STOP_FILE", "PROMPT_FILE", "CLAUDE_BIN"} {
			GinkgoT().Setenv(k, "")
		}
		out = &bytes.Buffer{}
		errBuf = &bytes.Buffer{}
		fake = &execx.Fake{}
		sleeps = nil
		notes = nil
		iters = nil
		opts = ralphloop.Options{
			WorkDir:     work,
			RepoRoot:    work, // no /opt/claude-sandbox in tests
			PromptRalph: []byte("ralph base prompt"),
			Limit:       1,
			Runner:      fake,
			Out:         out,
			Err:         errBuf,
			Sleep:       func(d time.Duration) { sleeps = append(sleeps, d) },
			Notify:      func(msg string) { notes = append(notes, msg) },
			Hostname:    "test-host",
			PID:         os.Getpid(),
			Rand:        func(int) int { return 25 }, // jitter factor exactly 1.0
		}
	})

	// script installs a RunIter that plays steps in order (last one repeats)
	// and records the iteration numbers it was called with.
	script := func(steps ...step) {
		i := 0
		opts.RunIter = func(l *ralphloop.Loop, iter int) int {
			iters = append(iters, iter)
			s := steps[i]
			if i < len(steps)-1 {
				i++
			}
			return s(l, iter)
		}
	}

	// backoffSleeps filters out the 3s pacing sleeps.
	backoffSleeps := func() []time.Duration {
		var got []time.Duration
		for _, d := range sleeps {
			if d != 3*time.Second {
				got = append(got, d)
			}
		}
		return got
	}

	// ---- flags, validation, startup ----

	Describe("setup", func() {
		It("CS-RLP-001: defaults resolve stop file, prompt, claude bin, limit, watchdog, timeouts", func() {
			script(stepOK())
			Expect(ralphloop.Run(opts)).To(Equal(0))
			banner := out.String()
			Expect(banner).To(ContainSubstring("stop file: " + filepath.Join(ralphDir, "stop")))
			Expect(banner).To(ContainSubstring("prompt:    " + filepath.Join(agentDir, "PROMPT.md")))
			Expect(banner).To(ContainSubstring("claude:    claude"))
			Expect(banner).To(ContainSubstring("model:     (default)"))
			Expect(banner).To(ContainSubstring("mode:      non-interactive"))
			Expect(banner).To(ContainSubstring("skip-perm: no"))
			Expect(banner).To(ContainSubstring("watchdog:  15m"))
			Expect(banner).To(ContainSubstring("iter-limit: 2h0m"))
			Expect(banner).To(ContainSubstring("run-log:   " + filepath.Join(ralphDir, "runlog.json")))
			Expect(banner).To(ContainSubstring("raw-log:   " + filepath.Join(ralphDir, "runlogs", "rawlog") + "_*"))
		})

		It("CS-RLP-001: default limit is 30", func() {
			opts.Limit = 0
			script(stepOK())
			// Stop after the first iteration so the loop doesn't run 30 times.
			opts.RunIter = func(l *ralphloop.Loop, iter int) int {
				iters = append(iters, iter)
				Expect(os.WriteFile(l.StopFile, []byte(""), 0o644)).To(Succeed())
				return 0
			}
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(out.String()).To(ContainSubstring("limit:     30"))
		})

		It("CS-RLP-001: STOP_FILE, PROMPT_FILE, CLAUDE_BIN env vars override defaults", func() {
			promptDir := filepath.Join(work, "prompts")
			Expect(os.MkdirAll(promptDir, 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(promptDir, "CUSTOM.md"), []byte("p"), 0o644)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(promptDir, "PROMPT_AUTO.md"), []byte("a"), 0o644)).To(Succeed())
			GinkgoT().Setenv("STOP_FILE", filepath.Join(work, "mystop"))
			GinkgoT().Setenv("PROMPT_FILE", filepath.Join(promptDir, "CUSTOM.md"))
			GinkgoT().Setenv("CLAUDE_BIN", "/custom/claude")
			script(stepOK())
			Expect(ralphloop.Run(opts)).To(Equal(0))
			banner := out.String()
			Expect(banner).To(ContainSubstring("stop file: " + filepath.Join(work, "mystop")))
			Expect(banner).To(ContainSubstring("prompt:    " + filepath.Join(promptDir, "CUSTOM.md")))
			Expect(banner).To(ContainSubstring("claude:    /custom/claude"))
		})

		It("CS-RLP-002: a non-positive limit exits 2", func() {
			opts.Limit = -1
			script(stepOK())
			Expect(ralphloop.Run(opts)).To(Equal(2))
			Expect(errBuf.String()).To(ContainSubstring("--limit must be a positive integer"))
			Expect(iters).To(BeEmpty())
		})

		It("CS-RLP-002: ParsePositiveInt rejects zero and non-numbers", func() {
			_, err := ralphloop.ParsePositiveInt("0")
			Expect(err).To(HaveOccurred())
			_, err = ralphloop.ParsePositiveInt("abc")
			Expect(err).To(HaveOccurred())
			n, err := ralphloop.ParsePositiveInt("5")
			Expect(err).NotTo(HaveOccurred())
			Expect(n).To(Equal(5))
		})

		It("CS-RLP-003: a missing prompt file exits 1 naming it", func() {
			Expect(os.Remove(filepath.Join(agentDir, "PROMPT.md"))).To(Succeed())
			script(stepOK())
			Expect(ralphloop.Run(opts)).To(Equal(1))
			Expect(errBuf.String()).To(ContainSubstring("PROMPT file not found: " + filepath.Join(agentDir, "PROMPT.md")))
		})

		It("CS-RLP-003: a missing mode addendum exits 1 naming it", func() {
			Expect(os.Remove(filepath.Join(agentDir, "PROMPT_AUTO.md"))).To(Succeed())
			script(stepOK())
			Expect(ralphloop.Run(opts)).To(Equal(1))
			Expect(errBuf.String()).To(ContainSubstring("Prompt addendum not found: " + filepath.Join(agentDir, "PROMPT_AUTO.md")))
		})

		It("CS-RLP-004: non-interactive mode selects PROMPT_AUTO.md beside the prompt", func() {
			script(stepOK())
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(out.String()).To(ContainSubstring("+ " + filepath.Join(agentDir, "PROMPT_AUTO.md")))
		})

		It("CS-RLP-004: --interactive selects PROMPT_INTERACTIVE.md", func() {
			opts.Interactive = true
			script(stepOK())
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(out.String()).To(ContainSubstring("+ " + filepath.Join(agentDir, "PROMPT_INTERACTIVE.md")))
			Expect(out.String()).To(ContainSubstring("mode:      interactive"))
		})

		It("CS-RLP-005: the startup banner reports effective settings", func() {
			opts.Model = "opus"
			opts.SkipPermissions = true
			opts.ClaudeBin = "/bin/claude-x"
			opts.WatchdogTimeout = -1
			opts.IterationTimeout = 90
			script(stepOK())
			Expect(ralphloop.Run(opts)).To(Equal(0))
			banner := out.String()
			Expect(banner).To(ContainSubstring("Ralph loop starting"))
			Expect(banner).To(ContainSubstring("repo:      " + work))
			Expect(banner).To(ContainSubstring("claude:    /bin/claude-x"))
			Expect(banner).To(ContainSubstring("model:     opus"))
			Expect(banner).To(ContainSubstring("skip-perm: yes"))
			Expect(banner).To(ContainSubstring("limit:     1"))
			Expect(banner).To(ContainSubstring("watchdog:  disabled"))
			Expect(banner).To(ContainSubstring("iter-limit: 1m30s"))
		})

		It("CS-RLP-006: runtime skeleton and a fresh runlog are created", func() {
			script(stepOK())
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(filepath.Join(ralphDir, "temp")).To(BeADirectory())
			Expect(filepath.Join(ralphDir, "runlogs")).To(BeADirectory())
			var runs []map[string]any
			raw, err := os.ReadFile(filepath.Join(ralphDir, "runlog.json"))
			Expect(err).NotTo(HaveOccurred())
			Expect(json.Unmarshal(raw, &runs)).To(Succeed())
			Expect(runs).To(HaveLen(1))
			Expect(runs[0]).To(HaveKey("startedAt"))
			Expect(runs[0]).To(HaveKeyWithValue("iterations", []any{}))
		})

		It("CS-RLP-006: a new run object is prepended to an existing runlog", func() {
			Expect(os.MkdirAll(ralphDir, 0o755)).To(Succeed())
			old := `[{"startedAt":"OLD","iterations":[]}]`
			Expect(os.WriteFile(filepath.Join(ralphDir, "runlog.json"), []byte(old), 0o644)).To(Succeed())
			script(stepOK())
			Expect(ralphloop.Run(opts)).To(Equal(0))
			var runs []map[string]any
			raw, _ := os.ReadFile(filepath.Join(ralphDir, "runlog.json"))
			Expect(json.Unmarshal(raw, &runs)).To(Succeed())
			Expect(runs).To(HaveLen(2))
			Expect(runs[1]).To(HaveKeyWithValue("startedAt", "OLD"))
		})

		It("CS-RLP-006: an unparseable runlog is replaced by a fresh array", func() {
			Expect(os.MkdirAll(ralphDir, 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(ralphDir, "runlog.json"), []byte("not json{"), 0o644)).To(Succeed())
			script(stepOK())
			Expect(ralphloop.Run(opts)).To(Equal(0))
			var runs []map[string]any
			raw, _ := os.ReadFile(filepath.Join(ralphDir, "runlog.json"))
			Expect(json.Unmarshal(raw, &runs)).To(Succeed())
			Expect(runs).To(HaveLen(1))
		})

		It("CS-RLP-007: the lock file holds pid/started_at/hostname and is removed on exit", func() {
			var seen map[string]any
			opts.RunIter = func(l *ralphloop.Loop, iter int) int {
				raw, err := os.ReadFile(l.LockFile)
				Expect(err).NotTo(HaveOccurred())
				Expect(json.Unmarshal(raw, &seen)).To(Succeed())
				return 0
			}
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(seen).To(HaveKeyWithValue("pid", float64(os.Getpid())))
			Expect(seen).To(HaveKeyWithValue("hostname", "test-host"))
			Expect(seen).To(HaveKey("started_at"))
			Expect(filepath.Join(ralphDir, "lock")).NotTo(BeAnExistingFile())
		})

		It("CS-RLP-007: a lock with an alive pid on the same host exits 1", func() {
			Expect(os.MkdirAll(ralphDir, 0o755)).To(Succeed())
			lock := fmt.Sprintf(`{"pid":%d,"started_at":"x","hostname":"test-host"}`, os.Getpid())
			Expect(os.WriteFile(filepath.Join(ralphDir, "lock"), []byte(lock), 0o644)).To(Succeed())
			script(stepOK())
			Expect(ralphloop.Run(opts)).To(Equal(1))
			Expect(errBuf.String()).To(ContainSubstring(fmt.Sprintf("Another ralph loop is active, PID %d", os.Getpid())))
			Expect(iters).To(BeEmpty())
		})

		It("CS-RLP-007: a lock with a dead pid is reclaimed with a stale warning", func() {
			cmd := exec.Command("true")
			Expect(cmd.Run()).To(Succeed())
			deadPid := cmd.Process.Pid // reaped: signal 0 fails
			Expect(os.MkdirAll(ralphDir, 0o755)).To(Succeed())
			lock := fmt.Sprintf(`{"pid":%d,"started_at":"x","hostname":"test-host"}`, deadPid)
			Expect(os.WriteFile(filepath.Join(ralphDir, "lock"), []byte(lock), 0o644)).To(Succeed())
			script(stepOK())
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(errBuf.String()).To(ContainSubstring(fmt.Sprintf("Stale lock file found (PID %d is dead). Reclaiming.", deadPid)))
			Expect(iters).To(Equal([]int{1}))
		})

		It("CS-RLP-007: a lock from a different hostname is reclaimed with a warning", func() {
			Expect(os.MkdirAll(ralphDir, 0o755)).To(Succeed())
			lock := fmt.Sprintf(`{"pid":%d,"started_at":"x","hostname":"other-host"}`, os.Getpid())
			Expect(os.WriteFile(filepath.Join(ralphDir, "lock"), []byte(lock), 0o644)).To(Succeed())
			script(stepOK())
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(errBuf.String()).To(ContainSubstring("Stale lock file from different host/container (other-host). Reclaiming."))
			Expect(iters).To(Equal([]int{1}))
		})

		It("CS-RLP-008: a pre-existing stop file is cleared at startup", func() {
			Expect(os.MkdirAll(ralphDir, 0o755)).To(Succeed())
			stop := filepath.Join(ralphDir, "stop")
			Expect(os.WriteFile(stop, []byte(""), 0o644)).To(Succeed())
			script(stepOK())
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(iters).To(Equal([]int{1})) // the stale stop file did not block the run
			Expect(stop).NotTo(BeAnExistingFile())
		})
	})

	// ---- iteration mechanics ----

	Describe("iteration mechanics", func() {
		It("CS-RLP-009: the stop file halts the loop between iterations", func() {
			opts.Limit = 5
			opts.RunIter = func(l *ralphloop.Loop, iter int) int {
				iters = append(iters, iter)
				Expect(os.WriteFile(l.StopFile, []byte(""), 0o644)).To(Succeed())
				return 0
			}
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(iters).To(Equal([]int{1}))
			Expect(out.String()).To(ContainSubstring("Stop file detected"))
			Expect(notes).To(ContainElement(ContainSubstring("Stop file detected")))
		})

		It("CS-RLP-010: temp/ is wiped between iterations", func() {
			opts.Limit = 2
			var secondSawSentinel bool
			opts.RunIter = func(l *ralphloop.Loop, iter int) int {
				iters = append(iters, iter)
				sentinel := filepath.Join(l.RalphDir, "temp", "sentinel")
				if iter == 1 {
					Expect(os.WriteFile(sentinel, []byte(""), 0o644)).To(Succeed())
				} else {
					_, err := os.Stat(sentinel)
					secondSawSentinel = err == nil
				}
				return 0
			}
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(iters).To(Equal([]int{1, 2}))
			Expect(secondSawSentinel).To(BeFalse())
		})

		It("CS-RLP-010: --resume keeps temp/ intact for the first iteration only", func() {
			opts.Resume = true
			opts.Limit = 2
			Expect(os.MkdirAll(filepath.Join(ralphDir, "temp"), 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(ralphDir, "temp", "sentinel"), []byte(""), 0o644)).To(Succeed())
			saw := map[int]bool{}
			opts.RunIter = func(l *ralphloop.Loop, iter int) int {
				iters = append(iters, iter)
				_, err := os.Stat(filepath.Join(l.RalphDir, "temp", "sentinel"))
				saw[iter] = err == nil
				return 0
			}
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(saw[1]).To(BeTrue(), "resumed first iteration keeps temp/")
			Expect(saw[2]).To(BeFalse(), "second iteration wipes temp/")
		})

		It("CS-RLP-010: without --resume the first iteration starts with a clean temp/", func() {
			Expect(os.MkdirAll(filepath.Join(ralphDir, "temp"), 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(ralphDir, "temp", "sentinel"), []byte(""), 0o644)).To(Succeed())
			var sawSentinel bool
			opts.RunIter = func(l *ralphloop.Loop, iter int) int {
				iters = append(iters, iter)
				_, err := os.Stat(filepath.Join(l.RalphDir, "temp", "sentinel"))
				sawSentinel = err == nil
				return 0
			}
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(sawSentinel).To(BeFalse())
		})

		It("CS-RLP-016: the iteration limit ends the loop with exit 0", func() {
			opts.Limit = 2
			script(stepOK())
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(iters).To(Equal([]int{1, 2}))
			Expect(out.String()).To(ContainSubstring("Iteration limit reached (2)"))
			Expect(notes).To(ContainElement(ContainSubstring("Iteration limit reached (2)")))
		})

		It("CS-RLP-017: an interrupt during an iteration notifies, removes the lock, and exits 0", func() {
			// The TERM-to-process-group half of this scenario needs a real
			// child tree; it is covered by the manual smoke checklist. This
			// asserts the loop-side handling via the Interrupted seam.
			opts.Limit = 5
			opts.Interrupted = func() bool { return true }
			script(stepOK())
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(iters).To(Equal([]int{1}))
			Expect(out.String()).To(ContainSubstring("Interrupted. Exiting."))
			Expect(notes).To(ContainElement(ContainSubstring("interrupted")))
			Expect(filepath.Join(ralphDir, "lock")).NotTo(BeAnExistingFile())
		})

		It("CS-RLP-018: ralph sleeps 3 seconds between iterations", func() {
			opts.Limit = 3
			script(stepOK())
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(sleeps).To(Equal([]time.Duration{3 * time.Second, 3 * time.Second}))
		})
	})

	// ---- outcome handling (CS-RQT) ----

	Describe("outcome handling", func() {
		It("CS-RQT-005: ok resets the consecutive-retry counter", func() {
			opts.Limit = 2
			opts.MaxRetries = 2
			script(stepStatus("rate_limit"), stepOK(), stepStatus("rate_limit"), stepOK())
			Expect(ralphloop.Run(opts)).To(Equal(0))
			// Both rate-limit retries are attempt 1 (30s), proving the ok in
			// between reset the counter; without the reset the second would
			// back off 60s.
			Expect(backoffSleeps()).To(Equal([]time.Duration{30 * time.Second, 30 * time.Second}))
			Expect(iters).To(Equal([]int{1, 1, 2, 2}))
		})

		It("CS-RQT-006: quota_exhausted parks, probes, and re-runs the same iteration", func() {
			opts.Limit = 1
			opts.QuotaPause = 300
			opts.QuotaMaxWait = 18000
			fake.On("-p ping", `{"type":"result","subtype":"success"}`+"\n", nil)
			script(stepStatus("quota_exhausted"), stepOK())
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(notes).To(ContainElement(ContainSubstring("Quota exhausted at iteration 1")))
			Expect(notes).To(ContainElement(ContainSubstring("Quota restored after 5m0s")))
			Expect(sleeps).To(ContainElement(300 * time.Second))
			Expect(iters).To(Equal([]int{1, 1}), "the iteration counter is not advanced")
		})

		It("CS-RQT-007: quota never restored within the cap exits 0", func() {
			opts.QuotaPause = 100
			opts.QuotaMaxWait = 200
			// Unstubbed probe commands produce no output: still exhausted.
			script(stepStatus("quota_exhausted"))
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(sleeps).To(Equal([]time.Duration{100 * time.Second, 100 * time.Second}))
			Expect(out.String()).To(ContainSubstring("Quota did not reset within 3m20s"))
			Expect(notes).To(ContainElement(ContainSubstring("Quota did not reset within 3m20s")))
			Expect(iters).To(Equal([]int{1}))
		})

		It("CS-RQT-008: the quota probe runs claude -p ping --output-format stream-json", func() {
			fake.On("-p ping", `{"type":"result","subtype":"success"}`+"\n", nil)
			script(stepStatus("quota_exhausted"), stepOK())
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(fake.Calls).To(HaveLen(1))
			Expect(fake.Calls[0].Name).To(Equal("claude"))
			Expect(fake.Calls[0].Args).To(Equal([]string{"-p", "ping", "--output-format", "stream-json"}))
		})

		It("CS-RQT-009: rate_limit retries the same iteration with exponential backoff", func() {
			opts.Limit = 1
			opts.MaxRetries = 5
			script(stepStatus("rate_limit"), stepStatus("rate_limit"), stepOK())
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(backoffSleeps()).To(Equal([]time.Duration{30 * time.Second, 60 * time.Second}))
			Expect(iters).To(Equal([]int{1, 1, 1}), "iteration N is re-run")
			Expect(out.String()).To(ContainSubstring("attempt 1/5"))
			Expect(out.String()).To(ContainSubstring("attempt 2/5"))
		})

		It("CS-RQT-010: retries exhausted notifies and exits 1", func() {
			opts.MaxRetries = 2
			script(stepStatus("rate_limit"))
			Expect(ralphloop.Run(opts)).To(Equal(1))
			Expect(backoffSleeps()).To(Equal([]time.Duration{30 * time.Second, 60 * time.Second}))
			Expect(notes).To(ContainElement(ContainSubstring("retries exhausted (2)")))
		})

		It("CS-RQT-011: watchdog_timeout continues to the next iteration", func() {
			opts.Limit = 2
			script(stepWatchdog(), stepOK())
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(iters).To(Equal([]int{1, 2}))
			Expect(notes).To(ContainElement(ContainSubstring("killed by watchdog (15m inactivity)")))
		})

		It("CS-RQT-011: iteration_timeout continues to the next iteration", func() {
			opts.Limit = 2
			script(stepExit(124), stepOK())
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(iters).To(Equal([]int{1, 2}))
			Expect(notes).To(ContainElement(ContainSubstring("hit hard time limit")))
		})

		It("CS-RQT-011: a timeout resets the retry counter", func() {
			opts.Limit = 2
			opts.MaxRetries = 2
			script(stepStatus("rate_limit"), stepWatchdog(), stepStatus("rate_limit"), stepOK())
			Expect(ralphloop.Run(opts)).To(Equal(0))
			// Both backoffs are attempt 1: the watchdog timeout reset the counter.
			Expect(backoffSleeps()).To(Equal([]time.Duration{30 * time.Second, 30 * time.Second}))
		})

		It("CS-RQT-012: an error outcome stops the loop with the iteration's exit code", func() {
			opts.Limit = 5
			script(stepExit(7))
			Expect(ralphloop.Run(opts)).To(Equal(7))
			Expect(iters).To(Equal([]int{1}))
			Expect(notes).To(ContainElement(ContainSubstring("exited with error (rc=7)")))
		})
	})
})
