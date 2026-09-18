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
	"github.com/kmacmcfarlane/claude-sandbox/internal/paths"
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

// hostTrackedFake scripts a git work tree whose host repo does NOT ignore
// .claude-sandbox/ — the coherent state for trackInHost true. A bare Fake
// lets check-ignore succeed, which layout reads as a whole-dir ignore and
// refuses the host-tracked entries over (CS-LAY-018).
func hostTrackedFake() *execx.Fake {
	f := &execx.Fake{}
	f.On("check-ignore", "", execx.Fail(1))
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
	var tmp, proj, sb, cfg, env, envExample string

	BeforeEach(func() {
		os.Unsetenv("CS_GITIGNORE_ASSUME")
		tmp = GinkgoT().TempDir()
		proj = filepath.Join(tmp, "p")
		mkdir(proj)
		sb = filepath.Join(proj, ".claude-sandbox")
		cfg = filepath.Join(sb, "config.yaml")
		env = filepath.Join(sb, "env")
		envExample = filepath.Join(sb, "env.example")
	})

	Describe("seeding (idempotent, sparse)", func() {
		It("CS-INIT-004: greenfield init seeds config.yaml, env.example, and Dockerfile.example", func() {
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

			By("env.example: every variable commented; no real env")
			Expect(exists(envExample)).To(BeTrue())
			for _, line := range strings.Split(read(envExample), "\n") {
				trimmed := strings.TrimSpace(line)
				if trimmed != "" {
					Expect(trimmed).To(HavePrefix("#"), "uncommented line: %q", line)
				}
			}
			Expect(exists(env)).To(BeFalse())

			Expect(exists(filepath.Join(sb, "Dockerfile.example"))).To(BeTrue())
			Expect(r.out.String()).To(ContainSubstring("created  config.yaml"))
			Expect(r.out.String()).To(ContainSubstring("created  env.example"))
			Expect(r.out.String()).NotTo(MatchRegexp(`created  env(\s|$)`))
			Expect(r.out.String()).To(ContainSubstring("created  Dockerfile.example"))
		})

		It("CS-INIT-005: existing files are never overwritten", func() {
			write(cfg, "model: opus\n")
			write(env, "MY_VAR=1\n")
			write(envExample, "# MY_EXAMPLE=1\n")
			write(filepath.Join(sb, "Dockerfile.example"), "FROM custom\n")

			r := &run{}
			Expect(r.init(proj, initcmd.Flags{TrackInHost: nil})).To(Succeed())

			Expect(read(cfg)).To(Equal("model: opus\n"))
			Expect(read(env)).To(Equal("MY_VAR=1\n"))
			Expect(read(envExample)).To(Equal("# MY_EXAMPLE=1\n"))
			Expect(read(filepath.Join(sb, "Dockerfile.example"))).To(Equal("FROM custom\n"))
			Expect(r.out.String()).To(ContainSubstring("skipped  config.yaml"))
			Expect(r.out.String()).To(ContainSubstring("skipped  env.example"))
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
				fake:     hostTrackedFake(), // inside a work tree, dir not ignored
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
			envExample = filepath.Join(sb, "env.example")
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
			Expect(r.out.String()).To(ContainSubstring("is inherited by this project"))
			Expect(exists(env)).To(BeFalse())
			Expect(read(envExample)).NotTo(ContainSubstring("PARENT_VAR"))
		})

		Context("with a parent Dockerfile", func() {
			const parentContent = "FROM claude-sandbox\n# parent marker\n"
			var parentDF string

			BeforeEach(func() {
				parentDF = filepath.Join(ws, ".claude-sandbox", "Dockerfile")
				write(parentDF, parentContent)
			})

			It("CS-INIT-021: parent Dockerfile found: the example is a copy of it, no prompt", func() {
				r := &run{prompter: &prompt.Scripted{IsTTY: true}}
				Expect(r.init(proj, initcmd.Flags{TrackInHost: ptr(false)})).To(Succeed())

				Expect(r.prompter.Asked).To(BeEmpty())
				Expect(read(filepath.Join(sb, "Dockerfile.example"))).To(Equal(parentContent))
				Expect(r.out.String()).To(ContainSubstring("created  Dockerfile.example (copied from " + parentDF))
			})

			It("CS-INIT-022: --no-copy-parent-dockerfile seeds the generic example instead", func() {
				r := &run{prompter: &prompt.Scripted{IsTTY: true}}
				Expect(r.init(proj, initcmd.Flags{
					TrackInHost: ptr(false), CopyParentDockerfile: ptr(false),
				})).To(Succeed())

				Expect(r.prompter.Asked).To(BeEmpty())
				generic, err := scaffold.ReadBase("Dockerfile.example")
				Expect(err).NotTo(HaveOccurred())
				Expect(read(filepath.Join(sb, "Dockerfile.example"))).To(Equal(string(generic)))
			})

			It("CS-INIT-023: --copy-parent-dockerfile / --no-copy-parent-dockerfile remain as overrides", func() {
				r := &run{prompter: &prompt.Scripted{IsTTY: true}}
				Expect(r.init(proj, initcmd.Flags{
					TrackInHost: ptr(false), CopyParentDockerfile: ptr(true),
				})).To(Succeed())
				Expect(read(filepath.Join(sb, "Dockerfile.example"))).To(Equal(parentContent))
				Expect(r.prompter.Asked).To(BeEmpty())

				By("run instead with --no-copy-parent-dockerfile")
				proj2 := filepath.Join(ws, "p2")
				mkdir(proj2)
				r2 := &run{prompter: &prompt.Scripted{IsTTY: true}}
				Expect(r2.init(proj2, initcmd.Flags{
					TrackInHost: ptr(false), CopyParentDockerfile: ptr(false),
				})).To(Succeed())
				generic, err := scaffold.ReadBase("Dockerfile.example")
				Expect(err).NotTo(HaveOccurred())
				Expect(read(filepath.Join(proj2, ".claude-sandbox", "Dockerfile.example"))).To(Equal(string(generic)))
				Expect(r2.prompter.Asked).To(BeEmpty())
			})
		})

		It("CS-INIT-023: --copy-parent-dockerfile without a parent Dockerfile falls back to the generic example", func() {
			r := &run{prompter: &prompt.Scripted{IsTTY: true}}
			Expect(r.init(proj, initcmd.Flags{
				TrackInHost: ptr(false), CopyParentDockerfile: ptr(true),
			})).To(Succeed())
			generic, err := scaffold.ReadBase("Dockerfile.example")
			Expect(err).NotTo(HaveOccurred())
			Expect(read(filepath.Join(sb, "Dockerfile.example"))).To(Equal(string(generic)))
			Expect(r.prompter.Asked).To(BeEmpty())
		})

		It("CS-INIT-024: no parent Dockerfile: generic example, no prompt", func() {
			r := &run{prompter: &prompt.Scripted{IsTTY: true}}
			Expect(r.init(proj, initcmd.Flags{TrackInHost: ptr(false)})).To(Succeed())

			Expect(r.prompter.Asked).To(BeEmpty())
			generic, err := scaffold.ReadBase("Dockerfile.example")
			Expect(err).NotTo(HaveOccurred())
			Expect(read(filepath.Join(sb, "Dockerfile.example"))).To(Equal(string(generic)))
		})
	})

	Describe("gitignore entries follow trackInHost; flags are overrides", func() {
		It("CS-INIT-025: --yes accepts the trackInHost prompt's default non-interactively", func() {
			ws := filepath.Join(tmp, "ws")
			wsCfg := filepath.Join(ws, ".claude-sandbox", "config.yaml")
			write(wsCfg, "trackInHost: true\n")
			parentDF := filepath.Join(ws, ".claude-sandbox", "Dockerfile")
			write(parentDF, "FROM claude-sandbox\n# parent marker\n")
			proj = filepath.Join(ws, "p")
			mkdir(proj)
			sb = filepath.Join(proj, ".claude-sandbox")

			r := &run{
				fake:     hostTrackedFake(), // git work tree; .gitignore missing entries
				prompter: &prompt.Scripted{IsTTY: false},
			}
			Expect(r.init(proj, initcmd.Flags{Yes: true})).To(Succeed())

			By("trackInHost inherited (prompt default)")
			Expect(r.out.String()).To(ContainSubstring("trackInHost inherited: true"))
			Expect(read(filepath.Join(sb, "config.yaml"))).NotTo(MatchRegexp(`(?m)^trackInHost:`))
			By("Dockerfile.example copied from the parent")
			Expect(read(filepath.Join(sb, "Dockerfile.example"))).To(ContainSubstring("# parent marker"))
			By(".gitignore entries for host-tracked mode added")
			Expect(read(filepath.Join(proj, ".gitignore"))).To(ContainSubstring(".claude-sandbox/env"))
			Expect(r.errOut.String()).NotTo(ContainSubstring("Add them?"))
		})

		It("CS-INIT-026: --gitignore / --no-gitignore override the gitignore entries", func() {
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

		It("CS-INIT-028: the gitignore entries implied by trackInHost are written without a prompt", func() {
			By("interactive: the trackInHost answer is the only prompt; entries follow it")
			r := &run{
				fake:     hostTrackedFake(), // git work tree; .gitignore missing entries
				prompter: &prompt.Scripted{IsTTY: true, Answers: []string{"y"}},
			}
			Expect(r.init(proj, initcmd.Flags{})).To(Succeed())
			Expect(r.prompter.Asked).To(HaveLen(1))
			Expect(r.prompter.Asked[0]).To(ContainSubstring("Track in host repo?"))
			Expect(r.prompter.Asked).NotTo(ContainElement(ContainSubstring("Add them?")))
			gi := read(filepath.Join(proj, ".gitignore"))
			Expect(gi).To(ContainSubstring(".claude-sandbox/env")) // host-tracked shape
			Expect(gi).NotTo(ContainSubstring("/.claude-sandbox/\n"))

			By("no terminal: the entries are appended, not skipped")
			proj2 := filepath.Join(tmp, "p2")
			mkdir(proj2)
			r2 := &run{fake: &execx.Fake{}, prompter: &prompt.Scripted{IsTTY: false}}
			Expect(r2.init(proj2, initcmd.Flags{})).To(Succeed())
			Expect(read(filepath.Join(proj2, ".gitignore"))).To(ContainSubstring("/.claude-sandbox/")) // foreign-safe shape
			Expect(r2.errOut.String()).NotTo(ContainSubstring("skipping .gitignore update"))

			By("CS_GITIGNORE_ASSUME=n is still honoured when no flag is passed")
			os.Setenv("CS_GITIGNORE_ASSUME", "n")
			DeferCleanup(os.Unsetenv, "CS_GITIGNORE_ASSUME")
			proj3 := filepath.Join(tmp, "p3")
			mkdir(proj3)
			r3 := &run{fake: &execx.Fake{}, prompter: &prompt.Scripted{IsTTY: true, Answers: []string{"y"}}}
			Expect(r3.init(proj3, initcmd.Flags{})).To(Succeed())
			Expect(r3.prompter.Asked).To(HaveLen(1))
			Expect(exists(filepath.Join(proj3, ".gitignore"))).To(BeFalse())
		})

		It("CS-INIT-029: a greenfield interactive init asks exactly one question", func() {
			// Worst case for prompt count: a parent Dockerfile to copy, a git
			// work tree whose .gitignore lacks every entry, no upstream config.
			ws := filepath.Join(tmp, "ws")
			parentDF := filepath.Join(ws, ".claude-sandbox", "Dockerfile")
			write(parentDF, "FROM claude-sandbox\n# parent marker\n")
			proj = filepath.Join(ws, "p")
			mkdir(proj)
			sb = filepath.Join(proj, ".claude-sandbox")

			r := &run{
				fake:     &execx.Fake{}, // git work tree
				prompter: &prompt.Scripted{IsTTY: true, Answers: []string{""}},
			}
			Expect(r.init(proj, initcmd.Flags{})).To(Succeed())

			Expect(r.prompter.Asked).To(HaveLen(1))
			Expect(r.prompter.Asked[0]).To(ContainSubstring("Track in host repo?"))
			By("everything else follows from that one answer (Enter = false)")
			Expect(read(filepath.Join(sb, "config.yaml"))).To(MatchRegexp(`(?m)^trackInHost: false$`))
			Expect(read(filepath.Join(sb, "Dockerfile.example"))).To(ContainSubstring("# parent marker"))
			Expect(read(filepath.Join(proj, ".gitignore"))).To(ContainSubstring("/.claude-sandbox/"))
			Expect(exists(filepath.Join(sb, ".gitignore"))).To(BeTrue()) // sidecar layout
		})
	})

	It("CS-INIT-027: completion message lists next steps", func() {
		r := &run{}
		Expect(r.init(proj, initcmd.Flags{TrackInHost: ptr(false)})).To(Succeed())

		out := r.out.String()
		Expect(out).To(ContainSubstring("Done. Next steps:"))
		Expect(out).To(ContainSubstring("1. Put secrets / env vars"))
		Expect(out).To(ContainSubstring("in an upstream .claude-sandbox/env"))
		Expect(out).To(ContainSubstring(".claude-sandbox/env.example → .claude-sandbox/env for a project-only override"))
		Expect(out).To(ContainSubstring("2. Review .claude-sandbox/config.yaml"))
		Expect(out).To(ContainSubstring("Dockerfile.example"))
		Expect(strings.TrimSpace(out)).To(HaveSuffix("Launch:  claude-sandbox"))
	})

	It("CS-INIT-030: init never creates a real env, in any mode", func() {
		for _, tc := range []struct {
			name  string
			flags initcmd.Flags
			fake  func() *execx.Fake
		}{
			{"init --no-track-in-host", initcmd.Flags{TrackInHost: ptr(false)}, nonGitFake},
			{"init --track-in-host", initcmd.Flags{TrackInHost: ptr(true)}, hostTrackedFake},
			{"init --yes", initcmd.Flags{Yes: true}, nonGitFake},
			{"init --no-gitignore", initcmd.Flags{TrackInHost: ptr(false), Gitignore: ptr(false)}, nonGitFake},
			{"init-ralph --no-track-in-host", initcmd.Flags{Ralph: true, TrackInHost: ptr(false)}, nonGitFake},
			{"init-ralph --track-in-host", initcmd.Flags{Ralph: true, TrackInHost: ptr(true)}, hostTrackedFake},
		} {
			p := filepath.Join(GinkgoT().TempDir(), "p")
			mkdir(p)
			r := &run{fake: tc.fake()}
			Expect(r.init(p, tc.flags)).To(Succeed(), tc.name)
			Expect(exists(filepath.Join(p, ".claude-sandbox", "env"))).To(BeFalse(), tc.name)
			Expect(exists(filepath.Join(p, ".claude-sandbox", "env.example"))).To(BeTrue(), tc.name)
		}

		By("the env.example header says it is an example and where secrets belong")
		r := &run{}
		Expect(r.init(proj, initcmd.Flags{TrackInHost: ptr(false)})).To(Succeed())
		header := read(envExample)
		Expect(header).To(ContainSubstring("EXAMPLE ONLY"))
		Expect(header).To(ContainSubstring("never reads this file"))
		Expect(header).To(ContainSubstring("<workspace>/.claude-sandbox/env"))
		Expect(header).To(ContainSubstring("Copy this file to .claude-sandbox/env"))
		Expect(header).To(ContainSubstring("per-project override"))
		Expect(header).To(ContainSubstring("OVERRIDE the same keys"))
		Expect(header).NotTo(ContainSubstring(".env.claude-sandbox"))

		By("an upstream-only token is the whole env cascade after init")
		ws := filepath.Join(tmp, "ws")
		p := filepath.Join(ws, "p")
		mkdir(p)
		upstream := filepath.Join(ws, ".claude-sandbox", "env")
		write(upstream, "TOKEN=upstream\n")
		Expect((&run{}).init(p, initcmd.Flags{TrackInHost: ptr(false)})).To(Succeed())
		envs, err := paths.CollectUp(p, paths.Env)
		Expect(err).NotTo(HaveOccurred())
		Expect(envs).To(Equal([]string{upstream}))
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
		Expect(exists(filepath.Join(sb, "env.example"))).To(BeTrue())
		Expect(exists(filepath.Join(sb, "env"))).To(BeFalse())
		Expect(exists(filepath.Join(sb, "Dockerfile.example"))).To(BeTrue())
		Expect(read(filepath.Join(sb, "config.yaml"))).To(MatchRegexp(`(?m)^trackInHost: false$`))
	})

	It("CS-INITR-002: the ralph scaffold tree is seeded under .claude-sandbox/", func() {
		r := &run{}
		Expect(r.init(proj, initcmd.Flags{Ralph: true, TrackInHost: ptr(false)})).To(Succeed())

		for _, rel := range []string{
			"agent/PROMPT.md", "agent/PROMPT_AUTO.md", "agent/PROMPT_INTERACTIVE.md",
			"agent/AGENT_FLOW.md", "agent/backlog.yaml", "scripts/backlog/backlog.py",
		} {
			Expect(exists(filepath.Join(sb, rel))).To(BeTrue(), rel)
		}
		// The legacy .worktrees/<id> helper is gone: the only worktree
		// convention is claude's own .claude/worktrees/<name> (CS-LNCH-041).
		Expect(exists(filepath.Join(sb, "scripts", "worktree"))).To(BeFalse())
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
