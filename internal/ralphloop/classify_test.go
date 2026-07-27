package ralphloop_test

// Spec: spec/ralph-quota.feature CS-RQT-001..004 (Classify) and CS-RQT-008
// (ProbeVerdict). Verdict inputs are written to real temp files, matching how
// run-logger and the pipeline leave them behind.

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/ralphloop"
)

var _ = Describe("Classify", func() {
	var quotaFile, stderrFile, markerFile string

	BeforeEach(func() {
		tmp := GinkgoT().TempDir()
		quotaFile = filepath.Join(tmp, "quota-status")
		stderrFile = filepath.Join(tmp, "stderr")
		markerFile = filepath.Join(tmp, "watchdog-fired")
	})

	// classify writes the given inputs (empty string = file absent) and runs
	// the classifier.
	classify := func(status, stderr string, marker bool, code int) ralphloop.Outcome {
		if status != "" {
			Expect(os.WriteFile(quotaFile, []byte(status+"\n"), 0o644)).To(Succeed())
		}
		if stderr != "" {
			Expect(os.WriteFile(stderrFile, []byte(stderr), 0o644)).To(Succeed())
		}
		if marker {
			Expect(os.WriteFile(markerFile, []byte("1\n"), 0o644)).To(Succeed())
		}
		return ralphloop.Classify(code, quotaFile, stderrFile, markerFile)
	}

	DescribeTable("outcome precedence: status file > stderr patterns > exit code",
		func(status, stderr string, marker bool, code int, want ralphloop.Outcome) {
			Expect(classify(status, stderr, marker, code)).To(Equal(want))
		},

		// CS-RQT-001: the structured status file trumps the exit code.
		Entry("CS-RQT-001: status quota_exhausted with exit 0",
			"quota_exhausted", "", false, 0, ralphloop.OutcomeQuotaExhausted),
		Entry("CS-RQT-001: status rate_limit with exit 0",
			"rate_limit", "", false, 0, ralphloop.OutcomeRateLimit),
		Entry("CS-RQT-001: status ok with exit 141 (SIGPIPE teardown noise)",
			"ok", "", false, 141, ralphloop.OutcomeOK),
		Entry("CS-RQT-001: status ok with exit 1 (is_error session)",
			"ok", "", false, 1, ralphloop.OutcomeOK),
		Entry("CS-RQT-001: status ok trumps stderr usage-limit patterns",
			"ok", "usage limit reached", false, 1, ralphloop.OutcomeOK),

		// CS-RQT-002: stderr patterns classify API-level failures.
		Entry("CS-RQT-002: stderr 'usage limit' is quota_exhausted",
			"", "Claude AI usage limit reached", false, 0, ralphloop.OutcomeQuotaExhausted),
		Entry("CS-RQT-002: stderr 'out of ... usage' is quota_exhausted",
			"", "You are out of Sonnet usage", false, 0, ralphloop.OutcomeQuotaExhausted),
		Entry("CS-RQT-002: stderr 'rate limit' is rate_limit",
			"", "API rate limit hit", false, 0, ralphloop.OutcomeRateLimit),
		Entry("CS-RQT-002: stderr 'rate_limit' is rate_limit",
			"", `{"error":"rate_limit_error"}`, false, 0, ralphloop.OutcomeRateLimit),
		Entry("CS-RQT-002: stderr 'overloaded' is rate_limit",
			"", "overloaded_error", false, 0, ralphloop.OutcomeRateLimit),
		Entry("CS-RQT-002: usage-limit wins when both pattern classes match",
			"", "rate limit: usage limit reached", false, 0, ralphloop.OutcomeQuotaExhausted),

		// CS-RQT-003: exit 124 splits watchdog vs hard timeout on the marker.
		Entry("CS-RQT-003: exit 124 with watchdog marker is watchdog_timeout",
			"", "", true, 124, ralphloop.OutcomeWatchdogTimeout),
		Entry("CS-RQT-003: exit 124 without marker is iteration_timeout",
			"", "", false, 124, ralphloop.OutcomeIterationTimeout),

		// CS-RQT-004: plain exit codes.
		Entry("CS-RQT-004: exit 0 is ok",
			"", "", false, 0, ralphloop.OutcomeOK),
		Entry("CS-RQT-004: exit 1 is error",
			"", "", false, 1, ralphloop.OutcomeError),
		Entry("CS-RQT-004: exit 143 is error",
			"", "", false, 143, ralphloop.OutcomeError),
	)

	It("CS-RQT-001: an unrecognized status file value falls through to the exit code", func() {
		Expect(classify("bogus", "", false, 0)).To(Equal(ralphloop.OutcomeOK))
	})
})

var _ = Describe("ProbeVerdict", func() {
	DescribeTable("quota-probe output interpretation",
		func(out string, restored bool) {
			Expect(ralphloop.ProbeVerdict(out)).To(Equal(restored))
		},
		Entry("CS-RQT-008: usage-limit text means still exhausted",
			`{"type":"result","result":"You have hit your usage limit"}`, false),
		Entry("CS-RQT-008: a result event means restored",
			`{"type":"result","subtype":"success"}`, true),
		Entry("CS-RQT-008: any other JSON event means restored",
			`{"type":"system","subtype":"init"}`, true),
		Entry("CS-RQT-008: no recognizable output means still exhausted",
			"", false),
		Entry("CS-RQT-008: plain text output means still exhausted",
			"command not found", false),
	)
})
