package ralphloop

import (
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Outcome classifies one iteration's result (CS-RQT-001..004).
type Outcome string

const (
	OutcomeOK               Outcome = "ok"
	OutcomeQuotaExhausted   Outcome = "quota_exhausted"
	OutcomeRateLimit        Outcome = "rate_limit"
	OutcomeWatchdogTimeout  Outcome = "watchdog_timeout"
	OutcomeIterationTimeout Outcome = "iteration_timeout"
	OutcomeError            Outcome = "error"
)

var (
	rateLimitRe  = regexp.MustCompile(`(?i)rate.limit|rate_limit|usage.limit|out of .* usage|overloaded`)
	usageLimitRe = regexp.MustCompile(`(?i)usage.limit|out of .* usage`)
)

// Classify determines the iteration outcome. Precedence: the structured
// quota-status file (written by run-logger) trumps everything — "ok" with a
// non-zero exit code is teardown noise (SIGPIPE from exit-on-result, or
// claude exiting non-zero after an is_error session) and the loop continues.
// Then stderr patterns catch API-level errors not in the JSON stream. Exit
// code 124 splits watchdog vs hard timeout via the watchdog marker file.
func Classify(exitCode int, quotaStatusFile, stderrFile, watchdogMarkerFile string) Outcome {
	if raw, err := os.ReadFile(quotaStatusFile); err == nil {
		switch strings.TrimSpace(string(raw)) {
		case "quota_exhausted":
			return OutcomeQuotaExhausted
		case "rate_limit":
			return OutcomeRateLimit
		case "ok":
			return OutcomeOK
		}
	}
	if raw, err := os.ReadFile(stderrFile); err == nil && rateLimitRe.Match(raw) {
		if usageLimitRe.Match(raw) {
			return OutcomeQuotaExhausted
		}
		return OutcomeRateLimit
	}
	if exitCode == 124 {
		if _, err := os.Stat(watchdogMarkerFile); err == nil {
			return OutcomeWatchdogTimeout
		}
		return OutcomeIterationTimeout
	}
	if exitCode == 0 {
		return OutcomeOK
	}
	return OutcomeError
}

// ProbeVerdict interprets a quota-probe's captured output (CS-RQT-008):
// restored only when the output shows the API answering again.
func ProbeVerdict(out string) bool {
	if usageLimitRe.MatchString(out) {
		return false
	}
	if strings.Contains(out, `"type":"result"`) {
		return true
	}
	if strings.Contains(out, `"type"`) {
		return true
	}
	return false
}

// FormatWait renders seconds as a human-readable duration (2h30m, 5m10s, 45s).
func FormatWait(secs int) string {
	switch {
	case secs >= 3600:
		return strconv.Itoa(secs/3600) + "h" + strconv.Itoa(secs%3600/60) + "m"
	case secs >= 60:
		return strconv.Itoa(secs/60) + "m" + strconv.Itoa(secs%60) + "s"
	default:
		return strconv.Itoa(secs) + "s"
	}
}
