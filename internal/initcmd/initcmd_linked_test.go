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
		Expect(r.errOut.String()).To(ContainSubstring("Upstream config sets trackInHost: true (inherited from " + mainCfg + ")"))
		Expect(read(cfg)).To(ContainSubstring("# trackInHost: true   # inherited from " + mainCfg))
		Expect(r.out.String()).To(ContainSubstring(filepath.Join(main, ".claude-sandbox") + "/"))
	})

	It("CS-INIT-037: not a linked worktree (git answers nothing) reads the plain parent walk", func() {
		r := &run{prompter: &prompt.Scripted{IsTTY: true, Answers: []string{"n"}}}
		Expect(r.init(proj, initcmd.Flags{})).To(Succeed())
		Expect(r.prompter.Asked).NotTo(ContainElement(ContainSubstring("Enter=inherit")))
		Expect(r.out.String()).NotTo(ContainSubstring(filepath.Join(main, ".claude-sandbox")))
	})

	It("CS-INIT-037: a back-link naming another path warns once and reads the plain parent walk", func() {
		write(filepath.Join(main, ".git", "worktrees", "proj", "gitdir"), "/somewhere/else/.git\n")
		r := &run{fake: linkedFake(), prompter: &prompt.Scripted{IsTTY: true, Answers: []string{"n"}}}
		Expect(r.init(proj, initcmd.Flags{})).To(Succeed())
		Expect(strings.Count(r.errOut.String(), "points elsewhere")).To(Equal(1))
		Expect(r.errOut.String()).To(ContainSubstring(
			"points elsewhere; reading the upstream configs without the main checkout's, so trackInHost is not inherited from it. Run 'git worktree repair' in " + proj))
		Expect(r.prompter.Asked).NotTo(ContainElement(ContainSubstring("Enter=inherit")))
		Expect(r.out.String()).NotTo(ContainSubstring(filepath.Join(main, ".claude-sandbox")))
		Expect(read(cfg)).To(MatchRegexp(`(?m)^trackInHost: false$`))
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

// Spec: spec/init.feature CS-INIT-038 — the upstream-env note and the
// parent-Dockerfile copy read the same chain as CS-INIT-037, from the one
// linked-worktree detection.
var _ = Describe("CS-INIT-038: init's env note and Dockerfile copy read the launch's chain", func() {
	var base, proj, main, mainEnv, mainDF, example string

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
		mainEnv = filepath.Join(main, ".claude-sandbox", "env")
		write(mainEnv, "FOO=bar\n")
		mainDF = filepath.Join(main, ".claude-sandbox", "Dockerfile")
		write(mainDF, "FROM claude-sandbox\n# main checkout's child\n")
		example = filepath.Join(proj, ".claude-sandbox", "Dockerfile.example")
	})

	linkedFake := func() *execx.Fake {
		f := nonGitFake()
		common := filepath.Join(main, ".git")
		f.On(linkedRevParse, filepath.Join(common, "worktrees", "proj")+"\n"+common+"\n"+proj+"\n", nil)
		return f
	}

	countRevParse := func(f *execx.Fake) int {
		n := 0
		for _, l := range f.CommandLines() {
			if strings.Contains(l, linkedRevParse) {
				n++
			}
		}
		return n
	}

	It("CS-INIT-038: a linked worktree notes the main checkout's env and copies its Dockerfile, with one detection", func() {
		r := &run{fake: linkedFake(), prompter: &prompt.Scripted{IsTTY: true, Answers: []string{"n"}}}
		Expect(r.init(proj, initcmd.Flags{})).To(Succeed())
		Expect(countRevParse(r.fake)).To(Equal(1))
		Expect(r.out.String()).To(ContainSubstring("note: " + mainEnv + " is inherited by this project (a project env would override its keys)"))
		Expect(read(example)).To(Equal("FROM claude-sandbox\n# main checkout's child\n"))
		Expect(r.out.String()).To(ContainSubstring("created  Dockerfile.example (copied from " + mainDF + ";"))
	})

	It("CS-INIT-038: --no-copy-parent-dockerfile still seeds the generic example in a linked worktree", func() {
		no := false
		r := &run{fake: linkedFake(), prompter: &prompt.Scripted{IsTTY: true, Answers: []string{"n"}}}
		Expect(r.init(proj, initcmd.Flags{CopyParentDockerfile: &no})).To(Succeed())
		Expect(read(example)).NotTo(ContainSubstring("main checkout's child"))
	})

	It("CS-INIT-038: not a linked worktree reads the plain parent walk (no main checkout env note, generic example)", func() {
		r := &run{prompter: &prompt.Scripted{IsTTY: true, Answers: []string{"n"}}}
		Expect(r.init(proj, initcmd.Flags{})).To(Succeed())
		Expect(r.out.String()).NotTo(ContainSubstring(mainEnv))
		Expect(read(example)).NotTo(ContainSubstring("main checkout's child"))
	})

	It("CS-INIT-038: a mismatched back-link falls back to the plain parent walk for the env note and the copy", func() {
		write(filepath.Join(main, ".git", "worktrees", "proj", "gitdir"), "/somewhere/else/.git\n")
		r := &run{fake: linkedFake(), prompter: &prompt.Scripted{IsTTY: true, Answers: []string{"n"}}}
		Expect(r.init(proj, initcmd.Flags{})).To(Succeed())
		Expect(r.out.String()).NotTo(ContainSubstring(mainEnv))
		Expect(read(example)).NotTo(ContainSubstring("main checkout's child"))
	})

	It("CS-INIT-038: a plain repository still notes and copies its own parents' files", func() {
		ws := filepath.Join(base, "ws")
		wsEnv := filepath.Join(ws, ".claude-sandbox", "env")
		write(wsEnv, "X=1\n")
		write(filepath.Join(ws, ".claude-sandbox", "Dockerfile"), "FROM claude-sandbox\n# workspace\n")
		plain := filepath.Join(ws, "plain")
		mkdir(plain)
		r := &run{prompter: &prompt.Scripted{IsTTY: true, Answers: []string{"n"}}}
		Expect(r.init(plain, initcmd.Flags{})).To(Succeed())
		Expect(r.out.String()).To(ContainSubstring("note: " + wsEnv + " is inherited"))
		Expect(read(filepath.Join(plain, ".claude-sandbox", "Dockerfile.example"))).To(ContainSubstring("# workspace"))
	})
})
