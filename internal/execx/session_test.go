package execx_test

// Spec: spec/launch.feature CS-LNCH-085/086 — the real session runner, against
// /bin/sh children (no docker). The signal tests signal a helper process (the
// test binary acting as a launcher, see helperEnv), as a terminal or an SDK
// client signals the launcher.

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
)

// syncBuffer is a bytes.Buffer safe to read while exec's copy goroutine
// writes it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// helper is the test binary running as a launcher (see helperEnv).
type helper struct {
	cmd    *exec.Cmd
	stdout *bufio.Reader
	stderr syncBuffer
}

// startHelper runs script as the helper's session child and waits for the
// child to print "ready <pid>", returning that pid. The helper runs in a new
// session, so it has no controlling terminal: /dev/tty cannot be opened, as
// for a supervisor or SDK client. extraEnv adds helper modes; wrap, when
// set, is a sh -c prefix that execs the helper ("$0").
func startHelper(script string, extraEnv ...string) (*helper, int) {
	return startHelperVia("", script, extraEnv...)
}

func startHelperVia(wrap, script string, extraEnv ...string) (*helper, int) {
	h := &helper{cmd: exec.Command(os.Args[0])}
	if wrap != "" {
		h.cmd = exec.Command("sh", "-c", wrap+`; exec "$0"`, os.Args[0])
	}
	h.cmd.Env = append(append(os.Environ(), helperEnv+"="+script), extraEnv...)
	h.cmd.Stderr = &h.stderr
	h.cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	out, err := h.cmd.StdoutPipe()
	Expect(err).NotTo(HaveOccurred())
	h.stdout = bufio.NewReader(out)
	Expect(h.cmd.Start()).To(Succeed())
	line, err := h.stdout.ReadString('\n')
	Expect(err).NotTo(HaveOccurred(), "helper stderr: %s", h.stderr.String())
	f := strings.Fields(line)
	Expect(f).To(HaveLen(2), line)
	Expect(f[0]).To(Equal("ready"))
	pid, err := strconv.Atoi(f[1])
	Expect(err).NotTo(HaveOccurred())
	return h, pid
}

// readLine reads the helper's next stdout line.
func (h *helper) readLine() string {
	line, err := h.stdout.ReadString('\n')
	Expect(err).NotTo(HaveOccurred(), "helper stderr: %s", h.stderr.String())
	return strings.TrimSpace(line)
}

// wait returns the helper's exit status.
func (h *helper) wait() int {
	io.Copy(io.Discard, h.stdout)
	h.cmd.Wait()
	return h.cmd.ProcessState.ExitCode()
}

// sigIgnored reads a SigIgn mask line and reports whether sig is in it.
func sigIgnored(line string, sig syscall.Signal) bool {
	mask, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(line, "SigIgn:")), 16, 64)
	Expect(err).NotTo(HaveOccurred(), line)
	return mask&(1<<(uint(sig)-1)) != 0
}

// gone reports whether pid no longer runs (absent, or a zombie).
func gone(pid int) bool {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	f := strings.Fields(string(raw[strings.LastIndexByte(string(raw), ')')+1:]))
	return len(f) > 0 && (f[0] == "Z" || f[0] == "X")
}

