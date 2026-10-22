package main

// The write-only host clipboard bridge, CLI side (CS-LNCH-194..203): the
// display target resolved from the launcher's shell, the helper spawned
// after the create, the restart and re-pointing on attach and join, the
// attach note, the JetBrains note, the hidden helper command and the
// in-container wl-copy shim (argv0).

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/clipboard"
	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

// tmuxEnvTimeout bounds the tmux session-environment read (CS-LNCH-203).
const tmuxEnvTimeout = time.Second

// clipboardPingTimeout bounds the attach/join health check (CS-LNCH-197).
const clipboardPingTimeout = 500 * time.Millisecond

func (e *Env) clipboardLookPath(name string) (string, error) {
	if e.ClipboardLookPath != nil {
		return e.ClipboardLookPath(name)
	}
	if testing.Testing() {
		panic("a test resolved the host clipboard tools; set Env.ClipboardLookPath")
	}
	return exec.LookPath(name)
}

func (e *Env) hostname() string {
	f := e.Hostname
	if f == nil {
		if testing.Testing() {
			panic("a test read the real hostname; set Env.Hostname")
		}
		f = os.Hostname
	}
	h, _ := f()
	return h
}

// resolveClipboard resolves the display target from the launcher's shell
// (CS-LNCH-203): the override, the shell, and under tmux the pane's session
// environment through one bounded show-environment.
func resolveClipboard(env *Env) clipboard.Resolution {
	_, _, _, home := hostIdentity(env.Getenv)
	in := clipboard.ResolveInput{
		Getenv:   env.Getenv,
		Hostname: env.hostname(),
		Home:     home,
		LookPath: env.clipboardLookPath,
		Files:    clipboard.FileChecks{UID: os.Getuid()},
	}
	if pane := env.Getenv("TMUX_PANE"); env.Getenv("TMUX") != "" && pane != "" {
		out, err := execx.Bounded(env.Runner, tmuxEnvTimeout, execx.Cmd{
			Name: "tmux", Args: []string{"show-environment", "-t", pane}})
		if err == nil {
			in.Tmux = clipboard.ParseTmuxEnv(out)
		}
	}
	return clipboard.Resolve(in)
}

// clipboardTargetPath is the target file of a shadow directory.
func clipboardTargetPath(shadow string) string {
	return filepath.Join(shadow, clipboard.TargetFile)
}

// writeClipboardTarget applies never-downgrade (CS-LNCH-203) and writes the
// target when it should. It reports the effective target afterwards.
func writeClipboardTarget(env *Env, shadow string, res clipboard.Resolution, by string) *clipboard.Target {
	path := clipboardTargetPath(shadow)
	if res.ShouldWrite() {
		t := res.TargetToWrite(by, env.now().UnixMilli())
		if err := clipboard.WriteTarget(path, t); err != nil {
			fmt.Fprintf(env.Err, "WARNING: hostClipboard: cannot write the display target %s: %v\n", path, err)
		}
	}
	t, err := clipboard.ReadTarget(path, env.hostname(), clipboard.FileChecks{UID: os.Getuid()})
	if err != nil {
		return nil
	}
	return t
}

// spawnClipboardHelper starts "claude-sandbox clipboard-bridge" detached
// (Cmd.Detach: its own session, outliving the launcher).
func spawnClipboardHelper(env *Env, dir, target, name, id, outer string) (execx.Process, error) {
	args := []string{clipboard.HelperSubcommand, "--dir", dir, "--container", name,
		"--log", filepath.Join(env.cacheDir(), clipboard.LogFile)}
	if id != "" {
		args = append(args, "--id", id)
	}
	if outer != "" {
		args = append(args, "--outer", outer)
	} else {
		args = append(args, "--target", target)
	}
	return env.Runner.Start(execx.Cmd{Name: env.executable(), Args: args, Dir: "/", Detach: true})
}

// startClipboardBridge runs right after a successful docker create
// (CS-LNCH-195): the target first (host launches only; a nested one writes
// none, CS-LNCH-200), then the helper, so its first read finds it.
func startClipboardBridge(env *Env, plan *launch.Plan, res *clipboard.Resolution) {
	if plan.ClipboardDir == "" {
		return
	}
	shadow := filepath.Dir(plan.ClipboardDir)
	if plan.ClipboardOuter == "" && res != nil {
		writeClipboardTarget(env, shadow, *res, clipboard.ByCreate)
	}
	proc, err := spawnClipboardHelper(env, plan.ClipboardDir, clipboardTargetPath(shadow), plan.ContainerName, plan.ContainerID, plan.ClipboardOuter)
	if err != nil {
		fmt.Fprintf(env.Err, "WARNING: hostClipboard: cannot start the clipboard bridge (%v); copies go over OSC 52 only until the next attach restarts it.\n", err)
		return
	}
	plan.ClipboardHelper = proc
}

