package clipboard

// The display target (CS-LNCH-203): which host display the helper copies
// onto. It comes from the operator's shell — the one that launched, or last
// attached to or joined, the container — never from the helper's own
// environment and never from the systemd user environment, which names the
// machine's console (the wrong clipboard for an operator working over ssh -X).

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"unicode"
)

// Backends a target can name.
const (
	BackendX       = "x"
	BackendWayland = "wayland"
	BackendNone    = "none"
)

// Who wrote a target.
const (
	ByCreate = "create"
	ByAttach = "attach"
	ByJoin   = "join"
)

// Target is the display target file's content.
type Target struct {
	V              int    `json:"v"`
	Backend        string `json:"backend"`
	Display        string `json:"display,omitempty"`
	XAuthority     string `json:"xauthority,omitempty"`
	WaylandDisplay string `json:"waylandDisplay,omitempty"`
	SetAt          int64  `json:"setAt"`
	By             string `json:"by"`
}

// Usable reports whether t names a display.
func (t *Target) Usable() bool {
	return t != nil && (t.Backend == BackendX || t.Backend == BackendWayland)
}

// Describe is the banner's name for a target.
func (t *Target) Describe() string {
	switch {
	case t == nil || t.Backend == BackendNone || t.Backend == "":
		return "none"
	case t.Backend == BackendX:
		return "X " + t.Display
	default:
		return "Wayland " + t.WaylandDisplay + " (experimental)"
	}
}

var displayRE = regexp.MustCompile(`^([A-Za-z0-9._-]*):([0-9]{1,4})(\.[0-9]{1,2})?$`)

// DisplayHost splits an X display into its host part and number; ok is false
// for a malformed one.
func DisplayHost(d string) (host, num string, ok bool) {
	m := displayRE.FindStringSubmatch(d)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// localHost reports whether an X display host part names this machine
// (CS-LNCH-203 validation): empty, unix, localhost, an IPv4 loopback
// address, or the machine's own name (short or as returned). Any other host
// is refused, so the helper never opens an X connection to another machine.
func localHost(host, hostname string) bool {
	switch host {
	case "", "unix", "localhost":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.To4() != nil && ip.To4()[0] == 127
	}
	if hostname == "" {
		return false
	}
	short, _, _ := strings.Cut(hostname, ".")
	return strings.EqualFold(host, hostname) || strings.EqualFold(host, short)
}

// forwardedHost reports whether a display host part names a TCP display on
// this machine (an SSH-forwarded one): local, but not empty or unix.
func forwardedHost(host, hostname string) bool {
	return host != "" && host != "unix" && localHost(host, hostname)
}

// ValidDisplay checks an X display against the host rule.
func ValidDisplay(d, hostname string) bool {
	host, _, ok := DisplayHost(d)
	return ok && localHost(host, hostname)
}

var waylandNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// printable reports whether s holds no control or bidi character.
func printable(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool {
		return unicode.IsControl(r) || isBidi(r)
	}) < 0
}

