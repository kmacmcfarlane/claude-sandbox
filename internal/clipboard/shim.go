package clipboard

// The container side (CS-LNCH-199): "wl-copy" in the container is this
// binary (argv0). Claude Code runs it because the claude process has a
// WAYLAND_DISPLAY (the dummy) and wl-copy on PATH; it ignores the exit
// status and kills it after 2 s (SIGTERM).

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// MaxAncestorLevels bounds the tty fallback's walk up from the shim's parent
// (plan 08 N1).
const MaxAncestorLevels = 4

// GuardFile is the unreachable-state OSC 52 guard's counter file, in the
// container's own /tmp (container-local, so per container).
const GuardFile = "/tmp/claude-sandbox-clipboard-guard"

// Shim is one wl-copy invocation; the zero values of the seams are the real
// ones where noted.
type Shim struct {
	Args   []string
	Stdin  io.Reader
	Stderr io.Writer
	Getenv func(string) string

	// OpenTTY opens /dev/tty O_WRONLY|O_NOCTTY (nil = the real one).
	OpenTTY func() (io.WriteCloser, error)
	// ProcRoot is the /proc the ancestor walk reads ("" = /proc).
	ProcRoot string
	// Getppid and UID identify the shim (nil/zero = the real ones).
	Getppid func() int
	UID     *int
	// Notify and StopNotify hold TERM/INT/HUP around the OSC write (nil =
	// signal.Notify / signal.Stop).
	Notify     func(c chan<- os.Signal, sig ...os.Signal)
	StopNotify func(c chan<- os.Signal)
	// GuardPath is the guard's file ("" = GuardFile); Now its clock.
	GuardPath string
	Now       func() time.Time
	// Exec replaces the process with the next real wl-copy (nil =
	// syscall.Exec); Self is this binary (nil = os.Executable).
	Exec func(path string, argv, env []string) error
	Self func() (string, error)
}

type shimOp struct {
	primary, trim, clear bool
	words                []string
}

func (s *Shim) errf(format string, a ...any) {
	fmt.Fprintf(s.Stderr, "wl-copy: "+format+"\n", a...)
}

// parseArgs is the argument table of CS-LNCH-199. code is non-zero for an
// error (2 for an unknown argument, 1 for a non-text type).
func parseArgs(args []string) (shimOp, int, string) {
	var o shimOp
	words := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if words {
			o.words = append(o.words, a)
			continue
		}
		switch {
		case a == "--":
			words = true
		case a == "-p" || a == "--primary":
			o.primary = true
		case a == "-n" || a == "--trim-newline":
			o.trim = true
		case a == "-c" || a == "--clear":
			o.clear = true
		case a == "-f" || a == "--foreground" || a == "-o" || a == "--paste-once":
		case a == "-t" || a == "--type" || strings.HasPrefix(a, "--type="):
			t := strings.TrimPrefix(a, "--type=")
			if t == a {
				if i+1 >= len(args) {
					return o, 2, "option " + a + " needs a type"
				}
				i++
				t = args[i]
			}
			if !strings.HasPrefix(t, "text/") {
				return o, 1, "only text can be copied out of the sandbox (type " + t + ")"
			}
		case strings.HasPrefix(a, "-") && a != "-":
			return o, 2, "unsupported option " + a
		default:
			o.words = append(o.words, a)
		}
	}
	return o, 0, ""
}

