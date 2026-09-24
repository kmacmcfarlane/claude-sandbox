package main

// Spec: spec/launch.feature + spec/image-build.feature — end-to-end launcher
// scenarios through MainWithEnv with a fully scripted execx.Fake: docker
// inspect/build/npm/git are all faked, the reserving "docker create" is a
// recorded call (launched) and the final "docker start" hand-off is recorded
// in Fake.Session. HOME/PROJECT_DIR/repo root come from a Getenv map
// pointed at temp dirs.

import (
	"bytes"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
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
	cache     string // Env.CacheDir: the cache-budget result file (CS-IMG-043)
}

func newCLIFixture() *cliFixture {
	base, err := filepath.EvalSymlinks(GinkgoT().TempDir())
	Expect(err).NotTo(HaveOccurred())
	f := &cliFixture{
		fake:  &execx.Fake{},
		out:   &bytes.Buffer{},
		errw:  &bytes.Buffer{},
		home:  filepath.Join(base, "home"),
		proj:  filepath.Join(base, "proj"),
		repo:  filepath.Join(base, "repo"),
		tmp:   filepath.Join(base, "tmp"),
		cache: filepath.Join(base, "cache"),
	}
	for _, d := range []string{f.home, f.proj, f.repo, f.tmp, f.cache} {
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
		// Nor the real cache-budget result file (CS-IMG-043); the detached
		// checker is recorded by the fake, never spawned (CS-IMG-041).
		CacheDir:   f.cache,
		Executable: func() (string, error) { return "/fake/claude-sandbox", nil },
	}
	return f
}

func (f *cliFixture) run(args ...string) int {
	return MainWithEnv(args, f.env)
}

