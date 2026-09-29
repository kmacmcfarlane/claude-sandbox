package main

// Spec: spec/tmux.feature (CS-TMUX-001..002) — the bin/claude-sandbox shim
// execs the launcher with argv[0] "claude-sandbox", so the first word of the
// full command line tmux-resurrect saves matches a plain `claude-sandbox`
// @resurrect-processes entry (the bin/dist path, possibly with a space, never did).
//
// The shim is run for real (bash) from a scratch copy of the repo layout. Its
// bin/dist/claude-sandbox is a symlink to THIS test binary: a script would not
// do, because the kernel replaces a script's argv[0] with the interpreter's.
// When shimArgv0Env is set, the init below records os.Args and the repo-root
// variable and exits before any test runs.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const shimArgv0Env = "CLAUDE_SANDBOX_TEST_SHIM_ARGV0_OUT"

type shimArgv0Record struct {
	Args     []string `json:"args"`
	RepoRoot string   `json:"repoRoot"`
}

func init() {
	out := os.Getenv(shimArgv0Env)
	if out == "" {
		return
	}
	b, _ := json.Marshal(shimArgv0Record{Args: os.Args, RepoRoot: os.Getenv("CLAUDE_SANDBOX_REPO_ROOT")})
	if err := os.WriteFile(out, b, 0o600); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

var _ = Describe("shim argv0 (spec/tmux.feature)", func() {
	var (
		repo string
		out  string
	)

	BeforeEach(func() {
		scratch := GinkgoT().TempDir()
		repo = filepath.Join(scratch, "repo")
		Expect(os.MkdirAll(filepath.Join(repo, "bin", "dist"), 0o755)).To(Succeed())
		// The shim resolves its own path (readlink -f); compare physical paths.
		var err error
		repo, err = filepath.EvalSymlinks(repo)
		Expect(err).NotTo(HaveOccurred())

		shim, err := os.ReadFile(filepath.Join("..", "..", "bin", "claude-sandbox"))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(repo, "bin", "claude-sandbox"), shim, 0o755)).To(Succeed())

		self, err := os.Executable()
		Expect(err).NotTo(HaveOccurred())
		// No build sources exist in the scratch repo, so the shim never builds.
		Expect(os.Symlink(self, filepath.Join(repo, "bin", "dist", "claude-sandbox"))).To(Succeed())

		out = filepath.Join(scratch, "argv.json")
	})

	runShim := func(args ...string) shimArgv0Record {
		cmd := exec.Command("bash", append([]string{filepath.Join(repo, "bin", "claude-sandbox")}, args...)...)
		env := []string{shimArgv0Env + "=" + out}
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, shimArgv0Env+"=") && !strings.HasPrefix(kv, "CLAUDE_SANDBOX_REPO_ROOT=") {
				env = append(env, kv)
			}
		}
		cmd.Env = env
		combined, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(combined))
		b, err := os.ReadFile(out)
		Expect(err).NotTo(HaveOccurred())
		var rec shimArgv0Record
		Expect(json.Unmarshal(b, &rec)).To(Succeed())
		return rec
	}

	It("CS-TMUX-001: the shim execs the launcher with argv[0] \"claude-sandbox\"", func() {
		rec := runShim("--new", "--", "--resume", "a b", "")
		Expect(rec.Args).To(Equal([]string{"claude-sandbox", "--new", "--", "--resume", "a b", ""}))
		Expect(rec.RepoRoot).To(Equal(repo))
	})

	It("CS-TMUX-002: the completion fast path execs with the same argv[0]", func() {
		for _, sub := range []string{"__complete", "__completeNoDesc"} {
			rec := runShim(sub, "--attach=", "")
			Expect(rec.Args).To(Equal([]string{"claude-sandbox", sub, "--attach=", ""}))
			Expect(rec.RepoRoot).To(Equal(repo))
		}
	})
})
