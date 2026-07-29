package paths_test

// Spec: spec/paths.feature (CS-PATH). Trees are built inside a temp dir; the
// walk functions continue to the real filesystem root, which contains no stray
// .claude-sandbox files.

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/paths"
)

// touch creates the file (and parents) with trivial content.
func touch(p string) {
	Expect(os.MkdirAll(filepath.Dir(p), 0o755)).To(Succeed())
	Expect(os.WriteFile(p, []byte("x\n"), 0o644)).To(Succeed())
}

var _ = Describe("foreign-path resolution", func() {
	var tmp string

	BeforeEach(func() {
		tmp = GinkgoT().TempDir()
	})

	It("CS-PATH-001: logical keys resolve under .claude-sandbox/", func() {
		proj := filepath.Join(tmp, "proj")
		expected := map[string]string{
			"config":     filepath.Join(proj, ".claude-sandbox/config.yaml"),
			"dockerfile": filepath.Join(proj, ".claude-sandbox/Dockerfile"),
			"env":        filepath.Join(proj, ".claude-sandbox/env"),
			"ralph":      filepath.Join(proj, ".claude-sandbox/ralph"),
			"agent":      filepath.Join(proj, ".claude-sandbox/agent"),
			"scripts":    filepath.Join(proj, ".claude-sandbox/scripts"),
		}
		for logical, want := range expected {
			got, err := paths.Resolve(proj, logical)
			Expect(err).NotTo(HaveOccurred(), logical)
			Expect(got).To(Equal(want), logical)
		}
	})

	It("CS-PATH-002: unknown logical key is an error", func() {
		_, err := paths.Resolve(tmp, "bogus")
		Expect(err).To(MatchError(ContainSubstring("bogus")))

		_, err = paths.FindUp(tmp, "bogus")
		Expect(err).To(MatchError(ContainSubstring("bogus")))

		_, err = paths.CollectUp(tmp, "bogus")
		Expect(err).To(MatchError(ContainSubstring("bogus")))
	})

	It("CS-PATH-003: FindUp returns the nearest ancestor hit", func() {
		a := filepath.Join(tmp, "a")
		touch(filepath.Join(a, ".claude-sandbox/config.yaml"))
		touch(filepath.Join(a, "b/c/.claude-sandbox/config.yaml"))
		Expect(os.MkdirAll(filepath.Join(a, "b/c/d"), 0o755)).To(Succeed())

		got, err := paths.FindUp(filepath.Join(a, "b/c/d"), "config")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(filepath.Join(a, "b/c/.claude-sandbox/config.yaml")))
	})

	It("CS-PATH-004: FindUp checks the start directory itself", func() {
		ab := filepath.Join(tmp, "a/b")
		touch(filepath.Join(ab, ".claude-sandbox/env"))

		got, err := paths.FindUp(ab, "env")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(filepath.Join(ab, ".claude-sandbox/env")))
	})

	It("CS-PATH-005: FindUp returns empty when nothing matches up to the root", func() {
		xyz := filepath.Join(tmp, "x/y/z")
		Expect(os.MkdirAll(xyz, 0o755)).To(Succeed())

		got, err := paths.FindUp(xyz, "config")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(BeEmpty())
	})

	It("CS-PATH-006: CollectUp returns every hit in root-first order", func() {
		a := filepath.Join(tmp, "a")
		touch(filepath.Join(a, ".claude-sandbox/config.yaml"))
		touch(filepath.Join(a, "b/.claude-sandbox/config.yaml"))
		touch(filepath.Join(a, "b/c/.claude-sandbox/config.yaml"))

		got, err := paths.CollectUp(filepath.Join(a, "b/c"), "config")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal([]string{
			filepath.Join(a, ".claude-sandbox/config.yaml"),
			filepath.Join(a, "b/.claude-sandbox/config.yaml"),
			filepath.Join(a, "b/c/.claude-sandbox/config.yaml"),
		}))
	})

	It("CS-PATH-007: CollectUp only matches files, not directories", func() {
		a := filepath.Join(tmp, "a")
		Expect(os.MkdirAll(filepath.Join(a, ".claude-sandbox/config.yaml"), 0o755)).To(Succeed())

		got, err := paths.CollectUp(a, "config")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(BeEmpty())
	})

	It("CS-PATH-008: layout mode reflects presence of .claude-sandbox/", func() {
		proj := filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		Expect(paths.LayoutMode(proj)).To(Equal("none"))

		Expect(os.MkdirAll(filepath.Join(proj, ".claude-sandbox"), 0o755)).To(Succeed())
		Expect(paths.LayoutMode(proj)).To(Equal("new"))
	})
})
