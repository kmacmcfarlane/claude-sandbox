package main

// Multi-session support: the `sessions` subcommand plus the launch-time
// decision of whether to start a new container, join an existing one, reattach
// to one, or stop. Spec: spec/sessions.feature (CS-SESS).

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/clients"
	"github.com/kmacmcfarlane/claude-sandbox/internal/clipboard"
	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/imagebuild"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
	"github.com/kmacmcfarlane/claude-sandbox/internal/oomreport"
	"github.com/kmacmcfarlane/claude-sandbox/internal/resumeguard"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

// exitDecisionRequired is returned when a choice has to be made and no terminal
// is attached. It is deliberately distinct from the generic failure code (2) so
// a script can tell "you must decide something" from "something broke", and it
// preserves the pre-existing behavior of failing loudly rather than guessing
// (CS-SESS-019).
const exitDecisionRequired = 3

// promptTimeout bounds every session prompt so a forgotten terminal does not
// wedge a launch indefinitely.
const promptTimeout = 2 * time.Minute

func newSessionsCmd(env *Env) *cobra.Command {
	var all, asJSON bool
	cmd := &cobra.Command{
		Use:           "sessions",
		Short:         "List sandbox sessions",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir, err := resolveProjectDir(env.Getenv)
			if err != nil {
				return err
			}
			var found []sessions.Session
			if all {
				found, err = sessions.DiscoverAll(env.Runner)
			} else {
				found, err = sessions.Discover(env.Runner, projectDir)
			}
			if err != nil {
				return err
			}
			// Reservations are launches in flight, not sessions (CS-SESS-051).
			found = sessions.Live(found)
			// One batched inspect for State.OOMKilled, which docker ps cannot
			// read; a failure just leaves the rows unmarked (CS-SESS-061/062).
			sessions.MarkOOM(env.Runner, found)
			// The friendly names (CS-SESS-114/116): one read per registry dir.
			names := sessionNames(env, found)
			if asJSON {
				b, merr := marshalSessionsJSON(found, names)
				if merr != nil {
					return merr
				}
				fmt.Fprintln(env.Out, string(b))
				return nil
			}
			// "sandbox sessions", not "running": a paused one is listed too
			// (CS-SESS-013).
			if len(found) == 0 {
				if all {
					fmt.Fprintln(env.Out, "No sandbox sessions.")
				} else {
					fmt.Fprintln(env.Out, "No sandbox sessions for this project.")
				}
				return nil
			}
			printSessionTable(env, found, all, projectDir, names)
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "List sessions for every project, not just this one")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit JSON")
	return cmd
}

// printSessionTable renders the human-readable listing (CS-SESS-010/011/074).
// NAME is the session's friendly name, CONTAINER the docker container name
// (CS-SESS-116).
func printSessionTable(env *Env, found []sessions.Session, all bool, projectDir string, names map[string]string) {
	header := []string{"INSTANCE", "NAME", "WORKTREE", "CONTAINER", "MODE", "STATE", "UP", "SESSIONS"}
	if all {
		header = append(header, "PROJECT")
	}
	rows := [][]string{header}
	anyOOM := false
	for _, s := range found {
		instance := s.Instance
		if instance == "" {
			instance = "-"
		}
		mark := ""
		if all && s.Project == projectDir {
			mark = "* "
		}
		worktree := s.Worktree
		if worktree == "" {
			worktree = "-"
		}
		count := fmt.Sprint(s.Count)
		if s.OOMKilled {
			// A suffix rather than a column: the condition is rare, and a
			// column would print "-" on nearly every row (CS-SESS-061).
			count += oomSuffix
			anyOOM = true
		}
		state := s.State
		if state == "" {
			state = "-" // an older docker ps row (CS-SESS-074)
		}
		name := sessions.DisplayName(names[s.Name])
		if name == "" {
			name = "-"
		}
		row := []string{mark + instance, name, worktree, s.Name, s.Mode, state, uptime(s.Status), count}
		if all {
			row = append(row, s.Project)
		}
		rows = append(rows, row)
	}
	// Widths in runes: a session name may hold non-ASCII text.
	widths := make([]int, len(header))
	for _, r := range rows {
		for i, c := range r {
			if n := utf8.RuneCountInString(c); n > widths[i] {
				widths[i] = n
			}
		}
	}
	for _, r := range rows {
		var b strings.Builder
		for i, c := range r {
			b.WriteString(c)
			if i < len(r)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-utf8.RuneCountInString(c)+2))
			}
		}
		fmt.Fprintln(env.Out, strings.TrimRight(b.String(), " "))
	}
	if all || anyOOM {
		fmt.Fprintln(env.Out)
	}
	if all {
		fmt.Fprintln(env.Out, "* = this project")
	}
	if anyOOM {
		fmt.Fprintln(env.Out, strings.TrimSpace(oomSuffix)+" = a process in this container was killed by the OOM killer: its memoryLimit or the host running out of memory")
	}
}

// sessionJSON is one --json row: the discovered session plus its friendly
// name (CS-SESS-116). "name" is the container name, as it always was.
type sessionJSON struct {
	sessions.Session
	SessionName string `json:"sessionName,omitempty"`
}

// marshalSessionsJSON is sessions.MarshalJSON with each row's sessionName.
func marshalSessionsJSON(found []sessions.Session, names map[string]string) ([]byte, error) {
	rows := make([]sessionJSON, 0, len(found))
	for _, s := range found {
		rows = append(rows, sessionJSON{Session: s, SessionName: sessions.DisplayName(names[s.Name])})
	}
	return json.MarshalIndent(rows, "", "  ")
}

