package layout_test

// Spec: spec/layout.feature (CS-LAY-001..014, 017..022). CS-LAY-015/016
// (launcher adoption) live in cmd/claude-sandbox. Git behavior is scripted
// through execx.Fake: unmatched commands succeed, so by default the project IS
// a git work tree and check-ignore reports the path as ignored. Host-tracked
// (trackInHost true) tests call hostTracks() so the host does NOT ignore the
// directory — otherwise they would land in the CS-LAY-018 conflict path.
// Unmatched `git ls-files` returns empty output, so by default the host tracks
// nothing under .claude-sandbox/ (the CS-LAY-020 path is opt-in via On).

import (
	"bytes"
	"fmt"
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

// gitignoreMatches is a deliberately small gitignore matcher for CS-LAY-019:
// it reports whether one .gitignore line (negations and comments never
// ignore) would ignore rel or any of its ancestor directories. A pattern with
// no inner slash matches a basename at any depth; otherwise it is anchored to
// the .gitignore's directory. Enough for the literal and simple-glob lines
// layout writes.
func gitignoreMatches(line, rel string) bool {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
		return false
	}
	pat := strings.TrimSuffix(line, "/")
	anchored := strings.Contains(strings.TrimPrefix(pat, "/"), "/") || strings.HasPrefix(pat, "/")
	pat = strings.TrimPrefix(pat, "/")
	for cand := rel; cand != "." && cand != "/" && cand != ""; cand = filepath.Dir(cand) {
		target := cand
		if !anchored {
			target = filepath.Base(cand)
		}
		if ok, _ := filepath.Match(pat, target); ok {
			return true
		}
	}
	return false
}

