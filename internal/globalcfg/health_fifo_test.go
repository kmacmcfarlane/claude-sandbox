package globalcfg_test

// Spec: spec/global-config.feature CS-GCFG-060 — the health check's default
// reader is cascade.ReadRegularFile: a FIFO, device or socket at the global
// file (under the read-write config dir in the linked layout) is an
// unreadable file at once — no wait for a writer, no parse retries — and the
// check stays warn-only. Scratch HOME and state root only.

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/globalcfg"
)

var _ = Describe("CS-GCFG-060: the health check never blocks on a FIFO", func() {
	var f *healthFixture

	BeforeEach(func() { f = newHealthFixture() })

	plant := func(path string) {
		Expect(syscall.Mkfifo(path, 0o600)).To(Succeed())
		DeferCleanup(func() {
			if w, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				_ = w.Close()
			}
		})
	}

	// checkDefault runs the check with the production reader (no ReadFile
	// seam) and fails unless it returns within 5 s.
	checkDefault := func() *globalcfg.Health {
		var h *globalcfg.Health
		done := make(chan struct{})
		go func() {
			defer GinkgoRecover()
			defer close(done)
			h = globalcfg.CheckHealth(globalcfg.HealthOptions{
				Home: f.home, StateRoot: f.state,
				Getenv: func(k string) string { return f.env[k] },
				Err:    f.errw,
				Now:    func() time.Time { return f.now },
				Sleep:  func(d time.Duration) { f.slept = append(f.slept, d) },
			})
		}()
		Eventually(done, 5*time.Second).Should(BeClosed(), "the check must not block on a FIFO")
		return h
	}

	It("CS-GCFG-060: a FIFO with no baseline warns as an unreadable file, without retries", func() {
		plant(f.link)
		h := checkDefault()
		Expect(f.slept).To(BeEmpty(), "a non-regular file is not a torn write")
		Expect(h.Findings).To(Equal([]string{"it cannot be read (not a regular file (p---------))"}))
		Expect(f.errw.String()).To(ContainSubstring("WARNING: the global config " + f.link + ": it cannot be read (not a regular file"))
		Expect(f.snapshots(f.link)).To(BeEmpty())
	})

	It("CS-GCFG-060: a FIFO at the linked target, after a baseline, is damage with the restore warning", func() {
		write(f.target, healthy)
		Expect(os.Symlink(".claude/.claude.json", f.link)).To(Succeed())
		Expect(checkDefault().Snapshot).NotTo(BeEmpty())
		Expect(f.errw.String()).To(BeEmpty())

		Expect(os.Remove(f.target)).To(Succeed())
		plant(f.target)
		f.errw = &bytes.Buffer{}
		h := checkDefault()
		Expect(h.Damaged()).To(BeTrue())
		Expect(f.errw.String()).To(ContainSubstring("it cannot be read (not a regular file"))
		Expect(f.slept).To(BeEmpty())
		fi, err := os.Lstat(f.target)
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode() & os.ModeNamedPipe).NotTo(BeZero(), "never written")
	})

	It("CS-GCFG-060: a FIFO among the snapshots is skipped as unparseable, never waited on", func() {
		write(f.link, healthy)
		Expect(checkDefault().Snapshot).NotTo(BeEmpty())
		dir := filepath.Join(f.state, globalcfg.StoreDirName, globalcfg.StoreKey(f.link))
		plant(filepath.Join(dir, globalcfg.SnapshotPrefix+"9999999999999"))
		f.now = f.now.Add(time.Minute)
		Expect(checkDefault().Damaged()).To(BeFalse())
	})
})
