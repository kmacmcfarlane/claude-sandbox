package scaffold_test

// Spec: spec/init-ralph.feature (CS-INITR-004..007) — ralph scaffold seeding
// mechanics. The seeds come from the embedded scaffold-ralph/ tree.

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/scaffold"
)

func write(p, content string) {
	Expect(os.MkdirAll(filepath.Dir(p), 0o755)).To(Succeed())
	Expect(os.WriteFile(p, []byte(content), 0o644)).To(Succeed())
}

func read(p string) string {
	raw, err := os.ReadFile(p)
	Expect(err).NotTo(HaveOccurred())
	return string(raw)
}

var _ = Describe("ralph scaffold seeding", func() {
	var sb string

	BeforeEach(func() {
		sb = filepath.Join(GinkgoT().TempDir(), ".claude-sandbox")
	})

	It("CS-INITR-004: __PROJECT_NAME__ is substituted only in newly created files", func() {
		preExisting := filepath.Join(sb, "agent", "backlog_done.yaml")
		write(preExisting, "project: __PROJECT_NAME__\n")

		created, skipped, err := scaffold.SeedRalph(sb, "myproj", io.Discard)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeNumerically(">", 0))
		Expect(skipped).To(Equal(1))

		Expect(read(filepath.Join(sb, "agent", "backlog.yaml"))).To(ContainSubstring("project: myproj"))
		Expect(read(filepath.Join(sb, "agent", "backlog.yaml"))).NotTo(ContainSubstring("__PROJECT_NAME__"))
		Expect(read(preExisting)).To(Equal("project: __PROJECT_NAME__\n"))
	})

	It("CS-INITR-005: project names with replacement metacharacters substitute literally", func() {
		_, _, err := scaffold.SeedRalph(sb, "foo&bar", io.Discard)
		Expect(err).NotTo(HaveOccurred())
		Expect(read(filepath.Join(sb, "agent", "backlog.yaml"))).To(ContainSubstring("project: foo&bar"))
	})

	It("CS-INITR-006: seeded python scripts are executable", func() {
		_, _, err := scaffold.SeedRalph(sb, "myproj", io.Discard)
		Expect(err).NotTo(HaveOccurred())

		fi, serr := os.Stat(filepath.Join(sb, "scripts", "backlog", "backlog.py"))
		Expect(serr).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm() & 0o111).NotTo(BeZero())
	})

	It("CS-INITR-007: __pycache__ contents are never seeded", func() {
		_, _, err := scaffold.SeedRalph(sb, "myproj", io.Discard)
		Expect(err).NotTo(HaveOccurred())

		Expect(filepath.WalkDir(sb, func(path string, d fs.DirEntry, werr error) error {
			Expect(werr).NotTo(HaveOccurred())
			Expect(strings.Contains(path, "__pycache__")).To(BeFalse(), path)
			return nil
		})).To(Succeed())
	})
})
