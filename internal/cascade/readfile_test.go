package cascade_test

// Spec: spec/config-cascade.feature CS-CASC-047 and spec/launch.feature
// CS-LNCH-172 — a FIFO (or another non-regular file) at a config.yaml or an
// env file fails at once through cascade.ReadRegularFile; it never hangs.

import (
	"bytes"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
)

// fifoBound is how long a read of a FIFO may take before the test calls it a
// hang. The reads under test return in microseconds.
const fifoBound = 5 * time.Second

// plantFIFO makes a FIFO at path, and on cleanup opens it for writing
// (non-blocking) and closes it, so a read a regression left blocked in open
// or read gets EOF and its goroutine ends.
func plantFIFO(path string) {
	Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
	Expect(syscall.Mkfifo(path, 0o600)).To(Succeed())
	DeferCleanup(func() {
		if w, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = w.Close()
		}
	})
}

// bounded runs fn in a goroutine and fails the test unless it returns within
// fifoBound.
func bounded(fn func()) {
	done := make(chan struct{})
	go func() {
		defer GinkgoRecover()
		defer close(done)
		fn()
	}()
	Eventually(done, fifoBound).Should(BeClosed(), "the read must not block on a FIFO")
}

var _ = Describe("cascade.ReadRegularFile (CS-CASC-047, CS-LNCH-172)", func() {
	var tmp string
	BeforeEach(func() { tmp = GinkgoT().TempDir() })

	It("CS-CASC-047: a FIFO returns at once with ErrNotRegular, naming the path", func() {
		p := filepath.Join(tmp, "config.yaml")
		plantFIFO(p)
		var err error
		bounded(func() { _, err = cascade.ReadRegularFile(p) })
		Expect(errors.Is(err, cascade.ErrNotRegular)).To(BeTrue(), "%v", err)
		Expect(err).To(MatchError(ContainSubstring(p)))
		Expect(err).To(MatchError(ContainSubstring("not a regular file (p")))
	})

	It("CS-CASC-047: a directory is not a regular file; a missing path is fs.ErrNotExist", func() {
		_, err := cascade.ReadRegularFile(tmp)
		Expect(errors.Is(err, cascade.ErrNotRegular)).To(BeTrue(), "%v", err)
		_, err = cascade.ReadRegularFile(filepath.Join(tmp, "gone"))
		Expect(errors.Is(err, fs.ErrNotExist)).To(BeTrue(), "%v", err)
	})

	It("CS-CASC-047: a character device is not a regular file", func() {
		_, err := cascade.ReadRegularFile("/dev/null")
		Expect(errors.Is(err, cascade.ErrNotRegular)).To(BeTrue(), "%v", err)
	})

	It("CS-CASC-047: a socket fails, naming the path", func() {
		p := filepath.Join(tmp, "s")
		if len(p) > 100 { // sun_path is 108 bytes; never fall back to the real temp root
			Skip("scratch dir too long for a unix socket path")
		}
		l, err := net.Listen("unix", p)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(l.Close)
		bounded(func() { _, err = cascade.ReadRegularFile(p) })
		Expect(err).To(HaveOccurred())
		Expect(err).To(MatchError(ContainSubstring(p)))
	})

	It("CS-CASC-047: a regular file, an empty one and a symlink to one are read", func() {
		p := filepath.Join(tmp, "config.yaml")
		Expect(os.WriteFile(p, []byte("model: opus\n"), 0o644)).To(Succeed())
		raw, err := cascade.ReadRegularFile(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(raw)).To(Equal("model: opus\n"))

		link := filepath.Join(tmp, "link.yaml")
		Expect(os.Symlink(p, link)).To(Succeed())
		raw, err = cascade.ReadRegularFile(link)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(raw)).To(Equal("model: opus\n"))

		empty := filepath.Join(tmp, "empty")
		Expect(os.WriteFile(empty, nil, 0o644)).To(Succeed())
		raw, err = cascade.ReadRegularFile(empty)
		Expect(err).NotTo(HaveOccurred())
		Expect(raw).NotTo(BeNil())
		Expect(raw).To(BeEmpty())
	})

	It("CS-CASC-047: ReadConfigFiles fails at once on a FIFO config.yaml, naming it", func() {
		files := writeConfigs(tmp, "model: opus\n")
		fifo := filepath.Join(tmp, "p", ".claude-sandbox", "config.yaml")
		plantFIFO(fifo)
		var err error
		bounded(func() { _, err = cascade.ReadConfigFiles(append(files, fifo)) })
		Expect(err).To(MatchError(ContainSubstring(fifo)))
		Expect(err).To(MatchError(ContainSubstring("not a regular file")))
		Expect(strings.Count(err.Error(), fifo)).To(Equal(1), "the path prints once: %v", err)
		Expect(errors.Is(err, cascade.ErrNotRegular)).To(BeTrue())
		bounded(func() { _, err = cascade.Load(append(files, fifo)) })
		Expect(err).To(MatchError(ContainSubstring(fifo)))
	})

	It("CS-CASC-047: the path-taking helpers skip a FIFO without waiting", func() {
		files := writeConfigs(tmp, "trackInHost: true\nmemoryLimit: 4g\n")
		fifo := filepath.Join(tmp, "p", ".claude-sandbox", "config.yaml")
		plantFIFO(fifo)
		all := append(files, fifo)
		bounded(func() {
			Expect(cascade.TrackInHost(all)).To(BeTrue())
			Expect(cascade.TrackInHostSource(all)).To(Equal(files[0]))
			Expect(cascade.KeySource(all, "memoryLimit")).To(Equal(files[0]))
		})
	})

	It("CS-LNCH-172: ReadEnvFiles fails at once on a FIFO env file, naming it", func() {
		good := filepath.Join(tmp, "ws", ".claude-sandbox", "env")
		Expect(os.MkdirAll(filepath.Dir(good), 0o755)).To(Succeed())
		Expect(os.WriteFile(good, []byte("TOKEN=a\n"), 0o600)).To(Succeed())
		fifo := filepath.Join(tmp, "ws", "p", ".claude-sandbox", "env")
		plantFIFO(fifo)
		var err error
		bounded(func() { _, err = cascade.ReadEnvFiles([]string{good, fifo}) })
		Expect(err).To(MatchError(ContainSubstring(fifo)))
		Expect(err).To(MatchError(ContainSubstring("not a regular file")))
	})

	It("CS-LNCH-172: the env lint and the override notice skip a FIFO env file without waiting", func() {
		good := filepath.Join(tmp, "ws", ".claude-sandbox", "env")
		Expect(os.MkdirAll(filepath.Dir(good), 0o755)).To(Succeed())
		Expect(os.WriteFile(good, []byte("TOKEN=\"a\"\n"), 0o600)).To(Succeed())
		fifo := filepath.Join(tmp, "ws", "p", ".claude-sandbox", "env")
		plantFIFO(fifo)
		var lint, over bytes.Buffer
		bounded(func() {
			cascade.LintEnvFiles(&lint, []string{good, fifo})
			cascade.PrintEnvOverrides(&over, []string{good, fifo}, nil)
			_, err := cascade.LintEnvFile(fifo)
			Expect(err).To(MatchError(ContainSubstring("not a regular file")))
		})
		Expect(lint.String()).To(ContainSubstring(good)) // the regular file is still linted
		Expect(over.String()).To(BeEmpty())
	})
})
