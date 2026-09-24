package ralphloop

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// OutcomeOOM is an iteration whose claude was killed by an OOM killer
// (CS-RLP-024): claude exited 137 AND the cgroup's oom_kill counter rose
// across the iteration. oom_kill counts kills by ANY OOM killer, the host's
// global one included; OOMCause says which (CS-RLP-030).
const OutcomeOOM Outcome = "oom"

// OOMCause is why an oom iteration's kill happened (CS-RLP-030).
type OOMCause string

const (
	// OOMCauseLimit: the container reached its own memory.max (the
	// memory.events "oom" counter rose).
	OOMCauseLimit OOMCause = "limit"
	// OOMCauseHost: "oom" did not rise, so the kill came from outside the
	// container's limit — the kernel's global OOM killer (the host ran out
	// of memory) or a parent cgroup's limit.
	OOMCauseHost OOMCause = "host"
	// OOMCauseUnknown: the "oom" counter could not be read.
	OOMCauseUnknown OOMCause = "unknown"
)

// DefaultCgroupDir is the container's own cgroup v2 root (CS-RLP-023).
const DefaultCgroupDir = "/sys/fs/cgroup"

// oomExitCode is 128+SIGKILL, what claude exits with when the OOM killer
// takes it.
const oomExitCode = 137

// oomSample is one read of memory.events: n is the oom_kill counter and
// limitHits the oom counter; ok and limitOK are false when the respective
// line could not be read. !ok disables OOM classification for the iteration
// (CS-RLP-025); !limitOK only makes the cause unknown (CS-RLP-030).
type oomSample struct {
	n         int
	ok        bool
	limitHits int
	limitOK   bool
}

// ReadOOMKills returns the oom_kill counter from <dir>/memory.events. ok is
// false when the file is missing (cgroup v1, no cgroup mount), has no
// oom_kill line, or the value does not parse — never an error or a panic.
func ReadOOMKills(dir string) (int, bool) {
	return readMemoryEvent(dir, "oom_kill")
}

// ReadOOMLimitHits returns the "oom" counter from <dir>/memory.events: the
// times the cgroup reached its own memory.max with the OOM killer as the way
// out (cgroup-v2.rst). A global OOM kill of a process inside the cgroup does
// not raise it (CS-RLP-030). ok as for ReadOOMKills.
func ReadOOMLimitHits(dir string) (int, bool) {
	return readMemoryEvent(dir, "oom")
}

func readMemoryEvent(dir, key string) (int, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, "memory.events"))
	if err != nil {
		return 0, false
	}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && f[0] == key {
			n, err := strconv.Atoi(f[1])
			if err != nil || n < 0 {
				return 0, false
			}
			return n, true
		}
	}
	return 0, false
}

// CauseOf tells an oom iteration's cause from the "oom" counter samples
// around it (CS-RLP-030): a rise is the container's limit, no rise the host.
func CauseOf(before int, beforeOK bool, after int, afterOK bool) OOMCause {
	switch {
	case !beforeOK || !afterOK:
		return OOMCauseUnknown
	case after > before:
		return OOMCauseLimit
	}
	return OOMCauseHost
}

// IsOOM reports whether an iteration was OOM-killed (CS-RLP-024/025): claude
// exited 137 and both counter samples were readable and the counter rose.
func IsOOM(claudeExit, before int, beforeOK bool, after int, afterOK bool) bool {
	return claudeExit == oomExitCode && beforeOK && afterOK && after > before
}

// ReadMemoryLimit renders <dir>/memory.max in memoryLimit notation
// (CS-RLP-028): "16g", "1536m", "unlimited" for "max", "unknown" when it
// cannot be read.
func ReadMemoryLimit(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, "memory.max"))
	if err != nil {
		return "unknown"
	}
	s := strings.TrimSpace(string(raw))
	if s == "max" {
		return "unlimited"
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return "unknown"
	}
	return FormatMemory(n)
}

// FormatMemory renders bytes in docker memory notation, choosing the largest
// unit that divides evenly (17179869184 -> "16g", 1610612736 -> "1536m").
func FormatMemory(n int64) string {
	for _, u := range []struct {
		size   int64
		suffix string
	}{{1 << 30, "g"}, {1 << 20, "m"}, {1 << 10, "k"}} {
		if n%u.size == 0 {
			return strconv.FormatInt(n/u.size, 10) + u.suffix
		}
	}
	return strconv.FormatInt(n, 10) + "b"
}

// parallelismHint is the build/test parallelism example every OOM remedy
// shares (CS-RLP-028).
const parallelismHint = "(e.g. ginkgo --procs=N, go test -p N, make -jN)"

// oomMessage is the CS-RLP-028 text: what happened and why (CS-RLP-030), the
// memoryLimit in effect, and the remedies for that cause.
func (l *Loop) oomMessage(iter, kills int, cause OOMCause) string {
	plural := "s"
	if kills == 1 {
		plural = ""
	}
	counts := fmt.Sprintf("at iteration %d (exit %d; %d OOM kill%s)", iter, oomExitCode, kills, plural)
	limit := fmt.Sprintf("memoryLimit in effect: %s (cgroup memory.max); swap is off by design.", ReadMemoryLimit(l.CgroupDir))
	const hostDoc = "see the README section \"When the host runs out of memory\""
	switch cause {
	case OOMCauseLimit:
		return fmt.Sprintf("claude was killed by the container's OOM killer %s: the container hit its memoryLimit. %s "+
			"Remedies: raise memoryLimit in .claude-sandbox/config.yaml, or cap build/test parallelism %s.",
			counts, limit, parallelismHint)
	case OOMCauseHost:
		return fmt.Sprintf("claude was killed by the host's OOM killer %s: the host ran out of memory while the container was under its memoryLimit. %s "+
			"Raising memoryLimit will not help. Remedies: run fewer sandboxes at once, or cap their build/test parallelism %s; %s.",
			counts, limit, parallelismHint, hostDoc)
	}
	return fmt.Sprintf("claude was killed by the OOM killer %s: the container's memoryLimit or the host running out of memory. %s "+
		"Remedies: at the limit, raise memoryLimit in .claude-sandbox/config.yaml; if the host ran out, run fewer sandboxes at once (%s); "+
		"either way, cap build/test parallelism %s.",
		counts, limit, hostDoc, parallelismHint)
}

func (l *Loop) sampleOOM() oomSample {
	n, ok := ReadOOMKills(l.CgroupDir)
	hits, limitOK := ReadOOMLimitHits(l.CgroupDir)
	return oomSample{n: n, ok: ok, limitHits: hits, limitOK: limitOK}
}
