package main

// Spec: spec/launch.feature + spec/image-build.feature — end-to-end launcher
// scenarios through MainWithEnv with a fully scripted execx.Fake: docker
// inspect/build/npm/git are all faked, the reserving "docker create" is a
// recorded call (launched) and the final "docker start" hand-off is recorded
// in Fake.Execed. HOME/PROJECT_DIR/repo root come from a Getenv map
// pointed at temp dirs.

import (
	"bytes"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/imagebuild"
	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
)

// fakeLock is the launch-lock seam under test (CS-SESS-048). It records where
// in the fake runner's call log the lock was taken and released, so tests can
// assert which docker calls ran inside the critical section.
type fakeLock struct {
	fake       *execx.Fake
	acquiredAt []int
	releasedAt []int
	err        error
}

func (l *fakeLock) Acquire() (func(), error) {
	if l.err != nil {
		return nil, l.err
	}
	l.acquiredAt = append(l.acquiredAt, len(l.fake.CommandLines()))
	return func() { l.releasedAt = append(l.releasedAt, len(l.fake.CommandLines())) }, nil
}

// held returns the command lines recorded while the lock was held (the first
// acquisition), failing when it was never taken or never released.
func (l *fakeLock) held() []string {
	Expect(l.acquiredAt).To(HaveLen(1), "the launch lock is taken exactly once")
	Expect(l.releasedAt).To(HaveLen(1), "the launch lock is released exactly once")
	return l.fake.CommandLines()[l.acquiredAt[0]:l.releasedAt[0]]
}

// cliFixture wires MainWithEnv to temp dirs and a recording fake.
type cliFixture struct {
	env       *Env
	fake      *execx.Fake
	lock      *fakeLock
	out, errw *bytes.Buffer
	envmap    map[string]string
	home      string
	proj      string
	repo      string
	tmp       string // Env.TempRoot: shadow directories are made and swept here
}

func newCLIFixture() *cliFixture {
	base, err := filepath.EvalSymlinks(GinkgoT().TempDir())
	Expect(err).NotTo(HaveOccurred())
	f := &cliFixture{
		fake: &execx.Fake{},
		out:  &bytes.Buffer{},
		errw: &bytes.Buffer{},
		home: filepath.Join(base, "home"),
		proj: filepath.Join(base, "proj"),
		repo: filepath.Join(base, "repo"),
		tmp:  filepath.Join(base, "tmp"),
	}
	for _, d := range []string{f.home, f.proj, f.repo, f.tmp} {
		Expect(os.MkdirAll(d, 0o755)).To(Succeed())
	}
	f.envmap = map[string]string{
		"HOME":                     f.home,
		"PROJECT_DIR":              f.proj,
		"CLAUDE_SANDBOX_REPO_ROOT": f.repo,
		"CLAUDE_SANDBOX_BASE_ONLY": "1",
	}
	f.lock = &fakeLock{fake: f.fake}
	f.env = &Env{
		Runner:   f.fake,
		Prompter: &prompt.Scripted{},
		Out:      f.out,
		Err:      f.errw,
		Getenv:   func(k string) string { return f.envmap[k] },
		// LookupEnv mirrors os.LookupEnv over envmap, so a key mapped to ""
		// is set-but-empty rather than unset (CS-CASC-027).
		LookupEnv: func(k string) (string, bool) { v, ok := f.envmap[k]; return v, ok },
		Lock:      f.lock,
		// Never the real temp root: a launch sweeps it (CS-LNCH-081).
		TempRoot: f.tmp,
	}
	return f
}

func (f *cliFixture) run(args ...string) int {
	return MainWithEnv(args, f.env)
}

// execLine renders the recorded exec hand-off (docker start, attach or exec)
// as one string.
func (f *cliFixture) execLine() string {
	Expect(f.fake.Execed).NotTo(BeNil(), "expected a docker hand-off; stderr:\n%s", f.errw.String())
	return f.fake.Execed.Name + " " + strings.Join(f.fake.Execed.Args, " ")
}

// launched returns the last "docker create" — the reserved container, carrying
// every flag, mount, env var and label of the launch (CS-LNCH-057).
func (f *cliFixture) launched() *execx.Cmd {
	for i := len(f.fake.Calls) - 1; i >= 0; i-- {
		c := f.fake.Calls[i]
		if c.Name == "docker" && len(c.Args) > 0 && c.Args[0] == "create" {
			return &c
		}
	}
	Fail("expected a docker create; stderr:\n" + f.errw.String())
	return nil
}

