package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/spf13/cobra"

	assets "github.com/kmacmcfarlane/claude-sandbox"
	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/imagebuild"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
	"github.com/kmacmcfarlane/claude-sandbox/internal/layout"
	"github.com/kmacmcfarlane/claude-sandbox/internal/paths"
	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
	"github.com/kmacmcfarlane/claude-sandbox/internal/ralphloop"
)

// Env is the top-level dependency bundle, overridable in tests.
type Env struct {
	Runner   execx.Runner
	Prompter prompt.Prompter
	Out      io.Writer
	Err      io.Writer
	Getenv   func(string) string
}

func defaultEnv() *Env {
	return &Env{
		Runner:   execx.System{},
		Prompter: &prompt.TTY{},
		Out:      os.Stdout,
		Err:      os.Stderr,
		Getenv:   os.Getenv,
	}
}

// Main runs the CLI and returns the process exit code.
func Main(args []string) int {
	return MainWithEnv(args, defaultEnv())
}

// isSubcommand reports whether a is a subcommand name. Subcommands are
// recognized only in argv[1] (CS-INIT-001); anything else takes the launch
// path so "claude-sandbox --rebuild init" errors instead of routing to init.
func isSubcommand(a string) bool {
	switch a {
	case "init", "init-ralph", "ralph", "help", "completion", "sessions":
		return true
	// CS-COMP-002/003: the hidden commands the generated completion scripts
	// call on every keystroke. Without these they fall through to runLaunch,
	// and a TAB press tries to build an image and start a container.
	case cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd:
		return true
	}
	return false
}

func MainWithEnv(args []string, env *Env) int {
	var err error
	if len(args) > 0 && isSubcommand(args[0]) {
		root := newRootCmd(env)
		root.SetArgs(args)
		root.SetOut(env.Out)
		root.SetErr(env.Err)
		err = root.Execute()
	} else {
		err = runLaunch(env, args)
	}
	if err != nil {
		var ce *execx.CodeError
		if ok := errorAs(err, &ce); ok {
			if ce.Msg != "" && ce.Msg != fmt.Sprintf("exit %d", ce.Code) {
				fmt.Fprintln(env.Err, ce.Msg)
			}
			return ce.Code
		}
		fmt.Fprintf(env.Err, "Error: %v\n", err)
		return 2
	}
	return 0
}

func errorAs[T error](err error, target *T) bool {
	for err != nil {
		if t, ok := err.(T); ok {
			*target = t
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func exitErr(code int, format string, a ...any) error {
	return &execx.CodeError{Code: code, Msg: fmt.Sprintf(format, a...)}
}

func newRootCmd(env *Env) *cobra.Command {
	root := &cobra.Command{
		Use:                "claude-sandbox",
		Short:              "Run Claude Code inside a sandboxed Docker container",
		SilenceUsage:       true,
		SilenceErrors:      true,
		DisableFlagParsing: true, // launch grammar is passthrough-heavy; parsed by scanLaunchArgs
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLaunch(env, args)
		},
		// DisableFlagParsing leaves cobra unaware of every launcher flag, so
		// root completes its own command line (CS-COMP-004..013).
		ValidArgsFunction: completeLaunch(env),
	}
	ralphCmd := newRalphCmd(env)
	registerRalphCompletions(ralphCmd)
	root.AddCommand(newInitCmd(env, false), newInitCmd(env, true), ralphCmd, newSessionsCmd(env))
	// CS-INIT-002: a rejected flag names itself and lists the command's valid
	// options (inherited by init/init-ralph/ralph).
	root.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return exitErr(2, "Error: %v\n\nValid %s options:\n%s", err, c.Name(),
			strings.TrimRight(c.LocalFlags().FlagUsages(), "\n"))
	})
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if cmd.Name() == "claude-sandbox" {
			fmt.Fprint(cmd.OutOrStdout(), launchUsage)
			return
		}
		fmt.Fprintln(cmd.OutOrStdout(), cmd.UsageString())
	})
	return root
}

