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

func writeEvents(dir string, kills int) {
	body := fmt.Sprintf("low 0\nhigh 0\nmax 12\noom 3\noom_kill %d\noom_group_kill 0\n", kills)
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
		kills = 0
		writeEvents(cgroup, kills)
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

	// oomed: claude exits 137 and the counter rises by n.
	oomed := func(n int) func(*ralphloop.Loop, int) int {
		return func(*ralphloop.Loop, int) int {
			kills += n
			writeEvents(cgroup, kills)
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
		It("CS-RLP-023: memory.events is read before and after each iteration", func() {
			var seenBefore []int
			opts.Limit = 2
			opts.RunIter = func(l *ralphloop.Loop, iter int) int {
				// The iteration sees the counter the loop sampled before it.
				n, readable := ralphloop.ReadOOMKills(cgroup)
				Expect(readable).To(BeTrue())
				seenBefore = append(seenBefore, n)
				return 0
			}
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(seenBefore).To(Equal([]int{0, 0}))
		})

		It("CS-RLP-023: ReadOOMKills parses the oom_kill line and reports unreadable input", func() {
			writeEvents(cgroup, 7)
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
			script(func(*ralphloop.Loop, int) int { kills++; writeEvents(cgroup, kills); return 0 })
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
			Expect(nonPacing()).To(Equal([]time.Duration{60 * time.Second}))
			Expect(out.String()).To(ContainSubstring("Retrying iteration 1 once in 1m0s"))
			Expect(notes[0]).To(ContainSubstring("OOM killer"))
		})

		It("CS-RLP-026: an injected OOMBackoff is honoured", func() {
			opts.OOMBackoff = 5 * time.Second
			script(oomed(1), ok)
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(nonPacing()).To(Equal([]time.Duration{5 * time.Second}))
		})

		It("CS-RLP-026: a non-oom retry resets the streak, so a later oom is retried again", func() {
			opts.Limit = 2
			script(oomed(1), ok, oomed(1), ok)
			Expect(ralphloop.Run(opts)).To(Equal(0))
			Expect(iters).To(Equal([]int{1, 1, 2, 2}))
			Expect(nonPacing()).To(Equal([]time.Duration{60 * time.Second, 60 * time.Second}))
		})

		It("CS-RLP-027: a second consecutive oom stops the loop, notifies, and exits 137", func() {
			opts.Limit = 5
			script(oomed(1), oomed(2), ok)
			Expect(ralphloop.Run(opts)).To(Equal(137))
			Expect(iters).To(Equal([]int{1, 1}))
			Expect(nonPacing()).To(Equal([]time.Duration{60 * time.Second}), "no second back-off")
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
			Expect(msg).To(ContainSubstring("claude was killed by the container's OOM killer at iteration 1 (exit 137; 1 OOM kill)"))
			Expect(msg).To(ContainSubstring("memoryLimit in effect: 16g (cgroup memory.max)"))
			Expect(msg).To(ContainSubstring("swap is off by design"))
			Expect(msg).To(ContainSubstring("raise memoryLimit in .claude-sandbox/config.yaml"))
			Expect(msg).To(ContainSubstring("cap build/test parallelism (e.g. ginkgo --procs=N, go test -p N, make -jN)"))
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
			Expect(got[0]).To(HaveKeyWithValue("sessionId", "s"), "run-logger's fields are kept")
			Expect(got[1]).To(HaveKeyWithValue("outcome", "ok"))
			Expect(got[1]).NotTo(HaveKey("oomKills"))
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