func isBidi(r rune) bool {
	switch {
	case r == 0x061C, r == 0x200E, r == 0x200F:
		return true
	case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

// FileChecks are the filesystem checks of the validation rules; the zero
// value means the real ones for uid.
type FileChecks struct {
	UID int
	// OwnedRegular opens path O_NOFOLLOW and requires a regular file owned
	// by UID (XAUTHORITY, CS-LNCH-203).
	OwnedRegular func(path string) error
	// OwnedSocket requires path to be a socket owned by UID (a Wayland
	// socket).
	OwnedSocket func(path string) error
}

func (c FileChecks) ownedRegular(p string) error {
	if c.OwnedRegular != nil {
		return c.OwnedRegular(p)
	}
	return OwnedRegular(p, c.UID)
}

func (c FileChecks) ownedSocket(p string) error {
	if c.OwnedSocket != nil {
		return c.OwnedSocket(p)
	}
	return OwnedSocket(p, c.UID)
}

// OwnedRegular opens path with O_NOFOLLOW and requires a regular file owned
// by uid.
func OwnedRegular(path string, uid int) error {
	if !filepath.IsAbs(path) {
		return errors.New("not absolute")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != uid {
		return errors.New("not owned by you")
	}
	return nil
}

// OwnedSocket requires path to be a socket (not a symlink) owned by uid.
func OwnedSocket(path string, uid int) error {
	if !filepath.IsAbs(path) {
		return errors.New("not absolute")
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return errors.New("not a socket")
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != uid {
		return errors.New("not owned by you")
	}
	return nil
}

// Validate re-checks a target's fields (the launcher before writing, the
// helper at each read): the X host rule, XAUTHORITY a regular user-owned
// file, a Wayland display an absolute user-owned socket, every value
// printable.
func (t *Target) Validate(hostname string, fc FileChecks) error {
	if t.V != 1 {
		return errors.New("unknown version")
	}
	for _, v := range []string{t.Backend, t.Display, t.XAuthority, t.WaylandDisplay, t.By} {
		if !printable(v) {
			return errors.New("unprintable value")
		}
	}
	switch t.Backend {
	case BackendNone:
		return nil
	case BackendX:
		if !ValidDisplay(t.Display, hostname) {
			return fmt.Errorf("display %q is not on this machine", t.Display)
		}
		if t.XAuthority != "" {
			if err := fc.ownedRegular(t.XAuthority); err != nil {
				return fmt.Errorf("XAUTHORITY %s: %v", t.XAuthority, err)
			}
		}
		return nil
	case BackendWayland:
		if err := fc.ownedSocket(t.WaylandDisplay); err != nil {
			return fmt.Errorf("Wayland socket %s: %v", t.WaylandDisplay, err)
		}
		return nil
	}
	return fmt.Errorf("unknown backend %q", t.Backend)
}

// ResolveInput is everything target resolution reads (CS-LNCH-203).
type ResolveInput struct {
	// Getenv is the launcher's own environment.
	Getenv func(string) string
	// Tmux is the session environment overlay of TMUX_PANE's session
	// (tmux show-environment -t "$TMUX_PANE"): a value, or nil for a key
	// tmux marks removed. A nil map (no tmux, a timeout, an error) keeps
	// the shell's values.
	Tmux map[string]*string
	// Hostname is this machine's name (os.Hostname).
	Hostname string
	// Home is the invoking user's home ($HOME/.Xauthority).
	Home string
	// LookPath finds the host copy tools.
	LookPath func(string) (string, error)
	Files    FileChecks
}

// Resolution is the outcome of resolving a target.
type Resolution struct {
	// Target is the resolved target; nil means none.
	Target *Target
	// Clear is set for the override "none": the one value that also
	// replaces a usable target (CS-LNCH-203 never-downgrade).
	Clear bool
	// Warning is one line naming an invalid override (it is then ignored).
	Warning string
	// Hint names the missing host tool when a display exists but nothing
	// can copy to it ("install xclip"), "" otherwise.
	Hint string
}

// overlayKeys are the keys in tmux's default update-environment that the
// overlay takes from the session (CS-LNCH-203 step 3).
var overlayKeys = []string{"DISPLAY", "XAUTHORITY", "SSH_CONNECTION"}

// ParseTmuxEnv parses "tmux show-environment" output: KEY=value lines and
// -KEY (removed) lines, for the overlay keys only.
func ParseTmuxEnv(out string) map[string]*string {
	m := map[string]*string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		for _, k := range overlayKeys {
			if line == "-"+k {
				m[k] = nil
			} else if v, ok := strings.CutPrefix(line, k+"="); ok {
				v := v
				m[k] = &v
			}
		}
	}
	return m
}

// Resolve applies CS-LNCH-203's steps: the override, the shell's
// environment, the tmux overlay, which displays count (the SSH rule), the
// backend (X before Wayland; never an automatic wl-copy on GNOME, operator
// answer 131 b), and validation.
func Resolve(in ResolveInput) Resolution {
	if raw := in.Getenv(EnvDisplay); raw != "" {
		res, ok := in.override(raw)
		if ok {
			return res
		}
		// Invalid: one warning, then resolve as if unset.
		r := in.resolveShell()
		r.Warning = res.Warning
		return r
	}
	return in.resolveShell()
}

func (in ResolveInput) look(name string) bool {
	if in.LookPath == nil {
		return false
	}
	_, err := in.LookPath(name)
	return err == nil
}

func (in ResolveInput) hasX() bool { return in.look("xclip") || in.look("xsel") }

func (in ResolveInput) xauthority(env func(string) string) (string, bool) {
	xa := env("XAUTHORITY")
	if xa == "" {
		if in.Home == "" {
			return "", false
		}
		xa = filepath.Join(in.Home, ".Xauthority")
	}
	if !filepath.IsAbs(xa) || !printable(xa) {
		return "", false
	}
	if err := in.Files.ownedRegular(xa); err != nil {
		return xa, false
	}
	return xa, true
}

// xauthHint names the X authority file that failed and the rule (CS-LNCH-203).
func xauthHint(xa string) string {
	if xa == "" {
		return "XAUTHORITY must be an absolute path to a regular file you own"
	}
	return "XAUTHORITY " + xa + " must be a regular file you own (not a symlink)"
}

func (in ResolveInput) xTarget(display string, env func(string) string) (*Target, string) {
	if !in.hasX() {
		return nil, "install xclip on the host"
	}
	xa, ok := in.xauthority(env)
	if !ok {
		return nil, xauthHint(xa)
	}
	return &Target{V: 1, Backend: BackendX, Display: display, XAuthority: xa}, ""
}

func (in ResolveInput) waylandPath(name, runtimeDir string) (string, bool) {
	if !printable(name) {
		return "", false
	}
	if !filepath.IsAbs(name) {
		if !waylandNameRE.MatchString(name) || !filepath.IsAbs(runtimeDir) {
			return "", false
		}
		name = filepath.Join(runtimeDir, name)
	}
	if in.Files.ownedSocket(name) != nil {
		return "", false
	}
	return name, true
}

func (in ResolveInput) waylandTarget(path string) (*Target, string) {
	if !in.look("wl-copy") {
		return nil, "install wl-clipboard on the host"
	}
	return &Target{V: 1, Backend: BackendWayland, WaylandDisplay: path}, ""
}

const overrideRule = "none, x:<DISPLAY> or a bare <DISPLAY> on this machine, or wayland:<name or absolute socket path>"

func (in ResolveInput) override(raw string) (Resolution, bool) {
	bad := func(why string) (Resolution, bool) {
		return Resolution{Warning: fmt.Sprintf("WARNING: %s=%q is ignored (%s); it takes %s.", EnvDisplay, raw, why, overrideRule)}, false
	}
	if !printable(raw) {
		return bad("unprintable")
	}
	switch {
	case raw == "none":
		return Resolution{Clear: true, Target: nil}, true
	case strings.HasPrefix(raw, "wayland:"):
		p, ok := in.waylandPath(strings.TrimPrefix(raw, "wayland:"), in.Getenv("XDG_RUNTIME_DIR"))
		if !ok {
			return bad("not a Wayland socket you own")
		}
		t, hint := in.waylandTarget(p)
		return Resolution{Target: t, Hint: hint}, true
	}
	d := strings.TrimPrefix(raw, "x:")
	if !ValidDisplay(d, in.Hostname) {
		return bad("not an X display on this machine")
	}
	if _, ok := in.xauthority(in.Getenv); !ok && in.hasX() {
		return bad("no XAUTHORITY file you own")
	}
	t, hint := in.xTarget(d, in.Getenv)
	if t == nil && hint == "" {
		return bad("no XAUTHORITY file you own")
	}
	return Resolution{Target: t, Hint: hint}, true
}

func (in ResolveInput) resolveShell() Resolution {
	env := func(k string) string {
		if in.Tmux != nil {
			if v, ok := in.Tmux[k]; ok {
				if v == nil {
					return ""
				}
				return *v
			}
		}
		return in.Getenv(k)
	}
	underSSH := env("SSH_CONNECTION") != ""
	display := env("DISPLAY")
	host, _, okDisplay := DisplayHost(display)
	countsX := okDisplay && printable(display)
	if underSSH {
		// Only an SSH-forwarded display: a local :N is the console's.
		countsX = countsX && forwardedHost(host, in.Hostname)
	} else {
		countsX = countsX && localHost(host, in.Hostname)
	}
	var hint string
	if countsX {
		t, h := in.xTarget(display, env)
		if t != nil {
			return Resolution{Target: t}
		}
		hint = h
	}
	if underSSH {
		// Any WAYLAND_DISPLAY under SSH is the console's (or waypipe's,
		// which needs the override).
		return Resolution{Hint: hint}
	}
	wd := in.Getenv("WAYLAND_DISPLAY")
	if wd == "" {
		return Resolution{Hint: hint}
	}
	p, ok := in.waylandPath(wd, in.Getenv("XDG_RUNTIME_DIR"))
	if !ok {
		return Resolution{Hint: hint}
	}
	if strings.Contains(strings.ToUpper(in.Getenv("XDG_CURRENT_DESKTOP")), "GNOME") {
		// Operator answer 131 b: on GNOME, wl-copy takes keyboard focus on
		// every copy; never picked automatically there.
		return Resolution{Hint: "install xclip on the host; on GNOME, wl-copy is used only through " + EnvDisplay + "=wayland:" + wd}
	}
	t, h := in.waylandTarget(p)
	if t == nil && hint == "" {
		hint = h
	}
	return Resolution{Target: t, Hint: hint}
}

// ShouldWrite is never-downgrade (CS-LNCH-203): only a usable target, or the
// explicit override none, replaces the file. A resolved none leaves it.
func (r Resolution) ShouldWrite() bool { return r.Target.Usable() || r.Clear }

// TargetToWrite is what a write stores: the target, or a none target for
// the override.
func (r Resolution) TargetToWrite(by string, setAt int64) Target {
	t := Target{V: 1, Backend: BackendNone}
	if r.Target != nil {
		t = *r.Target
	}
	t.V, t.By, t.SetAt = 1, by, setAt
	return t
}

// WriteTarget writes the target file atomically: a 0600 temp file in the
// same directory, renamed into place.
func WriteTarget(path string, t Target) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".clipboard-target-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// ErrNoTarget is ReadTarget's answer for a missing file: none.
var ErrNoTarget = errors.New("no display target")

// ReadTarget reads and re-validates the target file (CS-LNCH-198): opened
// O_NOFOLLOW, a regular file owned by uid, at most MaxTargetFile bytes.
func ReadTarget(path, hostname string, fc FileChecks) (*Target, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoTarget
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("target file is not a regular file")
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != fc.UID {
		return nil, errors.New("target file is not yours")
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxTargetFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxTargetFile {
		return nil, errors.New("target file too large")
	}
	var t Target
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("target file: %v", err)
	}
	if err := t.Validate(hostname, fc); err != nil {
		return nil, err
	}
	return &t, nil
}
