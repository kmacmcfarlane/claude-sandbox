package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"

	assets "github.com/kmacmcfarlane/claude-sandbox"
	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/imagebuild"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
	"github.com/kmacmcfarlane/claude-sandbox/internal/layout"
	"github.com/kmacmcfarlane/claude-sandbox/internal/paths"
	"github.com/kmacmcfarlane/claude-sandbox/internal/pidslot"
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
	// LookupEnv tells set-but-empty from unset (the env override notice
	// resolves bare env-file keys with it, CS-CASC-027). defaultEnv wires
	// os.LookupEnv. Nil falls back to Getenv, which cannot see set-but-empty
	// (it reads as unset); only test Envs that never launch leave it nil.
	LookupEnv func(string) (string, bool)

	// PidslotOps overrides the pidslot helper's process seams under test.
	PidslotOps *pidslot.Ops

	// Lock is the host launch lock (CS-SESS-048); nil means the real flock on
	// ~/.cache/claude-sandbox/launch.lock. Tests inject a recording fake.
	Lock launch.HostLock
	// Now is the clock stale reservations are judged by (CS-SESS-052); nil
	// means time.Now.
	Now func() time.Time
	// IsTerminal decides whether a writer is a terminal, for the terminal
	// reset before an OOM report (CS-LNCH-092); nil means execx.IsTerminal.
	IsTerminal func(io.Writer) bool
	// TempRoot is where shadow directories are made and swept
	// (CS-LNCH-080..084); "" means os.TempDir(). Tests must point it at a
	// scratch directory: under go test the launch panics on the real temp
	// root rather than sweep it (launch.shadowRoot).
	TempRoot string
	// CacheDir is where the detached cache-budget checker writes its result
	// and the next launch reads it (CS-IMG-041..043); "" means
	// $HOME/.cache/claude-sandbox. Tests point it at a scratch directory.
	CacheDir string
	// Executable is the binary the detached checker runs as; nil means
	// os.Executable. The checker is started through Runner.Start with
	// Cmd.Detach, so under execx.Fake nothing is spawned.
	Executable func() (string, error)
}

// cacheDir resolves Env.CacheDir.
func (e *Env) cacheDir() string {
	if e.CacheDir != "" {
		return e.CacheDir
	}
	if testing.Testing() {
		// A forgotten fixture would consume (and delete) the operator's real
		// result file, as launch.shadowRoot guards the real temp root.
		panic("a test resolved the real cache dir; set Env.CacheDir to a scratch directory such as GinkgoT().TempDir()")
	}
	_, _, _, home := hostIdentity(e.Getenv)
	return filepath.Join(home, launch.SandboxHomeRoot)
}

// lookupEnv is Env.LookupEnv with the Getenv fallback.
func (e *Env) lookupEnv(k string) (string, bool) {
	if e.LookupEnv != nil {
		return e.LookupEnv(k)
	}
	v := e.Getenv(k)
	return v, v != ""
}

