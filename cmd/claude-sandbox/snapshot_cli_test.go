package main

// Spec: spec/config-cascade.feature CS-CASC-046 and spec/image-build.feature
// CS-IMG-073/074 — end to end through MainWithEnv: the launch reads each
// config.yaml and the child Dockerfile once, and a rewrite of either after
// that read reaches nothing in the launch.

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/imagebuild"
)

var _ = Describe("the launch-config snapshot (CS-CASC-046, CS-IMG-073/074)", func() {
	var f *cliFixture
	BeforeEach(func() { f = newCLIFixture() })

	It("CS-CASC-046: a config.yaml rewritten mid-launch changes nothing: the key sources come from the bytes the launch merged", func() {
		cfg := filepath.Join(f.proj, ".claude-sandbox", "config.yaml")
		writeFile(cfg, "memoryLimit: 16g\n")
		// The image work runs between the merge and the create; a session
		// rewrites the file while it does.
		f.fake.OnFunc("docker buildx version", func(execx.Cmd) (string, error) {
			Expect(os.WriteFile(cfg, []byte("model: planted\n"), 0o644)).To(Succeed())
			return "", nil
		})
		Expect(f.run()).To(Equal(0))
		args := f.launched().Args
		Expect(args).To(ContainElements("claude-sandbox.memorylimit=16g", "claude-sandbox.memorylimitsource="+cfg))
		Expect(f.out.String()).To(ContainSubstring(filepath.Join(f.proj, ".claude-sandbox") + "/  →  config.yaml"))
	})

	It("CS-CASC-046: the oomScoreAdj source is named from the bytes the launch merged, not a later read", func() {
		cfg := filepath.Join(f.proj, ".claude-sandbox", "config.yaml")
		writeFile(cfg, "oomScoreAdj: -5000\n")
		// Session discovery runs between the merge and the key lookup; a
		// session rewrites the file while it does.
		f.fake.OnFunc("docker ps", func(execx.Cmd) (string, error) {
			Expect(os.WriteFile(cfg, []byte("model: planted\n"), 0o644)).To(Succeed())
			return "", nil
		})
		Expect(f.run()).To(Equal(2))
		Expect(f.errw.String()).To(ContainSubstring("oomScoreAdj: -5000 in " + cfg))
		Expect(createdAny(f)).To(BeFalse())
	})

	// childBuild forces the child build for df and records what the build saw
	// in its private directory (removed after the build, CS-IMG-073).
	type childSeen struct {
		args               []string
		dockerfile, ignore string
	}
	childBuild := func(df string, rewrite func()) *childSeen {
		delete(f.envmap, "CLAUDE_SANDBOX_BASE_ONLY")
		tag := "claude-sandbox-" + imagebuild.ImageSlug(df, f.proj)
		// The child's existence check runs after the read and before the
		// build: rewrite there, and report the image missing.
		f.fake.OnFunc("image inspect "+tag, func(execx.Cmd) (string, error) {
			rewrite()
			return "", execx.Fail(1)
		})
		var seen *childSeen
		f.fake.OnFunc("docker build -t "+tag+" ", func(c execx.Cmd) (string, error) {
			seen = &childSeen{args: c.Args}
			for i, a := range c.Args {
				if a == "-f" && i+1 < len(c.Args) {
					raw, _ := os.ReadFile(c.Args[i+1])
					seen.dockerfile = string(raw)
					raw, _ = os.ReadFile(c.Args[i+1] + ".dockerignore")
					seen.ignore = string(raw)
				}
			}
			Expect(c.Stdin).To(BeNil())
			return "", nil
		})
		Expect(f.run()).To(Equal(0))
		Expect(seen).NotTo(BeNil(), "the child build ran")
		return seen
	}

	It("CS-IMG-073: the child image is built from the bytes the launch read, through a private file, even if the file changed after", func() {
		df := filepath.Join(f.proj, ".claude-sandbox", "Dockerfile")
		writeFile(df, "FROM claude-sandbox\nRUN echo checked\n")
		seen := childBuild(df, func() {
			Expect(os.WriteFile(df, []byte("FROM claude-sandbox\nENV LD_PRELOAD=/planted.so\n"), 0o644)).To(Succeed())
		})
		n := len(seen.args)
		Expect(seen.args[n-3]).To(Equal("-f"))
		Expect(seen.args[n-2]).To(HavePrefix(f.env.TempRoot + string(filepath.Separator)))
		Expect(seen.args[n-1]).To(Equal(f.proj))
		Expect(seen.dockerfile).To(Equal("FROM claude-sandbox\nRUN echo checked\n"))
	})

	It("CS-IMG-078: the env file is excluded from the child build context, after the project's own rules", func() {
		df := filepath.Join(f.proj, ".claude-sandbox", "Dockerfile")
		writeFile(df, "FROM claude-sandbox\nCOPY . /src\n")
		writeFile(filepath.Join(f.proj, ".dockerignore"), "*.log\n!.claude-sandbox/env\n")
		seen := childBuild(df, func() {})
		Expect(seen.ignore).To(Equal("*.log\n!.claude-sandbox/env\n**/.claude-sandbox/env\n"))
	})

	It("CS-IMG-078: the launch's cascade env files reach the build as context-relative rules", func() {
		df := filepath.Join(f.proj, ".claude-sandbox", "Dockerfile")
		writeFile(df, "FROM claude-sandbox\n")
		writeFile(filepath.Join(f.proj, ".claude-sandbox", "env"), "TOKEN=x\n")
		seen := childBuild(df, func() {})
		Expect(seen.ignore).To(Equal("**/.claude-sandbox/env\n/.claude-sandbox/env\n"))
	})

	It("CS-IMG-078: dockerfileDir naming the .claude-sandbox dir keeps its env (at the context root) out", func() {
		sbx := filepath.Join(f.proj, ".claude-sandbox")
		df := filepath.Join(sbx, "Dockerfile")
		writeFile(df, "FROM claude-sandbox\n")
		writeFile(filepath.Join(sbx, "env"), "TOKEN=x\n")
		f.envmap["CLAUDE_SANDBOX_DOCKERFILE_DIR"] = sbx
		delete(f.envmap, "CLAUDE_SANDBOX_BASE_ONLY")
		tag := "claude-sandbox-" + imagebuild.ImageSlug(df, sbx)
		f.fake.On("image inspect "+tag, "", execx.Fail(1))
		var ignore string
		f.fake.OnFunc("docker build -t "+tag+" ", func(c execx.Cmd) (string, error) {
			Expect(c.Args[len(c.Args)-1]).To(Equal(sbx))
			raw, _ := os.ReadFile(c.Args[len(c.Args)-2] + ".dockerignore")
			ignore = string(raw)
			return "", nil
		})
		Expect(f.run()).To(Equal(0))
		Expect(ignore).To(Equal("**/.claude-sandbox/env\n/env\n"))
	})

	It("CS-IMG-074: an unreadable child Dockerfile fails the launch naming it, before anything is created", func() {
		if os.Geteuid() == 0 {
			Skip("root reads unreadable files")
		}
		df := filepath.Join(f.proj, ".claude-sandbox", "Dockerfile")
		writeFile(df, "FROM claude-sandbox\n")
		Expect(os.Chmod(df, 0o000)).To(Succeed())
		delete(f.envmap, "CLAUDE_SANDBOX_BASE_ONLY")
		Expect(f.run()).To(Equal(2))
		Expect(f.errw.String()).To(ContainSubstring(df))
		Expect(createdAny(f)).To(BeFalse())
	})

	It("CS-IMG-074: no child Dockerfile anywhere is base only, as before", func() {
		delete(f.envmap, "CLAUDE_SANDBOX_BASE_ONLY")
		Expect(f.run()).To(Equal(0))
		Expect(f.errw.String()).To(ContainSubstring("No child Dockerfile found"))
		Expect(f.launchLine()).To(ContainSubstring("claude-sandbox:run"))
	})
})