// sessionNames finds the friendly name of each session's primary claude
// (CS-SESS-114): from the container's registry label, else the launcher's
// <config dir>/sessions and the legacy peers root. Display only: every
// failure is no name.
func sessionNames(env *Env, found []sessions.Session) map[string]string {
	_, _, _, home := hostIdentity(env.Getenv)
	configDir := env.Getenv("CLAUDE_CONFIG_DIR")
	if configDir == "" && home != "" {
		configDir = filepath.Join(home, ".claude")
	}
	return sessions.SessionNames(found, sessions.NameLookup{
		// One builder of the dirs (resumeguard.RegistryDirs); dropping a
		// relative one is this display's own policy.
		Dirs: func(label string) []string {
			return keepAbs(resumeguard.RegistryDirs(label, configDir, home))
		},
		OwnDomain: func() string {
			d, err := resumeguard.Check{ProcRoot: env.ProcRoot, MachineIDPath: env.MachineIDPath}.OwnDomain()
			if err != nil {
				return ""
			}
			return d
		},
	})
}

// keepAbs keeps the absolute paths of dirs.
func keepAbs(dirs []string) []string {
	var out []string
	for _, d := range dirs {
		if filepath.IsAbs(d) {
			out = append(out, d)
		}
	}
	return out
}

// namedLabel is sessionLabel with the friendly name beside it, as the
// tier-1 list and the pick lines show it (CS-SESS-115): "otter (big
// refactor)", or the label alone without a name.
func namedLabel(s sessions.Session, names map[string]string) string {
	if n := sessions.DisplayName(names[s.Name]); n != "" {
		return sessionLabel(s) + " (" + n + ")"
	}
	return sessionLabel(s)
}

// oomSuffix marks a container whose State.OOMKilled is true in the SESSIONS
// column (CS-SESS-061).
const oomSuffix = " (OOM)"

// noteEarlierOOM prints one note when the container about to be attached to
// or joined has had a process OOM-killed (CS-SESS-063). docker's
// State.OOMKilled is sticky, so this surfaces a kill nobody was attached to
// see. It never prompts; an inspect that fails prints nothing.
func noteEarlierOOM(env *Env, s sessions.Session) {
	t := []sessions.Session{s}
	sessions.MarkOOM(env.Runner, t)
	if !t[0].OOMKilled {
		return
	}
	label := sessionLabel(s)
	lim := oomreport.Limit{Value: s.MemoryLimit, Source: s.MemoryLimitSource}
	fmt.Fprintf(env.Err, "Note: an earlier process in this container (session '%s') was killed by the OOM killer (%s); memoryLimit: %s.\n",
		label, oomreport.EitherCause, oomreport.DescribeLimit(lim))
}

// uptime trims docker's "Up 2 hours (healthy)" to "2 hours".
func uptime(status string) string {
	s := strings.TrimPrefix(status, "Up ")
	if i := strings.Index(s, " ("); i > 0 {
		s = s[:i]
	}
	if s == "" {
		return "?"
	}
	return s
}

// sessionAction is what the launcher should do about the sessions it found.
type sessionAction int

const (
	actionNew sessionAction = iota // launch a new container (the normal pipeline)
	actionAttach
	actionJoin
	// actionRestart replaces the target's container with a new one that
	// resumes its conversation (CS-SESS-105).
	actionRestart
	actionQuit
)

type sessionDecision struct {
	Action sessionAction
	Target sessions.Session
	// BranchArgs are claude flags the launcher prepends to the passthrough so
	// the new container forks an existing conversation (CS-SESS-039/040).
	BranchArgs []string
}

// Branching composes upstream claude flags — the launcher never reads the
// session transcripts, whose format is internal and version-unstable
// (CS-LNCH-002). All sessions of a project share the host-mounted transcript
// store, so the fork works from any container.
var (
	// branchNewestArgs forks the newest conversation for this directory —
	// which is the running session's, since it is actively appending to its
	// transcript (CS-SESS-039).
	branchNewestArgs = []string{"--continue", "--fork-session"}
	// branchPickerArgs shows claude's own resume picker inside the new
	// container, so choosing which conversation to fork gets id search, titles
	// and relative times for free (CS-SESS-040).
	branchPickerArgs = []string{"--resume", "--fork-session"}
)

