package paths_test

// Spec: spec/config-cascade.feature (CS-CASC-031) — the search chain of a
// linked git worktree.

import (
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/paths"
)

var _ = Describe("linked-worktree search chain (CS-CASC-031)", func() {
	It("CS-CASC-031: the worktree's own levels are more local than the main checkout's, and no level repeats", func() {
		Expect(paths.Chain("/h/.paseo/wt/abc/feat", "/h/ws/repo")).To(Equal([]string{
			"/h/.paseo/wt/abc/feat", "/h/.paseo/wt/abc", "/h/.paseo/wt", "/h/.paseo",
			"/h/ws/repo", "/h/ws", "/h",
		}))
	})

	It("CS-CASC-031: a harness worktree inside the repository gets exactly its plain parent walk", func() {
		wt := "/h/ws/repo/.claude/worktrees/x"
		Expect(paths.Chain(wt, "/h/ws/repo")).To(Equal(paths.Chain(wt, "")))
	})

	It("CS-CASC-031: without a main checkout the chain is the physical parent walk", func() {
		Expect(paths.Chain("/a/b/c", "")).To(Equal([]string{"/a/b/c", "/a/b", "/a"}))
	})

	It("CS-CASC-031: config and env collect root-first along the chain, main checkout as the project level", func() {
		tmp := GinkgoT().TempDir()
		home := filepath.Join(tmp, "h")
		main := filepath.Join(home, "ws", "repo")
		wt := filepath.Join(home, ".paseo", "wt", "abc", "feat")
		for _, d := range []string{home, filepath.Join(home, "ws"), main, filepath.Join(home, ".paseo"), wt} {
			touch(filepath.Join(d, ".claude-sandbox", "config.yaml"))
		}
		touch(filepath.Join(main, ".claude-sandbox", "env"))
		chain := paths.Chain(wt, main)
		cfgs, err := paths.CollectChain(chain, paths.Config)
		Expect(err).NotTo(HaveOccurred())
		rel := func(d string) string { return filepath.Join(d, ".claude-sandbox", "config.yaml") }
		Expect(cfgs).To(Equal([]string{
			rel(home), rel(filepath.Join(home, "ws")), rel(main), rel(filepath.Join(home, ".paseo")), rel(wt),
		}))
		envs, err := paths.CollectChain(chain, paths.Env)
		Expect(err).NotTo(HaveOccurred())
		Expect(envs).To(Equal([]string{filepath.Join(main, ".claude-sandbox", "env")}))
		Expect(paths.SandboxLevelsChain(chain)).To(Equal([]string{home, filepath.Join(home, "ws"), main, filepath.Join(home, ".paseo"), wt}))
		// Plain walk from the worktree never sees the main checkout.
		plain, _ := paths.CollectUp(wt, paths.Env)
		Expect(plain).To(BeEmpty())
	})
})
