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
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/clipboard"
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
	// LabelConfigDir, LabelRegistry and LabelLaunchFlags feed the tmux pane
	// mark of an attach or join (CS-LNCH-109, CS-TMUX-012).
	LabelConfigDir   = launch.LabelConfigDir
	LabelRegistry    = launch.LabelRegistry
	LabelLaunchFlags = launch.LabelLaunchFlags
	// LabelResume is the conversation a container was created to resume, read
	// by the resume guard (CS-LNCH-110, CS-SESS-065).
	LabelResume = launch.LabelResume
	// LabelPeerRoot is the peers root the container's bridge applied, or
	// "none" (CS-DIR-012), read by the drift check (CS-DIR-017).
	LabelPeerRoot = launch.LabelPeerRoot
	// LabelTerminal is the terminal identity the container's claude sees,
	// "none", or "" on a container that predates it (CS-LNCH-181), read by
	// the attach note (CS-SESS-091).
	LabelTerminal = launch.LabelTerminal
	// LabelContinue marks a container created to continue the newest
	// conversation unforked (CS-LNCH-182), read by the continue guard
	// (CS-SESS-093 rule a').
	LabelContinue = launch.LabelContinue
	// LabelClipboard records whether the host clipboard bridge applied.
	LabelClipboard = clipboard.LabelClipboard
	// LabelShadowDir names the container's shadow directory.
	LabelShadowDir = launch.LabelShadowDir
)

// ModeRalph marks a ralph loop container.
const ModeRalph = "ralph"

// ModeHeadless marks a container an SDK client drives over stream-json
// (CS-LNCH-064).
const ModeHeadless = launch.ModeHeadless

// Session is one sandbox container: running, paused, or reserved (created).
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

	// Count is the number of live claude sessions (processes whose argv[0] is
	// claude, or a ralph loop; CS-SESS-099), so joined sessions are visible
	// and not just the container that hosts them.
	Count int `json:"sessions"`

	// State is docker's container state: "running", "paused", or "created"
	// for a reservation made by a launch between "docker create" and
	// "docker start" (CS-SESS-050); "exited" only on the rows
	// DiscoverForLaunch returns apart (CS-SESS-070). Empty when docker did
	// not report it (an older row).
	State string `json:"state"`
	// CreatedAt is when the container was created; zero when unparsable.
	// Reservations need it to recognise orphans (CS-SESS-052); the tmux pane
	// mark of an attach records it (CS-TMUX-012).
	CreatedAt time.Time `json:"-"`

	// ID is the full container id ({{.ID}} under --no-trunc), "" from an
	// older row. ConfigDirEnv, RegistryDir and LaunchFlags are the
	// CS-LNCH-109 labels; RegistryDir is "" on a container that predates them.
	ID           string `json:"-"`
	ConfigDirEnv string `json:"-"`
	RegistryDir  string `json:"-"`
	LaunchFlags  string `json:"-"`
	// Resume is the claude-sandbox.resume label (CS-LNCH-110), "" when unset
	// or on an older row.
	Resume string `json:"-"`
	// PeerRoot is the claude-sandbox.peerroot label (CS-DIR-012): the peers
	// root the bridge applied, "none", or "" on a container that predates it.
	PeerRoot string `json:"-"`
	// Terminal is the claude-sandbox.terminal label (CS-LNCH-181): "none",
	// "NAME=value", or "" on a container that predates it or an older row.
	Terminal string `json:"-"`
	// Continue is the claude-sandbox.continue label (CS-LNCH-182): true when
	// "1"; false when unset or on an older row.
	Continue bool `json:"-"`
	// Clipboard is the claude-sandbox.clipboard label (CS-LNCH-194/197):
	// "bridge", "none", or "" for a container that predates it. ShadowDir
	// is the claude-sandbox.shadowdir label, where the bridge directory
	// and the display target live.
	Clipboard string `json:"-"`
	ShadowDir string `json:"-"`
	// Mounts are the container's bind sources ({{.Mounts}} under --no-trunc,
	// split on ","), read for the peers-root pin (CS-DIR-011). nil on an
	// older row.
	Mounts []string `json:"-"`
}

// StateCreated is docker's state for a container that exists but has never
// started — the reservation a launch holds between create and start.
const StateCreated = "created"

// StateExited is docker's state for a container whose process ended. Every
// sandbox container is created --rm, so an exited one is being removed by
// docker: never a session, only a bind-source holder for the peers-root pin
// (CS-SESS-070, CS-DIR-011).
const StateExited = "exited"

