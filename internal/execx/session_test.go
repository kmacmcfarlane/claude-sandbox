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
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
)

// helper is the test binary running as a launcher (see helperEnv).
type helper struct {
	cmd    *exec.Cmd
	stdout *bufio.Reader
	stderr bytes.Buffer
}

// startHelper runs script as the helper's session child and waits for the
// child to print "ready <pid>", returning that pid.
func startHelper(script string) (*helper, int) {
	h := &helper{cmd: exec.Command(os.Args[0])}
	h.cmd.Env = append(os.Environ(), helperEnv+"="+script)
	h.cmd.Stderr = &h.stderr
	out, err := h.cmd.StdoutPipe()
	Expect(err).NotTo(HaveOccurred())
	h.stdout = bufio.NewReader(out)
	Expect(h.cmd.Start()).To(Succeed())
	line, err := h.stdout.ReadString('\n')
	Expect(err).NotTo(HaveOccurred(), h.stderr.String())
	f := strings.Fields(line)
	Expect(f).To(HaveLen(2), line)
	Expect(f[0]).To(Equal("ready"))
	pid, err := strconv.Atoi(f[1])
	Expect(err).NotTo(HaveOccurred())
	return h, pid
}

// wait returns the helper's exit status.
func (h *helper) wait() int {
	io.Copy(io.Discard, h.stdout)
	h.cmd.Wait()
	return h.cmd.ProcessState.ExitCode()
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
		Expect(res).To(Equal(execx.SessionResult{Code: 7}))
	})

	It("CS-LNCH-085: reports death by signal n as 128+n", func() {
		res, err := execx.System{}.RunSession(execx.Cmd{Name: "sh", Args: []string{"-c", "kill -KILL $$"}, Stdin: strings.NewReader("")})
		Expect(err).NotTo(HaveOccurred())
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

	It("CS-LNCH-086: SIGINT and SIGQUIT to the launcher alone are dropped, and the launcher survives", func() {
		h, _ := startHelper(`echo ready $$; sleep 0.4; exit 5`)
		Expect(h.cmd.Process.Signal(syscall.SIGINT)).To(Succeed())
		Expect(h.cmd.Process.Signal(syscall.SIGQUIT)).To(Succeed())
		Expect(h.wait()).To(Equal(5))
		Expect(h.stderr.String()).NotTo(ContainSubstring("forwarded"))
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
		var out bytes.Buffer
		_, err := execx.System{}.RunSession(execx.Cmd{Name: "sh", Args: []string{"-c", "exec grep SigIgn /proc/self/status"}, Stdin: strings.NewReader(""), Stdout: &out})
		Expect(err).NotTo(HaveOccurred())
		mask, perr := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(out.String(), "SigIgn:")), 16, 64)
		Expect(perr).NotTo(HaveOccurred(), out.String())
		Expect(mask&(1<<(uint(syscall.SIGINT)-1))).To(BeZero(), "SIGINT ignored in the child")
		Expect(mask&(1<<(uint(syscall.SIGQUIT)-1))).To(BeZero(), "SIGQUIT ignored in the child")
	})

	It("CS-LNCH-086: the child stays in the launcher's process group, so the terminal reaches it", func() {
		var out bytes.Buffer
		_, err := execx.System{}.RunSession(execx.Cmd{Name: "sh", Args: []string{"-c", "ps -o pgid= -p $$ 2>/dev/null || cut -d' ' -f5 /proc/$$/stat"}, Stdin: strings.NewReader(""), Stdout: &out})
		Expect(err).NotTo(HaveOccurred())
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
		Expect(res).To(Equal(execx.SessionResult{Code: 0, Forwarded: syscall.SIGTERM}))
	})

	It("a non-code error is a failure to start", func() {
		f := &execx.Fake{}
		f.On("docker start", "", io.ErrUnexpectedEOF)
		_, err := f.RunSession(execx.Cmd{Name: "docker", Args: []string{"start"}})
		Expect(err).To(MatchError(io.ErrUnexpectedEOF))
	})
})
