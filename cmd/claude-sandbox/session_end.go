package main

// End the session when its terminal hangs up (CS-LNCH-206..210, CS-LNCH-212;
// plan detached-sandboxes 01 F2 as amended by 02..04): the launcher's side —
// whether a client's hang-up ends its container, the classification, the
// start of the detached session-end helper — and the helper's hidden
// subcommand, the ended-session notice claim and the attach/join check of a
// container the helper already ended.

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/clients"
	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessionend"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

// hupIgnored reports whether the launcher inherited SIGHUP as ignored
// (nohup): docker inherits that too and cannot die of a hang-up. A seam.
var hupIgnored = func() bool { return signal.Ignored(syscall.SIGHUP) }

// isHangup classifies a session's end (CS-LNCH-207): the first of SIGHUP and
// SIGTERM among the signals caught, in order, decides; with neither, docker
// dying of SIGHUP (never the bare code 129, which docker passes through from
// the container) is a hang-up.
func isHangup(sigs []os.Signal, child syscall.Signal) bool {
	for _, s := range sigs {
		switch s {
		case syscall.SIGHUP:
			return true
		case syscall.SIGTERM:
			return false
		}
	}
	return child == syscall.SIGHUP && !hupIgnored()
}

// goos is Env.GOOS with runtime.GOOS as its default.
func (e *Env) goos() string {
	if e.GOOS != "" {
		return e.GOOS
	}
	return runtime.GOOS
}

// hangupApplies is CS-LNCH-210's runtime stand-downs: only a Linux host
// launcher sees the leases and processes the helper needs.
func hangupApplies(env *Env) bool {
	return env.goos() == "linux" && !hostdirs.InSandbox(env.Getenv)
}

// endOnHangupFor decides whether an attach's hang-up ends container s
// (CS-LNCH-206): the attaching launcher's own env when set, else the
// container's label, else (a container from before the label) the cascade
// and the default. Ralph and headless containers never.
func endOnHangupFor(env *Env, s sessions.Session, cfg *cascade.Config) bool {
	if s.Mode == sessions.ModeRalph || s.Mode == launch.ModeHeadless || !hangupApplies(env) {
		return false
	}
	if on, set := launch.EndOnHangupEnvSet(env.Getenv); set {
		return on
	}
	switch s.OnHangup {
	case launch.OnHangupEnd:
		return true
	case launch.OnHangupKeep:
		return false
	}
	return launch.EndOnHangupWanted(env.Getenv, cfg)
}

// sessionEndArgs is the helper's argv after the binary.
func sessionEndArgs(env *Env, m tmuxpane.Mark, at time.Time) []string {
	_, _, _, home := hostIdentity(env.Getenv)
	configDir := env.Getenv("CLAUDE_CONFIG_DIR")
	if configDir == "" && home != "" {
		configDir = filepath.Join(home, ".claude")
	}
	return []string{sessionend.Subcommand,
		"--state-dir", env.stateDir(), "--cache-dir", env.cacheDir(),
		"--home", home, "--config-dir", configDir,
		"--hangup-at", strconv.FormatInt(at.UnixMilli(), 10),
		"--mark", m.JSON()}
}

// startSessionEnd is the launcher's part of a hang-up (CS-LNCH-207): release
// this client's lease, so the helper does not count it, then start the
// helper detached. Fast — a few syscalls and one fork and exec — and silent:
// a helper that cannot start is one line in <cache>/session-end.log, and the
// container is kept.
func startSessionEnd(env *Env, o sessionOpts) {
	if !o.endOnHangup || o.mark == nil {
		return
	}
	o.lease.Release()
	exe := env.executable()
	err := fmt.Errorf("the launcher's own binary cannot be found")
	if exe != "" {
		_, err = env.Runner.Start(execx.Cmd{Name: exe, Args: sessionEndArgs(env, o.mark.next, env.now()), Dir: "/", Detach: true})
	}
	if err != nil {
		if f := openHelperLog(filepath.Join(env.cacheDir(), sessionend.LogFile)); f != nil {
			fmt.Fprintf(f, "%s %s: cannot start the session-end helper: %v\n", env.now().UTC().Format(time.RFC3339), o.mark.next.Container, err)
			f.Close()
		}
	}
}

