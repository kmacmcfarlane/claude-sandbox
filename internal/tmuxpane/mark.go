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
	"encoding/json"
	"io"
	"regexp"
	"strings"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
)

// Option is the pane user option that holds the mark.
const Option = "@claude-sandbox"

// MarkVersion is the mark's schema version ("v").
const MarkVersion = 1

// Mark states (plan 06 § 3). The launcher's own mark is always active; a
// restore writes pending while it waits to act (F4).
const (
	StateActive  = "active"
	StatePending = "pending"
)

// Mark modes.
const (
	ModeClaude = "claude"
	ModeJoin   = "join"
	ModeRalph  = "ralph"
)

// Mark is the pane mark (CS-TMUX-011/012). Fields a later feature fills — the
// conversation id, its name and name source (the save hook, F3) — are empty
// at launch and omitted. Window labels are not part of it (decision 52).
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

	// Filled by the save hook (F3); empty at launch.
	Conversation string `json:"conversation,omitempty"`
	Name         string `json:"name,omitempty"`
	NameSource   string `json:"nameSource,omitempty"`
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

// Read returns the pane's current mark, raw; "" when there is none or tmux
// failed.
func (p Pane) Read() string {
	out, err := p.Runner.Output(execx.Cmd{
		Name:   "tmux",
		Args:   []string{"show-options", "-p", "-q", "-v", "-t", p.ID, Option},
		Stderr: io.Discard,
	})
	if err != nil {
		return ""
	}
	return strings.TrimRight(out, "\r\n")
}

// Set writes raw as the pane's mark. Failures are silent (CS-TMUX-016).
func (p Pane) Set(raw string) {
	if raw == "" {
		return
	}
	p.Runner.Run(execx.Cmd{
		Name:   "tmux",
		Args:   []string{"set-option", "-p", "-t", p.ID, Option, raw},
		Stdout: io.Discard, Stderr: io.Discard,
	})
}

// Unset removes the pane's mark. Failures are silent (CS-TMUX-016).
func (p Pane) Unset() {
	p.Runner.Run(execx.Cmd{
		Name:   "tmux",
		Args:   []string{"set-option", "-p", "-u", "-t", p.ID, Option},
		Stdout: io.Discard, Stderr: io.Discard,
	})
}
