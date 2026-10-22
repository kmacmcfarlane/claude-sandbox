package clipboard_test

import (
	"bufio"
	"bytes"
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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/clipboard"
)

// fakeBridge answers every request with adm and, when adm is accepted, res.
// adm "" closes the connection without answering (no admission in time).
type fakeBridge struct {
	sock     string
	mu       sync.Mutex
	adm, res string
	headers  []string
	payloads []string
}

func newFakeBridge(adm, res string) *fakeBridge {
	b := &fakeBridge{sock: filepath.Join(shortDir(), "bridge.sock"), adm: adm, res: res}
	l, err := net.Listen("unix", b.sock)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(l.Close)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				line, _ := br.ReadString('\n')
				req, _ := clipboard.ParseRequest(strings.TrimSpace(line))
				data := make([]byte, req.Len)
				io.ReadFull(br, data)
				b.mu.Lock()
				b.headers = append(b.headers, strings.TrimSpace(line))
				b.payloads = append(b.payloads, string(data))
				adm, res := b.adm, b.res
				b.mu.Unlock()
				if adm == "" {
					time.Sleep(clipboard.ShimAdmission + 100*time.Millisecond)
					return
				}
				fmt.Fprintln(c, adm)
				if adm == "accepted" {
					fmt.Fprintln(c, res)
				}
			}(c)
		}
	}()
	return b
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

type shimFixture struct {
	tty    *bytes.Buffer
	stderr *bytes.Buffer
	env    map[string]string
	guard  string
	noTTY  bool
}

func newShimFixture(sock string) *shimFixture {
	return &shimFixture{tty: &bytes.Buffer{}, stderr: &bytes.Buffer{},
		env:   map[string]string{"CLAUDE_SANDBOX_CLIPBOARD": sock},
		guard: filepath.Join(shortDir(), "guard")}
}

func (f *shimFixture) shim(stdin string, args ...string) *clipboard.Shim {
	uid := os.Getuid()
	return &clipboard.Shim{
		Args: args, Stdin: strings.NewReader(stdin), Stderr: f.stderr,
		Getenv: func(k string) string { return f.env[k] },
		OpenTTY: func() (io.WriteCloser, error) {
			if f.noTTY {
				return nil, errors.New("no tty")
			}
			return nopCloser{f.tty}, nil
		},
		ProcRoot: filepath.Join(shortDir(), "proc-none"), Getppid: func() int { return 4242 }, UID: &uid,
		GuardPath: f.guard,
	}
}

func osc(s string) string { return string(clipboard.OSCSequence([]byte(s))) }