const launchUsage = `Usage:
  claude-sandbox                          # launch claude interactively in $PWD
  claude-sandbox --resume                 # pass args through to claude
  claude-sandbox --continue               # resume the most recent session for this dir
  claude-sandbox --dangerous              # skip permission prompts
  claude-sandbox --docker-socket          # mount the host Docker socket
  claude-sandbox --aws                    # mount ~/.aws/ read-only
  claude-sandbox --git                    # mount ~/.gitconfig read-only
  claude-sandbox --ssh                    # mount ~/.ssh/ read-only
  claude-sandbox --package-caches         # keep go/npm/pip downloads on the host
  claude-sandbox --ralph [ralph-args]     # launch the ralph loop runner
  claude-sandbox --ralph --limit 5        # run ralph for 5 iterations
  claude-sandbox sessions                 # list running sandbox sessions
  claude-sandbox --attach                 # reattach after losing a terminal
  claude-sandbox --branch                 # fork a conversation into a new container
  claude-sandbox init                     # bootstrap .claude-sandbox/ (config, env, gitignore)
  claude-sandbox init-ralph               # bootstrap + seed ralph agent scaffolding
  PROJECT_DIR=/other claude-sandbox       # launch claude in /other

Commands (bootstrap the project, then exit — launcher flags do not apply):
  init                      Bootstrap .claude-sandbox/ in the project (config, env, gitignore, sidecar)
  init-ralph                Like init, plus seed ralph agent/ + scripts/ scaffolding
  sessions [--all] [--json] List running sandbox sessions (this project by default)
  completion SHELL          Print a shell completion script (bash, zsh, fish, powershell)
                            e.g. source <(claude-sandbox completion zsh)
     --track-in-host / --no-track-in-host              set trackInHost (skip the prompt)
     --gitignore / --no-gitignore                      answer the .gitignore prompt
     --copy-parent-dockerfile / --no-copy-parent-dockerfile
                                                       answer the parent-Dockerfile prompt
     --yes                                             accept every prompt's default

Options:
  --help, -h                Show this help message and exit
  --version                 Show claude-sandbox version (host, base image, Claude Code image) and exit
  --ralph                   Launch the ralph loop runner instead of interactive claude
  --limit N                 Run ralph for N iterations (only valid with --ralph)
  --model MODEL             Model to use (alias like 'opus' or a full model ID)
  --dangerous               Skip permission prompts (--dangerously-skip-permissions)
  --rebuild                 Force rebuild of the base, Claude Code, child and run images
                            (--no-cache: also starts the shared package caches empty)
  --update                  Auto-accept the Claude Code update prompt (rebuilds only the CLI image)
  --no-update-check         Skip Claude Code version check
  --docker-socket           Mount the host Docker socket into the container
  --aws                     Mount ~/.aws/ read-only into the container
  --git                     Mount ~/.gitconfig read-only into the container
  --ssh                     Mount ~/.ssh/ read-only into the container
  --package-caches          Mount ~/.cache/claude-sandbox/{go-mod,go-build,npm,pip} writable
                            and point GOMODCACHE/GOCACHE/npm_config_cache/PIP_CACHE_DIR at them

Multiple sessions (when a session is already running for this project):
  --new                     Launch a new container without prompting
  --branch                  Fork a conversation into a new container and work on it
                            in parallel (claude's --resume picker chooses which;
                            works with or without running sessions). Add claude's
                            --name "my-name" to set the fork's display name
  --attach[=INSTANCE]       Reattach to a running session (recovers a lost terminal)
  --join[=INSTANCE]         Start another session inside a running container
  --no-session-check        Skip the multi-session prompt and just launch
  --allow-config-drift      Attach or join even if the config has changed since it started

Environment variables:
  PROJECT_DIR                             Override the project directory (default: $PWD)
  CLAUDE_CONFIG_DIR                       Override the Claude config directory
  CLAUDE_SANDBOX_BASE_ONLY=1              Skip child Dockerfile detection
  CLAUDE_SANDBOX_DANGEROUS=1              Skip permission prompts (same as --dangerous;
                                          also config key 'dangerous: true')
  CLAUDE_SANDBOX_DOCKERFILE_DIR           Override child Dockerfile directory
  CLAUDE_SANDBOX_DOCKERFILE               Override child Dockerfile name
  CLAUDE_SANDBOX_HOST_ACCESS_*_ENABLED    Enable ssh/git/docker-socket/aws/package-caches mounts
  CLAUDE_SANDBOX_NO_UPDATE_CHECK          Skip Claude Code version check

The project is mounted at its REAL host path inside the container so that
docker compose volumes (which the host daemon resolves) work correctly.
`

