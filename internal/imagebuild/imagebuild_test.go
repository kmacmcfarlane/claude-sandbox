package imagebuild_test

// Spec: spec/image-build.feature (CS-IMG) plus the --version report
// (CS-LNCH-030). All docker/git/npm traffic goes through execx.Fake; image
// existence and creation timestamps are scripted per image name, and file
// staleness is driven with os.Chtimes against real temp files.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/imagebuild"
	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
)

// imgState scripts one image's docker inspect responses.
type imgState struct {
	created  time.Time
	revision string
}

// touch creates the file (and parents) and sets its mtime.
func touchAt(p string, mtime time.Time) {
	Expect(os.MkdirAll(filepath.Dir(p), 0o755)).To(Succeed())
	Expect(os.WriteFile(p, []byte("x\n"), 0o644)).To(Succeed())
	Expect(os.Chtimes(p, mtime, mtime)).To(Succeed())
}

func buildLines(f *execx.Fake) []string {
	var out []string
	for _, l := range f.CommandLines() {
		if strings.HasPrefix(l, "docker build ") {
			out = append(out, l)
		}
	}
	return out
}

var _ = Describe("image build lifecycle", func() {
	var (
		fake      *execx.Fake
		images    map[string]*imgState
		out, errw *bytes.Buffer
		repo      string
		o         imagebuild.Options
		imgT, old time.Time
	)

	BeforeEach(func() {
		fake = &execx.Fake{}
		images = map[string]*imgState{}
		out = &bytes.Buffer{}
		errw = &bytes.Buffer{}
		imgT = time.Now().Add(-time.Hour)
		old = imgT.Add(-time.Hour)

		// One dynamic stub answers every "docker image inspect" form (exists,
		// {{.Created}}, revision label) from the images map.
		fake.OnFunc("docker image inspect", func(c execx.Cmd) (string, error) {
			name := c.Args[len(c.Args)-1]
			st := images[name]
			if st == nil {
				return "", execx.Fail(1)
			}
			joined := strings.Join(c.Args, " ")
			if strings.Contains(joined, "{{.Created}}") {
				return st.created.Format(time.RFC3339Nano) + "\n", nil
			}
			if strings.Contains(joined, "image.revision") {
				return st.revision + "\n", nil
			}
			return "", nil
		})

		repo = GinkgoT().TempDir()
		touchAt(filepath.Join(repo, "Dockerfile"), old)
		touchAt(filepath.Join(repo, "cmd", "main.go"), old)
		touchAt(filepath.Join(repo, "entrypoint.sh"), old)

		o = imagebuild.Options{
			Runner: fake, Prompter: &prompt.Scripted{}, Out: out, Err: errw,
			RepoRoot: repo, Version: "v1.2.3",
		}
	})

	// ---- base image ----

	Describe("EnsureBase", func() {
		It("CS-IMG-001: builds the base when the image is missing", func() {
			rebuilt, err := imagebuild.EnsureBase(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeTrue())
			Expect(buildLines(fake)).To(ContainElement(
				"docker build -t claude-sandbox --build-arg CLAUDE_SANDBOX_VERSION=v1.2.3 " + repo))
		})

		It("CS-IMG-002: --rebuild forces a base rebuild with --no-cache", func() {
			images["claude-sandbox"] = &imgState{created: imgT}
			o.ForceRebuild = true
			rebuilt, err := imagebuild.EnsureBase(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeTrue())
			Expect(buildLines(fake)).To(ContainElement(
				"docker build -t claude-sandbox --build-arg CLAUDE_SANDBOX_VERSION=v1.2.3 --no-cache " + repo))
		})

		It("CS-IMG-003: rebuilds when the Dockerfile is newer than the image", func() {
			images["claude-sandbox"] = &imgState{created: imgT}
			touchAt(filepath.Join(repo, "Dockerfile"), time.Now())
			rebuilt, err := imagebuild.EnsureBase(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeTrue())
			Expect(buildLines(fake)).To(HaveLen(1))
		})

		It("CS-IMG-003: does not rebuild when nothing is newer than the image", func() {
			images["claude-sandbox"] = &imgState{created: imgT}
			rebuilt, err := imagebuild.EnsureBase(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeFalse())
			Expect(buildLines(fake)).To(BeEmpty())
		})

		It("CS-IMG-004: rebuilds when any baked source is newer than the image", func() {
			images["claude-sandbox"] = &imgState{created: imgT}
			touchAt(filepath.Join(repo, "cmd", "main.go"), time.Now())
			rebuilt, err := imagebuild.EnsureBase(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeTrue())
			Expect(out.String()).To(ContainSubstring("Baked sources changed"))
		})
	})

	Describe("Version", func() {
		It("CS-IMG-005: carries git describe --tags --always --dirty of the checkout", func() {
			fake.On("git -C "+repo+" describe --tags --always --dirty", "v1.2.3-4-gabc123-dirty\n", nil)
			Expect(imagebuild.Version(fake, repo)).To(Equal("v1.2.3-4-gabc123-dirty"))
		})

		It("CS-IMG-005: falls back to \"unknown\" outside a git repo", func() {
			fake.On("git", "", execx.Fail(128))
			Expect(imagebuild.Version(fake, repo)).To(Equal("unknown"))
		})

		It("CS-IMG-005: falls back to \"unknown\" on empty git output", func() {
			fake.On("git", "  \n", nil)
			Expect(imagebuild.Version(fake, repo)).To(Equal("unknown"))
		})
	})

	// ---- Claude Code update check ----

	Describe("UpdateCheck", func() {
		var scripted *prompt.Scripted

		stubVersions := func(baked, latest string) {
			fake.On("--entrypoint cat claude-sandbox /opt/claude-sandbox/claude-version", baked+"\n", nil)
			fake.On("npm view @anthropic-ai/claude-code version", latest+"\n", nil)
		}

		BeforeEach(func() {
			scripted = &prompt.Scripted{IsTTY: true}
			o.Prompter = scripted
			images["claude-sandbox"] = &imgState{created: imgT}
		})

		It("CS-IMG-006: compares the baked version to the npm registry when the base was not just built", func() {
			stubVersions("1.2.3", "1.2.3")
			Expect(imagebuild.UpdateCheck(o, false)).To(BeFalse())
			lines := fake.CommandLines()
			Expect(lines).To(ContainElement(ContainSubstring("claude-version")))
			Expect(lines).To(ContainElement(ContainSubstring("npm view @anthropic-ai/claude-code version")))
			Expect(scripted.Asked).To(BeEmpty()) // equal versions: no prompt
		})

		It("CS-IMG-006: skips the check entirely when the base was just built", func() {
			stubVersions("1.2.3", "1.2.4")
			Expect(imagebuild.UpdateCheck(o, true)).To(BeFalse())
			Expect(fake.CommandLines()).To(BeEmpty())
		})

		It("CS-IMG-007: skips the check when NoUpdateCheck is set", func() {
			stubVersions("1.2.3", "1.2.4")
			o.NoUpdateCheck = true
			Expect(imagebuild.UpdateCheck(o, false)).To(BeFalse())
			Expect(fake.CommandLines()).To(BeEmpty())
		})

		It("CS-IMG-008: prompt defaults to no — Enter/timeout declines, no rebuild", func() {
			stubVersions("1.2.3", "1.2.4")
			// No scripted answer: Ask returns "", Parse falls back to def=false.
			Expect(imagebuild.UpdateCheck(o, false)).To(BeFalse())
			Expect(scripted.Asked).To(HaveLen(1))
			Expect(buildLines(fake)).To(BeEmpty())
			Expect(out.String()).To(ContainSubstring("update available: 1.2.3 → 1.2.4"))
		})

		It("CS-IMG-008: answering \"y\" rebuilds the base with --no-cache", func() {
			stubVersions("1.2.3", "1.2.4")
			scripted.Answers = []string{"y"}
			Expect(imagebuild.UpdateCheck(o, false)).To(BeTrue())
			Expect(buildLines(fake)).To(ContainElement(
				"docker build --no-cache -t claude-sandbox --build-arg CLAUDE_SANDBOX_VERSION=v1.2.3 " + repo))
		})

		It("CS-IMG-009: --update auto-accepts the update rebuild without prompting", func() {
			stubVersions("1.2.3", "1.2.4")
			o.AutoUpdate = true
			Expect(imagebuild.UpdateCheck(o, false)).To(BeTrue())
			Expect(scripted.Asked).To(BeEmpty())
			Expect(buildLines(fake)).To(ContainElement(ContainSubstring("--no-cache")))
		})
	})

	// ---- child Dockerfile resolution ----

	Describe("ResolveChild", func() {
		var proj string

		BeforeEach(func() {
			base, err := filepath.EvalSymlinks(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())
			proj = filepath.Join(base, "proj")
			Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		})

		resolve := func(in imagebuild.ChildInputs) imagebuild.ChildSpec {
			if in.ProjectDir == "" {
				in.ProjectDir = proj
			}
			if in.Slug == "" {
				in.Slug = imagebuild.Slug(in.ProjectDir)
			}
			return imagebuild.ResolveChild(in, out)
		}

		It("CS-IMG-010: uses .claude-sandbox/Dockerfile with the PROJECT ROOT as build context", func() {
			df := filepath.Join(proj, ".claude-sandbox", "Dockerfile")
			touchAt(df, old)
			spec := resolve(imagebuild.ChildInputs{})
			Expect(spec.Use).To(BeTrue())
			Expect(spec.Dockerfile).To(Equal(df))
			Expect(spec.Context).To(Equal(proj))
		})

		It("CS-IMG-011: walks parents for a shared child Dockerfile and reports where it was found", func() {
			ws := filepath.Dir(proj)
			df := filepath.Join(ws, ".claude-sandbox", "Dockerfile")
			touchAt(df, old)
			spec := resolve(imagebuild.ChildInputs{})
			Expect(spec.Use).To(BeTrue())
			Expect(spec.Dockerfile).To(Equal(df))
			Expect(spec.Context).To(Equal(ws))
			Expect(out.String()).To(ContainSubstring("Found"))
			Expect(out.String()).To(ContainSubstring(ws))
		})

		It("CS-IMG-012: honors an explicit override verbatim with the override directory as context", func() {
			dir := filepath.Join(proj, "docker")
			df := filepath.Join(dir, "Dockerfile.sandbox")
			touchAt(df, old)
			spec := resolve(imagebuild.ChildInputs{DockerfileDir: dir, Dockerfile: "Dockerfile.sandbox"})
			Expect(spec.Use).To(BeTrue())
			Expect(spec.Dockerfile).To(Equal(df))
			Expect(spec.Context).To(Equal(dir))
		})

		It("CS-IMG-012: walks parents for the exact override filename when absent in the override dir", func() {
			deep := filepath.Join(proj, "a", "b")
			Expect(os.MkdirAll(deep, 0o755)).To(Succeed())
			shared := filepath.Join(proj, "Dockerfile.shared")
			touchAt(shared, old)
			spec := resolve(imagebuild.ChildInputs{DockerfileDir: deep, Dockerfile: "Dockerfile.shared"})
			Expect(spec.Use).To(BeTrue())
			Expect(spec.Dockerfile).To(Equal(shared))
			Expect(spec.Context).To(Equal(proj))
			Expect(out.String()).To(ContainSubstring("Found Dockerfile.shared"))
		})

		It("CS-IMG-013: baseOnly skips child detection silently", func() {
			touchAt(filepath.Join(proj, ".claude-sandbox", "Dockerfile"), old)
			spec := resolve(imagebuild.ChildInputs{BaseOnly: true})
			Expect(spec.Use).To(BeFalse())
			img, err := imagebuild.EnsureChild(o, spec, false, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(img).To(Equal("claude-sandbox"))
			Expect(errw.String()).To(BeEmpty())
			Expect(buildLines(fake)).To(BeEmpty())
		})

		It("CS-IMG-014: missing child Dockerfile warns but proceeds on the base image", func() {
			spec := resolve(imagebuild.ChildInputs{})
			Expect(spec.Use).To(BeFalse())
			img, err := imagebuild.EnsureChild(o, spec, false, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(img).To(Equal("claude-sandbox"))
			Expect(errw.String()).To(ContainSubstring("No child Dockerfile found"))
			Expect(errw.String()).To(ContainSubstring("baseOnly"))
		})

		It("CS-IMG-015: the child image name derives from the project slug", func() {
			odd := filepath.Join(filepath.Dir(proj), "My_Cool.Project!")
			Expect(os.MkdirAll(odd, 0o755)).To(Succeed())
			spec := resolve(imagebuild.ChildInputs{ProjectDir: odd, Slug: imagebuild.Slug(odd)})
			Expect(spec.ImageName).To(Equal("claude-sandbox-my_cool.project-"))
		})

		It("CS-IMG-015: Slug lowercases and replaces characters outside [a-z0-9._-]", func() {
			Expect(imagebuild.Slug("/x/My_Cool.Project!")).To(Equal("my_cool.project-"))
			Expect(imagebuild.Slug("/x/plain-name_1.0")).To(Equal("plain-name_1.0"))
		})
	})

	// ---- child image staleness ----

	Describe("EnsureChild", func() {
		var (
			df   string
			spec imagebuild.ChildSpec
			proj string
		)

		BeforeEach(func() {
			proj = GinkgoT().TempDir()
			df = filepath.Join(proj, ".claude-sandbox", "Dockerfile")
			touchAt(df, old)
			spec = imagebuild.ChildSpec{Use: true, Dockerfile: df, Context: proj, ImageName: "claude-sandbox-proj"}
			images["claude-sandbox"] = &imgState{created: imgT}
		})

		DescribeTable("CS-IMG-016: child rebuild triggers",
			func(setup func(), baseRebuilt bool) {
				setup()
				img, err := imagebuild.EnsureChild(o, spec, baseRebuilt, false)
				Expect(err).NotTo(HaveOccurred())
				Expect(img).To(Equal("claude-sandbox-proj"))
				Expect(buildLines(fake)).To(ContainElement(
					"docker build -t claude-sandbox-proj -f " + df + " " + proj))
			},
			Entry("the child image does not exist", func() {}, false),
			Entry("the base was rebuilt this launch", func() {
				images["claude-sandbox-proj"] = &imgState{created: imgT}
			}, true),
			Entry("the child Dockerfile is newer than the child image", func() {
				images["claude-sandbox-proj"] = &imgState{created: imgT}
				touchAt(df, time.Now())
			}, false),
			Entry("the base image is newer than the child image", func() {
				images["claude-sandbox-proj"] = &imgState{created: imgT}
				images["claude-sandbox"] = &imgState{created: imgT.Add(30 * time.Minute)}
			}, false),
		)

		It("CS-IMG-016: notes the out-of-band base rebuild when the base is newer", func() {
			images["claude-sandbox-proj"] = &imgState{created: imgT}
			images["claude-sandbox"] = &imgState{created: imgT.Add(30 * time.Minute)}
			_, err := imagebuild.EnsureChild(o, spec, false, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(out.String()).To(ContainSubstring("Base image is newer than child"))
		})

		It("CS-IMG-017: a fresh child is not rebuilt and is used as the image", func() {
			images["claude-sandbox-proj"] = &imgState{created: imgT}
			images["claude-sandbox"] = &imgState{created: imgT.Add(-30 * time.Minute)}
			img, err := imagebuild.EnsureChild(o, spec, false, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(img).To(Equal("claude-sandbox-proj"))
			Expect(buildLines(fake)).To(BeEmpty())
		})
	})

	// ---- --version report ----

	Describe("PrintVersion", func() {
		It("CS-LNCH-030: prints \"(not built yet)\" when the image does not exist", func() {
			imagebuild.PrintVersion(o)
			Expect(out.String()).To(ContainSubstring("claude-sandbox v1.2.3"))
			Expect(out.String()).To(ContainSubstring("(not built yet)"))
		})

		It("CS-LNCH-030: prints host and baked versions and notes a mismatch auto-rebuilds", func() {
			images["claude-sandbox"] = &imgState{created: imgT, revision: "v0.9.0"}
			imagebuild.PrintVersion(o)
			Expect(out.String()).To(ContainSubstring("claude-sandbox v1.2.3"))
			Expect(out.String()).To(ContainSubstring("v0.9.0"))
			Expect(out.String()).To(ContainSubstring("auto-rebuild"))
		})

		It("CS-LNCH-030: prints no mismatch note when versions agree", func() {
			images["claude-sandbox"] = &imgState{created: imgT, revision: "v1.2.3"}
			imagebuild.PrintVersion(o)
			Expect(out.String()).NotTo(ContainSubstring("auto-rebuild"))
		})
	})
})