var _ = Describe("the wl-copy shim (CS-LNCH-199)", func() {
	It("CS-LNCH-199: accepted: the OSC 52 sequence goes to the terminal, then ok exits 0", func() {
		b := newFakeBridge("accepted", "ok")
		f := newShimFixture(b.sock)
		Expect(f.shim("hi\n").Run()).To(Equal(0))
		Expect(f.tty.String()).To(Equal("\x1b]52;c;aGkK\a"))
		Expect(b.headers).To(Equal([]string{"CSCB1 clipboard 3 osc=tty"}))
		Expect(b.payloads).To(Equal([]string{"hi\n"}))
	})

	It("CS-LNCH-199: the argument table", func() {
		b := newFakeBridge("accepted", "ok")
		f := newShimFixture(b.sock)
		Expect(f.shim("x\n", "-n").Run()).To(Equal(0))
		Expect(f.shim("", "-p", "a", "b").Run()).To(Equal(0))
		Expect(f.shim("", "--clear").Run()).To(Equal(0))
		Expect(f.shim("", "-c", "--primary").Run()).To(Equal(0))
		Expect(f.shim("y", "--type", "text/plain;charset=utf-8", "-f", "-o", "--paste-once", "--foreground").Run()).To(Equal(0))
		Expect(f.shim("y", "--type=text/plain").Run()).To(Equal(0))
		Expect(f.shim("", "--", "-x").Run()).To(Equal(0))
		Expect(b.headers).To(Equal([]string{
			"CSCB1 clipboard 1 osc=tty", "CSCB1 primary 3", "CSCB1 clear-clipboard 0", "CSCB1 clear-primary 0",
			"CSCB1 clipboard 1 osc=tty", "CSCB1 clipboard 1 osc=tty", "CSCB1 clipboard 2 osc=tty"}))
		Expect(b.payloads[1]).To(Equal("a b"))
		Expect(b.payloads[6]).To(Equal("-x"))
		Expect(f.tty.String()).To(Equal(osc("x")+osc("y")+osc("y")+osc("-x")), "never for PRIMARY or a clear")

		Expect(f.shim("y", "-t", "image/png").Run()).To(Equal(1))
		Expect(f.shim("y", "--regular").Run()).To(Equal(2))
		Expect(f.shim("y", "-t").Run()).To(Equal(2))
		Expect(len(b.headers)).To(Equal(7), "nothing sent for a bad argument")
	})

	It("CS-LNCH-199: an invalid payload exits 1 and writes nothing anywhere", func() {
		b := newFakeBridge("accepted", "ok")
		f := newShimFixture(b.sock)
		Expect(f.shim("\x1b[201~").Run()).To(Equal(1))
		Expect(f.shim(strings.Repeat("a", clipboard.MaxCopyBytes+1)).Run()).To(Equal(1))
		Expect(f.tty.Len()).To(BeZero())
		Expect(b.headers).To(BeEmpty())
	})

	It("CS-LNCH-199: refusals write no OSC 52 and exit 1", func() {
		for _, adm := range []string{"err rate-limited", "err too-large", "err control-char", "err utf8"} {
			b := newFakeBridge(adm, "")
			f := newShimFixture(b.sock)
			Expect(f.shim("x").Run()).To(Equal(1), adm)
			Expect(f.tty.Len()).To(BeZero(), adm)
		}
	})

	It("CS-LNCH-199: any other admission error (peer, bad-request, an unknown answer) is treated as unreachable", func() {
		for _, adm := range []string{"err peer", "err bad-request", "what"} {
			b := newFakeBridge(adm, "")
			f := newShimFixture(b.sock)
			Expect(f.shim("x").Run()).To(Equal(0), adm)
			Expect(f.tty.String()).To(Equal(osc("x")), adm)
			Expect(f.stderr.String()).To(ContainSubstring("over OSC 52 only"), adm)
			data, _ := os.ReadFile(f.guard)
			Expect(strings.Fields(string(data))).To(HaveLen(1), "under the guard")
		}
	})

	It("CS-LNCH-199: err no-display after the write exits 0; another result error exits 1", func() {
		b := newFakeBridge("accepted", "err no-display")
		f := newShimFixture(b.sock)
		Expect(f.shim("x").Run()).To(Equal(0))
		Expect(f.tty.String()).To(Equal(osc("x")))
		b.res = "err stale-display"
		Expect(f.shim("x").Run()).To(Equal(1))
		b.res = "pending"
		Expect(f.shim("x").Run()).To(Equal(0))
		f.noTTY = true
		b.res = "err no-display"
		Expect(f.shim("x").Run()).To(Equal(1), "no terminal found: nothing was written")
	})

	It("CS-LNCH-199: unreachable or busy writes under the guard (10 per 10 s), exit 0 with a note; PRIMARY exits 1", func() {
		f := newShimFixture(filepath.Join(shortDir(), "missing.sock"))
		now := time.Unix(1_800_000_000, 0)
		for i := 0; i < clipboard.GuardWrites; i++ {
			s := f.shim("x")
			s.Now = func() time.Time { return now }
			Expect(s.Run()).To(Equal(0))
		}
		Expect(f.stderr.String()).To(ContainSubstring("over OSC 52 only"))
		Expect(f.tty.String()).To(Equal(strings.Repeat(osc("x"), clipboard.GuardWrites)))
		s := f.shim("x")
		s.Now = func() time.Time { return now }
		Expect(s.Run()).To(Equal(1), "the guard is full")
		Expect(f.tty.String()).To(Equal(strings.Repeat(osc("x"), clipboard.GuardWrites)))
		s = f.shim("x")
		s.Now = func() time.Time { return now.Add(clipboard.GuardWindow) }
		Expect(s.Run()).To(Equal(0), "the window moved on")
		Expect(f.shim("x", "-p").Run()).To(Equal(1))

		busy := newFakeBridge("err busy", "")
		g := newShimFixture(busy.sock)
		Expect(g.shim("y").Run()).To(Equal(0))
		Expect(g.tty.String()).To(Equal(osc("y")), "busy says nothing about this copy (plan 08 N3)")
		data, _ := os.ReadFile(g.guard)
		Expect(strings.Fields(string(data))).To(HaveLen(1), "the guard counter rose")

		silent := newFakeBridge("", "")
		h := newShimFixture(silent.sock)
		Expect(h.shim("z").Run()).To(Equal(0), "no admission within 300 ms")
		Expect(h.tty.String()).To(Equal(osc("z")))
	})

	It("CS-LNCH-199: OSC 52 only up to 256 KiB raw; above it the copy goes to the helper only", func() {
		b := newFakeBridge("accepted", "ok")
		f := newShimFixture(b.sock)
		Expect(f.shim(strings.Repeat("a", clipboard.MaxOSCBytes)).Run()).To(Equal(0))
		Expect(f.tty.Len()).To(Equal(len(osc(strings.Repeat("a", clipboard.MaxOSCBytes)))))
		f.tty.Reset()
		Expect(f.shim(strings.Repeat("a", clipboard.MaxOSCBytes+1)).Run()).To(Equal(0))
		Expect(f.tty.Len()).To(BeZero())
		Expect(b.headers[1]).To(Equal(fmt.Sprintf("CSCB1 clipboard %d osc=none", clipboard.MaxOSCBytes+1)))
	})

	It("CS-LNCH-199: a short write loops to a complete sequence; a SIGTERM held during it exits 143 after", func() {
		b := newFakeBridge("accepted", "ok")
		f := newShimFixture(b.sock)
		s := f.shim("hello")
		var held chan<- os.Signal
		s.Notify = func(c chan<- os.Signal, _ ...os.Signal) { held = c }
		s.StopNotify = func(chan<- os.Signal) {}
		w := &shortWriter{onFirst: func() { held <- syscall.SIGTERM }}
		s.OpenTTY = func() (io.WriteCloser, error) { return nopCloser{w}, nil }
		Expect(s.Run()).To(Equal(143))
		Expect(w.buf.String()).To(Equal(osc("hello")))
		Expect(w.writes).To(BeNumerically(">", 1))
	})

	It("CS-LNCH-199: without the variable it execs the next real wl-copy on PATH", func() {
		dir := shortDir()
		self := filepath.Join(dir, "self", "wl-copy")
		other := filepath.Join(dir, "other", "wl-copy")
		for _, p := range []string{self, other} {
			Expect(os.MkdirAll(filepath.Dir(p), 0o755)).To(Succeed())
			Expect(os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755)).To(Succeed())
		}
		var got []string
		stderr := &bytes.Buffer{}
		s := &clipboard.Shim{Args: []string{"-p"}, Stderr: stderr,
			Getenv: func(k string) string {
				if k == "PATH" {
					return filepath.Dir(self) + ":" + filepath.Dir(other)
				}
				return ""
			},
			Self: func() (string, error) { return self, nil },
			Exec: func(p string, argv, env []string) error {
				got = append([]string{p}, argv...)
				for _, e := range env {
					Expect(e).NotTo(HavePrefix("CLAUDE_SANDBOX_CLIPBOARD="))
				}
				return nil
			}}
		Expect(s.Run()).To(Equal(0))
		Expect(got).To(Equal([]string{other, "wl-copy", "-p"}))

		s.Getenv = func(k string) string {
			if k == "PATH" {
				return filepath.Dir(self)
			}
			return ""
		}
		s.Exec = func(string, []string, []string) error {
			Fail("no other wl-copy: nothing to exec")
			return nil
		}
		tty := &bytes.Buffer{}
		s.Args, s.Stdin = nil, strings.NewReader("copied")
		s.OpenTTY = func() (io.WriteCloser, error) { return nopCloser{tty}, nil }
		s.GuardPath = filepath.Join(dir, "guard")
		Expect(s.Run()).To(Equal(0))
		Expect(tty.String()).To(Equal(osc("copied")))
	})

	It("CS-LNCH-199: WAYLAND_DISPLAY from an env file, no socket and no other wl-copy: OSC 52 under the guard, exit 0", func() {
		dir := shortDir()
		tty, stderr := &bytes.Buffer{}, &bytes.Buffer{}
		env := map[string]string{"WAYLAND_DISPLAY": "wayland-0", "PATH": dir}
		uid := os.Getuid()
		shim := func(stdin string, args ...string) *clipboard.Shim {
			return &clipboard.Shim{Args: args, Stdin: strings.NewReader(stdin), Stderr: stderr,
				Getenv:    func(k string) string { return env[k] },
				OpenTTY:   func() (io.WriteCloser, error) { return nopCloser{tty}, nil },
				GuardPath: filepath.Join(dir, "guard"), UID: &uid,
				Self: func() (string, error) { return "/nowhere/wl-copy", nil }}
		}
		Expect(shim("hi").Run()).To(Equal(0))
		Expect(tty.String()).To(Equal(osc("hi")))
		Expect(shim("hi", "--primary").Run()).To(Equal(1))
		Expect(stderr.String()).To(ContainSubstring("no clipboard bridge in this session"))
		Expect(shim(strings.Repeat("a", clipboard.MaxOSCBytes+1)).Run()).To(Equal(1))
		Expect(shim("\x1b").Run()).To(Equal(1))
		Expect(tty.String()).To(Equal(osc("hi")), "nothing more written")
		data, _ := os.ReadFile(filepath.Join(dir, "guard"))
		Expect(strings.Fields(string(data))).To(HaveLen(1), "under the guard")
	})
})

