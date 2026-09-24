package main

// Spec: spec/launch.feature (CS-LNCH-070..075) and spec/config-cascade.feature
// (CS-CASC-031..033) — a project that is a linked git worktree, end to end
// through MainWithEnv. The fixture's project directory stands in for a Paseo
// worktree outside the repository; the main checkout is a sibling tree with a
// real .git/worktrees/<name>/gitdir back-link on disk, and git's rev-parse
// answer is scripted.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/imagebuild"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
	"github.com/kmacmcfarlane/claude-sandbox/internal/paths"
)

const linkedRevParse = "rev-parse --git-dir --git-common-dir --show-toplevel"

type linkedFixture struct {
	*cliFixture
	main, common, gitDir string
}

// newLinkedFixture makes f.proj a verified linked worktree of <base>/ws/repo,
// whose .claude-sandbox/ sets dangerous and holds an env file.
func newLinkedFixture() *linkedFixture {
	f := newCLIFixture()
	base := filepath.Dir(f.proj)
	l := &linkedFixture{cliFixture: f, main: filepath.Join(base, "ws", "repo")}
	l.common = filepath.Join(l.main, ".git")
	l.gitDir = filepath.Join(l.common, "worktrees", "proj")
	writeFile(filepath.Join(l.gitDir, "gitdir"), f.proj+"/.git\n")
	writeFile(filepath.Join(f.proj, ".git"), "gitdir: "+l.gitDir+"\n")
	writeFile(filepath.Join(l.main, ".claude-sandbox", "config.yaml"), "dangerous: true\n")
	writeFile(filepath.Join(l.main, ".claude-sandbox", "env"), "FROM_MAIN=1\n")
	f.fake.On(linkedRevParse, l.gitDir+"\n"+l.common+"\n"+f.proj+"\n", nil)
	return l
}

func (l *linkedFixture) commonMount() string { return l.common + ":" + l.common }