var _ = Describe("RunSession (CS-LNCH-085/086)", func() {
	It("CS-LNCH-085: passes the child's exit code through", func() {
		res, err := execx.System{}.RunSession(execx.Cmd{Name: "sh", Args: []string{"-c", "exit 7"}, Stdin: strings.NewReader("")})
		Expect(err).NotTo(HaveOccurred())
		res.Done()
		Expect(res.Code).To(Equal(7))
		Expect(res.Forwarded).To(BeNil())
	})

	It("CS-LNCH-085: reports death by signal n as 128+n", func() {
		res, err := execx.System{}.RunSession(execx.Cmd{Name: "sh", Args: []string{"-c", "kill -KILL $$"}, Stdin: strings.NewReader("")})
		Expect(err).NotTo(HaveOccurred())
		res.Done()
		Expect(res.Code).To(Equal(137))
	})

	It("CS-LNCH-085: a child that cannot start is an error", func() {
		_, err := execx.System{}.RunSession(execx.Cmd{Name: "/nonexistent/docker"})
		Expect(err).To(HaveOccurred())
	})

	It("CS-LNCH-086: forwards SIGTERM to the child and passes its status through", func() {
		h, _ := startHelper(`trap 'exit 42' TERM; echo ready $$; while :; do sleep 0.05; done`)
		Expect(h.cmd.Process.Signal(syscall.SIGTERM)).To(Succeed())
		Expect(h.wait()).To(Equal(42))
		Expect(h.stderr.String()).To(ContainSubstring("forwarded=terminated"))
	})

	It("CS-LNCH-086: forwards SIGHUP", func() {
		h, _ := startHelper(`trap 'exit 43' HUP; echo ready $$; while :; do sleep 0.05; done`)
		Expect(h.cmd.Process.Signal(syscall.SIGHUP)).To(Succeed())
		Expect(h.wait()).To(Equal(43))
		Expect(h.stderr.String()).To(ContainSubstring("forwarded=hangup"))
	})

	It("CS-LNCH-086: in the terminal's foreground group, SIGINT and SIGQUIT are dropped and the launcher survives", func() {
		// The terminal delivered them to docker already (same group).
		h, _ := startHelper(`trap 'exit 44' INT; trap 'exit 45' QUIT; echo ready $$; sleep 0.4; exit 5`, helperForeground+"=1")
		Expect(h.cmd.Process.Signal(syscall.SIGINT)).To(Succeed())
		Expect(h.cmd.Process.Signal(syscall.SIGQUIT)).To(Succeed())
		Expect(h.wait()).To(Equal(5))
		Expect(h.stderr.String()).NotTo(ContainSubstring("forwarded"))
	})

	It("CS-LNCH-086: with no controlling terminal, kill -INT <launcher> reaches the child", func() {
		h, _ := startHelper(`trap 'exit 44' INT; echo ready $$; while :; do sleep 0.05; done`)
		Expect(h.cmd.Process.Signal(syscall.SIGINT)).To(Succeed())
		Expect(h.wait()).To(Equal(44))
	})

	It("CS-LNCH-086: with no controlling terminal, SIGQUIT reaches the child too", func() {
		h, _ := startHelper(`trap 'exit 45' QUIT; echo ready $$; while :; do sleep 0.05; done`)
		Expect(h.cmd.Process.Signal(syscall.SIGQUIT)).To(Succeed())
		Expect(h.wait()).To(Equal(45))
	})

	It("CS-LNCH-086: the foreground check answers false with no controlling terminal", func() {
		h, _ := startHelper(`echo ready $$; if (: < /dev/tty) 2>/dev/null; then echo tty; else echo notty; fi`)
		Expect(h.readLine()).To(Equal("notty"), "the helper really has no /dev/tty")
		h.wait()
		// And in-process: the check is safe to call whatever the test runner has.
		Expect(func() { execx.ForegroundOfTTY() }).NotTo(Panic())
	})

	It("CS-LNCH-086: a signal inherited as ignored stays ignored, for the launcher and for docker", func() {
		if runtime.GOOS != "linux" {
			Skip("reads /proc")
		}
		h, _ := startHelperVia(`trap "" HUP`, `echo ready $$; grep SigIgn /proc/self/status; sleep 0.3; exit 6`)
		Expect(sigIgnored(h.readLine(), syscall.SIGHUP)).To(BeTrue(), "nohup's SIGHUP reaches docker still ignored")
		Expect(h.cmd.Process.Signal(syscall.SIGHUP)).To(Succeed())
		Expect(h.wait()).To(Equal(6), "the launcher neither died nor forwarded it")
		Expect(h.stderr.String()).NotTo(ContainSubstring("forwarded"))
	})

	It("CS-LNCH-097: after the child exits, signals arrive on Late until Release instead of killing the launcher", func() {
		h, _ := startHelper(`echo ready $$; exit 9`, helperLate+"=1")
		Expect(h.readLine()).To(Equal("returned"))
		Expect(h.cmd.Process.Signal(syscall.SIGTERM)).To(Succeed())
		Expect(h.wait()).To(Equal(9), "the child's status, not 143")
		Expect(h.stderr.String()).To(ContainSubstring("late=terminated"))
	})

	It("CS-LNCH-098: a process started with DieWithParent dies when the launcher is killed outright", func() {
		if runtime.GOOS != "linux" {
			Skip("parent-death signals are Linux-only")
		}
		h, child := startHelper(`echo ready $$; exec sleep 30`, helperTether+"=1")
		Expect(h.cmd.Process.Signal(syscall.SIGKILL)).To(Succeed())
		h.wait()
		Eventually(func() bool { return gone(child) }, 3*time.Second, 20*time.Millisecond).Should(BeTrue())
	})

	It("CS-LNCH-086: a launcher killed outright takes the child with it", func() {
		if runtime.GOOS != "linux" {
			Skip("parent-death signals are Linux-only")
		}
		h, child := startHelper(`echo ready $$; exec sleep 30`)
		Expect(h.cmd.Process.Signal(syscall.SIGKILL)).To(Succeed())
		h.wait()
		Eventually(func() bool { return gone(child) }, 3*time.Second, 20*time.Millisecond).Should(BeTrue())
	})

	It("CS-LNCH-086: the child inherits no ignored SIGINT or SIGQUIT", func() {
		if runtime.GOOS != "linux" {
			Skip("reads /proc")
		}
		h, _ := startHelper(`echo ready $$; grep SigIgn /proc/self/status`)
		line := h.readLine()
		h.wait()
		Expect(sigIgnored(line, syscall.SIGINT)).To(BeFalse(), "SIGINT ignored in the child")
		Expect(sigIgnored(line, syscall.SIGQUIT)).To(BeFalse(), "SIGQUIT ignored in the child")
	})

	It("CS-LNCH-086: the child stays in the launcher's process group, so the terminal reaches it", func() {
		var out bytes.Buffer
		res, err := execx.System{}.RunSession(execx.Cmd{Name: "sh", Args: []string{"-c", "ps -o pgid= -p $$ 2>/dev/null || cut -d' ' -f5 /proc/$$/stat"}, Stdin: strings.NewReader(""), Stdout: &out})
		Expect(err).NotTo(HaveOccurred())
		res.Done()
		pgid, _ := syscall.Getpgid(os.Getpid())
		Expect(strings.TrimSpace(out.String())).To(Equal(strconv.Itoa(pgid)))
	})

	It("IsTerminal is false for a buffer, a pipe and /dev/null", func() {
		Expect(execx.IsTerminal(&bytes.Buffer{})).To(BeFalse())
		r, w, err := os.Pipe()
		Expect(err).NotTo(HaveOccurred())
		defer r.Close()
		defer w.Close()
		Expect(execx.IsTerminal(w)).To(BeFalse())
		null, err := os.Open(os.DevNull)
		Expect(err).NotTo(HaveOccurred())
		defer null.Close()
		Expect(execx.IsTerminal(null)).To(BeFalse())
	})
})

