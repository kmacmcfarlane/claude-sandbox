// Package clipboard is the write-only host clipboard bridge
// (CS-LNCH-194..203; plan jetbrains-clipboard 07 as amended by 08).
//
// In a sandbox, Claude Code's fullscreen copy has only OSC 52, which some
// terminals drop (JetBrains' JediTerm, VTE, Terminal.app). With the bridge on,
// a dummy WAYLAND_DISPLAY makes Claude Code run "wl-copy", which in the
// container is this binary (argv0, CS-IMG-077): the shim. The shim hands the
// text to a per-container helper on the host through a unix socket in a
// read-only mounted directory, and the helper copies it with the host's own
// xclip / xsel / wl-copy onto a display target taken from the operator's
// shell. The shim also writes OSC 52 to its own terminal once the helper has
// admitted the request, so the bridge never takes away a copy path that works
// without it.
//
// A session can only overwrite the clipboard and PRIMARY with filtered text,
// within the limits below. It cannot read the clipboard (no op exists), reach
// a display, or change the target (a host-only file).
//
// This package imports execx only, so the launcher, the helper and the shim
// share it without a cycle.
package clipboard

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Paths and names shared by the launcher, the helper and the shim.
const (
	// ContainerDir is where the bridge directory is mounted read-only.
	ContainerDir = "/run/claude-sandbox/clipboard"
	// SocketName is the helper's socket in the bridge directory.
	SocketName = "bridge.sock"
	// ContainerSocket is CLAUDE_SANDBOX_CLIPBOARD's value in the container.
	ContainerSocket = ContainerDir + "/" + SocketName
	// DummyWaylandDisplay is the WAYLAND_DISPLAY the claude process gets:
	// absolute (so independent of XDG_RUNTIME_DIR), in a root-owned
	// directory a session cannot plant a socket in, naming nothing. Its only
	// job is to make Claude Code pick wl-copy.
	DummyWaylandDisplay = "/opt/claude-sandbox/clipboard/no-wayland-display"
	// EnvSocket names the helper's socket for the shim.
	EnvSocket = "CLAUDE_SANDBOX_CLIPBOARD"
	// EnvKey turns the bridge on or off for one launch (CS-LNCH-194).
	EnvKey = "CLAUDE_SANDBOX_HOST_CLIPBOARD"
	// EnvDisplay overrides the display target (CS-LNCH-203).
	EnvDisplay = "CLAUDE_SANDBOX_CLIPBOARD_DISPLAY"
	// DirName is the bridge directory's name in the shadow directory.
	DirName = "clipboard"
	// TargetFile is the display target's file in the shadow directory,
	// beside (never inside) the mounted bridge directory.
	TargetFile = "clipboard-target.json"
	// LockName is the helper's single-instance lock in the bridge directory.
	LockName = ".lock"
	// LogFile is the helper's content-free log in the cache root.
	LogFile = "clipboard-bridge.log"
	// HelperSubcommand is the hidden host command the launcher spawns.
	HelperSubcommand = "clipboard-bridge"
	// LabelClipboard records what a container got: "bridge" or "none".
	LabelClipboard = "claude-sandbox.clipboard"
	// MaxSocketPath is the longest sun_path a unix socket can bind.
	MaxSocketPath = 107
)

// Limits (operator answer 127 a). One block, so a change is one edit.
const (
	// MaxCopyBytes caps one copy, before any encoding.
	MaxCopyBytes = 1 << 20
	// MaxOSCBytes caps the payload of the shim's OSC 52 write (raw;
	// ~342 KiB encoded). Larger copies go to the helper only.
	MaxOSCBytes = 256 << 10
	// BucketBurst and BucketRate are the helper's token bucket. A Claude
	// Code copy is two requests (clipboard and PRIMARY), so ten quick
	// copies fit the burst.
	BucketBurst = 20
	BucketRate  = 2 // tokens per second
	// BackendTimeout bounds one host copy-tool run; its group is killed.
	BackendTimeout = 3 * time.Second
	// StickyFallback is how long xsel stays first after xclip failed.
	StickyFallback = 5 * time.Minute
	// PendingAfter is when the helper answers "pending" for a slow backend.
	PendingAfter = 1200 * time.Millisecond
	// ReadDeadline bounds one request on the helper's side.
	ReadDeadline = 2 * time.Second
	// MaxConns is the helper's concurrent connection cap.
	MaxConns = 4
	// ShimConnect, ShimAdmission and ShimTotal are the shim's waits: the
	// connect, the admission line, and the whole exchange.
	ShimConnect   = 500 * time.Millisecond
	ShimAdmission = 300 * time.Millisecond
	ShimTotal     = 1500 * time.Millisecond
	// GuardWrites per GuardWindow is the shim's cap on OSC 52 writes while
	// the helper is unreachable or busy, per container.
	GuardWrites = 10
	GuardWindow = 10 * time.Second
	// MaxTargetFile caps the target file the helper reads.
	MaxTargetFile = 4 << 10
	// maxHeader caps the request line.
	maxHeader = 256
)

// Ops of the protocol.
const (
	OpClipboard      = "clipboard"
	OpPrimary        = "primary"
	OpClearClipboard = "clear-clipboard"
	OpClearPrimary   = "clear-primary"
	OpPing           = "ping"
)

