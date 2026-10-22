package clipboard_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/clipboard"
)

// resolver builds a ResolveInput over a map environment. xauth names the
// files that pass the owned-regular check; sockets the Wayland sockets that
// pass the owned-socket check; tools the host tools found.
type resolver struct {
	env     map[string]string
	tmux    map[string]*string
	xauth   map[string]bool
	sockets map[string]bool
	tools   map[string]bool
}

func newResolver() *resolver {
	return &resolver{
		env:     map[string]string{},
		xauth:   map[string]bool{"/h/.Xauthority": true},
		sockets: map[string]bool{"/run/user/1000/wayland-0": true},
		tools:   map[string]bool{"xclip": true, "wl-copy": true},
	}
}

func (r *resolver) resolve() clipboard.Resolution {
	return clipboard.Resolve(clipboard.ResolveInput{
		Getenv:   func(k string) string { return r.env[k] },
		Tmux:     r.tmux,
		Hostname: "hooper.lan",
		Home:     "/h",
		LookPath: func(n string) (string, error) {
			if r.tools[n] {
				return "/usr/bin/" + n, nil
			}
			return "", errors.New("not found")
		},
		Files: clipboard.FileChecks{
			OwnedRegular: func(p string) error {
				if r.xauth[p] {
					return nil
				}
				return errors.New("no")
			},
			OwnedSocket: func(p string) error {
				if r.sockets[p] {
					return nil
				}
				return errors.New("no")
			},
		},
	})
}

func strp(s string) *string { return &s }

