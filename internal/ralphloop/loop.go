// Package ralphloop is the ralph loop runner: fresh-context Claude Code
// iterations with stop control, locking, run logging, and quota handling.
// Spec: spec/ralph-loop.feature (CS-RLP), spec/ralph-quota.feature (CS-RQT).
package ralphloop

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/paths"
)

// Options configures a loop run. Zero values take the documented defaults
// (CS-RLP-001).
type Options struct {
	WorkDir  string // project root (defaults to cwd)
	RepoRoot string // baked sandbox root (logstream stages); default /opt/claude-sandbox

	Limit            int    // 0 -> 30
	StopFile         string // "" -> <ralph-dir>/stop
	PromptFile       string // "" -> <agent-dir>/PROMPT.md
	ClaudeBin        string // "" -> claude
	Interactive      bool
	Model            string
	SkipPermissions  bool
	Resume           bool
	RunlogFile       string // "" -> <ralph-dir>/runlog.json
	RawLogBase       string // "" -> <ralph-dir>/runlogs/rawlog
	WatchdogTimeout  int    // minutes; -1 disables, 0 -> 15
	IterationTimeout int    // seconds; 0 -> 7200
	MaxRetries       int    // 0 -> 5
	RetryDelay       int    // seconds; 0 -> 30
	QuotaPause       int    // seconds; 0 -> 300
	QuotaMaxWait     int    // seconds; 0 -> 18000

	Runner execx.Runner
	Out    io.Writer
	Err    io.Writer

	// Seams (defaulted when nil).
	Sleep       func(time.Duration)
	Notify      func(msg string)            // Discord; best-effort
	RunIter     func(l *Loop, iter int) int // runs one iteration, returns exit code
	Interrupted func() bool                 // polled after each iteration
	Hostname    string
	PID         int
	PromptRalph []byte // base prompt prepended each iteration
	Rand        func(n int) int
}

// Loop is a resolved, running loop.
type Loop struct {
	Options
	RalphDir   string
	AgentDir   string
	Addendum   string
	QuotaFile  string
	StderrFile string
	MarkerFile string
	LockFile   string
}

func (o *Options) withDefaults() error {
	if o.WorkDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		o.WorkDir = wd
	}
	if o.RepoRoot == "" {
		o.RepoRoot = "/opt/claude-sandbox"
	}
	if o.Limit == 0 {
		o.Limit = 30
	}
	if o.ClaudeBin == "" {
		o.ClaudeBin = envOr("CLAUDE_BIN", "claude")
	}
	if o.WatchdogTimeout == 0 {
		o.WatchdogTimeout = 15
	}
	if o.IterationTimeout == 0 {
		o.IterationTimeout = 7200
	}
	if o.MaxRetries == 0 {
		o.MaxRetries = 5
	}
	if o.RetryDelay == 0 {
		o.RetryDelay = 30
	}
	if o.QuotaPause == 0 {
		o.QuotaPause = 300
	}
	if o.QuotaMaxWait == 0 {
		o.QuotaMaxWait = 18000
	}
	if o.Out == nil {
		o.Out = os.Stdout
	}
	if o.Err == nil {
		o.Err = os.Stderr
	}
	if o.Runner == nil {
		o.Runner = execx.System{}
	}
	if o.Sleep == nil {
		o.Sleep = time.Sleep
	}
	if o.Rand == nil {
		o.Rand = rand.Intn
	}
	if o.Hostname == "" {
		o.Hostname, _ = os.Hostname()
	}
	if o.PID == 0 {
		o.PID = os.Getpid()
	}
	if o.Interrupted == nil {
		o.Interrupted = func() bool { return false }
	}
	return nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// Run executes the loop and returns the process exit code.
