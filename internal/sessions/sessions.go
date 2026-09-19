// Package sessions discovers sandbox containers — running ones, and the
// "created" reservations of launches in flight — and names new ones.
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
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
	"github.com/kmacmcfarlane/claude-sandbox/internal/oomreport"
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
	// LabelPIDClass records the container's pid class (CS-PID-004), read back
	// so later launches allocate without replacement across the host.
	LabelPIDClass = "claude-sandbox.pidclass"
	// LabelWorktree records the worktree the session was launched into
	// (CS-LNCH-044); empty when it runs in the shared checkout.
	LabelWorktree = "claude-sandbox.worktree"
	// LabelMemoryLimit and LabelMemoryLimitSource record the memoryLimit a
	// container was created with and its cascade source (CS-LNCH-093), read
	// back for the attach/join OOM note (CS-SESS-063).
	LabelMemoryLimit       = oomreport.LabelMemoryLimit
	LabelMemoryLimitSource = oomreport.LabelMemoryLimitSource
)

// ModeRalph marks a ralph loop container.
const ModeRalph = "ralph"

// ModeHeadless marks a container an SDK client drives over stream-json
// (CS-LNCH-064).
const ModeHeadless = launch.ModeHeadless

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
	// PIDClass is the container's pid class label, "" for containers started
	// by a launcher that predates classes.
	PIDClass string `json:"pidClass,omitempty"`
	// Worktree is the claude-sandbox.worktree label: the name handed to
	// claude's --worktree, "" for a shared-checkout session (or a container
	// from a launcher that predates worktree mode).
	Worktree string `json:"worktree,omitempty"`
	// MemoryLimit and MemoryLimitSource are the create-time memoryLimit
	// labels (CS-LNCH-093); "" on a container from an older launcher.
	MemoryLimit       string `json:"memoryLimit,omitempty"`
	MemoryLimitSource string `json:"memoryLimitSource,omitempty"`
	// OOMKilled is docker's State.OOMKilled, which is sticky: true once any
	// process in the container was OOM-killed. Discovery never sets it; only
	// MarkOOM does (CS-SESS-061).
	OOMKilled bool `json:"oomKilled,omitempty"`

	// Count is the number of live claude processes, so joined sessions are
	// visible and not just the container that hosts them.
	Count int `json:"sessions"`

	// State is docker's container state: "running", or "created" for a
	// reservation made by a launch between "docker create" and "docker start"
	// (CS-SESS-050). Empty when docker did not report it.
	State string `json:"-"`
	// CreatedAt is when the container was created; zero when unparsable. Only
	// reservations need it, to recognise orphans (CS-SESS-052).
	CreatedAt time.Time `json:"-"`
}

// StateCreated is docker's state for a container that exists but has never
// started — the reservation a launch holds between create and start.
const StateCreated = "created"

// Reserved reports whether the container is a reservation (created, never
// started) rather than a live session.
func (s Session) Reserved() bool {
	if s.State != "" {
		return s.State == StateCreated
	}
	return strings.HasPrefix(s.Status, "Created")
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
	`{{.Label "` + LabelPIDClass + `"}}`,
	`{{.Label "` + LabelWorktree + `"}}`,
	// Optional trailing fields (CS-SESS-050/052, CS-SESS-063): rows without
	// them still parse.
	"{{.State}}",
	"{{.CreatedAt}}",
	`{{.Label "` + LabelMemoryLimit + `"}}`,
	`{{.Label "` + LabelMemoryLimitSource + `"}}`,
}, fieldSep)

// psFieldCount is the minimum a row must carry; State and CreatedAt follow.
const psFieldCount = 11

// createdAtLayout parses the first three fields of docker ps's {{.CreatedAt}},
// e.g. "2026-09-18 12:34:56 -0700" of "2026-09-18 12:34:56 -0700 PDT". The
// trailing zone abbreviation is deliberately NOT parsed: in zones without a
// letter abbreviation Go renders it as the numeric offset ("+1030 +1030" on
// Lord Howe, "+0545 +0545" in Kathmandu), which an "MST" layout rejects, and a
// failed parse would make every orphan look ageless (CS-SESS-052). The
// numeric offset alone fixes the instant.
const createdAtLayout = "2006-01-02 15:04:05 -0700"

