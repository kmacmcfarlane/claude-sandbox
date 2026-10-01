package layout_test

// Spec: spec/layout.feature CS-LAY-024 (every layout git call is bounded; a
// timed-out probe is an unknown answer) and CS-LAY-025 (the .gitignore files
// are written only inside the project tree).

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/layout"
	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
)

var _ = Describe("CS-LAY-024: bounded layout git calls", func() {
	var proj, sb, hostGI, sideGI, claudeMD string
	var fake *execx.Fake
	var sp *prompt.Scripted
	var out, errOut bytes.Buffer

	setup := func(track bool) error {
		return bounded(func() error {
			return layout.Setup(proj, track, layout.Options{
				Runner: fake, Prompter: sp, Out: &out, Err: &errOut, Gitignore: ptr(true),
			})
		})
	}
	gitCalls := func() []string { return fake.CommandLines() }
	timeoutWarning := func(cmd string) string {
		return "WARNING: git -C " + proj + " " + cmd + " did not finish within 50ms (a FIFO or other blocking file in its .git or a .gitignore can do this); "
	}

	BeforeEach(func() {
		os.Unsetenv("CS_GITIGNORE_ASSUME")
		proj = filepath.Join(GinkgoT().TempDir(), "p")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		sb = filepath.Join(proj, ".claude-sandbox")
		hostGI = filepath.Join(proj, ".gitignore")
		sideGI = filepath.Join(sb, ".gitignore")
		claudeMD = filepath.Join(sb, "CLAUDE.md")
		fake = &execx.Fake{GitBound: 50 * time.Millisecond}
		sp = &prompt.Scripted{IsTTY: true}
		out.Reset()
		errOut.Reset()
	})

	It("CS-LAY-024: a hung rev-parse is unknown: no later probe, no host .gitignore, no seed, no init — the sidecar .gitignore still", func() {
		fake.OnHang("rev-parse --is-inside-work-tree")
		Expect(setup(false)).To(Succeed())
		Expect(errOut.String()).To(Equal(timeoutWarning("rev-parse --is-inside-work-tree") +
			"skipping the .gitignore update and the git ignore checks, and so the sidecar git init.\n"))
		Expect(gitCalls()).To(HaveLen(1), "no later git call")
		Expect(hostGI).NotTo(BeAnExistingFile())
		Expect(claudeMD).NotTo(BeAnExistingFile(), "whether the host tracks files there is unknown")
		Expect(read(sideGI)).To(ContainSubstring("temp/"), "CS-LAY-004 still")
		Expect(fake.Killed).To(Equal(1))
	})

	It("CS-LAY-024: a hung ls-files is unknown the same way", func() {
		fake.OnHang("ls-files -z -- .claude-sandbox")
		Expect(setup(false)).To(Succeed())
		Expect(errOut.String()).To(HavePrefix(timeoutWarning("ls-files -z -- .claude-sandbox")))
		Expect(errOut.String()).NotTo(ContainSubstring("These entries are missing"))
		Expect(gitCalls()).To(HaveLen(2))
		Expect(hostGI).NotTo(BeAnExistingFile())
		Expect(claudeMD).NotTo(BeAnExistingFile())
	})

	It("CS-LAY-024: trackInHost true — a hung --no-index probe skips the host-tracked entries; the seed is written, no init clause", func() {
		fake.OnHang("check-ignore -q --no-index")
		Expect(setup(true)).To(Succeed())
		Expect(errOut.String()).To(Equal(timeoutWarning("check-ignore -q --no-index -- .claude-sandbox") +
			"skipping the .gitignore update and the git ignore checks.\n"))
		Expect(hostGI).NotTo(BeAnExistingFile())
		Expect(read(claudeMD)).NotTo(BeEmpty())
		Expect(sideGI).NotTo(BeAnExistingFile(), "trackInHost true writes no sidecar .gitignore")
	})

	It("CS-LAY-024: with host-tracked files a hung child probe prints the plain CS-LAY-020 warning and adds nothing", func() {
		fake.On("ls-files -z -- .claude-sandbox", ".claude-sandbox/config.yaml\x00", nil)
		fake.OnHang("check-ignore -q -- .claude-sandbox/ignore-probe")
		Expect(setup(false)).To(Succeed())
		Expect(errOut.String()).To(HavePrefix(timeoutWarning("check-ignore -q -- .claude-sandbox/ignore-probe")+
			"skipping the .gitignore update and the git ignore checks.\n"), "no init clause: host-tracked files skip it anyway")
		Expect(errOut.String()).To(ContainSubstring("the host repo already tracks 1 file under .claude-sandbox/; skipping"))
		Expect(errOut.String()).NotTo(ContainSubstring("hidden from git NOW"))
		Expect(hostGI).NotTo(BeAnExistingFile())
		Expect(strings.Count(strings.Join(gitCalls(), "\n"), "check-ignore")).To(Equal(1), "no later probe")
	})

	It("CS-LAY-024: a hung probe deciding the sidecar init skips only the init — the host .gitignore step is done", func() {
		fake.OnHang("check-ignore -q -- .claude-sandbox/ignore-probe")
		Expect(setup(false)).To(Succeed())
		Expect(read(hostGI)).To(ContainSubstring("/.claude-sandbox/"))
		Expect(errOut.String()).To(ContainSubstring(timeoutWarning("check-ignore -q -- .claude-sandbox/ignore-probe") +
			"skipping the sidecar git init.\n"))
		for _, l := range gitCalls() {
			Expect(l).NotTo(ContainSubstring(" init "))
		}
	})

	It("CS-LAY-024: a hung sidecar git init is killed with a warning naming the partial .git", func() {
		fake.On("check-ignore", "", nil) // the host ignores the directory
		fake.OnHang(" init -q")
		Expect(setup(false)).To(Succeed())
		Expect(errOut.String()).To(ContainSubstring("WARNING: git -C " + sb + " init -q did not finish within 50ms"))
		Expect(errOut.String()).To(ContainSubstring("no sidecar git repo; remove any partial " + filepath.Join(sb, ".git") +
			" and run 'git -C " + sb + " init' to create it."))
		Expect(out.String()).NotTo(ContainSubstring("Initialized sidecar"))
	})

	It("CS-LAY-024: init's HostTrackedCount and DirIgnored read a timeout as their old failure answer", func() {
		fake.OnHang("git ")
		var n int
		var ignored bool
		Expect(bounded(func() error {
			n = layout.HostTrackedCount(fake, proj)
			ignored = layout.DirIgnored(fake, proj)
			return nil
		})).To(Succeed())
		Expect(n).To(Equal(0))
		Expect(ignored).To(BeFalse())
		Expect(errOut.String()).To(BeEmpty())
	})

	It("CS-LAY-024: every layout git call starts with DieWithParent", func() {
		fake.On("check-ignore", "", nil)
		Expect(setup(false)).To(Succeed())
		Expect(fake.Calls).NotTo(BeEmpty())
		for _, c := range fake.Calls {
			Expect(c.DieWithParent).To(BeTrue(), "%s %v", c.Name, c.Args)
		}
	})
})