func defaultEnv() *Env {
	return &Env{
		Runner:    execx.System{},
		Prompter:  &prompt.TTY{},
		Out:       os.Stdout,
		Err:       os.Stderr,
		Getenv:    os.Getenv,
		LookupEnv: os.LookupEnv,
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
	case "init", "init-ralph", "ralph", "help", "completion", "sessions", "pidslot", "headless", cacheBudgetCheckCmd, imagebuild.PrefetchSubcommand:
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
	if len(args) > 0 && args[0] == "headless" {
		// Straight to the headless launch, not through cobra: everything after
		// "headless" is the launcher grammar plus claude's args, verbatim
		// (CS-LNCH-058). The cobra command exists for help and completion.
		err = runHeadless(env, args[1:])
	} else if len(args) > 0 && isSubcommand(args[0]) {
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
	root.AddCommand(newInitCmd(env, false), newInitCmd(env, true), ralphCmd, newSessionsCmd(env), newPidslotCmd(env), newHeadlessCmd(env), newCacheBudgetCheckCmd(env), newCLIPrefetchCmd(env))
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
		if cmd.Name() == "headless" {
			// Its flags are the launcher's, scanned by scanArgs; cobra's
			// usage would list only a "-h" that belongs to claude here.
			fmt.Fprintln(cmd.OutOrStdout(), cmd.Long)
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
  claude-sandbox --worktree               # work in a private worktree, not the shared checkout
  claude-sandbox init                     # bootstrap .claude-sandbox/ (config, env, gitignore)
  claude-sandbox init-ralph               # bootstrap + seed ralph agent scaffolding
  PROJECT_DIR=/other claude-sandbox       # launch claude in /other

Commands (bootstrap the project, then exit — launcher flags do not apply):
  init                      Bootstrap .claude-sandbox/ in the project (config, env, gitignore, sidecar)
  init-ralph                Like init, plus seed ralph agent/ + scripts/ scaffolding
  sessions [--all] [--json] List running sandbox sessions (this project by default)
  headless [flags] -- ARGS  Launch claude for an SDK client such as Paseo: no TTY, stdout
                            carries only claude's output, never prompts, always a new
                            container; everything after -- goes to claude verbatim
  completion SHELL          Print a shell completion script (bash, zsh, fish, powershell)
                            e.g. source <(claude-sandbox completion zsh)
     --track-in-host / --no-track-in-host              set trackInHost (skip the prompt)
     --gitignore / --no-gitignore                      add (default) / skip the host .gitignore entries
     --copy-parent-dockerfile / --no-copy-parent-dockerfile
                                                       copy a parent Dockerfile into Dockerfile.example
                                                       (default when one exists) / seed the generic one
     --yes                                             accept the trackInHost prompt's default

Options:
  --help, -h                Show this help message and exit
  --version                 Show claude-sandbox version (host, base image, Claude Code image) and exit
  --ralph                   Launch the ralph loop runner instead of interactive claude
  --limit N                 Run ralph for N iterations (only valid with --ralph)
  --model MODEL             Model to use (alias like 'opus' or a full model ID)
  --dangerous               Skip permission prompts (--dangerously-skip-permissions)
  --rebuild                 Force rebuild of the base, Claude Code, child and run images
                            (--no-cache: also starts the shared package caches empty)
  --update                  Check for a Claude Code update now and build it before launching (only the CLI image)
  --no-update-check         Skip Claude Code version check
  --docker-socket           Mount the host Docker socket into the container
  --aws                     Mount ~/.aws/ read-only into the container
  --git                     Mount ~/.gitconfig read-only into the container
  --ssh                     Mount ~/.ssh/ read-only into the container
  --package-caches          Mount ~/.cache/claude-sandbox/{go-mod,go-build,npm,pip} writable
                            and point GOMODCACHE/GOCACHE/npm_config_cache/PIP_CACHE_DIR at them

Worktree mode (off by default for interactive launches, on for --ralph;
config key 'worktree: true|false' changes the default for both):
  --worktree[=NAME]         Run claude in its own worktree, .claude/worktrees/NAME on branch
                            worktree-NAME (default NAME: the container's instance noun;
                            ralph: "ralph"). An existing NAME is reopened. Outside a git
                            repository the launch proceeds without it. A worktree has its
                            own session history: --resume there lists only conversations
                            started in it (Ctrl+W in the picker shows the others)
  --no-worktree             Run claude in the shared checkout (the interactive default;
                            turns ralph's worktree off)

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
  CLAUDE_SANDBOX_WORKTREE=0|1             Worktree mode off/on (flag > env > config 'worktree' >
                                          default: off interactive, on ralph)

Inside the container, CLAUDE_SANDBOX_PROJECT_DIR names the project root (where
.claude-sandbox/ lives) — a session in a worktree cannot otherwise tell.

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

	// Worktree is the tri-state --worktree/--no-worktree choice (nil = not
	// passed) and WorktreeName the optional "--worktree=NAME" value
	// (CS-LNCH-041..043).
	Worktree     *bool
	WorktreeName string

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

// knownPassthrough is the claude flag allowlist that locates the passthrough
// boundary (CS-LNCH-002). It is matched on the part before any "=" so claude's
// --flag=value spelling works too (CS-LNCH-100); claude validates the rest.
var knownPassthrough = map[string]bool{
	"--resume": true, "--continue": true, "--verbose": true, "--output-format": true,
	"--allowedTools": true, "--disallowedTools": true, "--permission-prompt-tool": true,
	// The kebab-case aliases "claude --help" lists beside allowlisted flags
	// (verified on Claude Code 2.1.277; no other allowlisted flag has one).
	// CS-LNCH-101.
	"--allowed-tools": true, "--disallowed-tools": true,
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
	return scanArgs(args, false)
}

// scanArgs is the launcher grammar; headless is the grammar after
// "claude-sandbox headless", where --help, -h and --version belong to claude
// and start the passthrough (CS-LNCH-058).
func scanArgs(args []string, headless bool) (*launchFlags, error) {
	f := &launchFlags{}
	boolTrue := func() *bool { t := true; return &t }
	i := 0
	for i < len(args) {
		a := args[i]
		switch a {
		case "--help", "-h", "--version":
			if headless {
				f.Passthrough = args[i:]
				return f, nil
			}
			if a == "--version" {
				f.Version = true
			} else {
				f.Help = true
			}
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
		case "--worktree":
			f.Worktree = boolTrue()
			i++
		case "--no-worktree":
			off := false
			f.Worktree = &off
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
			// --model=MODEL is the launcher's --model (CS-LNCH-100): consumed,
			// never passed through, so the launcher's model resolution sees it.
			if strings.HasPrefix(a, "--model=") {
				v, ok := flagValue(a, "--model")
				if !ok {
					return nil, exitErr(2, "Error: --model requires a value")
				}
				if strings.HasPrefix(v, "=") {
					return nil, exitErr(2, "Error: --model: invalid value '%s'", v)
				}
				f.Model = v
				i++
				continue
			}
			// --worktree=NAME: validated here so a bad name fails with exit 2
			// before any docker command runs (CS-LNCH-043).
			if v, ok := flagValue(a, "--worktree"); ok {
				if err := launch.ValidateWorktreeName(v); err != nil {
					return nil, exitErr(2, "Error: --worktree: %v", err)
				}
				f.Worktree, f.WorktreeName = boolTrue(), v
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
		// Match --flag=value by its name (CS-LNCH-100). Launcher flags never
		// reach here: scanArgs consumes them, including their "=" forms.
		name, _, _ := strings.Cut(a, "=")
		if knownPassthrough[name] {
			f.Passthrough = args[i:]
			return f, nil
		}
		return nil, exitErr(2, "Error: unknown flag '%s'", a)
	}
	if a == "init" || a == "init-ralph" || a == "headless" {
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

// resolveProjectDir returns the canonical project directory: PROJECT_DIR when
// set, else the working directory — on both branches the physical
// (symlink-resolved) absolute path (CS-LNCH-006, CS-LNCH-048).
func resolveProjectDir(getenv func(string) string) (string, error) {
	dir, _, err := resolveProjectDirFrom(getenv, io.Discard)
	return dir, err
}

// resolveProjectDirFrom also returns the path as given — the logical working
// directory (os.Getwd honours $PWD, so a shell standing in a symlink reports
// the link) or the absolute PROJECT_DIR — so the launch can show the redirect
// when the two differ (CS-LNCH-048). One repo reached through a symlink used
// to be two projects: the working-directory default kept the logical path
// while PROJECT_DIR was resolved, and the container slug, transcript slug,
// noun pool and fingerprint all hash the path. When EvalSymlinks fails the
// given absolute path is returned for both and a warning goes to errw — the
// one case where the launch is not on the physical path.
func resolveProjectDirFrom(getenv func(string) string, errw io.Writer) (dir, given string, err error) {
	given = getenv("PROJECT_DIR")
	if given == "" {
		if given, err = os.Getwd(); err != nil {
			return "", "", err
		}
	} else {
		abs, aerr := filepath.Abs(given)
		if aerr != nil {
			return "", "", aerr
		}
		if fi, serr := os.Stat(abs); serr != nil || !fi.IsDir() {
			return "", "", fmt.Errorf("PROJECT_DIR %s is not a directory", given)
		}
		given = abs
	}
	resolved, rerr := filepath.EvalSymlinks(given)
	if rerr != nil {
		fmt.Fprintf(errw, "WARNING: could not resolve symlinks in %s (%v); using it as given\n", given, rerr)
		return given, given, nil
	}
	return resolved, given, nil
}

func envTrue(v string) bool { return v == "1" || v == "true" || v == "yes" }

// resolveDangerous is the one dangerous-mode rule, shared by a new container
// and a join (CS-LNCH-038, CS-SESS-064): durable via env var or config, not
// just the flag. A more-local "dangerous: false" overrides an upstream true
// through the ordinary cascade merge before this OR is evaluated.
func resolveDangerous(env *Env, f *launchFlags, cfg *cascade.Config) bool {
	return f.Dangerous || envTrue(env.Getenv("CLAUDE_SANDBOX_DANGEROUS")) || cfg.Dangerous
}

// worktreeChoice is the resolved worktree mode for this launch (CS-LNCH-041).
type worktreeChoice struct {
	// Enabled is the final answer after the tri-state precedence AND the git
	// pre-check: false when requested off, or when the project is not inside
	// a git work tree (StoodDown).
	Enabled bool
	// StoodDown records that the mode resolved on but the project is not a
	// git work tree, so the launch proceeds without it (CS-LNCH-046).
	StoodDown bool
	// Root is the git work tree root ("" when none) — where claude anchors
	// .claude/worktrees/, which is not necessarily the project dir.
	Root string
	// Name is the explicit --worktree=NAME value, "" to default (CS-LNCH-043).
	Name string
}

// resolveWorktree applies CLI > CLAUDE_SANDBOX_WORKTREE > merged config >
// default (CS-LNCH-042), then the git pre-check (CS-LNCH-046). The default is
// OFF for interactive launches and ON for ralph: Claude Code files
// transcripts by working directory, so a worktree has its own empty history
// and `--resume` there lists none of the repo's conversations — an
// interactive launch must be transparent, an unattended ralph run should be
// isolated (CS-LNCH-041/045). The tri-state shape is required rather than
// the OR of CS-LNCH-038: with a true default an OR could never express "off".
//
// A headless launch ignores CLAUDE_SANDBOX_WORKTREE and the config key: only
// an explicit --worktree in its command prefix turns the mode on (CS-LNCH-066).
// An SDK client reads the transcript from the slug of the cwd it spawned in,
// and resumes by session id; a worktree files the transcript under another
// slug, and a fresh noun per spawn would put every resume in a new worktree.
func resolveWorktree(env *Env, projectDir string, f *launchFlags, cfg *cascade.Config, headless bool) worktreeChoice {
	wt := worktreeChoice{Name: f.WorktreeName}
	if headless {
		wt.Enabled = f.Worktree != nil && *f.Worktree
	} else {
		wt.Enabled = launch.ResolveTristate(f.Worktree, env.Getenv("CLAUDE_SANDBOX_WORKTREE"), cfg.Worktree, f.Ralph)
	}
	wt.Root = launch.GitRoot(env.Runner, projectDir)
	if wt.Enabled && wt.Root == "" {
		// claude itself refuses "--worktree requires a git repository", so
		// standing down is the only outcome that launches — however the mode
		// was requested.
		wt.Enabled, wt.StoodDown = false, true
	}
	return wt
}

// nameFor is the worktree name a NEW container gets: the explicit name, else
// the instance noun so container, worktree and branch share one word; ralph
// has no noun and is named "ralph" (CS-LNCH-041/045). Empty when off.
func (wt worktreeChoice) nameFor(instance string, ralph bool) string {
	switch {
	case !wt.Enabled:
		return ""
	case wt.Name != "":
		return wt.Name
	case ralph:
		return "ralph"
	}
	return instance
}

// banner is the one stdout line that makes a worktree visible, or a requested
// one's stand-down (CS-LNCH-041/046). It is "" — nothing printed — when the
// session simply works in the shared checkout: the interactive default must
// not narrate itself.
func (wt worktreeChoice) banner(name string) string {
	switch {
	case wt.StoodDown:
		return "Worktree: off (not a git repository)"
	case !wt.Enabled:
		return ""
	}
	return fmt.Sprintf("Worktree: %s (%s/%s, branch worktree-%s)", name, launch.WorktreeDir, name, name)
}

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
		// Compare the flag name, so --resume=ID is caught too (CS-LNCH-100).
		p := f.Passthrough[0]
		if name, _, _ := strings.Cut(p, "="); name == "--resume" || name == "--continue" {
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
	return launchWith(env, f, rr, version, false)
}

// runHeadless is "claude-sandbox headless" (CS-LNCH-058..067): a launch for an
// SDK client such as Paseo's daemon, which pipes stdin/stdout/stderr, speaks
// stream-json on them and has nobody to answer a prompt.
func runHeadless(env *Env, args []string) error {
	f, err := scanArgs(args, true)
	if err != nil {
		return err
	}
	if err := headlessRejection(f); err != nil {
		return err
	}
	// CS-LNCH-061: a decision would need a person, so there is none.
	f.NewSession = true

	h := *env
	// CS-LNCH-060: stdout is claude's stream-json. Every launcher message —
	// cascade, banners, build output, warnings — goes to stderr.
	h.Out = env.Err
	// CS-LNCH-061: never open /dev/tty, even when the process has a
	// controlling terminal; every question takes its default.
	h.Prompter = &prompt.Fixed{Out: env.Err}
	rr := repoRoot(h.Getenv)
	return launchWith(&h, f, rr, imagebuild.Version(h.Runner, rr), true)
}

// headlessRejection reports the launcher flags a headless launch refuses. It is
// the single source for both runHeadless and headless completion (CS-COMP-025),
// which derives the flags it offers from it.
func headlessRejection(f *launchFlags) error {
	switch {
	case f.Ralph:
		return exitErr(2, "Error: --ralph is not valid with headless")
	case f.Limit != "":
		return exitErr(2, "Error: --limit is not valid with headless")
	case f.Attach:
		return exitErr(2, "Error: --attach is not valid with headless: a headless launch is always a new container")
	case f.Join:
		return exitErr(2, "Error: --join is not valid with headless: a headless launch is always a new container")
	case f.Branch:
		return exitErr(2, "Error: --branch is not valid with headless; pass claude's own --resume/--fork-session after --")
	}
	return nil
}

func newHeadlessCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "headless [launcher flags] [--] [claude args]",
		Short: "Launch claude for an SDK client (Paseo): no TTY, clean stdout, never prompts",
		Long: "Usage:\n  claude-sandbox headless [launcher flags] [--] [claude args]\n\n" +
			"Launch claude in a new sandbox container for an SDK client such as Paseo.\n" +
			"The container has no TTY and no detach keys, every launcher message goes to\n" +
			"stderr, nothing prompts, and everything after -- reaches claude verbatim\n" +
			"(after headless, --version and --help are claude's). Launcher flags are those\n" +
			"of 'claude-sandbox --help', except --ralph, --limit, --attach, --join, --branch.\n\n" +
			"Paseo command: [\"claude-sandbox\", \"headless\", \"--\"]",
		SilenceUsage:       true,
		SilenceErrors:      true,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHeadless(env, args)
		},
		// DisableFlagParsing leaves cobra unaware of the launcher flags here
		// too, so headless completes its own command line (CS-COMP-025/026).
		ValidArgsFunction: completeHeadless,
	}
}

// launchWith runs a launch from scanned flags. headless selects the SDK-client
// container shape (CS-LNCH-059..064); runHeadless has already routed output
// and prompts for it.
func launchWith(env *Env, f *launchFlags, rr, version string, headless bool) error {
	if f.Limit != "" && !f.Ralph {
		return exitErr(2, "Error: --limit is only valid with --ralph")
	}
	if err := validateBranch(f); err != nil {
		return err
	}
	projectDir, givenDir, err := resolveProjectDirFrom(env.Getenv, env.Err)
	if err != nil {
		return err
	}
	if projectDir != givenDir {
		// The redirect is visible (CS-LNCH-048): the container, the slug and
		// the transcript store all use the physical path, not the one typed.
		fmt.Fprintf(env.Out, "Project: %s (resolved from %s)\n", projectDir, givenDir)
	}

	// A linked git worktree (CS-LNCH-070) cascades from its main checkout
	// (CS-CASC-031..033) and gets the common git dir mounted (CS-LNCH-071).
	// Identity — slug, labels, -w, noun pool — stays on projectDir (CS-LNCH-073).
	linked, lwWarn := launch.DetectLinkedWorktree(env.Runner, projectDir)
	if lwWarn != "" {
		fmt.Fprintln(env.Err, lwWarn)
	}
	mainCheckout := ""
	if linked != nil {
		mainCheckout = linked.Main
		fmt.Fprintln(env.Out, linked.Banner(linked.MountsCommonDir(projectDir)))
	}
	chain := paths.Chain(projectDir, mainCheckout)

	// Config + env cascade (CS-CASC, CS-LNCH-024).
	configFiles, err := paths.CollectChain(chain, paths.Config)
	if err != nil {
		return err
	}
	envFiles, err := paths.CollectChain(chain, paths.Env)
	if err != nil {
		return err
	}
	cascade.PrintReportChain(env.Out, chain)
	// Name env keys a more-local file shadows — names only (CS-CASC-021..029).
	cascade.PrintEnvOverrides(env.Out, envFiles, env.lookupEnv)
	cfg, err := cascade.Load(configFiles)
	if err != nil {
		return err
	}
	if err := cfg.Validate(configFiles); err != nil {
		return err
	}
	if len(envFiles) == 0 {
		warnNoEnv(env.Err, projectDir)
	} else {
		// Warn-only lint of every cascade level (CS-CASC-020).
		cascade.LintEnvFiles(env.Err, envFiles)
	}

	// Worktree mode (CS-LNCH-041/042/046), resolved once for every path: a new
	// container names its worktree after itself, a join enters a fresh one,
	// and attach can only report what the running session has.
	wt := resolveWorktree(env, projectDir, f, cfg, headless)

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
		done, aerr := joinExistingSession(env, projectDir, f, cfg, envFiles, decision, wt, linked)
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
	if headless && !f.Update {
		// CS-LNCH-062: off unless asked for; it would add a registry round
		// trip to every SDK probe.
		noUpdate = true
	}

	dangerous := resolveDangerous(env, f, cfg)

	// The instance noun names the container AND its worktree, so it is picked
	// here, before the banner, from the nouns neither running nor already
	// checked out under .claude/worktrees/ (CS-SESS-007, CS-SESS-045).
	instance := newInstance(env, projectDir, f, wt.Root)
	worktree := wt.nameFor(instance, f.Ralph)
	if b := wt.banner(worktree); b != "" {
		fmt.Fprintln(env.Out, b)
	}

	// Images (CS-IMG). Order: base, CLI image, update check (CLI only), child,
	// cap. A Claude Code update never touches the base or the child, and
	// without --update it is built in the background for the next launch
	// (CS-IMG-045).
	imgOpts := imagebuild.Options{
		Runner: env.Runner, Out: env.Out, Err: env.Err,
		RepoRoot: rr, Version: version,
		ForceRebuild: f.Rebuild, NoUpdateCheck: noUpdate, AutoUpdate: f.Update,
		CacheDir: env.cacheDir(), Now: env.Now, Self: env.executable(),
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
		ProjectDir: projectDir, MainCheckout: mainCheckout,
		BaseOnly: baseOnly, DockerfileDir: dfDir, Dockerfile: dfName,
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
	// Build-cache budget (CS-IMG-041..043): never inline. "docker system df"
	// alone measured 6-13.5 s, so a build leaves a detached checker behind and
	// the next launch prints what it found. Read before this launch starts its
	// own checker, so a report is always an earlier build's. CS-LNCH-062:
	// headless neither reads nor starts one; an SDK client has no one to read
	// stderr, and the next interactive launch still gets the report.
	if !headless {
		imagebuild.ConsumeCacheBudget(env.cacheDir(), env.Err)
		if baseRebuilt || cliBuilt || childBuilt || capBuilt {
			startCacheBudgetCheck(env)
		}
	}

	// Launch plan (CS-LNCH). Everything but the per-session picks is settled
	// here, outside the lock.
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
		Instance: instance,
		Worktree: worktree,
		Linked:   linked,
		Version:  version,
		// CS-LNCH-093: recorded on the container for the OOM report.
		MemoryLimitSource: cascade.MemoryLimitSource(configFiles),
		Headless:          headless, LookupEnv: env.lookupEnv,
		Out: env.Out, Err: env.Err,
	}
	// Reserve under the host lock: re-validate the noun, pick the pid class,
	// docker create (CS-SESS-048). The lock is released before the start.
	plan, err := reserveContainer(env, in, wt, f.Ralph)
	if err != nil {
		return err
	}
	return startReserved(env, plan, headless)
}

// cacheBudgetCheckCmd is the hidden subcommand the detached checker runs as
// (CS-IMG-041/042).
const cacheBudgetCheckCmd = "cache-budget-check"

// startCacheBudgetCheck starts "<this binary> cache-budget-check --dir <dir>"
// detached (CS-IMG-041): its own session, stdio on /dev/null, never waited on,
// so it outlives the launcher and cannot print into the session. Advisory:
// every failure is silent.
func startCacheBudgetCheck(env *Env) {
	exe := env.Executable
	if exe == nil {
		exe = os.Executable
	}
	bin, err := exe()
	if err != nil || bin == "" {
		return
	}
	env.Runner.Start(execx.Cmd{
		Name:   bin,
		Args:   []string{cacheBudgetCheckCmd, "--dir", env.cacheDir()},
		Detach: true,
	})
}

func newCacheBudgetCheckCmd(env *Env) *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:           cacheBudgetCheckCmd,
		Short:         "Check the BuildKit cache budget into a result file (internal)",
		Hidden:        true,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if dir == "" {
				dir = env.cacheDir()
			}
			o := imagebuild.Options{Runner: env.Runner, Out: env.Out, Err: env.Err}
			err := imagebuild.RunCacheBudgetCheck(o, dir, env.Now)
			switch {
			case errors.Is(err, imagebuild.ErrCacheBudgetBusy):
				// CS-IMG-042: another checker is producing the result.
				fmt.Fprintf(env.Err, "%s: skipped, %v\n", cacheBudgetCheckCmd, err)
				return nil
			case err != nil:
				return exitErr(1, "%s: %v", cacheBudgetCheckCmd, err)
			}
			fmt.Fprintf(env.Err, "%s: wrote %s\n", cacheBudgetCheckCmd, filepath.Join(dir, imagebuild.CacheBudgetFile))
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "directory for the result and lock files (default ~/.cache/claude-sandbox)")
	return cmd
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

// executable is the launcher's own binary via the Env.Executable seam; ""
// when it cannot be found.
func (e *Env) executable() string {
	exe := e.Executable
	if exe == nil {
		exe = os.Executable
	}
	bin, err := exe()
	if err != nil {
		return ""
	}
	return bin
}

// newCLIPrefetchCmd is the hidden "claude-sandbox cli-prefetch <version>"
// (CS-IMG-045..047): the background CLI image build a launch starts detached
// when a newer Claude Code exists. Its stdout and stderr are the prefetch
// log.
func newCLIPrefetchCmd(env *Env) *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:           imagebuild.PrefetchSubcommand + " [--dir DIR] <version>",
		Short:         "Build the Claude Code CLI image in the background (started by a launch)",
		Hidden:        true,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return exitErr(2, "Usage: claude-sandbox %s [--dir DIR] <version>", imagebuild.PrefetchSubcommand)
			}
			if dir == "" {
				dir = env.cacheDir()
			}
			o := imagebuild.Options{
				Runner: env.Runner, Out: env.Out, Err: env.Err,
				RepoRoot: repoRoot(env.Getenv), CacheDir: dir, Now: env.Now,
			}
			if err := imagebuild.Prefetch(o, args[0]); err != nil {
				return exitErr(1, "Error: %v", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "directory for the version cache, lock, log and status (default ~/.cache/claude-sandbox)")
	return cmd
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
	// The launcher hands this over in worktree mode (CS-LNCH-045); the loop
	// forwards it to every iteration (CS-RLP-019) and generates the
	// where-you-are prompt block (CS-RLP-022). Empty = shared checkout.
	fl.StringVar(&o.Worktree, "worktree", "", "Run every iteration in the named Claude Code worktree (.claude/worktrees/NAME, branch worktree-NAME); omit for the shared checkout")
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

// warnNoEnv reports an env cascade with no .claude-sandbox/env at any level.
// When the project's own env.example exists (what init seeds) it is one Note
// line, since the state is the one init produced (CS-LNCH-056); otherwise the
// full warning (CS-LNCH-025). Only the project level counts: a stray
// ~/.claude-sandbox/env.example must not soften the warning for every project
// under $HOME. env.example itself is never read (CS-CASC-030).
func warnNoEnv(w io.Writer, projectDir string) {
	if ex := filepath.Join(paths.SandboxDir(projectDir), paths.EnvExampleName); isRegularFile(ex) {
		fmt.Fprintf(w, "Note: no .claude-sandbox/env in the cascade; %s is a template and is not read.\n", ex)
		return
	}
	fmt.Fprintf(w, "WARNING: env file not found (.claude-sandbox/env) in %s or any parent\n\n", projectDir)
	fmt.Fprintf(w, "This file provides environment variables needed by Claude Code\n")
	fmt.Fprintf(w, "(e.g. DISCORD_WEBHOOK_URL for MCP server notifications).\n\n")
	fmt.Fprintf(w, "Create .claude-sandbox/env in a parent (workspace) directory for values shared\n")
	fmt.Fprintf(w, "by every project below it, or in this project for a project-only override.\n")
	fmt.Fprintf(w, "  claude-sandbox init   # seeds .claude-sandbox/env.example to copy from\n")
}

func isRegularFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}