// StateRestarting is docker's state for a container its restart policy is
// bringing back. Discovery never asks for it — a --rm container has no
// restart policy — but the tmux save hook counts it as live (CS-TMUX-032).
const StateRestarting = "restarting"

// exited reports whether the row is an exited container. Like Reserved, a
// row without a state falls back on docker's status text.
func (s Session) exited() bool {
	if s.State != "" {
		return s.State == StateExited
	}
	return strings.HasPrefix(s.Status, "Exited")
}

// running reports whether docker counts the container as running — "paused"
// included, since docker top works on a paused container. A row with an
// empty state is running unless its status says it is a reservation
// (CS-SESS-071).
func (s Session) running() bool {
	switch s.State {
	case "running", "paused":
		return true
	case "":
		return !s.Reserved()
	}
	return false
}

// Reserved reports whether the container is a reservation (created, never
// started) rather than a live session.
func (s Session) Reserved() bool {
	if s.State != "" {
		return s.State == StateCreated
	}
	return strings.HasPrefix(s.Status, "Created")
}

// fieldSep separates --format fields. Every field is rendered as JSON
// (CS-SESS-118), which escapes every control character, so neither this
// separator nor a newline can occur inside a field: each container is exactly
// one line with exactly psFields fields, whatever its labels or bind sources
// hold.
const fieldSep = "\x1f"

// jsonField renders one --format field as a JSON string.
func jsonField(expr string) string { return "{{json " + expr + "}}" }

// jsonLabel renders one label as a JSON string ("" when the container has no
// such label). docker ps's {{.Labels}} is one comma-joined string, ambiguous
// when a value holds a comma, so each label is its own field.
func jsonLabel(key string) string {
	ReadLabels = append(ReadLabels, key)
	return jsonField(`(.Label "` + key + `")`)
}

// ReadLabels is every label key discovery reads, in format order. Every
// launch writes each of them, empty when unset (CS-SESS-120): docker copies
// an image LABEL for a key the create does not set.
var ReadLabels []string

var psFormat = strings.Join([]string{
	jsonField(".Names"),
	jsonField(".Status"),
	jsonLabel(LabelProject),
	jsonLabel(LabelMode),
	jsonLabel(LabelInstance),
	jsonLabel(LabelVersion),
	jsonLabel(LabelModel),
	jsonLabel(LabelConfigHash),
	jsonLabel(LabelInputs),
	jsonLabel(LabelPIDClass),
	jsonLabel(LabelWorktree),
	jsonField(".State"),     // CS-SESS-050
	jsonField(".CreatedAt"), // CS-SESS-052
	jsonLabel(LabelMemoryLimit),
	jsonLabel(LabelMemoryLimitSource),
	// The slot of a retired label (claude-sandbox.keep, written by no
	// launcher): an empty JSON string, so the fields after it keep their
	// index.
	`""`,
	// CS-TMUX-012 / CS-LNCH-109: the full id (the list runs with --no-trunc)
	// and what an attach's pane mark needs.
	jsonField(".ID"),
	jsonLabel(LabelConfigDir),
	jsonLabel(LabelRegistry),
	jsonLabel(LabelLaunchFlags),
	jsonLabel(LabelResume),    // CS-SESS-065
	jsonLabel(LabelPeerRoot),  // CS-DIR-012
	jsonLabel(LabelTerminal),  // CS-SESS-091
	jsonLabel(LabelContinue),  // CS-LNCH-182
	jsonLabel(LabelClipboard), // CS-LNCH-197
	jsonLabel(LabelShadowDir), // CS-LNCH-197
	// CS-DIR-011: every bind source, comma-joined; last. docker prints a
	// source raw, so a path holding a newline would have cut the row in two
	// before this field was JSON.
	jsonField(".Mounts"),
}, fieldSep)

// psFields is the number of fields psFormat produces. docker renders the
// whole format for every container (an absent label is ""), and no JSON
// field can hold the separator or a newline, so a real row has exactly this
// many fields. Any other count did not come from this format and is never
// read (CS-SESS-118): there is no "older shape" to accept, since the format
// is this binary's own.
var psFields = strings.Count(psFormat, fieldSep) + 1

// decodeRow splits one line into its fields and decodes each from JSON.
// ok is false when the count is not psFields or a field is not a JSON
// string; f then holds the raw fields, for the warning.
func decodeRow(line string) (f []string, ok bool) {
	f = strings.Split(line, fieldSep)
	if len(f) != psFields {
		return f, false
	}
	out := make([]string, len(f))
	for i, raw := range f {
		if err := json.Unmarshal([]byte(raw), &out[i]); err != nil {
			return f, false
		}
	}
	return out, true
}