func Run(o Options) int {
	if err := o.withDefaults(); err != nil {
		fmt.Fprintln(o.Err, err)
		return 1
	}
	l := &Loop{Options: o}
	l.RalphDir, _ = paths.Resolve(l.WorkDir, paths.Ralph)
	l.AgentDir, _ = paths.Resolve(l.WorkDir, paths.Agent)
	if l.StopFile == "" {
		l.StopFile = envOr("STOP_FILE", filepath.Join(l.RalphDir, "stop"))
	}
	if l.PromptFile == "" {
		l.PromptFile = envOr("PROMPT_FILE", filepath.Join(l.AgentDir, "PROMPT.md"))
	}
	if l.RunlogFile == "" {
		l.RunlogFile = filepath.Join(l.RalphDir, "runlog.json")
	}
	if l.RawLogBase == "" {
		l.RawLogBase = filepath.Join(l.RalphDir, "runlogs", "rawlog")
	}
	l.QuotaFile = filepath.Join(l.RalphDir, "temp", "quota-status")
	l.StderrFile = filepath.Join(l.RalphDir, "temp", "stderr")
	l.MarkerFile = filepath.Join(l.RalphDir, "temp", "watchdog-fired")
	l.LockFile = filepath.Join(l.RalphDir, "lock")

	// CS-RLP-004: mode addendum beside the prompt file.
	if l.Interactive {
		l.Addendum = filepath.Join(filepath.Dir(l.PromptFile), "PROMPT_INTERACTIVE.md")
	} else {
		l.Addendum = filepath.Join(filepath.Dir(l.PromptFile), "PROMPT_AUTO.md")
	}

	// CS-RLP-003: prompt files must exist.
	if !fileExists(l.PromptFile) {
		fmt.Fprintf(l.Err, "PROMPT file not found: %s\n", l.PromptFile)
		return 1
	}
	if !fileExists(l.Addendum) {
		fmt.Fprintf(l.Err, "Prompt addendum not found: %s\n", l.Addendum)
		return 1
	}
	if l.Limit <= 0 {
		fmt.Fprintln(l.Err, "--limit must be a positive integer")
		return 2
	}

	l.printBanner()

	// CS-RLP-006: runtime skeleton + runlog.
	os.MkdirAll(filepath.Join(l.RalphDir, "temp"), 0o755)
	os.MkdirAll(filepath.Join(l.RalphDir, "runlogs"), 0o755)
	if err := l.initRunlog(); err != nil {
		fmt.Fprintln(l.Err, err)
		return 1
	}
	fmt.Fprintf(l.Out, "  run-log:   %s\n", l.RunlogFile)
	fmt.Fprintf(l.Out, "  raw-log:   %s_*\n", l.RawLogBase)

	// CS-RLP-007: lock.
	if code, ok := l.acquireLock(); !ok {
		return code
	}
	defer os.Remove(l.LockFile)

	// CS-RLP-008: a stale stop file never blocks a fresh run.
	os.Remove(l.StopFile)

	return l.run()
}

func (l *Loop) printBanner() {
	model := l.Model
	if model == "" {
		model = "(default)"
	}
	mode := "non-interactive"
	if l.Interactive {
		mode = "interactive"
	}
	skip := "no"
	if l.SkipPermissions {
		skip = "yes"
	}
	watchdog := "disabled"
	if l.watchdogMinutes() > 0 {
		watchdog = fmt.Sprintf("%dm", l.watchdogMinutes())
	}
	fmt.Fprintln(l.Out, "Ralph loop starting")
	fmt.Fprintf(l.Out, "  repo:      %s\n", l.WorkDir)
	fmt.Fprintf(l.Out, "  prompt:    %s + %s\n", l.PromptFile, l.Addendum)
	fmt.Fprintf(l.Out, "  stop file: %s\n", l.StopFile)
	fmt.Fprintf(l.Out, "  claude:    %s\n", l.ClaudeBin)
	fmt.Fprintf(l.Out, "  model:     %s\n", model)
	fmt.Fprintf(l.Out, "  mode:      %s\n", mode)
	fmt.Fprintf(l.Out, "  skip-perm: %s\n", skip)
	fmt.Fprintf(l.Out, "  limit:     %d\n", l.Limit)
	fmt.Fprintf(l.Out, "  watchdog:  %s\n", watchdog)
	fmt.Fprintf(l.Out, "  iter-limit: %s\n", FormatWait(l.IterationTimeout))
}

// watchdogMinutes maps the tri-state flag: -1 disabled, else minutes.
func (l *Loop) watchdogMinutes() int {
	if l.WatchdogTimeout < 0 {
		return 0
	}
	return l.WatchdogTimeout
}

