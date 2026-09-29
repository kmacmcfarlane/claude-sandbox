package main

// Spec: spec/launch.feature (CS-LNCH-169) — the attach/join drift check
// (wouldBeFingerprint) builds the same plan as a launch, so it creates the same
// mount-point placeholder and hashes like the launch made the same way.

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("shadow destination placeholders on the drift-check path (CS-LNCH-169)", func() {
	It("CS-LNCH-169: the drift check creates the CLAUDE.md placeholder silently and agrees with the launch", func() {
		f := newCLIFixture()
		cfgDir := filepath.Join(f.home, ".claude")
		Expect(os.MkdirAll(cfgDir, 0o755)).To(Succeed())
		md := filepath.Join(cfgDir, "CLAUDE.md")
		Expect(md).NotTo(BeAnExistingFile())

		want := currentHash(f) // runs wouldBeFingerprint
		fi, err := os.Lstat(md)
		Expect(err).NotTo(HaveOccurred(), "the drift check made the placeholder")
		Expect(fi.Mode().IsRegular()).To(BeTrue())
		Expect(fi.Size()).To(BeZero())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		Expect(f.out.String()).To(BeEmpty())
		Expect(f.errw.String()).To(BeEmpty())

		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(labelValue(f.launched().Args, "claude-sandbox.confighash")).To(Equal(want), "no drift against the launch")
		Expect(f.errw.String()).NotTo(ContainSubstring("not shadowed"))
	})
})
