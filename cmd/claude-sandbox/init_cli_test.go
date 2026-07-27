package main

// Spec: spec/init.feature (CS-INIT-001..003), spec/init-ralph.feature
// (CS-INITR-001), spec/layout.feature (CS-LAY-015/016) — CLI-level behavior
// through MainWithEnv. All external commands (git, docker) go through
// execx.Fake; the final docker run is recorded via Fake.Execed.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
)

// initCLI bundles one MainWithEnv invocation's collaborators.
type initCLI struct {
	env      *Env
	fake     *execx.Fake
	prompter *prompt.Scripted
	out, err bytes.Buffer
	vars     map[string]string
}

func newInitCLI(vars map[string]string) *initCLI {
	c := &initCLI{fake: &execx.Fake{}, prompter: &prompt.Scripted{}, vars: vars}
	if c.vars == nil {
		c.vars = map[string]string{}
	}
	c.env = &Env{
		Runner: c.fake, Prompter: c.prompter, Out: &c.out, Err: &c.err,
		Getenv: func(k string) string { return c.vars[k] },
	}
	return c
}

// launchVars builds the env-var map for a full launch-path run: everything
// docker-shaped is faked, images "exist", update check and child Dockerfile
// detection are disabled.
func launchVars(project string) map[string]string {
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	Expect(err).NotTo(HaveOccurred())
	cfgDir := filepath.Join(GinkgoT().TempDir(), "claude-config")
	Expect(os.MkdirAll(cfgDir, 0o755)).To(Succeed())
	return map[string]string{
		"PROJECT_DIR":                    project,
		"CLAUDE_SANDBOX_REPO_ROOT":       repo,
		"CLAUDE_CONFIG_DIR":              cfgDir,
		"CLAUDE_SANDBOX_NO_UPDATE_CHECK": "1",
		"CLAUDE_SANDBOX_BASE_ONLY":       "1",
	}
}

func mkProject() string {
	proj := filepath.Join(GinkgoT().TempDir(), "p")
	Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
	// Normalize through symlinks: resolveProjectDir does the same.
	resolved, err := filepath.EvalSymlinks(proj)
	Expect(err).NotTo(HaveOccurred())
	return resolved
}

func writeCLI(p, content string) {
	Expect(os.MkdirAll(filepath.Dir(p), 0o755)).To(Succeed())
	Expect(os.WriteFile(p, []byte(content), 0o644)).To(Succeed())
}

var _ = Describe("init CLI invocation shape", func() {
	BeforeEach(func() {
		os.Unsetenv("CS_GITIGNORE_ASSUME")
	})

	It("CS-INIT-001: init must be the first argument", func() {
		c := newInitCLI(nil)
		code := MainWithEnv([]string{"--rebuild", "init"}, c.env)
		Expect(code).To(Equal(2))
		Expect(c.err.String()).To(ContainSubstring("'init' must be the first argument"))
	})

	It("CS-INIT-002: init rejects launcher and claude flags", func() {
		c := newInitCLI(map[string]string{"PROJECT_DIR": mkProject()})
		code := MainWithEnv([]string{"init", "--rebuild"}, c.env)
		Expect(code).To(Equal(2))
		Expect(c.err.String()).To(ContainSubstring("--rebuild"))
		// ...and lists the valid init options.
		Expect(c.err.String()).To(ContainSubstring("--track-in-host"))
		Expect(c.err.String()).To(ContainSubstring("--no-gitignore"))
		Expect(c.err.String()).To(ContainSubstring("--yes"))
	})

	It("CS-INIT-003: init --help prints usage and exits 0", func() {
		c := newInitCLI(nil)
		code := MainWithEnv([]string{"init", "--help"}, c.env)
		Expect(code).To(Equal(0))
		Expect(c.out.String()).To(ContainSubstring("Usage:"))
		Expect(c.out.String()).To(ContainSubstring("init"))
		Expect(c.out.String()).To(ContainSubstring("--track-in-host"))
	})

	It("CS-INITR-001: init-ralph accepts the same option flags as init", func() {
		proj := mkProject()
		c := newInitCLI(map[string]string{"PROJECT_DIR": proj})
		code := MainWithEnv([]string{
			"init-ralph", "--no-track-in-host", "--no-gitignore", "--no-copy-parent-dockerfile",
		}, c.env)
		Expect(code).To(Equal(0))

		sb := filepath.Join(proj, ".claude-sandbox")
		for _, rel := range []string{"config.yaml", "env", "Dockerfile.example", "agent/PROMPT.md", "scripts/backlog/backlog.py"} {
			_, err := os.Stat(filepath.Join(sb, rel))
			Expect(err).NotTo(HaveOccurred(), rel)
		}
		Expect(c.prompter.Asked).To(BeEmpty())
	})
})