func (l *Loop) initRunlog() error {
	var runs []map[string]any
	if raw, err := os.ReadFile(l.RunlogFile); err == nil {
		json.Unmarshal(raw, &runs) // unparseable -> fresh array
	}
	if runs == nil {
		runs = []map[string]any{}
	}
	entry := map[string]any{
		"startedAt":  time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"iterations": []any{},
	}
	runs = append([]map[string]any{entry}, runs...)
	out, err := json.MarshalIndent(runs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(l.RunlogFile, append(out, '\n'), 0o644)
}

type lockInfo struct {
	PID       int    `json:"pid"`
	StartedAt string `json:"started_at"`
	Hostname  string `json:"hostname"`
}

func (l *Loop) acquireLock() (int, bool) {
	if raw, err := os.ReadFile(l.LockFile); err == nil {
		var info lockInfo
		json.Unmarshal(raw, &info)
		switch {
		case info.Hostname != "" && info.Hostname != l.Hostname:
			// Container hostnames differ per run: presumed stale.
			fmt.Fprintf(l.Err, "Warning: Stale lock file from different host/container (%s). Reclaiming.\n", info.Hostname)
		case info.PID > 0 && pidAlive(info.PID):
			fmt.Fprintf(l.Err, "Another ralph loop is active, PID %d\n", info.PID)
			return 1, false
		default:
			fmt.Fprintf(l.Err, "Warning: Stale lock file found (PID %d is dead). Reclaiming.\n", info.PID)
		}
	}
	info := lockInfo{PID: l.PID, StartedAt: time.Now().UTC().Format("2006-01-02T15:04:05Z"), Hostname: l.Hostname}
	raw, _ := json.Marshal(info)
	if err := os.WriteFile(l.LockFile, append(raw, '\n'), 0o644); err != nil {
		fmt.Fprintln(l.Err, err)
		return 1, false
	}
	return 0, true
}

func pidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0: existence probe without delivery. Must be a syscall.Signal —
	// os.Process.Signal rejects other os.Signal implementations.
	return p.Signal(syscall.Signal(0)) == nil
}

func (l *Loop) notify(msg string) {
	if l.Notify != nil {
		l.Notify(msg)
		return
	}
	NotifyDiscord(msg)
}

func (l *Loop) project() string { return filepath.Base(l.WorkDir) }

// run is the iteration state machine (CS-RLP-009..018, CS-RQT-005..012).
func (l *Loop) run() int {
	retryCount := 0
	iter := 0
	resume := l.Resume
	for {
		if fileExists(l.StopFile) {
			fmt.Fprintf(l.Out, "Stop file detected (%s). Exiting.\n", l.StopFile)
			l.notify(fmt.Sprintf("🛑 **%s** — Stop file detected. Loop exiting after iteration %d.", l.project(), iter))
			return 0
		}
		iter++
		fmt.Fprintf(l.Out, "---- iteration %d ----\n", iter)

		// CS-RLP-010: wipe scratch (kept on a resumed first iteration).
		if !resume {
			os.RemoveAll(filepath.Join(l.RalphDir, "temp"))
		}
		os.MkdirAll(filepath.Join(l.RalphDir, "temp"), 0o755)
		os.Remove(l.QuotaFile)
		os.Remove(l.StderrFile)
		os.Remove(l.MarkerFile)

		rc := l.runIteration(iter, resume)

		if l.Interrupted() {
			fmt.Fprintln(l.Out, "Interrupted. Exiting.")
			l.notify(fmt.Sprintf("🛑 **%s** — Loop interrupted by user at iteration %d.", l.project(), iter))
			return 0
		}

		outcome := Classify(rc, l.QuotaFile, l.StderrFile, l.MarkerFile)
		switch outcome {
		case OutcomeOK:
			retryCount = 0

		case OutcomeQuotaExhausted:
			fmt.Fprintln(l.Out, "[quota] Usage limit detected. Parking until quota resets.")
			l.notify(fmt.Sprintf("⏸ **%s** — Quota exhausted at iteration %d. Parking. Will probe every %s for up to %s.",
				l.project(), iter, FormatWait(l.QuotaPause), FormatWait(l.QuotaMaxWait)))
			waited, restored := l.parkForQuota()
			if !restored {
				fmt.Fprintf(l.Out, "[quota] Quota did not reset within %s. Exiting.\n", FormatWait(l.QuotaMaxWait))
				l.notify(fmt.Sprintf("⛔ **%s** — Quota did not reset within %s. Exiting at iteration %d.",
					l.project(), FormatWait(l.QuotaMaxWait), iter))
				return 0
			}
			fmt.Fprintf(l.Out, "[quota] Quota restored after %s!\n", FormatWait(waited))
			l.notify(fmt.Sprintf("▶ **%s** — Quota restored after %s. Resuming at iteration %d.", l.project(), FormatWait(waited), iter))
			iter-- // re-run the same iteration
			retryCount = 0

		case OutcomeRateLimit:
			retryCount++
			if retryCount > l.MaxRetries {
				fmt.Fprintf(l.Out, "[quota] Max retries exceeded (%d). Exiting.\n", l.MaxRetries)
				l.notify(fmt.Sprintf("⛔ **%s** — Rate limit retries exhausted (%d) at iteration %d. Exiting.", l.project(), l.MaxRetries, iter))
				return 1
			}
			delay := l.backoff(retryCount)
			fmt.Fprintf(l.Out, "[quota] Rate limit detected. Retrying in %s (attempt %d/%d)...\n", FormatWait(delay), retryCount, l.MaxRetries)
			l.Sleep(time.Duration(delay) * time.Second)
			iter-- // re-run the same iteration

		case OutcomeWatchdogTimeout:
			fmt.Fprintf(l.Out, "[watchdog] Iteration %d timed out after %dm of inactivity. Continuing to next iteration.\n", iter, l.watchdogMinutes())
			l.notify(fmt.Sprintf("⏱ **%s** — Iteration %d killed by watchdog (%dm inactivity). Continuing.", l.project(), iter, l.watchdogMinutes()))
			retryCount = 0

		case OutcomeIterationTimeout:
			fmt.Fprintf(l.Out, "[timeout] Iteration %d hit hard time limit (%ds). Continuing to next iteration.\n", iter, l.IterationTimeout)
			l.notify(fmt.Sprintf("⏱ **%s** — Iteration %d hit hard time limit (%s). Continuing.", l.project(), iter, FormatWait(l.IterationTimeout)))
			retryCount = 0

		default: // OutcomeError
			fmt.Fprintf(l.Out, "Claude process exited non-zero (rc=%d). Exiting.\n", rc)
			l.notify(fmt.Sprintf("❌ **%s** — Iteration %d exited with error (rc=%d). Loop stopped.", l.project(), iter, rc))
			return rc
		}

		if iter >= l.Limit {
			fmt.Fprintf(l.Out, "Iteration limit reached (%d). Exiting.\n", l.Limit)
			l.notify(fmt.Sprintf("🏁 **%s** — Iteration limit reached (%d). Loop complete.", l.project(), l.Limit))
			return 0
		}
		l.Sleep(3 * time.Second) // CS-RLP-018
		resume = false
	}
}