// launchLine renders the reserving docker create as one string.
func (f *cliFixture) launchLine() string {
	c := f.launched()
	return c.Name + " " + strings.Join(c.Args, " ")
}

func writeFile(p, content string) {
	Expect(os.MkdirAll(filepath.Dir(p), 0o755)).To(Succeed())
	Expect(os.WriteFile(p, []byte(content), 0o644)).To(Succeed())
}

var _ = Describe("launcher CLI (end-to-end argv)", func() {
	var f *cliFixture

	BeforeEach(func() {
		f = newCLIFixture()
	})

	It("CS-LNCH-002: exits 2 with \"unknown flag\" for an unknown flag", func() {
		Expect(f.run("--frobnicate")).To(Equal(2))
		Expect(f.errw.String()).To(ContainSubstring("unknown flag"))
		Expect(f.fake.Execed).To(BeNil())
	})

	It("CS-LNCH-002: appends a known claude flag and subsequent args to the container command", func() {
		Expect(f.run("--resume")).To(Equal(0))
		Expect(f.launchLine()).To(HaveSuffix(" claude-sandbox:run claude --resume"))
	})

	It("CS-IMG-024: the container runs the cap over the base, not the base itself", func() {
		Expect(f.run()).To(Equal(0))
		Expect(f.launched().Args).To(ContainElement("claude-sandbox:run"))
		Expect(f.launched().Args).NotTo(ContainElement("claude-sandbox"))
		Expect(f.fake.CommandLines()).To(ContainElement("docker build -t claude-sandbox:run -"))
	})

	It("CS-IMG-027: exits 2 naming docker-buildx-plugin when BuildKit is unavailable", func() {
		f.fake.On("docker buildx version", "", execx.Fail(1))
		Expect(f.run()).To(Equal(2))
		Expect(f.errw.String()).To(ContainSubstring("docker-buildx-plugin"))
		Expect(f.fake.Execed).To(BeNil())
		Expect(strings.Join(f.fake.CommandLines(), "\n")).NotTo(ContainSubstring("docker build "))
	})

	It("CS-IMG-028: the cache-budget check runs only when a build happened this launch", func() {
		// Everything fresh: no build, so no docker system df.
		f.fake.On("{{.Created}}", time.Now().Format(time.RFC3339Nano)+"\n", nil)
		Expect(f.run()).To(Equal(0))
		Expect(strings.Join(f.fake.CommandLines(), "\n")).NotTo(ContainSubstring("docker system df"))

		// The cap is missing on a second launch: a build happens and the check runs.
		g := newCLIFixture()
		g.fake.On("image inspect claude-sandbox:run", "", execx.Fail(1))
		Expect(g.run()).To(Equal(0))
		Expect(g.fake.CommandLines()).To(ContainElement("docker system df --format {{json .}}"))
	})

	It("CS-LNCH-004: --limit without --ralph exits 2 with an explanation", func() {
		Expect(f.run("--limit", "5")).To(Equal(2))
		Expect(f.errw.String()).To(ContainSubstring("--limit is only valid with --ralph"))
	})

	It("CS-LNCH-006: PROJECT_DIR overrides the working directory (mount + workdir)", func() {
		Expect(f.run()).To(Equal(0))
		args := f.launched().Args
		Expect(args).To(ContainElements("-w", f.proj))
		Expect(args).To(ContainElement(f.proj + ":" + f.proj))
	})

	Describe("the project directory is the physical path (CS-LNCH-048)", func() {
		var link string

		BeforeEach(func() {
			// f.proj is already symlink-free (the fixture resolves its temp
			// dir); link is a symlink to it, standing in for a checkout
			// reached through a linked path.
			link = filepath.Join(filepath.Dir(f.proj), "link")
			Expect(os.Symlink(f.proj, link)).To(Succeed())
		})

		// standIn puts the launcher in dir the way a shell would: cwd AND
		// $PWD, which os.Getwd honours — the very condition that made the
		// working-directory default return the logical path.
		standIn := func(dir string) {
			delete(f.envmap, "PROJECT_DIR")
			GinkgoT().Chdir(dir)
			GinkgoT().Setenv("PWD", dir)
		}

		expectPhysical := func() {
			args := f.launched().Args
			Expect(args).To(ContainElements("-w", f.proj))
			Expect(args).To(ContainElement(f.proj + ":" + f.proj))
			Expect(args).To(ContainElement("claude-sandbox.project=" + f.proj))
			Expect(f.launchLine()).To(MatchRegexp(
				`--name claude-sandbox-` + regexp.QuoteMeta(imagebuild.ProjectSlug(f.proj)) + `-[a-z]+ `))
			Expect(strings.Join(args, " ")).NotTo(ContainSubstring(link))
		}

		It("CS-LNCH-048: a symlinked working directory resolves to the physical path and says so", func() {
			standIn(link)
			Expect(f.run()).To(Equal(0))
			expectPhysical()
			Expect(f.out.String()).To(ContainSubstring("Project: " + f.proj + " (resolved from " + link + ")\n"))
		})

		It("CS-LNCH-048: PROJECT_DIR pointing at a symlink resolves the same way", func() {
			f.envmap["PROJECT_DIR"] = link
			Expect(f.run()).To(Equal(0))
			expectPhysical()
			Expect(f.out.String()).To(ContainSubstring("Project: " + f.proj + " (resolved from " + link + ")\n"))
		})

		It("CS-LNCH-048: an unsymlinked working directory prints no Project line", func() {
			standIn(f.proj)
			Expect(f.run()).To(Equal(0))
			expectPhysical()
			Expect(f.out.String()).NotTo(ContainSubstring("Project: "))
		})
	})

	It("CS-LNCH-026: interactive command shape and container name", func() {
		Expect(f.run("--dangerous", "--model", "opus", "--resume")).To(Equal(0))
		// The instance noun is chosen at random, so match its shape rather than
		// a fixed value; the project slug is derived from the fixture's temp dir.
		Expect(f.launchLine()).To(MatchRegexp(
			`--name claude-sandbox-` + regexp.QuoteMeta(imagebuild.ProjectSlug(f.proj)) +
				`-[a-z]+ claude-sandbox:run claude --dangerously-skip-permissions --model opus --resume$`))
	})

	It("CS-LNCH-027: ralph command shape, passthrough tail, and -ralph container name", func() {
		Expect(f.run("--ralph", "--limit", "5", "--dangerous", "--verbose")).To(Equal(0))
		Expect(f.launchLine()).To(HaveSuffix(
			"--name claude-sandbox-" + imagebuild.ProjectSlug(f.proj) +
				"-ralph claude-sandbox:run /opt/claude-sandbox/bin/ralph --limit 5 --dangerously-skip-permissions --verbose"))
	})

	It("CS-LNCH-029: container runtime environment", func() {
		Expect(f.run()).To(Equal(0))
		line := f.launchLine()
		Expect(line).To(HavePrefix("docker create -it --rm --init "))
		args := f.launched().Args
		uname := ""
		if u, err := user.Current(); err == nil {
			uname = u.Username
		}
		Expect(args).To(ContainElements(
			fmt.Sprintf("HOST_UID=%d", os.Getuid()),
			fmt.Sprintf("HOST_GID=%d", os.Getgid()),
			"HOST_USER="+uname,
			"HOST_HOME="+f.home,
			"HOME="+f.home,
			"DOCKER_GID=",
		))
	})

	It("CS-LNCH-106: an unset host ANTHROPIC_API_KEY leaves the env-file value in charge", func() {
		envFile := filepath.Join(f.proj, ".claude-sandbox", "env")
		writeFile(envFile, "ANTHROPIC_API_KEY=sk-ant-envfile-sentinel\n")
		Expect(f.run()).To(Equal(0))
		args := f.launched().Args
		for _, a := range args {
			Expect(a).NotTo(HavePrefix("ANTHROPIC_API_KEY"))
			Expect(a).NotTo(ContainSubstring("sk-ant-envfile-sentinel"))
		}
		Expect(args).To(ContainElements("--env-file", envFile))
	})

	It("CS-LNCH-024: prints the cascade report root-first with contributing files", func() {
		parent := filepath.Dir(f.proj)
		writeFile(filepath.Join(parent, ".claude-sandbox", "config.yaml"), "# defaults\n")
		writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "# local\n")
		writeFile(filepath.Join(f.proj, ".claude-sandbox", "env"), "")
		writeFile(filepath.Join(f.proj, ".claude-sandbox", "Dockerfile"), "FROM claude-sandbox\n")
		Expect(f.run()).To(Equal(0))
		out := f.out.String()
		Expect(out).To(ContainSubstring("Sandbox config cascade (root → project; later overrides earlier):"))
		Expect(out).To(ContainSubstring("config.yaml env Dockerfile (nearest wins)"))
		// Root level listed before the project level.
		Expect(strings.Index(out, parent+"/.claude-sandbox/")).To(BeNumerically("<",
			strings.Index(out, f.proj+"/.claude-sandbox/")))
		// The env file feeds an --env-file flag.
		Expect(f.launched().Args).To(ContainElements("--env-file", filepath.Join(f.proj, ".claude-sandbox", "env")))
	})

	It("CS-LNCH-025: warns and says where an env belongs when no env cascade exists, launching without --env-file", func() {
		Expect(f.run()).To(Equal(0))
		errOut := f.errw.String()
		Expect(errOut).To(ContainSubstring("WARNING: env file not found"))
		Expect(errOut).To(ContainSubstring("in a parent (workspace) directory"))
		Expect(errOut).To(ContainSubstring("project-only override"))
		Expect(errOut).To(ContainSubstring("claude-sandbox init   # seeds .claude-sandbox/env.example"))
		Expect(errOut).NotTo(ContainSubstring("creates .claude-sandbox/env"))
		Expect(f.launched().Args).NotTo(ContainElement("--env-file"))
	})

	It("CS-LNCH-056: a project with only env.example gets a one-line note, not a warning", func() {
		example := filepath.Join(f.proj, ".claude-sandbox", "env.example")
		writeFile(example, "# TOKEN=x\n")
		Expect(f.run()).To(Equal(0))
		errOut := f.errw.String()
		Expect(errOut).NotTo(ContainSubstring("WARNING"))
		note := "Note: no .claude-sandbox/env in the cascade; " + example + " is a template and is not read."
		Expect(strings.Count(errOut, note)).To(Equal(1))
		Expect(f.launched().Args).NotTo(ContainElement("--env-file"))
		Expect(f.launched().Args).NotTo(ContainElement(example))

		By("exactly one stderr line more than a launch with a clean project env")
		base := newCLIFixture()
		writeFile(filepath.Join(base.proj, ".claude-sandbox", "env"), "CLEAN=value\n")
		Expect(base.run()).To(Equal(0))
		lines := func(s string) []string {
			var out []string
			for _, l := range strings.Split(s, "\n") {
				if strings.TrimSpace(l) != "" {
					out = append(out, strings.ReplaceAll(l, base.proj, f.proj))
				}
			}
			return out
		}
		got, want := lines(errOut), lines(base.errw.String())
		Expect(got).To(HaveLen(len(want) + 1))
		Expect(got).To(ContainElements(want))
		Expect(got).To(ContainElement(note))

		By("an env.example only in a parent directory does not count")
		g := newCLIFixture()
		writeFile(filepath.Join(filepath.Dir(g.proj), ".claude-sandbox", "env.example"), "# TOKEN=x\n")
		Expect(g.run()).To(Equal(0))
		Expect(g.errw.String()).To(ContainSubstring("WARNING: env file not found"))
		Expect(g.errw.String()).NotTo(ContainSubstring("Note: no .claude-sandbox/env"))
	})

	It("CS-LNCH-030: --version reports host and baked-image versions with a mismatch note", func() {
		f.fake.On("describe --tags --always --dirty", "v2.0.0\n", nil)
		f.fake.On("image.revision", "v1.9.0\n", nil)
		f.fake.On("{{.Created}}", time.Now().Format(time.RFC3339Nano)+"\n", nil)
		Expect(f.run("--version")).To(Equal(0))
		out := f.out.String()
		Expect(out).To(ContainSubstring("claude-sandbox v2.0.0"))
		Expect(out).To(ContainSubstring("v1.9.0"))
		Expect(out).To(ContainSubstring("auto-rebuild"))
		Expect(f.fake.Execed).To(BeNil())
	})

	It("CS-LNCH-030: --version prints \"(not built yet)\" when the images do not exist", func() {
		f.fake.On("describe --tags --always --dirty", "v2.0.0\n", nil)
		f.fake.On("docker image inspect claude-sandbox", "", execx.Fail(1))
		Expect(f.run("--version")).To(Equal(0))
		Expect(f.out.String()).To(ContainSubstring("(not built yet)"))
	})

	It("CS-LNCH-030: --version prints the Claude Code version pinned in the CLI image", func() {
		f.fake.On("describe --tags --always --dirty", "v2.0.0\n", nil)
		f.fake.On("image.revision", "v2.0.0\n", nil)
		f.fake.On("claude-sandbox.claude-version", "2.1.247\n", nil)
		f.fake.On("{{.Created}}", time.Now().Format(time.RFC3339Nano)+"\n", nil)
		Expect(f.run("--version")).To(Equal(0))
		Expect(f.out.String()).To(ContainSubstring("claude:       2.1.247"))
	})

	Describe("update-check skips (CS-IMG-007 resolution in runLaunch)", func() {
		stubVersions := func() {
			f.fake.On("claude-sandbox.claude-version", "1.2.3\n", nil)
			f.fake.On("npm view @anthropic-ai/claude-code version", "1.2.3\n", nil)
		}

		npmCalled := func() bool {
			for _, l := range f.fake.CommandLines() {
				if strings.Contains(l, "npm view") {
					return true
				}
			}
			return false
		}

		It("CS-IMG-007: CLAUDE_SANDBOX_NO_UPDATE_CHECK=1 skips the version comparison", func() {
			stubVersions()
			f.envmap["CLAUDE_SANDBOX_NO_UPDATE_CHECK"] = "1"
			Expect(f.run()).To(Equal(0))
			Expect(npmCalled()).To(BeFalse())
		})

		It("CS-IMG-007: config disableUpdateCheck: true skips the version comparison", func() {
			stubVersions()
			writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "disableUpdateCheck: true\n")
			Expect(f.run()).To(Equal(0))
			Expect(npmCalled()).To(BeFalse())
		})

		It("CS-IMG-007: --no-update-check skips the version comparison", func() {
			stubVersions()
			Expect(f.run("--no-update-check")).To(Equal(0))
			Expect(npmCalled()).To(BeFalse())
		})

		It("CS-IMG-006: without a skip, the launch compares baked and registry versions", func() {
			stubVersions()
			Expect(f.run()).To(Equal(0))
			Expect(npmCalled()).To(BeTrue())
		})
	})

	Describe("dangerous mode from config or environment (CS-LNCH-038)", func() {
		It("CS-LNCH-038: config dangerous: true adds --dangerously-skip-permissions", func() {
			writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "dangerous: true\n")
			Expect(f.run()).To(Equal(0))
			Expect(f.launchLine()).To(HaveSuffix(" claude-sandbox:run claude --dangerously-skip-permissions"))
		})

		It("CS-LNCH-038: CLAUDE_SANDBOX_DANGEROUS=1 adds --dangerously-skip-permissions", func() {
			f.envmap["CLAUDE_SANDBOX_DANGEROUS"] = "1"
			Expect(f.run()).To(Equal(0))
			Expect(f.launchLine()).To(HaveSuffix(" claude-sandbox:run claude --dangerously-skip-permissions"))
		})

		It("CS-LNCH-038: ralph mode forwards the config-enabled flag the same way", func() {
			writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "dangerous: true\n")
			Expect(f.run("--ralph")).To(Equal(0))
			Expect(f.launchLine()).To(HaveSuffix(
				" claude-sandbox:run /opt/claude-sandbox/bin/ralph --dangerously-skip-permissions"))
		})

		It("CS-LNCH-038: a more-local dangerous: false overrides an upstream true", func() {
			writeFile(filepath.Join(filepath.Dir(f.proj), ".claude-sandbox", "config.yaml"), "dangerous: true\n")
			writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "dangerous: false\n")
			Expect(f.run()).To(Equal(0))
			Expect(f.launchLine()).NotTo(ContainSubstring("--dangerously-skip-permissions"))
		})
	})

	It("CS-IMG-012: the env var Dockerfile override wins over the config key", func() {
		dirA := filepath.Join(f.proj, "docker-a")
		dirB := filepath.Join(f.proj, "docker-b")
		writeFile(filepath.Join(dirA, "Dockerfile"), "FROM claude-sandbox\n")
		writeFile(filepath.Join(dirB, "Dockerfile"), "FROM claude-sandbox\n")
		writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "dockerfileDir: "+dirA+"\n")
		f.envmap["CLAUDE_SANDBOX_BASE_ONLY"] = ""
		f.envmap["CLAUDE_SANDBOX_DOCKERFILE_DIR"] = dirB
		// The tag now derives from the (Dockerfile, context) pair, so name it the
		// same way the launcher does rather than from the project.
		tag := "claude-sandbox-" + imagebuild.ImageSlug(filepath.Join(dirB, "Dockerfile"), dirB)
		f.fake.On("image inspect "+tag, "", execx.Fail(1)) // force the child build
		Expect(f.run()).To(Equal(0))
		Expect(f.fake.CommandLines()).To(ContainElement(
			"docker build -t " + tag + " -f " + filepath.Join(dirB, "Dockerfile") + " " + dirB))
	})
})
