package main

// Spec: spec/launch.feature CS-LNCH-112 — end to end through MainWithEnv: the
// reserving docker create carries --oom-score-adj, headless included, and a
// bad value fails the launch with exit 2 before anything is created.

import (
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// createFlag returns the values following flag in the reserving docker create.
func createFlag(f *cliFixture, flag string) []string {
	args := f.launched().Args
	var out []string
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

func createdAny(f *cliFixture) bool {
	for _, c := range f.fake.Calls {
		if c.Name == "docker" && len(c.Args) > 0 && c.Args[0] == "create" {
			return true
		}
	}
	return false
}

var _ = Describe("CS-LNCH-112: sandboxes are the host's preferred OOM victims", func() {
	var f *cliFixture
	BeforeEach(func() { f = newCLIFixture() })

	It("CS-LNCH-112: a new container is created with --oom-score-adj 500 by default", func() {
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(createFlag(f, "--oom-score-adj")).To(Equal([]string{"500"}))
	})

	It("CS-LNCH-112: a headless session gets it too", func() {
		Expect(f.run("headless", "--", "-p", "hi")).To(Equal(0), f.errw.String())
		Expect(createFlag(f, "--oom-score-adj")).To(Equal([]string{"500"}))
	})

	It("CS-LNCH-112: the cascade key sets it and the env var overrides the key", func() {
		writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "oomScoreAdj: 300\n")
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(createFlag(f, "--oom-score-adj")).To(Equal([]string{"300"}))

		g := newCLIFixture()
		writeFile(filepath.Join(g.proj, ".claude-sandbox", "config.yaml"), "oomScoreAdj: 300\n")
		g.envmap["CLAUDE_SANDBOX_OOM_SCORE_ADJ"] = "0"
		Expect(g.run()).To(Equal(0), g.errw.String())
		Expect(createFlag(g, "--oom-score-adj")).To(Equal([]string{"0"}))
	})

	It("CS-LNCH-112: a bad value exits 2 naming its source, and creates nothing", func() {
		f.envmap["CLAUDE_SANDBOX_OOM_SCORE_ADJ"] = "2000"
		Expect(f.run()).To(Equal(2))
		Expect(f.errw.String()).To(ContainSubstring("CLAUDE_SANDBOX_OOM_SCORE_ADJ"))
		Expect(createdAny(f)).To(BeFalse())
	})
})
