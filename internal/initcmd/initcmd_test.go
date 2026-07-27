package initcmd_test

// Spec: spec/init.feature (CS-INIT), spec/init-ralph.feature (CS-INITR).
// All git interaction goes through execx.Fake; prompts through prompt.Scripted.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/initcmd"
	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
	"github.com/kmacmcfarlane/claude-sandbox/internal/scaffold"
)

func mkdir(p string) {
	Expect(os.MkdirAll(p, 0o755)).To(Succeed())
}

func write(p, content string) {
	mkdir(filepath.Dir(p))
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

// nonGitFake scripts a project that is NOT inside a git work tree, which
// keeps layout.Setup away from gitignore prompts.
func nonGitFake() *execx.Fake {
	f := &execx.Fake{}
	f.On("rev-parse --is-inside-work-tree", "", execx.Fail(1))
	return f
}

// run bundles one initcmd.Run invocation's collaborators.
type run struct {
	out, errOut bytes.Buffer
	prompter    *prompt.Scripted
	fake        *execx.Fake
}

func (r *run) init(project string, f initcmd.Flags) error {
	if r.fake == nil {
		r.fake = nonGitFake()
	}
	if r.prompter == nil {
		r.prompter = &prompt.Scripted{}
	}
	r.prompter.Out = &r.errOut
	return initcmd.Run(project, f, initcmd.Deps{
		Runner: r.fake, Prompter: r.prompter, Out: &r.out, Err: &r.errOut,
	})
}

var _ = Describe("init subcommand", func() {
	var tmp, proj, sb, cfg, env string

	BeforeEach(func() {
		os.Unsetenv("CS_GITIGNORE_ASSUME")
		tmp = GinkgoT().TempDir()
		proj = filepath.Join(tmp, "p")
		mkdir(proj)
		sb = filepath.Join(proj, ".claude-sandbox")
		cfg = filepath.Join(sb, "config.yaml")
		env = filepath.Join(sb, "env")
	})

	Describe("seeding (idempotent, sparse)", func() {
		It("CS-INIT-004: greenfield init seeds config.yaml, env, and Dockerfile.example", func() {
			r := &run{}
			Expect(r.init(proj, initcmd.Flags{TrackInHost: ptr(false)})).To(Succeed())

			By("config.yaml: every non-trackInHost key commented")
			Expect(exists(cfg)).To(BeTrue())
			for _, line := range strings.Split(read(cfg), "\n") {
				trimmed := strings.TrimSpace(line)
				if trimmed == "" || trimmed == "trackInHost: false" {
					continue
				}
				Expect(trimmed).To(HavePrefix("#"), "uncommented line: %q", line)
			}

			By("env: every variable commented")
			Expect(exists(env)).To(BeTrue())
			for _, line := range strings.Split(read(env), "\n") {
				trimmed := strings.TrimSpace(line)
				if trimmed != "" {
					Expect(trimmed).To(HavePrefix("#"), "uncommented line: %q", line)
				}
			}

			Expect(exists(filepath.Join(sb, "Dockerfile.example"))).To(BeTrue())
			Expect(r.out.String()).To(ContainSubstring("created  config.yaml"))
			Expect(r.out.String()).To(ContainSubstring("created  env"))
			Expect(r.out.String()).To(ContainSubstring("created  Dockerfile.example"))
		})

		It("CS-INIT-005: existing files are never overwritten", func() {
			write(cfg, "model: opus\n")
			write(env, "MY_VAR=1\n")
			write(filepath.Join(sb, "Dockerfile.example"), "FROM custom\n")

			r := &run{}
			Expect(r.init(proj, initcmd.Flags{TrackInHost: nil})).To(Succeed())

			Expect(read(cfg)).To(Equal("model: opus\n"))
			Expect(read(env)).To(Equal("MY_VAR=1\n"))
			Expect(read(filepath.Join(sb, "Dockerfile.example"))).To(Equal("FROM custom\n"))
			Expect(r.out.String()).To(ContainSubstring("skipped  config.yaml"))
			Expect(r.out.String()).To(ContainSubstring("skipped  env"))
			Expect(r.out.String()).To(ContainSubstring("skipped  Dockerfile.example"))
		})

		It("CS-INIT-006: an existing Dockerfile suppresses the Dockerfile.example seed", func() {
			write(filepath.Join(sb, "Dockerfile"), "FROM claude-sandbox\n")
			r := &run{}
			Expect(r.init(proj, initcmd.Flags{TrackInHost: ptr(false)})).To(Succeed())
			Expect(exists(filepath.Join(sb, "Dockerfile.example"))).To(BeFalse())
		})
	})

	Describe("trackInHost resolution", func() {
		It("CS-INIT-007: --track-in-host writes an explicit true and skips the prompt", func() {
			r := &run{}
			Expect(r.init(proj, initcmd.Flags{TrackInHost: ptr(true)})).To(Succeed())
			Expect(read(cfg)).To(MatchRegexp(`(?m)^trackInHost: true$`))
			Expect(r.prompter.Asked).To(BeEmpty())
		})

		It("CS-INIT-008: --no-track-in-host writes an explicit false and skips the prompt", func() {
			r := &run{}
			Expect(r.init(proj, initcmd.Flags{TrackInHost: ptr(false)})).To(Succeed())
			Expect(read(cfg)).To(MatchRegexp(`(?m)^trackInHost: false$`))
			Expect(r.prompter.Asked).To(BeEmpty())
		})

		It("CS-INIT-009: no flag, no upstream, interactive: prompt defaults to false on Enter", func() {
			r := &run{prompter: &prompt.Scripted{IsTTY: true, Answers: []string{""}}}
			Expect(r.init(proj, initcmd.Flags{})).To(Succeed())
			Expect(r.prompter.Asked).To(ContainElement(ContainSubstring("Track in host repo?")))
			Expect(read(cfg)).To(MatchRegexp(`(?m)^trackInHost: false$`))
		})

		It("CS-INIT-010: no flag, no upstream, answering yes writes true", func() {
			r := &run{prompter: &prompt.Scripted{IsTTY: true, Answers: []string{"y"}}}
			Expect(r.init(proj, initcmd.Flags{})).To(Succeed())
			Expect(read(cfg)).To(MatchRegexp(`(?m)^trackInHost: true$`))
		})

		It("CS-INIT-011: no terminal resolves trackInHost to false without prompting", func() {
			// The real TTY prompter answers the default silently when /dev/tty
			// is unavailable; Scripted{IsTTY: false} with no answers models that.
			r := &run{prompter: &prompt.Scripted{IsTTY: false}}
			Expect(r.init(proj, initcmd.Flags{})).To(Succeed())
			Expect(read(cfg)).To(MatchRegexp(`(?m)^trackInHost: false$`))
		})

		It("CS-INIT-012: flag on an existing config updates trackInHost in place", func() {
			write(cfg, "model: opus\n# trackInHost: false\n")
			r := &run{}
			Expect(r.init(proj, initcmd.Flags{TrackInHost: ptr(true)})).To(Succeed())
			content := read(cfg)
			Expect(content).To(MatchRegexp(`(?m)^trackInHost: true$`))
			Expect(content).NotTo(ContainSubstring("# trackInHost"))
			Expect(content).To(ContainSubstring("model: opus"))
			Expect(r.out.String()).To(ContainSubstring("updated  config.yaml"))
		})

		It("CS-INIT-012: flag appends trackInHost when no such line exists", func() {
			write(cfg, "model: opus\n")
			r := &run{}
			Expect(r.init(proj, initcmd.Flags{TrackInHost: ptr(true)})).To(Succeed())
			Expect(read(cfg)).To(MatchRegexp(`(?m)^trackInHost: true$`))
		})

		It("CS-INIT-013: no flag on an existing config leaves it untouched", func() {
			write(cfg, "model: opus\n# trackInHost: false\n")
			r := &run{}
			Expect(r.init(proj, initcmd.Flags{})).To(Succeed())
			Expect(read(cfg)).To(Equal("model: opus\n# trackInHost: false\n"))
			Expect(r.prompter.Asked).To(BeEmpty())
		})
	})

	Describe("upstream trackInHost inheritance", func() {
		var ws, wsCfg string

		BeforeEach(func() {
			// Rebuild the project under a workspace ancestor: tmp/ws/p.
			ws = filepath.Join(tmp, "ws")
			wsCfg = filepath.Join(ws, ".claude-sandbox", "config.yaml")
			write(wsCfg, "trackInHost: true\n")
			proj = filepath.Join(ws, "p")
			mkdir(proj)
			sb = filepath.Join(proj, ".claude-sandbox")
			cfg = filepath.Join(sb, "config.yaml")
		})

		It("CS-INIT-014: prompt shows the inherited value; Enter inherits", func() {
			r := &run{prompter: &prompt.Scripted{IsTTY: true, Answers: []string{""}}}
			Expect(r.init(proj, initcmd.Flags{})).To(Succeed())

			Expect(r.prompter.Asked).To(ContainElement(
				ContainSubstring("[Enter=inherit true, y/n=override]")))
			// The preamble states the inherited value and its source file.
			Expect(r.errOut.String()).To(ContainSubstring("trackInHost: true"))
			Expect(r.errOut.String()).To(ContainSubstring(wsCfg))
			// Nothing written locally: no uncommented trackInHost line.
			Expect(read(cfg)).NotTo(MatchRegexp(`(?m)^trackInHost:`))
			Expect(r.out.String()).To(ContainSubstring("trackInHost inherited: true"))
		})

		It("CS-INIT-015: explicit answer writes a local override", func() {
			r := &run{prompter: &prompt.Scripted{IsTTY: true, Answers: []string{"n"}}}
			Expect(r.init(proj, initcmd.Flags{})).To(Succeed())
			Expect(read(cfg)).To(MatchRegexp(`(?m)^trackInHost: false$`))
		})

		It("CS-INIT-016: inherited hint comment reflects the inherited value and source", func() {
			r := &run{prompter: &prompt.Scripted{IsTTY: true, Answers: []string{""}}}
			Expect(r.init(proj, initcmd.Flags{})).To(Succeed())
			Expect(read(cfg)).To(ContainSubstring(
				"# trackInHost: true   # inherited from " + wsCfg))
		})

		It("CS-INIT-017: flag wins over upstream: no prompt, local explicit value", func() {
			r := &run{}
			Expect(r.init(proj, initcmd.Flags{TrackInHost: ptr(false)})).To(Succeed())
			Expect(read(cfg)).To(MatchRegexp(`(?m)^trackInHost: false$`))
			Expect(r.prompter.Asked).To(BeEmpty())
		})

		It("CS-INIT-018: layout uses the effective cascade value", func() {
			// Inherited true + git work tree => host-tracked layout: ephemeral
			// gitignore entries, no sidecar repo (see layout.feature).
			r := &run{
				fake:     &execx.Fake{}, // all git calls succeed: inside a work tree
				prompter: &prompt.Scripted{IsTTY: true, Answers: []string{""}},
			}
			Expect(r.init(proj, initcmd.Flags{Gitignore: ptr(true)})).To(Succeed())

			gi := read(filepath.Join(proj, ".gitignore"))
			Expect(gi).To(ContainSubstring(".claude-sandbox/env"))
			Expect(gi).To(ContainSubstring(".claude-sandbox/ralph/"))
			Expect(exists(filepath.Join(sb, ".gitignore"))).To(BeFalse())
			Expect(r.fake.CommandLines()).NotTo(ContainElement(ContainSubstring(" init -q")))
		})
	})

	Describe("inheritance visibility and parent-file handling", func() {
		var ws string

		BeforeEach(func() {
			ws = filepath.Join(tmp, "ws")
			proj = filepath.Join(ws, "p")
			mkdir(proj)
			sb = filepath.Join(proj, ".claude-sandbox")
			cfg = filepath.Join(sb, "config.yaml")
			env = filepath.Join(sb, "env")
		})

		It("CS-INIT-019: init prints the config cascade when ancestors contribute", func() {
			write(filepath.Join(ws, ".claude-sandbox", "config.yaml"), "# sparse\n")
			write(filepath.Join(ws, ".claude-sandbox", "env"), "# WS_VAR=1\n")

			r := &run{}
			Expect(r.init(proj, initcmd.Flags{TrackInHost: ptr(false)})).To(Succeed())

			out := r.out.String()
			Expect(out).To(ContainSubstring("Sandbox config cascade"))
			Expect(out).To(ContainSubstring(filepath.Join(ws, ".claude-sandbox")))
			Expect(out).To(ContainSubstring("config.yaml env"))
		})

		It("CS-INIT-020: inherited env files are reported, never copied", func() {
			parentEnv := filepath.Join(ws, ".claude-sandbox", "env")
			write(parentEnv, "PARENT_VAR=secret\n")

			r := &run{}
			Expect(r.init(proj, initcmd.Flags{TrackInHost: ptr(false)})).To(Succeed())

			Expect(r.out.String()).To(ContainSubstring(parentEnv))
			Expect(r.out.String()).To(ContainSubstring("layers under this project's env"))
			Expect(read(env)).NotTo(ContainSubstring("PARENT_VAR"))
		})

		Context("with a parent Dockerfile", func() {
			const parentContent = "FROM claude-sandbox\n# parent marker\n"
			var parentDF string

			BeforeEach(func() {
				parentDF = filepath.Join(ws, ".claude-sandbox", "Dockerfile")
				write(parentDF, parentContent)
			})

			It("CS-INIT-021: parent Dockerfile found: prompt to seed the example from it, default yes", func() {
				r := &run{prompter: &prompt.Scripted{IsTTY: true, Answers: []string{""}}}
				Expect(r.init(proj, initcmd.Flags{TrackInHost: ptr(false)})).To(Succeed())

				Expect(r.prompter.Asked).To(ContainElement(And(
					ContainSubstring("Found parent Dockerfile"),
					ContainSubstring(parentDF))))
				Expect(read(filepath.Join(sb, "Dockerfile.example"))).To(Equal(parentContent))
			})

			It("CS-INIT-022: parent Dockerfile prompt declined: scaffold example is seeded", func() {
				r := &run{prompter: &prompt.Scripted{IsTTY: true, Answers: []string{"n"}}}
				Expect(r.init(proj, initcmd.Flags{TrackInHost: ptr(false)})).To(Succeed())

				generic, err := scaffold.ReadBase("Dockerfile.example")
				Expect(err).NotTo(HaveOccurred())
				Expect(read(filepath.Join(sb, "Dockerfile.example"))).To(Equal(string(generic)))
			})

			It("CS-INIT-023: --copy-parent-dockerfile / --no-copy-parent-dockerfile skip the prompt", func() {
				r := &run{}
				Expect(r.init(proj, initcmd.Flags{
					TrackInHost: ptr(false), CopyParentDockerfile: ptr(true),
				})).To(Succeed())
				Expect(read(filepath.Join(sb, "Dockerfile.example"))).To(Equal(parentContent))
				Expect(r.prompter.Asked).To(BeEmpty())

				By("run instead with --no-copy-parent-dockerfile")
				proj2 := filepath.Join(ws, "p2")
				mkdir(proj2)
				r2 := &run{}
				Expect(r2.init(proj2, initcmd.Flags{
					TrackInHost: ptr(false), CopyParentDockerfile: ptr(false),
				})).To(Succeed())
				generic, err := scaffold.ReadBase("Dockerfile.example")
				Expect(err).NotTo(HaveOccurred())
				Expect(read(filepath.Join(proj2, ".claude-sandbox", "Dockerfile.example"))).To(Equal(string(generic)))
				Expect(r2.prompter.Asked).To(BeEmpty())
			})
		})

		It("CS-INIT-024: no parent Dockerfile: no copy prompt", func() {
			r := &run{prompter: &prompt.Scripted{IsTTY: true}}
			Expect(r.init(proj, initcmd.Flags{TrackInHost: ptr(false)})).To(Succeed())

			Expect(r.prompter.Asked).NotTo(ContainElement(ContainSubstring("parent Dockerfile")))
			generic, err := scaffold.ReadBase("Dockerfile.example")
			Expect(err).NotTo(HaveOccurred())
			Expect(read(filepath.Join(sb, "Dockerfile.example"))).To(Equal(string(generic)))
		})
	})

	Describe("uniform prompt flags", func() {
		It("CS-INIT-025: --yes accepts every prompt's default non-interactively", func() {
			ws := filepath.Join(tmp, "ws")
			wsCfg := filepath.Join(ws, ".claude-sandbox", "config.yaml")
			write(wsCfg, "trackInHost: true\n")
			parentDF := filepath.Join(ws, ".claude-sandbox", "Dockerfile")
			write(parentDF, "FROM claude-sandbox\n# parent marker\n")
			proj = filepath.Join(ws, "p")
			mkdir(proj)
			sb = filepath.Join(proj, ".claude-sandbox")

			r := &run{
				fake:     &execx.Fake{}, // git work tree; .gitignore missing entries
				prompter: &prompt.Scripted{IsTTY: false},
			}
			Expect(r.init(proj, initcmd.Flags{Yes: true})).To(Succeed())

			By("trackInHost inherited (prompt default)")
			Expect(r.out.String()).To(ContainSubstring("trackInHost inherited: true"))
			Expect(read(filepath.Join(sb, "config.yaml"))).NotTo(MatchRegexp(`(?m)^trackInHost:`))
			By("Dockerfile.example copied from the parent (prompt default)")
			Expect(read(filepath.Join(sb, "Dockerfile.example"))).To(ContainSubstring("# parent marker"))
			By(".gitignore entries added (prompt default; effective trackInHost true)")
			Expect(read(filepath.Join(proj, ".gitignore"))).To(ContainSubstring(".claude-sandbox/env"))
		})

		It("CS-INIT-026: --gitignore / --no-gitignore control the gitignore prompt", func() {
			r := &run{fake: &execx.Fake{}} // git work tree
			Expect(r.init(proj, initcmd.Flags{
				TrackInHost: ptr(false), Gitignore: ptr(true),
			})).To(Succeed())
			Expect(read(filepath.Join(proj, ".gitignore"))).To(ContainSubstring("/.claude-sandbox/"))
			Expect(r.prompter.Asked).To(BeEmpty())

			By("run instead with --no-gitignore")
			proj2 := filepath.Join(tmp, "p2")
			mkdir(proj2)
			r2 := &run{fake: &execx.Fake{}}
			Expect(r2.init(proj2, initcmd.Flags{
				TrackInHost: ptr(false), Gitignore: ptr(false),
			})).To(Succeed())
			Expect(exists(filepath.Join(proj2, ".gitignore"))).To(BeFalse())
			Expect(r2.prompter.Asked).To(BeEmpty())
		})
	})

	It("CS-INIT-027: completion message lists next steps", func() {
		r := &run{}
		Expect(r.init(proj, initcmd.Flags{TrackInHost: ptr(false)})).To(Succeed())

		out := r.out.String()
		Expect(out).To(ContainSubstring("Done. Next steps:"))
		Expect(out).To(ContainSubstring("1. Add secrets / env vars to .claude-sandbox/env"))
		Expect(out).To(ContainSubstring("2. Review .claude-sandbox/config.yaml"))
		Expect(out).To(ContainSubstring("Dockerfile.example"))
		Expect(strings.TrimSpace(out)).To(HaveSuffix("Launch:  claude-sandbox"))
	})
})

var _ = Describe("init-ralph subcommand", func() {
	var tmp, proj, sb string

	BeforeEach(func() {
		os.Unsetenv("CS_GITIGNORE_ASSUME")
		tmp = GinkgoT().TempDir()
		proj = filepath.Join(tmp, "myproj")
		mkdir(proj)
		sb = filepath.Join(proj, ".claude-sandbox")
	})

	It("CS-INITR-001: init-ralph performs the full init first", func() {
		r := &run{}
		Expect(r.init(proj, initcmd.Flags{Ralph: true, TrackInHost: ptr(false)})).To(Succeed())

		Expect(exists(filepath.Join(sb, "config.yaml"))).To(BeTrue())
		Expect(exists(filepath.Join(sb, "env"))).To(BeTrue())
		Expect(exists(filepath.Join(sb, "Dockerfile.example"))).To(BeTrue())
		Expect(read(filepath.Join(sb, "config.yaml"))).To(MatchRegexp(`(?m)^trackInHost: false$`))
	})

	It("CS-INITR-002: the ralph scaffold tree is seeded under .claude-sandbox/", func() {
		r := &run{}
		Expect(r.init(proj, initcmd.Flags{Ralph: true, TrackInHost: ptr(false)})).To(Succeed())

		for _, rel := range []string{
			"agent/PROMPT.md", "agent/PROMPT_AUTO.md", "agent/PROMPT_INTERACTIVE.md",
			"agent/backlog.yaml", "scripts/backlog/backlog.py", "scripts/worktree/worktree.py",
		} {
			Expect(exists(filepath.Join(sb, rel))).To(BeTrue(), rel)
		}
		Expect(r.out.String()).To(MatchRegexp(`\d+ created, \d+ skipped`))
	})

	It("CS-INITR-003: existing files are skipped, gaps are filled", func() {
		write(filepath.Join(sb, "agent", "PROMPT.md"), "template content\n")

		r := &run{}
		Expect(r.init(proj, initcmd.Flags{Ralph: true, TrackInHost: ptr(false)})).To(Succeed())

		Expect(read(filepath.Join(sb, "agent", "PROMPT.md"))).To(Equal("template content\n"))
		Expect(exists(filepath.Join(sb, "agent", "PROMPT_AUTO.md"))).To(BeTrue())
		Expect(r.out.String()).To(MatchRegexp(`\d+ created, 1 skipped`))
	})

	It("CS-INITR-008: completion message includes ralph next steps", func() {
		r := &run{}
		Expect(r.init(proj, initcmd.Flags{Ralph: true, TrackInHost: ptr(false)})).To(Succeed())

		out := r.out.String()
		Expect(out).To(ContainSubstring("agent/PRD.md"))
		Expect(out).To(ContainSubstring("DEVELOPMENT_PRACTICES.md"))
		Expect(out).To(ContainSubstring("scripts/backlog/backlog.py"))
		Expect(out).To(ContainSubstring("Run the loop:  claude-sandbox --ralph"))
		Expect(out).To(ContainSubstring("Stop it:       touch .claude-sandbox/ralph/stop"))
	})
})