// WarnOut receives discovery's warnings about rows it drops (CS-SESS-118).
// stderr, so a headless launch's stdout stays claude's alone; tests replace it.
var WarnOut io.Writer = os.Stderr

// containerNameRe is docker's container name rule (with the leading "/"
// some docker listings print).
var containerNameRe = regexp.MustCompile(`^/?[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// ValidContainerName reports whether s follows docker's container name rule.
func ValidContainerName(s string) bool { return containerNameRe.MatchString(s) }

// dropRow warns once about a row discovery will not interpret, naming the
// container when the row's first field decodes to a valid container name;
// nothing else of the row is printed.
func dropRow(f []string, why string) {
	who := "a container"
	if len(f) > 0 {
		var name string
		if json.Unmarshal([]byte(f[0]), &name) != nil {
			name = f[0]
		}
		if containerNameRe.MatchString(name) {
			who = "container " + strings.TrimPrefix(name, "/")
		}
	}
	fmt.Fprintf(WarnOut, "WARNING: ignoring %s in the sandbox listing (%s); it is left out of discovery.\n", who, why)
}

// knownStates are the container states docker reports.
var knownStates = map[string]bool{
	StateCreated: true, "running": true, "paused": true, StateRestarting: true,
	"removing": true, StateExited: true, "dead": true,
}

var (
	fullIDRe = regexp.MustCompile(`^[0-9a-f]{12,64}$`)
	uuidRe   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// rowProblem validates the fields of a decoded row (CS-SESS-119): every
// field with a fixed form must have it (the continue label stays "1" or not,
// as CS-LNCH-182 reads it). "" = valid.
func rowProblem(s Session) string {
	switch {
	case !containerNameRe.MatchString(s.Name):
		return "its name is not a container name"
	case s.State != "" && !knownStates[s.State]:
		return "its state is not a docker container state"
	case s.ID != "" && !fullIDRe.MatchString(s.ID):
		return "its id is not a container id"
	case s.PIDClass != "" && !validClass(s.PIDClass):
		return "its pid class label is not a number from 0 to 255"
	case s.Resume != "" && !uuidRe.MatchString(s.Resume):
		return "its resume label is not a conversation id"
	}
	return ""
}

// validClass is a pid class label: a decimal 0..255 without a sign or
// leading zeros.
func validClass(v string) bool {
	n, err := strconv.Atoi(v)
	return err == nil && n >= 0 && n < 256 && strconv.Itoa(n) == v
}

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
	found, _, err := listAll(r, LabelProject, false)
	return found, err
}

// DiscoverForLaunch is DiscoverAllUncounted plus the rows it leaves out:
// exited containers, which docker is still removing (CS-SESS-070). Their
// bind sources still count for the peers-root pin (CS-DIR-011) — a --rm
// container being removed may hold the legacy root a moment longer. Same
// single docker ps.
func DiscoverForLaunch(r execx.Runner) (found, removing []Session, err error) {
	return listAll(r, LabelProject, false)
}

// list runs one docker ps and reads names, status and every label from the same
// --format output; no per-container inspect is needed.
//
// -a with four status filters (docker ORs values of one filter key) returns
// the running containers, the paused ones, the exited ones (see below), AND
// the created ones, i.e. the reservations of launches between "docker
// create" and "docker start" (CS-SESS-050). Without the reservations a concurrent launch could pick a
// noun or pid class that is already reserved. Paused containers are listed
// because plain "docker ps" always listed them (their state is "paused", not
// "running"): dropping them would hide a paused session from attach and hand
// its pid class to the next launch, overwriting its peer-registry record once
// it is unpaused.
//
// Exited containers are asked for too, but never listed: a --rm container is
// only ever exited while docker removes it. listAll returns them apart for
// the peers-root pin (CS-SESS-070, CS-DIR-011).
func list(r execx.Runner, filter string, count bool) ([]Session, error) {
	found, _, err := listAll(r, filter, count)
	return found, err
}

// listAll is list, also returning the exited rows it leaves out of the
// listing (DiscoverForLaunch).
func listAll(r execx.Runner, filter string, count bool) ([]Session, []Session, error) {
	out, err := r.Output(execx.Cmd{
		Name: "docker",
		Args: []string{"ps", "-a",
			"--filter", "label=" + filter,
			"--filter", "status=" + StateCreated, "--filter", "status=running", "--filter", "status=paused",
			"--filter", "status=" + StateExited,
			"--format", psFormat,
			// Full ids for the tmux pane mark (CS-TMUX-012); labels and
			// names are never truncated anyway.
			"--no-trunc"},
		Stderr: io.Discard,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("listing sandbox containers: %w", err)
	}
	var out2, removing []Session
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		f, ok := decodeRow(line)
		if !ok {
			// CS-SESS-118: never interpreted. Only a row that did not come
			// from this format can fail here, and reading it by index would
			// let whoever forged it choose the state, id, registry or
			// resume label discovery reads.
			why := "not a row of the listing's format"
			if len(f) != psFields {
				why = fmt.Sprintf("%d fields, not %d", len(f), psFields)
			}
			dropRow(f, why)
			continue
		}
		s := Session{
			Name: strings.TrimPrefix(f[0], "/"), Status: f[1], Project: f[2], Mode: f[3],
			Instance: f[4], Version: f[5], Model: f[6], ConfigHash: f[7],
			Inputs: launch.DecodeInputs(f[8]), PIDClass: f[9], Worktree: f[10],
			State: strings.TrimSpace(f[11]), CreatedAt: parseCreatedAt(f[12]),
			MemoryLimit: f[13], MemoryLimitSource: f[14],
			ID:           strings.TrimSpace(f[16]),
			ConfigDirEnv: f[17], RegistryDir: f[18], LaunchFlags: f[19],
			Resume:   strings.TrimSpace(f[20]),
			PeerRoot: strings.TrimSpace(f[21]),
			Terminal: strings.TrimSpace(f[22]),
			Continue: strings.TrimSpace(f[23]) == "1", // CS-LNCH-182
			// CS-LNCH-197
			Clipboard: strings.TrimSpace(f[24]), ShadowDir: f[25],
			Mounts: launch.SplitMounts(f[26]),
		}
		if why := rowProblem(s); why != "" {
			dropRow(f, why)
			continue
		}
		// An exited container is never a session: every sandbox container
		// is --rm, so docker is removing it. Only DiscoverForLaunch returns
		// it, for its bind sources (CS-SESS-070, CS-DIR-011).
		if s.exited() {
			removing = append(removing, s)
			continue
		}
		// A reservation has no processes to count, and docker top fails on a
		// container that is not running (CS-SESS-051, CS-SESS-071).
		if count && s.running() {
			s.Count = countSessions(r, s.Name)
		}
		out2 = append(out2, s)
	}
	return out2, removing, nil
}

// countSessions counts live claude sessions in a container (CS-SESS-003,
// CS-SESS-099). A failure here degrades to 0 rather than failing the whole
// listing: not being able to count processes is no reason to hide a container
// that is running (CS-SESS-004).
func countSessions(r execx.Runner, name string) int {
	out, err := r.Output(execx.Cmd{
		Name:   "docker",
		Args:   []string{"top", name, "-o", "pid,ppid,args"},
		Stderr: io.Discard,
	})
	if err != nil {
		return 0
	}
	return countTop(out)
}

// claudeInternalKinds are the second words of Claude Code's own background
// processes, which run under the same "claude" argv[0] as a session: the pty
// host and pre-warmed spare of background (--bg) sessions, and the daemon and
// its workers. None is counted (CS-SESS-099): the count is display-only and
// ps cannot reliably tell a background session from that machinery, so
// background sessions are left out with it. Like tmuxpane's claude arity
// table, this list follows the Claude Code version (checked on 2.1.301);
// refresh it on a bump.
var claudeInternalKinds = map[string]bool{
	"bg-pty-host":   true,
	"bg-spare":      true,
	"daemon":        true,
	"daemon-worker": true,
}

// topRow is one parsed "docker top" row: argv0 is the base name of the
// command's first word, arg1 its second word ("" when none).
type topRow struct {
	pid, ppid, argv0, arg1 string
}

// countTop counts the session processes in "docker top -o pid,ppid,args"
// output (CS-SESS-099). A row counts only when the base name of its argv[0]
// is exactly "claude" (an interactive or joined session) or "ralph" (the
// ralph loop) — never docker-init, tini, the pidslot helper, entrypoint.sh or
// node, whose ARGUMENTS name claude (a container running one claude used to
// count 4). A claude row whose second word is in claudeInternalKinds never
// counts, and a claude whose parent is a counted ralph loop is that loop's
// iteration, not another session.
//
// The columns are found by the header row: the command is last (it holds
// spaces) and PPID is optional, so output without it still counts by argv[0]
// (without it a ralph iteration's claude counts too).
func countTop(out string) int {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return 0
	}
	header := strings.Fields(lines[0])
	cmdCol := len(header) - 1
	if cmdCol < 1 {
		return 0
	}
	pidCol, ppidCol := 0, -1
	for i, h := range header[:cmdCol] {
		switch h {
		case "PID":
			pidCol = i
		case "PPID":
			ppidCol = i
		}
	}
	var rows []topRow
	ralph := map[string]bool{} // pids of ralph loops
	for _, line := range lines[1:] {
		f := strings.Fields(line)
		if len(f) <= cmdCol {
			continue
		}
		row := topRow{pid: f[pidCol], argv0: path.Base(f[cmdCol])}
		if len(f) > cmdCol+1 {
			row.arg1 = f[cmdCol+1]
		}
		if ppidCol >= 0 {
			row.ppid = f[ppidCol]
		}
		if row.argv0 == "ralph" {
			ralph[row.pid] = true
		}
		rows = append(rows, row)
	}
	n := 0
	for _, row := range rows {
		switch {
		case row.argv0 == "ralph":
			n++
		case row.argv0 == "claude" && !claudeInternalKinds[row.arg1] && !ralph[row.ppid]:
			n++
		}
	}
	return n
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
	args := []string{"inspect", "--type", "container", "--format", oomFormat, "--"}
	for _, s := range all {
		// Discovery admits only valid names (CS-SESS-119); checked again
		// here, since a name that is not one never reaches docker's argv.
		if containerNameRe.MatchString(s.Name) {
			args = append(args, s.Name)
		}
	}
	if args[len(args)-1] == "--" {
		return
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
// messages that need to show what is available. Every listed row counts.
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
// Every listed row counts.
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

// Live drops reservations, keeping running and paused containers: what a
// person listing sessions wants to see (CS-SESS-051).
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

// Container is what InspectContainer reads (CS-TMUX-051): the container's
// name (without docker's leading "/"), state, creation time, and the project,
// instance and mode labels a restore compares with its recorded row.
type Container struct {
	Name, State             string
	Created                 time.Time
	Project, Instance, Mode string
}

// ErrNoSuchContainer is an inspect of a container docker does not know.
var ErrNoSuchContainer = errors.New("no such container")

// inspectSep separates InspectContainer's fields. Each field is rendered as
// JSON, which escapes every control character, so a label value can hold
// neither this nor a newline (CS-SESS-118).
const inspectSep = "\x1f"

// InspectFormat is InspectContainer's -f format: name, state, creation time
// and the project, instance and mode labels, each a JSON string.
var InspectFormat = strings.Join([]string{jsonField(".Name"), jsonField(".State.Status"), jsonField(".Created"),
	inspectLabel(LabelProject), inspectLabel(LabelInstance), inspectLabel(LabelMode)}, inspectSep)

func inspectLabel(k string) string { return jsonField(`(index .Config.Labels "` + k + `")`) }

// InspectContainer is one "docker inspect --type container" of idOrName
// (CS-TMUX-051). ErrNoSuchContainer when docker reports that the container
// does not exist; any other failure is returned as it is, so a caller can
// tell "gone" from "cannot tell".
func InspectContainer(r execx.Runner, idOrName string) (Container, error) {
	var stderr strings.Builder
	out, err := r.Output(execx.Cmd{
		Name:   "docker",
		Args:   []string{"inspect", "--type", "container", "-f", InspectFormat, "--", idOrName},
		Stderr: &stderr,
	})
	if err != nil {
		if e := strings.ToLower(stderr.String() + " " + err.Error()); strings.Contains(e, "no such container") || strings.Contains(e, "no such object") {
			return Container{}, ErrNoSuchContainer
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return Container{}, fmt.Errorf("docker inspect: %s", lastLine(msg))
		}
		return Container{}, fmt.Errorf("docker inspect: %v", err)
	}
	raw := strings.Split(strings.TrimRight(out, "\r\n"), inspectSep)
	if len(raw) != 6 {
		return Container{}, fmt.Errorf("docker inspect: unexpected output")
	}
	f := make([]string, len(raw))
	for i := range raw {
		if json.Unmarshal([]byte(raw[i]), &f[i]) != nil {
			return Container{}, fmt.Errorf("docker inspect: unexpected output")
		}
	}
	c := Container{Name: strings.TrimPrefix(f[0], "/"), State: f[1], Project: f[3], Instance: f[4], Mode: f[5]}
	if t, perr := time.Parse(time.RFC3339Nano, f[2]); perr == nil {
		c.Created = t
	}
	return c, nil
}

func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
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