// Run is the shim. It returns the exit status.
func (s *Shim) Run() int {
	if s.Stderr == nil {
		s.Stderr = os.Stderr
	}
	sock := s.Getenv(EnvSocket)
	if sock == "" {
		return s.execNext()
	}
	o, code, msg := parseArgs(s.Args)
	if code != 0 {
		s.errf("%s", msg)
		return code
	}
	var data []byte
	if !o.clear {
		if len(o.words) > 0 {
			data = []byte(strings.Join(o.words, " "))
		} else if s.Stdin != nil {
			b, err := io.ReadAll(io.LimitReader(s.Stdin, MaxCopyBytes+1))
			if err != nil {
				s.errf("reading stdin: %v", err)
				return 1
			}
			data = b
		}
		if o.trim && len(data) > 0 && data[len(data)-1] == '\n' {
			data = data[:len(data)-1]
		}
		if err := ValidatePayload(data); err != nil {
			// Nothing is written anywhere.
			s.errf("not copied: %s", payloadWhy(err.Error()))
			return 1
		}
	}
	op := OpClipboard
	switch {
	case o.clear && o.primary:
		op = OpClearPrimary
	case o.clear:
		op = OpClearClipboard
	case o.primary:
		op = OpPrimary
	}
	oscEligible := op == OpClipboard && len(data) <= MaxOSCBytes
	var term io.WriteCloser
	oscPath := "none"
	if oscEligible {
		term, oscPath = s.findTerminal()
		if term != nil {
			defer term.Close()
		}
	}
	req := Request{Op: op, Len: len(data)}
	if op == OpClipboard {
		req.OSC = oscPath
	}

	// Ask the helper first (M1): its admission bounds the terminal write.
	cl, err := Dial(sock, req, data, ShimTotal)
	var adm string
	if err == nil {
		defer cl.Close()
		adm, err = cl.Admission()
	}
	kind, reason := ParseAnswer(adm)
	unreachable := err != nil || adm == ""
	busy := kind == "err" && reason == ReasonBusy

	wrote := false
	switch {
	case !oscEligible || term == nil:
	case kind == AnswerAccepted && !unreachable:
		if rc, ok := s.writeOSC(term, data); !ok {
			return rc
		}
		wrote = true
	case unreachable || busy:
		if !s.guard() {
			s.errf("not copied: the clipboard bridge is down and the terminal-copy limit (%d per %s) is reached", GuardWrites, GuardWindow)
			return 1
		}
		if rc, ok := s.writeOSC(term, data); !ok {
			return rc
		}
		wrote = true
	}

	if unreachable || busy {
		if wrote {
			s.errf("the clipboard bridge is not reachable; copied to this terminal over OSC 52 only (the next attach restarts the bridge)")
			return 0
		}
		s.errf("the clipboard bridge is not reachable")
		return 1
	}
	if kind == "err" {
		s.errf("not copied: %s", payloadWhy(reason))
		return 1
	}
	res, err := cl.Result()
	if err != nil {
		if wrote {
			return 0
		}
		s.errf("no answer from the clipboard bridge")
		return 1
	}
	rk, rr := ParseAnswer(res)
	switch {
	case rk == AnswerOK || rk == AnswerPending:
		return 0
	case rk == "err" && rr == ReasonNoDisplay && wrote:
		return 0
	}
	s.errf("host copy failed: %s", rr)
	return 1
}

func payloadWhy(reason string) string {
	switch reason {
	case ReasonTooLarge:
		return fmt.Sprintf("larger than %d bytes", MaxCopyBytes)
	case ReasonUTF8:
		return "not valid UTF-8"
	case ReasonControlChar:
		return "holds a control character (only tab, newline and carriage return pass)"
	case ReasonRateLimited:
		return "too many copies; the bridge is rate-limited"
	}
	return reason
}

// OSCSequence is the OSC 52 clipboard write, in the plain form Claude Code
// uses in a container (no tmux passthrough: TMUX is unset inside).
func OSCSequence(data []byte) []byte {
	return []byte("\x1b]52;c;" + base64.StdEncoding.EncodeToString(data) + "\a")
}

// writeOSC writes the whole sequence with TERM, INT and HUP held (plan 08
// § 3.2 step 5): one write unless interrupted, looping on a short count, so
// the terminal always gets a complete sequence. A held signal is acted on
// after it: ok false and 128+n.
func (s *Shim) writeOSC(w io.Writer, data []byte) (int, bool) {
	notify, stop := s.Notify, s.StopNotify
	if notify == nil {
		notify = signal.Notify
	}
	if stop == nil {
		stop = signal.Stop
	}
	held := make(chan os.Signal, 3)
	notify(held, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	writeFull(w, OSCSequence(data))
	stop(held)
	select {
	case sig := <-held:
		if ss, ok := sig.(syscall.Signal); ok {
			return 128 + int(ss), false
		}
		return 143, false
	default:
	}
	return 0, true
}

// writeFull loops until every byte is written or the writer fails.
func writeFull(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		b = b[n:]
		if err != nil && !errors.Is(err, syscall.EINTR) && !errors.Is(err, syscall.EAGAIN) {
			return err
		}
		if n == 0 && err == nil {
			return io.ErrShortWrite
		}
	}
	return nil
}

func (s *Shim) procRoot() string {
	if s.ProcRoot == "" {
		return "/proc"
	}
	return s.ProcRoot
}

// findTerminal is CS-LNCH-199's terminal rule: /dev/tty; else an ancestor
// walk from the parent (at most MaxAncestorLevels, same uid, stopping at PID
// 1) to the first fd 0/1/2 that is a character device whose rdev equals the
// parent's tty_nr; else none. The shim never writes to a terminal it cannot
// tie to the copying session.
func (s *Shim) findTerminal() (io.WriteCloser, string) {
	open := s.OpenTTY
	if open == nil {
		open = func() (io.WriteCloser, error) {
			return os.OpenFile("/dev/tty", os.O_WRONLY|syscall.O_NOCTTY, 0)
		}
	}
	if t, err := open(); err == nil {
		return t, "tty"
	}
	ppid := os.Getppid()
	if s.Getppid != nil {
		ppid = s.Getppid()
	}
	uid := os.Getuid()
	if s.UID != nil {
		uid = *s.UID
	}
	f, path := FindAncestorTTY(s.procRoot(), ppid, uid)
	if f == nil {
		return nil, "none"
	}
	return f, path
}

