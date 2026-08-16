// Package sessions discovers running sandbox containers and names new ones.
// Spec: spec/sessions.feature (CS-SESS).
//
// Discovery is by container label, never by parsing container names. Names are
// lossy — normalized and hashed — so the absolute project directory cannot be
// recovered from one. Labels also keep discovery independent of the naming
// scheme, so changing how containers are named cannot silently break it.
package sessions

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
)

// Label keys written by the launcher (CS-LNCH-032).
const (
	LabelProject    = "claude-sandbox.project"
	LabelMode       = "claude-sandbox.mode"
	LabelInstance   = "claude-sandbox.instance"
	LabelVersion    = "claude-sandbox.version"
	LabelModel      = "claude-sandbox.model"
	LabelConfigHash = "claude-sandbox.confighash"
	LabelInputs     = "claude-sandbox.inputs"
)

// ModeRalph marks a ralph loop container.
const ModeRalph = "ralph"

// Session is one running sandbox container.
type Session struct {
	Name       string               `json:"name"`
	Project    string               `json:"project"`
	Mode       string               `json:"mode"`
	Instance   string               `json:"instance"`
	Version    string               `json:"version,omitempty"`
	Model      string               `json:"model,omitempty"`
	Status     string               `json:"status"`
	ConfigHash string               `json:"configHash,omitempty"`
	Inputs     []launch.InputDigest `json:"-"`

	// Count is the number of live claude processes, so joined sessions are
	// visible and not just the container that hosts them.
	Count int `json:"sessions"`
}

// fieldSep separates --format fields. Label values are arbitrary text (the
// inputs label is JSON containing commas, quotes and braces), so the separator
// has to be something no path, status or JSON payload contains.
const fieldSep = "\x1f"

var psFormat = strings.Join([]string{
	"{{.Names}}",
	"{{.Status}}",
	`{{.Label "` + LabelProject + `"}}`,
	`{{.Label "` + LabelMode + `"}}`,
	`{{.Label "` + LabelInstance + `"}}`,
	`{{.Label "` + LabelVersion + `"}}`,
	`{{.Label "` + LabelModel + `"}}`,
	`{{.Label "` + LabelConfigHash + `"}}`,
	`{{.Label "` + LabelInputs + `"}}`,
}, fieldSep)

const psFieldCount = 9

// Discover lists sessions for one project directory (CS-SESS-001).
func Discover(r execx.Runner, projectDir string) ([]Session, error) {
	return list(r, LabelProject+"="+projectDir)
}

// DiscoverAll lists sessions across every project (CS-SESS-002). Filtering on
// the bare label key matches any container carrying it.
func DiscoverAll(r execx.Runner) ([]Session, error) {
	return list(r, LabelProject)
}

// list runs one docker ps and reads names, status and every label from the same
// --format output; no per-container inspect is needed.
func list(r execx.Runner, filter string) ([]Session, error) {
	out, err := r.Output(execx.Cmd{
		Name:   "docker",
		Args:   []string{"ps", "--filter", "label=" + filter, "--format", psFormat},
		Stderr: io.Discard,
	})
	if err != nil {
		return nil, fmt.Errorf("listing sandbox containers: %w", err)
	}
	var out2 []Session
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, fieldSep)
		if len(f) < psFieldCount {
			continue // malformed row: skip it, keep the rest (CS-SESS-006)
		}
		s := Session{
			Name: f[0], Status: f[1], Project: f[2], Mode: f[3],
			Instance: f[4], Version: f[5], Model: f[6], ConfigHash: f[7],
			Inputs: launch.DecodeInputs(f[8]),
		}
		s.Count = countSessions(r, s.Name)
		out2 = append(out2, s)
	}
	return out2, nil
}

// countSessions counts live claude processes in a container (CS-SESS-003).
// A failure here degrades to 0 rather than failing the whole listing: not being
// able to count processes is no reason to hide a container that is running
// (CS-SESS-004).
func countSessions(r execx.Runner, name string) int {
	out, err := r.Output(execx.Cmd{
		Name:   "docker",
		Args:   []string{"top", name, "-o", "pid,args"},
		Stderr: io.Discard,
	})
	if err != nil {
		return 0
	}
	n := 0
	for i, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if i == 0 {
			continue // header row
		}
		if isClaudeProcess(line) {
			n++
		}
	}
	return n
}

// isClaudeProcess reports whether a `docker top` row is a claude session, as
// opposed to a helper process the session spawned.
func isClaudeProcess(row string) bool {
	fields := strings.Fields(row)
	if len(fields) < 2 {
		return false
	}
	cmd := strings.Join(fields[1:], " ")
	// The ralph loop runs the binary directly; interactive sessions run claude.
	return strings.Contains(cmd, "claude") || strings.Contains(cmd, "/bin/ralph")
}

// Attachable returns the host pid of the container's attachable process, i.e.
// its PID 1 (CS-SESS-005).
//
// Only PID 1 can be reached with `docker attach`; a process started by
// `docker exec` has its stdio bound to that exec client and becomes
// unreachable once the client is gone. PID is the way to identify it: every
// row from `docker top` shares the containerd shim as PPID, and the TTY column
// is "?" unless the container was started with -t, so neither distinguishes.
func Attachable(r execx.Runner, name string) (int, error) {
	out, err := r.Output(execx.Cmd{
		Name:   "docker",
		Args:   []string{"inspect", "-f", "{{.State.Pid}}", name},
		Stderr: io.Discard,
	})
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

// ByInstance finds a session by its instance noun.
func ByInstance(all []Session, instance string) (Session, bool) {
	for _, s := range all {
		if s.Instance == instance {
			return s, true
		}
	}
	return Session{}, false
}

// Instances lists the instance nouns in use, for noun selection and for error
// messages that need to show what is available.
func Instances(all []Session) []string {
	out := make([]string, 0, len(all))
	for _, s := range all {
		if s.Instance != "" {
			out = append(out, s.Instance)
		}
	}
	return out
}

// Interactive returns only the sessions a user can attach to or join. Ralph
// containers are excluded: concurrency there is owned by the ralph PID lock.
func Interactive(all []Session) []Session {
	out := make([]Session, 0, len(all))
	for _, s := range all {
		if s.Mode != ModeRalph {
			out = append(out, s)
		}
	}
	return out
}

// MarshalJSON output for `sessions --json` (CS-SESS-012).
func MarshalJSON(all []Session) ([]byte, error) {
	if all == nil {
		all = []Session{}
	}
	return json.MarshalIndent(all, "", "  ")
}