var _ = Describe("layout adoption at launch", func() {
	BeforeEach(func() {
		os.Unsetenv("CS_GITIGNORE_ASSUME")
	})

	It("CS-LAY-015: launch runs SetupLayout whenever .claude-sandbox/ exists, with the effective cascade value", func() {
		By("host-tracked cascade value: no sidecar init")
		proj := mkProject()
		sb := filepath.Join(proj, ".claude-sandbox")
		writeCLI(filepath.Join(sb, "config.yaml"), "trackInHost: true\n")

		c := newInitCLI(launchVars(proj))
		Expect(MainWithEnv([]string{}, c.env)).To(Equal(0))

		_, err := os.Stat(filepath.Join(sb, "CLAUDE.md"))
		Expect(err).NotTo(HaveOccurred(), "SetupLayout must run before the container starts")
		_, err = os.Stat(filepath.Join(sb, "temp"))
		Expect(err).NotTo(HaveOccurred())
		Expect(c.fake.CommandLines()).NotTo(ContainElement(ContainSubstring(" init -q")))
		Expect(c.fake.Execed).NotTo(BeNil())
		Expect(c.fake.Execed.Name).To(Equal("docker"))

		By("default (foreign-safe) value: sidecar init runs before the docker run")
		proj2 := mkProject()
		sb2 := filepath.Join(proj2, ".claude-sandbox")
		writeCLI(filepath.Join(sb2, "config.yaml"), "# sparse\n")

		c2 := newInitCLI(launchVars(proj2))
		Expect(MainWithEnv([]string{}, c2.env)).To(Equal(0))

		lines := c2.fake.CommandLines()
		initIdx, runIdx := -1, -1
		for i, l := range lines {
			if strings.Contains(l, "git -C "+sb2+" init -q") {
				initIdx = i
			}
			if strings.HasPrefix(l, "docker run ") {
				runIdx = i
			}
		}
		Expect(initIdx).To(BeNumerically(">=", 0), "sidecar init expected")
		Expect(runIdx).To(BeNumerically(">", initIdx), "layout setup must precede the container start")
	})

	It("CS-LAY-016: greenfield ralph adopts the layout; greenfield interactive does not", func() {
		By(`"claude-sandbox --ralph" creates .claude-sandbox/ and runs SetupLayout`)
		proj := mkProject()
		c := newInitCLI(launchVars(proj))
		Expect(MainWithEnv([]string{"--ralph"}, c.env)).To(Equal(0))

		sb := filepath.Join(proj, ".claude-sandbox")
		_, err := os.Stat(filepath.Join(sb, "CLAUDE.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(c.fake.Execed).NotTo(BeNil())
		Expect(strings.Join(c.fake.Execed.Args, " ")).To(ContainSubstring("/opt/claude-sandbox/bin/ralph"))

		By(`interactive "claude-sandbox" leaves the project untouched`)
		proj2 := mkProject()
		c2 := newInitCLI(launchVars(proj2))
		Expect(MainWithEnv([]string{}, c2.env)).To(Equal(0))

		_, err = os.Stat(filepath.Join(proj2, ".claude-sandbox"))
		Expect(os.IsNotExist(err)).To(BeTrue(), ".claude-sandbox/ must not be created")
		Expect(c2.fake.Execed).NotTo(BeNil())
		Expect(c2.fake.Execed.Args).To(ContainElement("claude"))
	})
})