// launchFlags is the scanned launcher flag set.
type launchFlags struct {
	Help, Version               bool
	Ralph                       bool
	Limit                       string
	Model                       string
	Dangerous                   bool
	Rebuild                     bool
	Update                      bool
	NoUpdateCheck               bool
	SSH, Git, DockerSocket, AWS *bool
	PackageCaches               *bool
	Passthrough                 []string

	// Multi-session bypasses (CS-SESS-028). Each removes a decision, which is
	// what makes them usable with no terminal attached.
	NewSession       bool
	Branch           bool
	Attach           bool
	AttachTarget     string
	Join             bool
	JoinTarget       string
	NoSessionCheck   bool
	AllowConfigDrift bool
}

var knownPassthrough = map[string]bool{
	"--resume": true, "--continue": true, "--verbose": true, "--output-format": true,
	"--allowedTools": true, "--disallowTools": true, "--permission-prompt-tool": true,
	"--mcp-config": true, "--permission-mode": true, "--append-system-prompt": true,
	"--system-prompt": true, "--max-turns": true, "--print": true, "--input-format": true,
	"--model": true, "--fallback-model": true,
	// -n, the short form of --name, needs no entry: single-dash args are
	// positionals to the launcher grammar and already pass through (CS-LNCH-002).
	"--name": true,
}

// scanLaunchArgs implements the launcher grammar (CS-LNCH-001..005): known
// launcher flags are consumed; a known claude flag or "--" or a positional
// argument ends parsing (the rest passes through); an unknown flag errors.
func scanLaunchArgs(args []string) (*launchFlags, error) {
	f := &launchFlags{}
	boolTrue := func() *bool { t := true; return &t }
	i := 0
	for i < len(args) {
		a := args[i]
		switch a {
		case "--help", "-h":
			f.Help = true
			i++
		case "--version":
			f.Version = true
			i++
		case "--ralph":
			f.Ralph = true
			i++
		case "--limit":
			if i+1 >= len(args) || args[i+1] == "" {
				return nil, exitErr(2, "Error: --limit requires a value")
			}
			f.Limit = args[i+1]
			i += 2
		case "--model":
			if i+1 >= len(args) || args[i+1] == "" {
				return nil, exitErr(2, "Error: --model requires a value")
			}
			f.Model = args[i+1]
			i += 2
		case "--rebuild":
			f.Rebuild = true
			i++
		case "--update":
			f.Update = true
			i++
		case "--no-update-check":
			f.NoUpdateCheck = true
			i++
		case "--dangerous", "--dangerously-skip-permissions":
			f.Dangerous = true
			i++
		case "--host-access-ssh-enabled", "--ssh":
			f.SSH = boolTrue()
			i++
		case "--host-access-git-enabled", "--git":
			f.Git = boolTrue()
			i++
		case "--host-access-docker-socket-enabled", "--docker-socket":
			f.DockerSocket = boolTrue()
			i++
		case "--host-access-aws-enabled", "--aws":
			f.AWS = boolTrue()
			i++
		case "--host-access-package-caches-enabled", "--package-caches":
			f.PackageCaches = boolTrue()
			i++
		case "--new":
			f.NewSession = true
			i++
		case "--branch":
			f.Branch = true
			i++
		case "--no-session-check":
			f.NoSessionCheck = true
			i++
		case "--allow-config-drift":
			f.AllowConfigDrift = true
			i++
		case "--attach":
			// Bare --attach is valid: with one candidate it is unambiguous, with
			// several it prompts (or fails without a terminal) — CS-SESS-029.
			f.Attach = true
			i++
		case "--join":
			f.Join = true
			i++
		case "--":
			f.Passthrough = args[i+1:]
			return f, nil
		default:
			// --attach=NOUN / --join=NOUN. Handled here rather than as cases so
			// the bare forms above keep working.
			if v, ok := flagValue(a, "--attach"); ok {
				f.Attach, f.AttachTarget = true, v
				i++
				continue
			}
			if v, ok := flagValue(a, "--join"); ok {
				f.Join, f.JoinTarget = true, v
				i++
				continue
			}
			return scanTail(f, args, i)
		}
	}
	return f, nil
}

// flagValue splits "--flag=value", returning false unless the value is present
// and non-empty.
func flagValue(arg, name string) (string, bool) {
	if !strings.HasPrefix(arg, name+"=") {
		return "", false
	}
	v := strings.TrimPrefix(arg, name+"=")
	return v, v != ""
}