// decideSessions discovers running sessions for the project and resolves what
// to do about them. It runs before any image build, since building an image the
// user is about to bypass by attaching is wasted work (CS-SESS-015).
func decideSessions(env *Env, projectDir string, f *launchFlags) (sessionDecision, error) {
	if f.NoSessionCheck {
		d := sessionDecision{Action: actionNew}
		if f.Branch {
			d.BranchArgs = branchPickerArgs
		}
		return d, nil
	}
	found, err := sessions.Discover(env.Runner, projectDir)
	if err != nil {
		return sessionDecision{}, err
	}
	candidates := sessions.Interactive(found)

	// Ralph never prompts: concurrency there belongs to the ralph PID lock. It
	// still reports what is running, which is the discoverability half of the
	// problem (CS-SESS-034).
	if f.Ralph {
		if live := sessions.Live(found); len(live) > 0 {
			reportRunning(env, live, sessionNames(env, live))
		}
		return sessionDecision{Action: actionNew}, nil
	}

	// CS-SESS-105: --restart[=NOUN] picks its target as --attach does.
	if f.Restart {
		s, serr := resolveRestartTarget(env, found, f.RestartTarget)
		if serr != nil {
			return sessionDecision{}, serr
		}
		return sessionDecision{Action: actionRestart, Target: s}, nil
	}
	// An explicit target bypasses both prompts.
	if f.Attach || f.Join {
		target := f.AttachTarget
		if f.Join {
			target = f.JoinTarget
		}
		act, verb := actionAttach, "attach"
		if f.Join {
			act, verb = actionJoin, "join"
		}
		s, serr := resolveTarget(env, candidates, target, verb)
		if serr != nil {
			return sessionDecision{}, serr
		}
		return sessionDecision{Action: act, Target: s}, nil
	}
	// --branch removes the decision like --new does — the conversation to fork
	// is chosen by claude's own picker inside the new container (CS-SESS-040/041).
	if f.Branch {
		return sessionDecision{Action: actionNew, BranchArgs: branchPickerArgs}, nil
	}
	if f.NewSession || len(candidates) == 0 {
		return sessionDecision{Action: actionNew}, nil
	}

	// A decision is needed. Report first, so the information is available even
	// when the command is about to fail for want of a terminal.
	names := sessionNames(env, candidates)
	reportRunning(env, candidates, names)
	if !env.Prompter.Interactive() {
		return sessionDecision{}, exitErr(exitDecisionRequired,
			"Error: %d session(s) already running for this project and no terminal is attached.\n"+
				"Choose explicitly: --new, --branch, --attach[=INSTANCE], --join[=INSTANCE], or --no-session-check.",
			len(candidates))
	}

	if tmuxpane.GuardedContinue(f.Passthrough) {
		// CS-SESS-096: the authoritative checks run later; no registry read.
		fmt.Fprintln(env.Err, continueNote)
	}
	fmt.Fprintln(env.Err, "  [n] new session in a new container   (isolated; attachable if your terminal drops)")
	fmt.Fprintln(env.Err, "  [b] branch the newest conversation into a new container   (fork it; both continue independently)")
	fmt.Fprintln(env.Err, "  [j] new session in an existing container   (dies with that container's primary; not attachable later)")
	fmt.Fprintln(env.Err, "  [a] attach to an existing session   (shares the terminal if someone is already using it)")
	fmt.Fprintln(env.Err, "  [r] restart a session in a new container   (resumes its conversation here with a fresh screen; stops the old container)")
	fmt.Fprintln(env.Err, "  [q] quit")
	switch strings.ToLower(strings.TrimSpace(env.Prompter.Ask("", "Choice [n/b/j/a/r/q]:", promptTimeout))) {
	case "n", "new":
		return sessionDecision{Action: actionNew}, nil
	case "b", "branch":
		// The newest conversation for this directory is the running session's;
		// to branch a different one, use --branch for claude's picker instead.
		return sessionDecision{Action: actionNew, BranchArgs: branchNewestArgs}, nil
	case "j", "join":
		s, serr := selectInstance(env, candidates, "join", names)
		return sessionDecision{Action: actionJoin, Target: s}, serr
	case "a", "attach":
		s, serr := selectInstance(env, candidates, "attach", names)
		return sessionDecision{Action: actionAttach, Target: s}, serr
	case "r", "restart":
		// CS-SESS-105: a paused container cannot be stopped gracefully.
		var running []sessions.Session
		for _, c := range candidates {
			if c.State != "paused" {
				running = append(running, c)
			}
		}
		if len(running) == 0 {
			return sessionDecision{}, exitErr(2, "Error: no running session for this project to restart (paused ones must be unpaused first).")
		}
		s, serr := selectInstance(env, running, "restart", names)
		return sessionDecision{Action: actionRestart, Target: s}, serr
	default:
		// Includes "q" and the empty answer: Ask returns "" on Enter, on EOF and
		// on timeout, none of which is consent to start or join anything.
		return sessionDecision{Action: actionQuit}, nil
	}
}

// sessionLabel names one container in a message: its instance noun, else its
// mode for a container without an instance label, else its container name,
// so a message never names an empty session (CS-SESS-063).
func sessionLabel(s sessions.Session) string {
	switch {
	case s.Instance != "":
		return s.Instance
	case s.Mode != "":
		return s.Mode
	}
	return s.Name
}

// reportRunning prints the tier-1 list: each session's label with its
// friendly name beside it (CS-SESS-115), padded in runes to the widest.
func reportRunning(env *Env, found []sessions.Session, names map[string]string) {
	fmt.Fprintf(env.Err, "\nFound %d running session(s) for this project:\n", len(found))
	width := 10
	for _, s := range found {
		if n := utf8.RuneCountInString(namedLabel(s, names)); n > width {
			width = n
		}
	}
	for _, s := range found {
		fmt.Fprintf(env.Err, "  %-*s up %-12s %d session(s)\n", width, namedLabel(s, names), uptime(s.Status), s.Count)
	}
	fmt.Fprintln(env.Err)
}