var _ = Describe("linked git worktree (CS-LNCH-070..075, CS-CASC-031..033)", func() {
	var l *linkedFixture
	BeforeEach(func() { l = newLinkedFixture() })

	It("CS-LNCH-070, CS-CASC-031, CS-CASC-032: the main checkout's config and env apply, and the report lists its level", func() {
		Expect(l.run()).To(Equal(0), l.errw.String())
		Expect(l.fake.CommandLines()).To(ContainElement("git -C " + l.proj + " " + linkedRevParse))
		Expect(l.launchLine()).To(MatchRegexp(` claude --dangerously-skip-permissions$`), "dangerous: true from the main checkout")
		args := l.launched().Args
		Expect(envFileContents(args)).To(Equal([]string{"FROM_MAIN=1\n"}))
		Expect(l.out.String()).To(ContainSubstring("Sandbox config cascade"))
		Expect(l.out.String()).To(ContainSubstring(filepath.Join(l.main, ".claude-sandbox") + "/  →  config.yaml env"))
	})

	It("CS-CASC-031: the worktree's own .claude-sandbox/ is more local than the main checkout's", func() {
		writeFile(filepath.Join(l.proj, ".claude-sandbox", "config.yaml"), "dangerous: false\n")
		Expect(l.run()).To(Equal(0), l.errw.String())
		Expect(l.launchLine()).NotTo(ContainSubstring("--dangerously-skip-permissions"))
		out := l.out.String()
		Expect(strings.Index(out, filepath.Join(l.main, ".claude-sandbox")+"/")).To(BeNumerically("<",
			strings.Index(out, filepath.Join(l.proj, ".claude-sandbox")+"/")))
	})

	It("CS-CASC-033: the main checkout's child Dockerfile builds with the main checkout as context", func() {
		delete(l.envmap, "CLAUDE_SANDBOX_BASE_ONLY")
		df := filepath.Join(l.main, ".claude-sandbox", "Dockerfile")
		writeFile(df, "FROM claude-sandbox\n")
		Expect(l.run()).To(Equal(0), l.errw.String())
		want := "claude-sandbox-" + imagebuild.ImageSlug(df, l.main)
		Expect(l.launchLine()).To(ContainSubstring(" " + want + ":run "))
		Expect(l.out.String()).To(ContainSubstring("Found Dockerfile in main checkout: " + l.main))
	})

	It("CS-LNCH-071, CS-LNCH-072, CS-LNCH-073: the common git dir is mounted rw, one banner names the main checkout, identity stays on the worktree", func() {
		Expect(l.run()).To(Equal(0), l.errw.String())
		args := l.launched().Args
		Expect(args).To(ContainElement(l.commonMount()))
		Expect(strings.Count(l.out.String(), "Linked worktree:")).To(Equal(1))
		Expect(l.out.String()).To(ContainSubstring("Linked worktree: main checkout " + l.main +
			" (its .claude-sandbox/ config, env and Dockerfile apply); git dir " + l.common + " mounted\n"))
		Expect(args).To(ContainElements("-w", l.proj))
		Expect(args).To(ContainElement(l.proj + ":" + l.proj))
		Expect(args).To(ContainElement("claude-sandbox.project=" + l.proj))
		Expect(args).To(ContainElement("CLAUDE_SANDBOX_PROJECT_DIR=" + l.proj))
		Expect(l.launchLine()).To(MatchRegexp(`--name claude-sandbox-` + regexp.QuoteMeta(imagebuild.ProjectSlug(l.proj)) + `-[a-z]+ `))
	})

	It("CS-LNCH-071: a cascade same-path mount that already covers the common dir wins", func() {
		writeFile(filepath.Join(l.main, ".claude-sandbox", "config.yaml"),
			"mounts:\n  - host: "+l.main+"\n    container: "+l.main+"\n")
		Expect(l.run()).To(Equal(0), l.errw.String())
		args := l.launched().Args
		Expect(args).To(ContainElement(l.main + ":" + l.main + ":ro"))
		Expect(args).NotTo(ContainElement(l.commonMount()))
	})

	It("CS-LNCH-071: from a subdirectory of the worktree the git dir is not mounted", func() {
		sub := filepath.Join(l.proj, "sub")
		Expect(os.MkdirAll(sub, 0o755)).To(Succeed())
		l.envmap["PROJECT_DIR"] = sub
		Expect(l.run()).To(Equal(0), l.errw.String())
		Expect(l.launched().Args).NotTo(ContainElement(l.commonMount()))
		Expect(l.launchLine()).To(ContainSubstring("--dangerously-skip-permissions"), "the cascade still comes from the main checkout")
		Expect(l.out.String()).To(ContainSubstring("Linked worktree: main checkout " + l.main + " (its .claude-sandbox/ config, env and Dockerfile apply)\n"))
	})

	It("CS-LNCH-070: an unverified back-link warns once and launches as a plain project", func() {
		writeFile(filepath.Join(l.gitDir, "gitdir"), "/somewhere/else/.git\n")
		Expect(l.run()).To(Equal(0), l.errw.String())
		Expect(l.errw.String()).To(ContainSubstring("git worktree repair"))
		Expect(l.out.String()).NotTo(ContainSubstring("Linked worktree:"))
		Expect(l.launched().Args).NotTo(ContainElement(l.commonMount()))
		Expect(l.launchLine()).NotTo(ContainSubstring("--dangerously-skip-permissions"))
	})

	It("CS-LNCH-070: a git dir outside <common>/worktrees is not trusted, silently", func() {
		// A crafted .git file naming another repository's git dir: the
		// back-link even matches, but the git dir is not one of the common
		// dir's registered worktrees.
		f := newCLIFixture()
		other := filepath.Join(filepath.Dir(f.proj), "victim", ".git")
		common := filepath.Join(filepath.Dir(f.proj), "ws", ".git")
		writeFile(filepath.Join(other, "gitdir"), f.proj+"/.git\n")
		Expect(os.MkdirAll(common, 0o755)).To(Succeed())
		f.fake.On(linkedRevParse, other+"\n"+common+"\n"+f.proj+"\n", nil)
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(f.out.String()).NotTo(ContainSubstring("Linked worktree:"))
		Expect(strings.Join(f.launched().Args, " ")).NotTo(ContainSubstring("/.git:"))
	})

	It("CS-LNCH-075: ralph and headless launches get the same cascade, mount and banner", func() {
		Expect(l.run("--ralph", "--no-worktree")).To(Equal(0), l.errw.String())
		Expect(l.launched().Args).To(ContainElement(l.commonMount()))
		Expect(l.launchLine()).To(HaveSuffix("/opt/claude-sandbox/bin/ralph --dangerously-skip-permissions"))
		Expect(l.out.String()).To(ContainSubstring("Linked worktree: main checkout " + l.main))

		h := newLinkedFixture()
		Expect(h.run("headless", "--")).To(Equal(0), h.errw.String())
		Expect(h.launched().Args).To(ContainElement(h.commonMount()))
		Expect(h.launchLine()).To(ContainSubstring("--dangerously-skip-permissions"))
		Expect(h.out.String()).NotTo(ContainSubstring("Linked worktree:"), "headless stdout is claude's")
		Expect(h.errw.String()).To(ContainSubstring("Linked worktree: main checkout " + h.main))
	})

	It("CS-LNCH-074: attach/join's would-be fingerprint matches the linked launch", func() {
		Expect(l.run()).To(Equal(0), l.errw.String())
		var launched string
		for _, a := range l.launched().Args {
			if v, ok := strings.CutPrefix(a, "claude-sandbox.confighash="); ok {
				launched = v
			}
		}
		Expect(launched).NotTo(BeEmpty())

		fl, err := scanLaunchArgs(nil)
		Expect(err).NotTo(HaveOccurred())
		linked, warn := launch.DetectLinkedWorktree(l.fake, l.proj)
		Expect(warn).To(BeEmpty())
		Expect(linked).NotTo(BeNil())
		chain := paths.Chain(l.proj, linked.Main)
		configFiles, _ := paths.CollectChain(chain, paths.Config)
		envFiles, _ := paths.CollectChain(chain, paths.Env)
		cfg, err := cascade.Load(configFiles)
		Expect(err).NotTo(HaveOccurred())
		hash, _ := wouldBeFingerprint(l.env, l.proj, fl, cfg, envFiles, linked)
		Expect(hash).To(Equal(launched))
		without, _ := wouldBeFingerprint(l.env, l.proj, fl, cfg, envFiles, nil)
		Expect(without).NotTo(Equal(launched), "the git dir mount is part of the fingerprint")
	})

	It("CS-LNCH-072, CS-LNCH-073: a plain launch runs the detection but prints and mounts nothing new", func() {
		p := newCLIFixture()
		Expect(p.run()).To(Equal(0), p.errw.String())
		Expect(p.out.String()).NotTo(ContainSubstring("Linked worktree:"))
		Expect(strings.Join(p.launched().Args, " ")).NotTo(ContainSubstring(".git"))
	})
})
