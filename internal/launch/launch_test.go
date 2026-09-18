package launch_test

// Spec: spec/launch.feature (CS-LNCH). Build() assembles the docker run plan
// from injectable inputs: Home points at a temp dir, Getenv reads a map, and
// shadow files land in Inputs.TempDir where their content is asserted.
//
// Not covered here (cmd/claude-sandbox CLI tests): CS-LNCH-001..006, 024, 025,
// 030 — argument scanning, cascade report, env-cascade warning, --version.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	assets "github.com/kmacmcfarlane/claude-sandbox"
	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/imagebuild"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
)

// argPairs collects the values following each occurrence of flag in argv.
func argPairs(args []string, flag string) []string {
	var out []string
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

// touch creates the file (and parents) with the given content.
func touch(p, content string) {
	Expect(os.MkdirAll(filepath.Dir(p), 0o755)).To(Succeed())
	Expect(os.WriteFile(p, []byte(content), 0o644)).To(Succeed())
}

func mkdir(p string) {
	Expect(os.MkdirAll(p, 0o755)).To(Succeed())
}

var _ = Describe("launch.Build", func() {
	var (
		home, proj string
		env        map[string]string
		in         launch.Inputs
		out, errw  *bytes.Buffer
	)

	BeforeEach(func() {
		base, err := filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		home = filepath.Join(base, "home")
		proj = filepath.Join(base, "proj")
		mkdir(home)
		mkdir(proj)
		env = map[string]string{}
		out = &bytes.Buffer{}
		errw = &bytes.Buffer{}
		in = launch.Inputs{
			ProjectDir: proj,
			Home:       home,
			HostUID:    1000,
			HostGID:    1000,
			HostUser:   "tester",
			Getenv:     func(k string) string { return env[k] },
			TempDir:    filepath.Join(base, "shadow"),
			ImageName:  "claude-sandbox-proj",
			Out:        out,
			Err:        errw,
		}
		mkdir(in.TempDir)
	})

	build := func() *launch.Plan {
		p, err := launch.Build(in)
		Expect(err).NotTo(HaveOccurred())
		return p
	}

	// ---- core mounts ----

	It("CS-LNCH-007: mounts the project at its real host path and uses it as workdir", func() {
		p := build()
		Expect(p.Volumes).To(ContainElement(proj + ":" + proj))
		args := p.CreateArgs(proj)
		Expect(args).To(ContainElements("-w", proj))
	})

	It("CS-LNCH-008: mounts the Claude config dir at its real path when present", func() {
		cfgDir := filepath.Join(home, ".claude")
		mkdir(cfgDir)
		p := build()
		Expect(p.Volumes).To(ContainElement(cfgDir + ":" + cfgDir))
	})

	It("CS-LNCH-008: honors CLAUDE_CONFIG_DIR on both sides of the mount and forwards it as -e", func() {
		alt := filepath.Join(home, "alt-cfg")
		mkdir(alt)
		env["CLAUDE_CONFIG_DIR"] = alt
		p := build()
		Expect(p.Volumes).To(ContainElement(alt + ":" + alt))
		Expect(p.EnvFlags).To(ContainElement("CLAUDE_CONFIG_DIR=" + alt))
		// The default location is not mounted.
		def := filepath.Join(home, ".claude")
		Expect(p.Volumes).NotTo(ContainElement(def + ":" + def))
	})

	It("CS-LNCH-009: mounts direnv allow-records read-only when present", func() {
		direnv := filepath.Join(home, ".local/share/direnv")
		mkdir(direnv)
		p := build()
		Expect(p.Volumes).To(ContainElement(direnv + ":" + direnv + ":ro"))
	})

	It("CS-LNCH-009: adds no direnv mount when the directory is absent", func() {
		p := build()
		direnv := filepath.Join(home, ".local/share/direnv")
		Expect(p.Volumes).NotTo(ContainElement(direnv + ":" + direnv + ":ro"))
	})

	// ---- shadow injections ----

	It("CS-LNCH-010: shadows CLAUDE.md with host memory + blank line + container context", func() {
		cfgDir := filepath.Join(home, ".claude")
		touch(filepath.Join(cfgDir, "CLAUDE.md"), "# my host memory\n")
		p := build()
		tmp := filepath.Join(in.TempDir, "CLAUDE.md")
		raw, err := os.ReadFile(tmp)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(raw)).To(Equal("# my host memory\n\n" + string(assets.ContainerContext)))
		Expect(p.Volumes).To(ContainElement(tmp + ":" + filepath.Join(cfgDir, "CLAUDE.md") + ":ro"))
	})

	It("CS-LNCH-010: shadows CLAUDE.md with container-context.md alone when no host file exists", func() {
		build()
		raw, err := os.ReadFile(filepath.Join(in.TempDir, "CLAUDE.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(raw).To(Equal(assets.ContainerContext))
	})

	It("CS-LNCH-011: shadows settings.json with the notification-hooks fragment merged over the host file", func() {
		cfgDir := filepath.Join(home, ".claude")
		touch(filepath.Join(cfgDir, "settings.json"), `{"theme":"dark","hooks":{"Old":[]}}`)
		p := build()
		tmp := filepath.Join(in.TempDir, "settings.json")
		raw, err := os.ReadFile(tmp)
		Expect(err).NotTo(HaveOccurred())
		var merged, fragment map[string]any
		Expect(json.Unmarshal(raw, &merged)).To(Succeed())
		Expect(json.Unmarshal(assets.NotificationHooks, &fragment)).To(Succeed())
		Expect(merged["theme"]).To(Equal("dark"))
		// Top-level keys from the fragment win wholesale.
		Expect(reflect.DeepEqual(merged["hooks"], fragment["hooks"])).To(BeTrue())
		// Read-write path shadow: no :ro suffix.
		Expect(p.Volumes).To(ContainElement(tmp + ":" + filepath.Join(cfgDir, "settings.json")))
	})

	It("CS-LNCH-011: shadows settings.json with the fragment alone when no host file exists", func() {
		build()
		raw, err := os.ReadFile(filepath.Join(in.TempDir, "settings.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(raw).To(Equal(assets.NotificationHooks))
	})

	It("CS-LNCH-012: mounts the .claude.json sibling read-write when present", func() {
		cj := filepath.Join(home, ".claude.json")
		touch(cj, "{}")
		p := build()
		Expect(p.Volumes).To(ContainElement(cj + ":" + cj))
	})

	It("CS-LNCH-012: adds no .claude.json mount when absent", func() {
		p := build()
		cj := filepath.Join(home, ".claude.json")
		Expect(p.Volumes).NotTo(ContainElement(cj + ":" + cj))
	})

	It("CS-LNCH-013: shadows .mcp.json with mcpServers key-merged, fragment servers winning", func() {
		hostMCP := filepath.Join(home, ".mcp.json")
		touch(hostMCP, `{"other":true,"mcpServers":{"mine":{"type":"stdio"},"discord":{"stale":true}}}`)
		p := build()
		tmp := filepath.Join(in.TempDir, ".mcp.json")
		raw, err := os.ReadFile(tmp)
		Expect(err).NotTo(HaveOccurred())
		var merged, fragment map[string]any
		Expect(json.Unmarshal(raw, &merged)).To(Succeed())
		Expect(json.Unmarshal(assets.MCPServers, &fragment)).To(Succeed())
		Expect(merged["other"]).To(Equal(true))
		servers := merged["mcpServers"].(map[string]any)
		Expect(servers).To(HaveKey("mine"))
		// Fragment wins the collision: the stale host "discord" entry is gone.
		fragServers := fragment["mcpServers"].(map[string]any)
		Expect(reflect.DeepEqual(servers["discord"], fragServers["discord"])).To(BeTrue())
		Expect(p.Volumes).To(ContainElement(tmp + ":" + hostMCP + ":ro"))
	})

	It("CS-LNCH-013: mounts the fragment alone read-only when no host .mcp.json exists", func() {
		// The third spec clause ("only the host file exists") is unreachable in
		// the Go implementation: the fragment is compiled in via go:embed.
		p := build()
		tmp := filepath.Join(in.TempDir, ".mcp.json")
		raw, err := os.ReadFile(tmp)
		Expect(err).NotTo(HaveOccurred())
		Expect(raw).To(Equal(assets.MCPServers))
		Expect(p.Volumes).To(ContainElement(tmp + ":" + filepath.Join(home, ".mcp.json") + ":ro"))
	})

	// ---- host access precedence ----

	Describe("CS-LNCH-014: host access precedence CLI > env var > YAML", func() {
		boolp := func(b bool) *bool { return &b }

		// hasAccess reports whether the plan shows the key's characteristic mount.
		hasAccess := func(p *launch.Plan, key string) bool {
			var probe string
			switch key {
			case "ssh":
				d := filepath.Join(home, ".ssh")
				probe = d + ":" + d + ":ro"
			case "git":
				probe = filepath.Join(in.TempDir, "gitconfig") + ":" + filepath.Join(home, ".gitconfig") + ":ro"
			case "dockerSocket":
				probe = "/var/run/docker.sock:/var/run/docker.sock"
			case "aws":
				d := filepath.Join(home, ".aws")
				probe = d + ":" + d + ":ro"
			case "packageCaches":
				d := filepath.Join(home, ".cache", "claude-sandbox", "go-mod")
				probe = d + ":" + d
			}
			for _, v := range p.Volumes {
				if v == probe {
					return true
				}
			}
			return false
		}

		DescribeTable("resolution",
			func(key, envvar, envVal string, yaml *bool, cli *bool, want bool) {
				// Every host resource exists so absence of a mount means the
				// toggle resolved false, not a missing directory.
				mkdir(filepath.Join(home, ".ssh"))
				mkdir(filepath.Join(home, ".aws"))
				touch(filepath.Join(home, ".gitconfig"), "[user]\n")
				if envVal != "" {
					env[envvar] = envVal
				}
				cfg := &cascade.Config{}
				entry := cascade.HostAccessEntry{Enabled: yaml}
				switch key {
				case "ssh":
					cfg.HostAccess.SSH = entry
					in.CLISSH = cli
				case "git":
					cfg.HostAccess.Git = entry
					in.CLIGit = cli
				case "dockerSocket":
					cfg.HostAccess.DockerSocket = entry
					in.CLIDockerSocket = cli
				case "aws":
					cfg.HostAccess.AWS = entry
					in.CLIAWS = cli
				case "packageCaches":
					cfg.HostAccess.PackageCaches = entry
					in.CLIPackageCaches = cli
				}
				in.Cfg = cfg
				Expect(hasAccess(build(), key)).To(Equal(want))
			},
			Entry("CS-LNCH-014: ssh: yaml false, env 1, cli absent -> true",
				"ssh", "CLAUDE_SANDBOX_HOST_ACCESS_SSH_ENABLED", "1", boolp(false), nil, true),
			Entry("CS-LNCH-014: git: yaml true, env unset, cli absent -> true",
				"git", "CLAUDE_SANDBOX_HOST_ACCESS_GIT_ENABLED", "", boolp(true), nil, true),
			Entry("CS-LNCH-014: dockerSocket: yaml false, env unset, cli set -> true",
				"dockerSocket", "CLAUDE_SANDBOX_HOST_ACCESS_DOCKER_SOCKET_ENABLED", "", boolp(false), boolp(true), true),
			Entry("CS-LNCH-014: aws: yaml false, env unset, cli absent -> false",
				"aws", "CLAUDE_SANDBOX_HOST_ACCESS_AWS_ENABLED", "", boolp(false), nil, false),
			Entry("CS-LNCH-014: packageCaches: yaml false, env unset, cli set -> true",
				"packageCaches", "CLAUDE_SANDBOX_HOST_ACCESS_PACKAGE_CACHES_ENABLED", "", boolp(false), boolp(true), true),
			Entry("CS-LNCH-014: packageCaches: yaml true, env unset, cli absent -> true",
				"packageCaches", "CLAUDE_SANDBOX_HOST_ACCESS_PACKAGE_CACHES_ENABLED", "", boolp(true), nil, true),
			Entry("CS-LNCH-014: packageCaches: nothing set -> false (opt-in)",
				"packageCaches", "CLAUDE_SANDBOX_HOST_ACCESS_PACKAGE_CACHES_ENABLED", "", nil, nil, false),
			// Env var truthy forms: "1", "true", "yes".
			Entry("CS-LNCH-014: ssh: env 'true' is truthy",
				"ssh", "CLAUDE_SANDBOX_HOST_ACCESS_SSH_ENABLED", "true", nil, nil, true),
			Entry("CS-LNCH-014: ssh: env 'yes' is truthy",
				"ssh", "CLAUDE_SANDBOX_HOST_ACCESS_SSH_ENABLED", "yes", nil, nil, true),
			Entry("CS-LNCH-014: ssh: env '0' is not truthy",
				"ssh", "CLAUDE_SANDBOX_HOST_ACCESS_SSH_ENABLED", "0", nil, nil, false),
		)
	})

	// ---- host access mounts ----

	It("CS-LNCH-015: mounts the docker socket and sets DOCKER_GID from the socket group", func() {
		t := true
		in.CLIDockerSocket = &t
		p := build()
		Expect(p.Volumes).To(ContainElement("/var/run/docker.sock:/var/run/docker.sock"))
		// DOCKER_GID matches the real socket's gid, or is empty when the
		// socket is unavailable on this machine.
		want := ""
		if fi, err := os.Stat("/var/run/docker.sock"); err == nil {
			if st, ok := fi.Sys().(*syscall.Stat_t); ok {
				want = fmt.Sprintf("%d", st.Gid)
			}
		}
		Expect(p.EnvFlags).To(ContainElement("DOCKER_GID=" + want))
	})

	It("CS-LNCH-016: mounts ~/.ssh read-only when enabled and present; absent directory adds nothing", func() {
		t := true
		in.CLISSH = &t
		sshDir := filepath.Join(home, ".ssh")

		p := build()
		Expect(p.Volumes).NotTo(ContainElement(sshDir + ":" + sshDir + ":ro"))

		mkdir(sshDir)
		p = build()
		Expect(p.Volumes).To(ContainElement(sshDir + ":" + sshDir + ":ro"))
	})

	It("CS-LNCH-017: mounts a read-only temp COPY of ~/.gitconfig, never the host file", func() {
		t := true
		in.CLIGit = &t
		src := filepath.Join(home, ".gitconfig")
		touch(src, "[user]\n\tname = Tester\n")
		p := build()
		tmp := filepath.Join(in.TempDir, "gitconfig")
		raw, err := os.ReadFile(tmp)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(raw)).To(Equal("[user]\n\tname = Tester\n"))
		Expect(p.Volumes).To(ContainElement(tmp + ":" + src + ":ro"))
		// The host file itself is never a mount source.
		Expect(p.Volumes).NotTo(ContainElement(src + ":" + src + ":ro"))
	})

	It("CS-LNCH-017: mounts nothing when git access is enabled but no ~/.gitconfig exists", func() {
		t := true
		in.CLIGit = &t
		p := build()
		for _, v := range p.Volumes {
			Expect(v).NotTo(ContainSubstring(".gitconfig"))
		}
	})

	It("CS-LNCH-018: mounts ~/.aws read-only and forwards set allowlist variables with -e", func() {
		t := true
		in.CLIAWS = &t
		awsDir := filepath.Join(home, ".aws")
		mkdir(awsDir)
		env["AWS_PROFILE"] = "dev"
		env["AWS_REGION"] = "us-west-2"
		env["AWS_ACCESS_KEY_ID"] = "AKIA123"
		p := build()
		Expect(p.Volumes).To(ContainElement(awsDir + ":" + awsDir + ":ro"))
		Expect(p.EnvFlags).To(ContainElements(
			"AWS_PROFILE=dev", "AWS_REGION=us-west-2", "AWS_ACCESS_KEY_ID=AKIA123"))
		// Unset allowlist vars are not forwarded.
		for _, e := range p.EnvFlags {
			Expect(e).NotTo(HavePrefix("AWS_SESSION_TOKEN="))
		}
	})

	// ---- package caches ----

	Describe("package caches (CS-LNCH-035..037)", func() {
		var root string
		BeforeEach(func() {
			t := true
			in.CLIPackageCaches = &t
			root = filepath.Join(home, ".cache", "claude-sandbox")
		})

		It("CS-LNCH-035: mounts the four cache dirs writable at the same path and points the toolchains at them", func() {
			p := build()
			for _, name := range []string{"go-mod", "go-build", "npm", "pip"} {
				d := filepath.Join(root, name)
				Expect(p.Volumes).To(ContainElement(d+":"+d), name+" must be writable (no :ro)")
			}
			Expect(p.EnvFlags).To(ContainElements(
				"GOMODCACHE="+filepath.Join(root, "go-mod"),
				"GOCACHE="+filepath.Join(root, "go-build"),
				"npm_config_cache="+filepath.Join(root, "npm"),
				"PIP_CACHE_DIR="+filepath.Join(root, "pip"),
			))
		})

		It("CS-LNCH-035: the fingerprint records the lever", func() {
			with := build().ConfigHash
			in.CLIPackageCaches = nil
			Expect(build().ConfigHash).NotTo(Equal(with))
		})

		It("CS-LNCH-036: creates the host directories before docker run", func() {
			Expect(filepath.Join(root, "go-mod")).NotTo(BeADirectory())
			build()
			for _, name := range []string{"go-mod", "go-build", "npm", "pip"} {
				Expect(filepath.Join(root, name)).To(BeADirectory())
			}
		})

		It("CS-LNCH-037: mounts nothing from the host's own caches", func() {
			mkdir(filepath.Join(home, "go", "pkg", "mod"))
			mkdir(filepath.Join(home, ".npm"))
			mkdir(filepath.Join(home, ".cache", "pip"))
			p := build()
			for _, v := range p.Volumes {
				host := strings.SplitN(v, ":", 2)[0]
				if strings.HasPrefix(host, root) {
					continue
				}
				Expect(host).NotTo(HavePrefix(filepath.Join(home, "go")))
				Expect(host).NotTo(HavePrefix(filepath.Join(home, ".npm")))
				Expect(host).NotTo(HavePrefix(filepath.Join(home, ".cache", "pip")))
			}
			Expect(launch.PackageCacheRoot).To(Equal(".cache/claude-sandbox"))
		})

		It("CS-LNCH-037: adds nothing when the lever is off", func() {
			in.CLIPackageCaches = nil
			p := build()
			for _, v := range p.Volumes {
				Expect(v).NotTo(ContainSubstring(".cache/claude-sandbox"))
			}
			for _, e := range p.EnvFlags {
				Expect(e).NotTo(HavePrefix("GOMODCACHE="))
			}
		})
	})

	Describe("shared peer registry (CS-LNCH-049..055)", func() {
		var root, cfgDir string
		BeforeEach(func() {
			root = filepath.Join(home, ".cache", "claude-sandbox", "peers")
			cfgDir = filepath.Join(home, ".claude")
			mkdir(cfgDir)
		})
		enable := func() {
			t := true
			in.Cfg = &cascade.Config{SharedPeerRegistry: &t}
		}
		// envValues returns every value docker run receives for key via -e.
		envValues := func(p *launch.Plan, key string) []string {
			var vals []string
			for _, e := range argPairs(p.CreateArgs(proj), "-e") {
				if k, v, ok := strings.Cut(e, "="); ok && k == key {
					vals = append(vals, v)
				}
			}
			return vals
		}
		// peerEntries is every -v / -e entry the bridge contributes.
		peerEntries := func(p *launch.Plan) []string {
			var got []string
			args := p.CreateArgs(proj)
			for _, v := range argPairs(args, "-v") {
				if strings.Contains(v, filepath.Join("claude-sandbox", "peers")) {
					got = append(got, "-v "+v)
				}
			}
			for _, e := range argPairs(args, "-e") {
				if strings.HasPrefix(e, "XDG_RUNTIME_DIR=") {
					got = append(got, "-e "+e)
				}
			}
			return got
		}
		rootMount := func() string { return root + ":" + root }

		It("CS-LNCH-049: adds nothing when the key is unset, and an explicit false is byte-identical", func() {
			before := build().CreateArgs(proj)
			f := false
			in.Cfg = &cascade.Config{SharedPeerRegistry: &f}
			Expect(build().CreateArgs(proj)).To(Equal(before))
			for _, v := range before {
				Expect(v).NotTo(ContainSubstring("claude-sandbox/peers"))
				Expect(v).NotTo(ContainSubstring("XDG_RUNTIME_DIR"))
			}
			Expect(filepath.Join(home, ".cache", "claude-sandbox")).NotTo(BeADirectory())
		})

		It("CS-LNCH-049: with the key on, the argv is the key-off argv plus exactly the bridge entries", func() {
			off := build().CreateArgs(proj)
			enable()
			on := build().CreateArgs(proj)
			// Remove the three bridge pairs; what remains must be the off argv.
			var rest []string
			for i := 0; i < len(on); i++ {
				if i+1 < len(on) && (on[i] == "-v" || on[i] == "-e") &&
					(strings.Contains(on[i+1], filepath.Join("claude-sandbox", "peers")) ||
						strings.HasPrefix(on[i+1], "XDG_RUNTIME_DIR=")) {
					i++
					continue
				}
				rest = append(rest, on[i])
			}
			// The fingerprint label moves with the key by design (CS-LNCH-052).
			mask := func(args []string) []string {
				out := make([]string, len(args))
				for i, a := range args {
					if strings.HasPrefix(a, "claude-sandbox.confighash=") {
						a = "claude-sandbox.confighash=<masked>"
					}
					out[i] = a
				}
				return out
			}
			Expect(mask(rest)).To(Equal(mask(off)))
		})

		It("CS-LNCH-050: mounts the registry, the same-path peers root and sets XDG_RUNTIME_DIR to it", func() {
			enable()
			p := build()
			Expect(p.Volumes).To(ContainElement(
				filepath.Join(root, "sessions") + ":" + filepath.Join(cfgDir, "sessions")))
			Expect(p.Volumes).To(ContainElement(rootMount()))
			Expect(envValues(p, "XDG_RUNTIME_DIR")).To(Equal([]string{root}))
			// Every session bind()s its socket and writes its record: neither
			// mount may be :ro (bind() under a read-only bind is EROFS).
			for _, v := range p.Volumes {
				if strings.Contains(v, filepath.Join("claude-sandbox", "peers")) {
					Expect(v).NotTo(HaveSuffix(":ro"))
				}
			}
		})

		It("CS-LNCH-050: mounts nothing over a cc-socks under the config dir or CLAUDE_CODE_TMPDIR", func() {
			enable()
			env["CLAUDE_CODE_TMPDIR"] = filepath.Join(proj, "scratch")
			p := build()
			for _, v := range p.Volumes {
				parts := strings.Split(v, ":")
				Expect(parts[1]).NotTo(HaveSuffix("cc-socks"), v)
			}
			Expect(filepath.Join(cfgDir, "tmp", "cc-socks")).NotTo(BeADirectory())
			Expect(filepath.Join(proj, "scratch", "cc-socks")).NotTo(BeADirectory())
		})

		It("CS-LNCH-050: the socket address is IDENTICAL under the default and any other CLAUDE_CONFIG_DIR", func() {
			enable()
			def := build()
			alt := filepath.Join(home, "work", "other-tree", ".claude")
			mkdir(alt)
			env["CLAUDE_CONFIG_DIR"] = alt
			other := build()

			Expect(envValues(other, "XDG_RUNTIME_DIR")).To(Equal(envValues(def, "XDG_RUNTIME_DIR")))
			Expect(envValues(def, "XDG_RUNTIME_DIR")).To(Equal([]string{root}))
			Expect(def.Volumes).To(ContainElement(rootMount()))
			Expect(other.Volumes).To(ContainElement(rootMount()))
			// Only the registry DESTINATION follows the config dir.
			Expect(other.Volumes).To(ContainElement(
				filepath.Join(root, "sessions") + ":" + filepath.Join(alt, "sessions")))
			Expect(def.Volumes).To(ContainElement(
				filepath.Join(root, "sessions") + ":" + filepath.Join(cfgDir, "sessions")))
		})

		It("CS-LNCH-050: CLAUDE_CODE_TMPDIR is unchanged by the bridge, so scratchpads do not move", func() {
			alt := filepath.Join(home, "alt-cfg")
			mkdir(alt)
			ef := filepath.Join(proj, "env")
			touch(ef, "CLAUDE_CODE_TMPDIR=/somewhere/else\n")
			cases := []func(){
				func() {},
				func() { env["CLAUDE_CONFIG_DIR"] = alt },
				func() { env["CLAUDE_CODE_TMPDIR"] = filepath.Join(proj, "scratch") },
				func() { in.EnvFiles = []string{ef} },
				func() { Expect(os.RemoveAll(cfgDir)).To(Succeed()) },
			}
			for i, setup := range cases {
				delete(env, "CLAUDE_CONFIG_DIR")
				delete(env, "CLAUDE_CODE_TMPDIR")
				in.EnvFiles = nil
				mkdir(cfgDir)
				setup()
				in.Cfg = nil
				off := envValues(build(), "CLAUDE_CODE_TMPDIR")
				enable()
				Expect(envValues(build(), "CLAUDE_CODE_TMPDIR")).To(Equal(off), fmt.Sprintf("case %d", i))
			}
		})

		It("CS-LNCH-051: creates peers/, sessions/ and cc-socks/ 0700 under the fixed root before docker run", func() {
			enable()
			Expect(root).NotTo(BeADirectory())
			build()
			for _, d := range []string{root, filepath.Join(root, "sessions"), filepath.Join(root, "cc-socks")} {
				fi, err := os.Stat(d)
				Expect(err).NotTo(HaveOccurred(), d)
				Expect(fi.IsDir()).To(BeTrue(), d)
				Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o700)), d)
			}
			Expect(launch.PeerRegistryRoot).To(Equal(".cache/claude-sandbox/peers"))
			// One root for every fixed host-side mount, so they cannot drift.
			Expect(launch.PeerRegistryRoot).To(HavePrefix(launch.PackageCacheRoot + "/"))
		})

		It("CS-LNCH-051: creates the registry DESTINATION 0700, so docker never makes it as root", func() {
			enable()
			dst := filepath.Join(cfgDir, "sessions")
			Expect(dst).NotTo(BeADirectory())
			build()
			fi, err := os.Stat(dst)
			Expect(err).NotTo(HaveOccurred())
			Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o700)))
		})

		It("CS-LNCH-051: creates no registry destination under a mount whose host and container differ", func() {
			enable()
			hostSide := filepath.Join(home, "srv-data")
			in.Cfg.Mounts = []cascade.Mount{{Host: hostSide, Container: "/data", Writable: true}}
			env["CLAUDE_CONFIG_DIR"] = "/data/cfg"
			p, err := launch.Build(in)
			Expect(err).NotTo(HaveOccurred())
			Expect("/data").NotTo(BeADirectory())
			Expect(filepath.Join(hostSide, "cfg")).NotTo(BeADirectory())
			Expect(p.Volumes).To(ContainElement(
				filepath.Join(root, "sessions") + ":/data/cfg/sessions"))
		})

		It("CS-LNCH-051: creates no destination when the config dir is absent and unmounted", func() {
			enable()
			Expect(os.RemoveAll(cfgDir)).To(Succeed())
			p, err := launch.Build(in)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfgDir).NotTo(BeADirectory())
			Expect(p.Volumes).To(ContainElement(
				filepath.Join(root, "sessions") + ":" + filepath.Join(cfgDir, "sessions")))
			// Messaging does not depend on the config dir: still bridged.
			Expect(envValues(p, "XDG_RUNTIME_DIR")).To(Equal([]string{root}))
		})

		It("CS-LNCH-052: the env var enables it over an unset or false config", func() {
			env["CLAUDE_SANDBOX_SHARED_PEER_REGISTRY"] = "1"
			p := build()
			Expect(p.Volumes).To(ContainElement(
				filepath.Join(root, "sessions") + ":" + filepath.Join(cfgDir, "sessions")))
			Expect(envValues(p, "XDG_RUNTIME_DIR")).To(Equal([]string{root}))
			f := false
			in.Cfg = &cascade.Config{SharedPeerRegistry: &f}
			Expect(build().Volumes).To(ContainElement(
				filepath.Join(root, "sessions") + ":" + filepath.Join(cfgDir, "sessions")))
		})

		It("CS-LNCH-052: a falsy env var is an explicit off over a config true", func() {
			enable()
			for _, v := range []string{"0", "false", "no", "NO"} {
				env["CLAUDE_SANDBOX_SHARED_PEER_REGISTRY"] = v
				Expect(peerEntries(build())).To(BeEmpty(), v)
			}
			// Anything else is unset, so the config still wins.
			env["CLAUDE_SANDBOX_SHARED_PEER_REGISTRY"] = "maybe"
			Expect(build().Volumes).To(ContainElement(
				filepath.Join(root, "sessions") + ":" + filepath.Join(cfgDir, "sessions")))
		})

		It("CS-LNCH-052: the fingerprint changes with the key", func() {
			off := build().ConfigHash
			enable()
			Expect(build().ConfigHash).NotTo(Equal(off))
		})

		It("CS-LNCH-053: prints exactly one banner naming the shared root, only when bridged", func() {
			Expect(build()).NotTo(BeNil())
			Expect(out.String()).NotTo(ContainSubstring("Peer registry:"))
			Expect(out.String()).NotTo(ContainSubstring("sharedPeerRegistry"))
			enable()
			out.Reset()
			build()
			Expect(strings.Count(out.String(), "Peer registry: shared (")).To(Equal(1))
			Expect(out.String()).To(ContainSubstring("Peer registry: shared (" + root + ")"))
			Expect(out.String()).To(ContainSubstring("every other opted-in sandbox on this host, and only those"))
			Expect(out.String()).NotTo(ContainSubstring("Warning: sharedPeerRegistry"))
		})

		// expectStoodDown pins a whole-bridge stand-down: nothing of the
		// bridge in the argv, nothing created, no banner, exactly one warning.
		expectStoodDown := func(p *launch.Plan, peersRoot, reason string) {
			Expect(peerEntries(p)).To(BeEmpty())
			for _, v := range p.Volumes {
				Expect(v).NotTo(HaveSuffix("/sessions"), "no registry overmount")
			}
			Expect(envValues(p, "XDG_RUNTIME_DIR")).To(BeEmpty())
			Expect(filepath.Join(peersRoot, "cc-socks")).NotTo(BeADirectory())
			Expect(peersRoot).NotTo(BeADirectory())
			Expect(out.String()).NotTo(ContainSubstring("Peer registry:"))
			Expect(strings.Count(out.String(), "Warning: sharedPeerRegistry")).To(Equal(1))
			Expect(out.String()).To(ContainSubstring("Warning: sharedPeerRegistry is off for this session"))
			Expect(out.String()).To(ContainSubstring(reason))
			Expect(out.String()).NotTo(ContainSubstring("listed"))
		}

		It("CS-LNCH-054: an env file that sets XDG_RUNTIME_DIR keeps it, and the whole bridge stands down", func() {
			ef := filepath.Join(proj, "env")
			touch(ef, "# comment\nXDG_RUNTIME_DIR=/run/user/1000\n")
			in.EnvFiles = []string{ef}
			off := build()
			out.Reset()
			enable()
			p := build()
			expectStoodDown(p, root, "an env file sets XDG_RUNTIME_DIR")
			// Exactly a key-off launch, fingerprint included.
			Expect(p.CreateArgs(proj)).To(Equal(off.CreateArgs(proj)))
			Expect(p.ConfigHash).To(Equal(off.ConfigHash))
		})

		It("CS-LNCH-055: a socket path over 103 bytes stands the whole bridge down with a warning naming the length", func() {
			// Pad the home so root + /cc-socks/1234567.sock exceeds 103 bytes.
			longHome := filepath.Join(home, strings.Repeat("h", 110-len(home)))
			mkdir(longHome)
			mkdir(filepath.Join(longHome, ".claude"))
			in.Home = longHome
			longRoot := filepath.Join(longHome, ".cache", "claude-sandbox", "peers")
			n := len(longRoot) + len("/cc-socks/1234567.sock")
			Expect(n).To(BeNumerically(">", 103))
			enable()
			p := build()
			expectStoodDown(p, longRoot, fmt.Sprintf("would be %d bytes", n))
		})

		It("CS-LNCH-051: tightens an existing wider peers/, sessions/ and cc-socks/ to 0700", func() {
			for _, d := range []string{root, filepath.Join(root, "sessions"), filepath.Join(root, "cc-socks")} {
				Expect(os.MkdirAll(d, 0o777)).To(Succeed())
				Expect(os.Chmod(d, 0o777)).To(Succeed())
			}
			enable()
			build()
			for _, d := range []string{root, filepath.Join(root, "sessions"), filepath.Join(root, "cc-socks")} {
				fi, err := os.Stat(d)
				Expect(err).NotTo(HaveOccurred())
				Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o700)), d)
			}
		})

		It("CS-LNCH-051: does not re-mode an existing registry destination under the user's config dir", func() {
			dst := filepath.Join(cfgDir, "sessions")
			Expect(os.MkdirAll(dst, 0o755)).To(Succeed())
			Expect(os.Chmod(dst, 0o755)).To(Succeed())
			enable()
			build()
			fi, err := os.Stat(dst)
			Expect(err).NotTo(HaveOccurred())
			Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o755)))
		})

		It("CS-LNCH-055: a path of exactly 103 bytes is still bridged", func() {
			// root = home + /.cache/claude-sandbox/peers (28); suffix 22.
			pad := 103 - 22 - 28 - len(home) - 1
			Expect(pad).To(BeNumerically(">", 0))
			edge := filepath.Join(home, strings.Repeat("e", pad))
			mkdir(edge)
			in.Home = edge
			edgeRoot := filepath.Join(edge, ".cache", "claude-sandbox", "peers")
			Expect(len(edgeRoot) + len("/cc-socks/1234567.sock")).To(Equal(103))
			enable()
			Expect(envValues(build(), "XDG_RUNTIME_DIR")).To(Equal([]string{edgeRoot}))
			Expect(out.String()).NotTo(ContainSubstring("Warning: sharedPeerRegistry"))
		})
	})

	It("CS-LNCH-019: mounts the parent DIRECTORY of AWS path vars read-only, deduplicated", func() {
		t := true
		in.CLIAWS = &t
		awsDir := filepath.Join(home, ".aws")
		mkdir(awsDir)
		credsDir := filepath.Join(home, "work", "creds-dir")
		touch(filepath.Join(credsDir, "creds"), "creds")
		touch(filepath.Join(credsDir, "config"), "config")
		touch(filepath.Join(awsDir, "token"), "tok")
		env["AWS_SHARED_CREDENTIALS_FILE"] = filepath.Join(credsDir, "creds")
		env["AWS_CONFIG_FILE"] = filepath.Join(credsDir, "config")
		env["AWS_WEB_IDENTITY_TOKEN_FILE"] = filepath.Join(awsDir, "token")
		p := build()
		// One directory mount for both files in creds-dir.
		count := 0
		for _, v := range p.Volumes {
			if v == credsDir+":"+credsDir+":ro" {
				count++
			}
		}
		Expect(count).To(Equal(1))
		// The token file's parent is ~/.aws, already mounted: no duplicate.
		count = 0
		for _, v := range p.Volumes {
			if v == awsDir+":"+awsDir+":ro" {
				count++
			}
		}
		Expect(count).To(Equal(1))
	})

	It("CS-LNCH-020: refuses to mount an overly-broad parent directory with a WARNING", func() {
		t := true
		in.CLIAWS = &t
		cfgFile := filepath.Join(home, "aws-config") // directly under $HOME
		touch(cfgFile, "x")
		env["AWS_CONFIG_FILE"] = cfgFile
		p := build()
		Expect(p.Volumes).NotTo(ContainElement(home + ":" + home + ":ro"))
		Expect(errw.String()).To(ContainSubstring("WARNING"))
		Expect(errw.String()).To(ContainSubstring("refusing to bind-mount"))
	})

	It("CS-LNCH-020: warns and skips the mount when the AWS path var's file does not exist", func() {
		t := true
		in.CLIAWS = &t
		missing := filepath.Join(home, "work", "creds-dir", "creds")
		env["AWS_SHARED_CREDENTIALS_FILE"] = missing
		p := build()
		dir := filepath.Dir(missing)
		Expect(p.Volumes).NotTo(ContainElement(dir + ":" + dir + ":ro"))
		Expect(errw.String()).To(ContainSubstring("file not found"))
	})

	// ---- config-driven container settings ----

	It("CS-LNCH-021: adds extra mounts (:ro unless writable) and skips project-dir duplicates with a notice", func() {
		in.Cfg = &cascade.Config{Mounts: []cascade.Mount{
			{Host: "/data/ro", Container: "/data/ro"},
			{Host: "/data/rw", Container: "/data/rw", Writable: true},
			{Host: "/elsewhere", Container: proj},
		}}
		p := build()
		Expect(p.Volumes).To(ContainElement("/data/ro:/data/ro:ro"))
		Expect(p.Volumes).To(ContainElement("/data/rw:/data/rw"))
		Expect(p.Volumes).NotTo(ContainElement("/elsewhere:" + proj))
		Expect(out.String()).To(ContainSubstring("duplicates project directory mount"))
	})

	It("CS-LNCH-022: sets --memory and --memory-swap to the configured memoryLimit, default 8g", func() {
		p := build()
		Expect(p.MemoryLimit).To(Equal("8g"))
		args := p.CreateArgs(proj)
		Expect(args).To(ContainElements("--memory", "8g", "--memory-swap", "8g"))

		in.Cfg = &cascade.Config{MemoryLimit: "16g"}
		p = build()
		Expect(p.CreateArgs(proj)).To(ContainElements("--memory", "16g", "--memory-swap", "16g"))
	})

	It("CS-LNCH-023: model precedence CLI > YAML", func() {
		in.Cfg = &cascade.Config{Model: "opus"}
		in.CLIModel = "sonnet"
		p := build()
		Expect(p.Command).To(ContainElements("--model", "sonnet"))
		Expect(p.Command).NotTo(ContainElement("opus"))

		in.CLIModel = ""
		p = build()
		Expect(p.Command).To(ContainElements("--model", "opus"))
	})

	// ---- container command & runtime env ----

	It("CS-LNCH-026: interactive command shape and container name", func() {
		in.SkipPermissions = true
		in.CLIModel = "opus"
		in.Passthrough = []string{"--resume"}
		in.Instance = "otter"
		p := build()
		Expect(p.Command).To(Equal([]string{"claude", "--dangerously-skip-permissions", "--model", "opus", "--resume"}))
		Expect(p.ContainerName).To(Equal("claude-sandbox-" + imagebuild.ProjectSlug(proj) + "-otter"))
	})

	It("CS-LNCH-027: ralph command shape, passthrough tail, and -ralph container name", func() {
		in.RalphMode = true
		in.Limit = "5"
		in.SkipPermissions = true
		in.Passthrough = []string{"--verbose"}
		p := build()
		Expect(p.Command).To(Equal([]string{"/opt/claude-sandbox/bin/ralph", "--limit", "5", "--dangerously-skip-permissions", "--verbose"}))
		Expect(p.ContainerName).To(Equal("claude-sandbox-" + imagebuild.ProjectSlug(proj) + "-ralph"))
	})

	It("CS-LNCH-028: the container name carries the parent segment and a path digest", func() {
		odd := filepath.Join(filepath.Dir(proj), "My_Cool.Project!")
		mkdir(odd)
		in.ProjectDir = odd
		in.Instance = "otter"
		p := build()
		Expect(p.ContainerName).To(MatchRegexp(
			`^claude-sandbox-` + regexp.QuoteMeta(imagebuild.Slug(filepath.Dir(odd))) + `-my_cool\.project--[0-9a-f]{6}-otter$`))
	})

	It("CS-LNCH-031: same-basename projects in different parents get different container names", func() {
		base := filepath.Dir(proj)
		a := filepath.Join(base, "marketing", "infrastructure")
		b := filepath.Join(base, "auth", "infrastructure")
		mkdir(a)
		mkdir(b)
		in.Instance = "otter"

		in.ProjectDir = a
		nameA := build().ContainerName
		in.ProjectDir = b
		nameB := build().ContainerName

		Expect(nameA).NotTo(Equal(nameB))
		Expect(nameA).To(ContainSubstring("marketing-infrastructure-"))
		Expect(nameB).To(ContainSubstring("auth-infrastructure-"))
	})

	It("CS-LNCH-032: docker run carries the identity labels", func() {
		in.Instance = "otter"
		in.Version = "v1.2.3"
		in.CLIModel = "opus"
		p := build()
		Expect(p.Labels).To(ContainElements(
			"claude-sandbox.project="+proj,
			"claude-sandbox.mode=claude",
			"claude-sandbox.instance=otter",
			"claude-sandbox.version=v1.2.3",
			"claude-sandbox.model=opus",
		))
		Expect(p.Labels).To(ContainElement("claude-sandbox.confighash=" + p.ConfigHash))

		args := p.CreateArgs(proj)
		for _, l := range p.Labels {
			Expect(argPairs(args, "--label")).To(ContainElement(l))
		}
	})

	It("CS-LNCH-039: the pid class rides a label and an env var, for interactive and ralph launches", func() {
		in.PIDClass = "17"
		for _, ralph := range []bool{false, true} {
			in.RalphMode = ralph
			p := build()
			Expect(p.Labels).To(ContainElement("claude-sandbox.pidclass=17"))
			Expect(p.EnvFlags).To(ContainElement("CLAUDE_SANDBOX_PID_CLASS=17"))
			args := p.CreateArgs(proj)
			Expect(argPairs(args, "--label")).To(ContainElement("claude-sandbox.pidclass=17"))
			Expect(argPairs(args, "-e")).To(ContainElement("CLAUDE_SANDBOX_PID_CLASS=17"))
		}
		in.PIDClass = ""
		p := build()
		Expect(p.Labels).NotTo(ContainElement(HavePrefix("claude-sandbox.pidclass=")))
		Expect(p.EnvFlags).NotTo(ContainElement(HavePrefix("CLAUDE_SANDBOX_PID_CLASS=")))
	})

	It("CS-LNCH-040: a different pid class is never drift", func() {
		in.PIDClass = "1"
		a := build()
		in.PIDClass = "200"
		b := build()
		Expect(a.ConfigHash).To(Equal(b.ConfigHash))
	})

	It("CS-LNCH-033: docker start carries the detach keys for the primary session, docker create does not", func() {
		// Regression: these were originally passed only to `docker attach`, so a
		// normally-launched session silently ran with docker's ctrl-p,ctrl-q —
		// which the Claude Code TUI binds — and ctrl-q,ctrl-q did nothing.
		p := build()
		Expect(p.DetachKeys).To(Equal("ctrl-q,ctrl-q"))
		Expect(p.StartArgs()).To(ContainElement("--detach-keys=ctrl-q,ctrl-q"))
		for _, a := range p.CreateArgs(proj) {
			Expect(a).NotTo(HavePrefix("--detach-keys"), "create attaches nothing; the keys belong to start")
		}
	})

	It("CS-LNCH-033: the detachKeys config key overrides the default", func() {
		in.Cfg = &cascade.Config{DetachKeys: "ctrl-^"}
		p := build()
		Expect(p.DetachKeys).To(Equal("ctrl-^"))
		Expect(p.StartArgs()).To(ContainElement("--detach-keys=ctrl-^"))
	})

	It("CS-LNCH-033: a whitespace-only override falls back to the default", func() {
		in.Cfg = &cascade.Config{DetachKeys: "   "}
		Expect(build().DetachKeys).To(Equal(launch.DefaultDetachKeys))
	})

	It("CS-LNCH-029, CS-LNCH-056: create leads with -it --rm --init; start is -ai with the keys and the name", func() {
		p := build()
		args := p.CreateArgs(proj)
		Expect(args[0:4]).To(Equal([]string{"create", "-it", "--rm", "--init"}))
		Expect(args).To(ContainElements("--name", p.ContainerName, p.Image))
		Expect(p.StartArgs()).To(Equal([]string{"start", "-ai", "--detach-keys=ctrl-q,ctrl-q", p.ContainerName}))
	})

	Describe("CS-LNCH-056: reserve, then start", func() {
		It("reserves with docker create and hands off to docker start -ai", func() {
			fake := &execx.Fake{}
			p := build()
			Expect(p.Reserve(fake, proj, errw)).To(Succeed())
			Expect(p.Start(fake)).To(Succeed())
			Expect(fake.Calls).To(HaveLen(2))
			Expect(fake.Calls[0].Args).To(Equal(p.CreateArgs(proj)))
			Expect(fake.Execed).NotTo(BeNil())
			Expect(fake.Execed.Args).To(Equal(p.StartArgs()))
		})

		It("forwards docker's warnings from a successful create", func() {
			fake := &execx.Fake{}
			fake.OnFunc("docker create", func(c execx.Cmd) (string, error) {
				io.WriteString(c.Stderr, "WARNING: swap limit not supported\n")
				return "abc123", nil
			})
			Expect(build().Reserve(fake, proj, errw)).To(Succeed())
			Expect(errw.String()).To(ContainSubstring("swap limit"))
		})

		It("CS-SESS-053: a name Conflict is ErrNameConflict; any other failure is not", func() {
			fake := &execx.Fake{}
			fake.OnFunc("docker create", func(c execx.Cmd) (string, error) {
				io.WriteString(c.Stderr, `Error response from daemon: Conflict. The container name "/x" is already in use`)
				return "", execx.Fail(125)
			})
			err := build().Reserve(fake, proj, errw)
			Expect(errors.Is(err, launch.ErrNameConflict)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("already in use"))

			other := &execx.Fake{}
			other.OnFunc("docker create", func(c execx.Cmd) (string, error) {
				io.WriteString(c.Stderr, "Error response from daemon: No such image: x")
				return "", execx.Fail(125)
			})
			err = build().Reserve(other, proj, errw)
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, launch.ErrNameConflict)).To(BeFalse())
			Expect(err.Error()).To(ContainSubstring("No such image"))

			// "Conflicting options" is a flag error a re-pick cannot fix.
			flags := &execx.Fake{}
			flags.OnFunc("docker create", func(c execx.Cmd) (string, error) {
				io.WriteString(c.Stderr, "docker: Conflicting options: --rm and --restart.")
				return "", execx.Fail(125)
			})
			err = build().Reserve(flags, proj, errw)
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, launch.ErrNameConflict)).To(BeFalse())
			Expect(err.Error()).To(ContainSubstring("Conflicting options"))
		})
	})

	It("CS-SESS-035: a ralph container carries mode=ralph and no instance label", func() {
		in.RalphMode = true
		p := build()
		Expect(p.Labels).To(ContainElement("claude-sandbox.mode=ralph"))
		for _, l := range p.Labels {
			Expect(l).NotTo(HavePrefix("claude-sandbox.instance="))
		}
	})

	It("CS-LNCH-029: container runtime environment flags", func() {
		env["ANTHROPIC_API_KEY"] = ""
		p := build()
		args := p.CreateArgs(proj)
		Expect(args[0:4]).To(Equal([]string{"create", "-it", "--rm", "--init"}))
		Expect(p.EnvFlags).To(ContainElements(
			"HOST_UID=1000",
			"HOST_GID=1000",
			"HOST_USER=tester",
			"HOST_HOME="+home,
			"HOME="+home,
			"DOCKER_GID=",
			"ANTHROPIC_API_KEY=",
		))
		// Every env flag is rendered as -e KEY=VAL in the argv.
		for _, e := range p.EnvFlags {
			Expect(args).To(ContainElement(e))
		}
	})

	It("CS-LNCH-029: forwards ANTHROPIC_API_KEY when set", func() {
		env["ANTHROPIC_API_KEY"] = "sk-test"
		p := build()
		Expect(p.EnvFlags).To(ContainElement("ANTHROPIC_API_KEY=sk-test"))
	})

	// ---- durable scratchpad root ----

	It("CS-LNCH-034: derives CLAUDE_CODE_TMPDIR under the default config dir", func() {
		cfgDir := filepath.Join(home, ".claude")
		mkdir(cfgDir)
		p := build()
		Expect(p.EnvFlags).To(ContainElement("CLAUDE_CODE_TMPDIR=" + filepath.Join(cfgDir, "tmp")))
	})

	It("CS-LNCH-034: derives CLAUDE_CODE_TMPDIR under CLAUDE_CONFIG_DIR when set", func() {
		alt := filepath.Join(home, "alt-cfg")
		mkdir(alt)
		env["CLAUDE_CONFIG_DIR"] = alt
		p := build()
		Expect(p.EnvFlags).To(ContainElement("CLAUDE_CODE_TMPDIR=" + filepath.Join(alt, "tmp")))
	})

	It("CS-LNCH-034: sets no CLAUDE_CODE_TMPDIR when the config dir does not exist", func() {
		p := build()
		for _, e := range p.EnvFlags {
			Expect(e).NotTo(HavePrefix("CLAUDE_CODE_TMPDIR="))
		}
	})

	It("CS-LNCH-034: forwards a host-env CLAUDE_CODE_TMPDIR verbatim instead of deriving", func() {
		cfgDir := filepath.Join(home, ".claude")
		mkdir(cfgDir)
		env["CLAUDE_CODE_TMPDIR"] = proj + "/scratch"
		p := build()
		Expect(p.EnvFlags).To(ContainElement("CLAUDE_CODE_TMPDIR=" + proj + "/scratch"))
		Expect(p.EnvFlags).NotTo(ContainElement("CLAUDE_CODE_TMPDIR=" + filepath.Join(cfgDir, "tmp")))
		// Inside the project mount: no warning.
		Expect(out.String()).NotTo(ContainSubstring("CLAUDE_CODE_TMPDIR"))
	})

	It("CS-LNCH-034: warns when a host-env CLAUDE_CODE_TMPDIR is outside every mount", func() {
		env["CLAUDE_CODE_TMPDIR"] = "/var/tmp/elsewhere"
		p := build()
		Expect(p.EnvFlags).To(ContainElement("CLAUDE_CODE_TMPDIR=/var/tmp/elsewhere"))
		Expect(out.String()).To(ContainSubstring("not under any container mount"))
	})

	It("CS-LNCH-034: stands down when an env file defines CLAUDE_CODE_TMPDIR (-e would override --env-file)", func() {
		cfgDir := filepath.Join(home, ".claude")
		mkdir(cfgDir)
		ef := filepath.Join(home, "envfile")
		touch(ef, "# comment\nCLAUDE_CODE_TMPDIR=/somewhere\n")
		in.EnvFiles = []string{ef}
		p := build()
		for _, e := range p.EnvFlags {
			Expect(e).NotTo(HavePrefix("CLAUDE_CODE_TMPDIR="))
		}
	})

	It("renders env files as stacked --env-file flags in cascade order", func() {
		in.EnvFiles = []string{"/root/env", "/proj/env"}
		args := build().CreateArgs(proj)
		Expect(args).To(ContainElements("--env-file", "/root/env"))
		i := indexOf(args, "/root/env")
		j := indexOf(args, "/proj/env")
		Expect(i).To(BeNumerically("<", j))
	})
})

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}