type shortWriter struct {
	buf     bytes.Buffer
	writes  int
	onFirst func()
}

func (w *shortWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == 1 {
		w.onFirst()
		n := len(p) / 2
		w.buf.Write(p[:n])
		return n, syscall.EINTR
	}
	return w.buf.Write(p)
}

// fakeProc builds a /proc tree: pids[i] has parent pids[i+1], fds pointing
// at the given device files, and the first pid's tty_nr.
func fakeProc(ttyNr uint64, chain []int, fds map[int]string) string {
	root := shortDir()
	for i, pid := range chain {
		parent := 1
		if i+1 < len(chain) {
			parent = chain[i+1]
		}
		d := filepath.Join(root, strconv.Itoa(pid))
		Expect(os.MkdirAll(filepath.Join(d, "fd"), 0o755)).To(Succeed())
		nr := uint64(0)
		if i == 0 {
			nr = ttyNr
		}
		stat := fmt.Sprintf("%d (cla ude)) S %d %d %d %d -1 4194560 0 0", pid, parent, pid, pid, nr)
		Expect(os.WriteFile(filepath.Join(d, "stat"), []byte(stat), 0o644)).To(Succeed())
		if dev, ok := fds[pid]; ok {
			Expect(os.Symlink(dev, filepath.Join(d, "fd", "1"))).To(Succeed())
		}
	}
	return root
}

