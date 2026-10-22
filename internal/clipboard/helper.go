package clipboard

// The host helper, "claude-sandbox clipboard-bridge" (CS-LNCH-198): one per
// container, spawned detached by the launcher after docker create. It serves
// the bridge socket, admits or refuses each request at once, and runs the
// host's copy tool on the display target the launcher wrote.

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
)

// Helper is one bridge helper. The zero values of the seams are the real
// ones where noted.
type Helper struct {
	// Dir is the bridge directory (socket, lock), mounted read-only into
	// the container.
	Dir string
	// TargetPath is the display target file; "" for a nested helper.
	TargetPath string
	// Container and ID name the container whose life the helper follows;
	// ID is the full 64-hex id, matched exactly.
	Container, ID string
	// Outer is the outer bridge's socket of a nested helper (CS-LNCH-200):
	// requests are forwarded there and the target is ignored.
	Outer string

	Runner execx.Runner
	Log    io.Writer
	// Now is the clock (nil = time.Now).
	Now func() time.Time
	// UID is the helper's uid, the only peer it serves.
	UID int
	// Hostname is this machine's name, for the X host rule.
	Hostname string
	// Home and Path are the backend's HOME and PATH.
	Home, Path string
	// LookPath finds the host copy tools (nil = exec.LookPath via PATH).
	LookPath func(string) (string, error)
	// ProcRoot is the /proc the stale-display check reads ("" = /proc);
	// X11Dir the X socket directory ("" = /tmp/.X11-unix).
	ProcRoot string
	X11Dir   string
	// PeerUID reads a connection's peer uid (nil = SO_PEERCRED).
	PeerUID func(*net.UnixConn) (int, error)
	// Files are the target's file checks (zero = the real ones for UID).
	Files FileChecks
	// DirCheck, EventsRetry and InspectEvery pace the exit watch (zero =
	// 30 s, 5 s and 5 min).
	DirCheck, EventsRetry, InspectEvery time.Duration

	mu       sync.Mutex
	tokens   float64
	last     time.Time
	conns    int
	queues   map[string]*queue
	sticky   map[string]time.Time // target key -> xsel first until
	lastLog  time.Time
	listener *net.UnixListener
	done     chan struct{}
	stopOnce sync.Once
}

// ref is how docker is asked about the container: the full id when known,
// else the name.
func (h *Helper) ref() string {
	if h.ID != "" {
		return h.ID
	}
	return h.Container
}

func (h *Helper) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Helper) logf(format string, a ...any) {
	if h.Log == nil {
		return
	}
	fmt.Fprintf(h.Log, "%s %s\n", h.now().UTC().Format(time.RFC3339), fmt.Sprintf(format, a...))
}

// logRefusal logs a refusal at most once a second (CS-LNCH-198).
func (h *Helper) logRefusal(format string, a ...any) {
	h.mu.Lock()
	now := h.now()
	if now.Sub(h.lastLog) < time.Second {
		h.mu.Unlock()
		return
	}
	h.lastLog = now
	h.mu.Unlock()
	h.logf(format, a...)
}

// take takes one token from the bucket (BucketBurst, BucketRate per second).
func (h *Helper) take() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	if h.last.IsZero() {
		h.tokens = BucketBurst
	} else {
		h.tokens += now.Sub(h.last).Seconds() * BucketRate
		if h.tokens > BucketBurst {
			h.tokens = BucketBurst
		}
	}
	h.last = now
	if h.tokens < 1 {
		return false
	}
	h.tokens--
	return true
}

// ErrHeld is Serve's answer when another helper holds the lock.
var ErrHeld = errors.New("another helper serves this directory")

// Lock takes the single-instance lock (CS-LNCH-198); the returned file
// holds it until closed.
func Lock(dir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, LockName), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrHeld
		}
		return nil, err
	}
	return f, nil
}

// checkDir requires the bridge directory to be a real directory owned by uid.
func checkDir(dir string, uid int) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return errors.New("not a directory")
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != uid {
		return errors.New("not owned by you")
	}
	return nil
}

