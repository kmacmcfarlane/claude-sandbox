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
			root, cfg, target, bindir, tmpdir, bash string
			run                                     func(args ...string) (string, int)
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
			tmpdir = filepath.Join(root, "tmp") // the script's $TMPDIR, apart from every target
			Expect(os.MkdirAll(tmpdir, 0o755)).To(Succeed())
			var err error
			bash, err = exec.LookPath("bash")
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
					"TMPDIR=" + tmpdir,
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
		// No temp file is left beside the files the script replaced, nor in
		// its $TMPDIR.
		expectNoTempFiles := func(dirs ...string) {
			left, err := os.ReadDir(tmpdir)
			Expect(err).NotTo(HaveOccurred())
			Expect(left).To(BeEmpty(), "temp file left in $TMPDIR")
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

		It("CS-IMG-050: a temp file made outside the target's directory is never mv'd; the fallback rewrites in place", func() {
			// The target's directory stays WRITABLE, so a stray mv from
			// $TMPDIR would succeed (and, across devices, unlink the live file
			// first): only the script's own guard keeps it out.
			realMktemp, err := exec.LookPath("mktemp")
			Expect(err).NotTo(HaveOccurred())
			realMv, err := exec.LookPath("mv")
			Expect(err).NotTo(HaveOccurred())
			mvLog := filepath.Join(root, "mv.log")
			for name, body := range map[string]string{
				// Refuse the beside-the-target call (it passes a template).
				"mktemp": "if [ $# -gt 0 ]; then exit 1; fi\nexec " + realMktemp + "\n",
				"mv":     "printf '%s\\n' \"$*\" >> " + mvLog + "\nexec " + realMv + " \"$@\"\n",
			} {
				wrapper := filepath.Join(bindir, name)
				Expect(os.Remove(wrapper)).To(Succeed())
				Expect(os.WriteFile(wrapper, []byte("#!"+bash+"\n"+body), 0o755)).To(Succeed())
			}
			// All three servers: several writes to each file in one run.
			for _, b := range []string{"typescript-language-server", "pyright"} {
				Expect(os.WriteFile(filepath.Join(bindir, b), []byte("#!/bin/sh\n"), 0o755)).To(Succeed())
			}

			out, code := run()
			Expect(code).To(Equal(0), out)

			logged, err := os.ReadFile(mvLog)
			if err == nil {
				Expect(string(logged)).NotTo(ContainSubstring(tmpdir), "a temp file in $TMPDIR was mv'd")
			} else {
				Expect(os.IsNotExist(err)).To(BeTrue())
			}
			// installed_plugins.json is created in place by this run: nothing to back up.
			Expect(strings.Count(out, "previous contents saved as")).To(Equal(1), out)
			Expect(os.ReadFile(filepath.Join(cfg, "settings.json.setup-lsp-plugins.bak"))).To(Equal([]byte(`{"model":"opus"}`)))
			Expect(filepath.Join(cfg, "installed_plugins.json.setup-lsp-plugins.bak")).NotTo(BeAnExistingFile())

			ids := []string{"gopls-lsp@claude-plugins-official", "typescript-lsp@claude-plugins-official", "pyright-lsp@claude-plugins-official"}
			settings := readJSON(target)
			Expect(settings).To(HaveKeyWithValue("model", "opus"))
			installed := readJSON(filepath.Join(cfg, "plugins", "installed_plugins.json"))
			for _, id := range ids {
				Expect(settings["enabledPlugins"]).To(HaveKeyWithValue(id, true))
				Expect(installed["plugins"]).To(HaveKey(id))
			}
			fi, err := os.Lstat(filepath.Join(cfg, "settings.json"))
			Expect(err).NotTo(HaveOccurred())
			Expect(fi.Mode() & os.ModeSymlink).NotTo(BeZero())
			expectNoTempFiles(cfg, filepath.Join(cfg, "plugins"), filepath.Dir(target))
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

	It("CS-IMG-051: the tools image ships notify-webhook, the body of the Notification hook", func() {
		df := repoFile("Dockerfile.tools")
		Expect(df).To(MatchRegexp(`(?m)^COPY --link --chmod=755 bin/notify-webhook /opt/claude-sandbox/bin/notify-webhook\s*$`))
		Expect(imagebuild.BakedSources).To(ContainElement("bin/notify-webhook"))
		// --chmod=755 on the COPY, so the checkout's umask never reaches the
		// image and the file stays out of ModeBakedSources (CS-IMG-039).
		Expect(imagebuild.ModeBakedSources).NotTo(ContainElement("bin/notify-webhook"))
		// The drop-in calls the script by that path and still swallows failure.
		Expect(repoFile("notification-hooks.json")).To(
			ContainSubstring(`"command": "/opt/claude-sandbox/bin/notify-webhook || true"`))
	})

	It("CS-IMG-052: the entrypoint hands the base venv's directories to the session user", func() {
		const venv = "/opt/claude-sandbox/venv"
		Expect(repoFile("Dockerfile")).To(MatchRegexp(`(?m)^ENV VIRTUAL_ENV=`+venv+`\s*$`),
			"the entrypoint's fixed path must be where the base builds the venv")

		ep := repoFile("entrypoint.sh")
		Expect(ep).To(ContainSubstring("\nVENV_DIR=" + venv + "\n"))
		start := strings.Index(ep, `if [ -d "$VENV_DIR" ]`)
		Expect(start).To(BeNumerically(">=", 0), "the venv block is present")
		block := ep[start:]
		block = block[:strings.Index(block, "\nfi\n")]
		Expect(block).To(ContainSubstring(`[ ! -L "$VENV_DIR" ]`), "a symlinked venv is skipped")
		// Bind mounts never change owner (the home chown's rule): the venv must
		// be on the root filesystem and not itself a mount point...
		Expect(block).To(ContainSubstring(`[ "$(stat -c %d "$VENV_DIR")" = "$(stat -c %d /)" ]`),
			"a venv on another device (a mount of the venv, /opt or /opt/claude-sandbox) is skipped")
		Expect(block).To(ContainSubstring(`! mountpoint -q "$VENV_DIR"`), "a mounted venv is skipped")
		// ...and every mount point below it is pruned, read from mountinfo.
		Expect(block).To(ContainSubstring(`_cs_prune_args "$VENV_DIR"`))
		Expect(block).To(ContainSubstring(`find "$VENV_DIR" -xdev "${_CS_PRUNE[@]}" -type d`))

		// Mount points are read once from mountinfo and DECODED (\040 = space,
		// \134 = backslash), and every consumer uses the decoded list.
		Expect(ep).To(ContainSubstring("while IFS= read -r _mp; do\n    printf -v _mp '%b' \"${_mp//\\\\/\\\\0}\"\n    _CS_MOUNT_POINTS+=(\"$_mp\")\ndone < <(awk '{print $5}' /proc/self/mountinfo)\n"))
		Expect(strings.Count(ep, "/proc/self/mountinfo)")).To(Equal(1), "no consumer reads mountinfo raw")
		Expect(ep).To(ContainSubstring(`for _mp in "${_CS_MOUNT_POINTS[@]}"; do`), "the home relocation's mount check")
		// The shared prune helper escapes find -path's glob characters,
		// backslash first, and matches the directory literally.
		helper := ep[strings.Index(ep, "_cs_prune_args() {"):]
		helper = helper[:strings.Index(helper, "\n}\n")]
		Expect(helper).To(ContainSubstring(`[[ "$mp" == "$1"/* ]] || continue`))
		Expect(helper).To(ContainSubstring(`mp=${mp//\\/\\\\}; mp=${mp//\*/\\*}; mp=${mp//\?/\\?}; mp=${mp//\[/\\[}`))
		Expect(helper).To(ContainSubstring(`_CS_PRUNE+=(-path "$mp" -prune -o)`))
		// The home chown uses the same helper.
		Expect(ep).To(ContainSubstring("_cs_prune_args \"$TARGET_HOME\"\nfind \"$TARGET_HOME\" \"${_CS_PRUNE[@]}\" \\( ! -uid \"$TARGET_UID\" -o ! -gid \"$TARGET_GID\" \\) -print0"))
		Expect(block).To(ContainSubstring(`-exec chown "$TARGET_UID:$TARGET_GID" {} +`))
		Expect(block).NotTo(ContainSubstring("VIRTUAL_ENV"), "never a path an env file can set")
		Expect(block).NotTo(ContainSubstring("-type f"), "files keep their owner (no overlay2 copy-up)")
		Expect(block).NotTo(MatchRegexp(`chown -R|find -[HL]|-follow`))

		// After the uid/gid remap, before the hand-off.
		Expect(start).To(BeNumerically(">", strings.Index(ep, `usermod -o -u "$TARGET_UID"`)))
		Expect(start).To(BeNumerically("<", strings.Index(ep, "exec /usr/sbin/gosu")))

		Expect(repoFile("container-context.md")).To(ContainSubstring("die with the container"))
	})

	It("CS-IMG-052: the entrypoint's mountinfo decode is unambiguous before a digit", func() {
		ep := repoFile("entrypoint.sh")
		var decode string
		for _, l := range strings.Split(ep, "\n") {
			if strings.Contains(l, "printf -v _mp '%b'") {
				decode = strings.TrimSpace(l)
			}
		}
		Expect(decode).NotTo(BeEmpty())
		// Raw mountinfo field -> the path it names.
		cases := map[string]string{
			`a\0401`:       "a 1",
			`x\134t\0402x`: `x\t 2x`,
			`tab\0119`:     "tab\t9",
			`nl\0127`:      "nl\n7",
			`b\134s`:       `b\s`,
			`sp\040ace`:    "sp ace",
			`/opt/br[1]`:   "/opt/br[1]",
		}
		for raw, want := range cases {
			out, err := exec.Command("bash", "-c", "_mp=$1; "+decode+"; printf '%s' \"$_mp\"", "_", raw).Output()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(out)).To(Equal(want), raw)
		}
	})

	Describe("CS-IMG-067: the entrypoint's root part trusts nothing from the container environment", func() {
		// Lines of the script that run something: not blank, not a comment.
		commands := func(ep string) []string {
			var out []string
			for _, l := range strings.Split(ep, "\n") {
				t := strings.TrimSpace(l)
				if t == "" || strings.HasPrefix(t, "#") {
					continue
				}
				out = append(out, t)
			}
			return out
		}

		It("fixes the environment before any command and restores PATH only for the hand-off", func() {
			ep := repoFile("entrypoint.sh")
			Expect(strings.HasPrefix(ep, "#!/bin/bash -p\n")).To(BeTrue(),
				"privileged mode: no BASH_ENV/ENV, no imported functions, SHELLOPTS, BASHOPTS, CDPATH, GLOBIGNORE")
			cmds := commands(ep)
			// The very first things executed, in order: save, fix, drop.
			Expect(cmds[0]).To(Equal(`_CS_SESSION_PATH="$PATH"`))
			Expect(cmds[1]).To(Equal("export PATH=/usr/sbin:/usr/bin:/sbin:/bin"), "root-only-writable dirs; /usr/local/* left out")
			Expect(cmds[2]).To(Equal("unset LD_PRELOAD LD_LIBRARY_PATH LD_AUDIT"))
			// Restored right before the exec, and nothing in between; the exec
			// names gosu and the binary absolutely (CS-PID-007), so no PATH
			// lookup happens under the restored PATH.
			Expect(cmds[len(cmds)-2]).To(Equal(`PATH="$_CS_SESSION_PATH"`))
			Expect(cmds[len(cmds)-1]).To(Equal(`exec /usr/sbin/gosu "$TARGET_USER" /opt/claude-sandbox/bin/claude-sandbox pidslot -- "$@"`))
			Expect(strings.Count(ep, "_CS_SESSION_PATH")).To(Equal(2), "saved once, used once")
			Expect(ep).NotTo(MatchRegexp(`(?m)^\s*(export )?LD_(PRELOAD|LIBRARY_PATH|AUDIT)=`), "the LD_* three are never restored")
			Expect(ep).NotTo(ContainSubstring("exec gosu"), "bare gosu would be looked up on the restored PATH")
		})

		It("the prologue resolves the root part's tools under a poisoned PATH (runnable)", func() {
			ep := repoFile("entrypoint.sh")
			cmds := commands(ep)
			prologue := strings.Join(cmds[:3], "\n")
			poison := GinkgoT().TempDir()
			for _, tool := range []string{"awk", "id", "find", "chown"} {
				Expect(os.WriteFile(filepath.Join(poison, tool), []byte("#!/bin/sh\necho POISON\n"), 0o755)).To(Succeed())
			}
			// The image order: user-writable dirs first (CS-IMG-052).
			cmd := exec.Command("bash", "-c", prologue+"\nprintf '%s\\n' \"$PATH\" \"$_CS_SESSION_PATH\" \"$(command -v awk)\" \"$(command -v id)\" \"${LD_PRELOAD-unset}\" \"${LD_LIBRARY_PATH-unset}\"")
			cmd.Env = []string{"PATH=" + poison + ":/usr/sbin:/usr/bin:/sbin:/bin", "LD_PRELOAD=" + poison + "/x.so", "LD_LIBRARY_PATH=" + poison}
			out, err := cmd.Output()
			Expect(err).NotTo(HaveOccurred())
			lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
			Expect(lines).To(HaveLen(6))
			Expect(lines[0]).To(Equal("/usr/sbin:/usr/bin:/sbin:/bin"))
			Expect(lines[1]).To(Equal(poison+":/usr/sbin:/usr/bin:/sbin:/bin"), "the session PATH is kept aside verbatim")
			Expect(lines[2]).To(HavePrefix("/usr/bin/"), "awk is the distribution's, not the planted one")
			Expect(lines[3]).To(HavePrefix("/usr/bin/"))
			Expect(lines[4]).To(Equal("unset"))
			Expect(lines[5]).To(Equal("unset"))
		})

		It("bash -p ignores BASH_ENV (the shebang's guarantee, runnable)", func() {
			dir := GinkgoT().TempDir()
			env := filepath.Join(dir, "evil.sh")
			Expect(os.WriteFile(env, []byte("CS_EVIL=ran\n"), 0o644)).To(Succeed())
			run := func(args ...string) string {
				cmd := exec.Command("bash", append(args, "-c", `printf '%s' "${CS_EVIL-unset}"`)...)
				cmd.Env = []string{"PATH=/usr/bin:/bin", "BASH_ENV=" + env}
				out, err := cmd.Output()
				Expect(err).NotTo(HaveOccurred())
				return string(out)
			}
			Expect(run()).To(Equal("ran"), "without -p a non-interactive bash sources BASH_ENV first")
			Expect(run("-p")).To(Equal("unset"))
		})
	})

	It("CS-IMG-068: the entrypoint is idempotent on a restart of the same container", func() {
		ep := repoFile("entrypoint.sh")
		// The user is resolved by name, claude first, then the already-renamed
		// host user; every later step keys on it.
		Expect(ep).To(ContainSubstring("if getent passwd claude >/dev/null; then\n    _CS_USER=claude\nelif getent passwd \"$TARGET_USER\" >/dev/null; then\n    _CS_USER=\"$TARGET_USER\"\nelse\n"))
		Expect(ep).To(ContainSubstring(`_CS_GROUP="$(id -gn "$_CS_USER")"`))
		Expect(ep).NotTo(MatchRegexp(`id -[ug] claude\b`), "never id on the build-time name: it is gone after the rename")
		Expect(ep).NotTo(MatchRegexp(`(?m)^\s*(usermod|groupmod) .* claude\s*(2>|\|\||$)`), "usermod/groupmod act on the resolved user/group")
		Expect(ep).To(ContainSubstring(`if [ "$(id -u "$_CS_USER")" != "$TARGET_UID" ] || [ "$(id -g "$_CS_USER")" != "$TARGET_GID" ]; then`))
		Expect(ep).To(ContainSubstring(`if [ "$_CS_USER" != "$TARGET_USER" ]; then` + "\n" +
			`    usermod -l "$TARGET_USER" -d "$TARGET_HOME" "$_CS_USER"`))
		// The relocation runs only while /home/claude is a real directory.
		Expect(ep).To(ContainSubstring(`&& [ -d /home/claude ] && [ ! -L /home/claude ]; then`))
		Expect(ep).To(ContainSubstring("    rm -rf /home/claude\n    ln -s \"$TARGET_HOME\" /home/claude\n"))
		// The home chown skips what is already owned and runs no empty chown.
		Expect(ep).To(ContainSubstring(`find "$TARGET_HOME" "${_CS_PRUNE[@]}" \( ! -uid "$TARGET_UID" -o ! -gid "$TARGET_GID" \) -print0 \` + "\n" +
			`    | xargs -0 --no-run-if-empty chown "$TARGET_UID:$TARGET_GID"`))
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
