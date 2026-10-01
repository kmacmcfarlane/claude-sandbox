package initcmd_test

// Spec: spec/init.feature CS-INIT-033 — init reads each upstream config.yaml
// once, through the launch's non-blocking regular-only reader: a FIFO there
// (or at the project's own config.yaml, or at a parent Dockerfile init copies)
// fails init at once naming it, never hangs it.

import (
	"os"
	"path/filepath"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/initcmd"
)

func plantInitFIFO(path string) {
	mkdir(filepath.Dir(path))
	Expect(syscall.Mkfifo(path, 0o600)).To(Succeed())
	DeferCleanup(func() {
		if w, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = w.Close()
		}
	})
}

// initBounded runs init and fails unless it returns within 5 s.
func initBounded(r *run, project string, f initcmd.Flags) error {
	done := make(chan error, 1)
	go func() {
		defer GinkgoRecover()
		done <- r.init(project, f)
	}()
	var err error
	Eventually(done, 5*time.Second).Should(Receive(&err), "init must not block on a FIFO")
	return err
}

var _ = Describe("CS-INIT-033: init reads the upstream configs once, without blocking", func() {
	var ws, proj string

	BeforeEach(func() {
		os.Unsetenv("CS_GITIGNORE_ASSUME")
		ws = filepath.Join(GinkgoT().TempDir(), "ws")
		proj = filepath.Join(ws, "p")
		mkdir(proj)
	})

	It("CS-INIT-033: a FIFO at an upstream config.yaml fails init at once, naming it", func() {
		up := filepath.Join(ws, ".claude-sandbox", "config.yaml")
		plantInitFIFO(up)
		err := initBounded(&run{}, proj, initcmd.Flags{TrackInHost: ptr(false)})
		Expect(err).To(MatchError(ContainSubstring(up)))
		Expect(err).To(MatchError(ContainSubstring("not a regular file")))
	})

	It("CS-INIT-033: a FIFO at the project's own config.yaml fails init at once, naming it", func() {
		own := filepath.Join(proj, ".claude-sandbox", "config.yaml")
		plantInitFIFO(own)
		err := initBounded(&run{}, proj, initcmd.Flags{TrackInHost: ptr(true)})
		Expect(err).To(MatchError(ContainSubstring(own)))
		err = initBounded(&run{}, proj, initcmd.Flags{})
		Expect(err).To(MatchError(ContainSubstring(own)))
	})

	It("CS-INIT-033: a FIFO at the parent Dockerfile init would copy fails init at once, naming it", func() {
		df := filepath.Join(ws, ".claude-sandbox", "Dockerfile")
		plantInitFIFO(df)
		err := initBounded(&run{}, proj, initcmd.Flags{TrackInHost: ptr(false), CopyParentDockerfile: ptr(true)})
		Expect(err).To(MatchError(ContainSubstring(df)))
	})

	It("CS-INIT-033: the inherited value, its source and the layout value come from one upstream snapshot", func() {
		up := filepath.Join(ws, ".claude-sandbox", "config.yaml")
		write(up, "trackInHost: true\n")
		r := &run{}
		Expect(initBounded(r, proj, initcmd.Flags{Yes: true})).To(Succeed())
		cfg := read(filepath.Join(proj, ".claude-sandbox", "config.yaml"))
		Expect(cfg).To(ContainSubstring("# trackInHost: true   # inherited from " + up))
		// The layout ran with the inherited true: no sidecar repo or its
		// .gitignore, which trackInHost false would set up (CS-LAY-004).
		Expect(filepath.Join(proj, ".claude-sandbox", ".gitignore")).NotTo(BeAnExistingFile())
		Expect(r.out.String()).NotTo(ContainSubstring("Initialized sidecar"))
	})
})