// stopClipboardHelper ends the helper of a removed reservation.
func stopClipboardHelper(plan *launch.Plan) {
	if plan.ClipboardHelper != nil {
		plan.ClipboardHelper.Signal(syscall.SIGTERM)
	}
}

// refreshClipboardBridge is the attach and join step (CS-LNCH-197): resolve
// this shell's target and write it (never downgrading), then ping the helper
// and restart it when it is dead. Returns the effective target (nil: none or
// not applicable) and whether the container has the bridge.
func refreshClipboardBridge(env *Env, s sessions.Session, by string) (*clipboard.Target, bool) {
	if s.Clipboard != launch.ClipboardLabelValue(true) || s.ShadowDir == "" {
		return nil, false
	}
	dir := filepath.Join(s.ShadowDir, clipboard.DirName)
	nested := hostdirs.InSandbox(env.Getenv)
	if nested {
		// The label's path is a host path; here it may mean nothing, or
		// something else. Only a real directory of ours is touched.
		fi, err := os.Lstat(dir)
		st, ok := statOf(fi)
		if err != nil || !fi.IsDir() || !ok || int(st.Uid) != os.Getuid() {
			return nil, true
		}
	}
	var eff *clipboard.Target
	outer := ""
	if nested {
		outer = env.Getenv(clipboard.EnvSocket)
	} else {
		eff = writeClipboardTarget(env, s.ShadowDir, resolveClipboard(env), by)
	}
	if clipboard.Ping(filepath.Join(dir, clipboard.SocketName), clipboardPingTimeout) {
		return eff, true
	}
	if nested && outer == "" {
		return eff, true
	}
	if _, err := spawnClipboardHelper(env, dir, clipboardTargetPath(s.ShadowDir), s.Name, s.ID, outer); err == nil {
		fmt.Fprintf(env.Out, "Clipboard bridge restarted for '%s'.\n", sessionLabel(s))
	}
	return eff, true
}

func statOf(fi os.FileInfo) (*syscall.Stat_t, bool) {
	if fi == nil {
		return nil, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return st, ok
}

// clipboardAttachNote is printed before an attach when the container was
// started without the bridge and this launcher wants it (CS-LNCH-197). The
// command is built as tmuxpane.ResumeCommand builds one, or its picker
// variant; silent for a container that predates the label.
func clipboardAttachNote(env *Env, s sessions.Session, cfg *cascade.Config) {
	if s.Clipboard != launch.ClipboardLabelValue(false) || !launch.ClipboardWanted(env.Getenv, cfg, false, false) {
		return
	}
	cd := s.ConfigDirEnv
	m := tmuxpane.Mark{Project: s.Project, ConfigDirEnv: &cd, Worktree: s.Worktree, Conversation: s.Resume, Instance: s.Instance}
	head := fmt.Sprintf("Note: '%s' was started without the clipboard bridge, so its copies stay on OSC 52. To get it, end that session, then ", sessionLabel(s))
	switch {
	case !tmuxpane.PrintableMark(m):
		fmt.Fprintln(env.Err, head+"relaunch it with --resume.")
	case s.Resume != "" && tmuxpane.ResumeCommand(m) != "":
		fmt.Fprintf(env.Err, "%srelaunch the conversation it started with: %s. If you have since switched conversations in it, use the same command without the id and pick the conversation in claude's resume picker.\n",
			head, tmuxpane.ResumeCommand(m))
	default:
		fmt.Fprintf(env.Err, "%srelaunch with: %s and pick the conversation in claude's resume picker.\n", head, tmuxpane.ResumePickerCommand(m))
	}
}

// jetbrainsTerminal is the terminal label of JetBrains' JediTerm.
const jetbrainsTerminal = "TERMINAL_EMULATOR=JetBrains-JediTerm"

// jetbrainsSilencers are the env-file settings that give the terminal its
// own selection back, which silence the note (CS-LNCH-202).
var jetbrainsSilencers = []string{"CLAUDE_CODE_DISABLE_MOUSE", "CLAUDE_CODE_DISABLE_MOUSE_CLICKS", "CLAUDE_CODE_DISABLE_ALTERNATE_SCREEN"}

// jetbrainsNote is the JetBrains note (CS-LNCH-202): a new interactive
// launch or an attach from JediTerm, with the bridge wanted and applied, and
// no usable display target after it — copies will not reach the clipboard.
// Once per project, through a marker in the cache root.
func jetbrainsNote(env *Env, projectDir string, snap []cascade.EnvFile, eff *clipboard.Target) {
	if eff.Usable() {
		return
	}
	term := launch.ResolveTerminal(env.lookupEnv, snap, env.lookupEnv)
	if term.Label(false) != jetbrainsTerminal {
		return
	}
	for _, k := range jetbrainsSilencers {
		if v, set, _, _ := cascade.EnvFilesValue(snap, k, env.lookupEnv); set && v != "" && v != "0" && !strings.EqualFold(v, "false") {
			return
		}
	}
	if v, set, _, _ := cascade.EnvFilesValue(snap, "CLAUDE_CODE_NO_FLICKER", env.lookupEnv); set && v == "0" {
		return
	}
	sum := sha256.Sum256([]byte(projectDir))
	dir := filepath.Join(env.cacheDir(), "notes")
	marker := filepath.Join(dir, "jediterm-copy-ii-"+hex.EncodeToString(sum[:])[:16])
	if _, err := os.Lstat(marker); err == nil {
		return
	}
	fmt.Fprintln(env.Err, "Note: this shell has no display the clipboard bridge can use (over SSH only a forwarded X display counts; see "+
		clipboard.EnvDisplay+" for remote desktops), and JetBrains terminals ignore OSC 52, so copies will not reach your clipboard. "+
		"Reconnect with ssh -X and attach again. Shown once for this project.")
	if os.MkdirAll(dir, 0o700) == nil {
		if f, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600); err == nil {
			f.Close()
		}
	}
}

