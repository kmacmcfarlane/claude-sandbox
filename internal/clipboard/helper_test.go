package clipboard_test

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/clipboard"
	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
)

type helperFixture struct {
	root, dir, sock, target, auth, proc string
	fake                                *execx.Fake
	h                                   *clipboard.Helper
	now                                 time.Time
	mu                                  sync.Mutex
}

// tcpRow renders one /proc/net/tcp LISTEN row.
func tcpRow(addr string, port, uid int) string {
	return fmt.Sprintf("   0: %s:%04X 00000000:0000 0A 00000000:00000000 00:00000000 00000000 %5d        0 1 1\n", addr, port, uid)
}

const tcpHeader = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"

func newHelperFixture() *helperFixture {
	f := &helperFixture{root: shortDir(), fake: &execx.Fake{}, now: time.Unix(1_800_000_000, 0)}
	f.dir = filepath.Join(f.root, "clipboard")
	Expect(os.Mkdir(f.dir, 0o700)).To(Succeed())
	f.sock = filepath.Join(f.dir, clipboard.SocketName)
	f.target = filepath.Join(f.root, clipboard.TargetFile)
	f.auth = filepath.Join(f.root, "auth")
	Expect(os.WriteFile(f.auth, []byte("cookie"), 0o600)).To(Succeed())
	f.proc = filepath.Join(f.root, "proc")
	Expect(os.MkdirAll(filepath.Join(f.proc, "net"), 0o755)).To(Succeed())
	// sshd's forwarded display :10, listening on 127.0.0.1 as this user.
	f.setTCP(tcpHeader+tcpRow("0100007F", 6010, os.Getuid()), tcpHeader)
	f.h = &clipboard.Helper{
		Dir: f.dir, TargetPath: f.target, Container: "c", ID: strings.Repeat("a", 64),
		Runner: f.fake, UID: os.Getuid(), Hostname: "hooper", Home: "/h", Path: "/usr/bin",
		LookPath: func(n string) (string, error) { return "/usr/bin/" + n, nil },
		ProcRoot: f.proc,
		Now: func() time.Time {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.now
		},
	}
	return f
}

func (f *helperFixture) setTCP(tcp, tcp6 string) {
	Expect(os.WriteFile(filepath.Join(f.proc, "net/tcp"), []byte(tcp), 0o644)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(f.proc, "net/tcp6"), []byte(tcp6), 0o644)).To(Succeed())
}

func (f *helperFixture) setTarget(t clipboard.Target) {
	t.V = 1
	Expect(clipboard.WriteTarget(f.target, t)).To(Succeed())
}

func (f *helperFixture) xTarget(display string) {
	f.setTarget(clipboard.Target{Backend: "x", Display: display, XAuthority: f.auth, By: "create"})
}

func (f *helperFixture) start() {
	lock, err := f.h.Listen()
	Expect(err).NotTo(HaveOccurred())
	go f.h.Serve()
	DeferCleanup(func() {
		f.h.Stop()
		lock.Close()
	})
}

func (f *helperFixture) copy(op, text string) (string, string) {
	adm, res, err := clipboard.Exchange(f.sock, clipboard.Request{Op: op, Len: len(text), OSC: "tty"}, []byte(text), 3*time.Second)
	Expect(err).NotTo(HaveOccurred())
	return adm, res
}

func (f *helperFixture) toolCalls() []string {
	var out []string
	for _, l := range f.fake.CommandLines() {
		if strings.HasPrefix(l, "/usr/bin/env ") {
			out = append(out, l)
		}
	}
	return out
}

