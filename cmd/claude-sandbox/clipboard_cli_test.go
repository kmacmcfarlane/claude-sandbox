package main

// Spec: spec/launch.feature CS-LNCH-194..197, 202, 203 (the host clipboard
// bridge through the CLI). newCLIFixture pins CLAUDE_SANDBOX_HOST_CLIPBOARD=0;
// these tests delete it to exercise the default (on), and fake the host's
// copy tools and hostname: no test depends on the host's xclip or display.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/clipboard"
	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
)

// psRowClip is a discovery row carried through the clipboard and shadowdir
// labels; resume is the resume label.
func psRowClip(name, project, instance, clip, shadow, resume, worktree string) string {
	return strings.Join([]string{name, "Up 1 hour", project, "claude", instance, "v1", "", "", "", "3", worktree,
		"running", "2026-09-30 12:00:00 +0000 UTC", "", "", "",
		strings.Repeat("b", 64), "", "", "", resume, "none", "none", "", clip, shadow, ""}, psSep)
}

// signalRunner wraps the fake and hands back, for the clipboard helper's
// start, a process that records the signals it gets.
type signalRunner struct {
	*execx.Fake
	helper *recordedProc
}

type recordedProc struct{ signals []os.Signal }

func (p *recordedProc) Signal(s os.Signal) error { p.signals = append(p.signals, s); return nil }
func (p *recordedProc) Wait() error              { return nil }
func (p *recordedProc) Pid() int                 { return 4243 }

func (r *signalRunner) Start(c execx.Cmd) (execx.Process, error) {
	if len(c.Args) > 0 && c.Args[0] == "clipboard-bridge" {
		r.Fake.Start(c)
		r.helper = &recordedProc{}
		return r.helper, nil
	}
	return r.Fake.Start(c)
}

