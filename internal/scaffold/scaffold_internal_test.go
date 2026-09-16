package scaffold

// Spec: spec/init-ralph.feature CS-INITR-007 — the seeding walk is the
// enforceable filter for tooling debris. Nothing can be planted into an
// embed.FS at test time, so this drives the unexported seedRalphFrom seam
// with a fstest.MapFS that carries the debris a dirty working tree would.

import (
	"io"
	"os"
	"path/filepath"
	"testing/fstest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ralph scaffold seeding filter", func() {
	It("CS-INITR-007: __pycache__, *.pyc, .pytest_cache and dot-entries are never seeded", func() {
		sb := filepath.Join(GinkgoT().TempDir(), ".claude-sandbox")
		f := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
		fsys := fstest.MapFS{
			"scaffold-ralph/agent/PROMPT.md":                               f("# __PROJECT_NAME__\n"),
			"scaffold-ralph/scripts/backlog/backlog.py":                    f("#!/usr/bin/env python3\n"),
			"scaffold-ralph/scripts/backlog/__pycache__/x.pyc":             f("pyc"),
			"scaffold-ralph/scripts/backlog/y.pyc":                         f("pyc"),
			"scaffold-ralph/scripts/backlog/z.pyo":                         f("pyo"),
			"scaffold-ralph/scripts/backlog/.pytest_cache/v/cache/nodeids": f("[]"),
			"scaffold-ralph/.hidden/z":                                     f("hidden"),
			"scaffold-ralph/agent/.DS_Store":                               f("junk"),
		}

		created, skipped, err := seedRalphFrom(fsys, "scaffold-ralph", sb, "myproj", io.Discard)
		Expect(err).NotTo(HaveOccurred())
		Expect(skipped).To(Equal(0))
		Expect(created).To(Equal(2), "only the two real files are seeded")

		Expect(filepath.Join(sb, "agent", "PROMPT.md")).To(BeAnExistingFile())
		Expect(filepath.Join(sb, "scripts", "backlog", "backlog.py")).To(BeAnExistingFile())

		for _, rel := range []string{
			"scripts/backlog/__pycache__",
			"scripts/backlog/__pycache__/x.pyc",
			"scripts/backlog/y.pyc",
			"scripts/backlog/z.pyo",
			"scripts/backlog/.pytest_cache",
			"scripts/backlog/.pytest_cache/v/cache/nodeids",
			".hidden",
			".hidden/z",
			"agent/.DS_Store",
		} {
			_, serr := os.Stat(filepath.Join(sb, filepath.FromSlash(rel)))
			Expect(os.IsNotExist(serr)).To(BeTrue(), rel+" must not be seeded")
		}
	})
})