// Listen takes the lock and binds the socket: a stale socket is removed only
// after a connect to it failed, and the new one is chmodded 0600.
func (h *Helper) Listen() (*os.File, error) {
	if err := checkDir(h.Dir, h.UID); err != nil {
		return nil, fmt.Errorf("%s: %v", h.Dir, err)
	}
	lock, err := Lock(h.Dir)
	if err != nil {
		return nil, err
	}
	sock := filepath.Join(h.Dir, SocketName)
	if len(sock) > MaxSocketPath {
		lock.Close()
		return nil, fmt.Errorf("%s: longer than %d bytes", sock, MaxSocketPath)
	}
	if _, err := os.Lstat(sock); err == nil {
		if c, err := net.DialTimeout("unix", sock, ShimConnect); err == nil {
			c.Close()
			lock.Close()
			return nil, ErrHeld
		}
		os.Remove(sock)
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		lock.Close()
		return nil, err
	}
	l.SetUnlinkOnClose(true)
	if err := os.Chmod(sock, 0o600); err != nil {
		l.Close()
		lock.Close()
		return nil, err
	}
	h.listener = l
	return lock, nil
}

// Serve accepts connections until Stop. Listen must have succeeded.
func (h *Helper) Serve() {
	h.mu.Lock()
	if h.done == nil {
		h.done = make(chan struct{})
	}
	h.mu.Unlock()
	for {
		c, err := h.listener.AcceptUnix()
		if err != nil {
			select {
			case <-h.done:
				return
			default:
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}
		go h.handle(c)
	}
}

// Stop closes the listener (removing the socket).
func (h *Helper) Stop() {
	h.stopOnce.Do(func() {
		h.mu.Lock()
		if h.done == nil {
			h.done = make(chan struct{})
		}
		close(h.done)
		h.mu.Unlock()
		if h.listener != nil {
			h.listener.Close()
		}
	})
}

// Done is closed once Stop ran.
func (h *Helper) Done() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.done == nil {
		h.done = make(chan struct{})
	}
	return h.done
}

func (h *Helper) handle(c *net.UnixConn) {
	defer c.Close()
	start := h.now()
	c.SetDeadline(time.Now().Add(ReadDeadline))
	answer := func(s string) { io.WriteString(c, s+"\n") }

	h.mu.Lock()
	h.conns++
	n := h.conns
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.conns--
		h.mu.Unlock()
	}()
	if n > MaxConns {
		answer(errAnswer(ReasonBusy))
		h.logRefusal("refused busy")
		return
	}
	peer := h.PeerUID
	if peer == nil {
		peer = peerUID
	}
	// Hygiene, not a control: under rootless docker or userns-remap the
	// mapping differs, and copies fail closed.
	if uid, err := peer(c); err != nil || uid != h.UID {
		answer(errAnswer(ReasonPeer))
		h.logRefusal("refused peer uid %d", uid)
		return
	}
	br := bufio.NewReaderSize(c, maxHeader+1)
	line, err := readLine(br)
	if err != nil {
		answer(errAnswer(ReasonBadRequest))
		h.logRefusal("refused bad-request")
		return
	}
	req, err := ParseRequest(line)
	if err != nil {
		answer(errAnswer(ReasonBadRequest))
		h.logRefusal("refused bad-request")
		return
	}
	if req.Op == OpPing {
		// No token, no backend (CS-LNCH-197's health check).
		answer(AnswerAccepted)
		answer(AnswerOK)
		return
	}
	if req.Len > MaxCopyBytes {
		answer(errAnswer(ReasonTooLarge))
		h.logRefusal("refused %s %d too-large osc=%s", req.Op, req.Len, req.OSC)
		return
	}
	data := make([]byte, req.Len)
	if _, err := io.ReadFull(br, data); err != nil {
		answer(errAnswer(ReasonBadRequest))
		h.logRefusal("refused %s short read", req.Op)
		return
	}
	if err := ValidatePayload(data); err != nil {
		answer(errAnswer(err.Error()))
		h.logRefusal("refused %s %d %s osc=%s", req.Op, req.Len, err.Error(), req.OSC)
		return
	}
	if !h.take() {
		answer(errAnswer(ReasonRateLimited))
		h.logRefusal("refused %s %d rate-limited osc=%s", req.Op, req.Len, req.OSC)
		return
	}
	answer(AnswerAccepted)

	res := h.submit(req.Op, data)
	timer := time.NewTimer(PendingAfter)
	defer timer.Stop()
	select {
	case r := <-res:
		answer(r.answer)
		h.logf("%s %d %s %s %dms osc=%s", req.Op, req.Len, r.backend, r.answer, h.now().Sub(start).Milliseconds(), req.OSC)
	case <-timer.C:
		answer(AnswerPending)
		go func() {
			r := <-res
			h.logf("%s %d %s %s %dms (after pending) osc=%s", req.Op, req.Len, r.backend, r.answer, h.now().Sub(start).Milliseconds(), req.OSC)
		}()
	}
}

