package tmuxpane

// The restore decision (CS-TMUX-051, plan sandbox-reboot-restore 10 § 4.1 as
// amended by 11 § 9): what `tmux restore` does with one recorded row. Decide
// is pure over its Probes; the dry-run prints its answer and F4b acts on it.
// The probes here only read: one docker version, one docker inspect, one
// list-panes, the resume guard (through the caller) and the global-config
// layout.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/globalcfg"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
)

// The pause after a resumed session is up, by the host's global-config
// layout (answer 50 d, 10 § 5.3; the values are operator answer 66 a, as
// built): a linked ~/.claude.json or a relocated CLAUDE_CONFIG_DIR tree is
// safe under concurrent writes, so the next start waits for "up" only; any
// other layout — a .config.json, the legacy regular file, a missing or
// refused one — spaces the starts.
const (
	LinkedGap     = 0 * time.Second
	RelocatedGap  = 0 * time.Second
	ConfigJSONGap = 10 * time.Second
	LegacyGap     = 10 * time.Second
)

// Outcome is what a restore does with a row.
type Outcome string

const (
	// OutcomeNone: nothing recorded for the pane; its mark is untouched.
	OutcomeNone Outcome = "nothing"
	// OutcomeClear: a final answer; the restore unsets the pane's mark.
	OutcomeClear Outcome = "clear"
	// OutcomePending: may change on a retry; the pending mark stays.
	OutcomePending Outcome = "pending"
	// OutcomeAttach and OutcomeResume start a session.
	OutcomeAttach Outcome = "attach"
	OutcomeResume Outcome = "resume"
	// OutcomeHint: a crashed row (row 19, CS-TMUX-077); the restore prints
	// the hint, forgets the pane's crashed mark and starts nothing.
	OutcomeHint Outcome = "hint"
)

// Decision is Decide's answer for one row.
type Decision struct {
	// Row is the number of the decision table row that matched (10 § 4.1).
	Row     int
	Outcome Outcome
	// Line is what the restore prints.
	Line string
	// Manual is the exact manual resume command, when there is one.
	Manual string
	// Attach is the container an attach would use (its 64-hex id).
	Attach string
	// Gap is the pause after a resume is up (OutcomeResume only).
	Gap time.Duration
	// Notes are printed before a resume (the flags it does not replay).
	Notes []string
}

// Would says in a few words what a restore would do, for the dry-run.
func (d Decision) Would() string {
	switch d.Outcome {
	case OutcomeAttach:
		return "attach to the running container " + d.Attach
	case OutcomeResume:
		w := "resume the conversation in a new container, after the restore start lock; then wait until it is up"
		if d.Gap > 0 {
			w += fmt.Sprintf(" and %d s more (this host's global config is not linked)", int(d.Gap/time.Second))
		}
		return w
	case OutcomeClear:
		return "clear the pane's mark"
	case OutcomePending:
		return "keep the pane's mark pending (retry: claude-sandbox tmux restore)"
	case OutcomeHint:
		return "print the crash hint and forget the row; nothing is started"
	}
	return "nothing; the pane is left as it is"
}

// ContainerInfo is what the row's container looks like now.
type ContainerInfo struct {
	State, Name, Project, Instance string
}

// ErrNoContainer is an inspect that found no such container.
var ErrNoContainer = errors.New("no such container")

// GuardResult is the resume guard's answer for a row (rows 15–17).
type GuardResult struct {
	Open bool
	// Holder names the sandbox holding the conversation ("'noun'
	// (container)" or the container); AttachCommand reaches it, "" when it
	// cannot be attached.
	Holder        string
	AttachCommand string
	// HolderReserved: the holder is a reservation ("created", never
	// started) — an orphan of a launcher that died between its create and
	// its start, or a launch about to start. It holds the id for the guard
	// (CS-SESS-065 rule a), but it is not a session to attach to, so the
	// row stays pending (CS-TMUX-051 row 15).
	HolderReserved bool
	HostPID        int
	Reason         string
	// Orphans name reservations for the conversation older than the
	// reclaim age (60 s, CS-SESS-052): they hold nothing, and the resume's
	// launch removes them under the launch lock.
	Orphans []string
}

