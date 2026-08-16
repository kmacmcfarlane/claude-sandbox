package main

// Spec: spec/launch.feature + spec/image-build.feature — end-to-end launcher
// scenarios through MainWithEnv with a fully scripted execx.Fake: docker
// inspect/build/npm/git are all faked and the final docker run hand-off is
// recorded in Fake.Execed. HOME/PROJECT_DIR/repo root come from a Getenv map
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

// cliFixture wires MainWithEnv to temp dirs and a recording fake.
type cliFixture struct {
	env       *Env
	fake      *execx.Fake
	out, errw *bytes.Buffer
	envmap    map[string]string
	home      string
	proj      string
	repo      string
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
	}
	for _, d := range []string{f.home, f.proj, f.repo} {
		Expect(os.MkdirAll(d, 0o755)).To(Succeed())
	}
	f.envmap = map[string]string{
		"HOME":                     f.home,
		"PROJECT_DIR":              f.proj,
		"CLAUDE_SANDBOX_REPO_ROOT": f.repo,
		"CLAUDE_SANDBOX_BASE_ONLY": "1",
	}
	f.env = &Env{
		Runner:   f.fake,
		Prompter: &prompt.Scripted{},
		Out:      f.out,
		Err:      f.errw,
		Getenv:   func(k string) string { return f.envmap[k] },
	}
	return f
}

func (f *cliFixture) run(args ...string) int {
	return MainWithEnv(args, f.env)
}

// execLine renders the recorded docker run hand-off as one string.
func (f *cliFixture) execLine() string {
	Expect(f.fake.Execed).NotTo(BeNil(), "expected a docker run hand-off; stderr:\n%s", f.errw.String())
	return f.fake.Execed.Name + " " + strings.Join(f.fake.Execed.Args, " ")
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
		Expect(f.execLine()).To(HaveSuffix(" claude-sandbox claude --resume"))
	})

	It("CS-LNCH-004: --limit without --ralph exits 2 with an explanation", func() {
		Expect(f.run("--limit", "5")).To(Equal(2))
		Expect(f.errw.String()).To(ContainSubstring("--limit is only valid with --ralph"))
	})

	It("CS-LNCH-006: PROJECT_DIR overrides the working directory (mount + workdir)", func() {
		Expect(f.run()).To(Equal(0))
		args := f.fake.Execed.Args
		Expect(args).To(ContainElements("-w", f.proj))
		Expect(args).To(ContainElement(f.proj + ":" + f.proj))
	})

	It("CS-LNCH-026: interactive command shape and container name", func() {
		Expect(f.run("--dangerous", "--model", "opus", "--resume")).To(Equal(0))
		// The instance noun is chosen at random, so match its shape rather than
		// a fixed value; the project slug is derived from the fixture's temp dir.
		Expect(f.execLine()).To(MatchRegexp(
			`--name claude-sandbox-` + regexp.QuoteMeta(imagebuild.ProjectSlug(f.proj)) +
				`-[a-z]+ claude-sandbox claude --dangerously-skip-permissions --model opus --resume$`))
	})

	It("CS-LNCH-027: ralph command shape, passthrough tail, and -ralph container name", func() {
		Expect(f.run("--ralph", "--limit", "5", "--dangerous", "--verbose")).To(Equal(0))
		Expect(f.execLine()).To(HaveSuffix(
			"--name claude-sandbox-" + imagebuild.ProjectSlug(f.proj) +
				"-ralph claude-sandbox /opt/claude-sandbox/bin/ralph --limit 5 --dangerously-skip-permissions --verbose"))
	})

	It("CS-LNCH-029: container runtime environment", func() {
		Expect(f.run()).To(Equal(0))
		line := f.execLine()
		Expect(line).To(HavePrefix("docker run -it --rm --init "))
		args := f.fake.Execed.Args
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
			"ANTHROPIC_API_KEY=",
		))
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
		Expect(f.fake.Execed.Args).To(ContainElements("--env-file", filepath.Join(f.proj, ".claude-sandbox", "env")))
	})

	It("CS-LNCH-025: warns and suggests init when no env cascade exists, launching without --env-file", func() {
		Expect(f.run()).To(Equal(0))
		Expect(f.errw.String()).To(ContainSubstring("env file not found"))
		Expect(f.errw.String()).To(ContainSubstring("claude-sandbox init"))
		Expect(f.fake.Execed.Args).NotTo(ContainElement("--env-file"))
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

	It("CS-LNCH-030: --version prints \"(not built yet)\" when the image does not exist", func() {
		f.fake.On("describe --tags --always --dirty", "v2.0.0\n", nil)
		f.fake.On("docker image inspect claude-sandbox", "", execx.Fail(1))
		Expect(f.run("--version")).To(Equal(0))
		Expect(f.out.String()).To(ContainSubstring("(not built yet)"))
	})

	Describe("update-check skips (CS-IMG-007 resolution in runLaunch)", func() {
		stubVersions := func() {
			f.fake.On("/opt/claude-sandbox/claude-version", "1.2.3\n", nil)
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