var _ = Describe("CS-LAY-025: .gitignore writes stay inside the project", func() {
	var base, proj, sb, hostGI, sideGI, outside string
	var fake *execx.Fake
	var sp *prompt.Scripted
	var out, errOut bytes.Buffer

	setup := func(track bool) error {
		return layout.Setup(proj, track, layout.Options{
			Runner: fake, Prompter: sp, Out: &out, Err: &errOut, Gitignore: ptr(true),
		})
	}

	BeforeEach(func() {
		os.Unsetenv("CS_GITIGNORE_ASSUME")
		base = GinkgoT().TempDir()
		proj = filepath.Join(base, "p")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		sb = filepath.Join(proj, ".claude-sandbox")
		Expect(os.MkdirAll(sb, 0o755)).To(Succeed())
		hostGI = filepath.Join(proj, ".gitignore")
		sideGI = filepath.Join(sb, ".gitignore")
		outside = filepath.Join(base, "bashrc")
		write(outside, "export X=1\n")
		fake = &execx.Fake{}
		fake.On("check-ignore", "", nil)
		sp = &prompt.Scripted{IsTTY: true}
		out.Reset()
		errOut.Reset()
	})

	It("CS-LAY-025: a host .gitignore linking out of the project is refused with one warning, before any prompt", func() {
		Expect(os.Symlink(outside, hostGI)).To(Succeed())
		Expect(setup(false)).To(Succeed())
		Expect(errOut.String()).To(ContainSubstring("WARNING: " + hostGI + " is a symlink to " + outside +
			", outside the project " + proj + "; skipping the .gitignore update (the launcher writes .gitignore lines only inside the project).\n"))
		Expect(errOut.String()).NotTo(ContainSubstring("These entries are missing"))
		Expect(read(outside)).To(Equal("export X=1\n"), "the target is untouched")
	})

	It("CS-LAY-025: a dangling link out of the project is refused too, and nothing is created there", func() {
		target := filepath.Join(base, "newfile")
		Expect(os.Symlink(target, hostGI)).To(Succeed())
		Expect(setup(true)).To(Succeed())
		Expect(errOut.String()).To(ContainSubstring("is a symlink to " + target + ", outside the project"))
		Expect(target).NotTo(BeAnExistingFile())
	})

	It("CS-LAY-025: a sidecar .gitignore linking out fails the setup naming it, and writes nothing", func() {
		Expect(os.Symlink(outside, sideGI)).To(Succeed())
		err := setup(false)
		Expect(err).To(MatchError(ContainSubstring(sideGI + " is a symlink to " + outside + ", outside the project")))
		Expect(read(outside)).To(Equal("export X=1\n"))
	})

	It("CS-LAY-025: a link inside the project is followed", func() {
		shared := filepath.Join(proj, "shared.gitignore")
		write(shared, "node_modules/\n")
		Expect(os.Symlink("shared.gitignore", hostGI)).To(Succeed())
		Expect(setup(false)).To(Succeed())
		Expect(read(shared)).To(ContainSubstring("/.claude-sandbox/"))
		fi, err := os.Lstat(hostGI)
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode()&os.ModeSymlink).NotTo(BeZero(), "the link itself stays")
	})

	It("CS-LAY-025: an ABSOLUTE link to a file inside the project is followed, host and sidecar", func() {
		shared := filepath.Join(proj, "shared.gitignore")
		write(shared, "node_modules/\n")
		Expect(os.Symlink(shared, hostGI)).To(Succeed())
		sideTarget := filepath.Join(sb, "real.gitignore")
		Expect(os.Symlink(sideTarget, sideGI)).To(Succeed())
		Expect(setup(false)).To(Succeed(), errOut.String())
		Expect(errOut.String()).NotTo(ContainSubstring("outside the project"))
		Expect(read(shared)).To(ContainSubstring("/.claude-sandbox/"))
		Expect(read(sideTarget)).To(ContainSubstring("temp/"), "a dangling absolute in-project link creates its target")
	})

	It("CS-LAY-025: a dangling link through a directory link out of the project is refused before the prompt", func() {
		Expect(os.Symlink("..", filepath.Join(proj, "up"))).To(Succeed())
		Expect(os.Symlink("up/newfile", hostGI)).To(Succeed())
		Expect(setup(true)).To(Succeed())
		Expect(errOut.String()).To(ContainSubstring("WARNING: " + hostGI + " is a symlink to " + filepath.Join(base, "newfile") +
			", outside the project " + proj + "; skipping the .gitignore update"))
		Expect(errOut.String()).NotTo(ContainSubstring("These entries are missing"), "refused before the prompt")
		Expect(filepath.Join(base, "newfile")).NotTo(BeAnExistingFile())

		Expect(os.Remove(hostGI)).To(Succeed())
		Expect(os.Symlink("../up/newfile", sideGI)).To(Succeed())
		errOut.Reset()
		err := setup(false)
		Expect(err).To(MatchError(ContainSubstring(sideGI + " is a symlink to " + filepath.Join(base, "newfile") + ", outside the project")))
		Expect(err).NotTo(MatchError(ContainSubstring("path escapes")), "the CS-LAY-025 refusal, not os.Root's raw error")
		Expect(filepath.Join(base, "newfile")).NotTo(BeAnExistingFile())
	})

	It("CS-LAY-025: the writer opens through the project root, so a link re-pointed out after the check cannot write outside", func() {
		gi := filepath.Join(proj, "gi")
		Expect(os.Symlink(outside, gi)).To(Succeed())
		err := layout.WriteRegularFile(proj, gi, []byte("x\n"))
		Expect(err).To(HaveOccurred())
		Expect(read(outside)).To(Equal("export X=1\n"))

		// Through a directory link too.
		Expect(os.Symlink(base, filepath.Join(proj, "up"))).To(Succeed())
		err = layout.WriteRegularFile(proj, filepath.Join(proj, "up", "bashrc"), []byte("x\n"))
		Expect(err).To(HaveOccurred())
		Expect(read(outside)).To(Equal("export X=1\n"))
	})
})
