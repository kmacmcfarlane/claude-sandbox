package main

// Multi-session support: the `sessions` subcommand plus the launch-time
// decision of whether to start a new container, join an existing one, reattach
// to one, or stop. Spec: spec/sessions.feature (CS-SESS).

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/imagebuild"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
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
		Short:         "List running sandbox sessions",
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
			if asJSON {
				b, merr := sessions.MarshalJSON(found)
				if merr != nil {
					return merr
				}
				fmt.Fprintln(env.Out, string(b))
				return nil
			}
			if len(found) == 0 {
				if all {
					fmt.Fprintln(env.Out, "No running sandbox sessions.")
				} else {
					fmt.Fprintln(env.Out, "No running sandbox sessions for this project.")
				}
				return nil
			}
			printSessionTable(env, found, all, projectDir)
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "List sessions for every project, not just this one")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit JSON")
	return cmd
}

// printSessionTable renders the human-readable listing (CS-SESS-010/011).
func printSessionTable(env *Env, found []sessions.Session, all bool, projectDir string) {
	header := []string{"INSTANCE", "NAME", "MODE", "UP", "SESSIONS"}
	if all {
		header = append(header, "PROJECT")
	}
	rows := [][]string{header}
	for _, s := range found {
		instance := s.Instance
		if instance == "" {
			instance = "-"
		}
		mark := ""
		if all && s.Project == projectDir {
			mark = "* "
		}
		row := []string{mark + instance, s.Name, s.Mode, uptime(s.Status), fmt.Sprint(s.Count)}
		if all {
			row = append(row, s.Project)
		}
		rows = append(rows, row)
	}
	widths := make([]int, len(header))
	for _, r := range rows {
		for i, c := range r {
			if len(c) > widths[i] {
				widths[i] = len(c)
			}
		}
	}
	for _, r := range rows {
		var b strings.Builder
		for i, c := range r {
			b.WriteString(c)
			if i < len(r)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-len(c)+2))
			}
		}
		fmt.Fprintln(env.Out, strings.TrimRight(b.String(), " "))
	}
	if all {
		fmt.Fprintln(env.Out, "\n* = this project")
	}
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
		if len(found) > 0 {
			reportRunning(env, found)
		}
		return sessionDecision{Action: actionNew}, nil
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
	reportRunning(env, candidates)
	if !env.Prompter.Interactive() {
		return sessionDecision{}, exitErr(exitDecisionRequired,
			"Error: %d session(s) already running for this project and no terminal is attached.\n"+
				"Choose explicitly: --new, --branch, --attach[=INSTANCE], --join[=INSTANCE], or --no-session-check.",
			len(candidates))
	}

	fmt.Fprintln(env.Err, "  [n] new session in a new container   (isolated; attachable if your terminal drops)")
	fmt.Fprintln(env.Err, "  [b] branch the newest conversation into a new container   (fork it; both continue independently)")
	fmt.Fprintln(env.Err, "  [j] new session in an existing container   (dies with that container's primary; not attachable later)")
	fmt.Fprintln(env.Err, "  [a] attach to an existing session   (shares the terminal if someone is already using it)")
	fmt.Fprintln(env.Err, "  [q] quit")
	switch strings.ToLower(strings.TrimSpace(env.Prompter.Ask("", "Choice [n/b/j/a/q]:", promptTimeout))) {
	case "n", "new":
		return sessionDecision{Action: actionNew}, nil
	case "b", "branch":
		// The newest conversation for this directory is the running session's;
		// to branch a different one, use --branch for claude's picker instead.
		return sessionDecision{Action: actionNew, BranchArgs: branchNewestArgs}, nil
	case "j", "join":
		s, serr := selectInstance(env, candidates, "join")
		return sessionDecision{Action: actionJoin, Target: s}, serr
	case "a", "attach":
		s, serr := selectInstance(env, candidates, "attach")
		return sessionDecision{Action: actionAttach, Target: s}, serr
	default:
		// Includes "q" and the empty answer: Ask returns "" on Enter, on EOF and
		// on timeout, none of which is consent to start or join anything.
		return sessionDecision{Action: actionQuit}, nil
	}
}