// newSessionEndCmd is the hidden helper (CS-LNCH-208).
func newSessionEndCmd(env *Env) *cobra.Command {
	var stateDir, cacheDir, home, configDir, markJSON string
	var at int64
	cmd := &cobra.Command{
		Use:          sessionend.Subcommand,
		Short:        "End a hung-up session after its turn (spawned by the launcher)",
		Hidden:       true,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			m, ok := tmuxpane.ParseMark(markJSON)
			if !ok || stateDir == "" || at <= 0 {
				return exitErr(2, "Error: %s needs --state-dir, --hangup-at and --mark", sessionend.Subcommand)
			}
			// Its own SIGTERM or SIGHUP means keep (03 § 1.3); caught from
			// the start, so a stop of the unit it inherited never kills it
			// half-way.
			sigs := make(chan os.Signal, 4)
			signal.Notify(sigs, syscall.SIGTERM, syscall.SIGHUP)
			defer signal.Stop(sigs)
			var log *os.File
			if cacheDir != "" {
				log = openHelperLog(filepath.Join(cacheDir, sessionend.LogFile))
			}
			o := sessionend.Options{
				Mark: m, HangupAt: time.UnixMilli(at), StateRoot: stateDir,
				Home: home, ConfigDir: configDir,
				ProcRoot: env.ProcRoot, MachineIDPath: env.MachineIDPath,
				Runner: env.Runner, Signals: sigs, UID: -1, GOOS: env.goos(),
				InSandbox: hostdirs.InSandbox(env.Getenv),
			}
			if env.Now != nil {
				o.Clock = envClock{env}
			}
			if log != nil {
				o.Log = log
				defer log.Close()
			}
			sessionend.Run(o)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&stateDir, "state-dir", "", "state root")
	f.StringVar(&cacheDir, "cache-dir", "", "cache root (diagnostics log)")
	f.StringVar(&home, "home", "", "the invoking user's home")
	f.StringVar(&configDir, "config-dir", "", "the launcher's Claude config dir")
	f.Int64Var(&at, "hangup-at", 0, "when the launcher saw the hang-up (unix ms)")
	f.StringVar(&markJSON, "mark", "", "the session's pane mark")
	return cmd
}

// envClock is the helper's clock over Env.Now (tests): After advances
// nothing; the test's Now does.
type envClock struct{ env *Env }

func (c envClock) Now() time.Time                         { return c.env.now() }
func (c envClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// claimEndedNotices prints this project's ended-session notices once
// (CS-LNCH-212). Host only; a failure prints nothing.
func claimEndedNotices(env *Env, projectDir string) {
	if hostdirs.InSandbox(env.Getenv) {
		return
	}
	sessionend.ClaimNotices(sessionend.ClaimOptions{StateRoot: env.stateDir(), Project: projectDir, Out: env.Err, Now: env.now()})
}

// endedBeforeAttach is the attach and join check of CS-LNCH-212: one bounded
// inspect of the chosen container right after its lease. Gone — the helper
// (or anything else) ended it while the person read the menu — prints the
// project's notices and "'<noun>' is no longer running." and exits 1 instead
// of docker's "No such container". Any other answer goes on as before.
func endedBeforeAttach(env *Env, projectDir string, s sessions.Session, lease *clients.Lease) error {
	target := s.Name
	if fullID.MatchString(s.ID) {
		target = s.ID
	}
	c, err := sessions.InspectContainer(tmuxpane.BoundedRunner{R: env.Runner, Timeout: tmuxpane.ProbeTimeout}, target)
	if !sessionend.Gone(c, err) {
		return nil
	}
	claimEndedNotices(env, projectDir)
	lease.Release()
	return exitErr(1, "'%s' is no longer running.", sessionLabel(s))
}
