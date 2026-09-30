package main

// Spec: spec/config-cascade.feature CS-CASC-046 and spec/image-build.feature
// CS-IMG-073/074 — end to end through MainWithEnv: the launch reads each
// config.yaml and the child Dockerfile once, and a rewrite of either after
// that read reaches nothing in the launch.

import (
	"io"
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

	It("CS-IMG-073: the child image is built from the bytes the launch read, on stdin, even if the file changed after", func() {
		df := filepath.Join(f.proj, ".claude-sandbox", "Dockerfile")
		writeFile(df, "FROM claude-sandbox\nRUN echo checked\n")
		delete(f.envmap, "CLAUDE_SANDBOX_BASE_ONLY")
		tag := "claude-sandbox-" + imagebuild.ImageSlug(df, f.proj)
		// The child's existence check runs after the read and before the
		// build: rewrite the file there, and report the image missing.
		f.fake.OnFunc("image inspect "+tag, func(execx.Cmd) (string, error) {
			Expect(os.WriteFile(df, []byte("FROM claude-sandbox\nENV LD_PRELOAD=/planted.so\n"), 0o644)).To(Succeed())
			return "", execx.Fail(1)
		})
		Expect(f.run()).To(Equal(0))
		var build *execx.Cmd
		for i, c := range f.fake.Calls {
			if c.Name == "docker" && len(c.Args) > 2 && c.Args[0] == "build" && c.Args[2] == tag {
				build = &f.fake.Calls[i]
			}
		}
		Expect(build).NotTo(BeNil(), "the child build ran")
		Expect(build.Args[len(build.Args)-3:]).To(Equal([]string{"-f", "-", f.proj}))
		raw, err := io.ReadAll(build.Stdin)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(raw)).To(Equal("FROM claude-sandbox\nRUN echo checked\n"))
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
