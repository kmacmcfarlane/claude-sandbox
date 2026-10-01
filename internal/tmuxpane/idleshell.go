package tmuxpane

// The guard before keys are typed into a pane that claude-sandbox did not
// start (CS-TMUX-069; F4d review round 1) — --all's armed panes and
// --rearm's resurrected ones (CS-TMUX-067) alike. A pane may be typed into
// only when it is provably idle at its own shell; anything else is marked
// only.

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
)

// ProcOptions are the seams of the foreground check: ProcRoot is /proc (""
// means the real one, which panics under go test so no test reads the
// host's processes), GOOS is runtime.GOOS unless set, Runner runs the darwin
// "ps".
type ProcOptions struct {
	Runner   execx.Runner
	ProcRoot string
	GOOS     string
}

// ShellPane is what IdleShell looks at in one pane.
type ShellPane struct {
	ID      string
	Command string
	// PID is #{pane_pid}: the pane's own process, its shell.
	PID                           int
	InMode, Synchronized, Focused bool
}

// IdleShell says why keys must NOT be typed into p, "" when they may: its
// current command is the default shell's basename (shell), it is in no mode,
// its window is not synchronized (send-keys would reach every pane of it), no
// client is looking at it, and the pane's own process leads the terminal's
// foreground process group. The last check is what tells the pane's shell at
// its prompt from a bash script, a program started from a bash wrapper, or a
// "su -" / "sudo -i" root shell: tmux names #{pane_current_command} after the
// foreground group leader's argv[0], which reads "bash" for all of them.
func IdleShell(o ProcOptions, p ShellPane, shell string) string {
	switch {
	case shell == "":
		return "the default shell is unknown (tmux show -gv default-shell did not answer)"
	case p.Command != shell:
		return "it runs " + printable(p.Command) + ", not the shell"
	case p.InMode:
		return "it is in copy mode"
	case p.Synchronized:
		return "its window has synchronize-panes on"
	case p.Focused:
		return "a client is looking at it"
	}
	fg, known := Foreground(o, p.PID)
	switch {
	case !known:
		return "cannot tell whether its shell is at its prompt (the terminal's foreground process group could not be read)"
	case !fg:
		return "a program runs in the foreground of its shell"
	}
	return ""
}

// Foreground reports whether pid leads its terminal's foreground process
// group (tpgid == pid); known is false when that cannot be read — a pid of 0,
// no /proc entry, an unparsable stat, a ps that fails or times out, or
// another OS — and the caller then types nothing.
func Foreground(o ProcOptions, pid int) (fg, known bool) {
	if pid <= 0 {
		return false, false
	}
	goos := o.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	var tpgid int
	switch goos {
	case "linux":
		root := o.ProcRoot
		if root == "" {
			if testing.Testing() {
				panic("a test reached the real /proc; set ProcOptions.ProcRoot (Env.ProcRoot) to a scratch directory")
			}
			root = "/proc"
		}
		b, err := readProcStat(filepath.Join(root, strconv.Itoa(pid), "stat"))
		if err != nil {
			return false, false
		}
		// Fields after the LAST ")" (the comm may hold spaces and parens):
		// state ppid pgrp session tty_nr tpgid ...
		s := string(b)
		i := strings.LastIndexByte(s, ')')
		if i < 0 {
			return false, false
		}
		f := strings.Fields(s[i+1:])
		if len(f) < 6 {
			return false, false
		}
		n, err := strconv.Atoi(f[5])
		if err != nil {
			return false, false
		}
		tpgid = n
	case "darwin":
		out, ok := bounded(o.Runner, CallTimeout, "ps", "-o", "tpgid=", "-p", strconv.Itoa(pid))
		n, err := strconv.Atoi(strings.TrimSpace(out))
		if !ok || err != nil {
			return false, false
		}
		tpgid = n
	default:
		return false, false
	}
	if tpgid <= 0 {
		return false, false // no controlling terminal
	}
	return tpgid == pid, true
}

// readProcStat reads a /proc stat file (at most 4 KiB).
func readProcStat(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b := make([]byte, 4096)
	n, err := f.Read(b)
	if n == 0 {
		return nil, err
	}
	return b[:n], nil
}

// DefaultShell is the basename of the server's default-shell, from one
// bounded "tmux show -gv default-shell"; "" when tmux does not answer.
func DefaultShell(r execx.Runner) string { return DefaultShellWithin(r, CallTimeout) }

// DefaultShellWithin is DefaultShell bounded by timeout (--rearm's calls
// never run past its whole-run deadline).
func DefaultShellWithin(r execx.Runner, timeout time.Duration) string {
	out, ok := bounded(r, timeout, "tmux", "show", "-gv", "default-shell")
	sh := strings.TrimSpace(out)
	if !ok || sh == "" {
		return ""
	}
	return filepath.Base(sh)
}

// TypeKeys are the keys TypeRestore sends, in one send-keys: C-e then C-u
// (key names) clear what the line editor holds — an emacs-mode readline line
// (C-y brings it back), an open reverse-i-search, a canonical-mode reader
// (VKILL); in bash's vi command mode C-e switches that shell to emacs mode
// (for good) first — then the restore as literal text, then Enter. Not every
// editor is cleared (bash and zsh vi insert mode, a builtin read): see
// CS-TMUX-069's residual.
func TypeKeys() []string { return []string{"C-e", "C-u", RetypeKeys, "C-m"} }

// TypeRestore types the restore into pane id (one bounded send-keys);
// false when tmux failed.
func TypeRestore(r execx.Runner, timeout time.Duration, id string) bool {
	_, ok := bounded(r, timeout, "tmux", append([]string{"send-keys", "-t", id}, TypeKeys()...)...)
	return ok
}

// focusFormat is PaneFocused's list-panes: a pane id, then the view fields
// that say whether a client looks at it through that line's session.
const focusFormat = "#{pane_id}\t#{pane_active}\t#{window_active}\t#{session_attached}"

// PaneFocused reads, with one bounded "tmux list-panes -a", whether a client
// is looking at pane id now: the active pane of the active window of an
// attached session, through ANY session the pane is listed under (grouped
// sessions, linked windows). known is false when tmux did not answer or no
// longer lists the pane.
func PaneFocused(r execx.Runner, timeout time.Duration, id string) (focused, known bool) {
	out, ok := bounded(r, timeout, "tmux", "list-panes", "-a", "-F", focusFormat)
	if !ok {
		return false, false
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) != 4 || f[0] != id {
			continue
		}
		known = true
		if f[1] == "1" && f[2] == "1" && f[3] != "" && f[3] != "0" {
			focused = true
		}
	}
	return focused, known
}