var _ = Describe("layout lifecycle", func() {
	var proj, sb, hostGI string
	var fake *execx.Fake
	var sp *prompt.Scripted
	var out, errOut bytes.Buffer

	// hostTracks scripts git check-ignore to fail: the host repo does not
	// ignore .claude-sandbox/, the coherent state for trackInHost true.
	hostTracks := func() { fake.On("check-ignore", "", execx.Fail(1)) }

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

		By("investigations/ is a claude-kit convention, not a sandbox skeleton dir")
		Expect(exists(filepath.Join(sb, "investigations"))).To(BeFalse())

		By("a pre-existing investigations/ dir is user data and survives untouched")
		inv := filepath.Join(sb, "investigations", "some-series")
		Expect(os.MkdirAll(inv, 0o755)).To(Succeed())
		write(filepath.Join(inv, "01-plan.md"), "keep me\n")
		Expect(setup(false, ptr(true))).To(Succeed())
		Expect(read(filepath.Join(inv, "01-plan.md"))).To(Equal("keep me\n"))
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
			hostTracks()
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

	Describe("env.example", func() {
		It("CS-LAY-019: env.example is never gitignored", func() {
			By("the matcher itself: the env rules match env, not env.example")
			Expect(gitignoreMatches("env", "env")).To(BeTrue())
			Expect(gitignoreMatches(".claude-sandbox/env", ".claude-sandbox/env")).To(BeTrue())
			Expect(gitignoreMatches("env", "env.example")).To(BeFalse())
			Expect(gitignoreMatches("env*", "env.example")).To(BeTrue())

			By("trackInHost false: the sidecar .gitignore leaves env.example tracked")
			Expect(setup(false, ptr(true))).To(Succeed())
			sidecar := read(filepath.Join(sb, ".gitignore"))
			Expect(countLine(sidecar, "env")).To(Equal(1))
			for _, l := range strings.Split(sidecar, "\n") {
				Expect(gitignoreMatches(l, "env.example")).To(BeFalse(), l)
			}

			By("trackInHost true: no host line matches .claude-sandbox/env.example")
			proj = filepath.Join(GinkgoT().TempDir(), "p2")
			Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
			sb = filepath.Join(proj, ".claude-sandbox")
			hostGI = filepath.Join(proj, ".gitignore")
			hostTracks()
			Expect(setup(true, ptr(true))).To(Succeed())
			host := read(hostGI)
			Expect(countLine(host, ".claude-sandbox/env")).To(Equal(1))
			for _, l := range strings.Split(host, "\n") {
				Expect(gitignoreMatches(l, ".claude-sandbox/env.example")).To(BeFalse(), l)
			}
		})
	})

	Describe("Claude Code worktrees", func() {
		const wt = ".claude/worktrees/"

		It("CS-LAY-017: .claude/worktrees/ is gitignored in both trackInHost modes", func() {
			By("trackInHost false")
			Expect(setup(false, ptr(true))).To(Succeed())
			Expect(countLine(read(hostGI), wt)).To(Equal(1))
			Expect(countLine(read(hostGI), "/.claude-sandbox/")).To(Equal(1))

			By("trackInHost true, proposed alongside the .claude-sandbox/ entries")
			hostTracks()
			proj2 := filepath.Join(GinkgoT().TempDir(), "p2")
			Expect(os.MkdirAll(proj2, 0o755)).To(Succeed())
			var err2 bytes.Buffer
			Expect(layout.Setup(proj2, true, layout.Options{
				Runner: fake, Prompter: sp, Out: &out, Err: &err2, Gitignore: ptr(true),
			})).To(Succeed())
			gi2 := read(filepath.Join(proj2, ".gitignore"))
			Expect(countLine(gi2, wt)).To(Equal(1))
			Expect(countLine(gi2, ".claude-sandbox/env")).To(Equal(1))
			Expect(err2.String()).To(ContainSubstring("  " + wt + "\n"))
			Expect(strings.Count(err2.String(), "These entries are missing")).To(Equal(1), "one proposal, not two")
		})

		It("CS-LAY-017: a second run is idempotent", func() {
			Expect(setup(false, ptr(true))).To(Succeed())
			errOut.Reset()
			Expect(setup(false, ptr(true))).To(Succeed())
			Expect(countLine(read(hostGI), wt)).To(Equal(1))
			Expect(errOut.String()).NotTo(ContainSubstring("These entries are missing"))
		})

		It("CS-LAY-017: an existing covering rule means nothing is proposed or added", func() {
			for _, rule := range []string{".claude/*", ".claude/", "/.claude/worktrees/", "  .claude  "} {
				p := filepath.Join(GinkgoT().TempDir(), "p")
				Expect(os.MkdirAll(p, 0o755)).To(Succeed())
				gi := filepath.Join(p, ".gitignore")
				write(gi, "/.claude-sandbox/\n"+rule+"\n")
				var e bytes.Buffer
				Expect(layout.Setup(p, false, layout.Options{
					Runner: fake, Prompter: sp, Out: &out, Err: &e, Gitignore: ptr(true),
				})).To(Succeed())
				Expect(countLine(read(gi), wt)).To(BeZero(), rule)
				Expect(e.String()).NotTo(ContainSubstring("These entries are missing"), rule)
			}
		})

		It("CS-LAY-017: declining gitignore management skips the line too", func() {
			hostTracks()
			Expect(setup(true, ptr(false))).To(Succeed())
			Expect(errOut.String()).To(ContainSubstring("  " + wt + "\n"))
			Expect(errOut.String()).To(ContainSubstring("Skipped .gitignore update."))
			Expect(exists(hostGI)).To(BeFalse())
		})
	})

	Describe("mode conflict: host-tracked config over a sidecar layout", func() {
		const wt = ".claude/worktrees/"
		trueLines := []string{
			".claude-sandbox/env", ".claude-sandbox/temp/", ".claude-sandbox/ralph/",
			"!.claude-sandbox/config.yaml", "!.claude-sandbox/Dockerfile",
		}
		expectRefused := func(errText string) {
			for _, l := range trueLines {
				Expect(errText).NotTo(ContainSubstring("  "+l+"\n"), l)
			}
			Expect(strings.Count(errText, "WARNING: trackInHost is true but")).To(Equal(1), "one warning")
			Expect(errText).To(ContainSubstring("skipping the host-tracked .gitignore entries"))
			Expect(errText).To(ContainSubstring("set trackInHost: false in .claude-sandbox/config.yaml"))
			Expect(errText).To(ContainSubstring("and delete any .claude-sandbox/env, temp/, ralph/, !config.yaml or !Dockerfile lines already in .gitignore"))
			Expect(errText).To(ContainSubstring("drop the ignore rule (`git check-ignore -v --no-index .claude-sandbox` names it) and .claude-sandbox/.git"))
			Expect(errText).NotTo(ContainSubstring("(sidecar layout)"))
		}

		It("CS-LAY-018: a whole-dir ignore refuses the host-tracked entries and warns", func() {
			// Default fake: check-ignore succeeds => the host ignores .claude-sandbox/.
			write(hostGI, "/.claude-sandbox/\n")
			Expect(setup(true, ptr(true))).To(Succeed())

			content := read(hostGI)
			for _, l := range trueLines {
				Expect(countLine(content, l)).To(BeZero(), l)
			}
			expectRefused(errOut.String())
			Expect(errOut.String()).To(ContainSubstring("but the host repo already ignores .claude-sandbox/; skipping"))
			Expect(errOut.String()).NotTo(ContainSubstring(".claude-sandbox/.git exists"))

			By("the worktrees line is still proposed and added (CS-LAY-017)")
			Expect(errOut.String()).To(ContainSubstring("  " + wt + "\n"))
			Expect(countLine(content, wt)).To(Equal(1))
			Expect(countLine(content, "/.claude-sandbox/")).To(Equal(1), "the existing rule is left alone")

			By("no sidecar is initialized in host-tracked mode, as before")
			Expect(fake.CommandLines()).NotTo(ContainElement(ContainSubstring(" init -q")))
			Expect(exists(filepath.Join(sb, ".gitignore"))).To(BeFalse())

			By("a second run proposes nothing and leaves the tree clean")
			errOut.Reset()
			Expect(setup(true, ptr(true))).To(Succeed())
			Expect(read(hostGI)).To(Equal(content))
			Expect(errOut.String()).NotTo(ContainSubstring("These entries are missing"))
			Expect(strings.Count(errOut.String(), "WARNING: trackInHost is true but")).To(Equal(1))
		})

		It("CS-LAY-018: a sidecar .git without the ignore refuses the entries too", func() {
			hostTracks()
			Expect(os.MkdirAll(filepath.Join(sb, ".git"), 0o755)).To(Succeed())
			Expect(setup(true, ptr(true))).To(Succeed())

			content := read(hostGI)
			for _, l := range trueLines {
				Expect(countLine(content, l)).To(BeZero(), l)
			}
			expectRefused(errOut.String())
			Expect(errOut.String()).To(ContainSubstring("but .claude-sandbox/.git exists; skipping"))
			Expect(errOut.String()).NotTo(ContainSubstring("already ignores"))
			Expect(countLine(content, wt)).To(Equal(1))
		})

		It("CS-LAY-018: both conditions are named in the one warning", func() {
			write(hostGI, "/.claude-sandbox/\n")
			Expect(os.MkdirAll(filepath.Join(sb, ".git"), 0o755)).To(Succeed())
			Expect(setup(true, ptr(true))).To(Succeed())
			expectRefused(errOut.String())
			Expect(errOut.String()).To(ContainSubstring("but the host repo already ignores .claude-sandbox/ and .claude-sandbox/.git exists; skipping"))
		})

		It("CS-LAY-018: a covering rule means not even the worktrees line is proposed", func() {
			write(hostGI, "/.claude-sandbox/\n.claude/\n")
			Expect(setup(true, ptr(true))).To(Succeed())
			Expect(read(hostGI)).To(Equal("/.claude-sandbox/\n.claude/\n"))
			Expect(errOut.String()).NotTo(ContainSubstring("These entries are missing"))
			Expect(errOut.String()).To(ContainSubstring("WARNING: trackInHost is true but"))
		})

		It("CS-LAY-018: with neither condition the CS-LAY-009 entries are proposed unchanged", func() {
			hostTracks()
			Expect(setup(true, ptr(false))).To(Succeed())
			Expect(errOut.String()).NotTo(ContainSubstring("WARNING: trackInHost"))
			for _, l := range trueLines {
				Expect(errOut.String()).To(ContainSubstring("  "+l+"\n"), l)
			}
		})
	})

	Describe("mode conflict: sidecar config over host-tracked content", func() {
		const wt = ".claude/worktrees/"
		const probe = "check-ignore -q -- .claude-sandbox/ignore-probe"
		const dirProbe = "check-ignore -q --no-index -- .claude-sandbox"
		const warn = "WARNING: trackInHost is false but the host repo already tracks"
		const warnRule = "WARNING: trackInHost is false and the host repo tracks"
		// tracked scripts `git ls-files -z -- .claude-sandbox` to list files.
		tracked := func(files ...string) {
			fake.On("ls-files -z -- .claude-sandbox", strings.Join(files, "\x00")+"\x00", nil)
		}
		// realGitRule models real git under a "/.claude-sandbox/" rule with
		// tracked content: the directory itself is NOT reported ignored unless
		// asked with --no-index (CS-LAY-018), a new child path is.
		realGitRule := func() {
			fake.On(probe, "", nil) // matches both child probes (CS-LAY-022)
			fake.On(dirProbe, "", nil)
			fake.On("check-ignore", "", execx.Fail(1))
		}
		expectRemedies := func(errText string) {
			Expect(errText).To(ContainSubstring("set trackInHost: true in .claude-sandbox/config.yaml"))
			Expect(errText).To(ContainSubstring("copy .claude-sandbox/ aside (or into the sidecar) first, then `git rm -r --cached .claude-sandbox` and commit"))
			Expect(errText).To(ContainSubstring("That commit deletes .claude-sandbox/ from every other clone and worktree that pulls or merges it."))
			Expect(errText).NotTo(ContainSubstring("  /.claude-sandbox/\n"), "the whole-dir line is never proposed")
		}
		expectWarned := func(errText, count string) {
			Expect(strings.Count(errText, "WARNING: trackInHost is false")).To(Equal(1), "one warning")
			Expect(errText).To(ContainSubstring(warn + " " + count + " under .claude-sandbox/; skipping the /.claude-sandbox/ .gitignore entry, which would silently hide new files there."))
			Expect(errText).NotTo(ContainSubstring("being hidden from git NOW"))
			expectRemedies(errText)
		}

		It("CS-LAY-020: host-tracked files refuse the whole-dir ignore and warn", func() {
			tracked(".claude-sandbox/work/a.md", ".claude-sandbox/work/b.md")
			fake.On("check-ignore", "", execx.Fail(1)) // the coherent state: not ignored
			Expect(setup(false, ptr(true))).To(Succeed())

			Expect(fake.CommandLines()).To(ContainElement("git -C " + proj + " ls-files -z -- .claude-sandbox"))
			content := read(hostGI)
			Expect(countLine(content, "/.claude-sandbox/")).To(BeZero())
			expectWarned(errOut.String(), "2 files")
			Expect(errOut.String()).NotTo(ContainSubstring("remove .claude-sandbox/.git"))

			By("the worktrees line is still proposed and added (CS-LAY-017)")
			Expect(errOut.String()).To(ContainSubstring("  " + wt + "\n"))
			Expect(countLine(content, wt)).To(Equal(1))

			By("no sidecar init, and the CS-LAY-006 note is replaced by the warning")
			Expect(fake.CommandLines()).NotTo(ContainElement(ContainSubstring(" init -q")))
			Expect(errOut.String()).NotTo(ContainSubstring("skipping sidecar git init"))
			Expect(errOut.String()).NotTo(ContainSubstring("Add /.claude-sandbox/ to .gitignore"))

			By("the sidecar's own .gitignore is still written (CS-LAY-004); CLAUDE.md is not seeded")
			Expect(countLine(read(filepath.Join(sb, ".gitignore")), "env")).To(Equal(1))
			Expect(exists(filepath.Join(sb, "CLAUDE.md"))).To(BeFalse())

			By("a second run proposes nothing and warns again")
			errOut.Reset()
			Expect(setup(false, ptr(true))).To(Succeed())
			Expect(read(hostGI)).To(Equal(content))
			Expect(errOut.String()).NotTo(ContainSubstring("These entries are missing"))
			expectWarned(errOut.String(), "2 files")
		})

		It("CS-LAY-020: an interactive launch never sees the whole-dir line in its prompt", func() {
			tracked(".claude-sandbox/work/a.md")
			fake.On("check-ignore", "", execx.Fail(1))
			sp.IsTTY = true
			sp.Answers = []string{""}
			Expect(setup(false, nil)).To(Succeed())
			expectWarned(errOut.String(), "1 file")
			Expect(countLine(read(hostGI), "/.claude-sandbox/")).To(BeZero())
			Expect(countLine(read(hostGI), wt)).To(Equal(1))
		})

		It("CS-LAY-020: an existing rule over tracked files (the incident state) says new files are hidden now", func() {
			tracked(".claude-sandbox/work/a.md")
			write(hostGI, "/.claude-sandbox/\n")
			realGitRule()
			Expect(setup(false, ptr(true))).To(Succeed())

			e := errOut.String()
			Expect(strings.Count(e, "WARNING: trackInHost is false")).To(Equal(1), "one warning")
			Expect(e).To(ContainSubstring(warnRule + " 1 file under .claude-sandbox/, but a host ignore rule already covers the directory: new files there are being hidden from git NOW."))
			Expect(e).To(ContainSubstring("Remove that rule whichever remedy you choose (`git check-ignore -v .claude-sandbox/ignore-probe` names it)."))
			Expect(e).NotTo(ContainSubstring("skipping the /.claude-sandbox/ .gitignore entry"))
			expectRemedies(e)

			By("the child probe is asked, and no sidecar init follows the rule")
			Expect(fake.CommandLines()).To(ContainElement("git -C " + proj + " check-ignore -q -- .claude-sandbox/ignore-probe"))
			Expect(fake.CommandLines()).To(ContainElement("git -C " + proj + " check-ignore -q -- .claude-sandbox/ignore-probe.md"))
			Expect(fake.CommandLines()).NotTo(ContainElement(ContainSubstring(" init -q")))
			Expect(countLine(read(hostGI), "/.claude-sandbox/")).To(Equal(1), "the existing rule is left alone")
		})

		It("CS-LAY-020: remedy 1 works end to end from the incident state (CS-LAY-018 sees the rule)", func() {
			tracked(".claude-sandbox/work/a.md")
			write(hostGI, "/.claude-sandbox/\n")
			realGitRule()

			By("trackInHost: true with the rule still present: CS-LAY-018 refuses the dead lines")
			Expect(setup(true, ptr(true))).To(Succeed())
			for _, l := range []string{".claude-sandbox/env", "!.claude-sandbox/config.yaml"} {
				Expect(countLine(read(hostGI), l)).To(BeZero(), l)
			}
			Expect(errOut.String()).To(ContainSubstring("WARNING: trackInHost is true but the host repo already ignores .claude-sandbox/"))

			By("once the rule is removed, the host-tracked entries are proposed")
			fake2 := &execx.Fake{}
			fake2.On("check-ignore", "", execx.Fail(1))
			write(hostGI, wt+"\n")
			errOut.Reset()
			Expect(layout.Setup(proj, true, layout.Options{
				Runner: fake2, Prompter: sp, Out: &out, Err: &errOut, Gitignore: ptr(true),
			})).To(Succeed())
			Expect(errOut.String()).NotTo(ContainSubstring("WARNING"))
			Expect(countLine(read(hostGI), ".claude-sandbox/env")).To(Equal(1))
		})

		It("CS-LAY-020: an existing sidecar .git adds one clause to remedy 1", func() {
			tracked(".claude-sandbox/work/a.md")
			fake.On("check-ignore", "", execx.Fail(1))
			Expect(os.MkdirAll(filepath.Join(sb, ".git"), 0o755)).To(Succeed())
			Expect(setup(false, ptr(true))).To(Succeed())
			expectWarned(errOut.String(), "1 file")
			Expect(errOut.String()).To(ContainSubstring("(and remove .claude-sandbox/.git, which CS-LAY-018 refuses in that mode)"))
		})

		It("CS-LAY-020: a covering rule means not even the worktrees line is proposed", func() {
			tracked(".claude-sandbox/work/a.md")
			fake.On("check-ignore", "", execx.Fail(1))
			write(hostGI, ".claude/\n")
			Expect(setup(false, ptr(true))).To(Succeed())
			Expect(read(hostGI)).To(Equal(".claude/\n"))
			Expect(errOut.String()).NotTo(ContainSubstring("These entries are missing"))
			expectWarned(errOut.String(), "1 file")
		})

		It("CS-LAY-020: a failed ls-files probe keeps today's behaviour", func() {
			fake.On("ls-files", "", execx.Fail(128))
			Expect(setup(false, ptr(true))).To(Succeed())
			Expect(errOut.String()).NotTo(ContainSubstring("WARNING"))
			Expect(countLine(read(hostGI), "/.claude-sandbox/")).To(Equal(1))
			Expect(fake.CommandLines()).To(ContainElement("git -C " + sb + " init -q"))
			Expect(exists(filepath.Join(sb, "CLAUDE.md"))).To(BeTrue())
		})

		It("CS-LAY-020: nothing tracked proposes the whole-dir ignore as in CS-LAY-003", func() {
			fake.On("ls-files", "", nil)
			Expect(setup(false, ptr(false))).To(Succeed())
			Expect(errOut.String()).NotTo(ContainSubstring("WARNING"))
			Expect(errOut.String()).To(ContainSubstring("  /.claude-sandbox/\n"))
		})

		It("CS-LAY-020: the probe does not run outside a git work tree or with trackInHost true", func() {
			fake.On("rev-parse --is-inside-work-tree", "", execx.Fail(1))
			tracked(".claude-sandbox/work/a.md")
			Expect(setup(false, nil)).To(Succeed())
			Expect(fake.CommandLines()).NotTo(ContainElement(ContainSubstring("ls-files")))
			Expect(fake.CommandLines()).To(ContainElement("git -C " + sb + " init -q"))

			fake2 := &execx.Fake{}
			fake2.On("check-ignore", "", execx.Fail(1))
			Expect(layout.Setup(proj, true, layout.Options{
				Runner: fake2, Prompter: sp, Out: &out, Err: &errOut, Gitignore: ptr(true),
			})).To(Succeed())
			Expect(fake2.CommandLines()).NotTo(ContainElement(ContainSubstring("ls-files")))
		})
	})
	Describe("probe robustness", func() {
		const dirProbe = "git -C %s check-ignore -q --no-index -- .claude-sandbox"
		const bare = ".claude-sandbox/ignore-probe"
		const withExt = ".claude-sandbox/ignore-probe.md"
		trueLines := []string{
			".claude-sandbox/env", ".claude-sandbox/temp/", ".claude-sandbox/ralph/",
			"!.claude-sandbox/config.yaml", "!.claude-sandbox/Dockerfile",
		}
		// ignores scripts git check-ignore path by path: the --no-index
		// directory probe answers dir, each child probe answers from children.
		ignores := func(dir bool, children map[string]bool) {
			fake.OnFunc("check-ignore", func(c execx.Cmd) (string, error) {
				path := c.Args[len(c.Args)-1]
				hit := children[path]
				if path == ".claude-sandbox" {
					hit = dir
				}
				if hit {
					return "", nil
				}
				return "", execx.Fail(1)
			})
		}
		tracked := func(files ...string) {
			fake.On("ls-files -z -- .claude-sandbox", strings.Join(files, "\x00")+"\x00", nil)
		}
		childProbes := func() []string {
			var got []string
			for _, l := range fake.CommandLines() {
				if strings.Contains(l, "check-ignore") && !strings.Contains(l, "--no-index") {
					got = append(got, l[strings.LastIndex(l, " ")+1:])
				}
			}
			return got
		}

		It("CS-LAY-021: a children-only rule does not refuse the host-tracked entries", func() {
			// ".claude-sandbox/*": the directory is not excluded, every child is.
			write(hostGI, ".claude-sandbox/*\n")
			ignores(false, map[string]bool{bare: true, withExt: true})
			Expect(setup(true, ptr(true))).To(Succeed())

			Expect(fake.CommandLines()).To(ContainElement(fmt.Sprintf(dirProbe, proj)))
			Expect(errOut.String()).NotTo(ContainSubstring("WARNING: trackInHost"))
			content := read(hostGI)
			for _, l := range trueLines {
				Expect(countLine(content, l)).To(Equal(1), l)
			}
			Expect(countLine(content, ".claude-sandbox/*")).To(Equal(1), "the rule is left alone")
		})

		It("CS-LAY-021: a rule excluding the directory itself still refuses them (CS-LAY-018)", func() {
			ignores(true, map[string]bool{bare: true, withExt: true})
			Expect(setup(true, ptr(true))).To(Succeed())
			Expect(errOut.String()).To(ContainSubstring("WARNING: trackInHost is true but the host repo already ignores .claude-sandbox/; skipping"))
			Expect(countLine(read(hostGI), ".claude-sandbox/env")).To(BeZero())
		})

		It("CS-LAY-022: a whitelist-style ignore that hides only the dot-less probe does not count", func() {
			// "*", "!*/", "!*.*": ignore-probe is hidden, ignore-probe.md is not.
			ignores(false, map[string]bool{bare: true})
			tracked(".claude-sandbox/config.yaml")
			Expect(setup(false, ptr(true))).To(Succeed())
			Expect(childProbes()).To(Equal([]string{bare, withExt}))
			Expect(errOut.String()).To(ContainSubstring("WARNING: trackInHost is false but the host repo already tracks 1 file under .claude-sandbox/; skipping"))
			Expect(errOut.String()).NotTo(ContainSubstring("being hidden from git NOW"))

			By("with nothing tracked, the CS-LAY-006 note applies and no sidecar is initialized")
			fake = &execx.Fake{}
			ignores(false, map[string]bool{bare: true})
			fake.On("ls-files", "", nil)
			errOut.Reset()
			Expect(setup(false, ptr(false))).To(Succeed())
			Expect(errOut.String()).To(ContainSubstring("is not gitignored by the host repo; skipping sidecar git init"))
			Expect(fake.CommandLines()).NotTo(ContainElement(ContainSubstring(" init -q")))
		})

		It("CS-LAY-022: a rule that hides only names with an extension does not count, and stops at the first probe", func() {
			// "*.md": ignore-probe is not hidden, so ignore-probe.md is never asked.
			ignores(false, map[string]bool{withExt: true})
			fake.On("ls-files", "", nil)
			Expect(setup(false, ptr(false))).To(Succeed())
			Expect(childProbes()).To(Equal([]string{bare}))
			Expect(errOut.String()).To(ContainSubstring("skipping sidecar git init"))
			Expect(fake.CommandLines()).NotTo(ContainElement(ContainSubstring(" init -q")))
		})

		It("CS-LAY-022: both probes ignored counts as ignored, as before", func() {
			ignores(false, map[string]bool{bare: true, withExt: true})
			fake.On("ls-files", "", nil)
			Expect(setup(false, ptr(true))).To(Succeed())
			Expect(childProbes()).To(Equal([]string{bare, withExt}))
			Expect(fake.CommandLines()).To(ContainElement("git -C " + sb + " init -q"))
		})
	})

	Describe("gitignore editing mechanics", func() {
		It("CS-LAY-010: only missing lines are proposed, matched exactly", func() {
			hostTracks()
			write(hostGI, ".claude-sandbox/env\n")
			Expect(setup(true, ptr(false))).To(Succeed())

			proposal := errOut.String()
			Expect(proposal).To(ContainSubstring("  .claude-sandbox/temp/\n"))
			Expect(proposal).To(ContainSubstring("  .claude-sandbox/ralph/\n"))
			Expect(proposal).NotTo(ContainSubstring("  .claude-sandbox/env\n"))
		})

		It("CS-LAY-011: appends preserve a well-formed file", func() {
			hostTracks()
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
			hostTracks()
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
			hostTracks()
			sp.IsTTY = false
			Expect(setup(true, nil)).To(Succeed())
			Expect(errOut.String()).To(ContainSubstring("(no tty; skipping .gitignore update)"))
			Expect(exists(hostGI)).To(BeFalse())
		})

		It("CS-LAY-014: CS_GITIGNORE_ASSUME overrides the gitignore prompt", func() {
			hostTracks()
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