// Probes are Decide's read-only looks at the world, called lazily in table
// order so a row decided early costs nothing more.
type Probes interface {
	// IsDir reports whether the project is a directory (row 7).
	IsDir(path string) bool
	// Docker is nil when docker answers (row 8).
	Docker() error
	// Inspect looks at the row's container by its 64-hex id (rows 9–12);
	// ErrNoContainer when docker knows no such container.
	Inspect(id string) (ContainerInfo, error)
	// OnScreen names another pane ("s:w.p") whose active mark holds the
	// container and which runs the launcher (row 9).
	OnScreen(id string) (string, bool)
	// Guard runs the resume guard for the row's conversation (rows 15–17).
	Guard(m Mark) GuardResult
	// Gap is the pause a resume of m would take after "up" (row 18).
	Gap(m Mark) time.Duration
}

// RowName is how a line names a row's session: its conversation name, else
// its noun, else its container.
func RowName(m Mark) string {
	switch {
	case m.Name != "":
		return m.Name
	case m.Instance != "":
		return m.Instance
	}
	return m.Container
}

// Decide is the decision table (CS-TMUX-051; 10 § 4.1). row nil is "nothing
// recorded" at coords. Rows 1 (refusals) and 6 (the sparse line) are the
// caller's: neither decides a row. Row 19 (a crashed row, CS-TMUX-077) is
// decided right after row 3 and calls no probe, so a caller may pass nil
// Probes for rows 2, 3 and 19 — a nil that a later edit lets a crashed row
// past panics, loudly.
func Decide(row *Row, coords string, p Probes) Decision {
	if row == nil {
		return Decision{Row: 2, Outcome: OutcomeNone,
			Line: "nothing recorded for this pane (" + coords + ") — the shell is yours"}
	}
	m := row.Mark
	if field := ValidateRow(m); field != "" {
		return Decision{Row: 3, Outcome: OutcomeClear,
			Line: "cannot use the recorded row for " + coords + ": its " + field + " is not valid"}
	}
	if m.State == StateCrashed {
		// Answer 64 c: a crashed session is never relaunched. ValidateRow
		// required mode claude and a conversation.
		return Decision{Row: 19, Outcome: OutcomeHint, Line: CrashHint(m), Manual: ResumeCommand(m)}
	}
	name := "'" + RowName(m) + "'"
	manual := ResumeCommand(m)
	pending := func(row int, line string) Decision {
		return Decision{Row: row, Outcome: OutcomePending, Line: line, Manual: manual}
	}
	switch m.Mode {
	case ModeRalph:
		return Decision{Row: 4, Outcome: OutcomeClear,
			Line: "a ralph run was here (" + m.Project + "); not restarted — run: cd " + shq(m.Project) + " && claude-sandbox --ralph"}
	case ModeJoin:
		who := RowName(m)
		if m.Name == "" && m.Conversation != "" {
			who = m.Conversation
		}
		return Decision{Row: 5, Outcome: OutcomeClear, Manual: manual,
			Line: "a joined session was here (" + who + "); joins are not restored"}
	}
	if !p.IsDir(m.Project) {
		return pending(7, "the project "+m.Project+" of "+name+" is missing or not a directory")
	}
	if err := p.Docker(); err != nil {
		return pending(8, "docker does not answer ("+err.Error()+"); "+name+" cannot be checked")
	}
	if hex64RE.MatchString(m.ContainerID) {
		info, err := p.Inspect(m.ContainerID)
		switch {
		case errors.Is(err, ErrNoContainer):
		case err != nil:
			return pending(12, "cannot tell whether "+name+" still runs ("+err.Error()+")")
		case info.Project != m.Project || info.Instance != m.Instance:
			// Another container now holds the id: never this row's.
		case info.State == "running":
			if where, ok := p.OnScreen(m.ContainerID); ok {
				return Decision{Row: 9, Outcome: OutcomeClear, Line: name + " is already on screen in " + where}
			}
			return Decision{Row: 9, Outcome: OutcomeAttach, Attach: m.ContainerID,
				Line: name + " still runs in " + info.Name + "; attach to it"}
		case info.State == "paused":
			return pending(10, name+" is paused: docker unpause "+info.Name+", then claude-sandbox tmux restore")
		case info.State == "restarting":
			return pending(11, name+" is restarting; retry: claude-sandbox tmux restore")
		default:
			// created, exited, dead, removing: gone.
		}
	}
	if m.Conversation == "" {
		w := "--no-worktree"
		if m.Worktree != "" {
			w = shq("--worktree=" + m.Worktree)
		}
		return Decision{Row: 13, Outcome: OutcomeClear,
			Line: "no conversation id was recorded for " + RowName(m) + "; pick it by hand: cd " + shq(m.Project) +
				" && claude-sandbox --new " + w + " -- --resume"}
	}
	if m.WorktreeGenerated && m.Worktree == "" {
		return Decision{Row: 14, Outcome: OutcomeClear,
			Line: name + " ran in a worktree whose name was never recorded; resume it by hand from that worktree"}
	}
	g := p.Guard(m)
	switch {
	case g.Open && g.Holder != "" && g.HolderReserved:
		// The guard holds a created, resume-labelled container with no
		// record yet; clearing the row would lose it if that reservation is
		// an orphan (the next launch removes one older than 60 s).
		return pending(15, name+" is being started in "+g.Holder+" (created, not started yet); retry in a minute: claude-sandbox tmux restore "+
			"(a reservation that never starts is removed after 60 s, and the retry then resumes)")
	case g.Open && g.Holder != "":
		line := name + " is already running in " + g.Holder
		if g.AttachCommand != "" {
			line += " — attach: " + g.AttachCommand
		}
		return Decision{Row: 15, Outcome: OutcomeClear, Line: line}
	case g.Open && g.HostPID != 0:
		return Decision{Row: 16, Outcome: OutcomeClear,
			Line: fmt.Sprintf("%s is open in a claude process on this host (pid %d)", name, g.HostPID)}
	case g.Open:
		return pending(17, "cannot tell whether "+m.Conversation+" is open elsewhere ("+g.Reason+")")
	}
	d := Decision{Row: 18, Outcome: OutcomeResume, Manual: manual, Gap: p.Gap(m),
		Line: "resume " + name + " (" + m.Conversation + ") in a new container"}
	d.Notes = ResumeNotes(m)
	for _, o := range g.Orphans {
		d.Notes = append(d.Notes, o+" was created for this conversation but never started (an interrupted launch); the resume's launch removes it")
	}
	return d
}