// parseCreatedAt reads a {{.CreatedAt}} value; zero when unparsable.
func parseCreatedAt(v string) time.Time {
	f := strings.Fields(v)
	if len(f) < 3 {
		return time.Time{}
	}
	t, err := time.Parse(createdAtLayout, strings.Join(f[:3], " "))
	if err != nil {
		return time.Time{}
	}
	return t
}

// Discover lists sessions for one project directory (CS-SESS-001).
func Discover(r execx.Runner, projectDir string) ([]Session, error) {
	return list(r, LabelProject+"="+projectDir, true)
}

// DiscoverAll lists sessions across every project (CS-SESS-002). Filtering on
// the bare label key matches any container carrying it.
func DiscoverAll(r execx.Runner) ([]Session, error) {
	return list(r, LabelProject, true)
}

// DiscoverAllUncounted is DiscoverAll without the per-container "docker top"
// (Count stays 0). The launch reservation runs discovery under the host lock
// and needs only nouns, classes, states and ages; counting would add one
// docker call per running sandbox on the host to the critical section
// (CS-SESS-048).
func DiscoverAllUncounted(r execx.Runner) ([]Session, error) {
	return list(r, LabelProject, false)
}

// list runs one docker ps and reads names, status and every label from the same
// --format output; no per-container inspect is needed.
//
// -a with three status filters (docker ORs values of one filter key) returns
// the running containers, the paused ones, AND the created ones, i.e. the
// reservations of launches between "docker create" and "docker start"
// (CS-SESS-050). Without the reservations a concurrent launch could pick a
// noun or pid class that is already reserved. Paused containers are listed
// because plain "docker ps" always listed them (their state is "paused", not
// "running"): dropping them would hide a paused session from attach and hand
// its pid class to the next launch, overwriting its peer-registry record once
// it is unpaused. Exited containers stay out.
func list(r execx.Runner, filter string, count bool) ([]Session, error) {
	out, err := r.Output(execx.Cmd{
		Name: "docker",
		Args: []string{"ps", "-a",
			"--filter", "label=" + filter,
			"--filter", "status=" + StateCreated, "--filter", "status=running", "--filter", "status=paused",
			"--format", psFormat},
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
			Inputs: launch.DecodeInputs(f[8]), PIDClass: f[9], Worktree: f[10],
		}
		if len(f) > 11 {
			s.State = strings.TrimSpace(f[11])
		}
		if len(f) > 12 {
			s.CreatedAt = parseCreatedAt(f[12])
		}
		if len(f) > 14 {
			s.MemoryLimit, s.MemoryLimitSource = f[13], f[14]
		}
		// A reservation has no processes to count, and docker top fails on a
		// container that is not running (CS-SESS-051).
		if count && !s.Reserved() {
			s.Count = countSessions(r, s.Name)
		}
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

// oomFormat is the batched OOM inspect's per-container line. docker renders
// {{.Name}} with a leading "/".
const oomFormat = "{{.Name}} {{.State.OOMKilled}}"

// MarkOOM sets OOMKilled on each session whose container docker reports as
// OOM-killed, with ONE "docker inspect" across all of them — "docker ps
// --format" cannot read State.OOMKilled (CS-SESS-061). Nothing runs for an
// empty list. The marker is informational, so a failure is never an error
// (CS-SESS-062). The output is parsed even when docker exits non-zero:
// docker prints the lines of the containers it found before failing on a
// missing one (a --rm container removed since the ps), so a partial failure
// keeps the marks it got; containers it did not report stay unmarked.
func MarkOOM(r execx.Runner, all []Session) {
	if len(all) == 0 {
		return
	}
	args := []string{"inspect", "--type", "container", "--format", oomFormat}
	for _, s := range all {
		args = append(args, s.Name)
	}
	out, _ := r.Output(execx.Cmd{Name: "docker", Args: args, Stderr: io.Discard})
	killed := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == "true" {
			killed[strings.TrimPrefix(f[0], "/")] = true
		}
	}
	for i := range all {
		all[i].OOMKilled = killed[all[i].Name]
	}
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

// Classes lists the pid classes in use, for class allocation (CS-PID-004).
func Classes(all []Session) []string {
	out := make([]string, 0, len(all))
	for _, s := range all {
		if s.PIDClass != "" {
			out = append(out, s.PIDClass)
		}
	}
	return out
}

// Interactive returns only the sessions a user can attach to or join. Ralph
// containers are excluded: concurrency there is owned by the ralph PID lock.
// Reservations are excluded too (CS-SESS-051): until "docker start" runs there
// is nothing to attach to or exec into. So are headless containers
// (CS-SESS-055): their stdio is an SDK client's stream-json channel, and a
// terminal attached to it would corrupt the stream.
func Interactive(all []Session) []Session {
	out := make([]Session, 0, len(all))
	for _, s := range all {
		if s.Mode != ModeRalph && s.Mode != ModeHeadless && !s.Reserved() {
			out = append(out, s)
		}
	}
	return out
}

// Live drops reservations, keeping running containers: what a person listing
// sessions means by "running" (CS-SESS-051).
func Live(all []Session) []Session {
	out := make([]Session, 0, len(all))
	for _, s := range all {
		if !s.Reserved() {
			out = append(out, s)
		}
	}
	return out
}

// ForProject keeps the sessions of one project directory, so one host-wide
// discovery can serve both the noun picker and the pid-class allocation.
func ForProject(all []Session, projectDir string) []Session {
	out := make([]Session, 0, len(all))
	for _, s := range all {
		if s.Project == projectDir {
			out = append(out, s)
		}
	}
	return out
}

// Stale returns the reservations created more than maxAge before now
// (CS-SESS-052). Create-to-start takes milliseconds, so an older reservation
// is an orphan of a launcher that died between the two. A reservation whose
// creation time is unknown is never stale: removing a live launch's container
// would be worse than leaving an orphan.
func Stale(all []Session, now time.Time, maxAge time.Duration) []Session {
	var out []Session
	for _, s := range all {
		if s.Reserved() && !s.CreatedAt.IsZero() && now.Sub(s.CreatedAt) > maxAge {
			out = append(out, s)
		}
	}
	return out
}

// Inspect reports a container's docker state ("created", "running", ...) and
// creation time. state is "" when it cannot be inspected (for instance, it no
// longer exists); created is zero when unparsable. docker inspect renders
// {{.Created}} as RFC 3339 with nanoseconds ("2026-09-18T18:03:16.123456789Z"),
// unlike docker ps's {{.CreatedAt}}, so it is not parsed with parseCreatedAt.
func Inspect(r execx.Runner, name string) (state string, created time.Time) {
	out, err := r.Output(execx.Cmd{
		Name:   "docker",
		Args:   []string{"inspect", "--type", "container", "-f", "{{.State.Status}} {{.Created}}", name},
		Stderr: io.Discard,
	})
	if err != nil {
		return "", time.Time{}
	}
	f := strings.Fields(out)
	if len(f) == 0 {
		return "", time.Time{}
	}
	if len(f) > 1 {
		if t, perr := time.Parse(time.RFC3339Nano, f[1]); perr == nil {
			created = t
		}
	}
	return f[0], created
}

// RemoveReservation removes a created container. Plain "docker rm", never -f:
// if the container has started after all, the removal fails and the session
// is left alone.
func RemoveReservation(r execx.Runner, name string) error {
	return r.Run(execx.Cmd{Name: "docker", Args: []string{"rm", name}, Stderr: io.Discard})
}

// MarshalJSON output for `sessions --json` (CS-SESS-012).
func MarshalJSON(all []Session) ([]byte, error) {
	if all == nil {
		all = []Session{}
	}
	return json.MarshalIndent(all, "", "  ")
}
