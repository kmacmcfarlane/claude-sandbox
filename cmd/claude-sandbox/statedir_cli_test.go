package main

// Spec: spec/host-dirs.feature (CS-DIR-007) — the Env.StateDir seam.

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Env.StateDir (CS-DIR-007)", func() {
	It("CS-DIR-007: resolving the real state root under go test panics naming Env.StateDir", func() {
		e := &Env{Getenv: func(string) string { return "" }}
		Expect(func() { e.stateDir() }).To(PanicWith(ContainSubstring("Env.StateDir")))
	})

	It("CS-DIR-007: a set StateDir is returned unchanged", func() {
		dir := GinkgoT().TempDir()
		Expect((&Env{StateDir: dir}).stateDir()).To(Equal(dir))
		Expect(newCLIFixture().env.stateDir()).NotTo(BeEmpty())
	})
})
