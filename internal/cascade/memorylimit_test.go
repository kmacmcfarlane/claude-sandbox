package cascade_test

// Spec: spec/config-cascade.feature CS-CASC-036 — the memoryLimit-only source
// helper behind the OOM report (CS-LNCH-089, CS-LNCH-093).

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
)

var _ = Describe("CS-CASC-036: memoryLimit source", func() {
	var tmp string
	BeforeEach(func() { tmp = GinkgoT().TempDir() })

	It("is the upstream file when only it sets the key", func() {
		files := writeConfigs(tmp, "memoryLimit: 16g\n", "model: opus\n")
		Expect(cascade.MemoryLimitSource(files)).To(Equal(files[0]))
	})

	It("is the most-local file that sets it", func() {
		files := writeConfigs(tmp, "memoryLimit: 16g\n", "memoryLimit: 4g\n")
		Expect(cascade.MemoryLimitSource(files)).To(Equal(files[1]))
	})

	It("is empty when no level sets it", func() {
		files := writeConfigs(tmp, "model: opus\n", "# memoryLimit: 8g\n")
		Expect(cascade.MemoryLimitSource(files)).To(BeEmpty())
		Expect(cascade.MemoryLimitSource(nil)).To(BeEmpty())
	})

	It("counts a level that sets it empty: that is what the merge took", func() {
		files := writeConfigs(tmp, "memoryLimit: 16g\n", "memoryLimit: \"\"\n")
		Expect(cascade.MemoryLimitSource(files)).To(Equal(files[1]))
		cfg, err := cascade.Load(files)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.MemoryLimit).To(BeEmpty(), "the launcher then applies the default")
	})
})