var _ = Describe("display target resolution (CS-LNCH-203)", func() {
	It("CS-LNCH-203: over ssh -X the forwarded display is the X target (row 3)", func() {
		r := newResolver()
		r.env["SSH_CONNECTION"] = "10.0.0.2 5 10.0.0.1 22"
		r.env["DISPLAY"] = "localhost:10.0"
		res := r.resolve()
		Expect(res.Target).NotTo(BeNil())
		Expect(*res.Target).To(Equal(clipboard.Target{V: 1, Backend: "x", Display: "localhost:10.0", XAuthority: "/h/.Xauthority"}))
		Expect(res.Target.Describe()).To(Equal("X localhost:10.0"))
		Expect(res.ShouldWrite()).To(BeTrue())
	})

	It("CS-LNCH-203: under SSH a console :0 and any WAYLAND_DISPLAY are ignored (rows 4, 5, 8)", func() {
		r := newResolver()
		r.env["SSH_CONNECTION"] = "x"
		r.env["DISPLAY"] = ":0"
		r.env["WAYLAND_DISPLAY"] = "wayland-0"
		r.env["XDG_RUNTIME_DIR"] = "/run/user/1000"
		res := r.resolve()
		Expect(res.Target).To(BeNil())
		Expect(res.ShouldWrite()).To(BeFalse(), "never downgrade")
	})

	It("CS-LNCH-203: a local X display (rows 1 and 2) is preferred over Wayland", func() {
		r := newResolver()
		r.env["DISPLAY"] = ":0"
		r.env["WAYLAND_DISPLAY"] = "wayland-0"
		r.env["XDG_RUNTIME_DIR"] = "/run/user/1000"
		r.env["XAUTHORITY"] = "/run/user/1000/xauth"
		r.xauth["/run/user/1000/xauth"] = true
		res := r.resolve()
		Expect(res.Target.Backend).To(Equal("x"))
		Expect(res.Target.XAuthority).To(Equal("/run/user/1000/xauth"))
	})

	It("CS-LNCH-203: a local Wayland shell without usable X uses wl-copy, labelled experimental", func() {
		r := newResolver()
		r.tools["xclip"] = false
		r.env["DISPLAY"] = ":0"
		r.env["WAYLAND_DISPLAY"] = "wayland-0"
		r.env["XDG_RUNTIME_DIR"] = "/run/user/1000"
		r.env["XDG_CURRENT_DESKTOP"] = "KDE"
		res := r.resolve()
		Expect(res.Target.Backend).To(Equal("wayland"))
		Expect(res.Target.WaylandDisplay).To(Equal("/run/user/1000/wayland-0"))
		Expect(res.Target.Describe()).To(ContainSubstring("experimental"))
	})

	It("CS-LNCH-203: on GNOME wl-copy is never picked automatically (answer 131 b): none with the hint", func() {
		r := newResolver()
		r.tools["xclip"] = false
		r.env["WAYLAND_DISPLAY"] = "wayland-0"
		r.env["XDG_RUNTIME_DIR"] = "/run/user/1000"
		r.env["XDG_CURRENT_DESKTOP"] = "ubuntu:GNOME"
		res := r.resolve()
		Expect(res.Target).To(BeNil())
		Expect(res.Hint).To(ContainSubstring("install xclip"))
		Expect(res.Hint).To(ContainSubstring("CLAUDE_SANDBOX_CLIPBOARD_DISPLAY=wayland:wayland-0"))
	})

	It("CS-LNCH-203: a display without a host copy tool resolves none with an install hint", func() {
		r := newResolver()
		r.tools = map[string]bool{}
		r.env["DISPLAY"] = ":0"
		res := r.resolve()
		Expect(res.Target).To(BeNil())
		Expect(res.Hint).To(Equal("install xclip on the host"))
	})

	It("CS-LNCH-203: the tmux session environment's DISPLAY, or its removal, wins over the pane's", func() {
		r := newResolver()
		r.env["SSH_CONNECTION"] = "x"
		r.env["DISPLAY"] = "localhost:10.0"
		r.tmux = clipboard.ParseTmuxEnv("DISPLAY=localhost:12.0\nSSH_CONNECTION=y\nOTHER=1\n")
		Expect(r.resolve().Target.Display).To(Equal("localhost:12.0"))
		r.tmux = clipboard.ParseTmuxEnv("-DISPLAY\n")
		Expect(r.resolve().Target).To(BeNil())
		r.tmux = clipboard.ParseTmuxEnv("-SSH_CONNECTION\n")
		r.env["DISPLAY"] = ":0"
		Expect(r.resolve().Target.Display).To(Equal(":0"), "no longer under SSH")
		r.tmux = nil // a timeout or error keeps the shell's values
		Expect(r.resolve().Target).To(BeNil())
	})

	It("CS-LNCH-203: only displays on this machine count; the machine's own name is accepted", func() {
		for _, d := range []string{"otherhost:0", "10.0.0.5:0", "localhost:x", "localhost", ":12345"} {
			Expect(clipboard.ValidDisplay(d, "hooper.lan")).To(BeFalse(), d)
		}
		for _, d := range []string{":0", "unix:1", "localhost:10.0", "127.0.0.2:3", "hooper:11.0", "hooper.lan:11", "HOOPER:2"} {
			Expect(clipboard.ValidDisplay(d, "hooper.lan")).To(BeTrue(), d)
		}
	})

	It("CS-LNCH-203: the X target needs XAUTHORITY (else ~/.Xauthority) to pass the owned-regular check", func() {
		r := newResolver()
		r.env["DISPLAY"] = ":0"
		r.env["XAUTHORITY"] = "/tmp/link"
		Expect(r.resolve().Target).To(BeNil())

		dir := shortDir()
		real := filepath.Join(dir, "auth")
		Expect(os.WriteFile(real, []byte("x"), 0o600)).To(Succeed())
		Expect(os.Symlink(real, filepath.Join(dir, "link"))).To(Succeed())
		Expect(clipboard.OwnedRegular(real, os.Getuid())).To(Succeed())
		Expect(clipboard.OwnedRegular(filepath.Join(dir, "link"), os.Getuid())).NotTo(Succeed(), "a symlink")
		Expect(clipboard.OwnedRegular(real, os.Getuid()+1)).NotTo(Succeed(), "another uid")
		Expect(clipboard.OwnedRegular(dir, os.Getuid())).NotTo(Succeed(), "a directory")
	})

	It("CS-LNCH-203: the override: none clears, x: and bare displays, wayland:, invalid ones warn and are ignored", func() {
		r := newResolver()
		r.env["DISPLAY"] = ":0"
		r.env["CLAUDE_SANDBOX_CLIPBOARD_DISPLAY"] = "none"
		res := r.resolve()
		Expect(res.Target).To(BeNil())
		Expect(res.Clear).To(BeTrue())
		Expect(res.ShouldWrite()).To(BeTrue(), "the one way to clear a usable target")
		Expect(res.TargetToWrite("attach", 5)).To(Equal(clipboard.Target{V: 1, Backend: "none", SetAt: 5, By: "attach"}))

		r.env["SSH_CONNECTION"] = "x" // the override ignores the SSH rule (rows 7, 8)
		r.env["CLAUDE_SANDBOX_CLIPBOARD_DISPLAY"] = "x::5"
		Expect(r.resolve().Target.Display).To(Equal(":5"))
		r.env["CLAUDE_SANDBOX_CLIPBOARD_DISPLAY"] = ":6"
		Expect(r.resolve().Target.Display).To(Equal(":6"))
		r.env["CLAUDE_SANDBOX_CLIPBOARD_DISPLAY"] = "wayland:/run/user/1000/wayland-0"
		Expect(r.resolve().Target.Backend).To(Equal("wayland"))

		r.env["CLAUDE_SANDBOX_CLIPBOARD_DISPLAY"] = "otherhost:0"
		res = r.resolve()
		Expect(res.Warning).To(HavePrefix("WARNING: CLAUDE_SANDBOX_CLIPBOARD_DISPLAY=\"otherhost:0\" is ignored"))
		Expect(res.Target).To(BeNil(), "resolved as if unset: under SSH :0 is the console's")
		r.env["CLAUDE_SANDBOX_CLIPBOARD_DISPLAY"] = "wayland:../x"
		Expect(r.resolve().Warning).NotTo(BeEmpty())
	})

	It("CS-LNCH-203: the target file is written 0600 atomically and read back re-validated", func() {
		dir := shortDir()
		auth := filepath.Join(dir, "auth")
		Expect(os.WriteFile(auth, []byte("x"), 0o600)).To(Succeed())
		path := filepath.Join(dir, clipboard.TargetFile)
		fc := clipboard.FileChecks{UID: os.Getuid()}

		_, err := clipboard.ReadTarget(path, "hooper", fc)
		Expect(err).To(MatchError(clipboard.ErrNoTarget))

		t := clipboard.Target{V: 1, Backend: "x", Display: "localhost:10.0", XAuthority: auth, SetAt: 1, By: "create"}
		Expect(clipboard.WriteTarget(path, t)).To(Succeed())
		fi, err := os.Stat(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		got, err := clipboard.ReadTarget(path, "hooper", fc)
		Expect(err).NotTo(HaveOccurred())
		Expect(*got).To(Equal(t))
		ents, _ := os.ReadDir(dir)
		Expect(ents).To(HaveLen(2), "no temp file left")

		_, err = clipboard.ReadTarget(path, "hooper", clipboard.FileChecks{UID: os.Getuid() + 1})
		Expect(err).To(HaveOccurred(), "another uid's file")
		Expect(os.WriteFile(path, []byte(`{"v":1,"backend":"x","display":"evil:0","by":"create"}`), 0o600)).To(Succeed())
		_, err = clipboard.ReadTarget(path, "hooper", fc)
		Expect(err).To(HaveOccurred(), "another host is refused at read too")
		Expect(os.WriteFile(path, []byte(strings.Repeat(" ", clipboard.MaxTargetFile+1)), 0o600)).To(Succeed())
		_, err = clipboard.ReadTarget(path, "hooper", fc)
		Expect(err).To(HaveOccurred(), "oversize")
		Expect(os.Remove(path)).To(Succeed())
		Expect(os.Symlink(auth, path)).To(Succeed())
		_, err = clipboard.ReadTarget(path, "hooper", fc)
		Expect(err).To(HaveOccurred(), "a symlink")
	})
})