// ResumeNotes are the lines a resume prints about what it does not replay
// (answer 49, A7): flag names only, never values.
func ResumeNotes(m Mark) []string {
	var n []string
	if len(m.Unreplayed) > 0 {
		n = append(n, "restored without flags given at launch: "+strings.Join(m.Unreplayed, ", ")+" — relaunch by hand to use them")
	}
	if m.FlagsUnknown {
		n = append(n, "the flags this session was launched with are unknown (it predates the launchflags label)")
	}
	if m.ConfigDirEnv == nil {
		n = append(n, "its CLAUDE_CONFIG_DIR was not recorded (an older container): the restore uses this shell's")
	}
	return n
}

// GapFor is the pause after a resume of m is up (answer 50 d): none when the
// global config is linked or relocated, LegacyGap otherwise. configDirEnv is
// the shell's CLAUDE_CONFIG_DIR, used when the row does not record one.
func GapFor(home string, m Mark, configDirEnv string) time.Duration {
	cde := configDirEnv
	if m.ConfigDirEnv != nil {
		cde = *m.ConfigDirEnv
	}
	switch globalcfg.Classify(home, cde).Mode {
	case globalcfg.ModeLinked:
		return LinkedGap
	case globalcfg.ModeRelocated:
		return RelocatedGap
	case globalcfg.ModeConfigJSON:
		return ConfigJSONGap
	}
	return LegacyGap
}

// DockerProbeTimeout bounds the dry-run's one "docker version".
var DockerProbeTimeout = 3 * time.Second

// ProbeTimeout bounds each of the dry-run's other docker calls — the
// container inspect and the resume guard's discovery — so a daemon that
// answers "docker version" but hangs on ps or inspect cannot hang it.
var ProbeTimeout = 5 * time.Second

// BoundedRunner runs Run and Output through Start in their own process
// group, killed with the caller (DieWithParent), and kills the whole group
// after Timeout, returning an error: the bounded helper's shape, for code
// that takes an execx.Runner (sessions discovery and inspect). Start and
// RunSession pass through.
type BoundedRunner struct {
	R       execx.Runner
	Timeout time.Duration
}

