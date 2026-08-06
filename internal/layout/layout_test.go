package layout_test

// Spec: spec/layout.feature (CS-LAY-001..014). CS-LAY-015/016 (launcher
// adoption) live in cmd/claude-sandbox. Git behavior is scripted through
// execx.Fake: unmatched commands succeed, so by default the project IS a git
// work tree and check-ignore reports the path as ignored.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/layout"
	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
)

func write(p, content string) {
	Expect(os.MkdirAll(filepath.Dir(p), 0o755)).To(Succeed())
	Expect(os.WriteFile(p, []byte(content), 0o644)).To(Succeed())
}

func read(p string) string {
	raw, err := os.ReadFile(p)
	Expect(err).NotTo(HaveOccurred())
	return string(raw)
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func ptr(b bool) *bool { return &b }

// countLine counts exact-line occurrences of line in content.
func countLine(content, line string) int {
	n := 0
	for _, l := range strings.Split(content, "\n") {
		if l == line {
			n++
		}
	}
	return n
}

var _ = Describe("layout lifecycle", func() {
	var proj, sb, hostGI string
	var fake *execx.Fake
	var sp *prompt.Scripted
	var out, errOut bytes.Buffer

	setup := func(track bool, gitignore *bool) error {
		return layout.Setup(proj, track, layout.Options{
			Runner: fake, Prompter: sp, Out: &out, Err: &errOut, Gitignore: gitignore,
		})
	}

	BeforeEach(func() {
		os.Unsetenv("CS_GITIGNORE_ASSUME")
		proj = filepath.Join(GinkgoT().TempDir(), "p")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		sb = filepath.Join(proj, ".claude-sandbox")
		hostGI = filepath.Join(proj, ".gitignore")
		fake = &execx.Fake{}
		sp = &prompt.Scripted{}
		out.Reset()
		errOut.Reset()
	})

	It("CS-LAY-001: skeleton directories are created", func() {
		Expect(setup(false, ptr(true))).To(Succeed())
		Expect(exists(filepath.Join(sb, "temp"))).To(BeTrue())
		Expect(exists(filepath.Join(sb, "reports"))).To(BeTrue())
		Expect(exists(filepath.Join(sb, "investigations"))).To(BeTrue())
	})

	It("CS-LAY-002: CLAUDE.md is seeded once and never overwritten", func() {
		fake.On("rev-parse --is-inside-work-tree", "", execx.Fail(1))
		Expect(setup(false, nil)).To(Succeed())

		claudeMD := filepath.Join(sb, "CLAUDE.md")
		content := read(claudeMD)
		Expect(content).To(ContainSubstring(".claude-sandbox/"))
		Expect(content).To(ContainSubstring("sidecar"))
		Expect(content).To(ContainSubstring("git -C .claude-sandbox add -A"))

		By("user edits are preserved on re-run")
		write(claudeMD, "my edited notes\n")
		Expect(setup(false, nil)).To(Succeed())
		Expect(read(claudeMD)).To(Equal("my edited notes\n"))
	})

	Describe("trackInHost = false (foreign-safe, sidecar repo)", func() {
		It("CS-LAY-003: foreign-safe mode gitignores the whole directory in the host repo", func() {
			Expect(setup(false, ptr(true))).To(Succeed())
			Expect(countLine(read(hostGI), "/.claude-sandbox/")).To(Equal(1))
		})

		It("CS-LAY-004: the sidecar keeps its own .gitignore, appended without prompting", func() {
			Expect(setup(false, ptr(true))).To(Succeed())

			content := read(filepath.Join(sb, ".gitignore"))
			for _, line := range []string{"temp/", "env", "ralph/"} {
				Expect(countLine(content, line)).To(Equal(1), line)
			}
			By("still exactly once after a second run")
			Expect(setup(false, ptr(true))).To(Succeed())
			content = read(filepath.Join(sb, ".gitignore"))
			for _, line := range []string{"temp/", "env", "ralph/"} {
				Expect(countLine(content, line)).To(Equal(1), line)
			}
			// The only prompt-eligible edit is the HOST .gitignore; the sidecar's
			// own file was written even though no prompt ever fired.
			Expect(sp.Asked).To(BeEmpty())
		})

		It("CS-LAY-005: sidecar git repo is initialized when the host ignores the directory", func() {
			// Default fake: check-ignore succeeds => /.claude-sandbox/ is ignored.
			Expect(setup(false, ptr(true))).To(Succeed())
			Expect(fake.CommandLines()).To(ContainElement("git -C " + sb + " init -q"))
			Expect(out.String()).To(ContainSubstring("Initialized sidecar git repo at " + sb))
		})

		It("CS-LAY-006: sidecar init is skipped when the host would track the directory", func() {
			fake.On("check-ignore", "", execx.Fail(1))
			Expect(setup(false, ptr(false))).To(Succeed())

			Expect(fake.CommandLines()).NotTo(ContainElement(ContainSubstring(" init -q")))
			Expect(errOut.String()).To(ContainSubstring("skipping sidecar git init"))
			Expect(errOut.String()).To(ContainSubstring("Add /.claude-sandbox/ to .gitignore to enable sidecar history"))
		})

		It("CS-LAY-007: outside a git work tree the sidecar is still initialized", func() {
			fake.On("rev-parse --is-inside-work-tree", "", execx.Fail(1))
			Expect(setup(false, nil)).To(Succeed())

			Expect(exists(hostGI)).To(BeFalse(), "host .gitignore must not be touched")
			Expect(fake.CommandLines()).To(ContainElement("git -C " + sb + " init -q"))
		})

		It("CS-LAY-008: an existing sidecar repo is left alone", func() {
			Expect(os.MkdirAll(filepath.Join(sb, ".git"), 0o755)).To(Succeed())
			Expect(setup(false, ptr(true))).To(Succeed())
			Expect(fake.CommandLines()).NotTo(ContainElement(ContainSubstring(" init -q")))
		})
	})

	Describe("trackInHost = true (host-tracked, no sidecar)", func() {
		It("CS-LAY-009: host-tracked mode gitignores only ephemeral content", func() {
			Expect(setup(true, ptr(true))).To(Succeed())

			content := read(hostGI)
			for _, line := range []string{
				".claude-sandbox/env", ".claude-sandbox/temp/", ".claude-sandbox/ralph/",
				"!.claude-sandbox/config.yaml", "!.claude-sandbox/Dockerfile",
			} {
				Expect(countLine(content, line)).To(Equal(1), line)
			}
			Expect(exists(filepath.Join(sb, ".gitignore"))).To(BeFalse())
			Expect(fake.CommandLines()).NotTo(ContainElement(ContainSubstring(" init -q")))
		})
	})

	Describe("gitignore editing mechanics", func() {
		It("CS-LAY-010: only missing lines are proposed, matched exactly", func() {
			write(hostGI, ".claude-sandbox/env\n")
			Expect(setup(true, ptr(false))).To(Succeed())

			proposal := errOut.String()
			Expect(proposal).To(ContainSubstring("  .claude-sandbox/temp/\n"))
			Expect(proposal).To(ContainSubstring("  .claude-sandbox/ralph/\n"))
			Expect(proposal).NotTo(ContainSubstring("  .claude-sandbox/env\n"))
		})

		It("CS-LAY-011: appends preserve a well-formed file", func() {
			write(hostGI, "vendor") // non-empty, no trailing newline
			Expect(setup(true, ptr(true))).To(Succeed())

			content := read(hostGI)
			Expect(content).To(HavePrefix("vendor\n"))
			Expect(content).To(HaveSuffix("\n"))
			for _, line := range []string{".claude-sandbox/env", ".claude-sandbox/temp/"} {
				Expect(countLine(content, line)).To(Equal(1), line)
			}
		})

		It("CS-LAY-012: the gitignore prompt defaults to yes", func() {
			sp.IsTTY = true
			sp.Answers = []string{""}
			Expect(setup(true, nil)).To(Succeed())
			Expect(sp.Asked).To(ContainElement(ContainSubstring("Add them?")))
			Expect(countLine(read(hostGI), ".claude-sandbox/env")).To(Equal(1))

			By(`answering "n" leaves the file unchanged`)
			proj2 := filepath.Join(GinkgoT().TempDir(), "p2")
			Expect(os.MkdirAll(proj2, 0o755)).To(Succeed())
			sp2 := &prompt.Scripted{IsTTY: true, Answers: []string{"n"}}
			var err2 bytes.Buffer
			Expect(layout.Setup(proj2, true, layout.Options{
				Runner: fake, Prompter: sp2, Out: &out, Err: &err2,
			})).To(Succeed())
			Expect(exists(filepath.Join(proj2, ".gitignore"))).To(BeFalse())
			Expect(err2.String()).To(ContainSubstring("Skipped .gitignore update."))
		})

		It("CS-LAY-013: no terminal: gitignore update is skipped, never blocks", func() {
			sp.IsTTY = false
			Expect(setup(true, nil)).To(Succeed())
			Expect(errOut.String()).To(ContainSubstring("(no tty; skipping .gitignore update)"))
			Expect(exists(hostGI)).To(BeFalse())
		})

		It("CS-LAY-014: CS_GITIGNORE_ASSUME overrides the gitignore prompt", func() {
			os.Setenv("CS_GITIGNORE_ASSUME", "y")
			DeferCleanup(os.Unsetenv, "CS_GITIGNORE_ASSUME")
			Expect(setup(true, nil)).To(Succeed())
			Expect(sp.Asked).To(BeEmpty())
			Expect(countLine(read(hostGI), ".claude-sandbox/env")).To(Equal(1))

			By("CS_GITIGNORE_ASSUME=n skips without prompting")
			os.Setenv("CS_GITIGNORE_ASSUME", "n")
			proj2 := filepath.Join(GinkgoT().TempDir(), "p2")
			Expect(os.MkdirAll(proj2, 0o755)).To(Succeed())
			sp2 := &prompt.Scripted{IsTTY: true}
			var err2 bytes.Buffer
			Expect(layout.Setup(proj2, true, layout.Options{
				Runner: fake, Prompter: sp2, Out: &out, Err: &err2,
			})).To(Succeed())
			Expect(sp2.Asked).To(BeEmpty())
			Expect(exists(filepath.Join(proj2, ".gitignore"))).To(BeFalse())
		})
	})
})
