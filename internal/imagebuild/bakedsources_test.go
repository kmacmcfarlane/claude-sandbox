package imagebuild_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/imagebuild"
)

// copySource is one source path of a COPY/ADD that reads from the build
// context, normalized to repo-relative form without "./" or a trailing slash.
type copySource struct {
	path       string
	chmod      bool // the instruction carries --chmod
	finalStage bool // the instruction is in the last FROM stage
}

// contextSources parses a Dockerfile and returns the source paths of every
// COPY/ADD that reads from the build context (not --from a stage or image).
func contextSources(dockerfile string) []string {
	var out []string
	for _, c := range contextCopies(dockerfile) {
		out = append(out, c.path)
	}
	return out
}

// contextCopies is contextSources with each source's --chmod and stage.
func contextCopies(dockerfile string) []copySource {
	// Join backslash continuations into one logical instruction per line.
	var logical []string
	cur := ""
	for _, line := range strings.Split(dockerfile, "\n") {
		t := strings.TrimSpace(line)
		if cur == "" && (t == "" || strings.HasPrefix(t, "#")) {
			continue
		}
		if strings.HasSuffix(t, "\\") {
			cur += strings.TrimSuffix(t, "\\") + " "
			continue
		}
		logical = append(logical, cur+t)
		cur = ""
	}
	lastFrom := -1
	for i, ins := range logical {
		if f := strings.Fields(ins); len(f) > 0 && strings.ToUpper(f[0]) == "FROM" {
			lastFrom = i
		}
	}
	var srcs []copySource
	for i, ins := range logical {
		f := strings.Fields(ins)
		if len(f) == 0 {
			continue
		}
		op := strings.ToUpper(f[0])
		if op != "COPY" && op != "ADD" {
			continue
		}
		var args []string
		fromStage, chmod := false, false
		for _, a := range f[1:] {
			if strings.HasPrefix(a, "--") {
				if strings.HasPrefix(a, "--from=") {
					fromStage = true
				}
				if strings.HasPrefix(a, "--chmod=") {
					chmod = true
				}
				continue
			}
			args = append(args, a)
		}
		if fromStage {
			continue
		}
		// The exec (JSON) form and heredocs would hide sources from this
		// parser; fail loudly so the test is extended rather than bypassed.
		Expect(ins).NotTo(MatchRegexp(`^\S+(\s+--\S+)*\s+[\[<]`), "unparsed COPY/ADD form: %s", ins)
		Expect(len(args)).To(BeNumerically(">=", 2), "COPY/ADD without a destination: %s", ins)
		for _, s := range args[:len(args)-1] {
			s = strings.TrimPrefix(s, "./")
			s = strings.TrimSuffix(s, "/")
			srcs = append(srcs, copySource{path: s, chmod: chmod, finalStage: i > lastFrom})
		}
	}
	return srcs
}

