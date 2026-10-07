// Package tmuxpane is claude-sandbox's side of the tmux-resurrect integration
// (spec/tmux.feature, plan .claude-sandbox/investigations/sandbox-reboot-restore/
// 05..09). tmux-resurrect + tmux-continuum own the layout; claude-sandbox
// records, in a pane user option, which sandbox each pane holds (the pane
// mark, CS-TMUX-010..019), so a save hook and `tmux restore` can bring the
// session back.
//
// Every tmux call is argv only, through the execx seam, and every failure is
// silent: the mark must never change a launch.
package tmuxpane

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/globalcfg"
	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
)

// Option is the pane user option that holds the mark.
const Option = "@claude-sandbox"

// MarkVersion is the mark's schema version ("v").
const MarkVersion = 1

// Mark states (plan 06 § 3). The launcher's own mark is always active; a
// restore writes pending while it waits to act (F4); a session that crashed
// leaves crashed (CS-TMUX-075): a restore prints its hint and never
// relaunches it. An older binary drops or clears a crashed row, never
// launches it — which is why it is a state of its own, not pending with a
// flag, and why "v" stays 1 (plan 16 § 2.3).
const (
	StateActive  = "active"
	StatePending = "pending"
	StateCrashed = "crashed"
)

// CleanExitCodes are the container exit codes that end a session cleanly
// (CS-TMUX-075): 0, and 78 — pidslot refusing to start claude without the
// global-config link (CS-GCFG-033), so claude never ran. Any other code of a
// die is a crash. Claude Code's own codes for /exit, Ctrl-D, a double Ctrl-C
// and SIGTERM are an owed host check (CS-TMUX-071's comment): a clean one
// measured non-zero belongs here.
var CleanExitCodes = []int{0, globalcfg.ExitLink}

// CleanExit reports whether code is in CleanExitCodes.
func CleanExit(code int) bool {
	for _, c := range CleanExitCodes {
		if c == code {
			return true
		}
	}
	return false
}

// Mark modes.
const (
	ModeClaude = "claude"
	ModeJoin   = "join"
	ModeRalph  = "ralph"
)

// Mark is the pane mark (CS-TMUX-011/012). Fields a later feature fills — the
// conversation id, its name and name source (the save hook, F3) — are empty
// at launch and omitted. Window labels live in window options (label.go).
type Mark struct {
	V     int    `json:"v"`
	State string `json:"state"`
	Mode  string `json:"mode"`

	Container   string `json:"container"`
	ContainerID string `json:"containerId,omitempty"`
	Instance    string `json:"instance,omitempty"`
	Project     string `json:"project"`
	// CwdRoot is where claude runs: <git root>/.claude/worktrees/<w> in
	// worktree mode, else the project (plan 06 § 10 M6).
	CwdRoot string `json:"cwdRoot,omitempty"`
	Class   string `json:"class,omitempty"`
	// Since is unix milliseconds: before the create (a new container), the
	// container's creation (attach), or before the exec (join).
	Since int64 `json:"since"`

	// ConfigDir is the host config dir the container uses; ConfigDirEnv the
	// launcher's RAW CLAUDE_CONFIG_DIR, "" when it was unset, nil when unknown
	// (a container that predates CS-LNCH-109). A restore replays the raw value
	// exactly: set only if it was set (plan 06 § 2).
	ConfigDir    string  `json:"configDir,omitempty"`
	ConfigDirEnv *string `json:"configDirEnv,omitempty"`
	RegistryDir  string  `json:"registryDir,omitempty"`

	// Worktree is always present: "" means the shared checkout, and a restore
	// replays that explicitly as --no-worktree (plan 09 finding 1).
	Worktree string `json:"worktree"`
	// WorktreeGenerated is true when the session runs in a worktree whose
	// name claude generated (a join's bare --worktree): Worktree is then ""
	// but means unknown, never the shared checkout. The save hook (F3) fills
	// it from the registry record's cwd (CS-TMUX-012).
	WorktreeGenerated bool `json:"worktreeGenerated,omitempty"`
	// Model is the launcher's --model, only when given on the command line.
	Model string `json:"model,omitempty"`

	// Replay holds the allowlisted claude flags given at launch, with their
	// values (CS-TMUX-013, decision 49). Unreplayed names — never values —
	// every other flag given at launch that a restore will not pass.
	// FlagsUnknown is true when the launch's flags cannot be known (an attach
	// to a container that predates the launchflags label).
	Replay       []string `json:"replay,omitempty"`
	Unreplayed   []string `json:"unreplayed,omitempty"`
	FlagsUnknown bool     `json:"flagsUnknown,omitempty"`

	// Labelled is true when this launch named its window and owns the label
	// (CS-TMUX-020..022). The sidecar keeps it in the row, so a restore of
	// the row — and only such a restore — may reclaim a window resurrect
	// restored under that name (case C): a hand name with the same text
	// looks exactly alike.
	Labelled bool `json:"labelled,omitempty"`

	// Filled by the save hook (F3); empty at launch.
	Conversation string `json:"conversation,omitempty"`
	Name         string `json:"name,omitempty"`
	NameSource   string `json:"nameSource,omitempty"`

	// Written only with state crashed (CS-TMUX-075): when the session child
	// returned (unix ms, the launcher's clock), the die's exit code, and
	// whether the OOM killer ended it. Printed only as a date, a number and
	// fixed words.
	EndedAt   int64 `json:"endedAt,omitempty"`
	ExitCode  int   `json:"exitCode,omitempty"`
	OOMKilled bool  `json:"oomKilled,omitempty"`
}

