package execx_test

// Spec: spec/tmux.feature CS-TMUX-040 — a bounded call that times out kills
// the whole process group, so a grandchild holding the stdout pipe cannot keep
// Wait waiting for a second timeout.

import (
	"io"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
)

var _ = Describe("KillGroup (CS-TMUX-040)", func() {
	It("CS-TMUX-040: KillGroup ends a DieWithParent process and the grandchild holding its stdout pipe", func() {
		p, err := (execx.System{}).Start(execx.Cmd{Name: "sh", Args: []string{"-c", "sleep 30 & exec sleep 30"},
			Stdout: io.Discard, DieWithParent: true})
		Expect(err).NotTo(HaveOccurred())
		g, ok := p.(execx.GroupKiller)
		Expect(ok).To(BeTrue())
		time.Sleep(100 * time.Millisecond)
		Expect(g.KillGroup()).To(Succeed())
		done := make(chan struct{})
		go func() { p.Wait(); close(done) }()
		Eventually(done, 3*time.Second).Should(BeClosed())
	})

	It("CS-TMUX-040: a Fake process is no GroupKiller, so a test never signals a real group", func() {
		p, err := (&execx.Fake{}).Start(execx.Cmd{Name: "tmux"})
		Expect(err).NotTo(HaveOccurred())
		_, ok := p.(execx.GroupKiller)
		Expect(ok).To(BeFalse())
	})
})