func readLine(br *bufio.Reader) (string, error) {
	var b []byte
	for len(b) <= maxHeader {
		c, err := br.ReadByte()
		if err != nil {
			return "", err
		}
		if c == '\n' {
			return string(b), nil
		}
		b = append(b, c)
	}
	return "", errors.New("header too long")
}

// result is one backend run's outcome.
type result struct {
	answer  string // "ok" or "err <reason>"
	backend string
}

type job struct {
	op   string
	data []byte
	res  chan result
}

// queue is one selection's latest-wins queue: one run at a time, and a newer
// queued request replaces the older queued one, which gets err superseded.
type queue struct {
	running bool
	next    *job
}

func selectionOf(op string) string {
	if op == OpPrimary || op == OpClearPrimary {
		return "primary"
	}
	return "clipboard"
}

func (h *Helper) submit(op string, data []byte) <-chan result {
	j := &job{op: op, data: data, res: make(chan result, 1)}
	sel := selectionOf(op)
	h.mu.Lock()
	if h.queues == nil {
		h.queues = map[string]*queue{}
	}
	q := h.queues[sel]
	if q == nil {
		q = &queue{}
		h.queues[sel] = q
	}
	if q.running {
		if q.next != nil {
			q.next.res <- result{answer: errAnswer(ReasonSuperseded)}
		}
		q.next = j
		h.mu.Unlock()
		return j.res
	}
	q.running = true
	h.mu.Unlock()
	go h.drain(q, j)
	return j.res
}

func (h *Helper) drain(q *queue, j *job) {
	for j != nil {
		j.res <- h.run(j.op, j.data)
		h.mu.Lock()
		j, q.next = q.next, nil
		if j == nil {
			q.running = false
		}
		h.mu.Unlock()
	}
}

// run copies one request's text with the backend the target names.
func (h *Helper) run(op string, data []byte) result {
	if h.Outer != "" {
		return h.forward(op, data)
	}
	fc := h.Files
	fc.UID = h.UID
	t, err := ReadTarget(h.TargetPath, h.Hostname, fc)
	if err != nil {
		if !errors.Is(err, ErrNoTarget) {
			h.logRefusal("target: %v", err)
		}
		return result{answer: errAnswer(ReasonNoDisplay), backend: "none"}
	}
	switch t.Backend {
	case BackendX:
		if err := h.checkXDisplay(t.Display); err != nil {
			h.logRefusal("display %s: %v", t.Display, err)
			return result{answer: errAnswer(ReasonStaleDisplay), backend: "x"}
		}
		return h.runX(t, op, data)
	case BackendWayland:
		if err := fc.ownedSocket(t.WaylandDisplay); err != nil {
			return result{answer: errAnswer(ReasonStaleDisplay), backend: "wayland"}
		}
		args := []string{}
		if selectionOf(op) == "primary" {
			args = append(args, "--primary")
		}
		if op == OpClearClipboard || op == OpClearPrimary {
			args = append(args, "--clear")
		} else {
			args = append(args, "--type", "text/plain;charset=utf-8")
		}
		return h.runTool("wl-copy", args, []string{"WAYLAND_DISPLAY=" + t.WaylandDisplay}, data)
	}
	return result{answer: errAnswer(ReasonNoDisplay), backend: "none"}
}

