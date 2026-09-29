package main

// Spec: spec/tmux.feature (CS-TMUX-030, CS-TMUX-038) — "claude-sandbox tmux
// save" through MainWithEnv: routed as a subcommand, silent on stdout and
// stderr, exit 0 whatever happens, problems in <cache>/tmux-save.log. The
// hook's own rules are tested in internal/tmuxpane/save_test.go.

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

var _ = Describe("tmux save (CS-TMUX-030)", func() {
	var (
		f     *cliFixture
		dir   string
		state string
	)

	BeforeEach(func() {
		f = newCLIFixture()
		dir = filepath.Join(f.home, ".local", "share", "tmux", "resurrect")
		Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
		state = filepath.Join(dir, "tmux_resurrect_20260929T120000.txt")
		Expect(os.WriteFile(state, []byte("pane\tmain\t1\t1\t:*\t0\tt\t:"+f.proj+"\t1\tclaude-sandbox\t:claude-sandbox\n"), 0o600)).To(Succeed())
	})

	logFile := func() string { return filepath.Join(f.cache, "tmux-save.log") }

	It("CS-TMUX-030: routes to the hook, writes the sidecar, prints nothing and exits 0", func() {
		m := tmuxpane.Mark{V: 1, State: tmuxpane.StatePending, Mode: tmuxpane.ModeClaude, Container: "c", Project: f.proj, Conversation: markConv}
		f.fake.On("tmux list-panes", "main\t1\t0\t%1\tzsh\t"+m.JSON()+"\n", nil)
		Expect(f.run("tmux", "save", state)).To(Equal(0))
		Expect(f.out.String()).To(BeEmpty())
		Expect(f.errw.String()).To(BeEmpty())
		b, err := os.ReadFile(filepath.Join(dir, "tmux_resurrect_20260929T120000.claude-sandbox.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring(markConv))
		Expect(logFile()).NotTo(BeAnExistingFile(), "nothing to log")
	})

	It("CS-TMUX-030: a bad argument or a failing tmux is exit 0 and silent, logged to the cache root 0600", func() {
		for _, args := range [][]string{{"tmux", "save"}, {"tmux", "save", "a", "b"}, {"tmux", "save", "--help"},
			{"tmux", "save", filepath.Join(dir, "missing.txt")}} {
			Expect(f.run(args...)).To(Equal(0), strings.Join(args, " "))
		}
		f.fake.On("tmux list-panes", "", execx.Fail(1))
		Expect(f.run("tmux", "save", state)).To(Equal(0))
		Expect(f.out.String()).To(BeEmpty())
		Expect(f.errw.String()).To(BeEmpty())
		for _, l := range f.fake.CommandLines() {
			Expect(l).To(HavePrefix("tmux list-panes"), "only the one valid run reaches tmux")
		}
		b, err := os.ReadFile(logFile())
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring("expected one argument"))
		Expect(string(b)).To(ContainSubstring("not a tmux-resurrect state file"))
		Expect(string(b)).To(ContainSubstring("tmux list-panes failed"))
		fi, _ := os.Stat(logFile())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))
	})

	It("CS-TMUX-030: the log is emptied first once it has grown past 64 KiB", func() {
		Expect(os.MkdirAll(f.cache, 0o700)).To(Succeed())
		Expect(os.WriteFile(logFile(), []byte(strings.Repeat("x", 65<<10)), 0o600)).To(Succeed())
		Expect(f.run("tmux", "save")).To(Equal(0))
		b, _ := os.ReadFile(logFile())
		Expect(len(b)).To(BeNumerically("<", 1024))
		Expect(string(b)).To(ContainSubstring("expected one argument"))
	})

	It("CS-TMUX-030: inside a sandbox it runs nothing and logs nothing", func() {
		f.envmap["CLAUDE_SANDBOX_PROJECT_DIR"] = f.proj
		Expect(f.run("tmux", "save", state)).To(Equal(0))
		Expect(f.fake.Calls).To(BeEmpty())
		Expect(logFile()).NotTo(BeAnExistingFile())
		Expect(filepath.Join(dir, "tmux_resurrect_20260929T120000.claude-sandbox.json")).NotTo(BeAnExistingFile())
		Expect(f.out.String() + f.errw.String()).To(BeEmpty())
	})
})