// JSON renders the mark compactly.
func (m Mark) JSON() string {
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}

// ParseMark reads a mark; ok is false for anything that is not a v1 mark.
func ParseMark(raw string) (Mark, bool) {
	var m Mark
	raw = strings.TrimSpace(raw)
	if raw == "" || json.Unmarshal([]byte(raw), &m) != nil || m.V != MarkVersion {
		return Mark{}, false
	}
	return m, true
}

// Pane is one tmux pane, addressed by its TMUX_PANE id.
type Pane struct {
	Runner execx.Runner
	ID     string
}

// paneID matches tmux's pane ids ("%7").
var paneID = regexp.MustCompile(`^%[0-9]+$`)

// FromEnv returns the launcher's pane when a mark applies at all
// (CS-TMUX-014): TMUX and TMUX_PANE set, and not inside a sandbox — a
// sandbox has no tmux socket, and a nested launcher's TMUX (if a cascade env
// file set one) would name a server it cannot reach. Headless and --detach
// launches never ask.
func FromEnv(getenv func(string) string) (string, bool) {
	if hostdirs.InSandbox(getenv) || getenv("TMUX") == "" {
		return "", false
	}
	id := getenv("TMUX_PANE")
	if !paneID.MatchString(id) {
		return "", false
	}
	return id, true
}

// CallTimeout bounds every tmux call (CS-TMUX-016): a hung tmux server must
// not hang the launch before docker start, nor the launcher after the session
// when no signal can interrupt it any more. The "claude --version" probe
// precedent (globalcfg.VersionTimeout).
var CallTimeout = time.Second

// syncBuffer is a bytes.Buffer safe to read while the process may still be
// writing it (a killed tmux).
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// run runs one tmux command through Runner.Start, in its own process group
// and killed with the launcher (DieWithParent), and kills it after
// CallTimeout. It returns stdout and whether tmux finished successfully.
func (p Pane) run(args ...string) (string, bool) {
	return bounded(p.Runner, CallTimeout, "tmux", args...)
}

// bounded runs name args through execx.Bounded — Runner.Start in its own
// process group, killed with the caller (DieWithParent), the whole group
// killed after timeout. It returns stdout and whether the command finished
// successfully. The save hook's docker call shares it (CS-TMUX-032/040).
func bounded(r execx.Runner, timeout time.Duration, name string, args ...string) (string, bool) {
	out, ran, werr := boundedRun(r, timeout, name, args...)
	if !ran || werr != nil {
		return "", false
	}
	return out, true
}

// boundedRun is bounded's core. ran is false when the command could not be
// started or timed out (its output is then ""); otherwise out is its stdout
// WHATEVER its exit status, and werr that status.
func boundedRun(r execx.Runner, timeout time.Duration, name string, args ...string) (out string, ran bool, werr error) {
	out, err := execx.Bounded(r, timeout, execx.Cmd{Name: name, Args: args, Stderr: io.Discard})
	var se *execx.StartError
	if errors.As(err, &se) || errors.Is(err, execx.ErrTimedOut) {
		return "", false, nil
	}
	return out, true, err
}

// SystemStopping reports whether the host is shutting down (CS-TMUX-071 row
// 2): one "systemctl is-system-running", bounded like a tmux call. Its stdout
// is read whatever its exit status — "stopping" and "degraded" exit non-zero
// — and only exactly "stopping" counts; an exec error (no systemd), a timeout
// or any other output is "not stopping".
func SystemStopping(r execx.Runner) bool {
	out, ran, _ := boundedRun(r, CallTimeout, "systemctl", "is-system-running")
	return ran && strings.TrimSpace(out) == "stopping"
}

// ContainerState is a container's docker state ("running", "exited", ...)
// from one bounded "docker inspect" (CS-TMUX-071); ok is false when the
// inspect failed, timed out or printed nothing.
func ContainerState(r execx.Runner, container string) (state string, ok bool) {
	out, ok := bounded(r, CallTimeout, "docker", "inspect", "--type", "container", "-f", "{{.State.Status}}", container)
	state = strings.TrimSpace(out)
	return state, ok && state != ""
}

// Read returns the pane's current mark, raw; "" when there is none or tmux
// failed.
func (p Pane) Read() string {
	out, ok := p.run("show-options", "-p", "-q", "-v", "-t", p.ID, Option)
	if !ok {
		return ""
	}
	return strings.TrimRight(out, "\r\n")
}

// Set writes raw as the pane's mark. Failures are silent (CS-TMUX-016).
func (p Pane) Set(raw string) {
	if raw == "" {
		return
	}
	p.run("set-option", "-p", "-t", p.ID, Option, raw)
}

// Unset removes the pane's mark. Failures are silent (CS-TMUX-016).
func (p Pane) Unset() {
	p.run("set-option", "-p", "-u", "-t", p.ID, Option)
}
