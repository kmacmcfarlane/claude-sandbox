package layout_test

// Spec: spec/layout.feature CS-LAY-023 — the layout setup runs on the launch
// path and the project tree is session-writable, so a FIFO, device or socket
// at the host .gitignore, the sidecar .gitignore or the seeded CLAUDE.md never
// blocks it: every read is non-blocking and regular-only, every write opens
// non-blocking and writes only a regular file.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/layout"
	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
)

// plantLayoutFIFO makes path a FIFO with no writer. The cleanup opens a
// writer, which releases a reader a regression left blocked in open(2).
func plantLayoutFIFO(path string) {
	Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
	Expect(syscall.Mkfifo(path, 0o600)).To(Succeed())
	DeferCleanup(func() {
		if w, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = w.Close()
		}
	})
}

func isFIFO(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode()&os.ModeNamedPipe != 0
}

// bounded runs f and fails unless it returns within 5 s.
func bounded(f func() error) error {
	done := make(chan error, 1)
	go func() {
		defer GinkgoRecover()
		done <- f()
	}()
	var err error
	Eventually(done, 5*time.Second).Should(Receive(&err), "must not block on a FIFO")
	return err
}

var _ = Describe("CS-LAY-023: layout never blocks on a non-regular file", func() {
	var proj, sb, hostGI string
	var fake *execx.Fake
	var sp *prompt.Scripted
	var out, errOut bytes.Buffer

	setup := func(track bool) error {
		return bounded(func() error {
			return layout.Setup(proj, track, layout.Options{
				Runner: fake, Prompter: sp, Out: &out, Err: &errOut, Gitignore: ptr(true),
			})
		})
	}

	BeforeEach(func() {
		os.Unsetenv("CS_GITIGNORE_ASSUME")
		proj = filepath.Join(GinkgoT().TempDir(), "p")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		sb = filepath.Join(proj, ".claude-sandbox")
		hostGI = filepath.Join(proj, ".gitignore")
		fake = &execx.Fake{}
		sp = &prompt.Scripted{IsTTY: true}
		out.Reset()
		errOut.Reset()
	})

	// probes are the git check-ignore calls made: with real git each one
	// opens the host (and sidecar) .gitignore blocking, so none may run.
	probes := func() []string {
		var out []string
		for _, l := range fake.CommandLines() {
			if strings.Contains(l, "check-ignore") {
				out = append(out, l)
			}
		}
		return out
	}
	skipWarning := func(p string) string {
		return "WARNING: cannot read " + p + " ("
	}

	It("CS-LAY-023: a FIFO at the host .gitignore skips its update and every git probe (trackInHost false)", func() {
		plantLayoutFIFO(hostGI)
		Expect(setup(false)).To(Succeed())
		Expect(errOut.String()).To(ContainSubstring(skipWarning(hostGI) + "not a regular file"))
		Expect(errOut.String()).To(ContainSubstring("skipping the .gitignore update and the git ignore checks, which would read it, and so the sidecar git init."))
		Expect(strings.Count(errOut.String(), "WARNING")).To(Equal(1))
		Expect(errOut.String()).NotTo(ContainSubstring("These entries are missing"))
		Expect(probes()).To(BeEmpty(), "git check-ignore would block on the FIFO")
		Expect(isFIFO(hostGI)).To(BeTrue(), "the FIFO is left alone")
		// The sidecar .gitignore (CS-LAY-004) is still written; the sidecar
		// init, which needs a probe's answer, is skipped.
		Expect(read(filepath.Join(sb, ".gitignore"))).To(ContainSubstring("temp/"))
		for _, l := range fake.CommandLines() {
			Expect(l).NotTo(ContainSubstring(" init "))
		}
	})

	It("CS-LAY-023: a FIFO at the host .gitignore skips its update and every git probe (trackInHost true)", func() {
		plantLayoutFIFO(hostGI)
		Expect(setup(true)).To(Succeed())
		Expect(errOut.String()).To(ContainSubstring(skipWarning(hostGI) + "not a regular file"))
		Expect(probes()).To(BeEmpty())
		Expect(isFIFO(hostGI)).To(BeTrue())
	})

	It("CS-LAY-023: with host-tracked files the CS-LAY-020 warning still prints, without the probe", func() {
		fake.On("ls-files -z -- .claude-sandbox", ".claude-sandbox/config.yaml\x00", nil)
		plantLayoutFIFO(hostGI)
		Expect(setup(false)).To(Succeed())
		Expect(errOut.String()).To(ContainSubstring(skipWarning(hostGI)))
		Expect(errOut.String()).To(ContainSubstring("the host repo already tracks 1 file under .claude-sandbox/"))
		Expect(probes()).To(BeEmpty())
		Expect(read(filepath.Join(sb, ".gitignore"))).To(ContainSubstring("temp/"), "CS-LAY-004 as on the ordinary path")
	})

	It("CS-LAY-023: a FIFO at the sidecar .gitignore with trackInHost true skips the probes, naming it", func() {
		gi := filepath.Join(sb, ".gitignore")
		plantLayoutFIFO(gi)
		Expect(setup(true)).To(Succeed())
		Expect(errOut.String()).To(ContainSubstring(skipWarning(gi) + "not a regular file"))
		Expect(probes()).To(BeEmpty())
	})

	It("CS-LAY-023: a directory at the host .gitignore is skipped the same way", func() {
		Expect(os.MkdirAll(hostGI, 0o755)).To(Succeed())
		Expect(setup(false)).To(Succeed())
		Expect(errOut.String()).To(ContainSubstring(skipWarning(hostGI) + "not a regular file"))
		Expect(probes()).To(BeEmpty())
		Expect(errOut.String()).NotTo(ContainSubstring("These entries are missing"))
	})

	It("CS-LAY-023: an unreadable (no permission) host .gitignore is skipped the same way", func() {
		if os.Geteuid() == 0 {
			Skip("root reads a mode-000 file")
		}
		write(hostGI, "node_modules/\n")
		Expect(os.Chmod(hostGI, 0)).To(Succeed())
		DeferCleanup(func() { _ = os.Chmod(hostGI, 0o644) })
		Expect(setup(true)).To(Succeed())
		Expect(errOut.String()).To(ContainSubstring(skipWarning(hostGI) + "permission denied"))
		Expect(probes()).To(BeEmpty())
		Expect(errOut.String()).NotTo(ContainSubstring("These entries are missing"))
	})

	It("CS-LAY-023: a FIFO at the sidecar .gitignore fails the setup at once, naming it, before any probe", func() {
		gi := filepath.Join(sb, ".gitignore")
		plantLayoutFIFO(gi)
		err := setup(false)
		Expect(err).To(MatchError(ContainSubstring(gi)))
		Expect(err).To(MatchError(ContainSubstring("not a regular file")))
		Expect(isFIFO(gi)).To(BeTrue())
		Expect(probes()).To(BeEmpty())
	})

	It("CS-LAY-023: the CLAUDE.md seed never writes through a symlink or onto a FIFO", func() {
		md := filepath.Join(sb, "CLAUDE.md")
		target := filepath.Join(proj, "elsewhere.md")
		Expect(os.MkdirAll(sb, 0o755)).To(Succeed())
		Expect(os.Symlink(target, md)).To(Succeed())
		Expect(setup(false)).To(Succeed())
		Expect(target).NotTo(BeAnExistingFile(), "a dangling link is not followed")

		Expect(os.Remove(md)).To(Succeed())
		plantLayoutFIFO(md)
		Expect(setup(false)).To(Succeed())
		Expect(isFIFO(md)).To(BeTrue())
	})

	It("CS-LAY-023: the .gitignore writer refuses a FIFO without blocking", func() {
		p := filepath.Join(proj, "gi")
		plantLayoutFIFO(p)
		err := bounded(func() error { return layout.WriteRegularFile(proj, p, []byte("x\n")) })
		Expect(err).To(HaveOccurred(), "ENXIO with no reader, else not a regular file")
		Expect(isFIFO(p)).To(BeTrue())

		reg := filepath.Join(proj, "reg")
		Expect(os.WriteFile(reg, []byte("a much longer old body\n"), 0o644)).To(Succeed())
		Expect(layout.WriteRegularFile(proj, reg, []byte("new\n"))).To(Succeed())
		Expect(read(reg)).To(Equal("new\n"))
	})
})