// execLine renders the recorded exec hand-off (docker start, attach or exec)
// as one string.
func (f *cliFixture) sessionLine() string {
	Expect(f.fake.Session).NotTo(BeNil(), "expected a docker session child; stderr:\n%s", f.errw.String())
	return f.fake.Session.Name + " " + strings.Join(f.fake.Session.Args, " ")
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
		Expect(f.fake.Session).To(BeNil())
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

	// toolsMissing matches EnsureTools' single inspect of the tools image,
	// which answers existence and the build-inputs label at once.
	const toolsMissing = `build-inputs" }} claude-sandbox-tools`

	// buildTags returns the image tag of every docker build, in order.
	buildTags := func(g *cliFixture) []string {
		var tags []string
		for _, l := range g.fake.CommandLines() {
			if strings.HasPrefix(l, "docker build -t ") {
				tags = append(tags, strings.Fields(l)[3])
			}
		}
		return tags
	}

	It("CS-IMG-049: a launch builds a missing tools image after the base and before the cap copies it", func() {
		f.fake.On(toolsMissing, "", execx.Fail(1))
		f.fake.On("image inspect claude-sandbox:run", "", execx.Fail(1))
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(buildTags(f)).To(Equal([]string{"claude-sandbox-tools", "claude-sandbox:run"}))
		var tools string
		for _, l := range f.fake.CommandLines() {
			if strings.HasPrefix(l, "docker build -t claude-sandbox-tools ") {
				tools = l
			}
		}
		Expect(tools).To(ContainSubstring(" -f " + filepath.Join(f.repo, "Dockerfile.tools") + " " + f.repo))
		Expect(tools).To(ContainSubstring("--build-arg CLAUDE_SANDBOX_VERSION="))
		Expect(f.launched().Args).To(ContainElement("claude-sandbox:run"))
	})

	It("CS-IMG-002: --rebuild rebuilds all five images, the base and the tools image with --no-cache", func() {
		f.envmap["CLAUDE_SANDBOX_BASE_ONLY"] = ""
		writeFile(filepath.Join(f.proj, ".claude-sandbox", "Dockerfile"), "FROM claude-sandbox\n")
		Expect(f.run("--rebuild")).To(Equal(0), f.errw.String())
		tags := buildTags(f)
		Expect(tags).To(HaveLen(5))
		Expect(tags[:3]).To(Equal([]string{"claude-sandbox", "claude-sandbox-tools", "claude-sandbox-cli"}))
		Expect(tags[3]).To(HavePrefix("claude-sandbox-df-"))
		Expect(tags[4]).To(Equal(tags[3] + ":run"))
		for _, l := range f.fake.CommandLines() {
			if strings.HasPrefix(l, "docker build -t claude-sandbox ") || strings.HasPrefix(l, "docker build -t claude-sandbox-tools ") {
				Expect(l).To(ContainSubstring("--no-cache"))
			}
		}
	})

	It("CS-IMG-027: exits 2 naming docker-buildx-plugin when BuildKit is unavailable", func() {
		f.fake.On("docker buildx version", "", execx.Fail(1))
		Expect(f.run()).To(Equal(2))
		Expect(f.errw.String()).To(ContainSubstring("docker-buildx-plugin"))
		Expect(f.fake.Session).To(BeNil())
		Expect(strings.Join(f.fake.CommandLines(), "\n")).NotTo(ContainSubstring("docker build "))
	})

	Describe("build-cache budget (CS-IMG-041, CS-IMG-043)", func() {
		// detached returns the recorded starts of the detached checker.
		detached := func(g *cliFixture) []execx.Cmd {
			var out []execx.Cmd
			for _, c := range g.fake.Calls {
				if c.Detach {
					out = append(out, c)
				}
			}
			return out
		}
		noDockerDF := func(g *cliFixture) {
			lines := strings.Join(g.fake.CommandLines(), "\n")
			Expect(lines).NotTo(ContainSubstring("system df"))
			Expect(lines).NotTo(ContainSubstring("buildx inspect"))
		}

		It("CS-IMG-041: a launch that built starts the checker detached and runs no docker system df itself", func() {
			f.fake.On("image inspect claude-sandbox:run", "", execx.Fail(1))
			Expect(f.run()).To(Equal(0), f.errw.String())
			Expect(f.fake.CommandLines()).To(ContainElement("docker build -t claude-sandbox:run -"))
			starts := detached(f)
			Expect(starts).To(HaveLen(1))
			Expect(starts[0].Name).To(Equal("/fake/claude-sandbox"))
			Expect(starts[0].Args).To(Equal([]string{"cache-budget-check", "--dir", f.cache}))
			Expect(starts[0].DieWithParent).To(BeFalse(), "the checker must outlive the launcher")
			Expect(starts[0].Stdout).To(BeNil())
			Expect(starts[0].Stderr).To(BeNil())
			noDockerDF(f)
			Expect(f.fake.Session).NotTo(BeNil(), "the session still starts")
		})

		It("CS-IMG-041: no build, no checker", func() {
			f.fake.On("{{.Created}}", time.Now().Format(time.RFC3339Nano)+"\n", nil)
			Expect(f.run()).To(Equal(0), f.errw.String())
			Expect(strings.Join(f.fake.CommandLines(), "\n")).NotTo(ContainSubstring("docker build "))
			Expect(detached(f)).To(BeEmpty())
			noDockerDF(f)
		})

		It("CS-IMG-041: a launch whose only build is the tools image starts the checker too", func() {
			f.fake.On("{{.Created}}", time.Now().Format(time.RFC3339Nano)+"\n", nil)
			f.fake.On(toolsMissing, "", execx.Fail(1))
			Expect(f.run()).To(Equal(0), f.errw.String())
			Expect(buildTags(f)).To(Equal([]string{"claude-sandbox-tools"}))
			Expect(detached(f)).To(HaveLen(1))
		})

		It("CS-IMG-041: --rebuild takes the same detached path", func() {
			Expect(f.run("--rebuild")).To(Equal(0), f.errw.String())
			Expect(detached(f)).To(HaveLen(1))
			noDockerDF(f)
		})

		It("CS-IMG-041: a checker that cannot be started is ignored", func() {
			f.env.Executable = func() (string, error) { return "", fmt.Errorf("no executable") }
			f.fake.On("image inspect claude-sandbox:run", "", execx.Fail(1))
			Expect(f.run()).To(Equal(0), f.errw.String())
			Expect(detached(f)).To(BeEmpty())
			Expect(f.fake.Session).NotTo(BeNil())
		})

		It("CS-IMG-043: the previous check's report prints once on stderr, before the container starts", func() {
			writeFile(filepath.Join(f.cache, imagebuild.CacheBudgetFile),
				`{"checkedAt":"2026-09-21T18:03:00Z","report":"\nWARNING: BuildKit build cache is 625 GB of its 719 GB budget.\n  Prune:  docker builder prune -af\n"}`)
			var atStart string
			f.fake.OnFunc("docker start", func(c execx.Cmd) (string, error) {
				atStart = f.errw.String()
				return "", nil
			})
			f.fake.On("{{.Created}}", time.Now().Format(time.RFC3339Nano)+"\n", nil)
			Expect(f.run()).To(Equal(0), f.errw.String())
			Expect(atStart).To(ContainSubstring("Build-cache check after the image build of"))
			Expect(atStart).To(ContainSubstring("625 GB of its 719 GB budget"))
			Expect(f.out.String()).NotTo(ContainSubstring("WARNING"), "stderr only")
			Expect(filepath.Join(f.cache, imagebuild.CacheBudgetFile)).NotTo(BeAnExistingFile())
			noDockerDF(f)

			// Once: a second launch has nothing to print.
			f.errw.Reset()
			Expect(f.run()).To(Equal(0))
			Expect(f.errw.String()).NotTo(ContainSubstring("Build-cache check"))
		})

		It("CS-IMG-042: the hidden checker subcommand writes the result file; on contention it skips without docker", func() {
			dir := filepath.Join(f.tmp, "budget")
			f.fake.On("docker system df --format {{json .}}", `{"Size":"10GB","Type":"Build Cache"}`+"\n", nil)
			f.fake.On("docker buildx inspect", "GC Policy rule#0:\n All: false\n Filters: type==exec.cachemount\n Max Used Space: 2.764GB\n"+
				"GC Policy rule#1:\n All: true\n Max Used Space: 669.6GiB\n", nil)
			Expect(f.run("cache-budget-check", "--dir", dir)).To(Equal(0), f.errw.String())
			data, err := os.ReadFile(filepath.Join(dir, imagebuild.CacheBudgetFile))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(data)).To(ContainSubstring("caps cache mounts at 3 GB"))
			Expect(f.fake.Session).To(BeNil(), "the checker launches nothing")

			// Another checker holds the lock: skip at once, run nothing.
			held, err := os.OpenFile(filepath.Join(dir, imagebuild.CacheBudgetLockFile), os.O_RDWR, 0)
			Expect(err).NotTo(HaveOccurred())
			defer held.Close()
			Expect(syscall.Flock(int(held.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)).To(Succeed())
			Expect(os.Remove(filepath.Join(dir, imagebuild.CacheBudgetFile))).To(Succeed())
			g := newCLIFixture()
			Expect(g.run("cache-budget-check", "--dir", dir)).To(Equal(0), g.errw.String())
			Expect(g.errw.String()).To(ContainSubstring("skipped"))
			Expect(g.fake.CommandLines()).To(BeEmpty())
			Expect(filepath.Join(dir, imagebuild.CacheBudgetFile)).NotTo(BeAnExistingFile())
		})

		It("CS-IMG-041: the checker subcommand is hidden from help and completion", func() {
			Expect(f.run("--help")).To(Equal(0))
			Expect(f.out.String()).NotTo(ContainSubstring("cache-budget-check"))

			// Completion: the empty word offers subcommands, but not this one.
			g := newCLIFixture()
			all := g.complete("")
			Expect(all.has("sessions")).To(BeTrue(), "the empty word must offer subcommands for this to mean anything")
			Expect(all.has("cache-budget-check")).To(BeFalse())
			h := newCLIFixture()
			Expect(h.complete("cache").names).NotTo(ContainElement("cache-budget-check"))
		})

		It("CS-IMG-043: the result is read before this launch's own checker starts", func() {
			writeFile(filepath.Join(f.cache, imagebuild.CacheBudgetFile), `{"checkedAt":"2026-09-21T18:03:00Z","report":"\nNOTE: old\n"}`)
			consumedBeforeStart := false
			f.fake.OnFunc("cache-budget-check", func(c execx.Cmd) (string, error) {
				_, err := os.Stat(filepath.Join(f.cache, imagebuild.CacheBudgetFile))
				consumedBeforeStart = os.IsNotExist(err)
				return "", nil
			})
			f.fake.On("image inspect claude-sandbox:run", "", execx.Fail(1))
			Expect(f.run()).To(Equal(0), f.errw.String())
			Expect(consumedBeforeStart).To(BeTrue())
			Expect(f.errw.String()).To(ContainSubstring("NOTE: old"))
		})
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
		Expect(envFileContents(args)).To(Equal([]string{"ANTHROPIC_API_KEY=sk-ant-envfile-sentinel\n"}))
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
		// The env file feeds an --env-file flag (a snapshot copy, CS-LNCH-132).
		Expect(envFileContents(f.launched().Args)).To(Equal([]string{""}))
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
		Expect(out).To(ContainSubstring("rebuilds when a baked source changes"))
		Expect(f.fake.Session).To(BeNil())
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

	Describe("background CLI prefetch (CS-IMG-044..047 through runLaunch)", func() {
		cacheDir := func() string { return f.cache }
		// detached returns the background CLI builds started through
		// Runner.Start with Cmd.Detach (the cache-budget checker is another).
		detached := func() []execx.Cmd {
			var out []execx.Cmd
			for _, c := range f.fake.Calls {
				if c.Detach && len(c.Args) > 0 && c.Args[0] == imagebuild.PrefetchSubcommand {
					out = append(out, c)
				}
			}
			return out
		}
		cliBuilds := func() []string {
			var out []string
			for _, l := range f.fake.CommandLines() {
				if strings.HasPrefix(l, "docker build -t claude-sandbox-cli ") {
					out = append(out, l)
				}
			}
			return out
		}

		BeforeEach(func() {
			f.env.Executable = func() (string, error) { return "/opt/bin/claude-sandbox", nil }
			f.fake.On("claude-sandbox.claude-version", "1.2.3\n", nil)
			f.fake.On("npm view @anthropic-ai/claude-code version", "1.2.4\n", nil)
		})

		It("CS-IMG-045: a newer version launches on the current image and starts cli-prefetch detached", func() {
			Expect(f.run()).To(Equal(0), f.errw.String())
			Expect(cliBuilds()).To(BeEmpty(), "nothing is built in the foreground")
			Expect(f.launchLine()).To(ContainSubstring(" claude-sandbox:run "))
			Expect(detached()).To(HaveLen(1))
			c := detached()[0]
			Expect(c.Name).To(Equal("/opt/bin/claude-sandbox"))
			Expect(c.Args).To(Equal([]string{"cli-prefetch", "--dir", cacheDir(), "1.2.4"}))
			Expect(c.Stdout.(*os.File).Name()).To(Equal(filepath.Join(cacheDir(), "cli-prefetch.log")))
			Expect(c.Env).To(ContainElement("CLAUDE_SANDBOX_REPO_ROOT=" + f.repo))
			Expect(f.out.String()).To(ContainSubstring("1.2.3 → 1.2.4; building it in the background"))
		})

		It("CS-IMG-044: the version is cached under ~/.cache/claude-sandbox and the next launch skips npm", func() {
			Expect(f.run()).To(Equal(0), f.errw.String())
			_, err := os.Stat(filepath.Join(cacheDir(), "claude-version.json"))
			Expect(err).NotTo(HaveOccurred())

			f.fake.Calls = nil
			Expect(f.run("--new")).To(Equal(0), f.errw.String())
			for _, l := range f.fake.CommandLines() {
				Expect(l).NotTo(ContainSubstring("npm view"))
			}
			Expect(detached()).To(HaveLen(1), "the update is still pending, so the second launch prefetches too")
		})

		It("CS-IMG-009: --update builds the CLI image in the foreground and starts nothing in the background", func() {
			Expect(f.run("--update")).To(Equal(0), f.errw.String())
			Expect(cliBuilds()).To(HaveLen(1))
			Expect(cliBuilds()[0]).To(ContainSubstring("CLAUDE_CODE_VERSION=1.2.4"))
			Expect(detached()).To(BeEmpty())
		})

		It("CS-IMG-045: cli-prefetch builds ONLY the CLI image and records the outcome under HOME", func() {
			f.fake.Calls = nil
			Expect(f.run("cli-prefetch", "--dir", cacheDir(), "1.2.4")).To(Equal(0), f.errw.String())
			var builds []string
			for _, l := range f.fake.CommandLines() {
				if strings.HasPrefix(l, "docker build") {
					builds = append(builds, l)
				}
			}
			Expect(builds).To(HaveLen(1))
			Expect(builds[0]).To(HavePrefix("docker build -t claude-sandbox-cli:prefetch "))
			Expect(f.fake.CommandLines()).To(ContainElement("docker tag claude-sandbox-cli:prefetch claude-sandbox-cli"))
			Expect(builds[0]).To(ContainSubstring("CLAUDE_CODE_VERSION=1.2.4"))
			Expect(builds[0]).To(ContainSubstring(filepath.Join(f.repo, "Dockerfile.cli")))
			raw, err := os.ReadFile(filepath.Join(cacheDir(), "cli-prefetch.json"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(raw)).To(ContainSubstring(`"ok":true`))
			Expect(f.fake.Session).To(BeNil(), "cli-prefetch never launches a session")
		})

		It("CS-IMG-047: a failed cli-prefetch exits non-zero, and the next launch warns naming the log and still launches", func() {
			f.fake.On("docker build -t claude-sandbox-cli", "", execx.Fail(1))
			Expect(f.run("cli-prefetch", "1.2.4")).To(Equal(1), "without --dir it uses the launcher's cache dir")
			f.errw.Reset()
			Expect(f.run()).To(Equal(0), f.errw.String())
			Expect(f.errw.String()).To(ContainSubstring("background build of Claude Code 1.2.4 failed"))
			Expect(f.errw.String()).To(ContainSubstring(filepath.Join(cacheDir(), "cli-prefetch.log")))
			Expect(detached()).To(BeEmpty(), "the failed version is not retried within 6 h")
			Expect(f.fake.Session).NotTo(BeNil())
		})

		It("CS-IMG-045: cli-prefetch wants exactly one X.Y.Z argument and is hidden from help", func() {
			Expect(f.run("cli-prefetch")).To(Equal(2))
			Expect(f.run("cli-prefetch", "latest")).To(Equal(1))
			f.out.Reset()
			Expect(f.run("help")).To(Equal(0))
			Expect(f.out.String()).NotTo(ContainSubstring("cli-prefetch"))
			f.out.Reset()
			Expect(f.run("__complete", "")).To(Equal(0))
			Expect(f.out.String()).To(ContainSubstring("sessions"), "the completion lists subcommands")
			Expect(f.out.String()).NotTo(ContainSubstring("cli-prefetch"))
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
