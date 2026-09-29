package main

// "claude-sandbox tmux save <state-file>" (CS-TMUX-030..040, plan
// sandbox-reboot-restore F3): the tmux-resurrect post-save-layout hook,
//
//	set -g @resurrect-hook-post-save-layout 'claude-sandbox tmux save'
//
// The work is internal/tmuxpane's Save; this file wires the Env seams in and
// keeps the hook silent: it never writes stdout or stderr (a manual save runs
// it through run-shell, which would show any output) and always exits 0.
// Problems go to <cache root>/tmux-save.log. `tmux restore` is F4's.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

// tmuxSaveLog is the hook's log, in the cache root (CS-TMUX-030).
const tmuxSaveLog = "tmux-save.log"

// tmuxSaveLogMax is the size past which the log is emptied before a write.
const tmuxSaveLogMax = 64 << 10

func newTmuxCmd(env *Env) *cobra.Command {
	t := &cobra.Command{
		Use:           "tmux",
		Short:         "tmux-resurrect integration: the save hook (host only)",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return exitErr(2, "Error: unknown tmux command %q (save)", args[0])
			}
			fmt.Fprintln(env.Out, cmd.UsageString())
			return exitErr(2, "Error: tmux needs a command: save")
		},
	}
	t.AddCommand(&cobra.Command{
		Use:   "save STATE-FILE",
		Short: "tmux-resurrect post-save-layout hook: record each sandbox pane's conversation beside the save",
		Long: "Run by tmux-resurrect after it writes a save, with the state file's path:\n" +
			"  set -g @resurrect-hook-post-save-layout 'claude-sandbox tmux save'\n" +
			"Never prints and always exits 0; problems go to ~/.cache/claude-sandbox/" + tmuxSaveLog + ".",
		SilenceUsage:  true,
		SilenceErrors: true,
		// Any argument is the state file's path, never a flag: an error
		// message would reach the operator's screen on a manual save.
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			runTmuxSave(env, args)
			return nil
		},
	})
	return t
}

// runTmuxSave is the hook (CS-TMUX-030): every outcome is exit 0 and silent.
func runTmuxSave(env *Env, args []string) {
	if hostdirs.InSandbox(env.Getenv) {
		return // a sandbox has no tmux socket, and its cache is not the host's
	}
	var logged []string
	logf := func(format string, a ...any) { logged = append(logged, fmt.Sprintf(format, a...)) }
	defer func() {
		if r := recover(); r != nil {
			logf("panic: %v", r)
		}
		writeTmuxSaveLog(env, logged)
	}()
	if len(args) != 1 {
		logf("expected one argument (the state file), got %d", len(args))
		return
	}
	_, _, _, home := hostIdentity(env.Getenv)
	if _, err := tmuxpane.Save(args[0], tmuxpane.SaveOptions{
		Runner:       env.Runner,
		Now:          env.now,
		Home:         home,
		ConfigDirEnv: env.Getenv("CLAUDE_CONFIG_DIR"),
		Logf:         logf,
	}); err != nil {
		logf("%v", err)
	}
}

// writeTmuxSaveLog appends the run's problems to the hook's log, 0600, never
// through a symlink, emptying it first once it has grown past 64 KiB. A
// failure to log is dropped: the hook must stay silent.
func writeTmuxSaveLog(env *Env, lines []string) {
	if len(lines) == 0 {
		return
	}
	dir := env.cacheDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	path := filepath.Join(dir, tmuxSaveLog)
	flags := os.O_WRONLY | os.O_CREATE | os.O_APPEND | syscall.O_NOFOLLOW
	if fi, err := os.Lstat(path); err == nil && fi.Mode().IsRegular() && fi.Size() > tmuxSaveLogMax {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return
	}
	stamp := env.now().UTC().Format("2006-01-02T15:04:05Z")
	var b strings.Builder
	for _, l := range lines {
		fmt.Fprintf(&b, "%s tmux save: %s\n", stamp, strings.ReplaceAll(l, "\n", " "))
	}
	f.WriteString(b.String())
}
