package launch_test

// Spec: spec/launch.feature (CS-LNCH-070, CS-LNCH-071) — linked worktree
// detection, its verification, and the common git dir mount.

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
)

const revParse = "rev-parse --git-dir --git-common-dir --show-toplevel"

func write(p, s string) {
	Expect(os.MkdirAll(filepath.Dir(p), 0o755)).To(Succeed())
	Expect(os.WriteFile(p, []byte(s), 0o644)).To(Succeed())
}

var _ = Describe("linked worktree detection (CS-LNCH-070)", func() {
	var base, wt, common, gitDir string
	var fake *execx.Fake

	BeforeEach(func() {
		var err error
		base, err = filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		wt = filepath.Join(base, "paseo", "feat")
		common = filepath.Join(base, "repo", ".git")
		gitDir = filepath.Join(common, "worktrees", "feat")
		write(filepath.Join(gitDir, "gitdir"), wt+"/.git\n")
		Expect(os.MkdirAll(wt, 0o755)).To(Succeed())
		fake = &execx.Fake{}
		fake.On(revParse, gitDir+"\n"+common+"\n"+wt+"\n", nil)
	})

	It("CS-LNCH-070: one git call; a verified worktree yields top, git dir, common dir and main checkout", func() {
		lw, warn := launch.DetectLinkedWorktree(fake, wt)
		Expect(warn).To(BeEmpty())
		Expect(lw).To(Equal(&launch.LinkedWorktree{Top: wt, GitDir: gitDir, CommonDir: common, Main: filepath.Join(base, "repo")}))
		Expect(fake.CommandLines()).To(Equal([]string{"git -C " + wt + " " + revParse}))
	})

	It("CS-LNCH-070: a main checkout (git dir == common dir) is not linked", func() {
		f := &execx.Fake{}
		f.On(revParse, ".git\n.git\n"+wt+"\n", nil)
		Expect(os.MkdirAll(filepath.Join(wt, ".git"), 0o755)).To(Succeed())
		lw, warn := launch.DetectLinkedWorktree(f, wt)
		Expect(lw).To(BeNil())
		Expect(warn).To(BeEmpty())
	})

	It("CS-LNCH-070: git failing (not a repository) is not linked", func() {
		f := &execx.Fake{}
		f.On(revParse, "", execx.Fail(128))
		lw, warn := launch.DetectLinkedWorktree(f, wt)
		Expect(lw).To(BeNil())
		Expect(warn).To(BeEmpty())
	})

	It("CS-LNCH-070: a missing back-link is not trusted", func() {
		Expect(os.Remove(filepath.Join(gitDir, "gitdir"))).To(Succeed())
		lw, _ := launch.DetectLinkedWorktree(fake, wt)
		Expect(lw).To(BeNil())
	})

	It("CS-LNCH-070: a back-link naming another place warns with the repair command", func() {
		write(filepath.Join(gitDir, "gitdir"), "/moved/away/.git\n")
		lw, warn := launch.DetectLinkedWorktree(fake, wt)
		Expect(lw).To(BeNil())
		Expect(warn).To(ContainSubstring("git worktree repair"))
	})

	It("CS-LNCH-070: a back-link written through a symlink still verifies", func() {
		link := filepath.Join(base, "link")
		Expect(os.Symlink(filepath.Join(base, "paseo"), link)).To(Succeed())
		write(filepath.Join(gitDir, "gitdir"), filepath.Join(link, "feat", ".git")+"\n")
		write(filepath.Join(wt, ".git"), "gitdir: "+gitDir+"\n")
		lw, warn := launch.DetectLinkedWorktree(fake, wt)
		Expect(warn).To(BeEmpty())
		Expect(lw).NotTo(BeNil())
	})

	It("CS-LNCH-070: a bare (or --separate-git-dir) common dir has no main checkout, and the banner does not guess", func() {
		bare := filepath.Join(base, "bare.git")
		bgd := filepath.Join(bare, "worktrees", "feat")
		write(filepath.Join(bgd, "gitdir"), wt+"/.git\n")
		f := &execx.Fake{}
		f.On(revParse, bgd+"\n"+bare+"\n"+wt+"\n", nil)
		lw, _ := launch.DetectLinkedWorktree(f, wt)
		Expect(lw).NotTo(BeNil())
		Expect(lw.Main).To(BeEmpty())
		Expect(lw.Banner(true)).To(Equal("Linked worktree: repository git dir " + bare + " (no main checkout found); git dir " + bare + " mounted"))
	})

	It("CS-LNCH-070: relative rev-parse output resolves against the directory", func() {
		f := &execx.Fake{}
		f.On(revParse, "../repo/.git/worktrees/feat\n../repo/.git\n"+wt+"\n", nil)
		lw, _ := launch.DetectLinkedWorktree(f, filepath.Join(base, "paseo"))
		Expect(lw).NotTo(BeNil())
		Expect(lw.CommonDir).To(Equal(common))
	})

	It("CS-LNCH-070: a directory that declares itself a git dir inside an untrusted clone is refused", func() {
		// The reviewer's layout: <clone>/worktrees/proj holds HEAD, commondir
		// (../..), gitdir (naming <self>/.git) and a .git file naming itself;
		// the clone root holds objects/ and refs/. git answers git dir =
		// top = <clone>/worktrees/proj, common dir = <clone>.
		clone := filepath.Join(base, "clone")
		self := filepath.Join(clone, "worktrees", "proj")
		write(filepath.Join(self, "HEAD"), "ref: refs/heads/main\n")
		write(filepath.Join(self, "commondir"), "../..\n")
		write(filepath.Join(self, "gitdir"), self+"/.git\n")
		write(filepath.Join(self, ".git"), "gitdir: "+self+"\n")
		Expect(os.MkdirAll(filepath.Join(clone, "objects"), 0o755)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(clone, "refs"), 0o755)).To(Succeed())
		f := &execx.Fake{}
		f.On(revParse, self+"\n"+clone+"\n"+self+"\n", nil)
		lw, warn := launch.DetectLinkedWorktree(f, self)
		Expect(lw).To(BeNil())
		Expect(warn).To(BeEmpty())
	})

	It("CS-LNCH-070: a worktree inside its own common dir is refused (the git dir is a sibling, not inside the top)", func() {
		clone := filepath.Join(base, "clone")
		gd := filepath.Join(clone, "worktrees", "fake")
		top := filepath.Join(clone, "worktrees", "proj")
		write(filepath.Join(gd, "gitdir"), top+"/.git\n")
		Expect(os.MkdirAll(top, 0o755)).To(Succeed())
		f := &execx.Fake{}
		f.On(revParse, gd+"\n"+clone+"\n"+top+"\n", nil)
		lw, _ := launch.DetectLinkedWorktree(f, top)
		Expect(lw).To(BeNil())
	})

	It("CS-LNCH-070: a common dir inside the worktree is refused", func() {
		inner := filepath.Join(wt, "inner.git")
		igd := filepath.Join(inner, "worktrees", "feat")
		write(filepath.Join(igd, "gitdir"), wt+"/.git\n")
		f := &execx.Fake{}
		f.On(revParse, igd+"\n"+inner+"\n"+wt+"\n", nil)
		lw, _ := launch.DetectLinkedWorktree(f, wt)
		Expect(lw).To(BeNil())
	})

	It("CS-LNCH-070: a relative back-link (git worktree.useRelativePaths) resolves against the git dir", func() {
		rel, err := filepath.Rel(gitDir, filepath.Join(wt, ".git"))
		Expect(err).NotTo(HaveOccurred())
		write(filepath.Join(gitDir, "gitdir"), rel+"\n")
		lw, warn := launch.DetectLinkedWorktree(fake, wt)
		Expect(warn).To(BeEmpty())
		Expect(lw).NotTo(BeNil())
		Expect(lw.CommonDir).To(Equal(common))
	})

	It("CS-LNCH-070: a relative back-link naming another place still warns", func() {
		write(filepath.Join(gitDir, "gitdir"), "../../../../elsewhere/.git\n")
		lw, warn := launch.DetectLinkedWorktree(fake, wt)
		Expect(lw).To(BeNil())
		Expect(warn).To(ContainSubstring("git worktree repair"))
	})

	Describe("the common git dir mount (CS-LNCH-071)", func() {
		inputs := func(project string, lw *launch.LinkedWorktree, cfg *cascade.Config) launch.Inputs {
			home := filepath.Join(base, "home")
			Expect(os.MkdirAll(home, 0o755)).To(Succeed())
			return launch.Inputs{
				ProjectDir: project, Home: home, HostUID: 1000, HostGID: 1000, HostUser: "t",
				Getenv:  func(string) string { return "" },
				TempDir: GinkgoT().TempDir(), ImageName: "img", Linked: lw, Cfg: cfg,
				Out: io.Discard, Err: io.Discard,
			}
		}
		lw := func() *launch.LinkedWorktree {
			return &launch.LinkedWorktree{Top: wt, GitDir: gitDir, CommonDir: common, Main: filepath.Join(base, "repo")}
		}

		It("CS-LNCH-071: is added read-write at its own path, after the project mount, and changes the fingerprint", func() {
			with, err := launch.Build(inputs(wt, lw(), nil))
			Expect(err).NotTo(HaveOccurred())
			Expect(with.Volumes).To(ContainElement(common + ":" + common))
			Expect(with.Volumes[0]).To(Equal(wt + ":" + wt))
			without, err := launch.Build(inputs(wt, nil, nil))
			Expect(err).NotTo(HaveOccurred())
			Expect(without.Volumes).NotTo(ContainElement(common + ":" + common))
			Expect(with.ConfigHash).NotTo(Equal(without.ConfigHash))
		})

		It("CS-LNCH-071: is omitted when a same-path cascade mount covers it", func() {
			repo := filepath.Join(base, "repo")
			p, err := launch.Build(inputs(wt, lw(), &cascade.Config{Mounts: []cascade.Mount{{Host: repo, Container: repo}}}))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Volumes).NotTo(ContainElement(common + ":" + common))
		})

		It("CS-LNCH-071: a read-only covering mount wins but warns that git cannot write", func() {
			repo := filepath.Join(base, "repo")
			in := inputs(wt, lw(), &cascade.Config{Mounts: []cascade.Mount{{Host: repo, Container: repo}}})
			var errw strings.Builder
			in.Err = &errw
			p, err := launch.Build(in)
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Volumes).NotTo(ContainElement(common + ":" + common))
			Expect(errw.String()).To(ContainSubstring("git dir " + common + " is under the read-only mount " + repo + ":" + repo + ":ro"))
		})

		It("CS-LNCH-071: a writable covering mount wins silently", func() {
			repo := filepath.Join(base, "repo")
			in := inputs(wt, lw(), &cascade.Config{Mounts: []cascade.Mount{{Host: repo, Container: repo, Writable: true}}})
			var errw strings.Builder
			in.Err = &errw
			_, err := launch.Build(in)
			Expect(err).NotTo(HaveOccurred())
			Expect(errw.String()).NotTo(ContainSubstring("read-only mount"))
		})

		It("CS-LNCH-071: a mount at another container path does not count as covering it", func() {
			repo := filepath.Join(base, "repo")
			p, err := launch.Build(inputs(wt, lw(), &cascade.Config{Mounts: []cascade.Mount{{Host: repo, Container: "/elsewhere"}}}))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Volumes).To(ContainElement(common + ":" + common))
		})

		It("CS-LNCH-071: is omitted from a subdirectory of the worktree", func() {
			sub := filepath.Join(wt, "sub")
			Expect(os.MkdirAll(sub, 0o755)).To(Succeed())
			p, err := launch.Build(inputs(sub, lw(), nil))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Volumes).NotTo(ContainElement(common + ":" + common))
		})
	})
})
