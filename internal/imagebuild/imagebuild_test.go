package imagebuild_test

// Spec: spec/image-build.feature (CS-IMG) plus the --version report
// (CS-LNCH-030). All docker/git/npm traffic goes through execx.Fake; image
// existence and creation timestamps are scripted per image name, and file
// staleness is driven with os.Chtimes against real temp files.

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/imagebuild"
)

// imgState scripts one image's docker inspect responses.
type imgState struct {
	created       time.Time
	revision      string
	claudeVersion string // claude-sandbox.claude-version label (CLI image)
	inputs        string // claude-sandbox.build-inputs label (CS-IMG-032)
	id            string // {{.Id}}
}

// repoFile reads a file from the real repo checkout (the tests run from the
// package directory) for the Dockerfile-content scenarios.
func repoFile(name string) string {
	raw, err := os.ReadFile(filepath.Join("..", "..", name))
	Expect(err).NotTo(HaveOccurred())
	return string(raw)
}

// stdinOf returns the stdin fed to the first "docker build ... -" call.
func stdinOf(f *execx.Fake) string {
	for _, c := range f.Calls {
		if c.Name == "docker" && len(c.Args) > 0 && c.Args[0] == "build" && c.Args[len(c.Args)-1] == "-" {
			Expect(c.Stdin).NotTo(BeNil())
			raw, err := io.ReadAll(c.Stdin)
			Expect(err).NotTo(HaveOccurred())
			return string(raw)
		}
	}
	return ""
}

// buildEnvOf returns the Env of every docker build call.
func buildEnvOf(f *execx.Fake) [][]string {
	var out [][]string
	for _, c := range f.Calls {
		if c.Name == "docker" && len(c.Args) > 0 && c.Args[0] == "build" {
			out = append(out, c.Env)
		}
	}
	return out
}

// touch creates the file (and parents) and sets its mtime.
func touchAt(p string, mtime time.Time) {
	Expect(os.MkdirAll(filepath.Dir(p), 0o755)).To(Succeed())
	Expect(os.WriteFile(p, []byte("x\n"), 0o644)).To(Succeed())
	Expect(os.Chtimes(p, mtime, mtime)).To(Succeed())
}

// labelRe matches the build-inputs stamp every build carries (CS-IMG-032).
var labelRe = regexp.MustCompile(` --label claude-sandbox\.build-inputs=([0-9a-f]{64})`)

// buildLines returns the docker build calls with the build-inputs label
// stripped, so argv assertions stay about the build itself.
func buildLines(f *execx.Fake) []string {
	var out []string
	for _, l := range f.CommandLines() {
		if strings.HasPrefix(l, "docker build ") {
			out = append(out, labelRe.ReplaceAllString(l, ""))
		}
	}
	return out
}