// devNullTTY is /dev/null's rdev (1:3) encoded as tty_nr.
const devNullTTY = 1<<8 | 3

var _ = Describe("the shim's terminal (CS-LNCH-199)", func() {
	It("CS-LNCH-199: the ancestor walk: claude at level 1 matches", func() {
		root := fakeProc(devNullTTY, []int{50}, map[int]string{50: "/dev/null"})
		f, path := clipboard.FindAncestorTTY(root, 50, os.Getuid())
		Expect(f).NotTo(BeNil())
		f.Close()
		Expect(path).To(Equal("ancestor:1:1"))
	})

	It("CS-LNCH-199: a tty_nr of 0 gives none; an fd whose rdev differs is skipped; a higher level matches", func() {
		root := fakeProc(0, []int{50}, map[int]string{50: "/dev/null"})
		f, _ := clipboard.FindAncestorTTY(root, 50, os.Getuid())
		Expect(f).To(BeNil())

		root = fakeProc(devNullTTY, []int{50, 40}, map[int]string{50: "/dev/zero", 40: "/dev/null"})
		f, path := clipboard.FindAncestorTTY(root, 50, os.Getuid())
		Expect(f).NotTo(BeNil())
		f.Close()
		Expect(path).To(Equal("ancestor:2:1"))
	})

	It("CS-LNCH-199: the walk stops at PID 1, at a process of another uid, and after 4 levels", func() {
		root := fakeProc(devNullTTY, []int{50, 1}, map[int]string{1: "/dev/null"})
		f, _ := clipboard.FindAncestorTTY(root, 50, os.Getuid())
		Expect(f).To(BeNil(), "never PID 1 (docker-init, root)")

		root = fakeProc(devNullTTY, []int{50}, map[int]string{50: "/dev/null"})
		f, _ = clipboard.FindAncestorTTY(root, 50, os.Getuid()+1)
		Expect(f).To(BeNil(), "another uid's process")

		root = fakeProc(devNullTTY, []int{60, 59, 58, 57, 56}, map[int]string{56: "/dev/null"})
		f, _ = clipboard.FindAncestorTTY(root, 60, os.Getuid())
		Expect(f).To(BeNil(), "level 5 is past the bound")
		root = fakeProc(devNullTTY, []int{60, 59, 58, 57}, map[int]string{57: "/dev/null"})
		f, path := clipboard.FindAncestorTTY(root, 60, os.Getuid())
		Expect(f).NotTo(BeNil())
		f.Close()
		Expect(path).To(Equal("ancestor:4:1"))
	})

	It("CS-LNCH-199: with no /dev/tty and nothing found, no OSC 52 is written and osc=none is sent", func() {
		b := newFakeBridge("accepted", "ok")
		f := newShimFixture(b.sock)
		f.noTTY = true
		Expect(f.shim("x").Run()).To(Equal(0))
		Expect(f.tty.Len()).To(BeZero())
		Expect(b.headers).To(Equal([]string{"CSCB1 clipboard 1 osc=none"}))
	})
})
