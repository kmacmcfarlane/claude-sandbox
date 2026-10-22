package launch

// The write-only host clipboard bridge, launcher side (CS-LNCH-194..201;
// internal/clipboard holds the protocol, the helper and the shim).
//
// Applied, a container gets the bridge directory <shadow>/clipboard mounted
// read-only at clipboard.ContainerDir (--mount, so a missing source is an
// error rather than a root-owned path docker makes) and the label
// claude-sandbox.clipboard=bridge. It gets NO -e: the dummy WAYLAND_DISPLAY
// and CLAUDE_SANDBOX_CLIPBOARD are set by pidslot on the claude process only
// (primary and joins), when it finds that mount (pidslot.ClipboardEnv), so a
// docker exec shell and the entrypoint never see them. claude's own children
// (Bash tool commands, MCP servers) still inherit them.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/clipboard"
	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
)

// ClipboardWanted resolves the key (CS-LNCH-194, operator answer 135):
// CLAUDE_SANDBOX_HOST_CLIPBOARD (1/true/yes on, 0/false/no off) >
// hostClipboard > ON. Headless and ralph never want it, whatever the key:
// an SDK stream and an unattended loop have no TUI selection.
func ClipboardWanted(getenv func(string) string, cfg *cascade.Config, headless, ralph bool) bool {
	if headless || ralph {
		return false
	}
	var key *bool
	if cfg != nil {
		key = cfg.HostClipboard
	}
	return ResolveTristate(nil, getenv(clipboard.EnvKey), key, true)
}

// ClipboardExplicitlyOff reports an explicit off (env or key), which
// silences the JetBrains note's case (i) (CS-LNCH-202).
func ClipboardExplicitlyOff(getenv func(string) string, cfg *cascade.Config) bool {
	if v := strings.TrimSpace(getenv(clipboard.EnvKey)); v != "" {
		return !envTruthy(v)
	}
	return cfg != nil && cfg.HostClipboard != nil && !*cfg.HostClipboard
}

func envTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// ClipboardBanner is the one line an applied bridge prints (CS-LNCH-195):
// the bridge usually arrives as a default, and it changes what a session
// can do on the host.
func ClipboardBanner(res *clipboard.Resolution) string {
	target := "none: OSC 52 only"
	if res != nil && res.Target.Usable() {
		target = res.Target.Describe()
	} else if res != nil && res.Hint != "" {
		target += " (" + res.Hint + ")"
	}
	return "Host clipboard: write-only bridge (target: " + target + "; sessions can overwrite your clipboard, never read it)."
}

// ClipboardLabelValue is the clipboard label's value.
func ClipboardLabelValue(applied bool) string {
	if applied {
		return "bridge"
	}
	return "none"
}

// assembleClipboard applies the bridge, or stands it down (CS-LNCH-196),
// and reports whether it applied. A stand-down warns once (a Note nested or
// on darwin; nothing on a restore resume) and the launch goes on exactly as
// with the key off.
func (in *Inputs) assembleClipboard(p *Plan) bool {
	if !in.ClipboardWanted {
		return false
	}
	nested := hostdirs.InSandbox(in.getenv)
	standDown := func(note bool, format string, a ...any) bool {
		if in.ClipboardQuiet || in.Err == nil {
			return false
		}
		prefix := "WARNING: hostClipboard: "
		if note {
			prefix = "Note: hostClipboard: "
		}
		fmt.Fprintf(in.Err, prefix+format+"; this session copies over OSC 52 only.\n", a...)
		return false
	}
	goos := runtime.GOOS
	if in.GOOS != "" {
		goos = in.GOOS
	}
	if goos == "darwin" {
		return standDown(true, "the clipboard bridge is not supported on macOS yet")
	}
	for _, k := range []string{"WAYLAND_DISPLAY", clipboard.EnvSocket} {
		if in.envFilesDefine(k) {
			return standDown(false, "an env file defines %s, which the bridge would have to override", k)
		}
	}
	if underAnyMount(p.Volumes, clipboard.ContainerDir) || coversPath(p.Volumes, clipboard.ContainerDir) {
		return standDown(false, "a cascade mount covers %s", clipboard.ContainerDir)
	}
	outer := ""
	if nested {
		outer = in.getenv(clipboard.EnvSocket)
		if outer == "" {
			return standDown(true, "this launcher runs inside a sandbox with no clipboard bridge of its own")
		}
	}
	shadow, err := in.shadowDirFor()
	if err != nil {
		return standDown(false, "no shadow directory (%v)", err)
	}
	if !nested {
		// M3: the target file is safe only while no container can reach it.
		real := shadow
		if r, err := filepath.EvalSymlinks(shadow); err == nil {
			real = r
		}
		if underAnyHostMount(p.Volumes, real) || underAnyHostMount(p.Volumes, shadow) {
			return standDown(false, "the shadow directory %s lies under a path mounted into the container (TMPDIR)", shadow)
		}
	}
	dir := filepath.Join(shadow, clipboard.DirName)
	if sock := filepath.Join(dir, clipboard.SocketName); len(sock) > clipboard.MaxSocketPath {
		return standDown(false, "the socket path %s is longer than %d bytes", sock, clipboard.MaxSocketPath)
	}
	if err := mkOwnedDir(dir, in.getuid()); err != nil {
		return standDown(false, "cannot create %s as you (%v)", dir, err)
	}
	if strings.ContainsAny(dir, ",=") {
		// --mount splits on commas.
		return standDown(false, "the bridge directory %s holds a comma or '='", dir)
	}
	p.ClipboardDir = dir
	p.ClipboardOuter = outer
	if in.Out != nil {
		fmt.Fprintln(in.Out, ClipboardBanner(in.ClipboardTarget))
	}
	return true
}

func (in *Inputs) getuid() int {
	if in.Getuid != nil {
		return in.Getuid()
	}
	return os.Getuid()
}

// mkOwnedDir makes dir 0700, or accepts an existing real directory of uid.
func mkOwnedDir(dir string, uid int) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("not a directory")
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != uid {
		return fmt.Errorf("not owned by you")
	}
	return os.Chmod(dir, 0o700)
}

// coversPath reports whether a -v spec's container side lies at or below
// path (a mount beneath the bridge target would also collide).
func coversPath(volumes []string, path string) bool {
	path = filepath.Clean(path)
	for _, v := range volumes {
		parts := strings.Split(v, ":")
		if len(parts) < 2 {
			continue
		}
		if dst := filepath.Clean(parts[1]); strings.HasPrefix(dst, path+"/") {
			return true
		}
	}
	return false
}

// underAnyHostMount reports whether path lies at or below the HOST side of
// any -v spec.
func underAnyHostMount(volumes []string, path string) bool {
	path = filepath.Clean(path)
	for _, v := range volumes {
		src, _, ok := strings.Cut(v, ":")
		if !ok {
			continue
		}
		src = filepath.Clean(src)
		if path == src || strings.HasPrefix(path, src+"/") || src == "/" {
			return true
		}
	}
	return false
}

// ClipboardMountArg is the --mount value of an applied bridge.
func ClipboardMountArg(dir string) string {
	return "type=bind,source=" + dir + ",target=" + clipboard.ContainerDir + ",readonly"
}