// resolveTarget picks the session named by an explicit --attach/--join value,
// or the only candidate when the value was omitted (CS-SESS-029, CS-SESS-030).
func resolveTarget(env *Env, candidates []sessions.Session, target, verb string) (sessions.Session, error) {
	if len(candidates) == 0 {
		return sessions.Session{}, exitErr(2, "Error: no running sessions for this project to attach to or join.")
	}
	if target != "" {
		s, ok := sessions.ByInstance(candidates, target)
		if !ok {
			return sessions.Session{}, exitErr(2, "Error: no running session named '%s'. Available: %s",
				target, strings.Join(sessions.Instances(candidates), ", "))
		}
		return s, nil
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	names := sessionNames(env, candidates)
	reportRunning(env, candidates, names)
	return selectInstance(env, candidates, verb, names)
}

// selectInstance is the second tier: it is skipped entirely when there is only
// one candidate, so the common case stays a single prompt (CS-SESS-017).
func selectInstance(env *Env, candidates []sessions.Session, verb string, friendly map[string]string) (sessions.Session, error) {
	if len(candidates) == 1 {
		fmt.Fprintf(env.Err, "Using the only running session: %s\n", namedLabel(candidates[0], friendly))
		return candidates[0], nil
	}
	names := sessions.Instances(candidates)
	if !env.Prompter.Interactive() {
		return sessions.Session{}, exitErr(exitDecisionRequired,
			"Error: several sessions are running and no terminal is attached.\n"+
				"Name one explicitly, e.g. --%s=%s. Available: %s", verb, names[0], strings.Join(names, ", "))
	}
	answer := strings.TrimSpace(env.Prompter.Ask("",
		fmt.Sprintf("Which session to %s? [%s]:", verb, strings.Join(names, "/")), promptTimeout))
	if answer == "" {
		return sessions.Session{}, exitErr(exitDecisionRequired, "Error: no session selected.")
	}
	s, ok := sessions.ByInstance(candidates, answer)
	if !ok {
		return sessions.Session{}, exitErr(2, "Error: no running session named '%s'. Available: %s",
			answer, strings.Join(names, ", "))
	}
	return s, nil
}

// joinExistingSession attaches to or joins the chosen container. It reports
// whether the launch is finished: false means the drift prompt asked for a new
// container instead, and the caller should continue with a normal launch.
//
// Neither path runs the image staleness check, the image build, mount assembly,
// or shadow-file injection — the container is already configured. That is
// exactly why config drift is reported instead (CS-SESS-033).
func joinExistingSession(env *Env, projectDir string, f *launchFlags, cfg *cascade.Config, envFiles []string, d sessionDecision, wt worktreeChoice, linked *launch.LinkedWorktree) (bool, error) {
	// CS-LNCH-204: the client lease, as soon as the container is chosen —
	// before the drift check, its prompt, the clipboard re-resolve and the
	// pane mark — so nothing reads this container as unattended while a
	// person sits at a prompt about to attach. Released on every return,
	// including a quit, a refused drift and a drift that chose a new
	// container.
	kind := clients.KindAttach
	if d.Action == actionJoin {
		kind = clients.KindJoin
	}
	lease := takeLease(env, d.Target.ID, kind)
	defer lease.Release()
	// CS-LNCH-212: a container ended (on hang-up) while the menu was read.
	if err := endedBeforeAttach(env, projectDir, d.Target, lease); err != nil {
		return true, err
	}

	model := f.Model
	if model == "" {
		model = cfg.Model
	}

	noteEarlierOOM(env, d.Target)

	// CS-SESS-092: the env files are read ONCE, for the drift check and for
	// the terminal identity of the join or the attach note. Still after the
	// session decision and without the refusal check (CS-LNCH-129).
	snap, snapErr := cascade.ReadEnvFiles(envFiles)

	img := &driftImage{}
	wantHash, wantInputs := wouldBeFingerprintNet(env, projectDir, f, cfg, envFiles, snap, snapErr, linked, &d.Target, launch.NetworkWanted(env.Getenv, cfg), img)
	if !f.AllowConfigDrift {
		// CS-LNCH-189: never blame a network a launch now could not apply.
		wantHash, wantInputs = networkDriftRecheck(env.Runner, d.Target, wantHash, wantInputs, func() (string, []launch.InputDigest) {
			return wouldBeFingerprintNet(env, projectDir, f, cfg, envFiles, snap, snapErr, linked, &d.Target, false, img)
		})
	}
	proceed, newContainer, err := confirmDrift(env, d.Target, wantHash, wantInputs, f)
	if err != nil {
		return false, err
	}
	if newContainer {
		return false, nil
	}
	if !proceed {
		return true, nil // quit
	}

	if d.Action == actionAttach {
		warnModelMismatch(env, d.Target, model)
		noteWorktree(env, d.Target, wt)
		terminalNote(env, d.Target, snap, snapErr)
		// CS-LNCH-197: started without the bridge this launcher wants.
		clipboardAttachNote(env, d.Target, cfg)
		_, _, _, home := hostIdentity(env.Getenv)
		if snapErr == nil {
			// CS-LNCH-202: the JetBrains note, after the target is written.
			return true, attachToWith(env, d.Target, cfg.DetachKeys, attachMark(d.Target, home, wt.Root), nil, lease, endOnHangupFor(env, d.Target, cfg), clipboard.ByAttach, func(eff *clipboard.Target, bridged bool) {
				if bridged && launch.ClipboardWanted(env.Getenv, cfg, false, false) {
					jetbrainsNote(env, projectDir, snap, eff)
				}
			})
		}
		return true, attachToWith(env, d.Target, cfg.DetachKeys, attachMark(d.Target, home, wt.Root), nil, lease, endOnHangupFor(env, d.Target, cfg), clipboard.ByAttach, nil)
	}
	_, _, hostUser, _ := hostIdentity(env.Getenv)
	// CS-SESS-092: an unreadable snapshot leaves the container's identity
	// alone (nil), the safe direction toward an explicit env file; the drift
	// check's warning above already named the file.
	var term *launch.TerminalResult
	if snapErr == nil {
		t := launch.ResolveTerminal(env.lookupEnv, snap, env.lookupEnv)
		term = &t
	}
	return true, joinInto(env, d.Target, projectDir, hostUser, model, cfg.DetachKeys, resolveDangerous(env, f, cfg), f, wt, term, lease)
}

// terminalNote prints one stderr Note before an attach when the container's
// claude was started with a terminal identity other than the one a launch
// from here would give it (CS-SESS-091): Claude Code reads it once at start,
// and docker attach is a raw TTY splice that cannot change it. The
// comparison value is computed as if creating now (launch.ResolveTerminal
// over this launcher's environment and snap). Silent for a container that
// predates the label, for an equal value, and when snapErr is set (it
// cannot tell). Shared by the hand attach and the tmux restore attach
// (CS-TMUX-056); never a prompt, never an exit-status change.
func terminalNote(env *Env, s sessions.Session, snap []cascade.EnvFile, snapErr error) {
	if s.Terminal == "" || snapErr != nil {
		return
	}
	term := launch.ResolveTerminal(env.lookupEnv, snap, env.lookupEnv)
	want := term.Label(false)
	if want == s.Terminal {
		return
	}
	if file := term.PinnedBy(); file != "" {
		// An env file assigns the value, so a join would take the file's value
		// too: name the file, not --join.
		fmt.Fprintf(env.Err, "Note: session '%s' was started with %s; %s now gives %s. Claude Code reads this once at start; relaunch (exit, then claude-sandbox --resume) to apply it.\n",
			sessionLabel(s), launch.DescribeTerminal(s.Terminal), file, launch.DescribeTerminal(want))
		return
	}
	fmt.Fprintf(env.Err, "Note: session '%s' was started in a terminal with %s; this terminal has %s. Claude Code reads this once at start, so mouse-wheel and rendering handling may not fit this terminal. For this terminal's handling: --join (a new claude), or exit and relaunch with --resume.\n",
		sessionLabel(s), launch.DescribeTerminal(s.Terminal), launch.DescribeTerminal(want))
}

// wouldBeFingerprint computes the config hash a launch would produce right now,
// without building anything. The image ID is read from the resolved image if it
// already exists; when it does not, the empty ID is itself a difference, which
// is correct — the launch would have built a new image. A linked worktree
// resolves its child Dockerfile and git dir mount as the launch does
// (CS-LNCH-074). target is the container being attached to or joined: its
// own peers root is reused (CS-DIR-017), so a session on the legacy root shows
// no drift once launches have switched; nil uses the new root.
//
// snap is the env files read ONCE by the caller, shared with the terminal
// identity (CS-SESS-092); snapErr is that read's error, reported here as the
// one warning of an unreadable env file, adding that the session keeps the
// container's terminal identity (the join and the attach note stand down).
//
// The launcher network is assumed to apply whenever the key resolves on
// (CS-LNCH-189): no docker call here. networkDriftRecheck corrects that when
// the hashes differ.
func wouldBeFingerprint(env *Env, projectDir string, f *launchFlags, cfg *cascade.Config, envFiles []string, snap []cascade.EnvFile, snapErr error, linked *launch.LinkedWorktree, target *sessions.Session) (string, []launch.InputDigest) {
	return wouldBeFingerprintNet(env, projectDir, f, cfg, envFiles, snap, snapErr, linked, target, launch.NetworkWanted(env.Getenv, cfg), nil)
}

// driftImage carries the cap's image ID between the drift check's builds, so
// the network recheck's rebuild (CS-LNCH-189) asks docker for it only once.
type driftImage struct {
	id       string
	resolved bool
}

// wouldBeFingerprintNet is wouldBeFingerprint with the network decided by the
// caller: network false is the would-be launch on the default bridge, without
// the network's context paragraph (CS-LNCH-189/191). img, when non-nil,
// holds the image ID once resolved and is reused by later calls.
func wouldBeFingerprintNet(env *Env, projectDir string, f *launchFlags, cfg *cascade.Config, envFiles []string, snap []cascade.EnvFile, snapErr error, linked *launch.LinkedWorktree, target *sessions.Session, network bool, img *driftImage) (string, []launch.InputDigest) {
	if snapErr != nil {
		fmt.Fprintf(env.Err, "WARNING: cannot compute the current configuration for the drift check: reading env file: %v; the session keeps the container's terminal identity.\n", snapErr)
		return "", nil
	}
	mainCheckout := ""
	if linked != nil {
		mainCheckout = linked.Main
	}
	baseOnly := cfg.BaseOnly || envTrue(env.Getenv("CLAUDE_SANDBOX_BASE_ONLY"))
	dfDir := env.Getenv("CLAUDE_SANDBOX_DOCKERFILE_DIR")
	if dfDir == "" {
		dfDir = cfg.DockerfileDir
	}
	dfName := env.Getenv("CLAUDE_SANDBOX_DOCKERFILE")
	if dfName == "" {
		dfName = cfg.Dockerfile
	}
	spec, err := imagebuild.ResolveChild(imagebuild.ChildInputs{
		ProjectDir: projectDir, MainCheckout: mainCheckout,
		BaseOnly: baseOnly, DockerfileDir: dfDir, Dockerfile: dfName,
	}, io.Discard)
	if err != nil {
		// An unreadable Dockerfile would fail a launch (CS-IMG-074). Here the
		// hash cannot be computed, so confirmDrift reports drift (a prompt,
		// or exit 3 without a terminal); name the cause, or its listing of
		// every input as "removed" would be the only clue.
		fmt.Fprintf(env.Err, "WARNING: cannot compute the current configuration for the drift check: %v\n", err)
		return "", nil
	}

	parent := spec.ImageName
	if !spec.Use {
		parent = imagebuild.BaseImageName
	}
	// The container runs the cap, not its parent (CS-SESS-038): a CLI update
	// or a rebuilt tools image (CS-IMG-048) changes the cap's ID while the
	// parent's stays put.
	image := imagebuild.CapImageName(parent)
	if img == nil {
		img = &driftImage{}
	}
	if !img.resolved {
		img.id, img.resolved = imagebuild.ImageID(env.Runner, image), true
	}
	id := img.id

	// CS-LNCH-084: Build writes the shadow files to hash their contents; give
	// it a private directory and remove it, or every drift check leaks one.
	shadow, err := launch.NewShadowDir(env.TempRoot)
	if err != nil {
		return "", nil
	}
	defer os.RemoveAll(shadow)

	netName := ""
	if network {
		netName = launch.NetworkName
	}
	uid, gid, uname, home := hostIdentity(env.Getenv)
	var peerRoot *launch.PeerRootChoice
	if target != nil {
		peerRoot = &launch.PeerRootChoice{Root: launch.DriftPeerRoot(home, target.PeerRoot, target.Mounts)}
	}
	plan, err := launch.Build(launch.Inputs{
		ProjectDir: projectDir, Home: home, TempDir: shadow,
		HostUID: uid, HostGID: gid, HostUser: uname,
		Getenv: env.Getenv,
		// The launch passes it too (runLaunch): a bare env-file key resolves
		// against it, so without it the XDG_RUNTIME_DIR stand-down could differ
		// and every attach would report false drift (CS-LNCH-108).
		LookupEnv: env.lookupEnv,
		RalphMode: f.Ralph, Limit: f.Limit, SkipPermissions: resolveDangerous(env, f, cfg),
		CLIModel: f.Model, Passthrough: f.Passthrough,
		CLISSH: f.SSH, CLIGit: f.Git, CLIDockerSocket: f.DockerSocket, CLIAWS: f.AWS,
		CLIPackageCaches: f.PackageCaches,
		Cfg:              cfg, EnvFiles: envFiles, Env: snap,
		ImageName: image, ImageID: id,
		Linked: linked,
		// The launch passes it too: a nested check that decided differently
		// would change the mount set, and with it the hash (CS-LNCH-163..165).
		MountInfo: env.MountInfo,
		// CS-DIR-017/019: the target's own root, and nothing created for it.
		PeerRoot: peerRoot, NoCreatePeerDirs: true,
		// CS-LNCH-226: creates nothing, so no label or mount value check: a
		// value that went bad since the create must not fail it.
		DriftOnly: true,
		Network:   netName,
		// CS-LNCH-201: the wanted bridge, as the launch hashes it.
		ClipboardWanted: launch.ClipboardWanted(env.Getenv, cfg, false, f.Ralph),
		ClipboardQuiet:  true,
		Out:             io.Discard, Err: io.Discard,
	})
	if err != nil {
		// Without a comparable hash, confirmDrift reports drift (a prompt, or
		// exit 3 without a terminal) against a labelled container. Name the
		// cause, as for the Dockerfile above: an env file that is a FIFO or
		// otherwise unreadable (CS-LNCH-172) is skipped by the lint and the
		// override notice, so this line is the only place it is named.
		fmt.Fprintf(env.Err, "WARNING: cannot compute the current configuration for the drift check: %v\n", err)
		return "", nil
	}
	return plan.ConfigHash, plan.ConfigInputs
}

// networkDriftRecheck corrects wouldBeFingerprint's assumption that the
// launcher network would apply (CS-LNCH-189). Only when the hashes differ and
// the drift adds <network> does it run ONE network inspect, through r (the
// restore attach passes a bounded runner): ours or missing leaves the drift
// standing — [n] gives a container on the network; foreign or any error
// returns rebuild(), the would-be launch without the network, since a launch
// now would stand down too and [n] could not fix the drift (CS-SESS-097).
// Equal hashes, no recorded hash or no <network> in the drift make no call.
func networkDriftRecheck(r execx.Runner, s sessions.Session, wantHash string, wantInputs []launch.InputDigest, rebuild func() (string, []launch.InputDigest)) (string, []launch.InputDigest) {
	if wantHash == "" || s.ConfigHash == "" || s.ConfigHash == wantHash {
		return wantHash, wantInputs
	}
	added := false
	for _, c := range launch.Drift(s.Inputs, wantInputs) {
		if c.Path == launch.NetworkInput && c.How == "added" {
			added = true
			break
		}
	}
	if !added {
		return wantHash, wantInputs
	}
	switch launch.InspectNetwork(r).State {
	case launch.NetworkOurs, launch.NetworkMissing:
		return wantHash, wantInputs
	}
	return rebuild()
}

// newInstance picks the instance noun for a container about to be launched.
// Ralph gets none: it is single-instance, so there is nothing to disambiguate.
// This is the EARLY pick, made before the image build because the noun names
// the worktree in the banner; reserveContainer re-validates it under the launch
// lock (CS-SESS-054). Discovery includes reservations (CS-SESS-050).
//
// The noun is chosen from those NOT already in use by this project, which is
// why a short word list suffices — see sessions.PickNoun. Because the noun
// also names the container's worktree, nouns whose worktree directory already
// exists under gitRoot are skipped too (CS-SESS-045): claude reopens an
// existing worktree, and reopening one left behind by an earlier session must
// be a deliberate --worktree=NAME, never a coin toss.
func newInstance(env *Env, projectDir string, f *launchFlags, gitRoot string) string {
	if f.Ralph {
		return ""
	}
	taken := launch.ExistingWorktrees(gitRoot)
	found, err := sessions.Discover(env.Runner, projectDir)
	if err != nil {
		// Discovery failing must not block a launch; an unfiltered pick is still
		// overwhelmingly likely to be unique.
		return sessions.PickNoun(taken, nil)
	}
	return sessions.PickNoun(append(taken, sessions.Instances(found)...), nil)
}

// attachTo runs `docker attach` as the session child (CS-SESS-031) and, like a
// new session, reports an OOM kill that ends it (CS-SESS-059). The limit
// comes from the container's own labels, carried by its events.
// mark is the pane mark for the attach (CS-TMUX-012), nil for none; onChild
// runs once the pane is marked, right before docker attach (a restore
// releases its start lock there, CS-TMUX-060). docker attach gets the full
// 64-hex id when it is known, else the name; the event filter keeps the name,
// which oomreport matches exactly (CS-TMUX-056, CS-LNCH-095).
func attachTo(env *Env, s sessions.Session, configuredKeys string, mark *paneMark, onChild func(), lease *clients.Lease) error {
	return attachToWith(env, s, configuredKeys, mark, onChild, lease, false, clipboard.ByAttach, nil)
}

// attachToWith is attachTo with a hook that sees the clipboard bridge's
// effective target once the attach re-pointed it (CS-LNCH-197/202). lease is
// the attach's client lease (CS-LNCH-204), taken and released by the caller.
// endOnHangup: this attach's hang-up ends the container (CS-LNCH-206).
func attachToWith(env *Env, s sessions.Session, configuredKeys string, mark *paneMark, onChild func(), lease *clients.Lease, endOnHangup bool, by string, clip func(*clipboard.Target, bool)) error {
	// CS-LNCH-197: re-point the bridge to this shell's display, never
	// downgrading, and restart a dead helper.
	eff, bridged := refreshClipboardBridge(env, s, by)
	if clip != nil {
		clip(eff, bridged)
	}
	detachKeys := launch.ResolveDetachKeys(configuredKeys)
	fmt.Fprintf(env.Out, "Attaching to %s. Press %s to detach without stopping it.\n", sessionLabel(s), detachKeys)
	// Docker cannot report whether another client is already attached, so this
	// cannot be prevented — only mentioned.
	fmt.Fprintln(env.Out, "If someone else is already attached, you will share the terminal.")
	// CS-GCFG-001: the health check before the session, and again after it.
	pre := checkGlobalConfig(env, nil)
	target := s.Name
	if fullID.MatchString(s.ID) {
		target = s.ID
	}
	end, err := runSession(env, execx.Cmd{
		Name: "docker",
		Args: []string{"attach", "--detach-keys=" + detachKeys, target},
	}, s.Name, sessionOpts{kind: primarySession, after: func() { checkGlobalConfig(env, pre) }, mark: mark, onChild: onChild, lease: lease, endOnHangup: endOnHangup})
	if err != nil {
		return err
	}
	return sessionExit(end.code)
}

// joinInto starts another claude inside a running container (CS-SESS-032), as
// a session child the launcher waits on.
//
// term is the joining terminal's identity (CS-SESS-090/092), nil when the env
// files could not be read: then no identity variable is touched and the
// container's value stands.
func joinInto(env *Env, s sessions.Session, projectDir, hostUser, model, configuredKeys string, dangerous bool, f *launchFlags, wt worktreeChoice, term *launch.TerminalResult, lease *clients.Lease) error {
	if tmuxpane.GuardedContinue(f.Passthrough) {
		// CS-SESS-095: before the docker exec, which nothing undoes.
		if err := guardJoinContinue(env, projectDir, wt, f.Passthrough); err != nil {
			return err
		}
	}
	detachKeys := launch.ResolveDetachKeys(configuredKeys)
	fmt.Fprintf(env.Out, "Starting a new session inside %s.\n", sessionLabel(s))
	fmt.Fprintln(env.Out, "Note: this session ends if that container's primary session exits, and it cannot be reattached.")
	// Detaching from a joined session orphans it beyond recovery, so this is
	// the path where docker's ctrl-p,ctrl-q default does the most damage: the
	// TUI binds ctrl+p, so a stray ctrl+p then ctrl+q would silently lose the
	// session. Never leave these keys to docker's default.
	fmt.Fprintf(env.Out, "Press %s to detach — but note that a detached joined session cannot be recovered.\n", detachKeys)

	// -u is required: exec skips the entrypoint's gosu step, and the image ends
	// USER root. -w is redundant (docker create's -w is inherited via
	// Config.WorkingDir) but passed so the working directory never depends on
	// how the container happened to be started.
	// The helper lands this claude on the container's pid class (CS-SESS-044,
	// CS-PID-005); CLAUDE_SANDBOX_PID_CLASS is inherited from the container.
	// CS-SESS-075: CLAUDE_SANDBOX_JOINED tells the baked Notification hook
	// that this claude is not the container's primary, so its ping does not
	// offer an --attach that would land on a different session.
	args := []string{"exec", "-it", "--detach-keys=" + detachKeys, "-u", hostUser,
		"-e", "CLAUDE_SANDBOX_JOINED=1"}
	// CS-SESS-090: docker exec starts the new claude with the CONTAINER's
	// environment, i.e. the creator's terminal identity. Each name is set to
	// this terminal's value (-e NAME=<value>), or removed by /usr/bin/env -u
	// in front of the command — not "-e NAME=", which is not "unset" for
	// every reader. The prefix runs before pidslot, which still execs tini, so
	// the pid class landing is unchanged (CS-PID-005).
	var unset []string
	if term != nil {
		var set []string
		set, unset = term.JoinEnv()
		for _, e := range set {
			args = append(args, "-e", e)
		}
	}
	args = append(args, "-w", projectDir, s.Name)
	if len(unset) > 0 {
		args = append(args, "/usr/bin/env")
		for _, n := range unset {
			args = append(args, "-u", n)
		}
	}
	args = append(args, "/opt/claude-sandbox/bin/claude-sandbox", "pidslot", "--", "claude")
	// CS-SESS-064: the resolved setting, not just the CLI flag.
	if dangerous {
		args = append(args, "--dangerously-skip-permissions")
	}
	// CS-SESS-046: a joined session gets its OWN worktree — bare --worktree
	// lets claude generate the name — never the primary's, which the harness's
	// lock and reset logic does not expect to share. --worktree=NAME still
	// names it explicitly.
	if wt.Enabled {
		args = append(args, "--worktree")
		if wt.Name != "" {
			args = append(args, wt.Name)
		}
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, f.Passthrough...)
	// CS-LNCH-197: a join re-points the bridge too and restarts a dead
	// helper; its claude gets the bridge's environment from pidslot.
	refreshClipboardBridge(env, s, clipboard.ByJoin)
	// CS-GCFG-001: before the session only — a join is not the primary.
	checkGlobalConfig(env, nil)
	// CS-TMUX-012: a join's pane mark, "since" taken before the exec.
	_, _, _, home := hostIdentity(env.Getenv)
	mark := joinMark(s, home, wt, launchRecord(env, f), env.now(), tmuxpane.ResumeID(f.Passthrough))
	// CS-SESS-060: judged by the exec's own status, since the container
	// normally outlives it.
	end, err := runSession(env, execx.Cmd{Name: "docker", Args: args}, s.Name, sessionOpts{kind: joinedSession, mark: mark, lease: lease})
	if err != nil {
		return err
	}
	return sessionExit(end.code)
}

// noteWorktree reports where the session being attached to works, the way
// warnModelMismatch reports the model (CS-SESS-047): a per-session choice
// attach cannot change, so it is stated rather than treated as drift.
func noteWorktree(env *Env, s sessions.Session, wt worktreeChoice) {
	where := "the shared checkout"
	if s.Worktree != "" {
		where = fmt.Sprintf("worktree '%s' (branch worktree-%s)", s.Worktree, s.Worktree)
	}
	msg := fmt.Sprintf("Note: session '%s' runs in %s", sessionLabel(s), where)
	requestedOn := wt.Enabled || wt.StoodDown
	if requestedOn != (s.Worktree != "") || (wt.Name != "" && wt.Name != s.Worktree) {
		msg += "; --worktree/--no-worktree cannot change a running session"
	}
	fmt.Fprintln(env.Err, msg+".")
}

// confirmDrift compares the configuration a container was started with against
// the configuration on disk now, and requires an explicit choice when they
// differ (CS-SESS-025). Returns false when the caller should launch a new
// container instead.
func confirmDrift(env *Env, s sessions.Session, wantHash string, wantInputs []launch.InputDigest, f *launchFlags) (proceed, newContainer bool, err error) {
	if f.AllowConfigDrift || s.ConfigHash == "" || s.ConfigHash == wantHash {
		// An empty label means a container from an older version: nothing to
		// compare against, so do not invent drift.
		return true, false, nil
	}
	fmt.Fprintf(env.Err, "\nSession '%s' was started with different configuration:\n", sessionLabel(s))
	changes := launch.Drift(s.Inputs, wantInputs)
	if len(changes) == 0 {
		fmt.Fprintln(env.Err, "  (the effective configuration differs; the specific files are not recorded)")
	}
	for _, c := range changes {
		// The kind column matters: the entries are not all filesystem paths.
		// "claude-sandbox" is an image, "CLAUDE.md" is a generated shadow file,
		// and "<merged config>" is the whole merged cascade.
		fmt.Fprintf(env.Err, "  %-8s %-11s %s\n", c.How, c.Kind, c.Path)
	}
	fmt.Fprintln(env.Err, "\nAttaching will NOT apply these changes to the running container.")

	if !env.Prompter.Interactive() {
		return false, false, exitErr(exitDecisionRequired,
			"Error: configuration has changed since session '%s' started, and no terminal is attached.\n"+
				"Pass --allow-config-drift to proceed anyway, or --new to launch a fresh container.", sessionLabel(s))
	}
	fmt.Fprintln(env.Err, "  [c] continue anyway")
	fmt.Fprintln(env.Err, "  [n] new container with the current config")
	fmt.Fprintln(env.Err, "  [q] quit")
	switch strings.ToLower(strings.TrimSpace(env.Prompter.Ask("", "Choice [c/n/q]:", promptTimeout))) {
	case "c":
		return true, false, nil
	case "n":
		return false, true, nil
	default:
		return false, false, nil
	}
}

// warnModelMismatch reports a model difference separately from config drift.
// The model is excluded from the hash because it is a per-session choice, but
// an attach cannot change the model of an already-running session, so silence
// here would be misleading (CS-SESS-027).
func warnModelMismatch(env *Env, s sessions.Session, want string) {
	if want == "" || s.Model == want {
		return
	}
	running := s.Model
	if running == "" {
		running = "(default)"
	}
	fmt.Fprintf(env.Err, "Note: session '%s' is running model %s; --model %s cannot change a running session.\n",
		sessionLabel(s), running, want)
}
