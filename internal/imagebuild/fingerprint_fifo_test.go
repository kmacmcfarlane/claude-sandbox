package imagebuild_test

// Spec: spec/image-build.feature CS-IMG-075 — the base, tools and CLI
// fingerprints read the repository's files non-blocking and regular-only: a
// FIFO at one (session-writable when a sandbox works on this repository)
// makes the fingerprint uncomputable at once — the time-rule fallback of
// CS-IMG-035 — and never hangs the launch.

import (
	"os"
	"path/filepath"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/imagebuild"
)

var _ = Describe("CS-IMG-075: repo-file fingerprints never block on a FIFO", func() {
	var repo string

	BeforeEach(func() { repo = GinkgoT().TempDir() })

	plant := func(name string) {
		p := filepath.Join(repo, name)
		Expect(syscall.Mkfifo(p, 0o600)).To(Succeed())
		DeferCleanup(func() {
			if w, err := os.OpenFile(p, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				_ = w.Close()
			}
		})
	}

	within5s := func(f func()) {
		done := make(chan struct{})
		go func() {
			defer GinkgoRecover()
			defer close(done)
			f()
		}()
		Eventually(done, 5*time.Second).Should(BeClosed(), "must not block on a FIFO")
	}

	It("CS-IMG-075: a FIFO at the base Dockerfile leaves the base fingerprint uncomputable", func() {
		plant("Dockerfile")
		within5s(func() { Expect(imagebuild.BaseInputs(repo)).To(BeEmpty()) })
	})

	It("CS-IMG-075: a FIFO at Dockerfile.tools leaves the tools fingerprint uncomputable", func() {
		plant(imagebuild.ToolsDockerfile)
		within5s(func() {
			fp, _ := imagebuild.ToolsInputs(repo)
			Expect(fp).To(BeEmpty())
		})
	})

	It("CS-IMG-075: a FIFO at Dockerfile.cli leaves the CLI fingerprint uncomputable", func() {
		plant(imagebuild.CLIDockerfile)
		within5s(func() { Expect(imagebuild.CLIInputs(repo)).To(BeEmpty()) })
	})
})
