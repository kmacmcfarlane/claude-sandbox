package launch_test

// Spec: spec/global-config.feature CS-GCFG-016..032 — how Build brings
// Claude Code's global config file into the container. Home is a scratch
// directory; nothing reads the real ~/.claude.json.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/globalcfg"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
)

var _ = Describe("launch.Build: the global config (CS-GCFG)", func() {
	var (
		home, proj, link, target string
		env                      map[string]string
		in                       launch.Inputs
		out, errw                *bytes.Buffer
	)

	BeforeEach(func() {
		base, err := filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		home = filepath.Join(base, "home")
		proj = filepath.Join(base, "proj")
		mkdir(home)
		mkdir(proj)
		mkdir(filepath.Join(home, ".claude"))
		link = filepath.Join(home, ".claude.json")
		target = filepath.Join(home, ".claude", ".claude.json")
		env = map[string]string{}
		out, errw = &bytes.Buffer{}, &bytes.Buffer{}
		in = launch.Inputs{
			ProjectDir: proj, Home: home,
			HostUID: 1000, HostGID: 1000, HostUser: "tester",
			Getenv:    func(k string) string { return env[k] },
			TempDir:   filepath.Join(base, "shadow"),
			ImageName: "claude-sandbox-proj",
			MountInfo: func() (string, error) { return "", nil },
			Out:       out, Err: errw,
		}
		mkdir(in.TempDir)
	})

	build := func() *launch.Plan {
		p, err := launch.Build(in)
		Expect(err).NotTo(HaveOccurred())
		return p
	}
	envFlag := "CLAUDE_SANDBOX_GLOBAL_CONFIG=" // globalcfg.EnvVar + "="
	hasGlobalEnv := func(p *launch.Plan) bool {
		for _, e := range p.EnvFlags {
			if strings.HasPrefix(e, envFlag) {
				return true
			}
		}
		return false
	}
	mountsClaudeJSON := func(p *launch.Plan) bool {
		for _, v := range p.Volumes {
			if strings.Contains(v, ".claude.json:") {
				return true
			}
		}
		return false
	}
	linked := func() {
		touch(target, "{}")
		Expect(os.Symlink(".claude/.claude.json", link)).To(Succeed())
	}
	quiet := func() {
		Expect(out.String()).NotTo(ContainSubstring("claude.json"))
		Expect(errw.String()).NotTo(ContainSubstring("claude.json"))
	}

	It("CS-GCFG-016: a relative link to ~/.claude/.claude.json launches linked: no mount, the env var, nothing printed", func() {
		Expect(globalcfg.EnvVar + "=").To(Equal(envFlag))
		linked()
		p := build()
		Expect(mountsClaudeJSON(p)).To(BeFalse())
		Expect(p.EnvFlags).To(ContainElement(envFlag + target))
		Expect(p.Volumes).To(ContainElement(filepath.Join(home, ".claude") + ":" + filepath.Join(home, ".claude")))
		quiet()
	})

	It("CS-GCFG-017: an absolute link launches linked", func() {
		touch(target, "{}")
		Expect(os.Symlink(target, link)).To(Succeed())
		p := build()
		Expect(mountsClaudeJSON(p)).To(BeFalse())
		Expect(p.EnvFlags).To(ContainElement(envFlag + target))
	})

	It("CS-GCFG-018: nested — inside a sandbox, pidslot's absolute link launches the inner container linked", func() {
		env["CLAUDE_SANDBOX_PROJECT_DIR"] = "/outer/proj"
		touch(target, "{}")
		Expect(globalcfg.EnsureLink(home, target, &bytes.Buffer{}, nil)).To(Succeed())
		p := build()
		Expect(mountsClaudeJSON(p)).To(BeFalse())
		Expect(p.EnvFlags).To(ContainElement(envFlag + target))
	})

	DescribeTable("a refused link: one warning, no mount, no env var",
		func(setup func()) {
			setup()
			p := build()
			Expect(mountsClaudeJSON(p)).To(BeFalse())
			Expect(hasGlobalEnv(p)).To(BeFalse())
			Expect(strings.Count(errw.String(), "WARNING: "+link)).To(Equal(1))
			Expect(out.String()).NotTo(ContainSubstring("Note: " + link))
		},
		Entry("CS-GCFG-019: a chain", func() {
			touch(target, "{}")
			Expect(os.Symlink(".claude/.claude.json", filepath.Join(home, "mid.json"))).To(Succeed())
			Expect(os.Symlink("mid.json", link)).To(Succeed())
		}),
		Entry("CS-GCFG-020: a link to another file", func() {
			other := filepath.Join(home, "dotfiles", "claude.json")
			touch(other, "{}")
			Expect(os.Symlink(other, link)).To(Succeed())
		}),
		Entry("CS-GCFG-021: a dangling link", func() {
			Expect(os.Symlink(".claude/.claude.json", link)).To(Succeed())
		}),
		Entry("CS-GCFG-022: a link to a directory", func() {
			mkdir(target)
			Expect(os.Symlink(".claude/.claude.json", link)).To(Succeed())
		}),
		Entry("CS-GCFG-023: a symlinked config dir", func() {
			real := filepath.Join(home, "real-claude")
			touch(filepath.Join(real, ".claude.json"), "{}")
			Expect(os.Remove(filepath.Join(home, ".claude"))).To(Succeed())
			Expect(os.Symlink("real-claude", filepath.Join(home, ".claude"))).To(Succeed())
			Expect(os.Symlink(".claude/.claude.json", link)).To(Succeed())
		}),
		Entry("CS-GCFG-024: a directory at ~/.claude.json", func() {
			mkdir(link)
		}),
	)

	It("CS-GCFG-025: a regular file is mounted as before, silently", func() {
		touch(link, "{}")
		p := build()
		Expect(p.Volumes).To(ContainElement(link + ":" + link))
		Expect(hasGlobalEnv(p)).To(BeFalse())
		quiet()
	})

	It("CS-GCFG-025: nested, the outer sandbox's single-file bind is mounted again; an image leftover is not", func() {
		touch(link, "{}")
		env["CLAUDE_SANDBOX_PROJECT_DIR"] = "/outer/proj"
		env["PRE_COMMIT_HOME"] = filepath.Join(home, ".cache/claude-sandbox/pre-commit")
		in.MountInfo = func() (string, error) { return withBind(link), nil }
		p := build()
		Expect(p.Volumes).To(ContainElement(link + ":" + link))
		Expect(errw.String()).NotTo(ContainSubstring(link))

		errw.Reset()
		in.MountInfo = func() (string, error) { return rootOnly, nil }
		p = build()
		Expect(mountsClaudeJSON(p)).To(BeFalse())
		Expect(strings.Count(errw.String(), "WARNING: "+link+" is not mounted: this launcher runs inside a sandbox")).To(Equal(1))
		Expect(errw.String()).To(ContainSubstring("container's own root filesystem"))
	})

	It("CS-GCFG-025: nothing at ~/.claude.json mounts and prints nothing", func() {
		p := build()
		Expect(mountsClaudeJSON(p)).To(BeFalse())
		Expect(hasGlobalEnv(p)).To(BeFalse())
		quiet()
	})

	It("CS-GCFG-026: split brain warns once instead of the note, and still mounts the legacy file", func() {
		touch(link, "{}")
		touch(target, "{}")
		p := build()
		Expect(p.Volumes).To(ContainElement(link + ":" + link))
		Expect(strings.Count(errw.String(), "WARNING: two global config files: "+link)).To(Equal(1))
		Expect(out.String()).NotTo(ContainSubstring("Note: " + link))

		errw.Reset()
		env["CLAUDE_SANDBOX_PROJECT_DIR"] = "/outer/proj"
		env["PRE_COMMIT_HOME"] = filepath.Join(home, ".cache/claude-sandbox/pre-commit")
		in.MountInfo = func() (string, error) { return withBind(link), nil }
		build()
		Expect(errw.String()).NotTo(ContainSubstring("two global config files"), "not repeated in a sandbox")
	})

	It("CS-GCFG-027: with CLAUDE_CONFIG_DIR set nothing is decided, set or mounted", func() {
		touch(link, "{}")
		touch(target, "{}")
		env["CLAUDE_CONFIG_DIR"] = filepath.Join(home, ".claude")
		p := build()
		Expect(hasGlobalEnv(p)).To(BeFalse())
		Expect(mountsClaudeJSON(p)).To(BeFalse())
		Expect(errw.String()).NotTo(ContainSubstring("claude.json"), "no split-brain or link warning")
		Expect(out.String()).NotTo(ContainSubstring("claude.json"))
	})

	It("CS-GCFG-056: with CLAUDE_CONFIG_DIR set, <parent>/.claude.json is never mounted, whatever it is", func() {
		secret := filepath.Join(home, ".ssh", "id_ed25519")
		touch(secret, "key")
		cd := filepath.Join(home, "work", ".claude-alt")
		mkdir(cd)
		env["CLAUDE_CONFIG_DIR"] = cd
		sib := filepath.Join(filepath.Dir(cd), ".claude.json")
		for _, kind := range []string{"regular", "link-to-secret", "link-to-target", "absent"} {
			os.Remove(sib)
			switch kind {
			case "regular":
				touch(sib, "{}")
			case "link-to-secret":
				Expect(os.Symlink(secret, sib)).To(Succeed())
			case "link-to-target":
				touch(target, "{}")
				Expect(os.Symlink(target, sib)).To(Succeed())
			}
			out.Reset()
			errw.Reset()
			p := build()
			for _, v := range p.Volumes {
				Expect(v).NotTo(ContainSubstring(".claude.json:"), kind)
				Expect(v).NotTo(ContainSubstring("id_ed25519"), kind)
			}
			Expect(out.String()).NotTo(ContainSubstring("claude.json"), kind)
			Expect(errw.String()).NotTo(ContainSubstring("claude.json"), kind)
		}
		// Relative and "~" values still mount nothing beside them. No files
		// are made for them: they would resolve against the test's cwd.
		for _, v := range []string{"rel/.claude", "~/.claude"} {
			env["CLAUDE_CONFIG_DIR"] = v
			for _, vol := range build().Volumes {
				Expect(vol).NotTo(ContainSubstring(".claude.json:"), v)
			}
		}
	})

	It("CS-GCFG-057: a migrated host with CLAUDE_CONFIG_DIR=$HOME/.claude launches silently", func() {
		linked()
		env["CLAUDE_CONFIG_DIR"] = filepath.Join(home, ".claude")
		for i := 0; i < 2; i++ {
			out.Reset()
			errw.Reset()
			p := build()
			Expect(hasGlobalEnv(p)).To(BeFalse())
			Expect(mountsClaudeJSON(p)).To(BeFalse(), "no mount of its own")
			cd := filepath.Join(home, ".claude")
			Expect(p.Volumes).To(ContainElement(cd+":"+cd), "the config-dir mount carries ~/.claude/.claude.json")
			Expect(p.Volumes).To(ContainElement(HaveSuffix(":"+filepath.Join(home, ".mcp.json")+":ro")), "CS-LNCH-013 still shadows .mcp.json")
			Expect(out.String()).NotTo(ContainSubstring("claude.json"))
			Expect(errw.String()).NotTo(ContainSubstring("claude.json"))
		}
	})

	It("CS-GCFG-059: a config-dir .claude.json linking outside the config dir warns once and mounts nothing", func() {
		alt := filepath.Join(home, "alt", ".claude")
		mkdir(alt)
		env["CLAUDE_CONFIG_DIR"] = alt
		cj := filepath.Join(alt, ".claude.json")
		parentFile := filepath.Join(home, "alt", ".claude.json")

		// ../.claude.json, existing and dangling, relative and absolute.
		for _, tc := range []struct{ text, make string }{
			{"../.claude.json", parentFile},
			{"../.claude.json", ""},
			{parentFile, parentFile},
		} {
			os.Remove(cj)
			os.Remove(parentFile)
			if tc.make != "" {
				touch(tc.make, "{}")
			}
			Expect(os.Symlink(tc.text, cj)).To(Succeed())
			errw.Reset()
			p := build()
			Expect(strings.Count(errw.String(), "WARNING: "+cj+" is a symlink to "+parentFile)).To(Equal(1), tc.text)
			Expect(errw.String()).To(ContainSubstring("the container cannot see its target"))
			Expect(mountsClaudeJSON(p)).To(BeFalse(), tc.text)
		}

		// Inside the config dir, a regular file, or absent: silent.
		os.Remove(cj)
		touch(filepath.Join(alt, "real.json"), "{}")
		Expect(os.Symlink("real.json", cj)).To(Succeed())
		for _, step := range []func(){
			func() {},
			func() { os.Remove(cj); touch(cj, "{}") },
			func() { os.Remove(cj) },
		} {
			step()
			errw.Reset()
			build()
			Expect(errw.String()).NotTo(ContainSubstring(".claude.json"))
		}
	})

	It("CS-GCFG-058: with CLAUDE_CONFIG_DIR set, the parent sibling no longer moves the config hash", func() {
		alt := filepath.Join(home, "alt", ".claude")
		mkdir(alt)
		env["CLAUDE_CONFIG_DIR"] = alt
		without := build().ConfigHash
		touch(filepath.Join(home, "alt", ".claude.json"), "{}")
		Expect(build().ConfigHash).To(Equal(without))
		Expect(without).NotTo(BeEmpty())
	})

	It("CS-GCFG-028: a relative or ~ CLAUDE_CONFIG_DIR warns once", func() {
		for _, v := range []string{"rel/.claude", "~/.claude"} {
			errw.Reset()
			env["CLAUDE_CONFIG_DIR"] = v
			p := build()
			Expect(hasGlobalEnv(p)).To(BeFalse())
			Expect(strings.Count(errw.String(), "WARNING: CLAUDE_CONFIG_DIR=")).To(Equal(1), v)
		}
	})

	It("CS-GCFG-029: .config.json keeps .claude.json legacy, with one note", func() {
		linked()
		touch(filepath.Join(home, ".claude", ".config.json"), "{}")
		p := build()
		Expect(hasGlobalEnv(p)).To(BeFalse())
		Expect(mountsClaudeJSON(p)).To(BeFalse(), "a link is never mounted")
		Expect(strings.Count(out.String(), ".config.json exists")).To(Equal(1))

		Expect(os.Remove(link)).To(Succeed())
		touch(link, "{}")
		out.Reset()
		p = build()
		Expect(p.Volumes).To(ContainElement(link + ":" + link))
		Expect(out.String()).To(ContainSubstring(".config.json exists"))
		Expect(out.String()).NotTo(ContainSubstring("is a regular file (the legacy layout)"), "the .config.json note replaces the legacy one")
	})

	Describe("the fingerprint (CS-GCFG-030/031)", func() {
		hashNow := func() string {
			out.Reset()
			errw.Reset()
			return build().ConfigHash
		}

		It("CS-GCFG-030: linked hashes differently from legacy; the env var itself is not hashed", func() {
			touch(link, "{}")
			legacy := hashNow()
			Expect(os.Remove(link)).To(Succeed())
			linked()
			p := build()
			Expect(p.ConfigHash).NotTo(Equal(legacy))
			Expect(hasGlobalEnv(p)).To(BeTrue())
		})

		It("CS-GCFG-030: the env var is not among the hashed inputs", func() {
			// EnvFlags never reach configFingerprint; the recorded inputs
			// (the drift explanation) do not name it either.
			linked()
			p := build()
			for _, d := range p.ConfigInputs {
				Expect(d.Path).NotTo(ContainSubstring("GLOBAL_CONFIG"))
			}
		})

		It("CS-GCFG-030: legacy, missing and refused launches add no globalConfig line", func() {
			// Missing and a dangling link produce the same mount set and no
			// line, so they hash alike; a linked launch with the same mount
			// set differs only by the line.
			missing := hashNow()
			Expect(os.Symlink(".claude/.claude.json", link)).To(Succeed())
			Expect(hashNow()).To(Equal(missing), "a refused link hashes like no file")
			touch(target, "{}")
			// Target created: now linked. Same mounts as missing (the config
			// dir existed all along), so only the line differs.
			Expect(hashNow()).NotTo(Equal(missing))
		})

		It("CS-GCFG-031: a migrate and a revert both change the would-be hash", func() {
			touch(link, "{}")
			beforeMigrate := hashNow()
			// migrate by hand: the file moves into ~/.claude, the link replaces it
			Expect(os.Rename(link, target)).To(Succeed())
			Expect(os.Symlink(".claude/.claude.json", link)).To(Succeed())
			afterMigrate := hashNow()
			Expect(afterMigrate).NotTo(Equal(beforeMigrate))
			// revert: a regular file again
			Expect(os.Remove(link)).To(Succeed())
			Expect(os.Rename(target, link)).To(Succeed())
			afterRevert := hashNow()
			Expect(afterRevert).NotTo(Equal(afterMigrate))
			Expect(afterRevert).To(Equal(beforeMigrate))
		})
	})

	It("CS-GCFG-032: every kind of new container carries the layout — ralph, headless, detached", func() {
		linked()
		for _, kind := range []string{"ralph", "headless", "detached", "interactive"} {
			k := in
			switch kind {
			case "ralph":
				k.RalphMode = true
			case "headless":
				k.Headless = true
			case "detached":
				k.Detached = true
			}
			p, err := launch.Build(k)
			Expect(err).NotTo(HaveOccurred())
			Expect(p.EnvFlags).To(ContainElement(envFlag+target), kind)
			Expect(mountsClaudeJSON(p)).To(BeFalse(), kind)
		}
	})

	It("CS-GCFG-032: an env file's CLAUDE_SANDBOX_GLOBAL_CONFIG is overridden with an empty -e, with one warning", func() {
		touch(link, "{}") // legacy
		in.Env = []cascade.EnvFile{{Path: filepath.Join(proj, ".claude-sandbox/env"), Content: []byte(envFlag + target + "\n")}}
		p := build()
		Expect(p.EnvFlags).To(ContainElement(envFlag))
		Expect(p.EnvFlags).NotTo(ContainElement(envFlag + target))
		Expect(strings.Count(errw.String(), "WARNING: an env file sets CLAUDE_SANDBOX_GLOBAL_CONFIG")).To(Equal(1))
		args := p.CreateArgs(proj)
		Expect(args).To(ContainElements("-e", envFlag))
	})

	It("CS-GCFG-032: a linked launch's own -e already wins over an env file, silently", func() {
		linked()
		in.Env = []cascade.EnvFile{{Path: filepath.Join(proj, ".claude-sandbox/env"), Content: []byte(envFlag + "/elsewhere\n")}}
		p := build()
		Expect(p.EnvFlags).To(ContainElement(envFlag + target))
		Expect(p.EnvFlags).NotTo(ContainElement(envFlag))
		Expect(errw.String()).NotTo(ContainSubstring("an env file sets"))
	})

	It("CS-GCFG-032: without an env-file line nothing extra is passed", func() {
		touch(link, "{}")
		Expect(hasGlobalEnv(build())).To(BeFalse())
	})
})
