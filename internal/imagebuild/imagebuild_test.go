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

		It("CS-IMG-015: the child image name derives from the resolved Dockerfile, not the project", func() {
			odd := filepath.Join(filepath.Dir(proj), "My_Cool.Project!")
			Expect(os.MkdirAll(odd, 0o755)).To(Succeed())
			df := filepath.Join(odd, ".claude-sandbox", "Dockerfile")
			touchAt(df, old)
			spec := resolve(imagebuild.ChildInputs{ProjectDir: odd})
			Expect(spec.Use).To(BeTrue())
			Expect(spec.ImageName).To(Equal("claude-sandbox-" + imagebuild.ImageSlug(df, odd)))
			// The context directory name makes the tag legible in `docker images`.
			Expect(spec.ImageName).To(HavePrefix("claude-sandbox-df-my_cool.project-"))
		})

		It("CS-IMG-015: no child image name is produced when no child is in use", func() {
			// EnsureChild returns the base image on this path and must never
			// read ImageName; leaving it empty keeps that honest.
			spec := resolve(imagebuild.ChildInputs{})
			Expect(spec.Use).To(BeFalse())
			Expect(spec.ImageName).To(BeEmpty())
			img, err := imagebuild.EnsureChild(o, spec, false, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(img).To(Equal("claude-sandbox"))
		})

		It("CS-IMG-015: Slug lowercases and replaces characters outside [a-z0-9._-]", func() {
			Expect(imagebuild.Slug("/x/My_Cool.Project!")).To(Equal("my_cool.project-"))
			Expect(imagebuild.Slug("/x/plain-name_1.0")).To(Equal("plain-name_1.0"))
		})

		It("CS-IMG-018: projects sharing a parent Dockerfile and context share one image tag", func() {
			// The motivating case: many same-named projects under one workspace
			// all resolve the workspace Dockerfile. Previously each built its own
			// identically-contented image under a colliding tag.
			ws := filepath.Dir(proj)
			df := filepath.Join(ws, ".claude-sandbox", "Dockerfile")
			touchAt(df, old)
			other := filepath.Join(ws, "other")
			Expect(os.MkdirAll(other, 0o755)).To(Succeed())

			a := resolve(imagebuild.ChildInputs{ProjectDir: proj})
			b := resolve(imagebuild.ChildInputs{ProjectDir: other})
			Expect(a.Use).To(BeTrue())
			Expect(b.Use).To(BeTrue())
			Expect(a.Dockerfile).To(Equal(b.Dockerfile))
			Expect(a.Context).To(Equal(b.Context))
			Expect(a.ImageName).To(Equal(b.ImageName))
		})

		It("CS-IMG-019: the same Dockerfile with different contexts yields different tags", func() {
			// The default branch builds with the PROJECT ROOT as context; the
			// dockerfileDir override builds with the override dir. Point both at
			// the very same file: same Dockerfile, different image, so the tag
			// must not merge them.
			sandboxDir := filepath.Join(proj, ".claude-sandbox")
			df := filepath.Join(sandboxDir, "Dockerfile")
			touchAt(df, old)

			viaDefault := resolve(imagebuild.ChildInputs{ProjectDir: proj})
			viaOverride := resolve(imagebuild.ChildInputs{ProjectDir: proj, DockerfileDir: sandboxDir})

			Expect(viaDefault.Dockerfile).To(Equal(viaOverride.Dockerfile))
			Expect(viaDefault.Context).To(Equal(proj))
			Expect(viaOverride.Context).To(Equal(sandboxDir))
			Expect(viaDefault.ImageName).NotTo(Equal(viaOverride.ImageName))
		})

		It("CS-IMG-015: ImageSlug is deterministic and keyed on the (dockerfile, context) pair", func() {
			Expect(imagebuild.ImageSlug("/a/Dockerfile", "/a")).To(Equal(imagebuild.ImageSlug("/a/Dockerfile", "/a")))
			Expect(imagebuild.ImageSlug("/a/Dockerfile", "/a")).NotTo(Equal(imagebuild.ImageSlug("/a/Dockerfile", "/b")))
			Expect(imagebuild.ImageSlug("/a/Dockerfile", "/a")).NotTo(Equal(imagebuild.ImageSlug("/b/Dockerfile", "/a")))
		})
	})

	Describe("project slug (CS-LNCH-028, CS-LNCH-031)", func() {
		It("CS-LNCH-028: qualifies the basename with the parent and a path digest", func() {
			s := imagebuild.ProjectSlug("/home/u/work/marketing/infrastructure")
			Expect(s).To(HavePrefix("marketing-infrastructure-"))
			Expect(s).To(MatchRegexp(`^marketing-infrastructure-[0-9a-f]{6}$`))
		})

		It("CS-LNCH-028: normalizes the parent segment by the same rules as the basename", func() {
			s := imagebuild.ProjectSlug("/x/My Group!/Sub_Project")
			Expect(s).To(MatchRegexp(`^my-group--sub_project-[0-9a-f]{6}$`))
		})

		It("CS-LNCH-028: omits the parent segment at the filesystem root", func() {
			Expect(imagebuild.ProjectSlug("/proj")).To(MatchRegexp(`^proj-[0-9a-f]{6}$`))
		})

		It("CS-LNCH-028: is deterministic for the same absolute path", func() {
			Expect(imagebuild.ProjectSlug("/a/b/c")).To(Equal(imagebuild.ProjectSlug("/a/b/c")))
			Expect(imagebuild.ProjectSlug("/a/b/c")).To(Equal(imagebuild.ProjectSlug("/a/b/c/")))
		})

		It("CS-LNCH-031: same-basename projects under different parents get distinct slugs", func() {
			a := imagebuild.ProjectSlug("/w/marketing/infrastructure")
			b := imagebuild.ProjectSlug("/w/auth/infrastructure")
			Expect(a).NotTo(Equal(b))
			// Distinct in the readable segment AND in the digest, so the names
			// stay legible without relying on the hash alone.
			Expect(a).To(HavePrefix("marketing-"))
			Expect(b).To(HavePrefix("auth-"))
			Expect(a[len(a)-6:]).NotTo(Equal(b[len(b)-6:]))
		})

		It("CS-LNCH-031: same basename AND same parent name in different trees stay distinct", func() {
			// Parent qualification alone is not sufficient; the digest is what
			// makes this correct.
			a := imagebuild.ProjectSlug("/one/shared/infrastructure")
			b := imagebuild.ProjectSlug("/two/shared/infrastructure")
			Expect(a).NotTo(Equal(b))
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
