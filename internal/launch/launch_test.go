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
	"os/user"
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

	It("CS-LNCH-011: does not shadow settings.json; the host file stays live through the config-dir bind", func() {
		cfgDir := filepath.Join(home, ".claude")
		settings := filepath.Join(cfgDir, "settings.json")
		touch(settings, `{"theme":"dark","hooks":{"SessionStart":[]}}`)
		p := build()
		for _, v := range p.Volumes {
			Expect(strings.Split(v, ":")[1]).NotTo(Equal(settings), "volume %q shadows settings.json", v)
		}
		// Read-write config-dir bind (CS-LNCH-008) is what carries the file.
		Expect(p.Volumes).To(ContainElement(cfgDir + ":" + cfgDir))
		_, err := os.Stat(filepath.Join(in.TempDir, "settings.json"))
		Expect(os.IsNotExist(err)).To(BeTrue(), "no settings.json temp file may be written")
		// The host file is untouched.
		raw, err := os.ReadFile(settings)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(raw)).To(Equal(`{"theme":"dark","hooks":{"SessionStart":[]}}`))
	})

	It("CS-LNCH-011: carries no settings.json digest in the drift fingerprint", func() {
		touch(filepath.Join(home, ".claude", "settings.json"), `{"theme":"dark"}`)
		p := build()
		for _, d := range p.ConfigInputs {
			Expect(d.Path).NotTo(Equal("settings.json"))
		}
	})

	Describe("CS-LNCH-069: symlinked settings.json", func() {
		var cfgDir, link, dotfiles string

		BeforeEach(func() {
			cfgDir = filepath.Join(home, ".claude")
			mkdir(cfgDir)
			link = filepath.Join(cfgDir, "settings.json")
			dotfiles = filepath.Join(filepath.Dir(home), "dotfiles")
			mkdir(dotfiles)
		})

		// mountsFor returns the volumes whose host side is target.
		mountsFor := func(p *launch.Plan, target string) []string {
			var got []string
			for _, v := range p.Volumes {
				if strings.HasPrefix(v, target+":") {
					got = append(got, v)
				}
			}
			return got
		}

		It("CS-LNCH-069: mounts an absolute link's target read-write at its own path", func() {
			target := filepath.Join(dotfiles, "claude-settings.json")
			touch(target, `{"theme":"dark"}`)
			Expect(os.Symlink(target, link)).To(Succeed())
			p := build()
			Expect(mountsFor(p, target)).To(Equal([]string{target + ":" + target}))
			Expect(errw.String()).To(BeEmpty())
		})

		It("CS-LNCH-069: resolves a relative link and a chain to the final target", func() {
			final := filepath.Join(dotfiles, "real.json")
			touch(final, `{}`)
			hop := filepath.Join(dotfiles, "hop.json")
			Expect(os.Symlink("real.json", hop)).To(Succeed())
			rel, err := filepath.Rel(cfgDir, hop)
			Expect(err).NotTo(HaveOccurred())
			Expect(os.Symlink(rel, link)).To(Succeed())
			p := build()
			Expect(mountsFor(p, final)).To(Equal([]string{final + ":" + final}))
			Expect(mountsFor(p, hop)).To(BeEmpty())
		})

		It("CS-LNCH-069: adds nothing when the target is inside the config dir", func() {
			touch(filepath.Join(cfgDir, "settings.real.json"), `{}`)
			Expect(os.Symlink("settings.real.json", link)).To(Succeed())
			before := len(build().Volumes)
			Expect(os.Remove(link)).To(Succeed())
			touch(link, `{}`)
			Expect(len(build().Volumes)).To(Equal(before))
		})

		It("CS-LNCH-069: adds nothing when the target is under the project mount", func() {
			target := filepath.Join(proj, "settings.json")
			touch(target, `{}`)
			Expect(os.Symlink(target, link)).To(Succeed())
			Expect(mountsFor(build(), target)).To(BeEmpty())
		})

		It("CS-LNCH-069: adds nothing when a same-path cascade mount already covers the target", func() {
			target := filepath.Join(dotfiles, "claude-settings.json")
			touch(target, `{}`)
			Expect(os.Symlink(target, link)).To(Succeed())
			in.Cfg = &cascade.Config{Mounts: []cascade.Mount{{Host: dotfiles, Container: dotfiles}}}
			p := build()
			Expect(mountsFor(p, target)).To(BeEmpty())
			Expect(p.Volumes).To(ContainElement(dotfiles + ":" + dotfiles + ":ro"))
		})

		It("CS-LNCH-069: warns once and mounts nothing for a dangling link", func() {
			missing := filepath.Join(dotfiles, "gone.json")
			Expect(os.Symlink(missing, link)).To(Succeed())
			p := build()
			Expect(mountsFor(p, missing)).To(BeEmpty())
			Expect(strings.Count(errw.String(), "\n")).To(Equal(1))
			Expect(errw.String()).To(ContainSubstring(link))
			Expect(errw.String()).To(ContainSubstring("without user settings"))
		})

		It("CS-LNCH-069: prints nothing and adds nothing for a regular file", func() {
			touch(link, `{}`)
			p := build()
			for _, v := range p.Volumes {
				Expect(v).NotTo(HavePrefix(dotfiles))
			}
			Expect(errw.String()).To(BeEmpty())
		})

		It("CS-LNCH-160: a target under a read-only same-path mount adds nothing and warns once", func() {
			target := filepath.Join(dotfiles, "claude-settings.json")
			touch(target, `{}`)
			Expect(os.Symlink(target, link)).To(Succeed())
			in.Cfg = &cascade.Config{Mounts: []cascade.Mount{{Host: dotfiles, Container: dotfiles}}}
			p := build()
			Expect(mountsFor(p, target)).To(BeEmpty())
			Expect(strings.Count(errw.String(), "\n")).To(Equal(1))
			Expect(errw.String()).To(ContainSubstring(target))
			Expect(errw.String()).To(ContainSubstring(dotfiles + ":" + dotfiles + ":ro"))
			Expect(errw.String()).To(ContainSubstring("read-only"))
		})

		It("CS-LNCH-160: a target under a writable same-path mount adds nothing and prints nothing", func() {
			target := filepath.Join(dotfiles, "claude-settings.json")
			touch(target, `{}`)
			Expect(os.Symlink(target, link)).To(Succeed())
			in.Cfg = &cascade.Config{Mounts: []cascade.Mount{{Host: dotfiles, Container: dotfiles, Writable: true}}}
			p := build()
			Expect(mountsFor(p, target)).To(BeEmpty())
			Expect(errw.String()).To(BeEmpty())
		})

		It("CS-LNCH-160: a directory target is not mounted, with one warning", func() {
			target := filepath.Join(dotfiles, "claude-dir")
			mkdir(target)
			Expect(os.Symlink(target, link)).To(Succeed())
			p := build()
			Expect(mountsFor(p, target)).To(BeEmpty())
			Expect(strings.Count(errw.String(), "\n")).To(Equal(1))
			Expect(errw.String()).To(ContainSubstring(link))
			Expect(errw.String()).To(ContainSubstring(target))
			Expect(errw.String()).To(ContainSubstring("not a regular file"))
			Expect(errw.String()).To(ContainSubstring("without user settings"))
		})

		It("CS-LNCH-160: a FIFO target is not mounted, with one warning", func() {
			target := filepath.Join(dotfiles, "claude-fifo")
			Expect(syscall.Mkfifo(target, 0o600)).To(Succeed())
			Expect(os.Symlink(target, link)).To(Succeed())
			p := build()
			Expect(mountsFor(p, target)).To(BeEmpty())
			Expect(errw.String()).To(ContainSubstring("not a regular file"))
		})

		It("CS-LNCH-160: a target path containing ':' is not mounted, with one warning, and the launch goes on", func() {
			colonDir := filepath.Join(dotfiles, "a:b")
			mkdir(colonDir)
			target := filepath.Join(colonDir, "settings.json")
			touch(target, `{}`)
			Expect(os.Symlink(target, link)).To(Succeed())
			p, err := launch.Build(in)
			Expect(err).NotTo(HaveOccurred())
			for _, v := range p.Volumes {
				Expect(v).NotTo(ContainSubstring("a:b"))
			}
			Expect(strings.Count(errw.String(), "\n")).To(Equal(1))
			Expect(errw.String()).To(ContainSubstring(link))
			Expect(errw.String()).To(ContainSubstring(target))
			Expect(errw.String()).To(ContainSubstring("contains ':'"))
			Expect(errw.String()).To(ContainSubstring("without user settings"))
		})

		It("CS-LNCH-069: the target mount is in the drift fingerprint", func() {
			target := filepath.Join(dotfiles, "claude-settings.json")
			touch(target, `{}`)
			in.TempDir = filepath.Join(filepath.Dir(home), "shadow1")
			mkdir(in.TempDir)
			plain := build().ConfigHash
			Expect(os.Symlink(target, link)).To(Succeed())
			in.TempDir = filepath.Join(filepath.Dir(home), "shadow2")
			mkdir(in.TempDir)
			Expect(build().ConfigHash).NotTo(Equal(plain))
		})
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
			"AWS_PROFILE", "AWS_REGION", "AWS_ACCESS_KEY_ID"))
		// Unset allowlist vars are not forwarded.
		for _, e := range p.EnvFlags {
			Expect(e).NotTo(HavePrefix("AWS_SESSION_TOKEN"))
		}
	})

	It("CS-LNCH-018, CS-LNCH-106: an unset or empty allowlist variable gets no -e, so the env-file value applies", func() {
		t := true
		in.CLIAWS = &t
		mkdir(filepath.Join(home, ".aws"))
		env["AWS_REGION"] = ""
		envFile := filepath.Join(proj, ".claude-sandbox", "env")
		Expect(os.MkdirAll(filepath.Dir(envFile), 0o755)).To(Succeed())
		Expect(os.WriteFile(envFile, []byte("AWS_PROFILE=from-envfile\nAWS_REGION=eu-west-1\n"), 0o600)).To(Succeed())
		in.EnvFiles = []string{envFile}
		p := build()
		for _, e := range p.EnvFlags {
			Expect(e).NotTo(HavePrefix("AWS_PROFILE"))
			Expect(e).NotTo(HavePrefix("AWS_REGION"))
		}
		Expect(envFileContents(p.CreateArgs(proj))).To(Equal([]string{"AWS_PROFILE=from-envfile\nAWS_REGION=eu-west-1\n"}))
	})

	It("CS-LNCH-103: forwards the AWS allowlist by name and never puts a value in argv", func() {
		t := true
		in.CLIAWS = &t
		env["AWS_PROFILE"] = "dev-profile-value"
		env["AWS_ACCESS_KEY_ID"] = "AKIASENTINELKEYID"
		env["AWS_SECRET_ACCESS_KEY"] = "sentinel-secret-access-key"
		env["AWS_SESSION_TOKEN"] = "sentinel-session-token"
		env["AWS_REGION"] = "" // set-but-empty stays unforwarded, as before
		p := build()
		args := p.CreateArgs(proj)
		for _, k := range []string{"AWS_PROFILE", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN"} {
			Expect(p.EnvFlags).To(ContainElement(k))
			Expect(args).To(ContainElement(k))
		}
		for _, e := range p.EnvFlags {
			Expect(e).NotTo(HavePrefix("AWS_REGION"), "empty allowlist var must not be forwarded")
		}
		for _, a := range args {
			for _, v := range []string{"dev-profile-value", "AKIASENTINELKEYID", "sentinel-secret-access-key", "sentinel-session-token"} {
				Expect(a).NotTo(ContainSubstring(v))
			}
		}
	})

	// ---- package caches ----

	Describe("package caches (CS-LNCH-035..037, 155..158)", func() {
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

		It("CS-LNCH-036: creates the host directories 0700 as the invoking user before docker create", func() {
			Expect(filepath.Join(root, "go-mod")).NotTo(BeADirectory())
			build()
			for _, name := range []string{"go-mod", "go-build", "npm", "pip"} {
				fi, err := os.Lstat(filepath.Join(root, name))
				Expect(err).NotTo(HaveOccurred())
				Expect(fi.IsDir()).To(BeTrue())
				Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o700)))
				Expect(int(fi.Sys().(*syscall.Stat_t).Uid)).To(Equal(os.Getuid()))
			}
		})

		It("CS-LNCH-036: tightens an existing wider directory", func() {
			d := filepath.Join(root, "npm")
			mkdir(d)
			Expect(os.Chmod(d, 0o755)).To(Succeed())
			Expect(build().Volumes).To(ContainElement(d + ":" + d))
			fi, err := os.Stat(d)
			Expect(err).NotTo(HaveOccurred())
			Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o700)))
		})

		cacheEnv := map[string]string{"go-mod": "GOMODCACHE", "go-build": "GOCACHE", "npm": "npm_config_cache", "pip": "PIP_CACHE_DIR"}
		// cacheMounts / cacheEnvs: the -v specs and -e values that name the
		// given cache dir, read from the docker create argv.
		cacheMounts := func(p *launch.Plan, d string) []string {
			var got []string
			for _, v := range argPairs(p.CreateArgs(proj), "-v") {
				if parts := strings.Split(v, ":"); len(parts) >= 2 && parts[1] == d {
					got = append(got, v)
				}
			}
			return got
		}
		cacheEnvs := func(p *launch.Plan, key string) []string {
			var vals []string
			for _, e := range argPairs(p.CreateArgs(proj), "-e") {
				if k, v, ok := strings.Cut(e, "="); ok && k == key {
					vals = append(vals, v)
				}
			}
			return vals
		}
		expectCache := func(p *launch.Plan, name string, on bool) {
			d := filepath.Join(root, name)
			if on {
				Expect(cacheMounts(p, d)).To(Equal([]string{d + ":" + d}), name)
				Expect(cacheEnvs(p, cacheEnv[name])).To(Equal([]string{d}), name)
			} else {
				Expect(cacheMounts(p, d)).To(BeEmpty(), name)
				Expect(cacheEnvs(p, cacheEnv[name])).To(BeEmpty(), name)
			}
		}

		It("CS-LNCH-156: a regular file in a cache's place leaves that cache out with one warning; the others and the launch go on", func() {
			d := filepath.Join(root, "go-mod")
			touch(d, "")
			p, err := launch.Build(in)
			Expect(err).NotTo(HaveOccurred())
			expectCache(p, "go-mod", false)
			for _, n := range []string{"go-build", "npm", "pip"} {
				expectCache(p, n, true)
			}
			o := out.String()
			Expect(strings.Count(o, "Warning: package cache")).To(Equal(1))
			Expect(o).To(ContainSubstring("Warning: package cache " + d + " not mounted: cannot prepare it (creating it:"))
			Expect(o).To(ContainSubstring("GOMODCACHE in this session"))
			Expect(o).To(ContainSubstring("chown it, or remove it and relaunch"))
		})

		It("CS-LNCH-156: a symlink in a cache's place is refused and its target left alone", func() {
			target := filepath.Join(home, "elsewhere")
			mkdir(target)
			Expect(os.Chmod(target, 0o755)).To(Succeed())
			mkdir(root)
			d := filepath.Join(root, "pip")
			Expect(os.Symlink(target, d)).To(Succeed())
			p := build()
			expectCache(p, "pip", false)
			Expect(out.String()).To(ContainSubstring("it is a symlink"))
			fi, err := os.Stat(target)
			Expect(err).NotTo(HaveOccurred())
			Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o755)))
		})

		It("CS-LNCH-156: an unwritable parent leaves every cache out without failing the launch", func() {
			if os.Getuid() == 0 {
				Skip("root ignores directory permissions")
			}
			mkdir(root)
			Expect(os.Chmod(root, 0o500)).To(Succeed())
			DeferCleanup(func() { _ = os.Chmod(root, 0o755) })
			p := build()
			for _, n := range []string{"go-mod", "go-build", "npm", "pip"} {
				expectCache(p, n, false)
			}
			Expect(strings.Count(out.String(), "Warning: package cache")).To(Equal(4))
			Expect(out.String()).To(ContainSubstring("permission denied"))
		})

		It("CS-LNCH-157: a cascade mount naming a cache dir is kept, not doubled, and its variable still names it", func() {
			d := filepath.Join(root, "go-build")
			in.Cfg = &cascade.Config{Mounts: []cascade.Mount{{Host: d, Container: d, Writable: true}}}
			p := build()
			expectCache(p, "go-build", true)
			Expect(errw.String()).NotTo(ContainSubstring("package cache"))
		})

		It("CS-LNCH-157: a read-only parent mount covers every cache, with one warning each", func() {
			cache := filepath.Join(home, ".cache")
			mkdir(cache)
			in.Cfg = &cascade.Config{Mounts: []cascade.Mount{{Host: cache, Container: cache}}}
			p := build()
			for n, key := range cacheEnv {
				d := filepath.Join(root, n)
				Expect(cacheMounts(p, d)).To(BeEmpty(), n)
				Expect(cacheEnvs(p, key)).To(Equal([]string{d}), n)
			}
			Expect(strings.Count(errw.String(), "WARNING: package cache")).To(Equal(4))
			Expect(errw.String()).To(ContainSubstring("under the read-only mount " + cache + ":" + cache + ":ro; GOMODCACHE cannot write to it"))
		})

		DescribeTable("CS-LNCH-158: a home that is not absolute panics under go test before anything is created",
			func(h string) {
				in.Home = h
				Expect(func() { _, _ = launch.Build(in) }).To(PanicWith(ContainSubstring("non-absolute home")))
				Expect(filepath.Join(h, ".cache", "claude-sandbox")).NotTo(BeAnExistingFile())
				Expect(filepath.Join(proj, ".cache")).NotTo(BeAnExistingFile())
			},
			Entry("relative", "rel/home"),
			Entry("empty", ""),
		)

		It("CS-LNCH-158: a test whose home is the real $HOME panics in the package caches, before the pre-commit cache", func() {
			real, err := os.UserHomeDir()
			Expect(err).NotTo(HaveOccurred())
			in.Home = real
			Expect(func() { _, _ = launch.Build(in) }).To(PanicWith(ContainSubstring("create the package caches under the real home")))
		})

		It("CS-LNCH-158: a test whose home is the user database's panics even when $HOME names elsewhere", func() {
			u, err := user.Current()
			Expect(err).NotTo(HaveOccurred())
			if u.HomeDir == "" {
				Skip("no home directory in the user database")
			}
			GinkgoT().Setenv("HOME", GinkgoT().TempDir())
			in.Home = u.HomeDir
			Expect(func() { _, _ = launch.Build(in) }).To(PanicWith(ContainSubstring("create the package caches under the real home")))
		})

		Describe("CS-LNCH-155: inside a sandbox", func() {
			BeforeEach(func() {
				env["CLAUDE_SANDBOX_PROJECT_DIR"] = "/outer/proj"
			})

			It("CS-LNCH-155: mounts each cache the outer sandbox mounted (its variable is this path)", func() {
				for n, key := range cacheEnv {
					env[key] = filepath.Join(root, n)
				}
				p := build()
				for n := range cacheEnv {
					expectCache(p, n, true)
				}
				Expect(out.String()).NotTo(ContainSubstring("package cache"))
			})

			It("CS-LNCH-155: with none mounted by the outer sandbox, nothing is mounted or created, with one note", func() {
				p := build()
				for n := range cacheEnv {
					expectCache(p, n, false)
					Expect(filepath.Join(root, n)).NotTo(BeAnExistingFile())
				}
				o := out.String()
				Expect(strings.Count(o, "Note: package caches not mounted")).To(Equal(1))
				Expect(o).To(ContainSubstring("inside a sandbox that does not mount " + filepath.Join(root, "go-mod") + ", "))
				Expect(o).To(ContainSubstring(filepath.Join(root, "pip") + ", so docker would create them on the host as root"))
			})

			It("CS-LNCH-155: decides per cache: a matching one is mounted, an unset or different one is not", func() {
				env["GOMODCACHE"] = filepath.Join(root, "go-mod")
				env["npm_config_cache"] = "/work/.npm"
				p := build()
				expectCache(p, "go-mod", true)
				for _, n := range []string{"go-build", "npm", "pip"} {
					expectCache(p, n, false)
					Expect(filepath.Join(root, n)).NotTo(BeAnExistingFile())
				}
				o := out.String()
				Expect(strings.Count(o, "Note: package caches not mounted")).To(Equal(1))
				Expect(o).NotTo(ContainSubstring(filepath.Join(root, "go-mod")))
				Expect(o).To(ContainSubstring(filepath.Join(root, "npm")))
			})

			DescribeTable("CS-LNCH-155: the outer's variable is compared path-cleaned",
				func(val func(string) string) {
					for n, key := range cacheEnv {
						env[key] = val(filepath.Join(root, n))
					}
					p := build()
					for n := range cacheEnv {
						expectCache(p, n, true)
					}
					Expect(out.String()).NotTo(ContainSubstring("package cache"))
				},
				Entry("trailing slash", func(d string) string { return d + "/" }),
				Entry("trailing /.", func(d string) string { return d + "/." }),
				Entry("doubled separator", func(d string) string {
					return strings.Replace(d, "/claude-sandbox/", "/claude-sandbox//", 1)
				}),
			)
		})

		Describe("CS-LNCH-159: an env file defining a cache's variable wins for that cache", func() {
			useEnvFile := func(content string) {
				ef := filepath.Join(proj, "env")
				touch(ef, content)
				in.EnvFiles = []string{ef}
			}
			expectSkipped := func(p *launch.Plan, name string) {
				d := filepath.Join(root, name)
				expectCache(p, name, false)
				Expect(d).NotTo(BeAnExistingFile())
				Expect(out.String()).NotTo(ContainSubstring(d))
				Expect(errw.String()).NotTo(ContainSubstring(d))
			}

			DescribeTable("CS-LNCH-159: only the defined cache is skipped; the other three are applied",
				func(name string) {
					useEnvFile(cacheEnv[name] + "=/work/elsewhere\n")
					p := build()
					expectSkipped(p, name)
					for n := range cacheEnv {
						if n != name {
							expectCache(p, n, true)
						}
					}
					Expect(out.String()).NotTo(ContainSubstring("package cache"))
				},
				Entry("GOMODCACHE", "go-mod"),
				Entry("GOCACHE", "go-build"),
				Entry("npm_config_cache", "npm"),
				Entry("PIP_CACHE_DIR", "pip"),
			)

			DescribeTable("CS-LNCH-159: read as docker reads it",
				func(content string, hostSet bool) {
					useEnvFile(content)
					if hostSet {
						env["GOMODCACHE"] = "/host/gomod"
					}
					p := build()
					expectSkipped(p, "go-mod")
					expectCache(p, "go-build", true)
					for _, a := range p.CreateArgs(proj) {
						Expect(a).NotTo(ContainSubstring("/host/gomod"))
					}
				},
				Entry("indented", "  GOMODCACHE=/work/gm\n", false),
				Entry("UTF-8 BOM", "\xEF\xBB\xBFGOMODCACHE=/work/gm\n", false),
				Entry("CRLF", "# c\r\nGOMODCACHE=/work/gm\r\n", false),
				Entry("bare key, host sets it (docker passes the host value through)", "GOMODCACHE\n", true),
			)

			DescribeTable("CS-LNCH-159: a line docker would not set the variable from leaves the cache applied",
				func(content string) {
					useEnvFile(content)
					p := build()
					for n := range cacheEnv {
						expectCache(p, n, true)
					}
				},
				Entry("bare key, host does not set it (docker drops it)", "GOMODCACHE\n"),
				Entry("commented out", "# GOMODCACHE=/work/gm\n"),
				Entry("different case", "gomodcache=/work/gm\n"),
			)

			It("CS-LNCH-159: one env file per variable across the cascade counts, each cache decided on its own", func() {
				up := filepath.Join(home, "upenv")
				touch(up, "GOCACHE=/work/gb\n")
				local := filepath.Join(proj, "env")
				touch(local, "PIP_CACHE_DIR=/work/pip\n")
				in.EnvFiles = []string{up, local}
				p := build()
				expectSkipped(p, "go-build")
				expectSkipped(p, "pip")
				expectCache(p, "go-mod", true)
				expectCache(p, "npm", true)
			})

			It("CS-LNCH-159: all four defined never reaches the home check: nothing created, nothing printed", func() {
				useEnvFile("GOMODCACHE=/a\nGOCACHE=/b\nnpm_config_cache=/c\nPIP_CACHE_DIR=/d\nPRE_COMMIT_HOME=/e\n")
				in.Home = "rel/home"
				p, err := launch.Build(in)
				Expect(err).NotTo(HaveOccurred())
				for n := range cacheEnv {
					expectCache(p, n, false)
				}
				Expect("rel").NotTo(BeAnExistingFile())
				Expect(filepath.Join(proj, "rel")).NotTo(BeAnExistingFile())
				Expect(out.String()).NotTo(ContainSubstring("package cache"))
			})

			It("CS-LNCH-159: inside a sandbox, an env-file cache is not named in the nested note", func() {
				env["CLAUDE_SANDBOX_PROJECT_DIR"] = "/outer/proj"
				useEnvFile("npm_config_cache=/work/npm\n")
				p := build()
				expectSkipped(p, "npm")
				o := out.String()
				Expect(strings.Count(o, "Note: package caches not mounted")).To(Equal(1))
				Expect(o).To(ContainSubstring(filepath.Join(root, "go-mod")))
				Expect(o).NotTo(ContainSubstring(filepath.Join(root, "npm")))
			})
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
			// Only the always-on pre-commit cache (CS-LNCH-133) is under the root.
			for _, v := range p.Volumes {
				if strings.Contains(v, ".cache/claude-sandbox/pre-commit") {
					continue
				}
				Expect(v).NotTo(ContainSubstring(".cache/claude-sandbox"))
			}
			for _, e := range p.EnvFlags {
				Expect(e).NotTo(HavePrefix("GOMODCACHE="))
			}
		})
	})

	// ---- pre-commit cache ----

	Describe("pre-commit cache (CS-LNCH-133..139)", func() {
		var dir string
		BeforeEach(func() {
			dir = filepath.Join(home, ".cache", "claude-sandbox", "pre-commit")
		})
		envValues := func(p *launch.Plan) []string {
			var vals []string
			for _, e := range argPairs(p.CreateArgs(proj), "-e") {
				if k, v, ok := strings.Cut(e, "="); ok && k == "PRE_COMMIT_HOME" {
					vals = append(vals, v)
				}
			}
			return vals
		}
		mounts := func(p *launch.Plan) []string {
			var got []string
			for _, v := range argPairs(p.CreateArgs(proj), "-v") {
				if strings.Contains(v, "pre-commit") {
					got = append(got, v)
				}
			}
			return got
		}
		expectApplied := func(p *launch.Plan) {
			Expect(mounts(p)).To(Equal([]string{dir + ":" + dir}))
			Expect(envValues(p)).To(Equal([]string{dir}))
		}
		expectAbsent := func(p *launch.Plan) {
			Expect(mounts(p)).To(BeEmpty())
			Expect(envValues(p)).To(BeEmpty())
		}

		It("CS-LNCH-133: mounts the sandbox-only cache writable at the same path and sets PRE_COMMIT_HOME", func() {
			mkdir(filepath.Join(home, ".cache", "pre-commit"))
			p := build()
			expectApplied(p)
			for _, v := range p.Volumes {
				Expect(v).NotTo(ContainSubstring(filepath.Join(home, ".cache", "pre-commit")))
			}
			Expect(launch.PreCommitCacheDir).To(Equal(".cache/claude-sandbox/pre-commit"))
			Expect(out.String()).NotTo(ContainSubstring("pre-commit"))
		})

		It("CS-LNCH-133: ralph and headless launches get it too", func() {
			in.RalphMode = true
			expectApplied(build())
			in.RalphMode = false
			in.Headless = true
			expectApplied(build())
		})

		It("CS-LNCH-133: the fingerprint carries the mount through the normalized mount set", func() {
			with := build().ConfigHash
			Expect(os.RemoveAll(dir)).To(Succeed())
			touch(dir, "") // CS-LNCH-134 stand-down: same launch without the mount
			p := build()
			expectAbsent(p)
			Expect(p.ConfigHash).NotTo(Equal(with))
		})

		It("CS-LNCH-134: creates the directory 0700 as the invoking user before docker create", func() {
			Expect(dir).NotTo(BeADirectory())
			build()
			fi, err := os.Lstat(dir)
			Expect(err).NotTo(HaveOccurred())
			Expect(fi.IsDir()).To(BeTrue())
			Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o700)))
			Expect(int(fi.Sys().(*syscall.Stat_t).Uid)).To(Equal(os.Getuid()))
		})

		It("CS-LNCH-134: tightens an existing wider directory", func() {
			mkdir(dir)
			Expect(os.Chmod(dir, 0o755)).To(Succeed())
			expectApplied(build())
			fi, err := os.Stat(dir)
			Expect(err).NotTo(HaveOccurred())
			Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o700)))
		})

		expectWarned := func(errText string) {
			o := out.String()
			Expect(strings.Count(o, "Warning: pre-commit cache")).To(Equal(1))
			Expect(o).To(ContainSubstring("Warning: pre-commit cache is not isolated for this session: cannot prepare " + dir + " ("))
			Expect(o).To(ContainSubstring(errText))
			Expect(o).To(ContainSubstring("shares the host's cache"))
			Expect(o).To(ContainSubstring("chown it, or remove it and relaunch"))
			Expect(o).To(ContainSubstring("set PRE_COMMIT_HOME in a .claude-sandbox/env file"))
		}

		It("CS-LNCH-134: a regular file in its place: the launch goes on without it, with one warning", func() {
			touch(dir, "")
			p, err := launch.Build(in)
			Expect(err).NotTo(HaveOccurred())
			expectAbsent(p)
			expectWarned("creating it:")
		})

		It("CS-LNCH-134: a symlink in its place is refused and its target left alone", func() {
			target := filepath.Join(home, "elsewhere")
			mkdir(target)
			Expect(os.Chmod(target, 0o755)).To(Succeed())
			mkdir(filepath.Dir(dir))
			Expect(os.Symlink(target, dir)).To(Succeed())
			p := build()
			expectAbsent(p)
			expectWarned("it is a symlink")
			fi, err := os.Stat(target)
			Expect(err).NotTo(HaveOccurred())
			Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o755)))
		})

		It("CS-LNCH-134: an unwritable parent: the launch goes on without it", func() {
			if os.Getuid() == 0 {
				Skip("root ignores directory permissions")
			}
			parent := filepath.Dir(dir)
			mkdir(parent)
			Expect(os.Chmod(parent, 0o500)).To(Succeed())
			DeferCleanup(func() { _ = os.Chmod(parent, 0o755) })
			expectAbsent(build())
			expectWarned("permission denied")
		})

		DescribeTable("CS-LNCH-135: an env file that defines PRE_COMMIT_HOME wins: nothing created, mounted or printed",
			func(content string, hostSet bool) {
				ef := filepath.Join(proj, "env")
				touch(ef, content)
				in.EnvFiles = []string{ef}
				if hostSet {
					env["PRE_COMMIT_HOME"] = "/host/pc"
				}
				p := build()
				expectAbsent(p)
				Expect(dir).NotTo(BeAnExistingFile())
				Expect(out.String()).NotTo(ContainSubstring("pre-commit"))
			},
			Entry("KEY=value", "PRE_COMMIT_HOME=/work/.pc\n", false),
			Entry("indented", "  PRE_COMMIT_HOME=/work/.pc\n", false),
			Entry("UTF-8 BOM", "\xEF\xBB\xBFPRE_COMMIT_HOME=/work/.pc\n", false),
			Entry("CRLF", "# c\r\nPRE_COMMIT_HOME=/work/.pc\r\n", false),
			Entry("bare key, host sets it (docker passes the host value through)", "PRE_COMMIT_HOME\n", true),
		)

		DescribeTable("CS-LNCH-135: an env-file line docker would not set PRE_COMMIT_HOME from leaves the default",
			func(content string) {
				ef := filepath.Join(proj, "env")
				touch(ef, content)
				in.EnvFiles = []string{ef}
				expectApplied(build())
			},
			Entry("bare key, host does not set it (docker drops it)", "PRE_COMMIT_HOME\n"),
			Entry("commented out", "# PRE_COMMIT_HOME=/work/.pc\n"),
			Entry("different case", "pre_commit_home=/work/.pc\n"),
		)

		It("CS-LNCH-136: the launcher's own PRE_COMMIT_HOME is not forwarded", func() {
			env["PRE_COMMIT_HOME"] = "/host/pc"
			p := build()
			expectApplied(p)
			for _, a := range p.CreateArgs(proj) {
				Expect(a).NotTo(ContainSubstring("/host/pc"))
			}
		})

		DescribeTable("CS-LNCH-138: a home that is not absolute panics under go test (and stands down otherwise)",
			func(h string) {
				in.Home = h
				Expect(func() { _, _ = launch.Build(in) }).To(PanicWith(ContainSubstring("non-absolute home")))
				Expect(filepath.Join(h, ".cache", "claude-sandbox", "pre-commit")).NotTo(BeAnExistingFile())
			},
			Entry("relative", "rel/home"),
			Entry("empty", ""),
		)

		It("CS-LNCH-138: a test whose home is the real $HOME panics before creating anything", func() {
			real, err := os.UserHomeDir()
			Expect(err).NotTo(HaveOccurred())
			in.Home = real
			Expect(func() { _, _ = launch.Build(in) }).To(PanicWith(ContainSubstring("under the real home")))
		})

		It("CS-LNCH-138: a test whose home is the user database's panics even when $HOME names elsewhere", func() {
			u, err := user.Current()
			Expect(err).NotTo(HaveOccurred())
			if u.HomeDir == "" {
				Skip("no home directory in the user database")
			}
			// HOME unset or pointing elsewhere (env -i): hostIdentity falls back
			// to the user database, so the guard must consult it too.
			GinkgoT().Setenv("HOME", GinkgoT().TempDir())
			in.Home = u.HomeDir
			Expect(func() { _, _ = launch.Build(in) }).To(PanicWith(ContainSubstring("under the real home")))
		})

		It("CS-LNCH-139: a cascade mount naming the directory is kept, not doubled, and PRE_COMMIT_HOME still names it", func() {
			in.Cfg = &cascade.Config{Mounts: []cascade.Mount{{Host: dir, Container: dir, Writable: true}}}
			p := build()
			var dsts []string
			for _, v := range argPairs(p.CreateArgs(proj), "-v") {
				if parts := strings.Split(v, ":"); len(parts) >= 2 && parts[1] == dir {
					dsts = append(dsts, v)
				}
			}
			Expect(dsts).To(Equal([]string{dir + ":" + dir}))
			Expect(envValues(p)).To(Equal([]string{dir}))
			Expect(errw.String()).NotTo(ContainSubstring("pre-commit"))
		})

		It("CS-LNCH-139: a read-only parent mount covers it, with one warning", func() {
			cache := filepath.Join(home, ".cache")
			mkdir(cache)
			in.Cfg = &cascade.Config{Mounts: []cascade.Mount{{Host: cache, Container: cache}}}
			p := build()
			Expect(mounts(p)).To(BeEmpty())
			Expect(envValues(p)).To(Equal([]string{dir}))
			Expect(strings.Count(errw.String(), "WARNING: pre-commit cache")).To(Equal(1))
			Expect(errw.String()).To(ContainSubstring("under the read-only mount " + cache + ":" + cache + ":ro"))
		})

		Describe("CS-LNCH-137: inside a sandbox", func() {
			BeforeEach(func() {
				env["CLAUDE_SANDBOX_PROJECT_DIR"] = "/outer/proj"
			})

			It("CS-LNCH-137: mounts it when the outer sandbox did (its PRE_COMMIT_HOME is this path)", func() {
				env["PRE_COMMIT_HOME"] = dir
				expectApplied(build())
				Expect(out.String()).NotTo(ContainSubstring("pre-commit"))
			})

			DescribeTable("CS-LNCH-137: otherwise nothing is mounted or created, with one note",
				func(val string) {
					if val != "" {
						env["PRE_COMMIT_HOME"] = val
					}
					p := build()
					expectAbsent(p)
					Expect(dir).NotTo(BeAnExistingFile())
					Expect(strings.Count(out.String(), "Note: pre-commit cache not mounted")).To(Equal(1))
					Expect(out.String()).To(ContainSubstring("inside a sandbox that does not mount " + dir))
				},
				Entry("unset (an older outer launcher)", ""),
				Entry("another path (the outer's env file chose one)", "/work/.pc"),
			)

			DescribeTable("CS-LNCH-137: the outer's PRE_COMMIT_HOME is compared path-cleaned",
				func(suffix string) {
					env["PRE_COMMIT_HOME"] = dir + suffix
					expectApplied(build())
					Expect(out.String()).NotTo(ContainSubstring("pre-commit"))
				},
				Entry("trailing slash", "/"),
				Entry("trailing /.", "/."),
			)

			It("CS-LNCH-137: a doubled separator inside the value still matches", func() {
				env["PRE_COMMIT_HOME"] = strings.Replace(dir, "/pre-commit", "//pre-commit", 1)
				expectApplied(build())
			})

			It("CS-LNCH-137: an env file still wins, silently", func() {
				ef := filepath.Join(proj, "env")
				touch(ef, "PRE_COMMIT_HOME=/work/.pc\n")
				in.EnvFiles = []string{ef}
				expectAbsent(build())
				Expect(out.String()).NotTo(ContainSubstring("pre-commit"))
			})
		})
	})

	Describe("shared peer registry (CS-LNCH-049..055, CS-LNCH-107)", func() {
		var root, cfgDir string
		BeforeEach(func() {
			// A short, fixed-root home: the bridge stands down past a 103-byte
			// socket path (CS-LNCH-055), so a home under a long TMPDIR would
			// fail every bridged case for a reason unrelated to the test.
			// /tmp, not os.TempDir(): TMPDIR is exactly what may be long.
			short, err := os.MkdirTemp("/tmp", "cs")
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(os.RemoveAll, short)
			short, err = filepath.EvalSymlinks(short)
			Expect(err).NotTo(HaveOccurred())
			home = filepath.Join(short, "h")
			mkdir(home)
			in.Home = home
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
			// The pre-commit cache (CS-LNCH-133) is always created; the peers
			// root is not.
			Expect(root).NotTo(BeADirectory())
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

		// CS-LNCH-108: the stand-down sees XDG_RUNTIME_DIR the way docker reads
		// the env file, through the cascade's shared reader.
		DescribeTable("CS-LNCH-108: an env-file XDG_RUNTIME_DIR docker would set stands the bridge down",
			func(content string, hostSet bool) {
				ef := filepath.Join(proj, "env")
				touch(ef, content)
				in.EnvFiles = []string{ef}
				if hostSet {
					env["XDG_RUNTIME_DIR"] = "/run/user/1000"
				}
				off := build()
				out.Reset()
				enable()
				p := build()
				expectStoodDown(p, root, "an env file sets XDG_RUNTIME_DIR")
				Expect(p.CreateArgs(proj)).To(Equal(off.CreateArgs(proj)))
				Expect(p.ConfigHash).To(Equal(off.ConfigHash))
			},
			Entry("indented with spaces", "  XDG_RUNTIME_DIR=/run/x\n", false),
			Entry("indented with a tab", "\tXDG_RUNTIME_DIR=/run/x\n", false),
			Entry("UTF-8 BOM on the first line", "\xEF\xBB\xBFXDG_RUNTIME_DIR=/run/x\n", false),
			Entry("CRLF line ending", "# c\r\nXDG_RUNTIME_DIR=/run/x\r\n", false),
			Entry("bare key, host sets it (docker passes it through)", "XDG_RUNTIME_DIR\n", true),
			Entry("bare CRLF key, host sets it", "XDG_RUNTIME_DIR\r\n", true),
		)

		It("CS-LNCH-108: a bare key the host sets to \"\" still stands the bridge down (docker passes it)", func() {
			ef := filepath.Join(proj, "env")
			touch(ef, "XDG_RUNTIME_DIR\n")
			in.EnvFiles = []string{ef}
			in.LookupEnv = func(k string) (string, bool) {
				if k == "XDG_RUNTIME_DIR" {
					return "", true
				}
				v, ok := env[k]
				return v, ok
			}
			enable()
			expectStoodDown(build(), root, "an env file sets XDG_RUNTIME_DIR")
		})

		DescribeTable("CS-LNCH-108: an env-file line docker would not set XDG_RUNTIME_DIR from leaves the bridge on",
			func(content string) {
				ef := filepath.Join(proj, "env")
				touch(ef, content)
				in.EnvFiles = []string{ef}
				enable()
				p := build()
				Expect(envValues(p, "XDG_RUNTIME_DIR")).To(Equal([]string{root}))
				Expect(out.String()).NotTo(ContainSubstring("Warning: sharedPeerRegistry"))
			},
			Entry("bare key, host does not set it (docker drops it)", "XDG_RUNTIME_DIR\n"),
			Entry("commented out, indented", "  # XDG_RUNTIME_DIR=/run/x\n"),
			Entry("blank before '=' (a key docker rejects)", "XDG_RUNTIME_DIR =/run/x\n"),
			Entry("different case", "xdg_runtime_dir=/run/x\n"),
			Entry("bare key ending in \\r\\r (key keeps one \\r)", "XDG_RUNTIME_DIR\r\r\n"),
		)

		It("CS-LNCH-055: a socket path over 103 bytes stands the whole bridge down with a warning naming the length", func() {
			// Pad the home so root + /cc-socks/1234567.sock exceeds 103 bytes.
			longHome := filepath.Join(home, strings.Repeat("h", 110-len(home)))
			mkdir(longHome)
			mkdir(filepath.Join(longHome, ".claude"))
			in.Home = longHome
			longRoot := filepath.Join(longHome, ".cache", "claude-sandbox", "peers")
			n := len(longRoot) + len("/cc-socks/1234567.sock")
			Expect(n).To(BeNumerically(">", 103))
			off := build()
			out.Reset()
			enable()
			p := build()
			expectStoodDown(p, longRoot, fmt.Sprintf("would be %d bytes", n))
			// Exactly a key-off launch, fingerprint included.
			Expect(p.CreateArgs(proj)).To(Equal(off.CreateArgs(proj)))
			Expect(p.ConfigHash).To(Equal(off.ConfigHash))
		})

		// expectPeerDirStandDown pins a CS-LNCH-107 stand-down: the launch
		// succeeds as a key-off launch, with one warning naming the directory,
		// the error and both remedies.
		expectPeerDirStandDown := func(setup func(), dir, errText string) {
			off := build()
			out.Reset()
			enable()
			setup()
			p, err := launch.Build(in)
			Expect(err).NotTo(HaveOccurred())
			Expect(peerEntries(p)).To(BeEmpty())
			Expect(envValues(p, "XDG_RUNTIME_DIR")).To(BeEmpty())
			Expect(p.CreateArgs(proj)).To(Equal(off.CreateArgs(proj)))
			Expect(p.ConfigHash).To(Equal(off.ConfigHash))
			o := out.String()
			Expect(o).NotTo(ContainSubstring("Peer registry:"))
			Expect(strings.Count(o, "Warning: sharedPeerRegistry")).To(Equal(1))
			Expect(o).To(ContainSubstring("Warning: sharedPeerRegistry is off for this session: cannot prepare " + dir + " ("))
			Expect(o).To(ContainSubstring(errText))
			Expect(o).To(ContainSubstring("chown it, or remove it and relaunch"))
			Expect(o).To(ContainSubstring("CLAUDE_SANDBOX_SHARED_PEER_REGISTRY=0"))
		}

		It("CS-LNCH-107: a chmod that fails on a peer dir stands the whole bridge down with the remedy", func() {
			var tried []string
			expectPeerDirStandDown(func() {
				in.Chmod = func(name string, _ os.FileMode) error {
					tried = append(tried, name)
					return &os.PathError{Op: "chmod", Path: name, Err: syscall.EPERM}
				}
			}, root, "restricting it to 0700: chmod "+root+": operation not permitted")
			// It stops at the first failure: one warning, not three.
			Expect(tried).To(Equal([]string{root}))
		})

		It("CS-LNCH-107: a peer dir that cannot be created stands the bridge down", func() {
			if os.Getuid() == 0 {
				Skip("root ignores directory permissions")
			}
			parent := filepath.Join(home, ".cache", "claude-sandbox")
			mkdir(parent)
			Expect(os.Chmod(parent, 0o500)).To(Succeed())
			DeferCleanup(func() { _ = os.Chmod(parent, 0o755) })
			expectPeerDirStandDown(func() {}, root, "creating it: mkdir "+root+": permission denied")
		})

		It("CS-LNCH-107: a regular file where cc-socks/ belongs stands the bridge down", func() {
			sock := filepath.Join(root, "cc-socks")
			mkdir(root)
			touch(sock, "")
			expectPeerDirStandDown(func() {}, sock, "creating it:")
		})

		It("CS-LNCH-107: a registry destination that cannot be created stands the bridge down", func() {
			if os.Getuid() == 0 {
				Skip("root ignores directory permissions")
			}
			Expect(os.Chmod(cfgDir, 0o500)).To(Succeed())
			DeferCleanup(func() { _ = os.Chmod(cfgDir, 0o755) })
			dst := filepath.Join(cfgDir, "sessions")
			expectPeerDirStandDown(func() {}, dst, "creating it: mkdir "+dst+": permission denied")
		})

		It("CS-LNCH-107: a symlinked peers dir is refused and its target is not re-moded", func() {
			target := filepath.Join(home, "elsewhere")
			Expect(os.MkdirAll(target, 0o755)).To(Succeed())
			Expect(os.Chmod(target, 0o755)).To(Succeed())
			mkdir(filepath.Dir(root))
			Expect(os.Symlink(target, root)).To(Succeed())
			expectPeerDirStandDown(func() {}, root, "it is a symlink")
			fi, err := os.Stat(target)
			Expect(err).NotTo(HaveOccurred())
			Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o755)))
			// Nothing was created through the link either.
			Expect(filepath.Join(target, "sessions")).NotTo(BeADirectory())
		})

		It("CS-LNCH-107: a symlinked sessions/ is refused and its target is not re-moded", func() {
			target := filepath.Join(home, "elsewhere-sessions")
			Expect(os.MkdirAll(target, 0o755)).To(Succeed())
			Expect(os.Chmod(target, 0o755)).To(Succeed())
			mkdir(root)
			link := filepath.Join(root, "sessions")
			Expect(os.Symlink(target, link)).To(Succeed())
			expectPeerDirStandDown(func() {}, link, "it is a symlink")
			fi, err := os.Stat(target)
			Expect(err).NotTo(HaveOccurred())
			Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o755)))
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

		Describe("CS-LNCH-165: inside a sandbox", func() {
			var reads int
			BeforeEach(func() {
				env["CLAUDE_SANDBOX_PROJECT_DIR"] = "/outer/proj"
				reads = 0
				// Only the container's own root filesystem: nothing is bound.
				in.MountInfo = func() (string, error) {
					reads++
					return "1 0 0:1 / / rw - overlay overlay rw\n", nil
				}
			})

			It("CS-LNCH-165: an outer sandbox that is bridged (XDG_RUNTIME_DIR is the peers root) bridges this one", func() {
				env["XDG_RUNTIME_DIR"] = root + "/"
				enable()
				p := build()
				Expect(p.Volumes).To(ContainElement(rootMount()))
				Expect(envValues(p, "XDG_RUNTIME_DIR")).To(Equal([]string{root}))
				Expect(out.String()).NotTo(ContainSubstring("Warning: sharedPeerRegistry"))
				Expect(reads).To(Equal(0), "the env proof needs no mountinfo")
			})

			It("CS-LNCH-165: a peers root mountinfo shows bound in bridges this one", func() {
				in.MountInfo = func() (string, error) {
					return "1 0 0:1 / / rw - overlay overlay rw\n2 1 0:2 /peers " + root + " rw - ext4 /dev/sda1 rw\n", nil
				}
				enable()
				p := build()
				Expect(p.Volumes).To(ContainElement(rootMount()))
				Expect(out.String()).NotTo(ContainSubstring("Warning: sharedPeerRegistry"))
			})

			DescribeTable("CS-LNCH-165: otherwise the whole bridge stands down, touching nothing",
				func(xdg string, mi func() (string, error), reason string) {
					if xdg != "" {
						env["XDG_RUNTIME_DIR"] = xdg
					}
					off := build()
					out.Reset()
					if mi != nil {
						in.MountInfo = mi
					}
					enable()
					p := build()
					expectStoodDown(p, root, "inside a sandbox that does not mount "+root+" ("+reason+")")
					Expect(out.String()).To(ContainSubstring("Launch the outer sandbox with sharedPeerRegistry on"))
					Expect(out.String()).To(ContainSubstring("CLAUDE_SANDBOX_SHARED_PEER_REGISTRY=0"))
					Expect(filepath.Join(cfgDir, "sessions")).NotTo(BeADirectory())
					Expect(p.CreateArgs(proj)).To(Equal(off.CreateArgs(proj)))
					Expect(p.ConfigHash).To(Equal(off.ConfigHash))
				},
				Entry("on the container's root filesystem", "", nil, "it is on the container's own root filesystem"),
				Entry("XDG_RUNTIME_DIR names something else", "/run/user/1000", nil, "it is on the container's own root filesystem"),
				Entry("on a tmpfs", "", func() (string, error) {
					return "1 0 0:1 / / rw - overlay overlay rw\n2 1 0:2 / /tmp rw - tmpfs tmpfs rw\n", nil
				}, "it is on a tmpfs mounted at /tmp inside the container"),
				Entry("mountinfo unreadable", "", func() (string, error) { return "", errors.New("boom") }, "/proc/self/mountinfo is unreadable: boom"),
			)
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

	Describe("CS-LNCH-112: sandboxes are the host's preferred OOM victims", func() {
		intp := func(v int) *int { return &v }

		It("CS-LNCH-112: creates with --oom-score-adj 500 by default", func() {
			p := build()
			Expect(p.OOMScoreAdj).To(Equal(500))
			Expect(argPairs(p.CreateArgs(proj), "--oom-score-adj")).To(Equal([]string{"500"}))
		})

		It("CS-LNCH-112: the oomScoreAdj key overrides the default, an explicit 0 included", func() {
			in.Cfg = &cascade.Config{OOMScoreAdj: intp(800)}
			Expect(argPairs(build().CreateArgs(proj), "--oom-score-adj")).To(Equal([]string{"800"}))
			in.Cfg = &cascade.Config{OOMScoreAdj: intp(0)}
			Expect(argPairs(build().CreateArgs(proj), "--oom-score-adj")).To(Equal([]string{"0"}))
		})

		It("CS-LNCH-112: CLAUDE_SANDBOX_OOM_SCORE_ADJ beats the key; empty falls through", func() {
			in.Cfg = &cascade.Config{OOMScoreAdj: intp(800)}
			env["CLAUDE_SANDBOX_OOM_SCORE_ADJ"] = " 0 "
			Expect(argPairs(build().CreateArgs(proj), "--oom-score-adj")).To(Equal([]string{"0"}))
			env["CLAUDE_SANDBOX_OOM_SCORE_ADJ"] = ""
			Expect(argPairs(build().CreateArgs(proj), "--oom-score-adj")).To(Equal([]string{"800"}))
		})

		DescribeTable("CS-LNCH-112: a value outside docker's range, or not an integer, fails the launch naming its source",
			func(envVal string, key *int, want string) {
				if envVal != "" {
					env["CLAUDE_SANDBOX_OOM_SCORE_ADJ"] = envVal
				}
				in.Cfg = &cascade.Config{OOMScoreAdj: key}
				_, err := launch.Build(in)
				Expect(err).To(MatchError(ContainSubstring(want)))
				Expect(err).To(MatchError(ContainSubstring("[-1000, 1000]")))
			},
			Entry("env not a number", "high", nil, "CLAUDE_SANDBOX_OOM_SCORE_ADJ"),
			Entry("env above the range", "1001", nil, "CLAUDE_SANDBOX_OOM_SCORE_ADJ"),
			Entry("key below the range", "", intp(-1001), "oomScoreAdj"),
		)

		It("CS-LNCH-112: an invalid key names the cascade file that set it", func() {
			in.Cfg = &cascade.Config{OOMScoreAdj: intp(5000)}
			in.OOMScoreAdjSource = "/ws/.claude-sandbox/config.yaml"
			_, err := launch.Build(in)
			Expect(err).To(MatchError(ContainSubstring("oomScoreAdj: 5000 in /ws/.claude-sandbox/config.yaml")))
		})

		It("CS-LNCH-112: the range ends are accepted", func() {
			env["CLAUDE_SANDBOX_OOM_SCORE_ADJ"] = "-1000"
			Expect(build().OOMScoreAdj).To(Equal(-1000))
			env["CLAUDE_SANDBOX_OOM_SCORE_ADJ"] = "1000"
			Expect(build().OOMScoreAdj).To(Equal(1000))
		})

		It("CS-LNCH-112: the applied value is in the config hash; unset and an explicit 500 hash alike", func() {
			def := build().ConfigHash
			in.Cfg = &cascade.Config{OOMScoreAdj: intp(500)}
			Expect(build().ConfigHash).To(Equal(def))
			in.Cfg = &cascade.Config{OOMScoreAdj: intp(0)}
			Expect(build().ConfigHash).NotTo(Equal(def))
			in.Cfg = &cascade.Config{}
			env["CLAUDE_SANDBOX_OOM_SCORE_ADJ"] = "900"
			Expect(build().ConfigHash).NotTo(Equal(def))
		})
	})

	It("CS-LNCH-093: records the memory limit and its source as labels and env, outside the config hash", func() {
		p := build()
		Expect(p.MemoryLimitSource).To(Equal("default"))
		Expect(p.Labels).To(ContainElements("claude-sandbox.memorylimit=8g", "claude-sandbox.memorylimitsource=default"))
		Expect(p.EnvFlags).To(ContainElements("CLAUDE_SANDBOX_MEMORY_LIMIT=8g", "CLAUDE_SANDBOX_MEMORY_LIMIT_SOURCE=default"))

		in.Cfg = &cascade.Config{MemoryLimit: "16g"}
		in.MemoryLimitSource = "/ws/.claude-sandbox/config.yaml"
		upstream := build()
		Expect(upstream.Labels).To(ContainElements("claude-sandbox.memorylimit=16g",
			"claude-sandbox.memorylimitsource=/ws/.claude-sandbox/config.yaml"))
		Expect(upstream.EnvFlags).To(ContainElements("CLAUDE_SANDBOX_MEMORY_LIMIT=16g",
			"CLAUDE_SANDBOX_MEMORY_LIMIT_SOURCE=/ws/.claude-sandbox/config.yaml"))
		Expect(upstream.CreateArgs(proj)).To(ContainElements("--label", "claude-sandbox.memorylimit=16g"))

		// The same limit written at another cascade level launches the same
		// container: not drift.
		in.MemoryLimitSource = "/ws/p/.claude-sandbox/config.yaml"
		Expect(build().ConfigHash).To(Equal(upstream.ConfigHash))
		// A different limit is.
		in.Cfg = &cascade.Config{MemoryLimit: "32g"}
		Expect(build().ConfigHash).NotTo(Equal(upstream.ConfigHash))
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

	It("CS-LNCH-029, CS-LNCH-057: create leads with -it --rm --init; start is -ai with the keys and the name", func() {
		p := build()
		args := p.CreateArgs(proj)
		Expect(args[0:4]).To(Equal([]string{"create", "-it", "--rm", "--init"}))
		Expect(args).To(ContainElements("--name", p.ContainerName, p.Image))
		Expect(p.StartArgs()).To(Equal([]string{"start", "-ai", "--detach-keys=ctrl-q,ctrl-q", p.ContainerName}))
	})

	Describe("headless (CS-LNCH-059, CS-LNCH-063, CS-LNCH-064)", func() {
		BeforeEach(func() { in.Headless = true })

		It("CS-LNCH-059: create has -i without -t; start has no detach keys, even when configured", func() {
			in.Cfg = &cascade.Config{DetachKeys: "ctrl-^"}
			p := build()
			args := p.CreateArgs(proj)
			Expect(args[0:4]).To(Equal([]string{"create", "-i", "--rm", "--init"}))
			Expect(args).NotTo(ContainElement("-it"))
			Expect(args).NotTo(ContainElement("-t"))
			Expect(p.StartArgs()).To(Equal([]string{"start", "-ai", p.ContainerName}))
		})

		It("CS-LNCH-064: labels the container mode=headless, with its noun and class", func() {
			in.Instance, in.PIDClass = "otter", "12"
			p := build()
			Expect(p.Labels).To(ContainElement("claude-sandbox.mode=headless"))
			Expect(p.Labels).NotTo(ContainElement("claude-sandbox.mode=claude"))
			Expect(p.Labels).To(ContainElements("claude-sandbox.instance=otter", "claude-sandbox.pidclass=12"))
		})

		It("CS-LNCH-063: forwards exactly the allowlisted names that are set, as bare -e NAME", func() {
			set := map[string]string{
				"CLAUDE_CODE_ENTRYPOINT":                      "sdk-ts",
				"CLAUDE_AGENT_SDK_MCP_NO_PREFIX":              "", // set but empty still counts as set
				"PASEO_AGENT_ID":                              "x",
				"PASEO_PASSWORD":                              "hunter2", // never forwarded
				"PASEO_HOME":                                  "/p",
				"CLAUDE_AGENT_SDK_SOMETHING_UNLISTED":         "1",
				"CLAUDE_CODE_ENABLE_SDK_FILE_CHECKPOINTING_X": "1",
			}
			in.LookupEnv = func(k string) (string, bool) { v, ok := set[k]; return v, ok }
			args := build().CreateArgs(proj)
			es := argPairs(args, "-e")
			Expect(es).To(ContainElements("CLAUDE_CODE_ENTRYPOINT", "CLAUDE_AGENT_SDK_MCP_NO_PREFIX", "PASEO_AGENT_ID"))
			for _, e := range es {
				Expect(e).NotTo(ContainSubstring("PASEO_PASSWORD"))
				Expect(e).NotTo(ContainSubstring("hunter2"))
				Expect(e).NotTo(HavePrefix("PASEO_HOME"))
				Expect(e).NotTo(HavePrefix("CLAUDE_AGENT_SDK_SOMETHING_UNLISTED"))
				Expect(e).NotTo(HavePrefix("CLAUDE_CODE_ENABLE_SDK_FILE_CHECKPOINTING"))
				Expect(e).NotTo(Equal("CLAUDE_AGENT_SDK_VERSION"), "unset names are not forwarded")
				Expect(e).NotTo(Equal("PASEO_AGENT_CWD"), "unset names are not forwarded")
				Expect(e).NotTo(Equal("CLAUDE_CODE_ENTRYPOINT=sdk-ts"), "values stay out of argv")
			}
		})

		It("CS-LNCH-063: every allowlisted name is forwarded when set, and the list is exact", func() {
			Expect(launch.HeadlessEnv).To(ConsistOf(
				"CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_ENABLE_SDK_FILE_CHECKPOINTING",
				"CLAUDE_AGENT_SDK_VERSION", "CLAUDE_AGENT_SDK_CLIENT_APP",
				"CLAUDE_AGENT_SDK_DISABLE_BUILTIN_AGENTS", "CLAUDE_AGENT_SDK_MCP_NO_PREFIX",
				"PASEO_AGENT_ID", "PASEO_AGENT_CWD"))
			in.LookupEnv = func(k string) (string, bool) { return "v", true }
			es := argPairs(build().CreateArgs(proj), "-e")
			Expect(es).To(ContainElements(launch.HeadlessEnv))
		})

		It("CS-LNCH-063: without LookupEnv, a name counts as set when Getenv is non-empty", func() {
			env["PASEO_AGENT_CWD"] = proj
			es := argPairs(build().CreateArgs(proj), "-e")
			Expect(es).To(ContainElement("PASEO_AGENT_CWD"))
			Expect(es).NotTo(ContainElement("PASEO_AGENT_ID"))
		})

		It("CS-LNCH-063: an interactive launch forwards none of them", func() {
			in.Headless = false
			in.LookupEnv = func(k string) (string, bool) { return "v", true }
			es := argPairs(build().CreateArgs(proj), "-e")
			for _, k := range launch.HeadlessEnv {
				Expect(es).NotTo(ContainElement(k))
			}
		})
	})

	Describe("CS-LNCH-057: reserve, then start", func() {
		It("reserves with docker create; the session child is docker start -ai", func() {
			fake := &execx.Fake{}
			p := build()
			Expect(p.Reserve(fake, proj, errw)).To(Succeed())
			Expect(fake.Calls).To(HaveLen(1))
			Expect(fake.Calls[0].Args).To(Equal(p.CreateArgs(proj)))
			// CS-LNCH-085: a Cmd for the launcher to run as a child, not an exec.
			Expect(p.StartCmd()).To(Equal(execx.Cmd{Name: "docker", Args: p.StartArgs()}))
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
		in.LookupEnv = func(k string) (string, bool) {
			if k == "ANTHROPIC_API_KEY" {
				return "", true
			}
			return "", false
		}
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
			"ANTHROPIC_API_KEY",
		))
		// Every env flag is rendered as "-e <flag>" in the argv.
		for _, e := range p.EnvFlags {
			Expect(args).To(ContainElement(e))
		}
	})

	Describe("ANTHROPIC_API_KEY forwarding (CS-LNCH-102)", func() {
		const secret = "sk-ant-sentinel-value"
		var lookup map[string]string
		BeforeEach(func() {
			lookup = map[string]string{}
			in.LookupEnv = func(k string) (string, bool) { v, ok := lookup[k]; return v, ok }
		})

		It("CS-LNCH-102: a set key is passed as a bare -e NAME and its value never reaches argv", func() {
			env["ANTHROPIC_API_KEY"] = secret
			lookup["ANTHROPIC_API_KEY"] = secret
			p := build()
			args := p.CreateArgs(proj)
			Expect(p.EnvFlags).To(ContainElement("ANTHROPIC_API_KEY"))
			idx := -1
			for i, a := range args {
				Expect(a).NotTo(ContainSubstring(secret))
				if a == "ANTHROPIC_API_KEY" {
					idx = i
				}
			}
			Expect(idx).To(BeNumerically(">", 0))
			Expect(args[idx-1]).To(Equal("-e"))
		})

		It("CS-LNCH-102: a set-but-empty key is also passed by name", func() {
			lookup["ANTHROPIC_API_KEY"] = ""
			p := build()
			Expect(p.EnvFlags).To(ContainElement("ANTHROPIC_API_KEY"))
			Expect(p.EnvFlags).NotTo(ContainElement("ANTHROPIC_API_KEY="))
		})

		It("CS-LNCH-102, CS-LNCH-106: an unset key gets no -e in any form, so an env-file value applies", func() {
			envFile := filepath.Join(proj, ".claude-sandbox", "env")
			Expect(os.MkdirAll(filepath.Dir(envFile), 0o755)).To(Succeed())
			Expect(os.WriteFile(envFile, []byte("ANTHROPIC_API_KEY="+secret+"\n"), 0o600)).To(Succeed())
			in.EnvFiles = []string{envFile}
			p := build()
			for _, e := range p.EnvFlags {
				Expect(e).NotTo(HavePrefix("ANTHROPIC_API_KEY"))
			}
			args := p.CreateArgs(proj)
			for _, a := range args {
				Expect(a).NotTo(HavePrefix("ANTHROPIC_API_KEY"))
				Expect(a).NotTo(ContainSubstring(secret))
			}
			Expect(envFileContents(args)).To(Equal([]string{"ANTHROPIC_API_KEY=" + secret + "\n"}))
		})

		It("CS-LNCH-106: a set key still outranks the env file with a bare -e NAME", func() {
			lookup["ANTHROPIC_API_KEY"] = ""
			envFile := filepath.Join(proj, ".claude-sandbox", "env")
			Expect(os.MkdirAll(filepath.Dir(envFile), 0o755)).To(Succeed())
			Expect(os.WriteFile(envFile, []byte("ANTHROPIC_API_KEY="+secret+"\n"), 0o600)).To(Succeed())
			in.EnvFiles = []string{envFile}
			p := build()
			Expect(p.EnvFlags).To(ContainElement("ANTHROPIC_API_KEY"))
			Expect(envFileContents(p.CreateArgs(proj))).To(Equal([]string{"ANTHROPIC_API_KEY=" + secret + "\n"}))
		})
	})

	It("CS-LNCH-104: forwarded credential values do not change the drift fingerprint", func() {
		t := true
		in.CLIAWS = &t
		mkdir(filepath.Join(home, ".aws"))
		set := func(v string) {
			env["ANTHROPIC_API_KEY"] = "sk-" + v
			env["AWS_ACCESS_KEY_ID"] = "AKIA" + v
			env["AWS_SECRET_ACCESS_KEY"] = "secret-" + v
			env["AWS_SESSION_TOKEN"] = "token-" + v
		}
		set("one")
		first := build()
		set("two")
		second := build()
		Expect(second.ConfigHash).To(Equal(first.ConfigHash))
		for _, l := range append(first.Labels, second.Labels...) {
			Expect(l).NotTo(ContainSubstring("secret-"))
			Expect(l).NotTo(ContainSubstring("token-"))
		}
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

	DescribeTable("CS-LNCH-108: the CLAUDE_CODE_TMPDIR stand-down reads env files as docker does",
		func(content string, hostSet, standsDown bool) {
			cfgDir := filepath.Join(home, ".claude")
			mkdir(cfgDir)
			ef := filepath.Join(home, "envfile")
			touch(ef, content)
			in.EnvFiles = []string{ef}
			if hostSet {
				// Set-but-empty: Getenv reads "", so no host forward (CS-LNCH-034),
				// yet docker passes the bare key through.
				in.LookupEnv = func(k string) (string, bool) {
					if k == "CLAUDE_CODE_TMPDIR" {
						return "", true
					}
					v, ok := env[k]
					return v, ok
				}
			}
			var got []string
			for _, e := range build().EnvFlags {
				if strings.HasPrefix(e, "CLAUDE_CODE_TMPDIR") {
					got = append(got, e)
				}
			}
			if standsDown {
				Expect(got).To(BeEmpty())
			} else {
				Expect(got).To(Equal([]string{"CLAUDE_CODE_TMPDIR=" + filepath.Join(cfgDir, "tmp")}))
			}
		},
		Entry("indented", "  CLAUDE_CODE_TMPDIR=/somewhere\n", false, true),
		Entry("BOM-prefixed", "\xEF\xBB\xBFCLAUDE_CODE_TMPDIR=/somewhere\n", false, true),
		Entry("CRLF", "CLAUDE_CODE_TMPDIR=/somewhere\r\n", false, true),
		Entry("bare key the host sets", "CLAUDE_CODE_TMPDIR\n", true, true),
		Entry("bare key the host does not set", "CLAUDE_CODE_TMPDIR\n", false, false),
		Entry("a key docker rejects", "CLAUDE_CODE_TMPDIR =/somewhere\n", false, false),
	)

	It("renders env files as stacked --env-file flags in cascade order", func() {
		root := filepath.Join(home, "root-env")
		local := filepath.Join(proj, "proj-env")
		touch(root, "A=root\n")
		touch(local, "A=proj\n")
		in.EnvFiles = []string{root, local}
		args := build().CreateArgs(proj)
		Expect(envFileContents(args)).To(Equal([]string{"A=root\n", "A=proj\n"}))
	})

	Describe("CS-LNCH-132: docker gets the checked bytes, never a re-read", func() {
		It("passes 0600 copies of the snapshot from the shadow directory, not the originals", func() {
			ef := filepath.Join(proj, "env")
			touch(ef, "TOKEN=checked\n")
			in.EnvFiles = []string{ef}
			in.Env = []cascade.EnvFile{{Path: ef, Content: []byte("TOKEN=checked\n")}}
			// The session rewrites the file between the check and the create.
			touch(ef, "LD_PRELOAD=/p/evil.so\n")
			p := build()
			args := p.CreateArgs(proj)
			Expect(envFileContents(args)).To(Equal([]string{"TOKEN=checked\n"}))
			Expect(args).NotTo(ContainElement(ef))
			Expect(p.EnvFiles).To(HaveLen(1))
			Expect(p.EnvFiles[0]).To(HavePrefix(in.TempDir + "/"))
			st, err := os.Stat(p.EnvFiles[0])
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		})

		It("hashes the snapshot under the original path, so a later rewrite is not in the fingerprint", func() {
			ef := filepath.Join(proj, "env")
			touch(ef, "TOKEN=checked\n")
			in.EnvFiles = []string{ef}
			plain := build() // snapshots the file itself
			in.Env = []cascade.EnvFile{{Path: ef, Content: []byte("TOKEN=checked\n")}}
			touch(ef, "TOKEN=rewritten\n")
			snap := build()
			Expect(snap.ConfigHash).To(Equal(plain.ConfigHash))
			var envDigests []launch.InputDigest
			for _, d := range snap.ConfigInputs {
				if d.Kind == launch.KindEnv {
					envDigests = append(envDigests, d)
				}
			}
			Expect(envDigests).To(HaveLen(1))
			Expect(envDigests[0].Path).To(Equal(ef))
		})

		It("the stand-downs read the snapshot (CS-LNCH-108), not the path", func() {
			ef := filepath.Join(proj, "env")
			touch(ef, "TOKEN=x\n")
			in.EnvFiles = []string{ef}
			in.Env = []cascade.EnvFile{{Path: ef, Content: []byte("CLAUDE_CODE_TMPDIR=/from-snapshot\n")}}
			p := build()
			for _, e := range p.EnvFlags {
				Expect(e).NotTo(HavePrefix("CLAUDE_CODE_TMPDIR="), "the snapshot defines it, so no -e is added")
			}
		})

		It("with no snapshot, Build reads each file once and fails on an unreadable one", func() {
			in.EnvFiles = []string{filepath.Join(proj, "missing-env")}
			_, err := launch.Build(in)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("reading env file"))
		})
	})
})

// envFileContents reads each --env-file copy of a create argv (CS-LNCH-132).
func envFileContents(args []string) []string {
	var out []string
	for i, a := range args {
		if a == "--env-file" && i+1 < len(args) {
			raw, err := os.ReadFile(args[i+1])
			Expect(err).NotTo(HaveOccurred())
			out = append(out, string(raw))
		}
	}
	return out
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}