// scanTail handles the end of the launcher grammar: a known claude flag or a
// positional argument ends parsing and everything from there passes through; an
// unknown flag is an error.
func scanTail(f *launchFlags, args []string, i int) (*launchFlags, error) {
	a := args[i]
	if strings.HasPrefix(a, "--") {
		if knownPassthrough[a] {
			f.Passthrough = args[i:]
			return f, nil
		}
		return nil, exitErr(2, "Error: unknown flag '%s'", a)
	}
	if a == "init" || a == "init-ralph" {
		return nil, exitErr(2, "Error: '%s' must be the first argument (claude-sandbox %s [options])", a, a)
	}
	f.Passthrough = args[i:]
	return f, nil
}

// repoRoot locates the sandbox repo checkout: CLAUDE_SANDBOX_REPO_ROOT
// override, else the parent of the binary's bin/ directory.
func repoRoot(getenv func(string) string) string {
	if r := getenv("CLAUDE_SANDBOX_REPO_ROOT"); r != "" {
		return r
	}
	exe, err := os.Executable()
	if err != nil {
		wd, _ := os.Getwd()
		return wd
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	return filepath.Dir(filepath.Dir(exe))
}

func resolveProjectDir(getenv func(string) string) (string, error) {
	dir := getenv("PROJECT_DIR")
	if dir == "" {
		return os.Getwd()
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if fi, serr := os.Stat(abs); serr != nil || !fi.IsDir() {
		return "", fmt.Errorf("PROJECT_DIR %s is not a directory", dir)
	}
	if resolved, rerr := filepath.EvalSymlinks(abs); rerr == nil {
		return resolved, nil
	}
	return abs, nil
}

func envTrue(v string) bool { return v == "1" || v == "true" || v == "yes" }

// validateBranch rejects flag combinations that contradict --branch
// (CS-SESS-042). Branch always means a NEW container forking a conversation,
// so entering an existing session (attach/join), ralph mode (which owns its
// own --resume semantics), or a second resume flag for claude are all errors.
func validateBranch(f *launchFlags) error {
	if !f.Branch {
		return nil
	}
	switch {
	case f.Ralph:
		return exitErr(2, "Error: --branch is not valid with --ralph (ralph has its own --resume)")
	case f.Attach:
		return exitErr(2, "Error: --branch conflicts with --attach: branch forks a conversation into a new container")
	case f.Join:
		return exitErr(2, "Error: --branch conflicts with --join: branch forks a conversation into a new container")
	}
	if len(f.Passthrough) > 0 {
		if p := f.Passthrough[0]; p == "--resume" || p == "--continue" {
			return exitErr(2, "Error: --branch already implies a resume (--resume --fork-session); drop %s or use it without --branch", p)
		}
	}
	return nil
}

func runLaunch(env *Env, args []string) error {
	f, err := scanLaunchArgs(args)
	if err != nil {
		return err
	}
	if f.Help {
		fmt.Fprint(env.Out, launchUsage)
		return nil
	}
	rr := repoRoot(env.Getenv)
	version := imagebuild.Version(env.Runner, rr)
	if f.Version {
		imagebuild.PrintVersion(imagebuild.Options{Runner: env.Runner, Out: env.Out, RepoRoot: rr, Version: version})
		return nil
	}
	if f.Limit != "" && !f.Ralph {
		return exitErr(2, "Error: --limit is only valid with --ralph")
	}
	if err := validateBranch(f); err != nil {
		return err
	}
	projectDir, err := resolveProjectDir(env.Getenv)
	if err != nil {
		return err
	}

	// Config + env cascade (CS-CASC, CS-LNCH-024).
	configFiles, err := paths.CollectUp(projectDir, paths.Config)
	if err != nil {
		return err
	}
	envFiles, err := paths.CollectUp(projectDir, paths.Env)
	if err != nil {
		return err
	}
	cascade.PrintReport(env.Out, projectDir)
	cfg, err := cascade.Load(configFiles)
	if err != nil {
		return err
	}
	if err := cfg.Validate(configFiles); err != nil {
		return err
	}
	if len(envFiles) == 0 {
		fmt.Fprintf(env.Err, "WARNING: env file not found (.claude-sandbox/env) in %s or any parent\n\n", projectDir)
		fmt.Fprintf(env.Err, "This file provides environment variables needed by Claude Code\n")
		fmt.Fprintf(env.Err, "(e.g. DISCORD_WEBHOOK_URL for MCP server notifications).\n\n")
		fmt.Fprintf(env.Err, "To create it, bootstrap the project and fill in your values:\n")
		fmt.Fprintf(env.Err, "  claude-sandbox init   # creates .claude-sandbox/env (and config)\n")
	} else {
		// Warn-only lint of every cascade level (CS-CASC-020).
		cascade.LintEnvFiles(env.Err, envFiles)
	}

	// Multi-session decision (CS-SESS-014..019). Deliberately before any image
	// work: building an image the user is about to bypass by attaching is waste.
	decision, err := decideSessions(env, projectDir, f)
	if err != nil {
		return err
	}
	switch decision.Action {
	case actionQuit:
		return nil
	case actionAttach, actionJoin:
		done, aerr := joinExistingSession(env, projectDir, f, cfg, envFiles, decision)
		if aerr != nil {
			return aerr
		}
		if done {
			return nil
		}
		// The drift prompt chose a new container instead: fall through and launch.
	}

	// A branch is an ordinary new container whose claude invocation forks an
	// existing conversation (CS-SESS-039/040). The flags go ahead of the user's
	// passthrough; the fingerprint is unaffected because passthrough args are
	// per-session choices excluded from the config hash (CS-SESS-020).
	passthrough := f.Passthrough
	if len(decision.BranchArgs) > 0 {
		passthrough = append(append([]string{}, decision.BranchArgs...), passthrough...)
	}

	noUpdate := f.NoUpdateCheck || envTrue(env.Getenv("CLAUDE_SANDBOX_NO_UPDATE_CHECK")) || cfg.DisableUpdateCheck

	// CS-LNCH-038: dangerous mode is durable via env var or config, not just
	// the flag. A more-local "dangerous: false" overrides an upstream true
	// through the ordinary cascade merge before this OR is evaluated.
	dangerous := f.Dangerous || envTrue(env.Getenv("CLAUDE_SANDBOX_DANGEROUS")) || cfg.Dangerous

	// Images (CS-IMG). Order: base, CLI image, update check (CLI only), child,
	// cap. A Claude Code update never touches the base or the child.
	imgOpts := imagebuild.Options{
		Runner: env.Runner, Prompter: env.Prompter, Out: env.Out, Err: env.Err,
		RepoRoot: rr, Version: version,
		ForceRebuild: f.Rebuild, NoUpdateCheck: noUpdate, AutoUpdate: f.Update,
	}
	if err := imagebuild.EnsureBuildKit(imgOpts); err != nil {
		return exitErr(2, "%s", err.Error())
	}
	baseRebuilt, err := imagebuild.EnsureBase(imgOpts)
	if err != nil {
		return err
	}
	cliBuilt, err := imagebuild.EnsureCLI(imgOpts)
	if err != nil {
		return err
	}
	if imagebuild.UpdateCheck(imgOpts, cliBuilt) {
		cliBuilt = true
	}

	// Layout adoption (CS-LAY-015/016).
	mode := paths.LayoutMode(projectDir)
	if mode == "none" && f.Ralph {
		if err := os.MkdirAll(paths.SandboxDir(projectDir), 0o755); err != nil {
			return err
		}
		mode = "new"
	}
	if mode == "new" {
		track := false
		if cfg.TrackInHost != nil {
			track = *cfg.TrackInHost
		}
		if err := layout.Setup(projectDir, track, layout.Options{
			Runner: env.Runner, Prompter: env.Prompter, Out: env.Out, Err: env.Err,
		}); err != nil {
			return err
		}
	}

	// Child image (CS-IMG-010..017).
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
	}, env.Out)
	parent, childBuilt, err := imagebuild.EnsureChild(imgOpts, spec, baseRebuilt, baseOnly)
	if err != nil {
		return err
	}

	// Run image: the cap over the base or child (CS-IMG-024..026).
	image, capBuilt, err := imagebuild.EnsureCap(imgOpts, parent)
	if err != nil {
		return err
	}
	if baseRebuilt || cliBuilt || childBuilt || capBuilt {
		imagebuild.WarnCacheBudget(imgOpts)
	}

	// Launch plan (CS-LNCH).
	uid, gid, uname, home := hostIdentity(env.Getenv)
	in := launch.Inputs{
		ProjectDir: projectDir, Home: home,
		HostUID: uid, HostGID: gid, HostUser: uname,
		Getenv:    env.Getenv,
		RalphMode: f.Ralph, Limit: f.Limit, SkipPermissions: dangerous,
		CLIModel: f.Model, Passthrough: passthrough,
		CLISSH: f.SSH, CLIGit: f.Git, CLIDockerSocket: f.DockerSocket, CLIAWS: f.AWS,
		CLIPackageCaches: f.PackageCaches,
		Cfg:              cfg, EnvFiles: envFiles, ImageName: image,
		ImageID:  imagebuild.ImageID(env.Runner, image),
		Instance: newInstance(env, projectDir, f),
		Version:  version,
		Out:      env.Out, Err: env.Err,
	}
	plan, err := launch.Build(in)
	if err != nil {
		return err
	}
	return plan.Exec(env.Runner, projectDir)
}