func reportRunning(env *Env, found []sessions.Session) {
	fmt.Fprintf(env.Err, "\nFound %d running session(s) for this project:\n", len(found))
	for _, s := range found {
		label := s.Instance
		if label == "" {
			label = s.Mode
		}
		fmt.Fprintf(env.Err, "  %-10s up %-12s %d session(s)\n", label, uptime(s.Status), s.Count)
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
	reportRunning(env, candidates)
	return selectInstance(env, candidates, verb)
}

// selectInstance is the second tier: it is skipped entirely when there is only
// one candidate, so the common case stays a single prompt (CS-SESS-017).
func selectInstance(env *Env, candidates []sessions.Session, verb string) (sessions.Session, error) {
	if len(candidates) == 1 {
		fmt.Fprintf(env.Err, "Using the only running session: %s\n", candidates[0].Instance)
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
func joinExistingSession(env *Env, projectDir string, f *launchFlags, cfg *cascade.Config, envFiles []string, d sessionDecision) (bool, error) {
	model := f.Model
	if model == "" {
		model = cfg.Model
	}

	wantHash, wantInputs := wouldBeFingerprint(env, projectDir, f, cfg, envFiles)
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
		return true, attachTo(env, d.Target, cfg.DetachKeys)
	}
	_, _, hostUser, _ := hostIdentity(env.Getenv)
	return true, joinInto(env, d.Target, projectDir, hostUser, model, cfg.DetachKeys, f)
}

// wouldBeFingerprint computes the config hash a launch would produce right now,
// without building anything. The image ID is read from the resolved image if it
// already exists; when it does not, the empty ID is itself a difference, which
// is correct — the launch would have built a new image.
func wouldBeFingerprint(env *Env, projectDir string, f *launchFlags, cfg *cascade.Config, envFiles []string) (string, []launch.InputDigest) {
	baseOnly := cfg.BaseOnly || envTrue(env.Getenv("CLAUDE_SANDBOX_BASE_ONLY"))
	dfDir := env.Getenv("CLAUDE_SANDBOX_DOCKERFILE_DIR")
	if dfDir == "" {
		dfDir = cfg.DockerfileDir
	}
	dfName := env.Getenv("CLAUDE_SANDBOX_DOCKERFILE")
	if dfName == "" {
		dfName = cfg.Dockerfile
	}
	spec := imagebuild.ResolveChild(imagebuild.ChildInputs{
		ProjectDir: projectDir,
		BaseOnly:   baseOnly, DockerfileDir: dfDir, Dockerfile: dfName,
	}, io.Discard)

	parent := spec.ImageName
	if !spec.Use {
		parent = imagebuild.BaseImageName
	}
	// The container runs the cap, not its parent (CS-SESS-038): a CLI update
	// changes the cap's ID while the parent's stays put.
	image := imagebuild.CapImageName(parent)
	id := imagebuild.ImageID(env.Runner, image)

	uid, gid, uname, home := hostIdentity(env.Getenv)
	plan, err := launch.Build(launch.Inputs{
		ProjectDir: projectDir, Home: home,
		HostUID: uid, HostGID: gid, HostUser: uname,
		Getenv:    env.Getenv,
		RalphMode: f.Ralph, Limit: f.Limit, SkipPermissions: f.Dangerous,
		CLIModel: f.Model, Passthrough: f.Passthrough,
		CLISSH: f.SSH, CLIGit: f.Git, CLIDockerSocket: f.DockerSocket, CLIAWS: f.AWS,
		CLIPackageCaches: f.PackageCaches,
		Cfg:              cfg, EnvFiles: envFiles,
		ImageName: image, ImageID: id,
		Out: io.Discard, Err: io.Discard,
	})
	if err != nil {
		// Without a comparable hash, drift cannot be judged; confirmDrift treats
		// an empty want-hash as "no opinion" rather than blocking the attach.
		return "", nil
	}
	return plan.ConfigHash, plan.ConfigInputs
}

// newInstance picks the instance noun for a container about to be launched.
// Ralph gets none: it is single-instance, so there is nothing to disambiguate.
//
// The noun is chosen from those NOT already in use by this project, which is
// why a short word list suffices — see sessions.PickNoun.
func newInstance(env *Env, projectDir string, f *launchFlags) string {
	if f.Ralph {
		return ""
	}
	found, err := sessions.Discover(env.Runner, projectDir)
	if err != nil {
		// Discovery failing must not block a launch; an unfiltered pick is still
		// overwhelmingly likely to be unique.
		return sessions.PickNoun(nil, nil)
	}
	return sessions.PickNoun(sessions.Instances(found), nil)
}

// attachTo hands the process over to `docker attach` (CS-SESS-031).
func attachTo(env *Env, s sessions.Session, configuredKeys string) error {
	detachKeys := launch.ResolveDetachKeys(configuredKeys)
	fmt.Fprintf(env.Out, "Attaching to %s. Press %s to detach without stopping it.\n", s.Instance, detachKeys)
	// Docker cannot report whether another client is already attached, so this
	// cannot be prevented — only mentioned.
	fmt.Fprintln(env.Out, "If someone else is already attached, you will share the terminal.")
	return env.Runner.Exec(execx.Cmd{
		Name: "docker",
		Args: []string{"attach", "--detach-keys=" + detachKeys, s.Name},
	})
}

// joinInto starts another claude inside a running container (CS-SESS-032).
func joinInto(env *Env, s sessions.Session, projectDir, hostUser, model, configuredKeys string, f *launchFlags) error {
	detachKeys := launch.ResolveDetachKeys(configuredKeys)
	fmt.Fprintf(env.Out, "Starting a new session inside %s.\n", s.Instance)
	fmt.Fprintln(env.Out, "Note: this session ends if that container's primary session exits, and it cannot be reattached.")
	// Detaching from an exec'd session orphans it beyond recovery, so this is
	// the path where docker's ctrl-p,ctrl-q default does the most damage: the
	// TUI binds ctrl+p, so a stray ctrl+p then ctrl+q would silently lose the
	// session. Never leave these keys to docker's default.
	fmt.Fprintf(env.Out, "Press %s to detach — but note that a detached joined session cannot be recovered.\n", detachKeys)

	// -u is required: exec skips the entrypoint's gosu step, and the image ends
	// USER root. -w is redundant (docker run's -w is inherited via
	// Config.WorkingDir) but passed so the working directory never depends on
	// how the container happened to be started.
	args := []string{"exec", "-it", "--detach-keys=" + detachKeys, "-u", hostUser, "-w", projectDir, s.Name, "claude"}
	if f.Dangerous {
		args = append(args, "--dangerously-skip-permissions")
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, f.Passthrough...)
	return env.Runner.Exec(execx.Cmd{Name: "docker", Args: args})
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
	fmt.Fprintf(env.Err, "\nSession '%s' was started with different configuration:\n", s.Instance)
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
				"Pass --allow-config-drift to proceed anyway, or --new to launch a fresh container.", s.Instance)
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
		s.Instance, running, want)
}