var _ = Describe("Fake.RunSession", func() {
	It("records the child and scripts its status and a forwarded signal", func() {
		f := &execx.Fake{}
		f.On("docker start", "", execx.Fail(137))
		res, err := f.RunSession(execx.Cmd{Name: "docker", Args: []string{"start", "-ai", "x"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Code).To(Equal(137))
		Expect(f.Session.Args).To(Equal([]string{"start", "-ai", "x"}))

		f.SessionSignal = syscall.SIGTERM
		res, _ = f.RunSession(execx.Cmd{Name: "docker", Args: []string{"attach", "x"}})
		Expect(res.Code).To(Equal(0))
		Expect(res.Forwarded).To(Equal(syscall.SIGTERM))
		res.Done()
		Expect(f.Released).To(Equal(1))

		f.LateSignal = syscall.SIGINT
		res, _ = f.RunSession(execx.Cmd{Name: "docker", Args: []string{"attach", "x"}})
		Expect(<-res.Late).To(Equal(syscall.SIGINT))
	})

	It("a non-code error is a failure to start", func() {
		f := &execx.Fake{}
		f.On("docker start", "", io.ErrUnexpectedEOF)
		_, err := f.RunSession(execx.Cmd{Name: "docker", Args: []string{"start"}})
		Expect(err).To(MatchError(io.ErrUnexpectedEOF))
	})
})