// Magic starts every request line.
const Magic = "CSCB1"

// Answers. The helper answers in two lines: an admission line at once,
// before any backend runs ("accepted" or "err <reason>"), and a result line
// later ("ok", "pending" then the result, or "err <reason>").
const (
	AnswerAccepted = "accepted"
	AnswerOK       = "ok"
	AnswerPending  = "pending"
)

// Refusal and failure reasons.
const (
	ReasonTooLarge     = "too-large"
	ReasonUTF8         = "utf8"
	ReasonControlChar  = "control-char"
	ReasonRateLimited  = "rate-limited"
	ReasonBusy         = "busy"
	ReasonBadRequest   = "bad-request"
	ReasonPeer         = "peer"
	ReasonNoDisplay    = "no-display"
	ReasonStaleDisplay = "stale-display"
	ReasonSuperseded   = "superseded"
	ReasonBackend      = "backend"
	ReasonTimeout      = "timeout"
	ReasonOuter        = "outer-bridge"
)

// Request is one parsed request line.
type Request struct {
	Op  string
	Len int
	// OSC is the shim's terminal path ("tty", "ancestor:<level>:<fd>",
	// "none"; CS-LNCH-198/199): the terminal it FOUND, not proof it wrote
	// there — a refused or rate-limited request is never written. "" when
	// the request carried none (ping, an outer-bridge forward).
	OSC string
}

// IsPayloadOp reports whether op carries text.
func IsPayloadOp(op string) bool { return op == OpClipboard || op == OpPrimary }

func validOp(op string) bool {
	switch op {
	case OpClipboard, OpPrimary, OpClearClipboard, OpClearPrimary, OpPing:
		return true
	}
	return false
}

// FormatRequest renders a request line, "CSCB1 <op> <len>[ osc=<path>]\n".
func FormatRequest(r Request) string {
	s := fmt.Sprintf("%s %s %d", Magic, r.Op, r.Len)
	if r.OSC != "" {
		s += " osc=" + r.OSC
	}
	return s + "\n"
}

var oscPathOK = func(s string) bool {
	if s == "tty" || s == "none" {
		return true
	}
	rest, ok := strings.CutPrefix(s, "ancestor:")
	if !ok {
		return false
	}
	lv, fd, ok := strings.Cut(rest, ":")
	if !ok {
		return false
	}
	l, e1 := strconv.Atoi(lv)
	f, e2 := strconv.Atoi(fd)
	return e1 == nil && e2 == nil && l >= 1 && l <= MaxAncestorLevels && f >= 0 && f <= 2
}

// ParseRequest parses a request line (without its newline).
func ParseRequest(line string) (Request, error) {
	f := strings.Fields(line)
	if len(f) < 3 || len(f) > 4 || f[0] != Magic || !validOp(f[1]) {
		return Request{}, errors.New(ReasonBadRequest)
	}
	n, err := strconv.Atoi(f[2])
	if err != nil || n < 0 || f[2] != strconv.Itoa(n) {
		return Request{}, errors.New(ReasonBadRequest)
	}
	r := Request{Op: f[1], Len: n}
	if len(f) == 4 {
		v, ok := strings.CutPrefix(f[3], "osc=")
		if !ok || !oscPathOK(v) {
			return Request{}, errors.New(ReasonBadRequest)
		}
		r.OSC = v
	}
	if !IsPayloadOp(r.Op) && r.Len != 0 {
		return Request{}, errors.New(ReasonBadRequest)
	}
	return r, nil
}

// PayloadError is ValidatePayload's refusal; Reason is the protocol reason.
type PayloadError struct{ Reason string }

func (e *PayloadError) Error() string { return e.Reason }

// RejectedRune reports whether r may not appear in a copied payload
// (CS-LNCH-198): C0 controls except tab, LF and CR; DEL; C1. ESC is C0, so
// the bracketed-paste end marker ESC [ 2 0 1 ~ and every other escape
// sequence is refused, and so is the one-code-point C1 CSI (U+009B).
func RejectedRune(r rune) bool {
	switch {
	case r == '\t' || r == '\n' || r == '\r':
		return false
	case r < 0x20, r == 0x7f:
		return true
	case r >= 0x80 && r <= 0x9f:
		return true
	}
	return false
}

// ValidatePayload checks one copy: at most MaxCopyBytes, valid UTF-8, and no
// rejected code point. The helper enforces it; the shim checks first so its
// message is friendly.
func ValidatePayload(b []byte) error {
	if len(b) > MaxCopyBytes {
		return &PayloadError{ReasonTooLarge}
	}
	if !utf8.Valid(b) {
		return &PayloadError{ReasonUTF8}
	}
	for _, r := range string(b) {
		if RejectedRune(r) {
			return &PayloadError{ReasonControlChar}
		}
	}
	return nil
}

// errAnswer renders "err <reason>".
func errAnswer(reason string) string { return "err " + reason }

// ParseAnswer splits an answer line into its kind ("accepted", "ok",
// "pending", "err") and reason.
func ParseAnswer(line string) (kind, reason string) {
	line = strings.TrimRight(line, "\r\n")
	if r, ok := strings.CutPrefix(line, "err "); ok {
		return "err", r
	}
	return line, ""
}