func hostIdentity(getenv func(string) string) (uid, gid int, username, home string) {
	uid, gid = os.Getuid(), os.Getgid()
	if home = getenv("HOME"); home == "" {
		home, _ = os.UserHomeDir()
	}
	if u, err := user.Current(); err == nil {
		username = u.Username
		if home == "" {
			home = u.HomeDir
		}
	}
	return uid, gid, username, home
}

func newRalphCmd(env *Env) *cobra.Command {
	o := ralphloop.Options{}
	var watchdog int
	var limit, runlog, rawlog string
	cmd := &cobra.Command{
		Use:           "ralph",
		Short:         "Run the ralph fresh-context loop (in-container)",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return exitErr(2, "Unknown argument: %s", args[0])
			}
			if limit != "" {
				n, err := ralphloop.ParsePositiveInt(limit)
				if err != nil {
					return exitErr(2, "--limit must be a positive integer")
				}
				o.Limit = n
			}
			if watchdog == 0 {
				o.WatchdogTimeout = -1 // 0 disables
			} else {
				o.WatchdogTimeout = watchdog
			}
			o.RunlogFile = runlog
			o.RawLogBase = rawlog
			o.Runner = env.Runner
			o.Out = env.Out
			o.Err = env.Err
			o.PromptRalph = assets.PromptRalph

			// CS-RLP-017: SIGINT terminates the in-flight iteration promptly.
			var interrupted atomic.Bool
			o.Interrupted = func() bool { return interrupted.Load() }
			sig := make(chan os.Signal, 1)
			signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
			defer signal.Stop(sig)
			go func() {
				<-sig
				interrupted.Store(true)
				fmt.Fprintln(env.Out, "\nInterrupt received. Stopping...")
				ralphloop.Terminate()
			}()

			if code := ralphloop.Run(o); code != 0 {
				return &execx.CodeError{Code: code}
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&limit, "limit", "", "Stop after N iterations (default: 30)")
	fl.StringVar(&o.StopFile, "stop-file", "", "Stop file path (default: <ralph-dir>/stop)")
	fl.StringVar(&o.PromptFile, "prompt", "", "Prompt file path (default: <agent-dir>/PROMPT.md)")
	fl.StringVar(&o.ClaudeBin, "claude-bin", "", "Claude Code CLI (default: claude)")
	fl.BoolVar(&o.Interactive, "interactive", false, "Run claude interactively")
	fl.StringVar(&o.Model, "model", "", "Model to use (forwarded to claude as --model)")
	fl.BoolVar(&o.SkipPermissions, "dangerous", false, "Pass --dangerously-skip-permissions to claude")
	fl.BoolVar(&o.SkipPermissions, "dangerously-skip-permissions", false, "Alias of --dangerous")
	fl.BoolVar(&o.Resume, "resume", false, "Pass --resume to claude on the first iteration")
	fl.StringVar(&runlog, "runlog-file", "", "Run log path (default: <ralph-dir>/runlog.json)")
	fl.StringVar(&rawlog, "raw-log", "", "Raw NDJSON base path (default: <ralph-dir>/runlogs/rawlog)")
	fl.IntVar(&watchdog, "watchdog-timeout", 15, "Inactivity timeout in minutes (0 to disable)")
	fl.IntVar(&o.IterationTimeout, "iteration-timeout", 7200, "Hard iteration time limit in seconds")
	fl.IntVar(&o.MaxRetries, "max-retries", 5, "Consecutive rate-limit retries before exiting")
	fl.IntVar(&o.RetryDelay, "retry-delay", 30, "Initial backoff delay in seconds")
	fl.IntVar(&o.QuotaPause, "quota-pause", 300, "Seconds between re-probes on quota exhaustion")
	fl.IntVar(&o.QuotaMaxWait, "quota-max-wait", 18000, "Max seconds to wait for quota reset")
	fl.MarkHidden("dangerously-skip-permissions")
	return cmd
}