// newClipboardBridgeCmd is the hidden host helper (CS-LNCH-198).
func newClipboardBridgeCmd(env *Env) *cobra.Command {
	var dir, target, name, id, outer, logPath string
	cmd := &cobra.Command{
		Use:          clipboard.HelperSubcommand,
		Short:        "Serve one container's write-only clipboard bridge (spawned by the launcher)",
		Hidden:       true,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if dir == "" || name == "" || (target == "" && outer == "") {
				return exitErr(2, "Error: clipboard-bridge needs --dir, --container and --target or --outer")
			}
			log := openHelperLog(logPath)
			if log != nil {
				defer log.Close()
			}
			host, _ := os.Hostname()
			home, _ := os.UserHomeDir()
			h := &clipboard.Helper{
				Dir: dir, TargetPath: target, Container: name, ID: id, Outer: outer,
				Runner: env.Runner, UID: os.Getuid(), Hostname: host,
				Home: home, Path: os.Getenv("PATH"),
			}
			if log != nil {
				h.Log = log
			}
			lock, err := h.Listen()
			if errors.Is(err, clipboard.ErrHeld) {
				return nil
			}
			if err != nil {
				if log != nil {
					fmt.Fprintf(log, "%s %s: cannot serve: %v\n", time.Now().UTC().Format(time.RFC3339), name, err)
				}
				return exitErr(1, "")
			}
			defer lock.Close()
			go h.Serve()
			why := h.Watch()
			h.Stop()
			if log != nil {
				fmt.Fprintf(log, "%s %s: exit (%s)\n", time.Now().UTC().Format(time.RFC3339), name, why)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&dir, "dir", "", "bridge directory")
	f.StringVar(&target, "target", "", "display target file")
	f.StringVar(&name, "container", "", "container name")
	f.StringVar(&id, "id", "", "container id")
	f.StringVar(&outer, "outer", "", "outer bridge socket (nested)")
	f.StringVar(&logPath, "log", "", "log file")
	return cmd
}

// openHelperLog opens the helper's content-free log (0600, O_NOFOLLOW,
// emptied past 64 KiB); nil when it cannot.
func openHelperLog(path string) *os.File {
	if path == "" {
		return nil
	}
	os.MkdirAll(filepath.Dir(path), 0o700)
	flags := os.O_WRONLY | os.O_CREATE | os.O_APPEND | syscall.O_NOFOLLOW
	if fi, err := os.Lstat(path); err == nil && fi.Mode().IsRegular() && fi.Size() > tmuxSaveLogMax {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil
	}
	return f
}

// runWlCopy is the in-container shim (argv0 wl-copy, CS-LNCH-199).
func runWlCopy(args []string, stdin io.Reader) int {
	s := &clipboard.Shim{Args: args, Stdin: stdin, Stderr: os.Stderr, Getenv: os.Getenv}
	return s.Run()
}
