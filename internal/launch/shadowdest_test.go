package launch_test

// Spec: spec/launch.feature (CS-LNCH-169..171) — a missing shadow destination
// under a read-write same-path mount is created as the invoking user before
// docker could create it on the host as root, or its mount is left out.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	assets "github.com/kmacmcfarlane/claude-sandbox"
	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
)

var _ = Describe("shadow destinations under a read-write same-path mount (CS-LNCH-169..171)", func() {
	var (
		base, home, proj, ws, cfgDir string
		env                          map[string]string
		in                           launch.Inputs
		out, errw                    *bytes.Buffer
		shadowN                      int
	)

	// The reported incident: CLAUDE_CONFIG_DIR=<ws>/.claude and a cascade
	// mount of <ws>, neither <ws>/.mcp.json nor <ws>/.claude/CLAUDE.md present.
	BeforeEach(func() {
		var err error
		base, err = filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		home, proj, ws = filepath.Join(base, "home"), filepath.Join(base, "proj"), filepath.Join(base, "ws")
		cfgDir = filepath.Join(ws, ".claude")
		for _, d := range []string{home, proj, cfgDir} {
			mkdir(d)
		}
		env = map[string]string{"CLAUDE_CONFIG_DIR": cfgDir}
		out, errw = &bytes.Buffer{}, &bytes.Buffer{}
		shadowN = 0
		in = launch.Inputs{
			ProjectDir: proj, Home: home, HostUID: 1000, HostGID: 1000, HostUser: "tester",
			Getenv:    func(k string) string { return env[k] },
			ImageName: "img", Out: out, Err: errw,
			Cfg: &cascade.Config{Mounts: []cascade.Mount{{Host: ws, Container: ws, Writable: true}}},
		}
	})

	build := func() *launch.Plan {
		shadowN++
		in.TempDir = filepath.Join(base, "shadow", strings.Repeat("s", shadowN))
		mkdir(in.TempDir)
		p, err := launch.Build(in)
		Expect(err).NotTo(HaveOccurred())
		return p
	}
	shadowSpec := func(name, dest string) string {
		return filepath.Join(in.TempDir, name) + ":" + dest + ":ro"
	}
	expectPlaceholder := func(p string) {
		fi, err := os.Lstat(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().IsRegular()).To(BeTrue())
		Expect(fi.Size()).To(BeZero())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		Expect(int(fi.Sys().(*syscall.Stat_t).Uid)).To(Equal(os.Getuid()))
	}

	Describe("CS-LNCH-169: created as the invoking user", func() {
		It("CS-LNCH-169: creates the missing .mcp.json and CLAUDE.md placeholders and keeps both shadow mounts", func() {
			p := build()
			mcp, md := filepath.Join(ws, ".mcp.json"), filepath.Join(cfgDir, "CLAUDE.md")
			expectPlaceholder(mcp)
			expectPlaceholder(md)
			Expect(p.Volumes).To(ContainElement(shadowSpec(".mcp.json", mcp)))
			Expect(p.Volumes).To(ContainElement(shadowSpec("CLAUDE.md", md)))
			Expect(errw.String()).To(BeEmpty())
			Expect(out.String()).NotTo(ContainSubstring("CLAUDE.md"))
		})

		It("CS-LNCH-169: an empty host CLAUDE.md counts as missing, so the content and the hash are stable", func() {
			first := build()
			raw, err := os.ReadFile(filepath.Join(in.TempDir, "CLAUDE.md"))
			Expect(err).NotTo(HaveOccurred())
			Expect(raw).To(Equal(assets.ContainerContext))
			second := build()
			raw, err = os.ReadFile(filepath.Join(in.TempDir, "CLAUDE.md"))
			Expect(err).NotTo(HaveOccurred())
			Expect(raw).To(Equal(assets.ContainerContext))
			Expect(second.ConfigHash).To(Equal(first.ConfigHash))
		})

		It("CS-LNCH-169: an existing destination is left as it is, a dangling symlink included", func() {
			mcp := filepath.Join(ws, ".mcp.json")
			Expect(os.Symlink(filepath.Join(base, "nowhere"), mcp)).To(Succeed())
			md := filepath.Join(cfgDir, "CLAUDE.md")
			touch(md, "# mine\n")
			p := build()
			dest, err := os.Readlink(mcp)
			Expect(err).NotTo(HaveOccurred())
			Expect(dest).To(Equal(filepath.Join(base, "nowhere")))
			Expect(os.ReadFile(md)).To(Equal([]byte("# mine\n")))
			Expect(p.Volumes).To(ContainElement(shadowSpec(".mcp.json", mcp)))
			Expect(errw.String()).To(BeEmpty())
		})

		It("CS-LNCH-169: creates missing directories between the covering mount and the destination, 0700", func() {
			nested := filepath.Join(ws, "sub", "deeper", ".claude")
			env["CLAUDE_CONFIG_DIR"] = nested // absent: no config-dir mount
			p := build()
			mcp := filepath.Join(ws, "sub", "deeper", ".mcp.json")
			expectPlaceholder(mcp)
			for _, d := range []string{filepath.Join(ws, "sub"), filepath.Join(ws, "sub", "deeper")} {
				fi, err := os.Stat(d)
				Expect(err).NotTo(HaveOccurred())
				Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o700)), d)
			}
			Expect(p.Volumes).To(ContainElement(shadowSpec(".mcp.json", mcp)))
		})

		It("CS-LNCH-169: a destination under no mount is not created", func() {
			env["CLAUDE_CONFIG_DIR"] = ""
			in.Cfg = &cascade.Config{}
			p := build()
			mcp := filepath.Join(home, ".mcp.json")
			Expect(mcp).NotTo(BeAnExistingFile())
			Expect(p.Volumes).To(ContainElement(shadowSpec(".mcp.json", mcp)))
		})

		It("CS-LNCH-169: a destination whose deepest cover is read-only is not created", func() {
			in.Cfg = &cascade.Config{Mounts: []cascade.Mount{{Host: ws, Container: ws}}}
			p := build()
			mcp := filepath.Join(ws, ".mcp.json")
			Expect(mcp).NotTo(BeAnExistingFile())
			Expect(p.Volumes).To(ContainElement(shadowSpec(".mcp.json", mcp)))
		})

		It("CS-LNCH-169: a destination whose deepest cover has host != container is not created", func() {
			other := filepath.Join(base, "other")
			mkdir(other)
			in.Cfg = &cascade.Config{Mounts: []cascade.Mount{{Host: other, Container: ws, Writable: true}}}
			p := build()
			Expect(filepath.Join(ws, ".mcp.json")).NotTo(BeAnExistingFile())
			Expect(filepath.Join(other, ".mcp.json")).NotTo(BeAnExistingFile())
			Expect(p.Volumes).To(ContainElement(shadowSpec(".mcp.json", filepath.Join(ws, ".mcp.json"))))
		})

		It("CS-LNCH-169: the config dir's own mount covers CLAUDE.md in the default layout", func() {
			env["CLAUDE_CONFIG_DIR"] = ""
			in.Cfg = &cascade.Config{}
			def := filepath.Join(home, ".claude")
			mkdir(def)
			build()
			expectPlaceholder(filepath.Join(def, "CLAUDE.md"))
		})
	})

	Describe("CS-LNCH-170: cannot be created as the user", func() {
		expectSkipped := func(p *launch.Plan, dest, name string) {
			for _, v := range p.Volumes {
				Expect(v).NotTo(HaveSuffix(":" + dest + ":ro"))
			}
			for _, d := range p.ConfigInputs {
				Expect(d.Path).NotTo(Equal(name), "the left-out file's digest is dropped")
			}
			Expect(strings.Count(errw.String(), "\n")).To(Equal(1), errw.String())
			Expect(errw.String()).To(HavePrefix("WARNING: " + dest + " is not shadowed"))
			Expect(errw.String()).To(ContainSubstring(ws + ":" + ws))
			Expect(errw.String()).To(ContainSubstring("starts without"))
		}

		It("CS-LNCH-170: a parent owned by another uid leaves the mount out with one warning", func() {
			in.Getuid = func() int { return os.Getuid() + 4242 }
			touch(filepath.Join(cfgDir, "CLAUDE.md"), "x") // only .mcp.json is missing
			p := build()
			mcp := filepath.Join(ws, ".mcp.json")
			Expect(mcp).NotTo(BeAnExistingFile())
			expectSkipped(p, mcp, ".mcp.json")
			Expect(errw.String()).To(ContainSubstring("owned by uid"))
		})

		It("CS-LNCH-170: a symlink on the way leaves the mount out with one warning", func() {
			elsewhere := filepath.Join(base, "elsewhere")
			mkdir(elsewhere)
			Expect(os.Symlink(elsewhere, filepath.Join(ws, "link"))).To(Succeed())
			env["CLAUDE_CONFIG_DIR"] = filepath.Join(ws, "link", ".claude") // absent
			p := build()
			mcp := filepath.Join(ws, "link", ".mcp.json")
			Expect(filepath.Join(elsewhere, ".mcp.json")).NotTo(BeAnExistingFile())
			// CLAUDE.md is refused the same way: two warnings, one per file.
			for _, v := range p.Volumes {
				Expect(v).NotTo(HaveSuffix(":" + mcp + ":ro"))
			}
			Expect(errw.String()).To(ContainSubstring("WARNING: " + mcp + " is not shadowed"))
			Expect(errw.String()).To(ContainSubstring("is a symlink"))
		})

		It("CS-LNCH-170: an unwritable parent leaves the mount out and the launch continues", func() {
			if os.Getuid() == 0 {
				Skip("root ignores directory permissions")
			}
			touch(filepath.Join(cfgDir, "CLAUDE.md"), "x")
			Expect(os.Chmod(ws, 0o500)).To(Succeed())
			DeferCleanup(os.Chmod, ws, os.FileMode(0o755))
			p := build()
			expectSkipped(p, filepath.Join(ws, ".mcp.json"), ".mcp.json")
		})

		It("CS-LNCH-170: the fingerprint follows the applied mount set", func() {
			touch(filepath.Join(cfgDir, "CLAUDE.md"), "x")
			shadowed := build().ConfigHash
			Expect(os.Remove(filepath.Join(ws, ".mcp.json"))).To(Succeed())
			in.Getuid = func() int { return os.Getuid() + 4242 }
			Expect(build().ConfigHash).NotTo(Equal(shadowed))
		})
	})

	Describe("CS-LNCH-171: inside a sandbox", func() {
		var reads int
		BeforeEach(func() {
			env["CLAUDE_SANDBOX_PROJECT_DIR"] = "/outer/proj"
			reads = 0
			touch(filepath.Join(cfgDir, "CLAUDE.md"), "x") // only .mcp.json is missing
		})
		mountinfoOf := func(text string) {
			in.MountInfo = func() (string, error) { reads++; return text, nil }
		}

		It("CS-LNCH-171: a parent the outer sandbox bound in gets the placeholder", func() {
			mountinfoOf(withBind(ws))
			p := build()
			mcp := filepath.Join(ws, ".mcp.json")
			expectPlaceholder(mcp)
			Expect(p.Volumes).To(ContainElement(shadowSpec(".mcp.json", mcp)))
			Expect(errw.String()).To(BeEmpty())
		})

		It("CS-LNCH-171: a parent on the container's own filesystem is not created and the mount is left out", func() {
			mountinfoOf(rootOnly)
			p := build()
			mcp := filepath.Join(ws, ".mcp.json")
			Expect(mcp).NotTo(BeAnExistingFile())
			for _, v := range p.Volumes {
				Expect(v).NotTo(HaveSuffix(":" + mcp + ":ro"))
			}
			Expect(strings.Count(errw.String(), "\n")).To(Equal(1), errw.String())
			Expect(errw.String()).To(ContainSubstring("WARNING: " + mcp + " is not shadowed"))
			Expect(errw.String()).To(ContainSubstring("inside a sandbox"))
		})

		It("CS-LNCH-171: an existing destination never reads mountinfo", func() {
			touch(filepath.Join(ws, ".mcp.json"), "")
			mountinfoOf(rootOnly)
			build()
			Expect(reads).To(Equal(0))
			Expect(errw.String()).To(BeEmpty())
		})
	})
})