func (h *Helper) runX(t *Target, op string, data []byte) result {
	key := t.Display + "\x00" + t.XAuthority
	env := []string{"DISPLAY=" + t.Display}
	if t.XAuthority != "" {
		env = append(env, "XAUTHORITY="+t.XAuthority)
	}
	sel := selectionOf(op)
	clear := op == OpClearClipboard || op == OpClearPrimary
	xclip := func() result {
		return h.runTool("xclip", []string{"-selection", sel, "-t", "UTF8_STRING", "-i"}, env, data)
	}
	xsel := func() result {
		args := []string{"--" + sel, "--input"}
		if clear {
			args = []string{"--" + sel, "--clear"}
		}
		return h.runTool("xsel", args, env, data)
	}
	h.mu.Lock()
	if h.sticky == nil {
		h.sticky = map[string]time.Time{}
	}
	// A new target resets the fallback: only this target's entry counts.
	for k := range h.sticky {
		if k != key {
			delete(h.sticky, k)
		}
	}
	xselFirst := h.now().Before(h.sticky[key])
	h.mu.Unlock()
	order := []func() result{xclip, xsel}
	if xselFirst {
		order = []func() result{xsel, xclip}
	}
	var r result
	for i, f := range order {
		r = f()
		if r.answer == AnswerOK {
			if i == 1 && !xselFirst {
				h.mu.Lock()
				h.sticky[key] = h.now().Add(StickyFallback)
				h.mu.Unlock()
				h.logf("fallback: xsel first for %s", StickyFallback)
			}
			return r
		}
	}
	return r
}

func (h *Helper) lookPath(name string) (string, error) {
	if h.LookPath != nil {
		return h.LookPath(name)
	}
	for _, d := range filepath.SplitList(h.Path) {
		if !filepath.IsAbs(d) {
			continue
		}
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found", name)
}

// runTool runs one host copy tool with an environment of ONLY the target's
// variables plus PATH, HOME and LC_ALL=C (env -i: the helper's own display
// variables never leak in), in its own process group, killed after
// BackendTimeout. Stdout and stderr are /dev/null, so xclip's forked serving
// child never holds a pipe this waits on.
func (h *Helper) runTool(name string, args, env []string, data []byte) result {
	tool, err := h.lookPath(name)
	if err != nil {
		return result{answer: errAnswer(ReasonBackend), backend: name}
	}
	argv := []string{"-i", "PATH=" + h.Path, "HOME=" + h.Home, "LC_ALL=C"}
	argv = append(append(append(argv, env...), tool), args...)
	proc, err := h.Runner.Start(execx.Cmd{Name: "/usr/bin/env", Args: argv, Stdin: bytes.NewReader(data)})
	if err != nil {
		return result{answer: errAnswer(ReasonBackend), backend: name}
	}
	done := make(chan error, 1)
	go func() { done <- proc.Wait() }()
	timer := time.NewTimer(BackendTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			return result{answer: errAnswer(ReasonBackend), backend: name}
		}
		return result{answer: AnswerOK, backend: name}
	case <-timer.C:
		if g, ok := proc.(execx.GroupKiller); ok {
			g.KillGroup()
		} else {
			proc.Signal(os.Kill)
		}
		select {
		case <-done:
		case <-time.After(time.Second):
		}
		return result{answer: errAnswer(ReasonTimeout), backend: name}
	}
}

// forward sends a nested helper's request to the outer bridge
// (CS-LNCH-200), in process, and returns the outer result.
func (h *Helper) forward(op string, data []byte) result {
	adm, res, err := Exchange(h.Outer, Request{Op: op, Len: len(data)}, data, ShimTotal)
	switch {
	case err != nil:
		return result{answer: errAnswer(ReasonOuter), backend: "outer"}
	case adm != AnswerAccepted:
		return result{answer: adm, backend: "outer"}
	case res == AnswerPending:
		return result{answer: AnswerOK, backend: "outer"}
	}
	return result{answer: res, backend: "outer"}
}

