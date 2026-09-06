package launch_test

// Spec: spec/launch.feature (CS-LNCH-041..047), spec/sessions.feature
// (CS-SESS-045) — the worktree-mode pieces of the launch package: the
// tri-state resolver, name validation, the git pre-check, existing-worktree
// listing, and the plan's command, label and env.

import (
	"bytes"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
)

func boolp(b bool) *bool { return &b }

var _ = Describe("worktree mode", func() {
	DescribeTable("CS-LNCH-042: ResolveTristate — CLI > env > YAML > default, falsy env is an explicit off",
		func(cli *bool, env string, yaml *bool, def, want bool) {
			Expect(launch.ResolveTristate(cli, env, yaml, def)).To(Equal(want))
		},
		Entry("all unset -> default on", nil, "", nil, true, true),
		Entry("all unset -> default off", nil, "", nil, false, false),
		Entry("yaml false", nil, "", boolp(false), true, false),
		Entry("yaml true", nil, "", boolp(true), false, true),
		Entry("env 1 beats yaml false", nil, "1", boolp(false), true, true),
		Entry("env true beats yaml false", nil, "true", boolp(false), true, true),
		Entry("env yes beats yaml false", nil, "yes", boolp(false), true, true),
		Entry("env 0 beats yaml true", nil, "0", boolp(true), true, false),
		Entry("env false beats yaml true", nil, "false", boolp(true), true, false),
		Entry("env no beats yaml true", nil, "no", boolp(true), true, false),
		Entry("env FALSE (case-insensitive)", nil, "FALSE", boolp(true), true, false),
		Entry("unrecognized env is unset", nil, "maybe", boolp(false), true, false),
		Entry("unrecognized env falls to default", nil, "maybe", nil, true, true),
		Entry("cli false beats env 1 and yaml true", boolp(false), "1", boolp(true), true, false),
		Entry("cli true beats env 0 and yaml false", boolp(true), "0", boolp(false), false, true),
	)

	It("CS-LNCH-043: ValidateWorktreeName applies claude's rule", func() {
		for _, ok := range []string{"otter", "feature-x", "v1.2_rc", strings.Repeat("a", 64)} {
			Expect(launch.ValidateWorktreeName(ok)).To(Succeed(), ok)
		}
		for _, bad := range []string{"", "has space", "a/b", strings.Repeat("a", 65), ".git", ".", "..", "ünïcode"} {
			Expect(launch.ValidateWorktreeName(bad)).To(HaveOccurred(), bad)
		}
	})

	It("CS-LNCH-046: GitRoot asks git for the top level and reports none when outside a repository", func() {
		fake := &execx.Fake{}
		Expect(launch.GitRoot(fake, "/p/sub")).To(Equal(""), "an unscripted fake answers nothing")
		Expect(fake.CommandLines()).To(ContainElement("git -C /p/sub rev-parse --show-toplevel"))

		fake = &execx.Fake{}
		fake.On("rev-parse --show-toplevel", "/p\n", nil)
		Expect(launch.GitRoot(fake, "/p/sub")).To(Equal("/p"), "a subdirectory of a repository still counts")

		fake = &execx.Fake{}
		fake.On("rev-parse --show-toplevel", "", execx.Fail(128))
		Expect(launch.GitRoot(fake, "/p")).To(Equal(""))
	})

	It("CS-SESS-045: ExistingWorktrees lists the directories under .claude/worktrees", func() {
		root := GinkgoT().TempDir()
		Expect(launch.ExistingWorktrees(root)).To(BeEmpty(), "no directory yet")
		Expect(launch.ExistingWorktrees("")).To(BeEmpty(), "no root at all")
		mkdir(filepath.Join(root, ".claude/worktrees/otter"))
		mkdir(filepath.Join(root, ".claude/worktrees/heron"))
		touch(filepath.Join(root, ".claude/worktrees/notes.txt"), "")
		Expect(launch.ExistingWorktrees(root)).To(ConsistOf("otter", "heron"))
	})

	Describe("the plan", func() {
		var in launch.Inputs

		BeforeEach(func() {
			base, err := filepath.EvalSymlinks(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())
			home := filepath.Join(base, "home")
			proj := filepath.Join(base, "proj")
			mkdir(home)
			mkdir(proj)
			in = launch.Inputs{
				ProjectDir: proj, Home: home, HostUID: 1000, HostGID: 1000, HostUser: "tester",
				Getenv:    func(string) string { return "" },
				TempDir:   filepath.Join(base, "shadow"),
				ImageName: "claude-sandbox:run",
				Out:       &bytes.Buffer{}, Err: &bytes.Buffer{},
			}
			mkdir(in.TempDir)
		})

		build := func() *launch.Plan {
			p, err := launch.Build(in)
			Expect(err).NotTo(HaveOccurred())
			return p
		}

		It("CS-LNCH-041, CS-LNCH-026: --worktree <name> follows the permission flag and precedes --model and passthrough", func() {
			in.Instance, in.Worktree = "otter", "otter"
			in.SkipPermissions, in.CLIModel = true, "opus"
			in.Passthrough = []string{"--resume"}
			Expect(build().Command).To(Equal([]string{
				"claude", "--dangerously-skip-permissions", "--worktree", "otter", "--model", "opus", "--resume",
			}))
			in.Worktree = ""
			Expect(build().Command).To(Equal([]string{"claude", "--dangerously-skip-permissions", "--model", "opus", "--resume"}))
		})

		It("CS-LNCH-045, CS-LNCH-027: ralph gets the same pair after its own flags", func() {
			in.RalphMode, in.Limit, in.Worktree = true, "5", "ralph"
			in.Passthrough = []string{"--verbose"}
			Expect(build().Command).To(Equal([]string{
				"/opt/claude-sandbox/bin/ralph", "--limit", "5", "--worktree", "ralph", "--verbose",
			}))
		})

		It("CS-LNCH-044: the worktree label carries the name, or an empty value when off", func() {
			in.Worktree = "otter"
			Expect(build().Labels).To(ContainElement("claude-sandbox.worktree=otter"))
			in.Worktree = ""
			Expect(build().Labels).To(ContainElement("claude-sandbox.worktree="))
		})

		It("CS-LNCH-044: neither the name nor the config key moves the fingerprint", func() {
			bare := build().ConfigHash
			in.Worktree = "otter"
			Expect(build().ConfigHash).To(Equal(bare))
			in.Cfg = &cascade.Config{Worktree: boolp(false)}
			Expect(build().ConfigHash).To(Equal(bare))
			in.Cfg = &cascade.Config{Worktree: boolp(true)}
			Expect(build().ConfigHash).To(Equal(bare))
		})

		It("CS-LNCH-047: CLAUDE_SANDBOX_PROJECT_DIR is always in the container environment", func() {
			for _, ralph := range []bool{false, true} {
				for _, wt := range []string{"", "otter"} {
					in.RalphMode, in.Worktree = ralph, wt
					p := build()
					Expect(p.EnvFlags).To(ContainElement("CLAUDE_SANDBOX_PROJECT_DIR=" + in.ProjectDir))
					Expect(argPairs(p.DockerArgs(in.ProjectDir), "-e")).To(ContainElement("CLAUDE_SANDBOX_PROJECT_DIR=" + in.ProjectDir))
				}
			}
		})
	})
})
