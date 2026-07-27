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
	case "init", "init-ralph", "ralph", "help", "completion":
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
	}
	root.AddCommand(newInitCmd(env, false), newInitCmd(env, true), newRalphCmd(env))
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
  claude-sandbox --dangerous              # skip permission prompts
  claude-sandbox --docker-socket          # mount the host Docker socket
  claude-sandbox --aws                    # mount ~/.aws/ read-only
  claude-sandbox --git                    # mount ~/.gitconfig read-only
  claude-sandbox --ssh                    # mount ~/.ssh/ read-only
  claude-sandbox --ralph [ralph-args]     # launch the ralph loop runner
  claude-sandbox --ralph --limit 5        # run ralph for 5 iterations
  claude-sandbox init                     # bootstrap .claude-sandbox/ (config, env, gitignore)
  claude-sandbox init-ralph               # bootstrap + seed ralph agent scaffolding
  PROJECT_DIR=/other claude-sandbox       # launch claude in /other

Commands (bootstrap the project, then exit — launcher flags do not apply):
  init                      Bootstrap .claude-sandbox/ in the project (config, env, gitignore, sidecar)
  init-ralph                Like init, plus seed ralph agent/ + scripts/ scaffolding
     --track-in-host / --no-track-in-host              set trackInHost (skip the prompt)
     --gitignore / --no-gitignore                      answer the .gitignore prompt
     --copy-parent-dockerfile / --no-copy-parent-dockerfile
                                                       answer the parent-Dockerfile prompt
     --yes                                             accept every prompt's default

Options:
  --help, -h                Show this help message and exit
  --version                 Show claude-sandbox version (host + baked image) and exit
  --ralph                   Launch the ralph loop runner instead of interactive claude
  --limit N                 Run ralph for N iterations (only valid with --ralph)
  --model MODEL             Model to use (alias like 'opus' or a full model ID)
  --dangerous               Skip permission prompts (--dangerously-skip-permissions)
  --rebuild                 Force rebuild of base and child images
  --update                  Auto-accept the Claude Code update rebuild prompt
  --no-update-check         Skip Claude Code version check
  --docker-socket           Mount the host Docker socket into the container
  --aws                     Mount ~/.aws/ read-only into the container
  --git                     Mount ~/.gitconfig read-only into the container
  --ssh                     Mount ~/.ssh/ read-only into the container

Environment variables:
  PROJECT_DIR                             Override the project directory (default: $PWD)
  CLAUDE_CONFIG_DIR                       Override the Claude config directory
  CLAUDE_SANDBOX_BASE_ONLY=1              Skip child Dockerfile detection
  CLAUDE_SANDBOX_DOCKERFILE_DIR           Override child Dockerfile directory
  CLAUDE_SANDBOX_DOCKERFILE               Override child Dockerfile name
  CLAUDE_SANDBOX_HOST_ACCESS_*_ENABLED    Enable ssh/git/docker-socket/aws mounts
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
	Passthrough                 []string
}

var knownPassthrough = map[string]bool{
	"--resume": true, "--continue": true, "--verbose": true, "--output-format": true,
	"--allowedTools": true, "--disallowTools": true, "--permission-prompt-tool": true,
	"--mcp-config": true, "--permission-mode": true, "--append-system-prompt": true,
	"--system-prompt": true, "--max-turns": true, "--print": true, "--input-format": true,
	"--model": true, "--fallback-model": true,
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
		case "--":
			f.Passthrough = args[i+1:]
			return f, nil
		default:
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
	}
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
	}

	noUpdate := f.NoUpdateCheck || envTrue(env.Getenv("CLAUDE_SANDBOX_NO_UPDATE_CHECK")) || cfg.DisableUpdateCheck

	// Images (CS-IMG).
	imgOpts := imagebuild.Options{
		Runner: env.Runner, Prompter: env.Prompter, Out: env.Out, Err: env.Err,
		RepoRoot: rr, Version: version,
		ForceRebuild: f.Rebuild, NoUpdateCheck: noUpdate, AutoUpdate: f.Update,
	}
	baseRebuilt, err := imagebuild.EnsureBase(imgOpts)
	if err != nil {
		return err
	}
	if imagebuild.UpdateCheck(imgOpts, baseRebuilt) {
		baseRebuilt = true
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
		ProjectDir: projectDir, Slug: imagebuild.Slug(projectDir),
		BaseOnly: baseOnly, DockerfileDir: dfDir, Dockerfile: dfName,
	}, env.Out)
	image, err := imagebuild.EnsureChild(imgOpts, spec, baseRebuilt, baseOnly)
	if err != nil {
		return err
	}

	// Launch plan (CS-LNCH).
	uid, gid, uname, home := hostIdentity(env.Getenv)
	in := launch.Inputs{
		ProjectDir: projectDir, Home: home,
		HostUID: uid, HostGID: gid, HostUser: uname,
		Getenv:    env.Getenv,
		RalphMode: f.Ralph, Limit: f.Limit, SkipPermissions: f.Dangerous,
		CLIModel: f.Model, Passthrough: f.Passthrough,
		CLISSH: f.SSH, CLIGit: f.Git, CLIDockerSocket: f.DockerSocket, CLIAWS: f.AWS,
		Cfg: cfg, EnvFiles: envFiles, ImageName: image,
		Out: env.Out, Err: env.Err,
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