// checkXDisplay is the stale-display rule before each X run (CS-LNCH-198,
// plan 08 N2): a TCP display needs a LISTEN socket on port 6000+N owned by
// the helper's uid or root, bound to loopback (or, for the machine's own
// name, the wildcard); a unix display needs /tmp/.X11-unix/XN to be a
// socket owned by the uid or root. Residual: X clients also try the
// abstract socket @/tmp/.X11-unix/XN, which has no owner to check.
func (h *Helper) checkXDisplay(d string) error {
	host, num, ok := DisplayHost(d)
	if !ok {
		return errors.New("malformed display")
	}
	n, _ := strconv.Atoi(num)
	if host == "" || host == "unix" {
		x11 := h.X11Dir
		if x11 == "" {
			x11 = "/tmp/.X11-unix"
		}
		p := filepath.Join(x11, "X"+num)
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSocket == 0 {
			return errors.New("not a socket")
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); !ok || (int(st.Uid) != h.UID && st.Uid != 0) {
			return errors.New("socket owned by another user")
		}
		return nil
	}
	wildcardOK := !localHostIsLoopback(host)
	root := h.ProcRoot
	if root == "" {
		root = "/proc"
	}
	ls := ReadListeners(filepath.Join(root, "net/tcp"), filepath.Join(root, "net/tcp6"))
	found := false
	for _, l := range ls {
		if l.Port != 6000+n || !(l.IP.IsLoopback() || (wildcardOK && l.IP.IsUnspecified())) {
			continue
		}
		// Any reachable listener of another non-root user refuses: xclip
		// might connect to it (IPv4 vs IPv6, loopback vs wildcard).
		if l.UID != h.UID && l.UID != 0 {
			return fmt.Errorf("port %d has a listener of uid %d", 6000+n, l.UID)
		}
		found = true
	}
	if found {
		return nil
	}
	return fmt.Errorf("no listener of yours on port %d", 6000+n)
}

// localHostIsLoopback reports whether a display host part names loopback
// (localhost or 127/8) rather than the machine's own name.
func localHostIsLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Listener is one LISTEN row of /proc/net/tcp{,6}.
type Listener struct {
	IP   net.IP
	Port int
	UID  int
}

// ReadListeners parses the LISTEN rows (state 0A) of the given
// /proc/net/tcp-format files; an unreadable file contributes nothing.
func ReadListeners(paths ...string) []Listener {
	var out []Listener
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(b), "\n") {
			if i == 0 {
				continue
			}
			f := strings.Fields(line)
			if len(f) < 8 || f[3] != "0A" {
				continue
			}
			addr, port, ok := strings.Cut(f[1], ":")
			if !ok {
				continue
			}
			pn, err := strconv.ParseUint(port, 16, 16)
			if err != nil {
				continue
			}
			ip := parseProcIP(addr)
			uid, err := strconv.Atoi(f[7])
			if ip == nil || err != nil {
				continue
			}
			out = append(out, Listener{IP: ip, Port: int(pn), UID: uid})
		}
	}
	return out
}

// parseProcIP decodes /proc/net/tcp's address: 32-bit words, each in host
// (little-endian) byte order.
func parseProcIP(s string) net.IP {
	b, err := hex.DecodeString(s)
	if err != nil || (len(b) != 4 && len(b) != 16) {
		return nil
	}
	for i := 0; i < len(b); i += 4 {
		b[i], b[i+1], b[i+2], b[i+3] = b[i+3], b[i+2], b[i+1], b[i]
	}
	return net.IP(b)
}

