package initcmd_test

// Spec: spec/init.feature CS-INIT-037 — in a linked git worktree outside its
// repository, init reads the upstream configs along the launch's search
// chain, so the main checkout's trackInHost is the inherited one. The
// fixture mirrors cmd/claude-sandbox/linked_worktree_cli_test.go: a sibling
// main checkout with a real .git/worktrees/<name>/gitdir back-link on disk,
// and git's rev-parse answer scripted.

import (
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/initcmd"
	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
)

const linkedRevParse = "rev-parse --git-dir --git-common-dir --show-toplevel"

var _ = Describe("CS-INIT-037: init reads the launch's search chain in a linked worktree", func() {
	var base, proj, main, mainCfg, cfg string

	BeforeEach(func() {
		t, err := filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		base = t
		proj = filepath.Join(base, "paseo", "wt", "proj")
		main = filepath.Join(base, "ws", "repo")
		gitDir := filepath.Join(main, ".git", "worktrees", "proj")
		mkdir(proj)
		write(filepath.Join(gitDir, "gitdir"), proj+"/.git\n")
		write(filepath.Join(proj, ".git"), "gitdir: "+gitDir+"\n")
		mainCfg = filepath.Join(main, ".claude-sandbox", "config.yaml")
		write(mainCfg, "trackInHost: true\n")
		cfg = filepath.Join(proj, ".claude-sandbox", "config.yaml")
	})

	linkedFake := func() *execx.Fake {
		f := nonGitFake()
		common := filepath.Join(main, ".git")
		f.On(linkedRevParse, filepath.Join(common, "worktrees", "proj")+"\n"+common+"\n"+proj+"\n", nil)
		return f
	}

	It("CS-INIT-037: the main checkout's trackInHost is inherited, with its source, and the report lists its level", func() {
		r := &run{fake: linkedFake(), prompter: &prompt.Scripted{IsTTY: true, Answers: []string{""}}}
		Expect(r.init(proj, initcmd.Flags{})).To(Succeed())
		Expect(r.fake.CommandLines()).To(ContainElement(execx.GitSafePrefix + " -C " + proj + " " + linkedRevParse))
		Expect(r.prompter.Asked).To(ContainElement(ContainSubstring("[Enter=inherit true, y/n=override]")))
		Expect(r.errOut.String()).To(ContainSubstring(mainCfg))
		Expect(read(cfg)).To(ContainSubstring("# trackInHost: true   # inherited from " + mainCfg))
		Expect(r.out.String()).To(ContainSubstring(filepath.Join(main, ".claude-sandbox") + "/"))
	})

	It("CS-INIT-037: not a linked worktree (git answers nothing) reads the plain parent walk", func() {
		r := &run{prompter: &prompt.Scripted{IsTTY: true, Answers: []string{"n"}}}
		Expect(r.init(proj, initcmd.Flags{})).To(Succeed())
		Expect(r.prompter.Asked).NotTo(ContainElement(ContainSubstring("Enter=inherit")))
		Expect(r.out.String()).NotTo(ContainSubstring(filepath.Join(main, ".claude-sandbox")))
	})

	It("CS-INIT-037: a git timeout warns once and falls back to the plain parent walk", func() {
		f := linkedFake()
		f.GitBound = 50 * time.Millisecond
		f.OnHang("--git-common-dir")
		r := &run{fake: f, prompter: &prompt.Scripted{IsTTY: true, Answers: []string{"n"}}}
		done := make(chan error, 1)
		go func() {
			defer GinkgoRecover()
			done <- r.init(proj, initcmd.Flags{})
		}()
		var err error
		Eventually(done, 5*time.Second).Should(Receive(&err))
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Killed).To(Equal(1))
		Expect(strings.Count(r.errOut.String(), "did not finish within 50ms")).To(Equal(1))
		Expect(r.errOut.String()).To(ContainSubstring(
			"; reading the upstream configs as for a plain project, without the linked-worktree check.\n"))
		Expect(r.prompter.Asked).NotTo(ContainElement(ContainSubstring("Enter=inherit")))
		Expect(read(cfg)).To(MatchRegexp(`(?m)^trackInHost: false$`))
	})
})