// statFields returns the fields of /proc/<pid>/stat after the comm field
// (field 3 onward), split after the LAST ")".
func statFields(procRoot string, pid int) ([]string, bool) {
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return nil, false
	}
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 {
		return nil, false
	}
	return strings.Fields(string(b)[i+1:]), true
}

// devParts decodes a Linux dev_t / tty_nr: major (dev>>8)&0xfff |
// (dev>>32)&~0xfff, minor (dev&0xff) | (dev>>12)&~0xff.
func devParts(dev uint64) (major, minor uint64) {
	major = (dev>>8)&0xfff | (dev>>32)&^uint64(0xfff)
	minor = dev&0xff | (dev>>12)&^uint64(0xff)
	return
}

// FindAncestorTTY is the ancestor walk (plan 08 N1).
func FindAncestorTTY(procRoot string, ppid, uid int) (*os.File, string) {
	pf, ok := statFields(procRoot, ppid)
	if !ok || len(pf) < 5 {
		return nil, ""
	}
	ttyNr, err := strconv.ParseUint(pf[4], 10, 64)
	if err != nil || ttyNr == 0 {
		return nil, ""
	}
	wantMaj, wantMin := devParts(ttyNr)
	pid := ppid
	for level := 1; level <= MaxAncestorLevels && pid > 1; level++ {
		fi, err := os.Stat(filepath.Join(procRoot, strconv.Itoa(pid)))
		st, ok := statT(fi)
		if err != nil || !ok || int(st.Uid) != uid {
			return nil, ""
		}
		for fd := 0; fd <= 2; fd++ {
			f, err := os.OpenFile(filepath.Join(procRoot, strconv.Itoa(pid), "fd", strconv.Itoa(fd)), os.O_WRONLY|syscall.O_NOCTTY, 0)
			if err != nil {
				continue
			}
			fst, err := f.Stat()
			if s, ok := statT(fst); err == nil && ok && fst.Mode()&os.ModeCharDevice != 0 {
				maj, mn := devParts(uint64(s.Rdev))
				if maj == wantMaj && mn == wantMin {
					return f, fmt.Sprintf("ancestor:%d:%d", level, fd)
				}
			}
			f.Close()
		}
		f, ok := statFields(procRoot, pid)
		if !ok || len(f) < 2 {
			return nil, ""
		}
		next, err := strconv.Atoi(f[1])
		if err != nil {
			return nil, ""
		}
		pid = next
	}
	return nil, ""
}

func statT(fi os.FileInfo) (*syscall.Stat_t, bool) {
	if fi == nil {
		return nil, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return st, ok
}

// guard counts the OSC 52 writes made while the helper is unreachable or
// busy: at most GuardWrites per GuardWindow per container, in a flocked
// counter file in the container's /tmp. A guard that cannot be read refuses.
func (s *Shim) guard() bool {
	path := s.GuardPath
	if path == "" {
		path = GuardFile
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return false
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return false
	}
	b, _ := io.ReadAll(io.LimitReader(f, 4096))
	var keep []string
	for _, l := range strings.Fields(string(b)) {
		ms, err := strconv.ParseInt(l, 10, 64)
		if err == nil && now.Sub(time.UnixMilli(ms)) < GuardWindow {
			keep = append(keep, l)
		}
	}
	if len(keep) >= GuardWrites {
		return false
	}
	keep = append(keep, strconv.FormatInt(now.UnixMilli(), 10))
	f.Truncate(0)
	f.WriteAt([]byte(strings.Join(keep, "\n")+"\n"), 0)
	return true
}

// execNext runs the next real wl-copy on PATH that is not this binary (a
// child image's), when the bridge variable is unset.
func (s *Shim) execNext() int {
	self := s.Self
	if self == nil {
		self = os.Executable
	}
	me, _ := self()
	meInfo, _ := os.Stat(me)
	for _, d := range filepath.SplitList(s.Getenv("PATH")) {
		if !filepath.IsAbs(d) {
			continue
		}
		p := filepath.Join(d, "wl-copy")
		fi, err := os.Stat(p)
		if err != nil || !fi.Mode().IsRegular() || fi.Mode()&0o111 == 0 {
			continue
		}
		if meInfo != nil && os.SameFile(fi, meInfo) {
			continue
		}
		exec := s.Exec
		if exec == nil {
			exec = syscall.Exec
		}
		var env []string
		for _, e := range os.Environ() {
			if !strings.HasPrefix(e, EnvSocket+"=") {
				env = append(env, e)
			}
		}
		if err := exec(p, append([]string{"wl-copy"}, s.Args...), env); err != nil {
			s.errf("exec %s: %v", p, err)
			return 126
		}
		return 0
	}
	s.errf("no clipboard in this sandbox (hostClipboard is off)")
	return 1
}
