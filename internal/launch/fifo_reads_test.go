package launch_test

// Spec: spec/launch.feature CS-LNCH-173..175 — the launch path's remaining
// reads of session-writable files and directories never block on a FIFO,
// device or socket: the linked worktree back-link (CS-LNCH-173), the shadow
// sources <config dir>/CLAUDE.md, <config parent>/.mcp.json and ~/.gitconfig
// (CS-LNCH-174), and the directory listings of .claude/worktrees and a
// shadow sweep root (CS-LNCH-175 — a pin, not a fix: os.ReadDir opens
// O_DIRECTORY, so these never blocked; the tests keep it that way).

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	assets "github.com/kmacmcfarlane/claude-sandbox"
	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
)

// plantFIFO makes path a FIFO with no writer. The cleanup opens a writer,
// which releases a reader a regression left blocked in open(2).
func plantFIFO(path string) {
	mkdir(filepath.Dir(path))
	Expect(syscall.Mkfifo(path, 0o600)).To(Succeed())
	DeferCleanup(func() {
		if w, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = w.Close()
		}
	})
}

// within5s runs f and fails unless it returns within 5 s.
func within5s(f func()) {
	done := make(chan struct{})
	go func() {
		defer GinkgoRecover()
		defer close(done)
		f()
	}()
	Eventually(done, 5*time.Second).Should(BeClosed(), "must not block on a FIFO")
}

var _ = Describe("launch-path reads never block on a FIFO", func() {
	var base, home, proj string
	var in launch.Inputs
	var errw *bytes.Buffer

	BeforeEach(func() {
		var err error
		base, err = filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		home = filepath.Join(base, "home")
		proj = filepath.Join(base, "proj")
		mkdir(home)
		mkdir(proj)
		errw = &bytes.Buffer{}
		in = launch.Inputs{
			ProjectDir: proj, Home: home, HostUID: 1000, HostGID: 1000, HostUser: "tester",
			Getenv:    func(string) string { return "" },
			TempDir:   filepath.Join(base, "shadow"),
			ImageName: "claude-sandbox-proj",
			Out:       &bytes.Buffer{}, Err: errw,
		}
		mkdir(in.TempDir)
	})

	buildBounded := func() *launch.Plan {
		var p *launch.Plan
		within5s(func() {
			var err error
			p, err = launch.Build(in)
			Expect(err).NotTo(HaveOccurred())
		})
		return p
	}

	It("CS-LNCH-173: a FIFO at the linked worktree's back-link is not linked, at once", func() {
		wt := filepath.Join(base, "paseo", "feat")
		common := filepath.Join(base, "repo", ".git")
		gitDir := filepath.Join(common, "worktrees", "feat")
		mkdir(wt)
		plantFIFO(filepath.Join(gitDir, "gitdir"))
		fake := &execx.Fake{}
		fake.On(revParse, gitDir+"\n"+common+"\n"+wt+"\n", nil)
		within5s(func() {
			lw, warn := launch.DetectLinkedWorktree(fake, wt)
			Expect(lw).To(BeNil())
			Expect(warn).To(BeEmpty())
		})
	})

	It("CS-LNCH-174: a FIFO at <config dir>/CLAUDE.md is no host memory", func() {
		plantFIFO(filepath.Join(home, ".claude", "CLAUDE.md"))
		buildBounded()
		raw, err := os.ReadFile(filepath.Join(in.TempDir, "CLAUDE.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(raw).To(Equal(assets.ContainerContext))
	})

	It("CS-LNCH-174: a FIFO at the host .mcp.json warns once (CS-LNCH-168) and uses the fragment", func() {
		hostMCP := filepath.Join(home, ".mcp.json")
		plantFIFO(hostMCP)
		buildBounded()
		raw, err := os.ReadFile(filepath.Join(in.TempDir, ".mcp.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(raw).To(Equal(assets.MCPServers))
		Expect(strings.Count(errw.String(), "WARNING")).To(Equal(1))
		Expect(errw.String()).To(ContainSubstring("WARNING: " + hostMCP + " cannot be read (open " + hostMCP + ": not a regular file"))
	})

	It("CS-LNCH-174: a FIFO at ~/.gitconfig is no gitconfig", func() {
		t := true
		in.CLIGit = &t
		plantFIFO(filepath.Join(home, ".gitconfig"))
		p := buildBounded()
		for _, v := range p.Volumes {
			Expect(v).NotTo(ContainSubstring("gitconfig"))
		}
	})

	It("CS-LNCH-175: a FIFO at .claude/worktrees lists no worktrees, at once", func() {
		plantFIFO(filepath.Join(proj, ".claude", "worktrees"))
		within5s(func() {
			Expect(launch.ExistingWorktrees(proj)).To(BeEmpty())
		})
	})

	It("CS-LNCH-175: a FIFO at a shadow sweep root is an error, at once, and removes nothing", func() {
		root := filepath.Join(base, "sweep")
		plantFIFO(root)
		within5s(func() {
			removed, err := launch.PruneShadowDirs(&execx.Fake{}, root, os.Getuid(), time.Now(), launch.ShadowDirMinAge, "")
			Expect(err).To(MatchError(ContainSubstring("reading " + root)))
			Expect(removed).To(BeEmpty())
		})
	})
})
