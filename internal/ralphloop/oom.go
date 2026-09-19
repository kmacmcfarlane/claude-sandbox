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

// OutcomeOOM is an iteration whose claude was killed by the container's OOM
// killer (CS-RLP-024): claude exited 137 AND the cgroup's oom_kill counter
// rose across the iteration.
const OutcomeOOM Outcome = "oom"

// DefaultCgroupDir is the container's own cgroup v2 root (CS-RLP-023).
const DefaultCgroupDir = "/sys/fs/cgroup"

// oomExitCode is 128+SIGKILL, what claude exits with when the OOM killer
// takes it.
const oomExitCode = 137

// oomSample is one read of the oom_kill counter; ok is false when it could
// not be read, which disables OOM classification for the iteration
// (CS-RLP-025).
type oomSample struct {
	n  int
	ok bool
}

// ReadOOMKills returns the oom_kill counter from <dir>/memory.events. ok is
// false when the file is missing (cgroup v1, no cgroup mount), has no
// oom_kill line, or the value does not parse — never an error or a panic.
func ReadOOMKills(dir string) (int, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, "memory.events"))
	if err != nil {
		return 0, false
	}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && f[0] == "oom_kill" {
			n, err := strconv.Atoi(f[1])
			if err != nil || n < 0 {
				return 0, false
			}
			return n, true
		}
	}
	return 0, false
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

// oomMessage is the CS-RLP-028 text: what happened, the memoryLimit in
// effect, and the remedies.
func (l *Loop) oomMessage(iter, kills int) string {
	plural := "s"
	if kills == 1 {
		plural = ""
	}
	return fmt.Sprintf("claude was killed by the container's OOM killer at iteration %d (exit %d; %d OOM kill%s). "+
		"memoryLimit in effect: %s (cgroup memory.max); swap is off by design. "+
		"Remedies: raise memoryLimit in .claude-sandbox/config.yaml, or cap build/test parallelism "+
		"(e.g. ginkgo --procs=N, go test -p N, make -jN).",
		iter, oomExitCode, kills, plural, ReadMemoryLimit(l.CgroupDir))
}

func (l *Loop) sampleOOM() oomSample {
	n, ok := ReadOOMKills(l.CgroupDir)
	return oomSample{n: n, ok: ok}
}
