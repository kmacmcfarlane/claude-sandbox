package main

// Spec: spec/tmux.feature (CS-TMUX-064..068) — the resurrect hook forms of
// "claude-sandbox tmux restore" through MainWithEnv: --pin and --rearm are
// silent, exit 0 and log to <cache>/tmux-restore.log; --resurrected reads the
// pane's own mark, then this server's pin, then last, and prints the sparse
// notice without claiming it. The hooks' own rules are unit-tested in
// internal/tmuxpane/pin_test.go.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

var _ = Describe("tmux restore: the resurrect hooks (CS-TMUX-064..068)", func() {
	var (
		f    *cliFixture
		dir  string
		base = time.Date(2026, 9, 29, 12, 0, 0, 0, time.Local)
		now  = base.Add(time.Hour)
		srv  = tmuxpane.Server{PID: 4242, Start: 1727000000}
	)

	BeforeEach(func() {
		f = newCLIFixture()
		f.envmap["TMUX"] = "/tmp/tmux-1000/default,4242,0"
		f.envmap["TMUX_PANE"] = "%7"
		dir = filepath.Join(f.home, ".local", "share", "tmux", "resurrect")
		Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
		f.env.ResurrectDir = dir
		f.env.Now = func() time.Time { return now }
		f.env.interrupt = func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }
		f.fake.On("docker version", "29.3.0\n", nil)
	})

	stampAt := func(min int) string { return base.Add(time.Duration(min) * time.Minute).Format("20060102T150405") }
	row := func(mode string) tmuxpane.Mark {
		return tmuxpane.Mark{V: 1, State: tmuxpane.StateActive, Mode: mode, Container: "claude-sandbox-x-proj-abc123-heron",
			Instance: "heron", Project: f.proj, Conversation: markConv, Name: "fix the build", NameSource: "user"}
	}
	save := func(st string, s *tmuxpane.Server, marks ...tmuxpane.Mark) {
		Expect(os.WriteFile(filepath.Join(dir, tmuxpane.StateFileName(st)),
			[]byte("pane\tmain\t2\t1\t:*\t0\tt\t:"+f.proj+"\t1\tzsh\t:\n"), 0o600)).To(Succeed())
		var rows []tmuxpane.Row
		for _, m := range marks {
			rows = append(rows, tmuxpane.Row{Session: "main", Window: 2, Pane: 0, Mark: m})
		}
		Expect(tmuxpane.WriteSidecar(filepath.Join(dir, tmuxpane.SidecarName(st)), tmuxpane.Sidecar{
			V: 1, StateFile: tmuxpane.StateFileName(st), Server: s, Panes: rows})).To(Succeed())
	}
	link := func(st string) {
		os.Remove(filepath.Join(dir, "last"))
		Expect(os.Symlink(tmuxpane.StateFileName(st), filepath.Join(dir, "last"))).To(Succeed())
	}
	writePin := func(st string, at time.Time, sparse *tmuxpane.PinVerdict) string {
		p := tmuxpane.Pin{V: 1, ServerPID: srv.PID, Server: &srv, At: at.UnixMilli(), Stamp: st,
			Sidecar: tmuxpane.SidecarName(st), StateFile: tmuxpane.StateFileName(st), Preexisting: []string{}, Sparse: sparse}
		b, _ := json.Marshal(p)
		path := filepath.Join(dir, tmuxpane.PinName(srv.PID, at.UnixMilli()))
		Expect(os.WriteFile(path, b, 0o600)).To(Succeed())
		return path
	}
	// pane scripts this pane (main:2.0) on server 4242 and its mark.
	pane := func(mark string) {
		f.fake.On("tmux display-message -p -t %7", "main\t2\t0\t4242\t1727000000\t"+mark+"\n", nil)
	}
	logFile := func() string { return filepath.Join(f.cache, tmuxRestoreLog) }

	Describe("CS-TMUX-064, CS-TMUX-068: --pin", func() {
		It("CS-TMUX-064, CS-TMUX-068: writes the pin and a sparse save's notice, prints nothing, exits 0", func() {
			other := tmuxpane.Server{PID: 100, Start: 1726000000}
			save(stampAt(-90), &other, row("claude"), row("claude"), row("claude"), row("claude"))
			save(stampAt(-1), &srv)
			link(stampAt(-1))
			f.fake.On("tmux list-panes", "%0\t4242\t1727000000\n", nil)
			Expect(f.run("tmux", "restore", "--pin")).To(Equal(0))
			Expect(f.out.String() + f.errw.String()).To(BeEmpty())
			pins, _ := filepath.Glob(filepath.Join(dir, "claude-sandbox-restore-pin.4242.*.json"))
			Expect(pins).To(HaveLen(1))
			Expect(filepath.Join(f.cache, tmuxpane.NoticeFile)).To(BeAnExistingFile())
			Expect(f.fake.CommandLines()).To(ContainElement(HavePrefix("tmux set -g @claude-sandbox-notice sparse restore: 0 of 4 sandbox panes")))
			Expect(f.fake.CommandLines()).NotTo(ContainElement(HavePrefix("tmux set -gu")), "a hook never claims")
			Expect(logFile()).NotTo(BeAnExistingFile())
		})

		It("CS-TMUX-068: a failing tmux is exit 0 and silent, logged 0600 to tmux-restore.log", func() {
			f.fake.On("tmux list-panes", "", execx.Fail(1))
			for _, form := range []string{"--pin", "--rearm"} {
				Expect(f.run("tmux", "restore", form)).To(Equal(0), form)
			}
			Expect(f.out.String() + f.errw.String()).To(BeEmpty())
			b, err := os.ReadFile(logFile())
			Expect(err).NotTo(HaveOccurred())
			Expect(string(b)).To(ContainSubstring("tmux restore --pin: tmux list-panes failed or timed out; no pin written"))
			Expect(string(b)).To(ContainSubstring("tmux restore --rearm: tmux list-panes failed or timed out; nothing re-armed"))
			fi, _ := os.Stat(logFile())
			Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		})

		It("CS-TMUX-068: inside a sandbox the hooks do nothing; under go test a forgotten resurrect dir panics", func() {
			f.envmap["CLAUDE_SANDBOX_PROJECT_DIR"] = f.proj
			Expect(f.run("tmux", "restore", "--pin")).To(Equal(0))
			Expect(f.run("tmux", "restore", "--rearm")).To(Equal(0))
			Expect(f.fake.Calls).To(BeEmpty())
			Expect(logFile()).NotTo(BeAnExistingFile())
			delete(f.envmap, "CLAUDE_SANDBOX_PROJECT_DIR")
			f.env.ResurrectDir = ""
			Expect(func() { f.run("tmux", "restore", "--pin") }).To(PanicWith(ContainSubstring("real resurrect dir")))
		})
	})

	It("CS-TMUX-066, CS-TMUX-067: --rearm marks and retypes a new pane from the pin, silently", func() {
		pend := row("claude")
		pend.State = tmuxpane.StatePending
		save(stampAt(-1), &srv, pend)
		pin := writePin(stampAt(-1), now.Add(-2*time.Second), nil)
		f.fake.On("tmux list-panes", "%7\tmain\t2\t0\t4242\t1727000000\tzsh\t"+f.proj+"\t\n", nil)
		f.fake.On("tmux display-message -p -t %7", "zsh\t\n", nil)
		Expect(f.run("tmux", "restore", "--rearm")).To(Equal(0))
		Expect(f.out.String() + f.errw.String()).To(BeEmpty())
		Expect(f.fake.CommandLines()).To(ContainElement(HavePrefix("tmux set-option -p -t %7 @claude-sandbox ")))
		Expect(f.fake.CommandLines()).To(ContainElement("tmux send-keys -t %7 claude-sandbox tmux restore --resurrected C-m"))
		Expect(pin).NotTo(BeAnExistingFile(), "consumed")
		Expect(logFile()).NotTo(BeAnExistingFile())
	})

	Describe("CS-TMUX-065: --resurrected", func() {
		It("CS-TMUX-065: reads this server's pin before last", func() {
			save(stampAt(-1), &srv, row(tmuxpane.ModeRalph))
			save(stampAt(-2), &srv, row(tmuxpane.ModeJoin))
			link(stampAt(-1))
			writePin(stampAt(-2), now.Add(-time.Minute), nil)
			pane("")
			Expect(f.run("tmux", "restore", "--resurrected")).To(Equal(0), f.errw.String())
			Expect(f.out.String()).To(ContainSubstring("a joined session was here"), "the pinned save's row")
			Expect(f.out.String()).NotTo(ContainSubstring("ralph"))
		})

		It("CS-TMUX-065: a pin of another server, or one older than 10 minutes, is not read: last is", func() {
			save(stampAt(-1), &srv, row(tmuxpane.ModeRalph))
			save(stampAt(-2), &srv, row(tmuxpane.ModeJoin))
			link(stampAt(-1))
			writePin(stampAt(-2), now.Add(-11*time.Minute), nil)
			pane("")
			Expect(f.run("tmux", "restore", "--resurrected")).To(Equal(0), f.errw.String())
			Expect(f.out.String()).To(ContainSubstring("a ralph run was here"))
		})

		It("CS-TMUX-065: the pane's own pending mark comes first", func() {
			save(stampAt(-1), &srv, row(tmuxpane.ModeRalph))
			link(stampAt(-1))
			writePin(stampAt(-1), now.Add(-time.Minute), nil)
			own := row(tmuxpane.ModeJoin)
			own.State = tmuxpane.StatePending
			pane(own.JSON())
			Expect(f.run("tmux", "restore", "--resurrected")).To(Equal(0), f.errw.String())
			Expect(f.out.String()).To(ContainSubstring("a joined session was here"))
		})

		It("CS-TMUX-065: prints the notice once (the pin's line is not repeated) and never claims it", func() {
			save(stampAt(-1), &srv, row(tmuxpane.ModeRalph))
			link(stampAt(-1))
			v := &tmuxpane.PinVerdict{N: 1, M: 9, K: 3, Lifetimes: true, Known: true, Sparse: true}
			writePin(stampAt(-1), now.Add(-time.Minute), v)
			Expect(tmuxpane.WriteNotice(f.cache, tmuxpane.Notice{Stamp: stampAt(-1), N: 1, M: 9, K: 3, Lifetimes: true,
				At: now.Add(-time.Minute).UnixMilli()})).To(Succeed())
			pane("")
			Expect(f.run("tmux", "restore", "--resurrected")).To(Equal(0), f.errw.String())
			Expect(strings.Count(f.errw.String(), "this save ("+stampAt(-1)+") has 1 sandbox panes")).To(Equal(1))
			Expect(filepath.Join(f.cache, tmuxpane.NoticeFile)).To(BeAnExistingFile())
			Expect(f.fake.CommandLines()).NotTo(ContainElement(HavePrefix("tmux set -gu")))
		})
	})
})
