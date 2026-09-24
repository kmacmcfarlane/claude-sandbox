package ralphloop_test

// Spec: spec/ralph-loop.feature CS-RLP-023..029 (OOM-killed iterations).
// The cgroup is a temp dir whose memory.events the scripted RunIter bumps,
// standing in for the kernel's OOM killer; Sleep and Notify are recorders.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/ralphloop"
)

// writeEvents writes memory.events with oom_kill = kills and oom = hits.
func writeEvents(dir string, kills, hits int) {
	body := fmt.Sprintf("low 0\nhigh 0\nmax 12\noom %d\noom_kill %d\noom_group_kill 0\n", hits, kills)
	Expect(os.WriteFile(filepath.Join(dir, "memory.events"), []byte(body), 0o644)).To(Succeed())
}

var _ = Describe("OOM-killed iterations", func() {
	var (
		work    string
		cgroup  string
		ralph   string
		out     *bytes.Buffer
		sleeps  []time.Duration
		notes   []string
		iters   []int
		opts    ralphloop.Options
		kills   int
		hits    int
		runlogF string
	)

	BeforeEach(func() {
		work = GinkgoT().TempDir()
		cgroup = filepath.Join(work, "cgroup")
		Expect(os.MkdirAll(cgroup, 0o755)).To(Succeed())
		agent := filepath.Join(work, ".claude-sandbox", "agent")
		ralph = filepath.Join(work, ".claude-sandbox", "ralph")
		runlogF = filepath.Join(ralph, "runlog.json")
		Expect(os.MkdirAll(agent, 0o755)).To(Succeed())
		for _, name := range []string{"PROMPT.md", "PROMPT_AUTO.md"} {
			Expect(os.WriteFile(filepath.Join(agent, name), []byte("p"), 0o644)).To(Succeed())
		}
		for _, k := range []string{"STOP_FILE", "PROMPT_FILE", "CLAUDE_BIN"} {
			GinkgoT().Setenv(k, "")
		}
		kills, hits = 0, 3
		writeEvents(cgroup, kills, hits)
		Expect(os.WriteFile(filepath.Join(cgroup, "memory.max"), []byte("17179869184\n"), 0o644)).To(Succeed())
		out = &bytes.Buffer{}
		sleeps, notes, iters = nil, nil, nil
		opts = ralphloop.Options{
			WorkDir:     work,
			RepoRoot:    work,
			PromptRalph: []byte("base"),
			Limit:       1,
			Runner:      &execx.Fake{},
			Out:         out,
			Err:         &bytes.Buffer{},
			Sleep:       func(d time.Duration) { sleeps = append(sleeps, d) },
			Notify:      func(msg string) { notes = append(notes, msg) },
			Hostname:    "test-host",
			PID:         os.Getpid(),
			Rand:        func(int) int { return 25 },
			CgroupDir:   cgroup,
		}
	})

	// oomed: claude exits 137 and the counter rises by n, at the
	// container's own limit (the oom counter rises too).
	oomed := func(n int) func(*ralphloop.Loop, int) int {
		return func(*ralphloop.Loop, int) int {
			kills += n
			hits++
			writeEvents(cgroup, kills, hits)
			return 137
		}
	}
	// hostOOMed: claude exits 137 and oom_kill rises by n, but oom does not:
	// the host's global OOM killer (CS-RLP-030).
	hostOOMed := func(n int) func(*ralphloop.Loop, int) int {
		return func(*ralphloop.Loop, int) int {
			kills += n
			writeEvents(cgroup, kills, hits)
			return 137
		}
	}
	// logEntry appends run-logger's runlog entry for the iteration.
	logEntry := func(iter int) {
		raw, err := os.ReadFile(runlogF)
		Expect(err).NotTo(HaveOccurred())
		var runs []map[string]any
		Expect(json.Unmarshal(raw, &runs)).To(Succeed())
		its := runs[0]["iterations"].([]any)
		runs[0]["iterations"] = append(its, map[string]any{"iteration": iter, "sessionId": "s"})
		raw, _ = json.Marshal(runs)
		Expect(os.WriteFile(runlogF, raw, 0o644)).To(Succeed())
	}
	// withLog: what the pipeline leaves behind (runlog entry, status "ok").
	withLog := func(f func(*ralphloop.Loop, int) int) func(*ralphloop.Loop, int) int {
		return func(l *ralphloop.Loop, iter int) int {
			logEntry(iter)
			// run-logger writes "ok" when claude's stdout just closes.
			Expect(os.WriteFile(l.QuotaFile, []byte("ok\n"), 0o644)).To(Succeed())
			return f(l, iter)
		}
	}
	ok := func(*ralphloop.Loop, int) int { return 0 }
	script := func(steps ...func(*ralphloop.Loop, int) int) {
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
	iterations := func() []map[string]any {
		raw, err := os.ReadFile(runlogF)
		Expect(err).NotTo(HaveOccurred())
		var runs []map[string]any
		Expect(json.Unmarshal(raw, &runs)).To(Succeed())
		var got []map[string]any
		for _, e := range runs[0]["iterations"].([]any) {
			got = append(got, e.(map[string]any))
		}
		return got
	}
	nonPacing := func() []time.Duration {
		var got []time.Duration
		for _, d := range sleeps {
			if d != 3*time.Second {
				got = append(got, d)
			}
		}
		return got
	}

	Describe("the counter", func() {
		It("CS-RLP-023: the before-sample is taken per iteration, not assumed to be 0", func() {
			// The counter already stands at 5 when the loop starts: a 137
			// with no rise during the iteration is not an oom.
			kills = 5
			writeEvents(cgroup, kills, hits)
			script(stepExit(137))
			Expect(ralphloop.Run(opts)).To(Equal(137), "CS-RQT-012 error")
			Expect(out.String()).NotTo(ContainSubstring("[oom]"))
		})

		It("CS-RLP-023: a rise between iterations is not charged to the next one", func() {
			// The counter rises during the 3s pacing sleep after iteration 1;
			// iteration 2's claude then exits 137 with no rise of its own.
			opts.Limit = 2
			opts.Sleep = func(d time.Duration) {
				sleeps = append(sleeps, d)
				if d == 3*time.Second {
					kills++
					writeEvents(cgroup, kills, hits)
				}
			}
			script(ok, stepExit(137))
			Expect(ralphloop.Run(opts)).To(Equal(137))
			Expect(out.String()).NotTo(ContainSubstring("[oom]"))
		})

		It("CS-RLP-023: the after-sample is taken once the iteration is over", func() {
			script(oomed(1), ok)
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(out.String()).To(ContainSubstring("[oom]"))
			Expect(iters).To(Equal([]int{1, 1}))
		})

		It("CS-RLP-023: ReadOOMKills parses the oom_kill line and reports unreadable input", func() {
			writeEvents(cgroup, 7, hits)
			n, readable := ralphloop.ReadOOMKills(cgroup)
			Expect(readable).To(BeTrue())
			Expect(n).To(Equal(7))

			_, readable = ralphloop.ReadOOMKills(filepath.Join(work, "absent"))
			Expect(readable).To(BeFalse(), "missing file")
			Expect(os.WriteFile(filepath.Join(cgroup, "memory.events"), []byte("low 0\noom 1\n"), 0o644)).To(Succeed())
			_, readable = ralphloop.ReadOOMKills(cgroup)
			Expect(readable).To(BeFalse(), "no oom_kill line")
			Expect(os.WriteFile(filepath.Join(cgroup, "memory.events"), []byte("oom_kill x\n"), 0o644)).To(Succeed())
			_, readable = ralphloop.ReadOOMKills(cgroup)
			Expect(readable).To(BeFalse(), "unparseable value")
		})
	})

	Describe("classification", func() {
		DescribeTable("CS-RLP-024: exit 137 plus a raised counter is oom; anything else is the CS-RQT chain",
			func(exit, before, after int, want bool) {
				Expect(ralphloop.IsOOM(exit, before, true, after, true)).To(Equal(want))
			},
			Entry("137, 0 -> 1", 137, 0, 1, true),
			Entry("137, 2 -> 5", 137, 2, 5, true),
			Entry("137, no raise", 137, 1, 1, false),
			Entry("exit 0 with a raise (non-fatal kill)", 0, 0, 1, false),
			Entry("exit 1 with a raise", 1, 0, 1, false),
		)

		It("CS-RLP-024: an OOM-killed claude is oom even though run-logger wrote ok", func() {
			opts.Limit = 1
			script(withLog(oomed(1)), withLog(ok))
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(out.String()).To(ContainSubstring("[oom]"))
			Expect(iterations()[0]).To(HaveKeyWithValue("outcome", "oom"))
		})

		It("CS-RLP-024: exit 137 without a raise keeps the existing classification", func() {
			script(stepExit(137))
			Expect(ralphloop.Run(opts)).To(Equal(137), "CS-RQT-012 error exits with the code")
			Expect(out.String()).NotTo(ContainSubstring("[oom]"))
			Expect(iters).To(Equal([]int{1}))
		})

		It("CS-RLP-024: a raise without claude dying is not an iteration failure", func() {
			script(func(*ralphloop.Loop, int) int { kills++; writeEvents(cgroup, kills, hits); return 0 })
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(out.String()).NotTo(ContainSubstring("[oom]"))
		})

		DescribeTable("CS-RLP-025: an unreadable counter never invents an oom",
			func(breakIt func()) {
				opts.RunIter = func(*ralphloop.Loop, int) int {
					iters = append(iters, 1)
					breakIt()
					return 137
				}
				Expect(ralphloop.Run(opts)).To(Equal(137), "falls through to CS-RQT-012")
				Expect(out.String()).NotTo(ContainSubstring("[oom]"))
				Expect(notes).NotTo(ContainElement(ContainSubstring("OOM")))
			},
			Entry("file removed", func() { os.Remove(filepath.Join(cgroup, "memory.events")) }),
			Entry("no oom_kill line", func() {
				os.WriteFile(filepath.Join(cgroup, "memory.events"), []byte("low 0\n"), 0o644)
			}),
			Entry("garbage value", func() {
				os.WriteFile(filepath.Join(cgroup, "memory.events"), []byte("oom_kill ???\n"), 0o644)
			}),
		)

		It("CS-RLP-025: an unreadable BEFORE-sample is not read as 0", func() {
			Expect(os.Remove(filepath.Join(cgroup, "memory.events"))).To(Succeed())
			script(func(*ralphloop.Loop, int) int { writeEvents(cgroup, 1, hits); return 137 })
			Expect(ralphloop.Run(opts)).To(Equal(137), "falls through to CS-RQT-012")
			Expect(out.String()).NotTo(ContainSubstring("[oom]"))
		})

		It("CS-RLP-025: no cgroup at all (cgroup v1) runs exactly as before", func() {
			opts.CgroupDir = filepath.Join(work, "absent")
			script(stepExit(137))
			Expect(ralphloop.Run(opts)).To(Equal(137))
			Expect(out.String()).NotTo(ContainSubstring("[oom]"))
		})
	})

	Describe("handling", func() {
		It("CS-RLP-026: the first oom backs off a fixed 60s and re-runs the same iteration once", func() {
			opts.Limit = 2
			script(oomed(1), ok, ok)
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(iters).To(Equal([]int{1, 1, 2}), "iteration 1 is re-run, not skipped")
			Expect(nonPacing()).To(Equal(steps(60)))
			Expect(out.String()).To(ContainSubstring("Retrying iteration 1 once in 1m0s"))
			Expect(notes[0]).To(ContainSubstring("OOM killer"))
		})

		It("CS-RLP-026: an injected OOMBackoff is honoured", func() {
			opts.OOMBackoff = 5 * time.Second
			script(oomed(1), ok)
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(nonPacing()).To(Equal(steps(5)))
		})

		It("CS-RLP-026: a non-oom retry resets the streak, so a later oom is retried again", func() {
			opts.Limit = 2
			script(oomed(1), ok, oomed(1), ok)
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(iters).To(Equal([]int{1, 1, 2, 2}))
			Expect(nonPacing()).To(Equal(steps(120)))
		})

		It("CS-RLP-026: an interrupt during the back-off ends it early and launches no retry", func() {
			interrupted := false
			opts.Interrupted = func() bool { return interrupted }
			opts.Sleep = func(d time.Duration) {
				sleeps = append(sleeps, d)
				if len(sleeps) == 3 {
					interrupted = true
				}
			}
			script(oomed(1), ok)
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(iters).To(Equal([]int{1}), "no retry")
			Expect(sleeps).To(Equal(steps(3)), "the rest of the back-off is not waited out")
			Expect(out.String()).To(ContainSubstring("Interrupted. Exiting."))
			Expect(notes[len(notes)-1]).To(ContainSubstring("interrupted by user"))
			Expect(filepath.Join(ralph, "lock")).NotTo(BeAnExistingFile())
		})

		It("CS-RLP-026: the stop file during the back-off ends it early and launches no retry", func() {
			opts.Sleep = func(d time.Duration) {
				sleeps = append(sleeps, d)
				if len(sleeps) == 2 {
					Expect(os.WriteFile(filepath.Join(ralph, "stop"), nil, 0o644)).To(Succeed())
				}
			}
			script(oomed(1), ok)
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(iters).To(Equal([]int{1}), "no retry")
			Expect(sleeps).To(Equal(steps(2)))
			Expect(out.String()).To(ContainSubstring("Stop file detected"))
		})

		It("CS-RLP-026: a quota park between two ooms does not reset the streak", func() {
			fake := &execx.Fake{}
			fake.On("ping", `{"type":"result"}`, nil)
			opts.Runner = fake
			opts.Limit = 3
			quota := func(l *ralphloop.Loop, _ int) int {
				Expect(os.WriteFile(l.QuotaFile, []byte("quota_exhausted\n"), 0o644)).To(Succeed())
				return 0
			}
			script(oomed(1), quota, oomed(1), ok)
			Expect(ralphloop.Run(opts)).To(Equal(137), "second consecutive oom")
			Expect(iters).To(Equal([]int{1, 1, 1}))
			Expect(out.String()).To(ContainSubstring("The retry was OOM-killed too"))
		})

		It("CS-RLP-026: a rate-limit retry between two ooms does not reset the streak", func() {
			opts.Limit = 3
			rate := func(l *ralphloop.Loop, _ int) int {
				Expect(os.WriteFile(l.QuotaFile, []byte("rate_limit\n"), 0o644)).To(Succeed())
				return 0
			}
			script(oomed(1), rate, oomed(1), ok)
			Expect(ralphloop.Run(opts)).To(Equal(137))
			Expect(iters).To(Equal([]int{1, 1, 1}))
		})

		It("CS-RLP-026: a completed timeout iteration resets the streak", func() {
			opts.Limit = 3
			script(oomed(1), stepExit(124), oomed(1), ok)
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(iters).To(Equal([]int{1, 1, 2, 2, 3}))
		})

		It("CS-RLP-027: a second consecutive oom stops the loop, notifies, and exits 137", func() {
			opts.Limit = 5
			script(oomed(1), oomed(2), ok)
			Expect(ralphloop.Run(opts)).To(Equal(137))
			Expect(iters).To(Equal([]int{1, 1}))
			Expect(nonPacing()).To(Equal(steps(60)), "no second back-off")
			Expect(out.String()).To(ContainSubstring("The retry was OOM-killed too. Exiting."))
			last := notes[len(notes)-1]
			Expect(last).To(ContainSubstring("Loop stopped"))
			Expect(last).To(ContainSubstring("2 OOM kills"))
			Expect(last).To(ContainSubstring("memoryLimit in effect: 16g"))
			Expect(filepath.Join(ralph, "lock")).NotTo(BeAnExistingFile())
		})
	})

	Describe("the message", func() {
		It("CS-RLP-028: names the kill, memoryLimit from memory.max, swap, and the remedies", func() {
			script(oomed(1), oomed(1))
			Expect(ralphloop.Run(opts)).To(Equal(137))
			msg := notes[len(notes)-1]
			Expect(msg).To(ContainSubstring("claude was killed by the container's OOM killer at iteration 1 (exit 137; 1 OOM kill): the container hit its memoryLimit."))
			Expect(msg).To(ContainSubstring("memoryLimit in effect: 16g (cgroup memory.max)"))
			Expect(msg).To(ContainSubstring("swap is off by design"))
			Expect(msg).To(ContainSubstring("raise memoryLimit in .claude-sandbox/config.yaml"))
			Expect(msg).To(ContainSubstring("cap build/test parallelism (e.g. ginkgo --procs=N, go test -p N, make -jN)"))
		})

		It("CS-RLP-028: a host OOM names the host, says raising memoryLimit will not help, and points at the README", func() {
			script(hostOOMed(1), hostOOMed(1))
			Expect(ralphloop.Run(opts)).To(Equal(137))
			msg := notes[len(notes)-1]
			Expect(msg).To(ContainSubstring("claude was killed from outside the container's memoryLimit at iteration 1 (exit 137; 1 OOM kill) (the host ran out of memory, or a parent cgroup's limit)."))
			Expect(msg).To(ContainSubstring("memoryLimit in effect: 16g (cgroup memory.max)"))
			Expect(msg).To(ContainSubstring("Raising memoryLimit will not help."))
			Expect(msg).To(ContainSubstring("run fewer sandboxes at once, or cap their build/test parallelism (e.g. ginkgo --procs=N, go test -p N, make -jN)"))
			Expect(msg).To(ContainSubstring(`see the README section "When the host runs out of memory"`))
			Expect(msg).NotTo(ContainSubstring("raise memoryLimit in"))
			Expect(msg).NotTo(ContainSubstring("container's OOM killer"))
		})

		It("CS-RLP-028: an unreadable oom counter names both causes and both remedies", func() {
			// oom_kill is readable (so the outcome is oom) but the oom line is gone.
			script(func(*ralphloop.Loop, int) int {
				kills++
				Expect(os.WriteFile(filepath.Join(cgroup, "memory.events"), []byte(fmt.Sprintf("oom_kill %d\n", kills)), 0o644)).To(Succeed())
				return 137
			}, ok)
			Expect(ralphloop.Run(opts)).To(Equal(0))
			msg := notes[0]
			Expect(msg).To(ContainSubstring("claude was killed by the OOM killer at iteration 1 (exit 137; 1 OOM kill): the container's memoryLimit or the host running out of memory."))
			Expect(msg).To(ContainSubstring("at the limit, raise memoryLimit in .claude-sandbox/config.yaml"))
			Expect(msg).To(ContainSubstring(`if the host ran out, run fewer sandboxes at once (see the README section "When the host runs out of memory")`))
			Expect(msg).To(ContainSubstring("either way, cap build/test parallelism (e.g. ginkgo --procs=N, go test -p N, make -jN)"))
		})

		It("CS-RLP-028: the back-off and single retry are the same for a host OOM", func() {
			opts.Limit = 5
			script(hostOOMed(1), hostOOMed(1), ok)
			Expect(ralphloop.Run(opts)).To(Equal(137))
			Expect(iters).To(Equal([]int{1, 1}))
			Expect(nonPacing()).To(Equal(steps(60)))
			Expect(out.String()).To(ContainSubstring("The retry was OOM-killed too. Exiting."))
		})

		DescribeTable("CS-RLP-028: memory.max in memoryLimit notation",
			func(content *string, want string) {
				if content != nil {
					Expect(os.WriteFile(filepath.Join(cgroup, "memory.max"), []byte(*content), 0o644)).To(Succeed())
				} else {
					Expect(os.Remove(filepath.Join(cgroup, "memory.max"))).To(Succeed())
				}
				Expect(ralphloop.ReadMemoryLimit(cgroup)).To(Equal(want))
			},
			Entry("16g", ptr("17179869184\n"), "16g"),
			Entry("1536m", ptr("1610612736\n"), "1536m"),
			Entry("unlimited", ptr("max\n"), "unlimited"),
			Entry("garbage", ptr("lots\n"), "unknown"),
			Entry("unreadable", nil, "unknown"),
		)
	})

	Describe("the cause", func() {
		DescribeTable("CS-RLP-030: the oom counter tells the container's limit from the host",
			func(before int, beforeOK bool, after int, afterOK bool, want ralphloop.OOMCause) {
				Expect(ralphloop.CauseOf(before, beforeOK, after, afterOK)).To(Equal(want))
			},
			Entry("oom rose: limit", 3, true, 4, true, ralphloop.OOMCauseLimit),
			Entry("oom unchanged: host", 3, true, 3, true, ralphloop.OOMCauseHost),
			Entry("before unreadable: unknown", 0, false, 4, true, ralphloop.OOMCauseUnknown),
			Entry("after unreadable: unknown", 3, true, 0, false, ralphloop.OOMCauseUnknown),
		)

		It("CS-RLP-030: ReadMemoryEvents parses every counter in one read; a garbage value reads as absent", func() {
			writeEvents(cgroup, 9, 4)
			ev := ralphloop.ReadMemoryEvents(cgroup)
			Expect(ev).To(HaveKeyWithValue("oom", 4))
			Expect(ev).To(HaveKeyWithValue("oom_kill", 9))
			Expect(ev).To(HaveKeyWithValue("oom_group_kill", 0))
			Expect(os.WriteFile(filepath.Join(cgroup, "memory.events"), []byte("oom x\noom_kill 2\n"), 0o644)).To(Succeed())
			Expect(ralphloop.ReadMemoryEvents(cgroup)).To(Equal(map[string]int{"oom_kill": 2}))
			Expect(ralphloop.ReadMemoryEvents(filepath.Join(work, "absent"))).To(BeNil())
		})

		It("CS-RLP-030: ReadOOMLimitHits parses the oom line, not oom_kill or oom_group_kill", func() {
			writeEvents(cgroup, 9, 4)
			n, readable := ralphloop.ReadOOMLimitHits(cgroup)
			Expect(readable).To(BeTrue())
			Expect(n).To(Equal(4))
			Expect(os.WriteFile(filepath.Join(cgroup, "memory.events"), []byte("oom_kill 2\noom_group_kill 0\n"), 0o644)).To(Succeed())
			_, readable = ralphloop.ReadOOMLimitHits(cgroup)
			Expect(readable).To(BeFalse(), "no oom line")
		})

		It("CS-RLP-030: an unreadable oom counter leaves the classification to oom_kill", func() {
			script(func(*ralphloop.Loop, int) int {
				kills++
				Expect(os.WriteFile(filepath.Join(cgroup, "memory.events"), []byte(fmt.Sprintf("oom_kill %d\n", kills)), 0o644)).To(Succeed())
				return 137
			}, ok)
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(out.String()).To(ContainSubstring("[oom]"))
			Expect(iters).To(Equal([]int{1, 1}))
		})
	})

	Describe("runlog", func() {
		It("CS-RLP-029: run-logger's entry gets the outcome; an oom entry gets claudeExit and oomKills", func() {
			opts.Limit = 1
			script(withLog(oomed(2)), withLog(ok))
			Expect(ralphloop.Run(opts)).To(Equal(0))
			got := iterations()
			Expect(got).To(HaveLen(2), "the retry's entry is annotated separately")
			Expect(got[0]).To(HaveKeyWithValue("outcome", "oom"))
			Expect(got[0]).To(HaveKeyWithValue("claudeExit", float64(137)))
			Expect(got[0]).To(HaveKeyWithValue("oomKills", float64(2)))
			Expect(got[0]).To(HaveKeyWithValue("oomCause", "limit"))
			Expect(got[0]).To(HaveKeyWithValue("sessionId", "s"), "run-logger's fields are kept")
			Expect(got[1]).To(HaveKeyWithValue("outcome", "ok"))
			Expect(got[1]).NotTo(HaveKey("oomKills"))
			Expect(got[1]).NotTo(HaveKey("oomCause"))
		})

		It("CS-RLP-029: a host OOM is recorded with oomCause host", func() {
			opts.Limit = 1
			script(withLog(hostOOMed(1)), withLog(ok))
			Expect(ralphloop.Run(opts)).To(Equal(0))
			got := iterations()
			Expect(got[0]).To(HaveKeyWithValue("outcome", "oom"))
			Expect(got[0]).To(HaveKeyWithValue("oomCause", "host"))
		})

		It("CS-RLP-029: with no run-logger entry, a non-ok outcome gets a minimal entry and ok stays unrecorded", func() {
			opts.Limit = 1
			script(oomed(1), ok)
			Expect(ralphloop.Run(opts)).To(Equal(0))
			got := iterations()
			Expect(got).To(HaveLen(1))
			Expect(got[0]).To(HaveKeyWithValue("iteration", float64(1)))
			Expect(got[0]).To(HaveKeyWithValue("outcome", "oom"))
			Expect(got[0]).To(HaveKey("endedAt"))
		})

		It("CS-RLP-029: existing outcomes are recorded too", func() {
			opts.Limit = 2
			script(withLog(func(l *ralphloop.Loop, _ int) int {
				Expect(os.WriteFile(l.MarkerFile, []byte("1\n"), 0o644)).To(Succeed())
				Expect(os.Remove(l.QuotaFile)).To(Succeed())
				return 124
			}), withLog(ok))
			Expect(ralphloop.Run(opts)).To(Equal(0))
			got := iterations()
			Expect(got[0]).To(HaveKeyWithValue("outcome", "watchdog_timeout"))
			Expect(got[1]).To(HaveKeyWithValue("outcome", "ok"))
		})
	})
})

func ptr(s string) *string { return &s }

// steps is the OOM back-off as the loop sleeps it: n one-second steps
// (CS-RLP-026), interruptible between steps.
func steps(n int) []time.Duration {
	got := make([]time.Duration, n)
	for i := range got {
		got[i] = time.Second
	}
	return got
}