// backoff computes the rate-limit delay: RetryDelay * 2^(attempt-1), capped
// at 300s, jittered by 0.75–1.0 (CS-RQT-009).
func (l *Loop) backoff(attempt int) int {
	delay := l.RetryDelay * (1 << (attempt - 1))
	if delay > 300 {
		delay = 300
	}
	jitter := 75 + l.Rand(26)
	return delay * jitter / 100
}

// parkForQuota sleeps and probes until quota is restored or the cap is
// reached (CS-RQT-006/007).
func (l *Loop) parkForQuota() (waited int, restored bool) {
	for waited < l.QuotaMaxWait {
		fmt.Fprintf(l.Out, "[quota] Sleeping %s (%s / %s waited)...\n", FormatWait(l.QuotaPause), FormatWait(waited), FormatWait(l.QuotaMaxWait))
		l.Sleep(time.Duration(l.QuotaPause) * time.Second)
		waited += l.QuotaPause
		fmt.Fprintln(l.Out, "[quota] Probing quota status...")
		if l.probeQuota() {
			return waited, true
		}
		fmt.Fprintln(l.Out, "[quota] Still exhausted.")
	}
	return waited, false
}

func (l *Loop) probeQuota() bool {
	var buf bytes.Buffer
	l.Runner.Run(execx.Cmd{
		Name:   l.ClaudeBin,
		Args:   []string{"-p", "ping", "--output-format", "stream-json"},
		Stdout: &buf,
		Stderr: io.Discard,
	})
	// First lines only, mirroring `head -5`.
	lines := strings.SplitN(buf.String(), "\n", 6)
	if len(lines) > 5 {
		lines = lines[:5]
	}
	return ProbeVerdict(strings.Join(lines, "\n"))
}

// claudeArgs assembles the per-iteration claude argv (CS-RLP-012).
func (l *Loop) claudeArgs(resume bool) []string {
	var args []string
	if !l.Interactive {
		args = append(args, "-p")
	}
	if l.SkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	if l.Model != "" {
		args = append(args, "--model", l.Model)
	}
	if resume {
		args = append(args, "--resume")
	}
	if !l.Interactive {
		args = append(args, "--verbose", "--output-format", "stream-json")
	}
	return args
}

// promptData concatenates the prompt inputs with blank-line separators
// (CS-RLP-011).
func (l *Loop) promptData() ([]byte, error) {
	parts := [][]byte{}
	if len(l.PromptRalph) > 0 {
		parts = append(parts, l.PromptRalph)
	} else if raw, err := os.ReadFile(filepath.Join(l.RepoRoot, "PROMPT_RALPH.md")); err == nil {
		parts = append(parts, raw)
	}
	for _, f := range []string{l.PromptFile, l.Addendum} {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		parts = append(parts, raw)
	}
	return bytes.Join(parts, []byte("\n\n")), nil
}

func (l *Loop) runIteration(iter int, resume bool) int {
	if l.RunIter != nil {
		return l.RunIter(l, iter)
	}
	return l.runIterationReal(iter, resume)
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// ParsePositiveInt validates --limit style flags (CS-RLP-002).
func ParsePositiveInt(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("must be a positive integer")
	}
	return n, nil
}