var _ = Describe("the host clipboard bridge (CLI)", func() {
	var (
		f     *cliFixture
		tools map[string]bool
	)
	BeforeEach(func() {
		f = newCLIFixture()
		delete(f.envmap, "CLAUDE_SANDBOX_HOST_CLIPBOARD") // the default: on
		short, err := os.MkdirTemp("/tmp", "cbc")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(os.RemoveAll, short)
		f.env.TempRoot = short // the socket path's length is checked
		tools = map[string]bool{"xclip": true}
		f.env.ClipboardLookPath = func(n string) (string, error) {
			if tools[n] {
				return "/usr/bin/" + n, nil
			}
			return "", errors.New("not found")
		}
		f.env.Hostname = func() (string, error) { return "hooper", nil }
		writeFile(filepath.Join(f.home, ".Xauthority"), "cookie")
	})

	helperStarts := func() []execx.Cmd {
		var out []execx.Cmd
		for _, c := range f.fake.Calls {
			if len(c.Args) > 0 && c.Args[0] == "clipboard-bridge" {
				out = append(out, c)
			}
		}
		return out
	}
	readTarget := func(shadow string) map[string]any {
		b, err := os.ReadFile(filepath.Join(shadow, "clipboard-target.json"))
		Expect(err).NotTo(HaveOccurred())
		var m map[string]any
		Expect(json.Unmarshal(b, &m)).To(Succeed())
		return m
	}

	It("CS-LNCH-194, CS-LNCH-195: on by default: mount, label, banner, the target written, then the helper started detached", func() {
		f.envmap["SSH_CONNECTION"] = "10.0.0.2 1 10.0.0.1 22"
		f.envmap["DISPLAY"] = "localhost:10.0"
		Expect(f.run("--new")).To(Equal(0), f.errw.String())
		args := f.launched().Args
		Expect(args).To(ContainElement("--mount"))
		Expect(labelsOf(args)["claude-sandbox.clipboard"]).To(Equal("bridge"))
		shadow := labelsOf(args)["claude-sandbox.shadowdir"]
		Expect(f.out.String()).To(ContainSubstring("Host clipboard: write-only bridge (target: X localhost:10.0;"))
		t := readTarget(shadow)
		Expect(t["backend"]).To(Equal("x"))
		Expect(t["display"]).To(Equal("localhost:10.0"))
		Expect(t["by"]).To(Equal("create"))
		starts := helperStarts()
		Expect(starts).To(HaveLen(1))
		Expect(starts[0].Detach).To(BeTrue())
		Expect(starts[0].Name).To(Equal("/fake/claude-sandbox"))
		Expect(strings.Join(starts[0].Args, " ")).To(Equal("clipboard-bridge --dir " + filepath.Join(shadow, "clipboard") +
			" --container " + argPairsCLI(args, "--name")[0] +
			" --log " + filepath.Join(f.cache, "clipboard-bridge.log") + " --target " + filepath.Join(shadow, "clipboard-target.json")))
	})

	It("CS-LNCH-195: a removed reservation (a docker start that could not run) SIGTERMs the helper", func() {
		rec := &signalRunner{Fake: f.fake}
		f.env.Runner = rec
		f.fake.On("docker start -ai", "", errors.New("exec: docker: not found"))
		Expect(f.run("--new")).NotTo(Equal(0))
		Expect(rec.helper).NotTo(BeNil())
		Expect(rec.helper.signals).To(Equal([]os.Signal{syscall.SIGTERM}))
	})

	It("CS-LNCH-194: explicitly off (env or key) launches without it", func() {
		f.envmap["CLAUDE_SANDBOX_HOST_CLIPBOARD"] = "no"
		Expect(f.run("--new")).To(Equal(0), f.errw.String())
		Expect(f.launched().Args).NotTo(ContainElement("--mount"))
		Expect(labelsOf(f.launched().Args)["claude-sandbox.clipboard"]).To(Equal("none"))
		Expect(helperStarts()).To(BeEmpty())

		g := newCLIFixture()
		delete(g.envmap, "CLAUDE_SANDBOX_HOST_CLIPBOARD")
		writeFile(filepath.Join(g.proj, ".claude-sandbox", "config.yaml"), "hostClipboard: false\n")
		Expect(g.run("--new")).To(Equal(0), g.errw.String())
		Expect(labelsOf(g.launched().Args)["claude-sandbox.clipboard"]).To(Equal("none"))
	})

	It("CS-LNCH-197: headless and ralph never get it, whatever the default", func() {
		Expect(f.run("headless", "--", "-p", "hi")).To(Equal(0), f.errw.String())
		Expect(f.launched().Args).NotTo(ContainElement("--mount"))
		Expect(helperStarts()).To(BeEmpty())
		g := newCLIFixture()
		g.envmap["CLAUDE_SANDBOX_HOST_CLIPBOARD"] = "1"
		Expect(g.run("--ralph")).To(Equal(0), g.errw.String())
		Expect(g.launched().Args).NotTo(ContainElement("--mount"))
		Expect(labelsOf(g.launched().Args)["claude-sandbox.clipboard"]).To(Equal("none"))
	})

	It("CS-LNCH-195: no display is no warning: the banner says OSC 52 only, no target file", func() {
		Expect(f.run("--new")).To(Equal(0), f.errw.String())
		Expect(f.out.String()).To(ContainSubstring("(target: none: OSC 52 only;"))
		Expect(f.errw.String()).NotTo(ContainSubstring("hostClipboard"))
		shadow := labelsOf(f.launched().Args)["claude-sandbox.shadowdir"]
		_, err := os.Stat(filepath.Join(shadow, "clipboard-target.json"))
		Expect(os.IsNotExist(err)).To(BeTrue())
		Expect(helperStarts()).To(HaveLen(1), "the helper serves anyway: copies answer no-display")
	})

	It("CS-LNCH-203: under tmux the pane's session environment decides, through one bounded show-environment", func() {
		f.envmap["TMUX"] = "/tmp/tmux-1000/default,1,0"
		f.envmap["TMUX_PANE"] = "%3"
		f.envmap["SSH_CONNECTION"] = "a"
		f.envmap["DISPLAY"] = "localhost:10.0"
		f.fake.On("tmux show-environment -t %3", "DISPLAY=localhost:12.0\nSSH_CONNECTION=b\n", nil)
		Expect(f.run("--new")).To(Equal(0), f.errw.String())
		Expect(readTarget(labelsOf(f.launched().Args)["claude-sandbox.shadowdir"])["display"]).To(Equal("localhost:12.0"))
	})

	It("CS-LNCH-203: an invalid override warns once and is ignored", func() {
		f.envmap["CLAUDE_SANDBOX_CLIPBOARD_DISPLAY"] = "elsewhere:0"
		Expect(f.run("--new")).To(Equal(0), f.errw.String())
		Expect(f.errw.String()).To(ContainSubstring(`WARNING: CLAUDE_SANDBOX_CLIPBOARD_DISPLAY="elsewhere:0" is ignored`))
	})

	Describe("attach and join (CS-LNCH-197)", func() {
		var shadow, row string
		BeforeEach(func() {
			shadow = filepath.Join(f.env.TempRoot, "claude-sandbox42")
			Expect(os.MkdirAll(filepath.Join(shadow, "clipboard"), 0o700)).To(Succeed())
			f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
			f.fake.OnFunc("docker ps", func(execx.Cmd) (string, error) { return row + "\n", nil })
		})
		attach := func(r string) {
			row = r
			Expect(runBounded(f, "--attach=otter", "--allow-config-drift")).To(Equal(0), f.errw.String())
		}

		It("CS-LNCH-197: an attach re-points the target and restarts a dead helper", func() {
			f.envmap["DISPLAY"] = ":0"
			attach(psRowClip("cs-a", f.proj, "otter", "bridge", shadow, "", ""))
			t := readTarget(shadow)
			Expect(t["display"]).To(Equal(":0"))
			Expect(t["by"]).To(Equal("attach"))
			starts := helperStarts()
			Expect(starts).To(HaveLen(1))
			Expect(strings.Join(starts[0].Args, " ")).To(ContainSubstring("--container cs-a --log "))
			Expect(strings.Join(starts[0].Args, " ")).To(ContainSubstring("--id " + strings.Repeat("b", 64)))
			Expect(f.out.String()).To(ContainSubstring("Clipboard bridge restarted for 'otter'."))
		})

		It("CS-LNCH-203: never downgrade: an attach from a shell without a display keeps the target", func() {
			Expect(clipboard.WriteTarget(filepath.Join(shadow, "clipboard-target.json"),
				clipboard.Target{V: 1, Backend: "x", Display: "localhost:10.0", By: "create"})).To(Succeed())
			attach(psRowClip("cs-a", f.proj, "otter", "bridge", shadow, "", ""))
			Expect(readTarget(shadow)["display"]).To(Equal("localhost:10.0"))
			f.envmap["CLAUDE_SANDBOX_CLIPBOARD_DISPLAY"] = "none"
			attach(psRowClip("cs-a", f.proj, "otter", "bridge", shadow, "", ""))
			Expect(readTarget(shadow)["backend"]).To(Equal("none"), "only the override none clears it")
		})

		It("CS-LNCH-197: on the host, a container whose shadow dir lies under one of its bind sources gets no target and no helper", func() {
			f.envmap["DISPLAY"] = ":0"
			// A nested container: its shadow root is inside the config-dir
			// mount, here the TempRoot itself as a bind source.
			attach(psRowClip("cs-a", f.proj, "otter", "bridge", shadow, "", "") + f.env.TempRoot + "," + f.proj)
			_, err := os.Stat(filepath.Join(shadow, "clipboard-target.json"))
			Expect(os.IsNotExist(err)).To(BeTrue())
			Expect(helperStarts()).To(BeEmpty())

			link := filepath.Join(f.env.TempRoot, "link")
			Expect(os.Symlink(f.env.TempRoot, link)).To(Succeed())
			attach(psRowClip("cs-a", f.proj, "otter", "bridge", shadow, "", "") + link)
			Expect(helperStarts()).To(BeEmpty(), "the source after EvalSymlinks")
		})

		It("CS-LNCH-197: a restore attach (by \"\") pings and restarts the helper but writes no target", func() {
			f.envmap["DISPLAY"] = ":0"
			s := sessions.Session{Name: "cs-a", Instance: "otter", Clipboard: "bridge", ShadowDir: shadow}
			eff, bridged := refreshClipboardBridge(f.env, s, "")
			Expect(bridged).To(BeTrue())
			Expect(eff).To(BeNil())
			_, err := os.Stat(filepath.Join(shadow, "clipboard-target.json"))
			Expect(os.IsNotExist(err)).To(BeTrue())
			Expect(helperStarts()).To(HaveLen(1))
		})

		It("CS-LNCH-197: a join re-points the target too", func() {
			f.envmap["DISPLAY"] = ":0"
			row = psRowClip("cs-a", f.proj, "otter", "bridge", shadow, "", "")
			Expect(f.run("--join=otter")).To(Equal(0), f.errw.String())
			Expect(readTarget(shadow)["by"]).To(Equal("join"))
			Expect(helperStarts()).To(HaveLen(1))
		})

		It("CS-LNCH-197: the attach note for a container started without the bridge: resume and picker forms", func() {
			id := "0b9d8e57-1c4f-4a7e-9c55-3f1d2a6b7c8e"
			attach(psRowClip("cs-a", f.proj, "otter", "none", "", id, "otter"))
			Expect(f.errw.String()).To(ContainSubstring("Note: 'otter' was started without the clipboard bridge, so its copies stay on OSC 52. To get it, end that session, then relaunch the conversation it started with: cd " +
				f.proj + " && claude-sandbox --new --worktree=otter -- --resume " + id + ". If you have since switched conversations"))
			Expect(helperStarts()).To(BeEmpty())

			f.errw.Reset()
			attach(psRowClip("cs-a", f.proj, "otter", "none", "", "", ""))
			Expect(f.errw.String()).To(ContainSubstring("then relaunch with: cd " + f.proj + " && claude-sandbox --new --no-worktree -- --resume and pick the conversation in claude's resume picker."))

			f.errw.Reset()
			attach(psRowClip("cs-a", f.proj, "otter", "none", "", "", "bad\x1bname"))
			Expect(f.errw.String()).To(ContainSubstring("then relaunch it with --resume."))
			Expect(f.errw.String()).NotTo(ContainSubstring("claude-sandbox --new --worktree=bad"))

			f.errw.Reset()
			attach(psRowClip("cs-a", f.proj, "otter", "", "", "", ""))
			Expect(f.errw.String()).NotTo(ContainSubstring("clipboard bridge"), "a container that predates the label")

			f.errw.Reset()
			f.envmap["CLAUDE_SANDBOX_HOST_CLIPBOARD"] = "0"
			attach(psRowClip("cs-a", f.proj, "otter", "none", "", "", ""))
			Expect(f.errw.String()).NotTo(ContainSubstring("clipboard bridge"), "not wanted here")
		})
	})

	Describe("the JetBrains note (CS-LNCH-202)", func() {
		const note = "JetBrains terminals ignore OSC 52, so copies will not reach your clipboard"

		It("CS-LNCH-202: JediTerm with no usable display prints the note once per project", func() {
			f.envmap["TERMINAL_EMULATOR"] = "JetBrains-JediTerm"
			Expect(f.run("--new")).To(Equal(0), f.errw.String())
			Expect(f.errw.String()).To(ContainSubstring(note))
			f.errw.Reset()
			Expect(f.run("--new")).To(Equal(0), f.errw.String())
			Expect(f.errw.String()).NotTo(ContainSubstring(note))
		})

		It("CS-LNCH-202: silent with a display, another terminal, an env-file silencer, or the bridge off", func() {
			f.envmap["TERMINAL_EMULATOR"] = "JetBrains-JediTerm"
			f.envmap["DISPLAY"] = ":0"
			Expect(f.run("--new")).To(Equal(0), f.errw.String())
			Expect(f.errw.String()).NotTo(ContainSubstring(note))

			delete(f.envmap, "DISPLAY")
			writeFile(filepath.Join(f.proj, ".claude-sandbox", "env"), "CLAUDE_CODE_DISABLE_MOUSE=1\n")
			Expect(f.run("--new")).To(Equal(0), f.errw.String())
			Expect(f.errw.String()).NotTo(ContainSubstring(note))

			writeFile(filepath.Join(f.proj, ".claude-sandbox", "env"), "CLAUDE_CODE_DISABLE_MOUSE=0\n")
			f.envmap["CLAUDE_SANDBOX_HOST_CLIPBOARD"] = "0"
			Expect(f.run("--new")).To(Equal(0), f.errw.String())
			Expect(f.errw.String()).NotTo(ContainSubstring(note))

			delete(f.envmap, "CLAUDE_SANDBOX_HOST_CLIPBOARD")
			f.envmap["TERMINAL_EMULATOR"] = "xterm"
			Expect(f.run("--new")).To(Equal(0), f.errw.String())
			Expect(f.errw.String()).NotTo(ContainSubstring(note))
		})
	})
})
