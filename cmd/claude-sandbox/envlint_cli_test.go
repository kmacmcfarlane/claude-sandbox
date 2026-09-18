package main

// Spec: spec/config-cascade.feature CS-CASC-020 — every env file in the
// cascade is linted at launch, warnings go to stderr, and the launch proceeds.
// CS-CASC-030 — env.example is a template, never part of the env cascade.

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func readFile(p string) string {
	raw, err := os.ReadFile(p)
	Expect(err).NotTo(HaveOccurred())
	return string(raw)
}

var _ = Describe("env file linting at launch", func() {
	It("CS-CASC-020: warns for every cascade level and still launches", func() {
		f := newCLIFixture()
		parent := filepath.Dir(f.proj)
		parentEnv := filepath.Join(parent, ".claude-sandbox", "env")
		projEnv := filepath.Join(f.proj, ".claude-sandbox", "env")
		writeFile(parentEnv, "UPSTREAM=\"quoted\"\n")
		writeFile(projEnv, "LOCAL='quoted'\n")

		Expect(f.run()).To(Equal(0))

		errOut := f.errw.String()
		Expect(errOut).To(ContainSubstring(parentEnv + ":1: value for UPSTREAM is wrapped in \" quotes."))
		Expect(errOut).To(ContainSubstring(projEnv + ":1: value for LOCAL is wrapped in ' quotes."))

		// Warn-only: both files still feed --env-file, and neither is rewritten.
		Expect(f.launched().Args).To(ContainElements("--env-file", parentEnv))
		Expect(f.launched().Args).To(ContainElements("--env-file", projEnv))
		Expect(readFile(projEnv)).To(Equal("LOCAL='quoted'\n"))
	})

	It("CS-CASC-020: stays silent for clean env files", func() {
		f := newCLIFixture()
		writeFile(filepath.Join(f.proj, ".claude-sandbox", "env"), "CLEAN=value\n")

		Expect(f.run()).To(Equal(0))
		Expect(f.errw.String()).NotTo(ContainSubstring("wrapped in"))
		Expect(f.errw.String()).NotTo(ContainSubstring("carriage return"))
	})

	It("CS-CASC-030: env.example is a template, never an env file", func() {
		f := newCLIFixture()
		parent := filepath.Dir(f.proj)
		upstream := filepath.Join(parent, ".claude-sandbox", "env")
		example := filepath.Join(f.proj, ".claude-sandbox", "env.example")
		writeFile(upstream, "TOKEN=upstream\n")
		// Quoted so linting it would be visible.
		writeFile(example, "TOKEN=\"example\"\n")

		Expect(f.run()).To(Equal(0))

		var envFiles []string
		args := f.launched().Args
		for i, a := range args {
			if a == "--env-file" && i+1 < len(args) {
				envFiles = append(envFiles, args[i+1])
			}
		}
		Expect(envFiles).To(Equal([]string{upstream}))
		Expect(f.errw.String()).NotTo(ContainSubstring(example))
		Expect(f.out.String()).NotTo(ContainSubstring("env.example"))
		// With an upstream env present, no missing-env message at all.
		Expect(f.errw.String()).NotTo(ContainSubstring("env file not found"))
		Expect(f.errw.String()).NotTo(ContainSubstring("Note: no .claude-sandbox/env"))
	})
})

// Spec: spec/config-cascade.feature CS-CASC-021/025 — the launch names env
// keys a more-local file overrides, names only.
var _ = Describe("env override notice at launch", func() {
	It("CS-CASC-021: prints one line naming the shadowed key, never its value", func() {
		f := newCLIFixture()
		parentEnv := filepath.Join(filepath.Dir(f.proj), ".claude-sandbox", "env")
		projEnv := filepath.Join(f.proj, ".claude-sandbox", "env")
		writeFile(parentEnv, "GITLAB_TOKEN=fresh-secret\n")
		writeFile(projEnv, "GITLAB_TOKEN=stale-secret\n")

		Expect(f.run()).To(Equal(0))

		out := f.out.String()
		Expect(out).To(ContainSubstring("Env override: GITLAB_TOKEN in " + projEnv + " overrides " + parentEnv + "\n"))
		Expect(out + f.errw.String()).NotTo(ContainSubstring("secret"))
	})

	It("CS-CASC-024: prints no notice when env files share no key", func() {
		f := newCLIFixture()
		writeFile(filepath.Join(filepath.Dir(f.proj), ".claude-sandbox", "env"), "UP=1\n")
		writeFile(filepath.Join(f.proj, ".claude-sandbox", "env"), "LOCAL=1\n")

		Expect(f.run()).To(Equal(0))
		Expect(f.out.String()).NotTo(ContainSubstring("Env override"))
	})

	It("CS-CASC-027: a bare key overrides when the launcher's environment sets it", func() {
		f := newCLIFixture()
		f.envmap["GITLAB_TOKEN"] = "host-secret"
		parentEnv := filepath.Join(filepath.Dir(f.proj), ".claude-sandbox", "env")
		projEnv := filepath.Join(f.proj, ".claude-sandbox", "env")
		writeFile(parentEnv, "GITLAB_TOKEN=fresh-value\n")
		writeFile(projEnv, "GITLAB_TOKEN\n")

		Expect(f.run()).To(Equal(0))
		Expect(f.out.String()).To(ContainSubstring("Env override: GITLAB_TOKEN in " + projEnv + " overrides " + parentEnv + "\n"))
		Expect(f.out.String()).NotTo(ContainSubstring("host-secret"))
	})

	It("CS-CASC-027: a CRLF bare key set-but-empty in the launcher's environment overrides", func() {
		f := newCLIFixture()
		f.envmap["GITLAB_TOKEN"] = ""
		parentEnv := filepath.Join(filepath.Dir(f.proj), ".claude-sandbox", "env")
		projEnv := filepath.Join(f.proj, ".claude-sandbox", "env")
		writeFile(parentEnv, "GITLAB_TOKEN=fresh-value\r\n")
		writeFile(projEnv, "GITLAB_TOKEN\r\n")

		Expect(f.run()).To(Equal(0))
		Expect(f.out.String()).To(ContainSubstring("Env override: GITLAB_TOKEN in " + projEnv + " overrides " + parentEnv + "\n"))
	})
})
