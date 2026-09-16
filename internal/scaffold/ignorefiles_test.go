package scaffold_test

// Spec: spec/init-ralph.feature CS-INITR-007 — the Docker build context and
// the host gitignore exclude the same debris the seeding walk filters. The
// builder stage does `COPY scaffold-ralph/ scaffold-ralph/`, and dockerignore
// patterns are root-anchored unless prefixed with `**/`, so these tests pin
// the exact recursive lines. String presence only — no docker, no
// patternmatcher dependency.

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// repoRoot walks up from the package directory to the directory holding go.mod.
func repoRoot() string {
	dir, err := os.Getwd()
	Expect(err).NotTo(HaveOccurred())
	for {
		if _, serr := os.Stat(filepath.Join(dir, "go.mod")); serr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		Expect(parent).NotTo(Equal(dir), "go.mod not found above "+dir)
		dir = parent
	}
}

func ignoreLines(name string) []string {
	raw, err := os.ReadFile(filepath.Join(repoRoot(), name))
	Expect(err).NotTo(HaveOccurred())
	var lines []string
	for _, l := range strings.Split(string(raw), "\n") {
		lines = append(lines, strings.TrimSpace(l))
	}
	return lines
}

var _ = Describe("scaffold debris ignore rules", func() {
	It("CS-INITR-007: .dockerignore excludes the debris recursively and dot-entries under the scaffold trees", func() {
		lines := ignoreLines(".dockerignore")
		for _, want := range []string{
			"**/__pycache__",
			"**/*.pyc",
			"**/*.pyo",
			"**/.pytest_cache",
			"scaffold-ralph/**/.*",
			"scaffold/**/.*",
		} {
			Expect(lines).To(ContainElement(want), ".dockerignore must carry the exact line "+want)
		}
	})

	It("CS-INITR-007: .gitignore ignores the debris", func() {
		lines := ignoreLines(".gitignore")
		for _, want := range []string{"__pycache__/", "*.pyc", ".pytest_cache/"} {
			Expect(lines).To(ContainElement(want), ".gitignore must carry the exact line "+want)
		}
	})
})
