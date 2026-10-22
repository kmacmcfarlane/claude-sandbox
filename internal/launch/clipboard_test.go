package launch_test

// Spec: spec/launch.feature CS-LNCH-194..197, 200, 201 (the host clipboard
// bridge, launcher side). Paths are built under a short fixed root in /tmp:
// the bridge socket's length is checked (CS-LNCH-196).

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/clipboard"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
)

var _ = Describe("the host clipboard bridge (launcher side)", func() {
	var (
		base, proj, home string
		env              map[string]string
		out, errw        *bytes.Buffer
	)

	BeforeEach(func() {
		d, err := os.MkdirTemp("/tmp", "cbl")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(os.RemoveAll, d)
		base = d
		proj, home = filepath.Join(base, "proj"), filepath.Join(base, "home")
		mkdir(proj)
		mkdir(home)
		env = map[string]string{}
		out, errw = &bytes.Buffer{}, &bytes.Buffer{}
	})

	inputs := func() launch.Inputs {
		sh, err := os.MkdirTemp(base, "claude-sandbox")
		Expect(err).NotTo(HaveOccurred())
		return launch.Inputs{
			ProjectDir: proj, Home: home, HostUID: 1000, HostGID: 1000, HostUser: "t",
			Getenv: func(k string) string { return env[k] }, TempDir: sh,
			Instance: "otter", ImageName: "img", Cfg: &cascade.Config{},
			Out: out, Err: errw, GOOS: "linux",
		}
	}
	build := func(in launch.Inputs) (*launch.Plan, []string) {
		p, err := launch.Build(in)
		Expect(err).NotTo(HaveOccurred())
		return p, p.CreateArgs(proj)
	}

	It("CS-LNCH-194: wanted by default, tri-state, never for headless or ralph", func() {
		t, f := true, false
		get := func(k string) string { return env[k] }
		Expect(launch.ClipboardWanted(get, &cascade.Config{}, false, false)).To(BeTrue())
		Expect(launch.ClipboardWanted(get, nil, false, false)).To(BeTrue())
		Expect(launch.ClipboardWanted(get, &cascade.Config{HostClipboard: &f}, false, false)).To(BeFalse())
		Expect(launch.ClipboardWanted(get, &cascade.Config{}, true, false)).To(BeFalse(), "headless")
		Expect(launch.ClipboardWanted(get, &cascade.Config{HostClipboard: &t}, false, true)).To(BeFalse(), "ralph")
		for _, v := range []string{"0", "false", "no"} {
			env["CLAUDE_SANDBOX_HOST_CLIPBOARD"] = v
			Expect(launch.ClipboardWanted(get, &cascade.Config{HostClipboard: &t}, false, false)).To(BeFalse(), v)
		}
		env["CLAUDE_SANDBOX_HOST_CLIPBOARD"] = "1"
		Expect(launch.ClipboardWanted(get, &cascade.Config{HostClipboard: &f}, false, false)).To(BeTrue())
		delete(env, "CLAUDE_SANDBOX_HOST_CLIPBOARD")
	})

	It("CS-LNCH-194: off gives today's argv plus the clipboard=none label, and no hash line", func() {
		var texts []string
		DeferCleanup(launch.CaptureFingerprintText(func(s string) { texts = append(texts, s) }))
		p, args := build(inputs())
		Expect(p.ClipboardDir).To(BeEmpty())
		Expect(args).NotTo(ContainElement("--mount"))
		Expect(strings.Join(args, " ")).To(ContainSubstring("--label claude-sandbox.clipboard=none"))
		Expect(strings.Join(args, " ")).NotTo(ContainSubstring("WAYLAND_DISPLAY"))
		Expect(strings.Join(texts, "")).NotTo(ContainSubstring("hostClipboard"))
		Expect(out.String()).NotTo(ContainSubstring("Host clipboard"))
	})

	It("CS-LNCH-195: applied: a 0700 directory, a read-only --mount, the bridge label, the banner, and no -e", func() {
		in := inputs()
		in.ClipboardWanted = true
		res := clipboard.Resolution{Target: &clipboard.Target{V: 1, Backend: "x", Display: "localhost:10.0"}}
		in.ClipboardTarget = &res
		p, args := build(in)
		dir := filepath.Join(in.TempDir, "clipboard")
		Expect(p.ClipboardDir).To(Equal(dir))
		fi, err := os.Stat(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o700)))
		i := slices.Index(args, "--mount")
		Expect(i).To(BeNumerically(">", 0))
		Expect(args[i+1]).To(Equal("type=bind,source=" + dir + ",target=/run/claude-sandbox/clipboard,readonly"))
		Expect(args[i-2]).To(Equal("-v"), "after the -v list")
		line := strings.Join(args, " ")
		Expect(line).To(ContainSubstring("--label claude-sandbox.clipboard=bridge"))
		Expect(line).NotTo(ContainSubstring("WAYLAND_DISPLAY"), "pidslot sets it on claude only")
		Expect(line).NotTo(ContainSubstring("CLAUDE_SANDBOX_CLIPBOARD"))
		Expect(out.String()).To(ContainSubstring("Host clipboard: write-only bridge (target: X localhost:10.0; sessions can overwrite your clipboard, never read it)."))
		Expect(errw.String()).To(BeEmpty(), "no display is not a warning either")

		out.Reset()
		in2 := inputs()
		in2.ClipboardWanted = true
		in2.ClipboardTarget = &clipboard.Resolution{Hint: "install xclip on the host"}
		build(in2)
		Expect(out.String()).To(ContainSubstring("(target: none: OSC 52 only (install xclip on the host);"))
	})

	It("CS-LNCH-196: structural stand-downs warn once and give the key-off argv", func() {
		standsDown := func(in launch.Inputs, want string) {
			errw.Reset()
			in.ClipboardWanted = true
			p, args := build(in)
			Expect(p.ClipboardDir).To(BeEmpty(), want)
			Expect(args).NotTo(ContainElement("--mount"))
			Expect(strings.Join(args, " ")).To(ContainSubstring("claude-sandbox.clipboard=none"))
			Expect(errw.String()).To(ContainSubstring(want))
			Expect(errw.String()).To(HaveSuffix("this session has no clipboard bridge, so copies reach only terminals that take OSC 52.\n"))
		}
		in := inputs()
		in.GOOS = "darwin"
		standsDown(in, "Note: hostClipboard: the clipboard bridge is not supported on macOS yet")

		for _, k := range []string{"WAYLAND_DISPLAY", "CLAUDE_SANDBOX_CLIPBOARD"} {
			in = inputs()
			in.Env = []cascade.EnvFile{{Path: "/p/env", Content: []byte(k + "=x\n")}}
			standsDown(in, "WARNING: hostClipboard: an env file defines "+k)
		}

		for _, c := range []string{"/run/claude-sandbox", "/run/claude-sandbox/clipboard/x"} {
			in = inputs()
			in.Cfg = &cascade.Config{Mounts: []cascade.Mount{{Host: base, Container: c}}}
			standsDown(in, "a cascade mount covers /run/claude-sandbox/clipboard")
		}

		in = inputs()
		in.TempDir = filepath.Join(proj, "claude-sandbox9")
		mkdir(in.TempDir)
		standsDown(in, "lies under a path mounted into the container")

		// L7: a mount source that is a symlink to the shadow's parent.
		link := filepath.Join(home, "via-link")
		Expect(os.Symlink(base, link)).To(Succeed())
		in = inputs()
		in.Cfg = &cascade.Config{Mounts: []cascade.Mount{{Host: link, Container: "/data"}}}
		standsDown(in, "lies under a path mounted into the container")
		Expect(launch.UnderAnySource(filepath.Join(link, "x"), []string{base})).To(BeTrue(), "the path's resolved form too")

		in = inputs()
		long := filepath.Join(base, strings.Repeat("d", 100))
		mkdir(long)
		in.TempDir = long
		standsDown(in, "is longer than 107 bytes")

		in = inputs()
		Expect(os.WriteFile(filepath.Join(in.TempDir, "clipboard"), nil, 0o600)).To(Succeed())
		standsDown(in, "cannot create")

		errw.Reset()
		in = inputs()
		in.ClipboardWanted, in.ClipboardQuiet, in.GOOS = true, true, "darwin"
		build(in)
		Expect(errw.String()).To(BeEmpty(), "silent on a restore resume")
	})

	It("CS-LNCH-200: nested, the bridge chains to the launcher's own CLAUDE_SANDBOX_CLIPBOARD, else stands down with a Note", func() {
		env["CLAUDE_SANDBOX_PROJECT_DIR"] = "/outer"
		in := inputs()
		in.ClipboardWanted = true
		in.MountInfo = func() (string, error) { return "", nil }
		p, _ := build(in)
		Expect(p.ClipboardDir).To(BeEmpty())
		Expect(errw.String()).To(ContainSubstring("Note: hostClipboard: this launcher runs inside a sandbox with no clipboard bridge of its own"))

		env["CLAUDE_SANDBOX_CLIPBOARD"] = "/run/claude-sandbox/clipboard/bridge.sock"
		in = inputs()
		in.ClipboardWanted = true
		in.MountInfo = func() (string, error) { return "", nil }
		in.TempDir = filepath.Join(proj, "claude-sandbox8") // the config-dir mount by design
		mkdir(in.TempDir)
		p, _ = build(in)
		Expect(p.ClipboardDir).To(Equal(filepath.Join(in.TempDir, "clipboard")))
		Expect(p.ClipboardOuter).To(Equal("/run/claude-sandbox/clipboard/bridge.sock"))
	})

	It("CS-LNCH-201: the wanted value is hashed; the mount and the target are not", func() {
		var texts []string
		DeferCleanup(launch.CaptureFingerprintText(func(s string) { texts = append(texts, s) }))
		in := inputs()
		in.ClipboardWanted = true
		in.ClipboardTarget = &clipboard.Resolution{Target: &clipboard.Target{V: 1, Backend: "x", Display: ":0"}}
		a, _ := build(in)
		Expect(texts[len(texts)-1]).To(ContainSubstring("hostClipboard=on\n"))
		Expect(texts[len(texts)-1]).NotTo(ContainSubstring("clipboard,"))
		in2 := inputs()
		in2.ClipboardWanted = true
		in2.ClipboardTarget = &clipboard.Resolution{Target: &clipboard.Target{V: 1, Backend: "x", Display: "localhost:11"}}
		b, _ := build(in2)
		Expect(b.ConfigHash).To(Equal(a.ConfigHash), "another shadow dir and another target hash alike")
		in3 := inputs()
		in3.ClipboardWanted, in3.GOOS = true, "darwin"
		c, _ := build(in3)
		Expect(c.ConfigHash).To(Equal(a.ConfigHash), "a stood-down launch hashes like an applied one")
		d, _ := build(inputs())
		Expect(d.ConfigHash).NotTo(Equal(a.ConfigHash))
	})
})