func (b BoundedRunner) Output(c execx.Cmd) (string, error) {
	var out syncBuffer
	err := b.run(c, &out)
	return out.String(), err
}

func (b BoundedRunner) Run(c execx.Cmd) error {
	var out syncBuffer
	err := b.run(c, &out)
	if c.Stdout != nil {
		io.WriteString(c.Stdout, out.String())
	}
	return err
}

func (b BoundedRunner) Start(c execx.Cmd) (execx.Process, error) { return b.R.Start(c) }

func (b BoundedRunner) RunSession(c execx.Cmd) (execx.SessionResult, error) {
	return b.R.RunSession(c)
}

// run starts c with stdout into out; its stderr goes through a private
// buffer, copied to c.Stderr only once the process is done, so a killed
// process can never write into the caller's writer afterwards.
func (b BoundedRunner) run(c execx.Cmd, out *syncBuffer) error {
	var errBuf syncBuffer
	stderr := c.Stderr
	c.Stdout, c.Stderr, c.DieWithParent, c.Detach = out, &errBuf, true, false
	proc, err := b.R.Start(c)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- proc.Wait() }()
	select {
	case werr := <-done:
		if stderr != nil {
			io.WriteString(stderr, errBuf.String())
		}
		return werr
	case <-time.After(b.Timeout):
		if g, ok := proc.(execx.GroupKiller); ok {
			g.KillGroup()
		} else {
			proc.Signal(os.Kill)
		}
		select {
		case <-done:
		case <-time.After(b.Timeout):
		}
		return fmt.Errorf("%s %s did not finish within %s", c.Name, firstArg(c.Args), b.Timeout)
	}
}

func firstArg(a []string) string {
	if len(a) == 0 {
		return ""
	}
	return a[0]
}

// PaneInfo is one pane of the running server, from one list-panes.
type PaneInfo struct {
	ID, Session    string
	Window, Pane   int
	CurrentCommand string
	Mark           Mark
	Marked         bool
}

// Coords is "s:w.p".
func (p PaneInfo) Coords() string { return fmt.Sprintf("%s:%d.%d", p.Session, p.Window, p.Pane) }

// panesFormat is the restore's one list-panes (CS-TMUX-051); the mark stays
// last, as in the save hook's.
const panesFormat = "#{pane_id}\t#{session_name}\t#{window_index}\t#{pane_index}\t#{pane_current_command}\t#{" + Option + "}"

// ListPanes reads every pane of the running server; ok is false when tmux
// does not answer.
func ListPanes(r execx.Runner) ([]PaneInfo, bool) {
	out, ok := bounded(r, CallTimeout, "tmux", "list-panes", "-a", "-F", panesFormat)
	if !ok {
		return nil, false
	}
	var ps []PaneInfo
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(strings.TrimRight(line, "\r"), "\t", 6)
		if len(f) != 6 {
			continue
		}
		var w, pn int
		if _, err := fmt.Sscan(f[2], &w); err != nil {
			continue
		}
		if _, err := fmt.Sscan(f[3], &pn); err != nil {
			continue
		}
		pi := PaneInfo{ID: f[0], Session: f[1], Window: w, Pane: pn, CurrentCommand: f[4]}
		pi.Mark, pi.Marked = ParseMark(f[5])
		ps = append(ps, pi)
	}
	return ps, true
}

// ThisPane is the pane a per-pane restore runs in (CS-TMUX-051): its
// coordinates, its server and its mark, from one bounded display-message.
type ThisPane struct {
	Session      string
	Window, Pane int
	Server       *Server
	Mark         Mark
	Marked       bool
	// RawMark is the mark as tmux holds it ("" for none).
	RawMark string
}

// Coords is "s:w.p".
func (t ThisPane) Coords() string { return fmt.Sprintf("%s:%d.%d", t.Session, t.Window, t.Pane) }

const thisPaneFormat = "#{session_name}\t#{window_index}\t#{pane_index}\t#{pid}\t#{start_time}\t#{" + Option + "}"