// stampOf returns the build-inputs label the build of image carried, "" when
// it carried none or no such build ran.
func stampOf(f *execx.Fake, image string) string {
	for _, l := range f.CommandLines() {
		if strings.HasPrefix(l, "docker build -t "+image+" ") {
			if m := labelRe.FindStringSubmatch(l); m != nil {
				return m[1]
			}
		}
	}
	return ""
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
			if strings.Contains(joined, "{{.Id}}") && strings.Contains(joined, "image.revision") {
				return st.id + "|" + st.revision + "\n", nil // the cap's one tools inspect
			}
			if strings.Contains(joined, "{{.Created}}") {
				return st.created.Format(time.RFC3339Nano) + "\n", nil
			}
			if strings.Contains(joined, "image.revision") {
				return st.revision + "\n", nil
			}
			if strings.Contains(joined, "claude-sandbox.claude-version") {
				return st.claudeVersion + "\n", nil
			}
			if strings.Contains(joined, "claude-sandbox.build-inputs") {
				return st.inputs + "\n", nil
			}
			if strings.Contains(joined, "{{.Id}}") {
				return st.id + "\n", nil
			}
			return "", nil
		})

		repo = GinkgoT().TempDir()
		touchAt(filepath.Join(repo, "Dockerfile"), old)
		touchAt(filepath.Join(repo, "Dockerfile.cli"), old)
		touchAt(filepath.Join(repo, "Dockerfile.tools"), old)
		touchAt(filepath.Join(repo, "cmd", "main.go"), old)
		touchAt(filepath.Join(repo, "entrypoint.sh"), old)

		o = imagebuild.Options{
			Runner: fake, Out: out, Err: errw,
			RepoRoot: repo, Version: "v1.2.3",
		}
	})

	// ---- base image ----

	Describe("EnsureBase", func() {
		It("CS-IMG-001: builds the base when the image is missing", func() {
			rebuilt, err := imagebuild.EnsureBase(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeTrue())
			Expect(buildLines(fake)).To(ContainElement("docker build -t claude-sandbox " + repo))
		})

		It("CS-IMG-002: --rebuild forces a base rebuild with --no-cache", func() {
			images["claude-sandbox"] = &imgState{created: imgT}
			o.ForceRebuild = true
			rebuilt, err := imagebuild.EnsureBase(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeTrue())
			Expect(buildLines(fake)).To(ContainElement("docker build -t claude-sandbox --no-cache " + repo))
		})

		It("CS-IMG-002: --rebuild forces a tools rebuild with --no-cache", func() {
			images["claude-sandbox-tools"] = &imgState{created: imgT}
			o.ForceRebuild = true
			rebuilt, err := imagebuild.EnsureTools(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeTrue())
			Expect(buildLines(fake)).To(ContainElement(
				"docker build -t claude-sandbox-tools --build-arg CLAUDE_SANDBOX_VERSION=v1.2.3 --no-cache -f " +
					filepath.Join(repo, "Dockerfile.tools") + " " + repo))
		})

		It("CS-IMG-002: --rebuild also rebuilds the CLI image with --no-cache and the cap", func() {
			images["claude-sandbox"] = &imgState{created: imgT}
			images["claude-sandbox-cli"] = &imgState{created: imgT, claudeVersion: "1.2.3"}
			images["claude-sandbox:run"] = &imgState{created: imgT.Add(time.Minute)}
			fake.On("npm view @anthropic-ai/claude-code version", "1.2.3\n", nil)
			o.ForceRebuild = true
			rebuilt, err := imagebuild.EnsureCLI(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeTrue())
			Expect(buildLines(fake)).To(ContainElement(
				"docker build -t claude-sandbox-cli --build-arg CLAUDE_CODE_VERSION=1.2.3 --no-cache -f " +
					filepath.Join(repo, "Dockerfile.cli") + " " + repo))
			_, built, err := imagebuild.EnsureCap(o, "claude-sandbox")
			Expect(err).NotTo(HaveOccurred())
			Expect(built).To(BeTrue())
		})

		It("CS-IMG-027: every docker build runs with DOCKER_BUILDKIT=1", func() {
			images["claude-sandbox-cli"] = &imgState{created: imgT}
			_, err := imagebuild.EnsureBase(o)
			Expect(err).NotTo(HaveOccurred())
			_, _, err = imagebuild.EnsureCap(o, "claude-sandbox")
			Expect(err).NotTo(HaveOccurred())
			envs := buildEnvOf(fake)
			Expect(envs).To(HaveLen(2))
			for _, e := range envs {
				Expect(e).To(ContainElement("DOCKER_BUILDKIT=1"))
			}
		})
	})

	// ---- BuildKit prerequisite ----

	Describe("EnsureBuildKit", func() {
		It("CS-IMG-027: passes when docker buildx version succeeds", func() {
			Expect(imagebuild.EnsureBuildKit(o)).To(Succeed())
			Expect(fake.CommandLines()).To(ContainElement("docker buildx version"))
		})

		It("CS-IMG-027: fails naming the docker-buildx-plugin package when buildx is missing", func() {
			fake.On("docker buildx version", "", execx.Fail(1))
			err := imagebuild.EnsureBuildKit(o)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("docker-buildx-plugin"))
			Expect(buildLines(fake)).To(BeEmpty())
		})
	})

	// ---- Claude Code CLI image ----

	Describe("EnsureCLI", func() {
		cliBuild := func(v string) string {
			return "docker build -t claude-sandbox-cli --build-arg CLAUDE_CODE_VERSION=" + v +
				" -f " + filepath.Join(repo, "Dockerfile.cli") + " " + repo
		}

		It("CS-IMG-021: builds the CLI image when missing, pinned to the npm version", func() {
			fake.On("npm view @anthropic-ai/claude-code version", "2.1.247\n", nil)
			rebuilt, err := imagebuild.EnsureCLI(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeTrue())
			Expect(buildLines(fake)).To(ConsistOf(cliBuild("2.1.247")))
		})

		It("CS-IMG-022: rebuilds when Dockerfile.cli is newer than the image", func() {
			images["claude-sandbox-cli"] = &imgState{created: imgT}
			fake.On("npm view @anthropic-ai/claude-code version", "2.1.247\n", nil)
			touchAt(filepath.Join(repo, "Dockerfile.cli"), time.Now())
			rebuilt, err := imagebuild.EnsureCLI(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeTrue())
			Expect(buildLines(fake)).To(HaveLen(1))
		})

		It("CS-IMG-022: a fresh CLI image is not rebuilt", func() {
			images["claude-sandbox-cli"] = &imgState{created: imgT}
			rebuilt, err := imagebuild.EnsureCLI(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeFalse())
			Expect(buildLines(fake)).To(BeEmpty())
		})

		It("CS-IMG-023: falls back to \"latest\" with a warning when npm is unreachable", func() {
			fake.On("npm view", "", execx.Fail(1))
			rebuilt, err := imagebuild.EnsureCLI(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeTrue())
			Expect(buildLines(fake)).To(ConsistOf(cliBuild("latest")))
			Expect(errw.String()).To(ContainSubstring("latest"))
		})

		It("CS-IMG-020: the base Dockerfile does not install the CLI; Dockerfile.cli does", func() {
			for _, name := range []string{"Dockerfile", "Dockerfile.tools"} {
				Expect(repoFile(name)).NotTo(ContainSubstring("install.sh"), name)
				Expect(repoFile(name)).NotTo(ContainSubstring("claude-version"), name)
			}
			cli := repoFile("Dockerfile.cli")
			Expect(cli).To(ContainSubstring("https://claude.ai/install.sh"))
			Expect(cli).To(ContainSubstring("ARG CLAUDE_CODE_VERSION"))
			Expect(cli).To(ContainSubstring("/opt/claude-sandbox/claude-version"))
			Expect(cli).To(ContainSubstring("LABEL claude-sandbox.claude-version"))
		})

		It("CS-IMG-029: the base and CLI Dockerfiles declare the shared cache-mount ids", func() {
			idRe := regexp.MustCompile(`--mount=type=cache,id=([a-z0-9-]+)`)
			allowed := map[string]bool{
				"claude-sandbox-apt": true, "claude-sandbox-apt-lists": true, "claude-sandbox-pip": true,
				"claude-sandbox-npm": true, "claude-sandbox-go-mod": true, "claude-sandbox-go-build": true,
			}
			seen := map[string]bool{}
			for _, name := range []string{"Dockerfile", "Dockerfile.tools", "Dockerfile.cli"} {
				for _, m := range idRe.FindAllStringSubmatch(repoFile(name), -1) {
					Expect(allowed).To(HaveKey(m[1]), name+" uses an id outside the shared set")
					seen[m[1]] = true
				}
			}
			for id := range allowed {
				Expect(seen).To(HaveKey(id), id+" is declared nowhere")
			}
			// No external frontend: "# syntax=" would make every build resolve
			// docker/dockerfile:1 from Docker Hub (CS-IMG-030).
			Expect(repoFile("Dockerfile")).NotTo(ContainSubstring("# syntax="))
			Expect(repoFile("Dockerfile.cli")).NotTo(ContainSubstring("# syntax="))
			Expect(repoFile("Dockerfile.tools")).NotTo(ContainSubstring("# syntax="))
			Expect(repoFile(filepath.Join("scaffold", "Dockerfile.example"))).NotTo(ContainSubstring("# syntax="))
		})
	})

	// ---- run image (cap) ----

	Describe("EnsureCap", func() {
		BeforeEach(func() {
			images["claude-sandbox-cli"] = &imgState{created: imgT.Add(-time.Hour)}
			images["claude-sandbox-tools"] = &imgState{created: imgT.Add(-time.Hour), revision: "v1.2.3-4-gabc1234-dirty"}
			images["claude-sandbox-proj"] = &imgState{created: imgT.Add(-time.Hour)}
		})

		It("CS-IMG-024: builds <under>:run from a stdin Dockerfile copying the tools and the CLI", func() {
			img, built, err := imagebuild.EnsureCap(o, "claude-sandbox-proj")
			Expect(err).NotTo(HaveOccurred())
			Expect(built).To(BeTrue())
			Expect(img).To(Equal("claude-sandbox-proj:run"))
			Expect(buildLines(fake)).To(ConsistOf("docker build -t claude-sandbox-proj:run -"))
			Expect(stdinOf(fake)).To(Equal(
				"FROM claude-sandbox-proj\n" +
					"COPY --link --from=claude-sandbox-tools /opt/claude-sandbox/ /opt/claude-sandbox/\n" +
					"COPY --link --from=claude-sandbox-tools /etc/claude-code/managed-settings.d/10-claude-sandbox.json /etc/claude-code/managed-settings.d/10-claude-sandbox.json\n" +
					"ENV CLAUDE_SANDBOX_VERSION=v1.2.3-4-gabc1234-dirty\n" +
					"COPY --link --from=claude-sandbox-cli /home/claude/.local /home/claude/.local\n" +
					"COPY --link --from=claude-sandbox-cli /opt/claude-sandbox/claude-version /opt/claude-sandbox/claude-version\n"))
			Expect(stdinOf(fake)).NotTo(ContainSubstring("--chown"))
			Expect(stdinOf(fake)).NotTo(ContainSubstring("--chmod"), "the managed-settings dirs must stay 0755 (CS-LNCH-068)")
		})

		It("CS-IMG-024: the cap reads the tools image's ID and version with one inspect", func() {
			_, _, err := imagebuild.EnsureCap(o, "claude-sandbox-proj")
			Expect(err).NotTo(HaveOccurred())
			n := 0
			for _, l := range fake.CommandLines() {
				if strings.HasPrefix(l, "docker image inspect") && strings.HasSuffix(l, " claude-sandbox-tools") {
					n++
				}
			}
			Expect(n).To(Equal(1))
		})

		DescribeTable("CS-IMG-005: the cap sets CLAUDE_SANDBOX_VERSION from the tools image's revision label",
			func(label, want string) {
				images["claude-sandbox-tools"].revision = label
				_, _, err := imagebuild.EnsureCap(o, "claude-sandbox-proj")
				Expect(err).NotTo(HaveOccurred())
				Expect(stdinOf(fake)).To(ContainSubstring("\nENV CLAUDE_SANDBOX_VERSION=" + want + "\n"))
			},
			Entry("a git describe stamp", "v2.0.0-1-gdeadbee", "v2.0.0-1-gdeadbee"),
			Entry("no label", "", "unknown"),
			Entry("docker's <no value>", "<no value>", "unknown"),
			Entry("a value unsafe in an ENV line", "v1 && rm -rf /", "unknown"),
		)

		It("CS-IMG-030: the generated cap Dockerfile carries no # syntax= directive", func() {
			_, _, err := imagebuild.EnsureCap(o, "claude-sandbox-proj")
			Expect(err).NotTo(HaveOccurred())
			Expect(stdinOf(fake)).NotTo(ContainSubstring("# syntax="))
		})

		It("CS-IMG-024: CapImageName is the single resolver for the run image", func() {
			Expect(imagebuild.CapImageName("claude-sandbox")).To(Equal("claude-sandbox:run"))
			Expect(imagebuild.CapImageName("claude-sandbox-df-x-abc123")).To(Equal("claude-sandbox-df-x-abc123:run"))
		})

		DescribeTable("CS-IMG-025: cap rebuild triggers",
			func(setup func()) {
				setup()
				img, built, err := imagebuild.EnsureCap(o, "claude-sandbox-proj")
				Expect(err).NotTo(HaveOccurred())
				Expect(built).To(BeTrue())
				Expect(img).To(Equal("claude-sandbox-proj:run"))
				Expect(buildLines(fake)).To(HaveLen(1))
			},
			Entry("the cap image does not exist", func() {}),
			Entry("the parent image is newer than the cap", func() {
				images["claude-sandbox-proj:run"] = &imgState{created: imgT}
				images["claude-sandbox-proj"] = &imgState{created: imgT.Add(time.Minute)}
			}),
			Entry("the tools image is newer than the cap", func() {
				images["claude-sandbox-proj:run"] = &imgState{created: imgT}
				images["claude-sandbox-tools"] = &imgState{created: imgT.Add(time.Minute)}
			}),
			Entry("the CLI image is newer than the cap", func() {
				images["claude-sandbox-proj:run"] = &imgState{created: imgT}
				images["claude-sandbox-cli"] = &imgState{created: imgT.Add(time.Minute)}
			}),
			Entry("--rebuild was given", func() {
				images["claude-sandbox-proj:run"] = &imgState{created: imgT.Add(time.Hour)}
				o.ForceRebuild = true
			}),
		)

		It("CS-IMG-026: a fresh cap is not rebuilt and is the image to run", func() {
			images["claude-sandbox-proj:run"] = &imgState{created: imgT}
			img, built, err := imagebuild.EnsureCap(o, "claude-sandbox-proj")
			Expect(err).NotTo(HaveOccurred())
			Expect(built).To(BeFalse())
			Expect(img).To(Equal("claude-sandbox-proj:run"))
			Expect(buildLines(fake)).To(BeEmpty())
		})
	})

	// ---- build-cache budget warning ----

	Describe("WarnCacheBudget", func() {
		const dfOut = `{"Active":"11","Reclaimable":"232.4GB (34%)","Size":"682.4GB","TotalCount":"1064","Type":"Images"}
{"Active":"0","Reclaimable":"115.9GB","Size":"625.1GB","TotalCount":"6475","Type":"Build Cache"}
`
		inspect := func(ephemeral, all string) string {
			return "Name:   default\nDriver: docker\n\nGC Policy rule#0:\n All:            false\n" +
				" Filters:        type==source.local,type==exec.cachemount,type==source.git.checkout\n" +
				" Keep Duration:  48h0m0s\n Max Used Space: " + ephemeral + "\n" +
				"GC Policy rule#1:\n All:            false\n Keep Duration:  1440h0m0s\n Reserved Space: 83.82GiB\n Max Used Space: " + all + "\n" +
				"GC Policy rule#3:\n All:            true\n Reserved Space: 83.82GiB\n Max Used Space: " + all + "\n Min Free Space: 167.6GiB\n"
		}

		// dfSize swaps the Build Cache row's size.
		dfSize := func(size string) string { return strings.Replace(dfOut, "625.1GB", size, 1) }

		It("CS-IMG-028: warns with the prune command when the cache is at least 80% of the budget", func() {
			fake.On("docker system df --format {{json .}}", dfOut, nil)
			fake.On("docker buildx inspect", inspect("41GiB", "669.6GiB"), nil)
			imagebuild.WarnCacheBudget(o, errw)
			Expect(errw.String()).To(ContainSubstring("625 GB of its 719 GB budget"))
			Expect(errw.String()).To(ContainSubstring("docker builder prune -af"))
			Expect(errw.String()).To(ContainSubstring("BuildKit cache"))
			// The ephemeral budget is healthy here, so nothing claims otherwise.
			Expect(errw.String()).NotTo(ContainSubstring("caps cache mounts"))
		})

		It("CS-IMG-028: reports a small cache-mount cap as its own note, and does not recommend pruning for it", func() {
			// Docker Desktop's out-of-box 20GB keep-storage resolves to this.
			fake.On("docker system df --format {{json .}}", dfSize("10GB"), nil)
			fake.On("docker buildx inspect", inspect("2.764GB", "669.6GiB"), nil)
			imagebuild.WarnCacheBudget(o, errw)
			Expect(errw.String()).To(ContainSubstring("caps cache mounts at 3 GB"))
			Expect(errw.String()).To(ContainSubstring("Pruning does not help"))
			Expect(errw.String()).To(ContainSubstring("builder.gc policy"))
			Expect(errw.String()).NotTo(ContainSubstring("docker builder prune -af"))
			// The total is healthy: it is context, not the complaint.
			Expect(errw.String()).NotTo(ContainSubstring("of its 719 GB budget"))
		})

		It("CS-IMG-028: stays silent when usage is well under budget and the cache-mount cap is adequate", func() {
			// The reported false alarm: 147 GB of 719 GB with an 11.58 GiB
			// cache-mount cap is a healthy host and must produce no output.
			fake.On("docker system df --format {{json .}}", dfSize("147GB"), nil)
			fake.On("docker buildx inspect", inspect("11.58GiB", "669.6GiB"), nil)
			imagebuild.WarnCacheBudget(o, errw)
			Expect(errw.String()).To(BeEmpty())
		})

		It("CS-IMG-028: stays silent when the cache is comfortably under budget", func() {
			fake.On("docker system df --format {{json .}}", dfSize("10GB"), nil)
			fake.On("docker buildx inspect", inspect("41GiB", "300GB"), nil)
			imagebuild.WarnCacheBudget(o, errw)
			Expect(errw.String()).To(BeEmpty())
		})

		It("CS-IMG-028: reports both conditions separately when both hold", func() {
			fake.On("docker system df --format {{json .}}", dfOut, nil)
			fake.On("docker buildx inspect", inspect("2.764GB", "669.6GiB"), nil)
			imagebuild.WarnCacheBudget(o, errw)
			Expect(errw.String()).To(ContainSubstring("625 GB of its 719 GB budget"))
			Expect(errw.String()).To(ContainSubstring("caps cache mounts at 3 GB"))
		})

		It("CS-IMG-028: stays silent when docker system df cannot be parsed", func() {
			fake.On("docker system df", "not json\n", nil)
			fake.On("docker buildx inspect", inspect("41GiB", "669.6GiB"), nil)
			imagebuild.WarnCacheBudget(o, errw)
			Expect(errw.String()).To(BeEmpty())
		})

		It("CS-IMG-028: stays silent when docker buildx inspect fails", func() {
			fake.On("docker system df --format {{json .}}", dfOut, nil)
			fake.On("docker buildx inspect", "", execx.Fail(1))
			imagebuild.WarnCacheBudget(o, errw)
			Expect(errw.String()).To(BeEmpty())
		})

		It("CS-IMG-028: the daemon.json recipe the NOTE points at is valid, copy-pasteable JSON", func() {
			// daemon.json is strict JSON. A ```jsonc block with // comments
			// reads fine on GitHub and then fails to parse when pasted into the
			// file it is written for, which is how this shipped the first time.
			readme := repoFile("README.md")
			blocks := regexp.MustCompile("(?s)```json\n(.*?)```").FindAllStringSubmatch(readme, -1)
			Expect(blocks).NotTo(BeEmpty(), "the BuildKit cache section must carry a daemon.json example")
			for _, b := range blocks {
				var v any
				Expect(json.Unmarshal([]byte(b[1]), &v)).To(Succeed(), "README json block is not valid JSON:\n"+b[1])
			}
			Expect(readme).NotTo(ContainSubstring("```jsonc"), "jsonc permits comments that daemon.json rejects")
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

		It("CS-IMG-004: the tools image rebuilds when any baked source is newer than it", func() {
			images["claude-sandbox-tools"] = &imgState{created: imgT}
			touchAt(filepath.Join(repo, "cmd", "main.go"), time.Now())
			rebuilt, err := imagebuild.EnsureTools(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeTrue())
			Expect(out.String()).To(ContainSubstring("Baked sources changed"))
			Expect(buildLines(fake)).To(ConsistOf(
				"docker build -t claude-sandbox-tools --build-arg CLAUDE_SANDBOX_VERSION=v1.2.3 -f " +
					filepath.Join(repo, "Dockerfile.tools") + " " + repo))
		})

		It("CS-IMG-004: the tools image rebuilds when Dockerfile.tools is newer than it", func() {
			images["claude-sandbox-tools"] = &imgState{created: imgT}
			touchAt(filepath.Join(repo, "Dockerfile.tools"), time.Now())
			rebuilt, err := imagebuild.EnsureTools(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeTrue())
		})

		It("CS-IMG-048: a newer baked source does not rebuild the base", func() {
			images["claude-sandbox"] = &imgState{created: imgT}
			for _, rel := range []string{"cmd/main.go", "entrypoint.sh", "notification-hooks.json", "logstream/run-logger.js"} {
				touchAt(filepath.Join(repo, rel), time.Now())
			}
			rebuilt, err := imagebuild.EnsureBase(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeFalse())
			Expect(buildLines(fake)).To(BeEmpty())
		})

		It("CS-IMG-049: builds the tools image when it is missing, stamped with the version", func() {
			rebuilt, err := imagebuild.EnsureTools(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeTrue())
			Expect(buildLines(fake)).To(ConsistOf(
				"docker build -t claude-sandbox-tools --build-arg CLAUDE_SANDBOX_VERSION=v1.2.3 -f " +
					filepath.Join(repo, "Dockerfile.tools") + " " + repo))
			Expect(buildEnvOf(fake)[0]).To(ContainElement("DOCKER_BUILDKIT=1"))
		})

		It("CS-IMG-049: a fresh tools image is not rebuilt", func() {
			images["claude-sandbox-tools"] = &imgState{created: imgT}
			rebuilt, err := imagebuild.EnsureTools(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeFalse())
			Expect(buildLines(fake)).To(BeEmpty())
		})

		It("CS-IMG-049: a labeled tools image costs one inspect: existence and label together", func() {
			rebuilt, err := imagebuild.EnsureTools(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeTrue())
			images["claude-sandbox-tools"] = &imgState{created: imgT, inputs: stampOf(fake, "claude-sandbox-tools")}
			fake.Calls = nil
			rebuilt, err = imagebuild.EnsureTools(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeFalse())
			var inspects []string
			for _, l := range fake.CommandLines() {
				if strings.HasPrefix(l, "docker image inspect") {
					inspects = append(inspects, l)
				}
			}
			Expect(inspects).To(HaveLen(1))
		})

		It("CS-IMG-004: the tools image rebuilds when notification-hooks.json (baked as managed settings) is newer", func() {
			images["claude-sandbox-tools"] = &imgState{created: imgT}
			touchAt(filepath.Join(repo, "notification-hooks.json"), time.Now())
			rebuilt, err := imagebuild.EnsureTools(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeTrue())
			Expect(out.String()).To(ContainSubstring("Baked sources changed"))
		})

		DescribeTable("CS-IMG-004: the tools image rebuilds when a source embedded into the binary is newer",
			func(rel string) {
				images["claude-sandbox-tools"] = &imgState{created: imgT}
				touchAt(filepath.Join(repo, rel), time.Now())
				rebuilt, err := imagebuild.EnsureTools(o)
				Expect(err).NotTo(HaveOccurred())
				Expect(rebuilt).To(BeTrue())
				Expect(out.String()).To(ContainSubstring("Baked sources changed"))
			},
			Entry("scaffold/", "scaffold/config.yaml"),
			Entry("scaffold-ralph/", "scaffold-ralph/agent/PROMPT.md"),
			Entry("container-context.md", "container-context.md"),
			Entry("mcp-servers.json", "mcp-servers.json"),
		)

		DescribeTable("CS-IMG-038: does not rebuild when only build-context debris is newer",
			func(rel string) {
				images["claude-sandbox-tools"] = &imgState{created: imgT}
				touchAt(filepath.Join(repo, rel), time.Now())
				rebuilt, err := imagebuild.EnsureTools(o)
				Expect(err).NotTo(HaveOccurred())
				Expect(rebuilt).To(BeFalse())
				Expect(buildLines(fake)).To(BeEmpty())
			},
			Entry("__pycache__", "scaffold-ralph/scripts/backlog/__pycache__/backlog.cpython-311.pyc"),
			Entry(".pytest_cache", "scaffold-ralph/scripts/.pytest_cache/v/cache/nodeids"),
			Entry("a stray .pyc", "scaffold-ralph/scripts/backlog/stray.pyc"),
			Entry("a dot-entry under scaffold/", "scaffold/.DS_Store"),
			Entry("a dot-dir under scaffold-ralph/", "scaffold-ralph/agent/.idea/workspace.xml"),
		)

		It("CS-IMG-031: does not rebuild when only a Go test file is newer", func() {
			images["claude-sandbox-tools"] = &imgState{created: imgT}
			touchAt(filepath.Join(repo, "internal", "foo", "foo_test.go"), time.Now())
			rebuilt, err := imagebuild.EnsureTools(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeFalse())
			Expect(buildLines(fake)).To(BeEmpty())
		})
	})

	// ---- build-input fingerprints ----

	Describe("build-input fingerprints", func() {
		var spec imagebuild.ChildSpec

		BeforeEach(func() {
			fake.On("npm view @anthropic-ai/claude-code version", "2.1.247\n", nil)
			ctx := GinkgoT().TempDir()
			df := filepath.Join(ctx, ".claude-sandbox", "Dockerfile")
			touchAt(df, old)
			spec = imagebuild.ChildSpec{Use: true, Dockerfile: df, Context: ctx, ImageName: "claude-sandbox-proj"}
		})

		// stampAfter runs build with the named image missing and returns the
		// fingerprint its build carried.
		stampAfter := func(image string, build func()) string {
			delete(images, image)
			fake.Calls = nil
			build()
			return stampOf(fake, image)
		}
		baseStamp := func() string {
			return stampAfter("claude-sandbox", func() { _, err := imagebuild.EnsureBase(o); Expect(err).NotTo(HaveOccurred()) })
		}
		toolsStamp := func() string {
			return stampAfter("claude-sandbox-tools", func() { _, err := imagebuild.EnsureTools(o); Expect(err).NotTo(HaveOccurred()) })
		}
		cliStamp := func() string {
			return stampAfter("claude-sandbox-cli", func() { _, err := imagebuild.EnsureCLI(o); Expect(err).NotTo(HaveOccurred()) })
		}
		childStamp := func() string {
			return stampAfter("claude-sandbox-proj", func() {
				_, _, err := imagebuild.EnsureChild(o, spec, false, false)
				Expect(err).NotTo(HaveOccurred())
			})
		}
		capStamp := func() string {
			return stampAfter("claude-sandbox-proj:run", func() {
				_, _, err := imagebuild.EnsureCap(o, "claude-sandbox-proj")
				Expect(err).NotTo(HaveOccurred())
			})
		}
		parents := func() {
			images["claude-sandbox"] = &imgState{created: imgT, id: "sha256:base1"}
			images["claude-sandbox-cli"] = &imgState{created: imgT, id: "sha256:cli1"}
			images["claude-sandbox-tools"] = &imgState{created: imgT, id: "sha256:tools1", revision: "v1.2.3"}
			images["claude-sandbox-proj"] = &imgState{created: imgT, id: "sha256:child1"}
		}

		It("CS-IMG-032: every build of the base, tools, CLI, child and cap carries the build-inputs label", func() {
			parents()
			Expect(baseStamp()).NotTo(BeEmpty())
			Expect(toolsStamp()).NotTo(BeEmpty())
			parents()
			Expect(cliStamp()).NotTo(BeEmpty())
			parents()
			Expect(childStamp()).NotTo(BeEmpty())
			parents()
			Expect(capStamp()).NotTo(BeEmpty())
		})

		It("CS-IMG-032: --rebuild builds and the update-check CLI rebuild are stamped too", func() {
			parents()
			images["claude-sandbox-proj:run"] = &imgState{created: imgT}
			o.ForceRebuild = true
			_, err := imagebuild.EnsureBase(o)
			Expect(err).NotTo(HaveOccurred())
			_, err = imagebuild.EnsureTools(o)
			Expect(err).NotTo(HaveOccurred())
			_, err = imagebuild.EnsureCLI(o)
			Expect(err).NotTo(HaveOccurred())
			_, _, err = imagebuild.EnsureCap(o, "claude-sandbox-proj")
			Expect(err).NotTo(HaveOccurred())
			Expect(stampOf(fake, "claude-sandbox")).NotTo(BeEmpty())
			Expect(stampOf(fake, "claude-sandbox-tools")).NotTo(BeEmpty())
			Expect(stampOf(fake, "claude-sandbox-cli")).NotTo(BeEmpty())
			Expect(stampOf(fake, "claude-sandbox-proj:run")).NotTo(BeEmpty())

			o.ForceRebuild = false
			o.AutoUpdate = true
			fake.Calls = nil
			images["claude-sandbox-cli"].claudeVersion = "2.1.200"
			Expect(imagebuild.UpdateCheck(o, false)).To(BeTrue())
			Expect(stampOf(fake, "claude-sandbox-cli")).NotTo(BeEmpty())
		})

		It("CS-IMG-033: a labeled CLI image whose Dockerfile.cli was only touched is not rebuilt", func() {
			fp := cliStamp()
			images["claude-sandbox-cli"] = &imgState{created: imgT, inputs: fp}
			touchAt(filepath.Join(repo, "Dockerfile.cli"), time.Now()) // same content, newer mtime
			fake.Calls = nil
			rebuilt, err := imagebuild.EnsureCLI(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeFalse())
			Expect(buildLines(fake)).To(BeEmpty())
		})

		It("CS-IMG-033: a labeled CLI image rebuilds when Dockerfile.cli content changes, whatever its mtime", func() {
			fp := cliStamp()
			images["claude-sandbox-cli"] = &imgState{created: imgT, inputs: fp}
			p := filepath.Join(repo, "Dockerfile.cli")
			Expect(os.WriteFile(p, []byte("changed\n"), 0o644)).To(Succeed())
			Expect(os.Chtimes(p, old, old)).To(Succeed()) // older than the image
			fake.Calls = nil
			rebuilt, err := imagebuild.EnsureCLI(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeTrue())
			Expect(stampOf(fake, "claude-sandbox-cli")).NotTo(Equal(fp))
		})

		It("CS-IMG-033: labeled base and tools images with newer but unchanged sources are not rebuilt", func() {
			fp := baseStamp()
			images["claude-sandbox"] = &imgState{created: imgT, inputs: fp}
			tfp := toolsStamp()
			images["claude-sandbox-tools"] = &imgState{created: imgT, inputs: tfp}
			for _, rel := range []string{"Dockerfile", "Dockerfile.tools", "cmd/main.go", "entrypoint.sh"} {
				p := filepath.Join(repo, rel)
				Expect(os.Chtimes(p, time.Now(), time.Now())).To(Succeed())
			}
			fake.Calls = nil
			rebuilt, err := imagebuild.EnsureBase(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeFalse())
			rebuilt, err = imagebuild.EnsureTools(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeFalse())
			Expect(buildLines(fake)).To(BeEmpty())
		})

		It("CS-IMG-033: a labeled tools image rebuilds when a baked source's content changes, saying its inputs changed", func() {
			fp := toolsStamp()
			images["claude-sandbox-tools"] = &imgState{created: imgT, inputs: fp}
			Expect(os.WriteFile(filepath.Join(repo, "cmd", "main.go"), []byte("package main // edited\n"), 0o644)).To(Succeed())
			fake.Calls = nil
			rebuilt, err := imagebuild.EnsureTools(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeTrue())
			Expect(out.String()).To(ContainSubstring("Tools image inputs changed"))
		})

		It("CS-IMG-033: a labeled base rebuilds when its Dockerfile's content changes, saying its inputs changed", func() {
			fp := baseStamp()
			images["claude-sandbox"] = &imgState{created: imgT, inputs: fp}
			Expect(os.WriteFile(filepath.Join(repo, "Dockerfile"), []byte("FROM scratch\n"), 0o644)).To(Succeed())
			fake.Calls = nil
			rebuilt, err := imagebuild.EnsureBase(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeTrue())
			Expect(out.String()).To(ContainSubstring("Base image inputs changed"))
		})

		It("CS-IMG-048: the base fingerprint is the Dockerfile alone; baked sources feed only the tools fingerprint", func() {
			a, t := baseStamp(), toolsStamp()
			Expect(os.WriteFile(filepath.Join(repo, "cmd", "main.go"), []byte("package main // edited\n"), 0o644)).To(Succeed())
			touchAt(filepath.Join(repo, "notification-hooks.json"), old)
			Expect(baseStamp()).To(Equal(a), "a baked source is not a base input")
			Expect(toolsStamp()).NotTo(Equal(t), "it is a tools input")
			t = toolsStamp()
			Expect(os.WriteFile(filepath.Join(repo, "Dockerfile"), []byte("FROM scratch\n"), 0o644)).To(Succeed())
			Expect(baseStamp()).NotTo(Equal(a), "the Dockerfile is the base input")
			Expect(toolsStamp()).To(Equal(t), "and not a tools input")
		})

		It("CS-IMG-048: a baked-source commit rebuilds the tools image and the cap, never the base or the child", func() {
			// Every image labeled with its current fingerprint.
			bfp, tfp := baseStamp(), toolsStamp()
			parents()
			cfp := childStamp()
			parents()
			capfp := capStamp()
			parents()
			images["claude-sandbox"].inputs = bfp
			images["claude-sandbox-tools"].inputs = tfp
			images["claude-sandbox-proj"].inputs = cfp
			images["claude-sandbox-proj:run"] = &imgState{created: imgT, inputs: capfp}

			Expect(os.WriteFile(filepath.Join(repo, "cmd", "main.go"), []byte("package main // commit\n"), 0o644)).To(Succeed())
			fake.Calls = nil
			baseRebuilt, err := imagebuild.EnsureBase(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(baseRebuilt).To(BeFalse())
			toolsRebuilt, err := imagebuild.EnsureTools(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(toolsRebuilt).To(BeTrue())
			images["claude-sandbox-tools"].id = "sha256:tools2" // the build produced a new image
			parent, childBuilt, err := imagebuild.EnsureChild(o, spec, baseRebuilt, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(childBuilt).To(BeFalse())
			_, capBuilt, err := imagebuild.EnsureCap(o, parent)
			Expect(err).NotTo(HaveOccurred())
			Expect(capBuilt).To(BeTrue())
			var tags []string
			for _, l := range buildLines(fake) {
				tags = append(tags, strings.Fields(l)[3])
			}
			Expect(tags).To(Equal([]string{"claude-sandbox-tools", "claude-sandbox-proj:run"}))
		})

		It("CS-IMG-034: the tools fingerprint covers Dockerfile.tools and baked sources but not _test.go files", func() {
			touchAt(filepath.Join(repo, "internal", "foo", "foo.go"), old)
			touchAt(filepath.Join(repo, "mcp", "discord-notify", "index.mjs"), old)
			a := toolsStamp()
			touchAt(filepath.Join(repo, "internal", "foo", "foo_test.go"), time.Now())
			Expect(toolsStamp()).To(Equal(a), "a test file is not an input")
			touchAt(filepath.Join(repo, "notification-hooks.json"), old)
			b := toolsStamp()
			Expect(b).NotTo(Equal(a), "a new baked source is an input")
			Expect(os.WriteFile(filepath.Join(repo, "Dockerfile.tools"), []byte("FROM scratch\n"), 0o644)).To(Succeed())
			Expect(toolsStamp()).NotTo(Equal(b), "Dockerfile.tools is an input")
		})

		It("CS-IMG-004: the sources embedded into the binary are tools fingerprint inputs", func() {
			for _, rel := range []string{"scaffold/config.yaml", "scaffold-ralph/agent/PROMPT.md", "container-context.md", "mcp-servers.json"} {
				touchAt(filepath.Join(repo, rel), old)
			}
			a := toolsStamp()
			for _, rel := range []string{"scaffold/config.yaml", "scaffold-ralph/agent/PROMPT.md", "container-context.md", "mcp-servers.json"} {
				p := filepath.Join(repo, rel)
				orig, err := os.ReadFile(p)
				Expect(err).NotTo(HaveOccurred())
				Expect(os.WriteFile(p, []byte("edited\n"), 0o644)).To(Succeed())
				Expect(toolsStamp()).NotTo(Equal(a), rel+" is an input")
				Expect(os.WriteFile(p, orig, 0o644)).To(Succeed())
				Expect(toolsStamp()).To(Equal(a))
			}
		})

		It("CS-IMG-038: build-context debris under the baked sources is not a fingerprint input", func() {
			touchAt(filepath.Join(repo, "scaffold-ralph", "scripts", "backlog", "backlog.py"), old)
			touchAt(filepath.Join(repo, "scaffold", "config.yaml"), old)
			touchAt(filepath.Join(repo, "internal", "foo", "foo.go"), old)
			touchAt(filepath.Join(repo, "mcp", "discord-notify", "index.mjs"), old)
			a := toolsStamp()
			for _, rel := range []string{
				"scaffold-ralph/scripts/backlog/__pycache__/backlog.cpython-311.pyc",
				"scaffold-ralph/scripts/.pytest_cache/v/cache/nodeids",
				"internal/foo/stray.pyo",
				"scaffold/.DS_Store",
				"scaffold-ralph/agent/.idea/workspace.xml",
			} {
				touchAt(filepath.Join(repo, rel), time.Now())
				Expect(toolsStamp()).To(Equal(a), rel+" is not an input")
			}
			// A dot-entry outside the scaffold trees is in the build context.
			touchAt(filepath.Join(repo, "mcp", "discord-notify", ".npmrc"), old)
			Expect(toolsStamp()).NotTo(Equal(a))
		})

		It("CS-IMG-034: symlinks count by their target, so directory and dangling links still fingerprint", func() {
			Expect(os.Symlink("../internal", filepath.Join(repo, "cmd", "dirlink"))).To(Succeed())
			Expect(os.Symlink("nowhere", filepath.Join(repo, "cmd", "dangling"))).To(Succeed())
			a := toolsStamp()
			Expect(a).NotTo(BeEmpty())
			Expect(errw.String()).NotTo(ContainSubstring("could not fingerprint"))
			Expect(os.Remove(filepath.Join(repo, "cmd", "dangling"))).To(Succeed())
			Expect(os.Symlink("elsewhere", filepath.Join(repo, "cmd", "dangling"))).To(Succeed())
			Expect(toolsStamp()).NotTo(Equal(a), "a retargeted link is a change")
		})

		It("CS-IMG-039: a checkout under umask 002 fingerprints like one under 022", func() {
			files := []string{
				"cmd/main.go", "entrypoint.sh", "notification-hooks.json", "scaffold/config.yaml",
				"logstream/run-logger.js", "PROMPT_RALPH.md", "mcp/discord-notify/index.mjs",
			}
			for _, rel := range files {
				touchAt(filepath.Join(repo, rel), old)
				Expect(os.Chmod(filepath.Join(repo, rel), 0o644)).To(Succeed())
			}
			Expect(os.Chmod(filepath.Join(repo, "logstream", "run-logger.js"), 0o755)).To(Succeed())
			a := toolsStamp()
			for _, rel := range files {
				Expect(os.Chmod(filepath.Join(repo, rel), 0o664)).To(Succeed())
			}
			Expect(os.Chmod(filepath.Join(repo, "logstream", "run-logger.js"), 0o775)).To(Succeed())
			Expect(toolsStamp()).To(Equal(a), "group-write is a umask artifact, not an image change")
		})

		DescribeTable("CS-IMG-039: an exec-bit change is an input only where COPY preserves the mode",
			func(rel string, input bool) {
				p := filepath.Join(repo, rel)
				touchAt(p, old)
				Expect(os.Chmod(p, 0o644)).To(Succeed())
				a := toolsStamp()
				Expect(os.Chmod(p, 0o755)).To(Succeed())
				if input {
					Expect(toolsStamp()).NotTo(Equal(a), rel+" keeps its mode in the image")
				} else {
					Expect(toolsStamp()).To(Equal(a), rel+" is compiled, bundled, embedded or COPY --chmod'd")
				}
			},
			Entry("a logstream script", "logstream/run-logger.js", true),
			Entry("PROMPT_RALPH.md", "PROMPT_RALPH.md", true),
			Entry("the MCP server source (only its bundle ships)", "mcp/discord-notify/index.mjs", false),
			Entry("entrypoint.sh (--chmod=755)", "entrypoint.sh", false),
			Entry("notification-hooks.json (--chmod=644)", "notification-hooks.json", false),
			Entry("Go source", "cmd/main.go", false),
			Entry("an embedded scaffold file", "scaffold-ralph/scripts/backlog/backlog.py", false),
		)

		It("CS-IMG-040: a symlinked top-level baked directory is fingerprinted by its in-context target's contents", func() {
			touchAt(filepath.Join(repo, "logstream", "run-logger.js"), old)
			Expect(os.Chmod(filepath.Join(repo, "logstream", "run-logger.js"), 0o755)).To(Succeed())
			a := toolsStamp()

			// The same tree, reached through a relative link inside the repo.
			tgt := filepath.Join(repo, "shared", "logstream")
			touchAt(filepath.Join(tgt, "run-logger.js"), old)
			Expect(os.Chmod(filepath.Join(tgt, "run-logger.js"), 0o755)).To(Succeed())
			Expect(os.RemoveAll(filepath.Join(repo, "logstream"))).To(Succeed())
			Expect(os.Symlink("shared/logstream", filepath.Join(repo, "logstream"))).To(Succeed())
			Expect(toolsStamp()).To(Equal(a), "COPY bakes the target's contents under the source's name")
			Expect(errw.String()).NotTo(ContainSubstring("could not fingerprint"))

			Expect(os.WriteFile(filepath.Join(tgt, "run-logger.js"), []byte("edited\n"), 0o755)).To(Succeed())
			b := toolsStamp()
			Expect(b).NotTo(Equal(a), "an edit behind the link is a change")
			touchAt(filepath.Join(tgt, "added.js"), old)
			c := toolsStamp()
			Expect(c).NotTo(Equal(b), "a file added behind the link is a change")
			Expect(os.Chmod(filepath.Join(tgt, "added.js"), 0o755)).To(Succeed())
			Expect(toolsStamp()).NotTo(Equal(c), "the target's exec bits reach the image")
		})

		It("CS-IMG-040: a symlinked top-level baked file is fingerprinted by its in-context target's content", func() {
			touchAt(filepath.Join(repo, "PROMPT_RALPH.md"), old)
			a := toolsStamp()
			touchAt(filepath.Join(repo, "docs", "prompt.md"), old)
			Expect(os.Remove(filepath.Join(repo, "PROMPT_RALPH.md"))).To(Succeed())
			Expect(os.Symlink("docs/prompt.md", filepath.Join(repo, "PROMPT_RALPH.md"))).To(Succeed())
			Expect(toolsStamp()).To(Equal(a))
			Expect(os.WriteFile(filepath.Join(repo, "docs", "prompt.md"), []byte("edited\n"), 0o644)).To(Succeed())
			Expect(toolsStamp()).NotTo(Equal(a))
		})

		It("CS-IMG-040: a symlink at a nested COPY source path (mcp/discord-notify) is followed", func() {
			tgt := filepath.Join(repo, "shared", "discord-notify")
			touchAt(filepath.Join(tgt, "index.mjs"), old)
			Expect(os.MkdirAll(filepath.Join(repo, "mcp"), 0o755)).To(Succeed())
			Expect(os.Symlink("../shared/discord-notify", filepath.Join(repo, "mcp", "discord-notify"))).To(Succeed())
			a := toolsStamp()
			Expect(os.WriteFile(filepath.Join(tgt, "index.mjs"), []byte("edited\n"), 0o644)).To(Succeed())
			Expect(toolsStamp()).NotTo(Equal(a), "an MCP source edit behind the link is a change")
		})

		It("CS-IMG-040: behind a followed link, .dockerignore debris rules apply to the target's real path", func() {
			tgt := filepath.Join(repo, "scaffold", "logstream")
			touchAt(filepath.Join(tgt, "run-logger.js"), old)
			Expect(os.Symlink("scaffold/logstream", filepath.Join(repo, "logstream"))).To(Succeed())
			a := toolsStamp()
			touchAt(filepath.Join(tgt, ".DS_Store"), old)
			Expect(toolsStamp()).To(Equal(a), "scaffold/logstream/.DS_Store is outside the build context")
		})

		DescribeTable("CS-IMG-040: a symlinked baked source whose target is outside the build context is never walked",
			func(link func(ext string) string) {
				ext := GinkgoT().TempDir()
				touchAt(filepath.Join(ext, "run-logger.js"), old)
				Expect(os.Symlink(link(ext), filepath.Join(repo, "logstream"))).To(Succeed())
				a := toolsStamp()
				Expect(a).NotTo(BeEmpty())
				Expect(errw.String()).NotTo(ContainSubstring("could not fingerprint"))
				Expect(os.WriteFile(filepath.Join(ext, "run-logger.js"), []byte("edited\n"), 0o644)).To(Succeed())
				touchAt(filepath.Join(ext, "more", "added.js"), time.Now())
				Expect(toolsStamp()).To(Equal(a), "COPY cannot read it, so its contents are not inputs")

				// Nor is it a time-rule trigger.
				images["claude-sandbox-tools"] = &imgState{created: imgT}
				fake.Calls = nil
				rebuilt, err := imagebuild.EnsureTools(o)
				Expect(err).NotTo(HaveOccurred())
				Expect(rebuilt).To(BeFalse())
			},
			Entry("an absolute target", func(ext string) string { return ext }),
			Entry("a ../ target", func(ext string) string {
				r, err := filepath.Rel(repo, ext)
				Expect(err).NotTo(HaveOccurred())
				Expect(r).To(HavePrefix(".."))
				return r
			}),
		)

		It("CS-IMG-040: the time rule follows a symlinked top-level baked directory", func() {
			tgt := filepath.Join(repo, "shared", "logstream")
			touchAt(filepath.Join(tgt, "run-logger.js"), old)
			Expect(os.Symlink("shared/logstream", filepath.Join(repo, "logstream"))).To(Succeed())
			images["claude-sandbox-tools"] = &imgState{created: imgT}
			fake.Calls = nil
			rebuilt, err := imagebuild.EnsureTools(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeFalse())
			touchAt(filepath.Join(tgt, "run-logger.js"), time.Now())
			rebuilt, err = imagebuild.EnsureTools(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeTrue(), "a newer file behind the link is a newer baked source")
		})

		It("CS-IMG-002: --rebuild rebuilds a labeled child even when the base image ID is unchanged", func() {
			parents()
			fp := childStamp()
			images["claude-sandbox-proj"] = &imgState{created: imgT, inputs: fp}
			o.ForceRebuild = true
			fake.Calls = nil
			_, built, err := imagebuild.EnsureChild(o, spec, false, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(built).To(BeTrue())
			Expect(stampOf(fake, "claude-sandbox-proj")).To(Equal(fp))
		})

		It("CS-IMG-034: the version stamp is not a base or tools input", func() {
			a, t := baseStamp(), toolsStamp()
			o.Version = "v9.9.9-dirty"
			Expect(baseStamp()).To(Equal(a))
			Expect(toolsStamp()).To(Equal(t))
		})

		It("CS-IMG-034: a labeled child rebuilds when the base image ID changes, even if the base is older than the child", func() {
			parents()
			fp := childStamp()
			images["claude-sandbox-proj"] = &imgState{created: imgT, inputs: fp}
			images["claude-sandbox"] = &imgState{created: imgT.Add(-time.Hour), id: "sha256:base2"}
			fake.Calls = nil
			_, built, err := imagebuild.EnsureChild(o, spec, false, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(built).To(BeTrue())
		})

		It("CS-IMG-034: a labeled child is left alone when the base was rebuilt to the same image", func() {
			parents()
			fp := childStamp()
			images["claude-sandbox-proj"] = &imgState{created: imgT, inputs: fp}
			images["claude-sandbox"] = &imgState{created: imgT.Add(time.Hour), id: "sha256:base1"}
			Expect(os.Chtimes(spec.Dockerfile, time.Now(), time.Now())).To(Succeed())
			fake.Calls = nil
			img, built, err := imagebuild.EnsureChild(o, spec, true, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(built).To(BeFalse())
			Expect(img).To(Equal("claude-sandbox-proj"))
			Expect(buildLines(fake)).To(BeEmpty())
		})

		It("CS-IMG-034: a labeled child rebuilds when its Dockerfile content changes", func() {
			parents()
			fp := childStamp()
			images["claude-sandbox-proj"] = &imgState{created: imgT, inputs: fp}
			Expect(os.WriteFile(spec.Dockerfile, []byte("FROM claude-sandbox\nRUN true\n"), 0o644)).To(Succeed())
			Expect(os.Chtimes(spec.Dockerfile, old, old)).To(Succeed())
			fake.Calls = nil
			_, built, err := imagebuild.EnsureChild(o, spec, false, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(built).To(BeTrue())
		})

		DescribeTable("CS-IMG-034: a labeled cap follows its parents' image IDs, not their Created times",
			func(mutate func(), wantBuilt bool) {
				parents()
				fp := capStamp()
				images["claude-sandbox-proj:run"] = &imgState{created: imgT, inputs: fp}
				mutate()
				fake.Calls = nil
				_, built, err := imagebuild.EnsureCap(o, "claude-sandbox-proj")
				Expect(err).NotTo(HaveOccurred())
				Expect(built).To(Equal(wantBuilt))
			},
			Entry("parent newer by Created but the same image", func() {
				images["claude-sandbox-proj"].created = imgT.Add(time.Hour)
				images["claude-sandbox-cli"].created = imgT.Add(time.Hour)
			}, false),
			Entry("parent switched to an older-built, different image", func() {
				images["claude-sandbox-proj"] = &imgState{created: imgT.Add(-time.Hour), id: "sha256:child0"}
			}, true),
			Entry("tools image changed", func() {
				images["claude-sandbox-tools"].id = "sha256:tools2"
			}, true),
			Entry("CLI image changed", func() {
				images["claude-sandbox-cli"].id = "sha256:cli2"
			}, true),
		)

		It("CS-IMG-035: an unlabeled image keeps the time-based rule and its rebuild stamps the label", func() {
			images["claude-sandbox-cli"] = &imgState{created: imgT}
			touchAt(filepath.Join(repo, "Dockerfile.cli"), time.Now())
			rebuilt, err := imagebuild.EnsureCLI(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeTrue())
			Expect(stampOf(fake, "claude-sandbox-cli")).NotTo(BeEmpty())
		})

		It("CS-IMG-035: a child whose fingerprint cannot be computed keeps the time-based rule", func() {
			// No base ID readable: the child fingerprint is undefined, so the
			// label is ignored and a fresh child by the time rule is kept.
			images["claude-sandbox"] = &imgState{created: imgT.Add(-time.Hour)}
			images["claude-sandbox-proj"] = &imgState{created: imgT, inputs: strings.Repeat("0", 64)}
			_, built, err := imagebuild.EnsureChild(o, spec, false, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(built).To(BeFalse())
			Expect(os.Chtimes(spec.Dockerfile, time.Now(), time.Now())).To(Succeed())
			_, built, err = imagebuild.EnsureChild(o, spec, false, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(built).To(BeTrue())
			Expect(stampOf(fake, "claude-sandbox-proj")).To(BeEmpty(), "nothing to stamp without a fingerprint")
		})

		It("CS-IMG-035: an uncomputable tools fingerprint warns and keeps the time rule", func() {
			if os.Geteuid() == 0 {
				Skip("root reads unreadable directories")
			}
			locked := filepath.Join(repo, "internal", "locked")
			touchAt(filepath.Join(locked, "x.go"), old)
			Expect(os.Chmod(locked, 0o000)).To(Succeed())
			DeferCleanup(func() { os.Chmod(locked, 0o755) })
			images["claude-sandbox-tools"] = &imgState{created: imgT, inputs: strings.Repeat("0", 64)}
			rebuilt, err := imagebuild.EnsureTools(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(rebuilt).To(BeFalse(), "nothing is newer than the image by the time rule")
			Expect(errw.String()).To(ContainSubstring("could not fingerprint the tools image inputs"))
		})

		It("CS-IMG-036: after a cached rebuild that kept Created, the next launch builds nothing", func() {
			// A fresh worktree: every source is newer than the images.
			parents()
			// The cap predates a parent, so the time rule rebuilds it too.
			images["claude-sandbox-proj:run"] = &imgState{created: imgT.Add(-time.Minute), id: "sha256:cap1"}
			for _, rel := range []string{"Dockerfile", "Dockerfile.tools", "Dockerfile.cli", "cmd/main.go", "entrypoint.sh"} {
				Expect(os.Chtimes(filepath.Join(repo, rel), time.Now(), time.Now())).To(Succeed())
			}
			Expect(os.Chtimes(spec.Dockerfile, time.Now(), time.Now())).To(Succeed())
			launch := func() []string {
				fake.Calls = nil
				baseRebuilt, err := imagebuild.EnsureBase(o)
				Expect(err).NotTo(HaveOccurred())
				_, err = imagebuild.EnsureTools(o)
				Expect(err).NotTo(HaveOccurred())
				_, err = imagebuild.EnsureCLI(o)
				Expect(err).NotTo(HaveOccurred())
				parent, _, err := imagebuild.EnsureChild(o, spec, baseRebuilt, false)
				Expect(err).NotTo(HaveOccurred())
				_, _, err = imagebuild.EnsureCap(o, parent)
				Expect(err).NotTo(HaveOccurred())
				return buildLines(fake)
			}
			Expect(launch()).To(HaveLen(5))
			// Fully cached: IDs and Created unchanged; only the labels are new.
			for _, name := range []string{"claude-sandbox", "claude-sandbox-tools", "claude-sandbox-cli", "claude-sandbox-proj", "claude-sandbox-proj:run"} {
				images[name].inputs = stampOf(fake, name)
				Expect(images[name].inputs).NotTo(BeEmpty(), name)
			}
			Expect(launch()).To(BeEmpty())
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
		var (
			cache string
			now   time.Time
		)

		// detached returns the processes started through Runner.Start with
		// Cmd.Detach — the background builds (CS-IMG-045).
		detached := func() []execx.Cmd {
			var out []execx.Cmd
			for _, c := range fake.Calls {
				if c.Detach {
					out = append(out, c)
				}
			}
			return out
		}

		cliBuild := func(v string) string {
			return "docker build -t claude-sandbox-cli --build-arg CLAUDE_CODE_VERSION=" + v +
				" -f " + filepath.Join(repo, "Dockerfile.cli") + " " + repo
		}

		stubVersions := func(pinned, latest string) {
			images["claude-sandbox-cli"] = &imgState{created: imgT, claudeVersion: pinned}
			fake.On("npm view @anthropic-ai/claude-code version", latest+"\n", nil)
		}

		npmCalls := func() int {
			n := 0
			for _, l := range fake.CommandLines() {
				if strings.Contains(l, "npm view") {
					n++
				}
			}
			return n
		}

		writeCache := func(name, body string) {
			Expect(os.MkdirAll(cache, 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(cache, name), []byte(body), 0o644)).To(Succeed())
		}
		versionCacheAt := func(v string, checked time.Time) {
			writeCache(imagebuild.VersionCacheFile, `{"latest":"`+v+`","checked":"`+checked.Format(time.RFC3339Nano)+`"}`)
		}
		statusAt := func(v string, ok bool, finished time.Time) {
			okS := "false"
			if ok {
				okS = "true"
			}
			writeCache(imagebuild.PrefetchStatusFile, `{"version":"`+v+`","ok":`+okS+`,"finished":"`+finished.Format(time.RFC3339Nano)+`"}`)
		}
		readCache := func() (string, time.Time) {
			raw, err := os.ReadFile(filepath.Join(cache, imagebuild.VersionCacheFile))
			Expect(err).NotTo(HaveOccurred())
			var c struct {
				Latest  string    `json:"latest"`
				Checked time.Time `json:"checked"`
			}
			Expect(json.Unmarshal(raw, &c)).To(Succeed())
			return c.Latest, c.Checked
		}
		// holdLock takes the prefetch lock the way a running background build
		// does: a flock on another open file description.
		holdLock := func() func() {
			Expect(os.MkdirAll(cache, 0o755)).To(Succeed())
			fh, err := os.OpenFile(filepath.Join(cache, imagebuild.PrefetchLockFile), os.O_RDWR|os.O_CREATE, 0o600)
			Expect(err).NotTo(HaveOccurred())
			Expect(syscall.Flock(int(fh.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)).To(Succeed())
			return func() { fh.Close() }
		}

		BeforeEach(func() {
			images["claude-sandbox"] = &imgState{created: imgT}
			cache = filepath.Join(GinkgoT().TempDir(), ".cache", "claude-sandbox")
			now = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
			o.CacheDir = cache
			o.Now = func() time.Time { return now }
			o.Self = "/opt/bin/claude-sandbox"
		})

		It("CS-IMG-006: compares the CLI image's label to the npm registry when the CLI image was not just built", func() {
			stubVersions("1.2.3", "1.2.3")
			Expect(imagebuild.UpdateCheck(o, false)).To(BeFalse())
			lines := fake.CommandLines()
			Expect(lines).To(ContainElement(ContainSubstring(`docker image inspect -f {{ index .Config.Labels "claude-sandbox.claude-version" }} claude-sandbox-cli`)))
			Expect(lines).NotTo(ContainElement(ContainSubstring("docker run")), "the label is read with an inspect, not by spawning a container")
			Expect(lines).To(ContainElement(ContainSubstring("npm view @anthropic-ai/claude-code version")))
			Expect(detached()).To(BeEmpty())
		})

		It("CS-IMG-006: falls back to the version file when the image was pinned to \"latest\"", func() {
			stubVersions("latest", "1.2.3")
			fake.On("--entrypoint cat claude-sandbox-cli /opt/claude-sandbox/claude-version", "1.2.3 (Claude Code)\n", nil)
			Expect(imagebuild.UpdateCheck(o, false)).To(BeFalse())
			Expect(detached()).To(BeEmpty())
		})

		It("CS-IMG-006: skips the check entirely when the CLI image was just built", func() {
			stubVersions("1.2.3", "1.2.4")
			Expect(imagebuild.UpdateCheck(o, true)).To(BeFalse())
			Expect(fake.CommandLines()).To(BeEmpty())
		})

		It("CS-IMG-007: skips the check when NoUpdateCheck is set", func() {
			stubVersions("1.2.3", "1.2.4")
			o.NoUpdateCheck = true
			Expect(imagebuild.UpdateCheck(o, false)).To(BeFalse())
			Expect(fake.CommandLines()).To(BeEmpty())
			Expect(detached()).To(BeEmpty())
		})

		It("CS-IMG-008: an available update never prompts and builds nothing in the foreground", func() {
			stubVersions("1.2.3", "1.2.4")
			Expect(imagebuild.UpdateCheck(o, false)).To(BeFalse(), "this launch builds nothing itself")
			Expect(buildLines(fake)).To(BeEmpty())
			Expect(detached()).To(HaveLen(1))
		})

		It("CS-IMG-008: without a cache directory the update is only reported", func() {
			stubVersions("1.2.3", "1.2.4")
			o.CacheDir = ""
			Expect(imagebuild.UpdateCheck(o, false)).To(BeFalse())
			Expect(buildLines(fake)).To(BeEmpty())
			Expect(detached()).To(BeEmpty())
			Expect(out.String()).To(ContainSubstring("update available: 1.2.3 → 1.2.4"))
		})

		It("CS-IMG-009: --update checks now and rebuilds ONLY the CLI image in the foreground, without prompting", func() {
			stubVersions("1.2.3", "1.2.4")
			o.AutoUpdate = true
			Expect(imagebuild.UpdateCheck(o, false)).To(BeTrue())
			Expect(buildLines(fake)).To(ConsistOf(cliBuild("1.2.4")))
			Expect(buildLines(fake)[0]).NotTo(ContainSubstring("-t claude-sandbox "), "the base is never rebuilt for a CLI update")
			Expect(detached()).To(BeEmpty(), "--update builds in the foreground, not in the background")
		})

		It("CS-IMG-009: --update bypasses a fresh version cache and rewrites it", func() {
			stubVersions("1.2.3", "1.2.5")
			versionCacheAt("1.2.3", now.Add(-time.Minute))
			o.AutoUpdate = true
			Expect(imagebuild.UpdateCheck(o, false)).To(BeTrue())
			Expect(npmCalls()).To(Equal(1))
			Expect(buildLines(fake)).To(ConsistOf(cliBuild("1.2.5")))
			v, checked := readCache()
			Expect(v).To(Equal("1.2.5"))
			Expect(checked).To(BeTemporally("==", now))
		})

		It("CS-IMG-044: a version checked less than 6 hours ago is used without asking npm", func() {
			stubVersions("1.2.3", "9.9.9")
			versionCacheAt("1.2.4", now.Add(-5*time.Hour-59*time.Minute))
			Expect(imagebuild.UpdateCheck(o, false)).To(BeFalse())
			Expect(npmCalls()).To(Equal(0))
			Expect(detached()).To(HaveLen(1))
			Expect(detached()[0].Args).To(Equal([]string{"cli-prefetch", "--dir", cache, "1.2.4"}), "the cached version, not the registry's")
		})

		It("CS-IMG-044: a cached version equal to the pinned one means no update, still without npm", func() {
			stubVersions("1.2.3", "9.9.9")
			versionCacheAt("1.2.3", now.Add(-time.Hour))
			Expect(imagebuild.UpdateCheck(o, false)).To(BeFalse())
			Expect(npmCalls()).To(Equal(0))
			Expect(detached()).To(BeEmpty())
			Expect(out.String()).To(BeEmpty())
		})

		It("CS-IMG-044: an expired, future-dated, corrupt or missing cache asks npm and rewrites the cache", func() {
			for _, prep := range []func(){
				func() { versionCacheAt("1.2.3", now.Add(-6*time.Hour)) },
				func() { versionCacheAt("1.2.3", now.Add(time.Hour)) },
				func() { writeCache(imagebuild.VersionCacheFile, "{not json") },
				// Unanchored matching would accept this and start a doomed
				// background build of "1.2.4 junk" on every launch.
				func() {
					writeCache(imagebuild.VersionCacheFile, `{"latest":"1.2.4 junk","checked":"`+now.Add(-time.Minute).Format(time.RFC3339Nano)+`"}`)
				},
				func() { os.RemoveAll(cache) },
			} {
				fake.Calls = nil
				prep()
				stubVersions("1.2.3", "1.2.3")
				Expect(imagebuild.UpdateCheck(o, false)).To(BeFalse())
				Expect(npmCalls()).To(Equal(1))
				v, checked := readCache()
				Expect(v).To(Equal("1.2.3"))
				Expect(checked).To(BeTemporally("==", now))
			}
		})

		It("CS-IMG-044: a failed or empty npm lookup is not cached", func() {
			images["claude-sandbox-cli"] = &imgState{created: imgT, claudeVersion: "1.2.3"}
			fake.On("npm view", "", execx.Fail(1))
			Expect(imagebuild.UpdateCheck(o, false)).To(BeFalse())
			_, err := os.Stat(filepath.Join(cache, imagebuild.VersionCacheFile))
			Expect(os.IsNotExist(err)).To(BeTrue())
			Expect(detached()).To(BeEmpty())
		})

		It("CS-IMG-045: a newer version starts one detached cli-prefetch of it and prints one line", func() {
			stubVersions("1.2.3", "1.2.4")
			Expect(os.MkdirAll(cache, 0o755)).To(Succeed())
			log := filepath.Join(cache, imagebuild.PrefetchLogFile)
			Expect(os.WriteFile(log, []byte("previous\n"), 0o600)).To(Succeed())
			Expect(imagebuild.UpdateCheck(o, false)).To(BeFalse())
			Expect(detached()).To(HaveLen(1))
			c := detached()[0]
			Expect(c.Name).To(Equal("/opt/bin/claude-sandbox"))
			Expect(c.Args).To(Equal([]string{"cli-prefetch", "--dir", cache, "1.2.4"}))
			Expect(c.Env).To(Equal([]string{"CLAUDE_SANDBOX_REPO_ROOT=" + repo}))
			Expect(c.DieWithParent).To(BeFalse(), "it must outlive the launcher")
			Expect(c.Stdin).To(BeNil(), "stdin is /dev/null")
			// stdout and stderr are the log itself, a real fd opened for append.
			for _, w := range []io.Writer{c.Stdout, c.Stderr} {
				fh, ok := w.(*os.File)
				Expect(ok).To(BeTrue(), "the log is handed over as a file, not a pipe")
				Expect(fh.Name()).To(Equal(log))
			}
			raw, _ := os.ReadFile(log)
			Expect(string(raw)).To(Equal("previous\n"), "the launcher appends, never truncates")
			Expect(strings.Count(out.String(), "\n")).To(Equal(1))
			Expect(out.String()).To(ContainSubstring("1.2.3 → 1.2.4"))
			Expect(out.String()).To(ContainSubstring("in the background"))
			Expect(out.String()).To(ContainSubstring(log))
			Expect(errw.String()).To(BeEmpty())
		})

		It("CS-IMG-045: a registry version older than the image is never built", func() {
			stubVersions("1.2.5", "1.2.4")
			Expect(imagebuild.UpdateCheck(o, false)).To(BeFalse())
			Expect(detached()).To(BeEmpty())
			Expect(out.String()).To(BeEmpty())
			o.AutoUpdate = true
			Expect(imagebuild.UpdateCheck(o, false)).To(BeFalse())
			Expect(buildLines(fake)).To(BeEmpty())
		})

		It("CS-IMG-045: a detach that cannot start warns once and the launch goes on", func() {
			stubVersions("1.2.3", "1.2.4")
			o.Self = "" // os.Executable failed: nothing to start
			Expect(imagebuild.UpdateCheck(o, false)).To(BeFalse())
			Expect(detached()).To(BeEmpty())
			Expect(strings.Count(errw.String(), "WARNING")).To(Equal(1))
			Expect(errw.String()).To(ContainSubstring("could not start"))
			Expect(errw.String()).To(ContainSubstring("--update"))
		})

		It("CS-IMG-046: a running background build means no second one, and one line saying so", func() {
			stubVersions("1.2.3", "1.2.4")
			release := holdLock()
			defer release()
			Expect(imagebuild.UpdateCheck(o, false)).To(BeFalse())
			Expect(detached()).To(BeEmpty())
			Expect(strings.Count(out.String(), "\n")).To(Equal(1))
			Expect(out.String()).To(ContainSubstring("already running"))
			Expect(out.String()).To(ContainSubstring(imagebuild.PrefetchLogFile))
		})

		It("CS-IMG-046: the launcher's probe releases the lock, so the child can take it", func() {
			stubVersions("1.2.3", "1.2.4")
			Expect(imagebuild.UpdateCheck(o, false)).To(BeFalse())
			release := holdLock() // fails the test if the probe still held it
			release()
		})

		It("CS-IMG-047: a failed build of the latest version warns naming the log and is not retried within 6 h", func() {
			stubVersions("1.2.3", "1.2.4")
			statusAt("1.2.4", false, now.Add(-5*time.Hour))
			Expect(imagebuild.UpdateCheck(o, false)).To(BeFalse())
			Expect(detached()).To(BeEmpty())
			Expect(buildLines(fake)).To(BeEmpty())
			Expect(strings.Count(errw.String(), "\n")).To(Equal(1))
			Expect(errw.String()).To(ContainSubstring("1.2.4 failed"))
			Expect(errw.String()).To(ContainSubstring(filepath.Join(cache, imagebuild.PrefetchLogFile)))
			Expect(out.String()).To(BeEmpty())
		})

		It("CS-IMG-047: after 6 h the failed version is retried in the background, still with the warning", func() {
			stubVersions("1.2.3", "1.2.4")
			statusAt("1.2.4", false, now.Add(-6*time.Hour))
			Expect(imagebuild.UpdateCheck(o, false)).To(BeFalse())
			Expect(detached()).To(HaveLen(1))
			Expect(errw.String()).To(ContainSubstring("1.2.4 failed"))
		})

		It("CS-IMG-047: a failure of an older version does not hold back a newer one", func() {
			stubVersions("1.2.3", "1.2.5")
			statusAt("1.2.4", false, now.Add(-time.Minute))
			Expect(imagebuild.UpdateCheck(o, false)).To(BeFalse())
			Expect(detached()).To(HaveLen(1))
			Expect(detached()[0].Args).To(Equal([]string{"cli-prefetch", "--dir", cache, "1.2.5"}))
		})

		It("CS-IMG-047: no warning once the image is on the failed version, nor after a success", func() {
			stubVersions("1.2.4", "1.2.4")
			statusAt("1.2.4", false, now.Add(-time.Minute))
			Expect(imagebuild.UpdateCheck(o, false)).To(BeFalse())
			Expect(errw.String()).To(BeEmpty())

			errw.Reset()
			statusAt("1.2.4", true, now.Add(-time.Minute))
			images["claude-sandbox-cli"].claudeVersion = "1.2.3"
			Expect(imagebuild.UpdateCheck(o, false)).To(BeFalse())
			Expect(errw.String()).To(BeEmpty())
		})

		It("CS-IMG-047: a failure of a version OLDER than the image's pin never warns", func() {
			// E1: v1.2.4 failed in the background, npm moved on, --update
			// built 1.2.5. The record must not nag until the next release.
			stubVersions("1.2.5", "1.2.5")
			statusAt("1.2.4", false, now.Add(-time.Minute))
			Expect(imagebuild.UpdateCheck(o, false)).To(BeFalse())
			Expect(errw.String()).To(BeEmpty())
			Expect(out.String()).To(BeEmpty())
			Expect(detached()).To(BeEmpty())
		})

		It("CS-IMG-047: a successful --update records its version, clearing a failure record", func() {
			stubVersions("1.2.3", "1.2.5")
			statusAt("1.2.4", false, now.Add(-time.Minute))
			o.AutoUpdate = true
			Expect(imagebuild.UpdateCheck(o, false)).To(BeTrue())
			raw, err := os.ReadFile(filepath.Join(cache, imagebuild.PrefetchStatusFile))
			Expect(err).NotTo(HaveOccurred())
			var st map[string]any
			Expect(json.Unmarshal(raw, &st)).To(Succeed())
			Expect(st["version"]).To(Equal("1.2.5"))
			Expect(st["ok"]).To(BeTrue())

			fake.On("docker build -t claude-sandbox-cli", "", execx.Fail(1))
			statusAt("1.2.4", false, now.Add(-time.Minute))
			images["claude-sandbox-cli"].claudeVersion = "1.2.3"
			Expect(imagebuild.UpdateCheck(o, false)).To(BeFalse())
			raw, _ = os.ReadFile(filepath.Join(cache, imagebuild.PrefetchStatusFile))
			Expect(string(raw)).To(ContainSubstring(`"version":"1.2.4"`), "a failed --update leaves the record alone")
		})

		It("CS-IMG-047: --update retries a failed version at once, in the foreground", func() {
			stubVersions("1.2.3", "1.2.4")
			statusAt("1.2.4", false, now.Add(-time.Minute))
			o.AutoUpdate = true
			Expect(imagebuild.UpdateCheck(o, false)).To(BeTrue())
			Expect(buildLines(fake)).To(ConsistOf(cliBuild("1.2.4")))
		})

		It("CS-IMG-047: an unusable cache directory warns once and the launch goes on", func() {
			stubVersions("1.2.3", "1.2.4")
			// A file where the directory should be: nothing under it can be
			// created, read or locked.
			Expect(os.MkdirAll(filepath.Dir(cache), 0o755)).To(Succeed())
			Expect(os.WriteFile(cache, []byte("x"), 0o644)).To(Succeed())
			Expect(imagebuild.UpdateCheck(o, false)).To(BeFalse())
			Expect(detached()).To(BeEmpty())
			Expect(strings.Count(errw.String(), "\n")).To(Equal(1), errw.String())
			Expect(errw.String()).To(ContainSubstring("version cache"))
			Expect(errw.String()).To(ContainSubstring("1.2.3 → 1.2.4"))

			errw.Reset()
			images["claude-sandbox-cli"].claudeVersion = "1.2.4"
			Expect(imagebuild.UpdateCheck(o, false)).To(BeFalse())
			Expect(strings.Count(errw.String(), "\n")).To(Equal(1), "no update: still one line about the cache")
		})
	})

	Describe("Prefetch (cli-prefetch)", func() {
		var (
			cache string
			now   time.Time
		)
		prefetchBuild := func(v string) string {
			return "docker build -t claude-sandbox-cli:prefetch --build-arg CLAUDE_CODE_VERSION=" + v +
				" -f " + filepath.Join(repo, "Dockerfile.cli") + " " + repo
		}
		docker := func(sub string) []string {
			var out []string
			for _, l := range fake.CommandLines() {
				if strings.HasPrefix(l, "docker "+sub+" ") {
					out = append(out, l)
				}
			}
			return out
		}
		status := func() map[string]any {
			raw, err := os.ReadFile(filepath.Join(cache, imagebuild.PrefetchStatusFile))
			Expect(err).NotTo(HaveOccurred())
			var m map[string]any
			Expect(json.Unmarshal(raw, &m)).To(Succeed())
			return m
		}

		BeforeEach(func() {
			cache = filepath.Join(GinkgoT().TempDir(), "cache")
			now = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
			o.CacheDir = cache
			o.Now = func() time.Time { return now }
			images["claude-sandbox-cli"] = &imgState{created: imgT, claudeVersion: "1.2.3"}
		})

		It("CS-IMG-045: builds ONLY the CLI image under a temporary tag, then moves claude-sandbox-cli onto it", func() {
			Expect(os.MkdirAll(cache, 0o755)).To(Succeed())
			log := filepath.Join(cache, imagebuild.PrefetchLogFile)
			Expect(os.WriteFile(log, []byte("an older build's log\n"), 0o600)).To(Succeed())
			Expect(imagebuild.Prefetch(o, "1.2.4")).To(Succeed())
			Expect(buildLines(fake)).To(ConsistOf(prefetchBuild("1.2.4")))
			Expect(docker("tag")).To(ConsistOf("docker tag claude-sandbox-cli:prefetch claude-sandbox-cli"))
			Expect(docker("rmi")).To(ConsistOf("docker rmi claude-sandbox-cli:prefetch"), "only the temporary name goes")
			// The pin is re-read after the build, before the tag moves.
			tagAt, lastPin := -1, -1
			for i, l := range fake.CommandLines() {
				if strings.Contains(l, "claude-sandbox.claude-version") {
					lastPin = i
				}
				if strings.HasPrefix(l, "docker tag ") {
					tagAt = i
				}
			}
			Expect(lastPin).To(BeNumerically("<", tagAt))
			Expect(lastPin).To(BeNumerically(">", 0))
			Expect(status()).To(Equal(map[string]any{"version": "1.2.4", "ok": true, "finished": now.Format(time.RFC3339)}))
			raw, _ := os.ReadFile(log)
			Expect(string(raw)).NotTo(ContainSubstring("an older build's log"), "the log holds the latest build only")
		})

		It("CS-IMG-045: skips the build unless the version is newer than the image's pin", func() {
			for _, pinned := range []string{"1.2.4", "1.2.5", "2.0.0"} {
				fake.Calls = nil
				images["claude-sandbox-cli"].claudeVersion = pinned
				Expect(imagebuild.Prefetch(o, "1.2.4")).To(Succeed())
				Expect(buildLines(fake)).To(BeEmpty(), "pinned %s", pinned)
				Expect(docker("tag")).To(BeEmpty())
				Expect(status()["ok"]).To(BeTrue())
			}
		})

		It("CS-IMG-045: never moves the tag back when a later version landed during the build", func() {
			// A foreground --update to 1.2.5 finishes while the background
			// 1.2.4 build runs: the second pin read sees it.
			fake.OnFunc("docker build -t claude-sandbox-cli:prefetch", func(execx.Cmd) (string, error) {
				images["claude-sandbox-cli"].claudeVersion = "1.2.5"
				return "", nil
			})
			Expect(imagebuild.Prefetch(o, "1.2.4")).To(Succeed())
			Expect(buildLines(fake)).To(ConsistOf(prefetchBuild("1.2.4")))
			Expect(docker("tag")).To(BeEmpty())
			Expect(docker("rmi")).To(ConsistOf("docker rmi claude-sandbox-cli:prefetch"))
			Expect(out.String()).To(ContainSubstring("moved to 1.2.5 during the build"))
			Expect(status()["ok"]).To(BeTrue(), "superseded is not a failure")
		})

		It("CS-IMG-045: rejects anything but an exact X.Y.Z version", func() {
			for _, v := range []string{"latest", "1.2", "1.2.3; rm -rf /", ""} {
				Expect(imagebuild.Prefetch(o, v)).NotTo(Succeed())
			}
			Expect(buildLines(fake)).To(BeEmpty())
		})

		It("CS-IMG-046: does not wait for a held lock; it logs the skip and succeeds without building", func() {
			Expect(os.MkdirAll(cache, 0o755)).To(Succeed())
			fh, err := os.OpenFile(filepath.Join(cache, imagebuild.PrefetchLockFile), os.O_RDWR|os.O_CREATE, 0o600)
			Expect(err).NotTo(HaveOccurred())
			defer fh.Close()
			Expect(syscall.Flock(int(fh.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)).To(Succeed())
			Expect(imagebuild.Prefetch(o, "1.2.4")).To(Succeed())
			Expect(buildLines(fake)).To(BeEmpty())
			Expect(out.String()).To(ContainSubstring("skipping"))
			_, err = os.Stat(filepath.Join(cache, imagebuild.PrefetchStatusFile))
			Expect(os.IsNotExist(err)).To(BeTrue(), "a skipped run records nothing")
		})

		It("CS-IMG-047: a failed build is logged, recorded as failed, and claude-sandbox-cli is left alone", func() {
			fake.On("docker build -t claude-sandbox-cli:prefetch", "", execx.Fail(1))
			Expect(imagebuild.Prefetch(o, "1.2.4")).NotTo(Succeed())
			Expect(docker("tag")).To(BeEmpty())
			Expect(errw.String()).To(ContainSubstring("FAILED"))
			Expect(status()).To(Equal(map[string]any{"version": "1.2.4", "ok": false, "finished": now.Format(time.RFC3339)}))
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
			img, built, err := imagebuild.EnsureChild(o, spec, false, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(built).To(BeFalse())
			Expect(img).To(Equal("claude-sandbox"))
			Expect(errw.String()).To(BeEmpty())
			Expect(buildLines(fake)).To(BeEmpty())
		})

		Describe("a linked worktree (CS-CASC-033)", func() {
			var main, wt string
			BeforeEach(func() {
				base := filepath.Dir(proj)
				main = filepath.Join(base, "ws", "repo")
				wt = filepath.Join(base, "paseo", "abc", "feat")
				Expect(os.MkdirAll(main, 0o755)).To(Succeed())
				Expect(os.MkdirAll(wt, 0o755)).To(Succeed())
			})

			It("CS-CASC-033: resolves the main checkout's Dockerfile with the main checkout as context — the same image tag", func() {
				df := filepath.Join(main, ".claude-sandbox", "Dockerfile")
				touchAt(df, old)
				spec := resolve(imagebuild.ChildInputs{ProjectDir: wt, MainCheckout: main})
				Expect(spec.Use).To(BeTrue())
				Expect(spec.Dockerfile).To(Equal(df))
				Expect(spec.Context).To(Equal(main))
				Expect(spec.ImageName).To(Equal(resolve(imagebuild.ChildInputs{ProjectDir: main}).ImageName))
				Expect(out.String()).To(ContainSubstring("Found Dockerfile in main checkout: " + main))
			})

			It("CS-CASC-033: without the main checkout the physical walk never reaches it", func() {
				touchAt(filepath.Join(main, ".claude-sandbox", "Dockerfile"), old)
				Expect(resolve(imagebuild.ChildInputs{ProjectDir: wt}).Use).To(BeFalse())
			})

			It("CS-CASC-033: the worktree's own Dockerfile still wins, with the worktree as context", func() {
				touchAt(filepath.Join(main, ".claude-sandbox", "Dockerfile"), old)
				own := filepath.Join(wt, ".claude-sandbox", "Dockerfile")
				touchAt(own, old)
				spec := resolve(imagebuild.ChildInputs{ProjectDir: wt, MainCheckout: main})
				Expect(spec.Dockerfile).To(Equal(own))
				Expect(spec.Context).To(Equal(wt))
			})

			It("CS-CASC-033: a dockerfile name without dockerfileDir is searched along the same chain", func() {
				df := filepath.Join(main, "Dockerfile.sandbox")
				touchAt(df, old)
				spec := resolve(imagebuild.ChildInputs{ProjectDir: wt, MainCheckout: main, Dockerfile: "Dockerfile.sandbox"})
				Expect(spec.Use).To(BeTrue())
				Expect(spec.Dockerfile).To(Equal(df))
				Expect(spec.Context).To(Equal(main))
			})
		})

		It("CS-IMG-014: missing child Dockerfile warns but proceeds on the base image", func() {
			spec := resolve(imagebuild.ChildInputs{})
			Expect(spec.Use).To(BeFalse())
			img, _, err := imagebuild.EnsureChild(o, spec, false, false)
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
			img, _, err := imagebuild.EnsureChild(o, spec, false, false)
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
				img, built, err := imagebuild.EnsureChild(o, spec, baseRebuilt, false)
				Expect(err).NotTo(HaveOccurred())
				Expect(built).To(BeTrue())
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
			_, _, err := imagebuild.EnsureChild(o, spec, false, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(out.String()).To(ContainSubstring("Base image is newer than child"))
		})

		It("CS-IMG-017: a fresh child is not rebuilt and is the cap's parent", func() {
			images["claude-sandbox-proj"] = &imgState{created: imgT}
			images["claude-sandbox"] = &imgState{created: imgT.Add(-30 * time.Minute)}
			img, built, err := imagebuild.EnsureChild(o, spec, false, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(built).To(BeFalse())
			Expect(img).To(Equal("claude-sandbox-proj"))
			Expect(buildLines(fake)).To(BeEmpty())
		})
	})

	// ---- --version report ----

	Describe("PrintVersion", func() {
		It("CS-LNCH-030: prints \"(not built yet)\" for the tools and CLI images when neither exists", func() {
			imagebuild.PrintVersion(o)
			Expect(out.String()).To(ContainSubstring("claude-sandbox v1.2.3"))
			Expect(strings.Count(out.String(), "(not built yet)")).To(Equal(2))
		})

		It("CS-LNCH-030: prints host and baked versions and notes a mismatch rebuilds only with a baked-source change", func() {
			images["claude-sandbox-tools"] = &imgState{created: imgT, revision: "v0.9.0"}
			imagebuild.PrintVersion(o)
			Expect(out.String()).To(ContainSubstring("claude-sandbox v1.2.3"))
			Expect(out.String()).To(ContainSubstring("tools:        v0.9.0  (image claude-sandbox-tools"))
			Expect(out.String()).To(ContainSubstring("rebuilds when a baked source changes"))
			Expect(out.String()).NotTo(ContainSubstring("next launch"), "the stamp is not an input: a mismatch alone rebuilds nothing")
		})

		It("CS-LNCH-030: the baked version is read from the tools image, not the base", func() {
			images["claude-sandbox"] = &imgState{created: imgT, revision: "v0.1.0"}
			imagebuild.PrintVersion(o)
			Expect(out.String()).NotTo(ContainSubstring("v0.1.0"))
			Expect(out.String()).To(ContainSubstring("tools:        (not built yet)"))
		})

		It("CS-LNCH-030: prints the Claude Code version pinned in the CLI image", func() {
			images["claude-sandbox-tools"] = &imgState{created: imgT, revision: "v1.2.3"}
			images["claude-sandbox-cli"] = &imgState{created: imgT, claudeVersion: "2.1.247"}
			imagebuild.PrintVersion(o)
			Expect(out.String()).To(ContainSubstring("claude:       2.1.247"))
			Expect(out.String()).To(ContainSubstring("claude-sandbox-cli"))
		})

		It("CS-LNCH-030: prints no mismatch note when versions agree", func() {
			images["claude-sandbox-tools"] = &imgState{created: imgT, revision: "v1.2.3"}
			imagebuild.PrintVersion(o)
			Expect(out.String()).NotTo(ContainSubstring("rebuilds when"))
		})
	})
})
