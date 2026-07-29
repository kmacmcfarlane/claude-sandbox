package main

// Spec: spec/config-cascade.feature CS-CASC-020 — every env file in the
// cascade is linted at launch, warnings go to stderr, and the launch proceeds.

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func readFile(p string) string {
	raw, err := os.ReadFile(p)
	Expect(err).NotTo(HaveOccurred())
	return string(raw)
}

var _ = Describe("env file linting at launch", func() {
	It("CS-CASC-020: warns for every cascade level and still launches", func() {
		f := newCLIFixture()
		parent := filepath.Dir(f.proj)
		parentEnv := filepath.Join(parent, ".claude-sandbox", "env")
		projEnv := filepath.Join(f.proj, ".claude-sandbox", "env")
		writeFile(parentEnv, "UPSTREAM=\"quoted\"\n")
		writeFile(projEnv, "LOCAL='quoted'\n")

		Expect(f.run()).To(Equal(0))

		errOut := f.errw.String()
		Expect(errOut).To(ContainSubstring(parentEnv + ":1: value for UPSTREAM is wrapped in \" quotes."))
		Expect(errOut).To(ContainSubstring(projEnv + ":1: value for LOCAL is wrapped in ' quotes."))

		// Warn-only: both files still feed --env-file, and neither is rewritten.
		Expect(f.fake.Execed.Args).To(ContainElements("--env-file", parentEnv))
		Expect(f.fake.Execed.Args).To(ContainElements("--env-file", projEnv))
		Expect(readFile(projEnv)).To(Equal("LOCAL='quoted'\n"))
	})

	It("CS-CASC-020: stays silent for clean env files", func() {
		f := newCLIFixture()
		writeFile(filepath.Join(f.proj, ".claude-sandbox", "env"), "CLEAN=value\n")

		Expect(f.run()).To(Equal(0))
		Expect(f.errw.String()).NotTo(ContainSubstring("wrapped in"))
		Expect(f.errw.String()).NotTo(ContainSubstring("carriage return"))
	})
})