var _ = Describe("baked sources", func() {
	It("CS-IMG-037: every source Dockerfile.tools COPYs from the build context is a baked source", func() {
		srcs := contextSources(repoFile("Dockerfile.tools"))
		Expect(srcs).To(ContainElements("scaffold", "scaffold-ralph", "container-context.md", "mcp-servers.json"),
			"the parser found Dockerfile.tools' COPY lines")
		for _, s := range srcs {
			Expect(s).NotTo(BeElementOf("", "."), "a whole-context COPY cannot be tracked as a baked source")
			covered := false
			for _, b := range imagebuild.BakedSources {
				if s == b || strings.HasPrefix(s, b+"/") {
					covered = true
					break
				}
			}
			Expect(covered).To(BeTrue(), "Dockerfile.tools COPY source %q is not in imagebuild.BakedSources (CS-IMG-004)", s)
		}
	})

	It("CS-IMG-048: the base Dockerfile COPYs nothing from the build context and carries no version stamp", func() {
		df := repoFile("Dockerfile")
		Expect(contextSources(df)).To(BeEmpty(),
			"a baked source in the base would rebuild it, and every child, on each commit; put it in Dockerfile.tools")
		Expect(df).NotTo(ContainSubstring("CLAUDE_SANDBOX_VERSION"))
		Expect(df).NotTo(MatchRegexp(`(?m)^FROM\s+golang`), "the Go build belongs to Dockerfile.tools")
		// The files arrive with the cap; the base only names them.
		Expect(df).To(MatchRegexp(`(?m)^ENTRYPOINT \["/opt/claude-sandbox/bin/entrypoint\.sh"\]\s*$`))
		Expect(df).To(MatchRegexp(`(?m)^ENV PATH="/opt/claude-sandbox/bin:\$PATH"\s*$`))
	})

	It("CS-IMG-048: the scaffold child Dockerfile says the sandbox binary is not there at build time", func() {
		ex := repoFile("scaffold/Dockerfile.example")
		Expect(ex).To(ContainSubstring("claude-sandbox-tools"))
		Expect(ex).To(MatchRegexp(`(?s)RUN step that invokes.*claude-sandbox`))
	})

	It("CS-IMG-049: Dockerfile.tools lays out the files the cap copies", func() {
		df := repoFile("Dockerfile.tools")
		for _, re := range []string{
			`(?m)^COPY --link --chmod=755 entrypoint\.sh /opt/claude-sandbox/bin/entrypoint\.sh\s*$`,
			`(?m)^COPY --link --chmod=755 --from=builder /out/claude-sandbox /opt/claude-sandbox/bin/claude-sandbox\s*$`,
			`(?m)^RUN ln -s /opt/claude-sandbox/bin/claude-sandbox /opt/claude-sandbox/bin/ralph\s*$`,
			`(?m)^COPY --link logstream/ /opt/claude-sandbox/logstream/\s*$`,
			`(?m)^COPY --link PROMPT_RALPH\.md /opt/claude-sandbox/PROMPT_RALPH\.md\s*$`,
			`(?m)^COPY --link --from=mcp /src/dist/index\.mjs /opt/claude-sandbox/mcp/discord-notify/dist/index\.mjs\s*$`,
			`(?m)^ARG CLAUDE_SANDBOX_VERSION=unknown\s*$`,
			`(?m)^RUN echo "\$CLAUDE_SANDBOX_VERSION" > /opt/claude-sandbox/version\s*$`,
			`(?m)^LABEL org\.opencontainers\.image\.revision=\$CLAUDE_SANDBOX_VERSION\s*$`,
		} {
			Expect(df).To(MatchRegexp(re))
		}
		// The MCP bundle is built in its own node stage from the COPYed source.
		Expect(df).To(MatchRegexp(`(?m)^FROM node:22\S* AS mcp\s*$`))
		Expect(df).To(MatchRegexp(`(?m)^COPY mcp/discord-notify/ \./\s*$`))
		// mcp-servers.json runs exactly the bundle the tools image ships.
		Expect(repoFile("mcp-servers.json")).To(ContainSubstring(`"/opt/claude-sandbox/mcp/discord-notify/dist/index.mjs"`))
	})

	Describe("CS-IMG-050: setup-lsp-plugins", func() {
		It("CS-IMG-050: Dockerfile.tools ships it executable on the session PATH", func() {
			df := repoFile("Dockerfile.tools")
			Expect(df).To(MatchRegexp(`(?m)^COPY --link --chmod=755 bin/setup-lsp-plugins /opt/claude-sandbox/bin/setup-lsp-plugins\s*$`))
			Expect(imagebuild.BakedSources).To(ContainElement("bin/setup-lsp-plugins"))
			// The base puts /opt/claude-sandbox/bin on PATH (CS-IMG-048), and the
			// session is told to run it by name.
			Expect(repoFile("container-context.md")).To(ContainSubstring("setup-lsp-plugins"))
		})

		// The script is bash + jq; run it against a scratch config dir, never a
		// real one.
		var (
			root, cfg, target, bindir string
			run                       func(args ...string) (string, int)
		)
		BeforeEach(func() {
			root = GinkgoT().TempDir()
			cfg = filepath.Join(root, "config")
			bindir = filepath.Join(root, "bin")
			Expect(os.MkdirAll(cfg, 0o755)).To(Succeed())
			Expect(os.MkdirAll(bindir, 0o755)).To(Succeed())
			// A hermetic PATH: only the tools the script uses, so a language
			// server installed on the test machine cannot leak in. A missing
			// tool fails the spec: the image ships all of them, and a skip
			// would hide the script's only behavioral coverage.
			bash, err := exec.LookPath("bash")
			Expect(err).NotTo(HaveOccurred(), "bash is required for the CS-IMG-050 script tests")
			for _, tool := range []string{"jq", "mktemp", "cat", "date", "mkdir", "rm", "readlink",
				"dirname", "basename", "chmod", "mv", "cp", "flock"} {
				p, err := exec.LookPath(tool)
				Expect(err).NotTo(HaveOccurred(), "%s is required for the CS-IMG-050 script tests", tool)
				Expect(os.Symlink(p, filepath.Join(bindir, tool))).To(Succeed())
			}
			// settings.json is a symlink into a dotfiles dir (CS-LNCH-069).
			target = filepath.Join(root, "dotfiles", "settings.json")
			Expect(os.MkdirAll(filepath.Dir(target), 0o755)).To(Succeed())
			Expect(os.WriteFile(target, []byte(`{"model":"opus"}`), 0o600)).To(Succeed())
			Expect(os.Symlink(target, filepath.Join(cfg, "settings.json"))).To(Succeed())
			// Only gopls is "installed".
			Expect(os.WriteFile(filepath.Join(bindir, "gopls"), []byte("#!/bin/sh\n"), 0o755)).To(Succeed())
			script, err := filepath.Abs(filepath.Join("..", "..", "bin", "setup-lsp-plugins"))
			Expect(err).NotTo(HaveOccurred())
			run = func(args ...string) (string, int) {
				cmd := exec.Command(bash, append([]string{script}, args...)...)
				cmd.Env = []string{
					"HOME=" + filepath.Join(root, "home"), // must not be used
					"CLAUDE_CONFIG_DIR=" + cfg,
					"PATH=" + bindir,
					"TMPDIR=" + root,
				}
				out, err := cmd.CombinedOutput()
				code := 0
				if ee, ok := err.(*exec.ExitError); ok {
					code = ee.ExitCode()
				} else {
					Expect(err).NotTo(HaveOccurred())
				}
				return string(out), code
			}
		})

		readJSON := func(path string) map[string]any {
			raw, err := os.ReadFile(path)
			Expect(err).NotTo(HaveOccurred())
			var m map[string]any
			Expect(json.Unmarshal(raw, &m)).To(Succeed())
			return m
		}
		inode := func(path string) uint64 {
			fi, err := os.Stat(path)
			Expect(err).NotTo(HaveOccurred())
			return uint64(fi.Sys().(*syscall.Stat_t).Ino)
		}
		// No temp file is left beside the files the script replaced.
		expectNoTempFiles := func(dirs ...string) {
			for _, d := range dirs {
				entries, err := os.ReadDir(d)
				Expect(err).NotTo(HaveOccurred())
				for _, e := range entries {
					Expect(e.Name()).NotTo(MatchRegexp(`^\.(settings|installed_plugins)\.json\.`), "temp file left in %s", d)
				}
			}
		}

		It("CS-IMG-050: registers and enables the plugins whose server is on PATH, in CLAUDE_CONFIG_DIR, through a settings symlink", func() {
			before := inode(target)
			out, code := run()
			Expect(code).To(Equal(0), out)

			fi, err := os.Lstat(filepath.Join(cfg, "settings.json"))
			Expect(err).NotTo(HaveOccurred())
			Expect(fi.Mode()&os.ModeSymlink).NotTo(BeZero(), "settings.json must stay a symlink")
			settings := readJSON(target)
			Expect(settings).To(HaveKeyWithValue("model", "opus"))
			Expect(settings["enabledPlugins"]).To(Equal(map[string]any{"gopls-lsp@claude-plugins-official": true}))
			tfi, err := os.Stat(target)
			Expect(err).NotTo(HaveOccurred())
			Expect(tfi.Mode().Perm()).To(Equal(os.FileMode(0o600)), "mode kept")
			Expect(inode(target)).NotTo(Equal(before), "the link's target is replaced by a rename, not truncated")
			Expect(filepath.Join(cfg, "settings.json.setup-lsp-plugins.bak")).NotTo(BeAnExistingFile())

			installed := readJSON(filepath.Join(cfg, "plugins", "installed_plugins.json"))
			Expect(installed["plugins"]).To(HaveKey("gopls-lsp@claude-plugins-official"))
			Expect(installed["plugins"]).NotTo(HaveKey("typescript-lsp@claude-plugins-official"))
			Expect(filepath.Join(root, "home")).NotTo(BeADirectory(), "HOME/.claude is not the config dir")
			expectNoTempFiles(cfg, filepath.Join(cfg, "plugins"), filepath.Dir(target), root)

			// Idempotent.
			out, code = run()
			Expect(code).To(Equal(0), out)
			Expect(out).To(ContainSubstring("Already registered: gopls-lsp@claude-plugins-official"))
		})

		It("CS-IMG-050: a plain settings.json is replaced atomically by a rename", func() {
			settingsPath := filepath.Join(cfg, "settings.json")
			Expect(os.Remove(settingsPath)).To(Succeed())
			Expect(os.WriteFile(settingsPath, []byte(`{"model":"opus"}`), 0o640)).To(Succeed())
			before := inode(settingsPath)

			out, code := run()
			Expect(code).To(Equal(0), out)

			fi, err := os.Lstat(settingsPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(fi.Mode().IsRegular()).To(BeTrue())
			Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o640)), "mode kept")
			Expect(inode(settingsPath)).NotTo(Equal(before), "renamed over, never truncated in place")
			settings := readJSON(settingsPath)
			Expect(settings).To(HaveKeyWithValue("model", "opus"))
			Expect(settings["enabledPlugins"]).To(Equal(map[string]any{"gopls-lsp@claude-plugins-official": true}))
			expectNoTempFiles(cfg, filepath.Join(cfg, "plugins"), root)
		})

		It("CS-IMG-050: when the rename cannot work it saves a backup once and rewrites in place", func() {
			if os.Geteuid() == 0 {
				Skip("root ignores the directory permissions this scenario relies on")
			}
			// All three servers: three writes to settings.json in one run.
			for _, b := range []string{"typescript-language-server", "pyright"} {
				Expect(os.WriteFile(filepath.Join(bindir, b), []byte("#!/bin/sh\n"), 0o755)).To(Succeed())
			}
			// An unwritable target directory stands in for the sandbox's
			// single-file bind mount: no temp file beside it, no rename onto it.
			dir := filepath.Dir(target)
			Expect(os.Chmod(dir, 0o555)).To(Succeed())
			DeferCleanup(os.Chmod, dir, os.FileMode(0o755))

			out, code := run()
			Expect(code).To(Equal(0), out)
			Expect(strings.Count(out, "previous contents saved as")).To(Equal(1), out)

			backup := filepath.Join(cfg, "settings.json.setup-lsp-plugins.bak")
			Expect(os.ReadFile(backup)).To(Equal([]byte(`{"model":"opus"}`)), "the pre-run contents, not a mid-run state")
			settings := readJSON(target)
			Expect(settings).To(HaveKeyWithValue("model", "opus"))
			Expect(settings["enabledPlugins"]).To(Equal(map[string]any{
				"gopls-lsp@claude-plugins-official":      true,
				"typescript-lsp@claude-plugins-official": true,
				"pyright-lsp@claude-plugins-official":    true,
			}))
			fi, err := os.Lstat(filepath.Join(cfg, "settings.json"))
			Expect(err).NotTo(HaveOccurred())
			Expect(fi.Mode() & os.ModeSymlink).NotTo(BeZero())
			expectNoTempFiles(cfg, root)
		})

		It("CS-IMG-050: a dangling settings.json symlink stops it before any write", func() {
			Expect(os.Remove(target)).To(Succeed())

			out, code := run()
			Expect(code).To(Equal(1), out)
			Expect(out).To(ContainSubstring("symlink to a missing file"))
			Expect(target).NotTo(BeAnExistingFile())
			entries, err := os.ReadDir(cfg)
			Expect(err).NotTo(HaveOccurred())
			Expect(entries).To(HaveLen(1), "only the dangling link: no plugins dir, no lock, nothing written")
		})

		It("CS-IMG-050: --check exits 0 only when all three are registered, enabled and on PATH", func() {
			_, code := run()
			Expect(code).To(Equal(0))
			out, code := run("--check")
			Expect(code).To(Equal(1), out)
			Expect(out).To(ContainSubstring("gopls-lsp: registered=true enabled=true binary=true"))

			for _, b := range []string{"typescript-language-server", "pyright"} {
				Expect(os.WriteFile(filepath.Join(bindir, b), []byte("#!/bin/sh\n"), 0o755)).To(Succeed())
			}
			_, code = run()
			Expect(code).To(Equal(0))
			out, code = run("--check")
			Expect(code).To(Equal(0), out)
		})
	})

	It("CS-IMG-037: the parser skips multi-stage COPYs and joins continuations", func() {
		df := "FROM x AS b\n# COPY commented/ out/\nCOPY --link --chmod=755 a.sh \\\n  b/ /dst/\nCOPY --from=b /out/bin /bin\nADD ./c.txt /c\n"
		Expect(contextSources(df)).To(Equal([]string{"a.sh", "b", "c.txt"}))
	})

	It("CS-IMG-039: the mode-keeping sources are exactly the final stage's COPYs without --chmod", func() {
		var keep []string
		for _, c := range contextCopies(repoFile("Dockerfile.tools")) {
			if c.finalStage && !c.chmod {
				keep = append(keep, c.path)
			}
		}
		Expect(keep).To(ContainElement("logstream"), "the parser found the final stage's COPY lines")
		Expect(imagebuild.ModeBakedSources).To(ConsistOf(keep),
			"a baked source's mode reaches the image exactly when the final stage COPYs it without --chmod; "+
				"update imagebuild.ModeBakedSources to match Dockerfile.tools")
	})

	It("CS-IMG-039: the parser tracks --chmod and the final stage", func() {
		df := "FROM x AS b\nCOPY a.go ./\nFROM y\nCOPY --link --chmod=755 e.sh /e\nCOPY --link l/ /l/\n"
		Expect(contextCopies(df)).To(Equal([]copySource{
			{path: "a.go"}, {path: "e.sh", chmod: true, finalStage: true}, {path: "l", finalStage: true},
		}))
	})

	It("CS-IMG-040: each baked source is exactly a COPY source path, the level BuildKit follows a symlink at", func() {
		seen := map[string]bool{}
		var srcs []string
		for _, s := range contextSources(repoFile("Dockerfile.tools")) {
			if !seen[s] {
				seen[s] = true
				srcs = append(srcs, s)
			}
		}
		Expect(imagebuild.BakedSources).To(ConsistOf(srcs))
	})
})