// Watch blocks until the container is gone (CS-LNCH-198): a die or destroy
// event whose Actor.ID equals the id exactly, the directory gone (checked
// every DirCheck), or a bounded inspect (every InspectEvery, and whenever
// the events stream ends) finding no such container. It returns the reason.
func (h *Helper) Watch() string {
	dirEvery, retry, inspectEvery := h.DirCheck, h.EventsRetry, h.InspectEvery
	if dirEvery == 0 {
		dirEvery = 30 * time.Second
	}
	if retry == 0 {
		retry = 5 * time.Second
	}
	if inspectEvery == 0 {
		inspectEvery = 5 * time.Minute
	}
	gone := make(chan string, 4)
	stopped := make(chan struct{})
	// The events child: started with DieWithParent (its own group, killed
	// if the helper dies) and its group killed on every return below, so
	// it never outlives the watch (CS-LNCH-198).
	var (
		procMu  sync.Mutex
		current execx.Process
		closed  bool
	)
	watcherDone := make(chan struct{})
	defer func() {
		procMu.Lock()
		closed = true
		p := current
		procMu.Unlock()
		if p != nil {
			killGroup(p)
		}
		close(stopped)
		// The watcher ends promptly once its child is gone; nothing it
		// started is left behind when Watch returns.
		<-watcherDone
	}()
	go func() {
		defer close(watcherDone)
		for {
			// An OS pipe, not io.Pipe: its kernel buffer takes a write made
			// before the reader starts (a runner that writes at Start).
			pr, pw, perr := os.Pipe()
			if perr != nil {
				gone <- "cannot watch: " + perr.Error()
				return
			}
			procMu.Lock()
			if closed {
				procMu.Unlock()
				pr.Close()
				pw.Close()
				return
			}
			proc, err := h.Runner.Start(execx.Cmd{Name: "docker", Args: []string{
				"events", "--filter", "container=" + h.ref(), "--filter", "event=die", "--filter", "event=destroy",
				"--format", "{{.Actor.ID}} {{.Action}} {{.Actor.Attributes.name}}"}, Stdout: pw, DieWithParent: true})
			if err == nil {
				current = proc
			}
			procMu.Unlock()
			if err == nil {
				go func() { proc.Wait(); pw.Close() }()
				sc := bufio.NewScanner(pr)
				for sc.Scan() {
					f := strings.Fields(sc.Text())
					if len(f) < 2 {
						continue
					}
					id, action, name := f[0], f[1], ""
					if len(f) > 2 {
						name = f[2]
					}
					// docker's container= filter is a prefix match: keep
					// only the exact id, else the exact name (CS-LNCH-095).
					exact := (h.ID != "" && id == h.ID) || (h.ID == "" && name == h.Container)
					if exact && (action == "die" || action == "destroy") {
						gone <- "container " + action
						return
					}
				}
			} else {
				pw.Close()
			}
			pr.Close()
			procMu.Lock()
			current = nil
			done := closed
			procMu.Unlock()
			if done {
				return
			}
			if !h.containerExists() {
				gone <- "container gone"
				return
			}
			select {
			case <-stopped:
				return
			case <-time.After(retry):
			}
		}
	}()
	dirTick := time.NewTicker(dirEvery)
	defer dirTick.Stop()
	inspTick := time.NewTicker(inspectEvery)
	defer inspTick.Stop()
	for {
		select {
		case why := <-gone:
			return why
		case <-h.Done():
			return "stopped"
		case <-dirTick.C:
			if _, err := os.Lstat(h.Dir); err != nil {
				return "directory gone"
			}
		case <-inspTick.C:
			if !h.containerExists() {
				return "container gone"
			}
		}
	}
}

// killGroup SIGKILLs a process's whole group (a real tethered process leads
// its own), else the process.
func killGroup(p execx.Process) {
	if g, ok := p.(execx.GroupKiller); ok {
		g.KillGroup()
		return
	}
	p.Signal(os.Kill)
}

// containerExists runs one bounded inspect; only docker's "No such ..."
// answer counts as gone (a docker hiccup keeps the helper).
func (h *Helper) containerExists() bool {
	var stderr bytes.Buffer
	_, err := execx.Bounded(h.Runner, 10*time.Second, execx.Cmd{Name: "docker",
		Args: []string{"inspect", "--type", "container", "--format", "{{.Id}}", h.ref()}, Stderr: &stderr})
	if err == nil {
		return true
	}
	s := stderr.String()
	return !(strings.Contains(s, "No such container") || strings.Contains(s, "No such object"))
}