// ReadThisPane reads paneID's coordinates, server and mark; ok is false when
// tmux does not answer.
func ReadThisPane(r execx.Runner, paneID string) (ThisPane, bool) {
	out, ok := bounded(r, CallTimeout, "tmux", "display-message", "-p", "-t", paneID, thisPaneFormat)
	if !ok {
		return ThisPane{}, false
	}
	f := strings.SplitN(strings.TrimRight(out, "\r\n"), "\t", 6)
	if len(f) != 6 {
		return ThisPane{}, false
	}
	var t ThisPane
	if _, err := fmt.Sscan(f[1], &t.Window); err != nil {
		return ThisPane{}, false
	}
	if _, err := fmt.Sscan(f[2], &t.Pane); err != nil {
		return ThisPane{}, false
	}
	t.Session, t.Server, t.RawMark = f[0], parseServer(f[3], f[4]), f[5]
	t.Mark, t.Marked = ParseMark(f[5])
	return t, true
}

// RunningServer is the tmux server the caller runs in, from one bounded
// display-message; nil when it does not answer.
func RunningServer(r execx.Runner) *Server {
	out, ok := bounded(r, CallTimeout, "tmux", "display-message", "-p", "#{pid}\t#{start_time}")
	if !ok {
		return nil
	}
	f := strings.SplitN(strings.TrimSpace(out), "\t", 2)
	if len(f) != 2 {
		return nil
	}
	return parseServer(f[0], f[1])
}

// SaveInterval is continuum's autosave interval as set, for Procedure A
// (12 § 7): "" when unset, not a number, or tmux does not answer.
func SaveInterval(r execx.Runner) string {
	out, ok := bounded(r, CallTimeout, "tmux", "show", "-gqv", "@continuum-save-interval")
	v := strings.TrimSpace(out)
	if !ok || v == "" || strings.Trim(v, "0123456789") != "" {
		return ""
	}
	return v
}

// ReadProbes are the dry-run's probes (CS-TMUX-051): each docker or tmux
// look runs at most once per process. GuardFunc runs the resume guard (the
// caller owns discovery and the attach command); Self is the pane the
// restore runs in, never "another pane" for row 9.
type ReadProbes struct {
	Runner       execx.Runner
	Home         string
	ConfigDirEnv string
	Self         string
	GuardFunc    func(m Mark) GuardResult
	// Panes, when set, is the listing already read (--all); else it is read
	// on first use.
	Panes []PaneInfo

	once      sync.Once
	dockerErr error
	panesRead bool
}

func (p *ReadProbes) IsDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func (p *ReadProbes) Docker() error {
	p.once.Do(func() { p.dockerErr = DockerAnswers(p.Runner) })
	return p.dockerErr
}

// DockerAnswers is one "docker version", bounded by DockerProbeTimeout: nil
// when the daemon answered (row 8).
func DockerAnswers(r execx.Runner) error {
	out, ran, werr := boundedRun(r, DockerProbeTimeout, "docker", "version", "--format", "{{.Server.Version}}")
	switch {
	case !ran:
		return errors.New("docker version did not finish")
	case werr != nil || strings.TrimSpace(out) == "":
		return errors.New("docker version failed")
	}
	return nil
}

func (p *ReadProbes) Inspect(id string) (ContainerInfo, error) {
	c, err := sessions.InspectContainer(BoundedRunner{R: p.Runner, Timeout: ProbeTimeout}, id)
	if errors.Is(err, sessions.ErrNoSuchContainer) {
		return ContainerInfo{}, ErrNoContainer
	}
	if err != nil {
		return ContainerInfo{}, err
	}
	return ContainerInfo{State: c.State, Name: c.Name, Project: c.Project, Instance: c.Instance}, nil
}

func (p *ReadProbes) OnScreen(id string) (string, bool) {
	if p.Panes == nil && !p.panesRead {
		p.panesRead = true
		p.Panes, _ = ListPanes(p.Runner)
	}
	for _, pi := range p.Panes {
		if pi.ID != p.Self && pi.Marked && pi.Mark.State == StateActive && pi.Mark.ContainerID == id &&
			pi.CurrentCommand == LauncherCommand {
			return pi.Coords(), true
		}
	}
	return "", false
}

func (p *ReadProbes) Guard(m Mark) GuardResult {
	if p.GuardFunc == nil {
		return GuardResult{Open: true, Reason: "no resume guard"}
	}
	return p.GuardFunc(m)
}

func (p *ReadProbes) Gap(m Mark) time.Duration { return GapFor(p.Home, m, p.ConfigDirEnv) }