var _ = Describe("the clipboard helper (CS-LNCH-198)", func() {
	It("CS-LNCH-198: binds bridge.sock 0600 under a single-instance lock", func() {
		f := newHelperFixture()
		f.start()
		fi, err := os.Stat(f.sock)
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		_, err = (&clipboard.Helper{Dir: f.dir, UID: os.Getuid()}).Listen()
		Expect(err).To(MatchError(clipboard.ErrHeld))
		Expect(clipboard.Ping(f.sock, time.Second)).To(BeTrue())
		Expect(f.toolCalls()).To(BeEmpty(), "ping runs nothing")
	})

	It("CS-LNCH-198: a copy runs xclip with ONLY the target's environment (a decoy DISPLAY never leaks)", func() {
		os.Setenv("DISPLAY", ":99")
		DeferCleanup(os.Unsetenv, "DISPLAY")
		f := newHelperFixture()
		f.xTarget("localhost:10.0")
		f.start()
		adm, res := f.copy("clipboard", "hello")
		Expect(adm).To(Equal("accepted"))
		Expect(res).To(Equal("ok"))
		Expect(f.toolCalls()).To(Equal([]string{
			"/usr/bin/env -i PATH=/usr/bin HOME=/h LC_ALL=C DISPLAY=localhost:10.0 XAUTHORITY=" + f.auth +
				" /usr/bin/xclip -selection clipboard -t UTF8_STRING -i"}))
		Expect(strings.Join(f.toolCalls(), " ")).NotTo(ContainSubstring(":99"))
		f.copy("primary", "p")
		Expect(f.toolCalls()[1]).To(HaveSuffix("xclip -selection primary -t UTF8_STRING -i"))
	})

	It("CS-LNCH-198: payload refusals are admission answers; nothing runs", func() {
		f := newHelperFixture()
		f.xTarget("localhost:10")
		f.start()
		adm, _ := f.copy("clipboard", "\x1b[201~")
		Expect(adm).To(Equal("err control-char"))
		adm, _ = f.copy("clipboard", "\xff")
		Expect(adm).To(Equal("err utf8"))
		c, err := net.Dial("unix", f.sock)
		Expect(err).NotTo(HaveOccurred())
		fmt.Fprintf(c, "CSCB1 clipboard %d\n", clipboard.MaxCopyBytes+1)
		buf := make([]byte, 64)
		n, _ := c.Read(buf)
		c.Close()
		Expect(string(buf[:n])).To(Equal("err too-large\n"))
		adm, _ = f.copy("paste", "")
		Expect(adm).To(Equal("err bad-request"))
		Expect(f.toolCalls()).To(BeEmpty())
	})

	It("CS-LNCH-198: the token bucket admits 20 in a burst, then refills 2 per second", func() {
		f := newHelperFixture()
		f.xTarget("localhost:10")
		f.start()
		accepted, limited := 0, 0
		for i := 0; i < 25; i++ {
			switch adm, _ := f.copy("clipboard", "x"); adm {
			case "accepted":
				accepted++
			case "err rate-limited":
				limited++
			}
		}
		Expect([]int{accepted, limited}).To(Equal([]int{20, 5}))
		f.mu.Lock()
		f.now = f.now.Add(time.Second)
		f.mu.Unlock()
		a1, _ := f.copy("clipboard", "x")
		a2, _ := f.copy("clipboard", "x")
		a3, _ := f.copy("clipboard", "x")
		Expect([]string{a1, a2, a3}).To(Equal([]string{"accepted", "accepted", "err rate-limited"}))
		Expect(clipboard.Ping(f.sock, time.Second)).To(BeTrue(), "ping takes no token")
	})

	It("CS-LNCH-198: no target, a none target or an invalid one answers err no-display without a process", func() {
		f := newHelperFixture()
		f.start()
		_, res := f.copy("clipboard", "x")
		Expect(res).To(Equal("err no-display"))
		f.setTarget(clipboard.Target{Backend: "none", By: "attach"})
		_, res = f.copy("clipboard", "x")
		Expect(res).To(Equal("err no-display"))
		f.setTarget(clipboard.Target{Backend: "x", Display: "elsewhere:0", By: "attach"})
		_, res = f.copy("clipboard", "x")
		Expect(res).To(Equal("err no-display"))
		Expect(f.toolCalls()).To(BeEmpty())
	})

	It("CS-LNCH-198: xclip failing makes xsel first for 5 minutes within that target", func() {
		f := newHelperFixture()
		f.fake.On("/usr/bin/xclip", "", execx.Fail(1))
		f.xTarget("localhost:10")
		f.start()
		_, res := f.copy("clipboard", "x")
		Expect(res).To(Equal("ok"))
		_, res = f.copy("clipboard", "y")
		Expect(res).To(Equal("ok"))
		calls := f.toolCalls()
		Expect(calls).To(HaveLen(3))
		Expect(calls[0]).To(ContainSubstring("/usr/bin/xclip"))
		Expect(calls[1]).To(HaveSuffix("/usr/bin/xsel --clipboard --input"))
		Expect(calls[2]).To(ContainSubstring("/usr/bin/xsel"), "sticky")
		f.mu.Lock()
		f.now = f.now.Add(clipboard.StickyFallback + time.Second)
		f.mu.Unlock()
		f.copy("clipboard", "z")
		Expect(f.toolCalls()[3]).To(ContainSubstring("/usr/bin/xclip"), "retried from the top")
	})

	It("CS-LNCH-198: a TCP display needs a loopback LISTEN socket on 6000+N owned by the uid or root", func() {
		f := newHelperFixture()
		f.start()
		f.xTarget("localhost:10")
		_, res := f.copy("clipboard", "x")
		Expect(res).To(Equal("ok"), "own listener on 127.0.0.1:6010")

		f.setTCP(tcpHeader+tcpRow("0100007F", 6010, os.Getuid()+1), tcpHeader)
		_, res = f.copy("clipboard", "x")
		Expect(res).To(Equal("err stale-display"), "another user's listener")

		f.setTCP(tcpHeader, tcpHeader)
		_, res = f.copy("clipboard", "x")
		Expect(res).To(Equal("err stale-display"), "no listener")

		f.setTCP(tcpHeader+tcpRow("0100007F", 6010, 0), tcpHeader)
		_, res = f.copy("clipboard", "x")
		Expect(res).To(Equal("ok"), "root-owned listener accepted")

		f.setTCP(tcpHeader, tcpHeader+tcpRow("00000000000000000000000001000000", 6010, os.Getuid()))
		_, res = f.copy("clipboard", "x")
		Expect(res).To(Equal("ok"), "::1 in tcp6")

		f.setTCP(tcpHeader+tcpRow("0100007F", 6010, os.Getuid()), tcpHeader+tcpRow("00000000000000000000000001000000", 6010, os.Getuid()+1))
		_, res = f.copy("clipboard", "x")
		Expect(res).To(Equal("err stale-display"), "any reachable listener of another non-root uid refuses")

		f.setTCP(tcpHeader+tcpRow("00000000", 6010, os.Getuid()), tcpHeader)
		_, res = f.copy("clipboard", "x")
		Expect(res).To(Equal("err stale-display"), "a wildcard does not count for localhost")
		f.xTarget("hooper:10")
		_, res = f.copy("clipboard", "x")
		Expect(res).To(Equal("ok"), "a wildcard counts for the machine's own name")
	})

	It("CS-LNCH-198: a unix display needs /tmp/.X11-unix/XN to be a socket of the uid (or root)", func() {
		f := newHelperFixture()
		x11 := filepath.Join(f.root, "x11")
		Expect(os.Mkdir(x11, 0o755)).To(Succeed())
		f.h.X11Dir = x11
		f.start()
		f.xTarget(":0")
		_, res := f.copy("clipboard", "x")
		Expect(res).To(Equal("err stale-display"), "missing")
		Expect(os.WriteFile(filepath.Join(x11, "X0"), nil, 0o600)).To(Succeed())
		_, res = f.copy("clipboard", "x")
		Expect(res).To(Equal("err stale-display"), "not a socket")
		Expect(os.Remove(filepath.Join(x11, "X0"))).To(Succeed())
		l, err := net.Listen("unix", filepath.Join(x11, "X0"))
		Expect(err).NotTo(HaveOccurred())
		defer l.Close()
		_, res = f.copy("clipboard", "x")
		Expect(res).To(Equal("ok"))
	})

	It("CS-LNCH-198: a Wayland target runs wl-copy with the forced text type", func() {
		f := newHelperFixture()
		l, err := net.Listen("unix", filepath.Join(f.root, "wayland-0"))
		Expect(err).NotTo(HaveOccurred())
		defer l.Close()
		f.setTarget(clipboard.Target{Backend: "wayland", WaylandDisplay: filepath.Join(f.root, "wayland-0"), By: "create"})
		f.start()
		_, res := f.copy("primary", "x")
		Expect(res).To(Equal("ok"))
		Expect(f.toolCalls()[0]).To(HaveSuffix("WAYLAND_DISPLAY=" + filepath.Join(f.root, "wayland-0") + " /usr/bin/wl-copy --primary --type text/plain;charset=utf-8"))
	})

	It("CS-LNCH-198: a peer of another uid is refused (rootless docker fails closed)", func() {
		f := newHelperFixture()
		f.h.PeerUID = func(*net.UnixConn) (int, error) { return 0, nil }
		f.start()
		adm, _, err := clipboard.Exchange(f.sock, clipboard.Request{Op: "ping"}, nil, time.Second)
		Expect(err).NotTo(HaveOccurred())
		Expect(adm).To(Equal("err peer"))
	})

	It("CS-LNCH-198: past 4 concurrent connections a new one gets err busy", func() {
		f := newHelperFixture()
		f.start()
		held := make([]net.Conn, clipboard.MaxConns)
		defer func() {
			for _, c := range held {
				if c != nil {
					c.Close()
				}
			}
		}()
		// Accept order is not dial order: a held connection may itself be
		// the one answered busy (and closed), so top the held set up until
		// all four are being served and a fifth is refused.
		Eventually(func() string {
			for i, c := range held {
				if c != nil {
					c.SetReadDeadline(time.Now().Add(5 * time.Millisecond))
					if _, err := c.Read(make([]byte, 16)); err == nil || !os.IsTimeout(err) {
						c.Close()
						c = nil
					}
				}
				if c == nil {
					nc, err := net.Dial("unix", f.sock)
					Expect(err).NotTo(HaveOccurred())
					held[i] = nc
				}
			}
			time.Sleep(20 * time.Millisecond)
			adm, _, _ := clipboard.Exchange(f.sock, clipboard.Request{Op: "ping"}, nil, time.Second)
			return adm
		}, 3*time.Second).Should(Equal("err busy"))
	})

	It("CS-LNCH-198: exits on the die event of the exact id, never a prefix match", func() {
		f := newHelperFixture()
		id := f.h.ID
		f.fake.On("docker events", id[:12]+"ffff die c-other\n"+id+" die c\n", nil)
		f.h.DirCheck, f.h.InspectEvery, f.h.EventsRetry = time.Hour, time.Hour, time.Millisecond
		Expect(f.h.Watch()).To(Equal("container die"))

		g := newHelperFixture()
		g.fake.On("docker events", g.h.ID[:12]+"ffff destroy c\n", nil)
		g.fake.OnFunc("docker inspect", func(c execx.Cmd) (string, error) {
			fmt.Fprint(c.Stderr, "Error: No such container: x")
			return "", execx.Fail(1)
		})
		g.h.DirCheck, g.h.InspectEvery, g.h.EventsRetry = time.Hour, time.Hour, time.Millisecond
		Expect(g.h.Watch()).To(Equal("container gone"), "the prefix event is ignored; the inspect decides")
	})

	It("CS-LNCH-198: the docker events child is tethered and killed when the watch returns", func() {
		f := newHelperFixture()
		f.fake.OnHang("docker events")
		f.h.DirCheck, f.h.InspectEvery, f.h.EventsRetry = 10*time.Millisecond, time.Hour, time.Hour
		Expect(os.RemoveAll(f.dir)).To(Succeed())
		Expect(f.h.Watch()).To(Equal("directory gone"))
		Eventually(func() int { return f.fake.Killed }).Should(Equal(1))
		var events []execx.Cmd
		for _, c := range f.fake.Calls {
			if c.Name == "docker" && len(c.Args) > 0 && c.Args[0] == "events" {
				events = append(events, c)
			}
		}
		Expect(events).To(HaveLen(1))
		Expect(events[0].DieWithParent).To(BeTrue())
	})

	It("CS-LNCH-200: a nested helper forwards to the outer bridge and ignores any target", func() {
		outer := newHelperFixture()
		outer.xTarget("localhost:10")
		outer.start()
		inner := newHelperFixture()
		inner.h.Outer = outer.sock
		inner.h.TargetPath = ""
		inner.start()
		adm, res := inner.copy("clipboard", "via outer")
		Expect(adm).To(Equal("accepted"))
		Expect(res).To(Equal("ok"))
		Expect(inner.toolCalls()).To(BeEmpty())
		Expect(outer.toolCalls()).To(HaveLen(1))
	})
})
