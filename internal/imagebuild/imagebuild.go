// Package imagebuild manages the layered image lifecycle: base image staleness
// + rebuild, the Claude Code CLI image and its update check, child Dockerfile
// resolution + build, and the generated run image ("cap") that copies the CLI
// onto the base or child. Spec: spec/image-build.feature (CS-IMG).
//
// The CLI is deliberately kept out of the base and the children. Installed
// mid-Dockerfile, every update invalidated the base from that layer down and
// — because each child's FROM ID changed — rebuilt every child cold. Now an
// update rebuilds one small image (claude-sandbox-cli) and a one-layer cap per
// project; the base and children are untouched.
package imagebuild

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/paths"
	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
)

const (
	BaseImageName = "claude-sandbox"

	// CLIImageName is the image whose only job is installing Claude Code
	// (CS-IMG-020..023). Built from CLIDockerfile in the repo root.
	CLIImageName  = "claude-sandbox-cli"
	CLIDockerfile = "Dockerfile.cli"

	// CLIVersionLabel records the version the CLI image was pinned to; the
	// update check reads it with an inspect instead of spawning a container.
	CLIVersionLabel = "claude-sandbox.claude-version"

	// CLIVersionFile is copied into the cap so the version is discoverable
	// in-container as well.
	CLIVersionFile = "/opt/claude-sandbox/claude-version"

	// CapTag suffixes the parent image name to form the run image (CS-IMG-024).
	CapTag = "run"
)

// buildEnv is added to every docker build the launcher issues (CS-IMG-027):
// COPY --link, RUN --mount=type=cache and stdin builds all need BuildKit.
var buildEnv = []string{"DOCKER_BUILDKIT=1"}

// EnsureBuildKit verifies the buildx plugin is present (CS-IMG-027). Without
// it a modern CLI silently falls back to the legacy builder and the first
// BuildKit-only instruction fails with an unhelpful parse error.
func EnsureBuildKit(o Options) error {
	err := o.Runner.Run(execx.Cmd{Name: "docker", Args: []string{"buildx", "version"}, Stdout: io.Discard, Stderr: io.Discard})
	if err == nil {
		return nil
	}
	return fmt.Errorf("Error: claude-sandbox needs BuildKit to build images, but \"docker buildx version\" failed.\n" +
		"  Install the docker-buildx-plugin package (apt/dnf: docker-buildx-plugin; Docker Desktop ships it) and retry.")
}

// Options configures the image lifecycle for one launch.
type Options struct {
	Runner        execx.Runner
	Prompter      prompt.Prompter
	Out           io.Writer
	Err           io.Writer
	RepoRoot      string // sandbox repo checkout (Dockerfile, baked sources)
	Version       string // git-describe version stamp
	ForceRebuild  bool   // --rebuild
	NoUpdateCheck bool   // --no-update-check / env / config
	AutoUpdate    bool   // --update: auto-accept the update rebuild (CS-IMG-009)
}

// BakedSources are the paths (relative to RepoRoot) whose mtimes trigger a
// base rebuild when newer than the image (CS-IMG-004).
var BakedSources = []string{"cmd", "internal", "go.mod", "go.sum", "assets.go", "logstream", "entrypoint.sh", "PROMPT_RALPH.md", "mcp"}

// Version computes the git-describe version of the repo checkout.
func Version(r execx.Runner, repoRoot string) string {
	out, err := r.Output(execx.Cmd{Name: "git", Args: []string{"-C", repoRoot, "describe", "--tags", "--always", "--dirty"}, Stderr: io.Discard})
	if err != nil || strings.TrimSpace(out) == "" {
		return "unknown"
	}
	return strings.TrimSpace(out)
}

// EnsureBase builds the base image when missing or stale.
// Returns whether a build happened.
func EnsureBase(o Options) (rebuilt bool, err error) {
	need := false
	switch {
	case o.ForceRebuild:
		need = true
	case !imageExists(o.Runner, BaseImageName):
		need = true
	default:
		created := imageCreated(o.Runner, BaseImageName)
		if !created.IsZero() {
			if mtimeAfter(filepath.Join(o.RepoRoot, "Dockerfile"), created) {
				need = true
			} else if anyNewer(o.RepoRoot, BakedSources, created) {
				fmt.Fprintln(o.Out, "Baked sources changed since last build — rebuilding base image.")
				need = true
			}
		}
	}
	if !need {
		return false, nil
	}
	fmt.Fprintf(o.Out, "Building %s base image (%s)...\n", BaseImageName, o.Version)
	args := []string{"build", "-t", BaseImageName, "--build-arg", "CLAUDE_SANDBOX_VERSION=" + o.Version}
	if o.ForceRebuild {
		args = append(args, "--no-cache")
	}
	args = append(args, o.RepoRoot)
	err = o.Runner.Run(execx.Cmd{Name: "docker", Args: args, Env: buildEnv, Stdout: o.Out, Stderr: o.Err})
	return true, err
}

var semverRe = regexp.MustCompile(`[0-9]+\.[0-9]+\.[0-9]+`)

// latestClaudeVersion asks the npm registry for the current release. Empty
// when the registry is unreachable or answers with something that is not a
// version.
func latestClaudeVersion(o Options) string {
	out, err := o.Runner.Output(execx.Cmd{Name: "npm", Args: []string{"view", "@anthropic-ai/claude-code", "version"}, Stderr: io.Discard})
	if err != nil {
		return ""
	}
	return semverRe.FindString(out)
}

// resolveClaudeVersion is the pin handed to Dockerfile.cli: the registry's
// latest, or "latest" — the installer's own keyword — when the registry cannot
// be reached (CS-IMG-023). Every build path routes through here so the CLI
// image is always pinned the same way.
func resolveClaudeVersion(o Options) string {
	if v := latestClaudeVersion(o); v != "" {
		return v
	}
	fmt.Fprintln(o.Err, "WARNING: could not resolve the latest Claude Code version from npm; installing \"latest\".")
	return "latest"
}

// buildCLI runs the CLI image build pinned to version (CS-IMG-021).
func buildCLI(o Options, version string, noCache bool) error {
	fmt.Fprintf(o.Out, "Building %s image (Claude Code %s)...\n", CLIImageName, version)
	args := []string{"build", "-t", CLIImageName, "--build-arg", "CLAUDE_CODE_VERSION=" + version}
	if noCache {
		args = append(args, "--no-cache")
	}
	args = append(args, "-f", filepath.Join(o.RepoRoot, CLIDockerfile), o.RepoRoot)
	return o.Runner.Run(execx.Cmd{Name: "docker", Args: args, Env: buildEnv, Stdout: o.Out, Stderr: o.Err})
}

// EnsureCLI builds the Claude Code CLI image when missing or stale
// (CS-IMG-021, CS-IMG-022, CS-IMG-002). Returns whether a build happened.
func EnsureCLI(o Options) (rebuilt bool, err error) {
	need := false
	switch {
	case o.ForceRebuild:
		need = true
	case !imageExists(o.Runner, CLIImageName):
		need = true
	default:
		created := imageCreated(o.Runner, CLIImageName)
		if !created.IsZero() && mtimeAfter(filepath.Join(o.RepoRoot, CLIDockerfile), created) {
			need = true
		}
	}
	if !need {
		return false, nil
	}
	return true, buildCLI(o, resolveClaudeVersion(o), o.ForceRebuild)
}

// pinnedClaudeVersion reads the version the CLI image was built for: the
// label first (CS-IMG-006); when the image was pinned to "latest" the label
// says nothing useful, so fall back to the version file the installer left.
func pinnedClaudeVersion(o Options) string {
	label, _ := o.Runner.Output(execx.Cmd{
		Name:   "docker",
		Args:   []string{"image", "inspect", "-f", `{{ index .Config.Labels "` + CLIVersionLabel + `" }}`, CLIImageName},
		Stderr: io.Discard,
	})
	if v := semverRe.FindString(label); v != "" {
		return v
	}
	file, _ := o.Runner.Output(execx.Cmd{
		Name:   "docker",
		Args:   []string{"run", "--rm", "--entrypoint", "cat", CLIImageName, CLIVersionFile},
		Stderr: io.Discard,
	})
	return semverRe.FindString(file)
}

// UpdateCheck compares the CLI image's pinned Claude Code version against the
// npm registry and offers to rebuild the CLI image — only that image
// (CS-IMG-006..009). cliBuilt skips the check: a fresh CLI image is already
// current. Returns whether the CLI image was rebuilt here.
func UpdateCheck(o Options, cliBuilt bool) bool {
	if o.NoUpdateCheck || cliBuilt {
		return false
	}
	pinned := pinnedClaudeVersion(o)
	latest := latestClaudeVersion(o)
	if pinned == "" || latest == "" || pinned == latest {
		return false
	}
	fmt.Fprintf(o.Out, "\nClaude Code update available: %s → %s\n", pinned, latest)
	accept := o.AutoUpdate
	if !accept {
		accept = o.Prompter.Confirm("", "Rebuild Claude Code image to update?", false, 5*time.Second)
	}
	if !accept {
		fmt.Fprintln(o.Out)
		return false
	}
	// The pin is a build arg, so the install layer busts on its own; no
	// --no-cache needed, and the base and children are never touched.
	err := buildCLI(o, latest, false)
	fmt.Fprintln(o.Out)
	return err == nil
}

// CapImageName is the run image for a base or child image (CS-IMG-024).
// Resolved in one place so the launch and the attach/join fingerprint cannot
// disagree about which image a container runs (CS-SESS-038).
func CapImageName(under string) string {
	return under + ":" + CapTag
}

// capDockerfile is the generated Dockerfile fed to docker on stdin. COPY --link
// makes the layer independent of the parent's content; no --chown because the
// CLI image installs as uid 1000 and --link preserves it (a named --chown for
// a user absent from the parent would silently yield root).
func capDockerfile(under string) string {
	return "# syntax=docker/dockerfile:1\n" +
		"FROM " + under + "\n" +
		"COPY --link --from=" + CLIImageName + " /home/claude/.local /home/claude/.local\n" +
		"COPY --link --from=" + CLIImageName + " " + CLIVersionFile + " " + CLIVersionFile + "\n"
}

// EnsureCap builds the run image over under when missing or stale
// (CS-IMG-024..026). Returns the image to run and whether a build happened.
func EnsureCap(o Options, under string) (image string, built bool, err error) {
	cap := CapImageName(under)
	need := false
	switch {
	case o.ForceRebuild:
		need = true
	case !imageExists(o.Runner, cap):
		need = true
	default:
		capCreated := imageCreated(o.Runner, cap)
		if capCreated.IsZero() {
			need = true
			break
		}
		if u := imageCreated(o.Runner, under); !u.IsZero() && u.After(capCreated) {
			need = true
		} else if c := imageCreated(o.Runner, CLIImageName); !c.IsZero() && c.After(capCreated) {
			need = true
		}
	}
	if !need {
		return cap, false, nil
	}
	fmt.Fprintf(o.Out, "Building %s run image (%s + Claude Code)...\n", cap, under)
	err = o.Runner.Run(execx.Cmd{
		Name:   "docker",
		Args:   []string{"build", "-t", cap, "-"},
		Env:    buildEnv,
		Stdin:  strings.NewReader(capDockerfile(under)),
		Stdout: o.Out, Stderr: o.Err,
	})
	if err != nil {
		return "", true, err
	}
	return cap, true, nil
}

// ChildSpec is the resolved child Dockerfile plan.
type ChildSpec struct {
	Use        bool
	Dockerfile string
	Context    string
	ImageName  string
}

// ChildInputs are the resolution inputs (env vars already read by the caller).
type ChildInputs struct {
	ProjectDir    string
	BaseOnly      bool
	DockerfileDir string // env CLAUDE_SANDBOX_DOCKERFILE_DIR > config dockerfileDir
	Dockerfile    string // env CLAUDE_SANDBOX_DOCKERFILE > config dockerfile
}

// used builds a ChildSpec for a resolved Dockerfile. The image name is derived
// here rather than up front because it depends on what was resolved — see
// ImageSlug (CS-IMG-015).
func used(dockerfile, context string) ChildSpec {
	return ChildSpec{
		Use:        true,
		Dockerfile: dockerfile,
		Context:    context,
		ImageName:  BaseImageName + "-" + ImageSlug(dockerfile, context),
	}
}

// ResolveChild determines which child Dockerfile (if any) to use, and its
// build context (CS-IMG-010..014). When no child is in use the returned
// ImageName is empty: EnsureChild uses BaseImageName on that path.
func ResolveChild(in ChildInputs, out io.Writer) ChildSpec {
	var spec ChildSpec
	if in.BaseOnly {
		return spec
	}
	override := in.DockerfileDir != "" || in.Dockerfile != ""
	if override {
		dir := in.DockerfileDir
		if dir == "" {
			dir = in.ProjectDir
		}
		name := in.Dockerfile
		if name == "" {
			name = "Dockerfile"
		}
		df := filepath.Join(dir, name)
		if fileExists(df) {
			// Honored verbatim: context stays the override directory.
			return used(df, dir)
		}
		// Walk parents for the exact override filename.
		if found := paths.FindUpFile(filepath.Dir(dir), name); found != "" {
			fmt.Fprintf(out, "Found %s in parent directory: %s\n", name, filepath.Dir(found))
			return used(found, filepath.Dir(found))
		}
		spec.Dockerfile = df
		return spec
	}
	// Default: .claude-sandbox/Dockerfile with the PROJECT ROOT as context so
	// COPY instructions reference the project.
	df, _ := paths.Resolve(in.ProjectDir, paths.Dockerfile)
	if fileExists(df) {
		return used(df, in.ProjectDir)
	}
	if found, _ := paths.FindUp(filepath.Dir(in.ProjectDir), paths.Dockerfile); found != "" {
		ctx := filepath.Dir(filepath.Dir(found)) // parent of the .claude-sandbox/ dir
		fmt.Fprintf(out, "Found %s in parent directory: %s\n", filepath.Base(found), ctx)
		// Every project resolving to this same shared Dockerfile gets the same
		// context and therefore the same image tag — one build, not one per
		// project (CS-IMG-018).
		return used(found, ctx)
	}
	spec.Dockerfile = df
	return spec
}

// EnsureChild builds the child image when in use and stale (CS-IMG-015..017).
// Returns the cap's parent image and whether a build happened.
func EnsureChild(o Options, spec ChildSpec, baseRebuilt bool, baseOnly bool) (image string, built bool, err error) {
	if !spec.Use {
		if !baseOnly {
			fmt.Fprintf(o.Err, "WARNING: No child Dockerfile found at %s\n", spec.Dockerfile)
			fmt.Fprintf(o.Err, "  Project-specific tools (Go, TypeScript LSP, etc.) will not be available.\n")
			fmt.Fprintf(o.Err, "  Create .claude-sandbox/Dockerfile to add them, or set baseOnly: true\n")
			fmt.Fprintf(o.Err, "  in your config to suppress this warning.\n\n")
		}
		return BaseImageName, false, nil
	}
	need := false
	switch {
	case !imageExists(o.Runner, spec.ImageName):
		need = true
	case baseRebuilt:
		need = true
	default:
		childCreated := imageCreated(o.Runner, spec.ImageName)
		if !childCreated.IsZero() {
			if mtimeAfter(spec.Dockerfile, childCreated) {
				need = true
			} else if base := imageCreated(o.Runner, BaseImageName); !base.IsZero() && base.After(childCreated) {
				// Base rebuilt out-of-band: the child still carries the old
				// base's layers.
				fmt.Fprintln(o.Out, "Base image is newer than child — rebuilding child.")
				need = true
			}
		}
	}
	if !need {
		return spec.ImageName, false, nil
	}
	fmt.Fprintf(o.Out, "Building %s child image from %s (context: %s)...\n", spec.ImageName, spec.Dockerfile, spec.Context)
	err = o.Runner.Run(execx.Cmd{
		Name:   "docker",
		Args:   []string{"build", "-t", spec.ImageName, "-f", spec.Dockerfile, spec.Context},
		Env:    buildEnv,
		Stdout: o.Out, Stderr: o.Err,
	})
	if err != nil {
		return "", true, err
	}
	return spec.ImageName, true, nil
}

// PrintVersion implements --version (CS-LNCH-030).
func PrintVersion(o Options) {
	fmt.Fprintf(o.Out, "claude-sandbox %s  (host: %s)\n", o.Version, o.RepoRoot)
	if !imageExists(o.Runner, BaseImageName) {
		fmt.Fprintln(o.Out, "  image:        (not built yet)")
	} else {
		baked, _ := o.Runner.Output(execx.Cmd{
			Name:   "docker",
			Args:   []string{"image", "inspect", "-f", `{{ index .Config.Labels "org.opencontainers.image.revision" }}`, BaseImageName},
			Stderr: io.Discard,
		})
		baked = strings.TrimSpace(baked)
		if baked == "" {
			baked = "unknown"
		}
		fmt.Fprintf(o.Out, "  image:        %s  (built %s)\n", baked, createdDate(o, BaseImageName))
		if baked != "unknown" && baked != o.Version {
			fmt.Fprintln(o.Out, "  note: image differs from host scripts — it will auto-rebuild on next launch (or run --rebuild).")
		}
	}
	if !imageExists(o.Runner, CLIImageName) {
		fmt.Fprintln(o.Out, "  claude:       (not built yet)")
		return
	}
	v := pinnedClaudeVersion(o)
	if v == "" {
		v = "unknown"
	}
	fmt.Fprintf(o.Out, "  claude:       %s  (image %s, built %s)\n", v, CLIImageName, createdDate(o, CLIImageName))
}

func createdDate(o Options, image string) string {
	if created := imageCreated(o.Runner, image); !created.IsZero() {
		return created.Format("2006-01-02")
	}
	return "?"
}

// ---- BuildKit cache budget (CS-IMG-028) ----

// CacheBudget is what the daemon reports about its build cache: the current
// size and the GC policy thresholds that matter to the sandbox.
type CacheBudget struct {
	Size      int64 // build cache in use
	AllBudget int64 // Max Used Space of the rule with All: true
	Ephemeral int64 // Max Used Space of the rule filtering type==exec.cachemount
}

const (
	cacheWarnRatio     = 0.8
	ephemeralFloorGiB  = 20
	ephemeralFloorSize = int64(ephemeralFloorGiB) << 30
)

// WarnCacheBudget prints a warning when the daemon's build cache is close to
// its GC budget, or when the budget for ephemeral records (which is where
// RUN --mount=type=cache lives) is too small to hold a day of sandbox builds.
// Called only when a build ran this launch: the check costs two docker calls,
// and the common no-build launch should not pay them. Silent when either
// command cannot be parsed.
func WarnCacheBudget(o Options) {
	b, ok := readCacheBudget(o)
	if !ok {
		return
	}
	over := b.AllBudget > 0 && float64(b.Size) >= cacheWarnRatio*float64(b.AllBudget)
	tiny := b.Ephemeral > 0 && b.Ephemeral < ephemeralFloorSize
	if !over && !tiny {
		return
	}
	fmt.Fprintf(o.Err, "\nWARNING: BuildKit build cache: %s of a %s budget (cache-mount budget %s).\n",
		humanSize(b.Size), humanSize(b.AllBudget), humanSize(b.Ephemeral))
	fmt.Fprintf(o.Err, "  Cache mounts are being evicted between builds, so every apt/pip/npm/go step downloads again.\n")
	fmt.Fprintf(o.Err, "  Prune:            docker builder prune -af\n")
	fmt.Fprintf(o.Err, "  Raise the budget: see README.md, \"BuildKit cache\" (builder.gc.defaultKeepStorage in daemon.json).\n\n")
}

func readCacheBudget(o Options) (CacheBudget, bool) {
	df, err := o.Runner.Output(execx.Cmd{Name: "docker", Args: []string{"system", "df", "--format", "{{json .}}"}, Stderr: io.Discard})
	if err != nil {
		return CacheBudget{}, false
	}
	size, ok := parseBuildCacheSize(df)
	if !ok {
		return CacheBudget{}, false
	}
	insp, err := o.Runner.Output(execx.Cmd{Name: "docker", Args: []string{"buildx", "inspect"}, Stderr: io.Discard})
	if err != nil {
		return CacheBudget{}, false
	}
	all, eph, ok := parseGCPolicy(insp)
	if !ok {
		return CacheBudget{}, false
	}
	return CacheBudget{Size: size, AllBudget: all, Ephemeral: eph}, true
}

// parseBuildCacheSize reads the "Build Cache" row of `docker system df
// --format '{{json .}}'` (one JSON object per line).
func parseBuildCacheSize(out string) (int64, bool) {
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		var row struct {
			Type string `json:"Type"`
			Size string `json:"Size"`
		}
		if json.Unmarshal([]byte(sc.Text()), &row) != nil || row.Type != "Build Cache" {
			continue
		}
		return parseSize(row.Size)
	}
	return 0, false
}

// parseGCPolicy reads the "GC Policy rule#N:" blocks of `docker buildx
// inspect`: the all-records budget is the Max Used Space of the rule with
// "All: true"; the ephemeral budget is that of the rule whose Filters name
// exec.cachemount.
func parseGCPolicy(out string) (all, ephemeral int64, ok bool) {
	type rule struct {
		all     bool
		filters string
		maxUsed int64
	}
	var rules []*rule
	var cur *rule
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "GC Policy rule#"):
			cur = &rule{}
			rules = append(rules, cur)
		case cur == nil:
			continue
		case strings.HasPrefix(line, "All:"):
			cur.all = strings.TrimSpace(strings.TrimPrefix(line, "All:")) == "true"
		case strings.HasPrefix(line, "Filters:"):
			cur.filters = strings.TrimSpace(strings.TrimPrefix(line, "Filters:"))
		case strings.HasPrefix(line, "Max Used Space:"):
			if n, pok := parseSize(strings.TrimSpace(strings.TrimPrefix(line, "Max Used Space:"))); pok {
				cur.maxUsed = n
			}
		}
	}
	for _, r := range rules {
		if r.all && r.maxUsed > 0 {
			all = r.maxUsed
			ok = true
		}
		if strings.Contains(r.filters, "exec.cachemount") && r.maxUsed > 0 {
			ephemeral = r.maxUsed
		}
	}
	return all, ephemeral, ok
}

var sizeRe = regexp.MustCompile(`^([0-9]*\.?[0-9]+)\s*([A-Za-z]*)$`)

// parseSize accepts docker's two spellings: decimal (kB/MB/GB/TB, as `docker
// system df` prints) and binary (KiB/MiB/GiB/TiB, as `buildx inspect` prints).
func parseSize(s string) (int64, bool) {
	m := sizeRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	var mult float64
	switch strings.ToLower(m[2]) {
	case "", "b":
		mult = 1
	case "kb":
		mult = 1e3
	case "mb":
		mult = 1e6
	case "gb":
		mult = 1e9
	case "tb":
		mult = 1e12
	case "kib":
		mult = 1 << 10
	case "mib":
		mult = 1 << 20
	case "gib":
		mult = 1 << 30
	case "tib":
		mult = 1 << 40
	default:
		return 0, false
	}
	return int64(math.Round(n * mult)), true
}

func humanSize(n int64) string {
	switch {
	case n >= 1e12:
		return fmt.Sprintf("%.1f TB", float64(n)/1e12)
	case n >= 1e9:
		return fmt.Sprintf("%.0f GB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.0f MB", float64(n)/1e6)
	}
	return fmt.Sprintf("%d B", n)
}

// Slug normalizes a project basename into an image/container name fragment:
// lowercased, characters outside [a-z0-9._-] replaced with '-' (CS-LNCH-028).
func Slug(projectDir string) string {
	s := strings.ToLower(filepath.Base(projectDir))
	return slugUnsafe.ReplaceAllString(s, "-")
}

var slugUnsafe = regexp.MustCompile(`[^a-z0-9._-]`)

// h6 is the short digest used to disambiguate names that would otherwise
// collide. Six hex characters is ample: it only has to separate paths a single
// user has checked out, not resist collision attacks.
func h6(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])[:6]
}

// ProjectSlug identifies a project for container naming: the parent directory
// name, the project directory name, and a digest of the absolute path
// (CS-LNCH-028, CS-LNCH-031).
//
// The basename alone is not enough. A single workspace can hold dozens of
// directories sharing a name (~22 named "infrastructure" in the case that
// motivated this), and every one of them produced the same container name, so
// only one could run at a time. The parent segment makes the common case
// readable; the digest makes every case correct.
func ProjectSlug(projectDir string) string {
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		abs = projectDir
	}
	abs = filepath.Clean(abs)
	base := Slug(abs)
	parent := Slug(filepath.Dir(abs))
	// At the filesystem root, Dir() repeats the path (or yields separators that
	// normalize to a bare "-"); there is no meaningful parent to name.
	if d := filepath.Dir(abs); d == abs || parent == "" || strings.Trim(parent, "-.") == "" {
		return base + "-" + h6(abs)
	}
	return parent + "-" + base + "-" + h6(abs)
}

// ImageSlug identifies a child image by what it was built FROM, not by which
// project triggered the build (CS-IMG-015, CS-IMG-018, CS-IMG-019).
//
// Keying on the project made two mistakes at once: projects sharing a
// Dockerfile each rebuilt an identical image, and same-named projects with
// different Dockerfiles silently overwrote each other's tag — which the
// mtime-based staleness check cannot detect, since it never knew which
// Dockerfile produced the cached image.
//
// The context is part of the identity, not just the Dockerfile: the default
// branch builds with the project root as context while the dockerfileDir
// override builds with the override directory, so the same Dockerfile can
// legitimately produce different images.
func ImageSlug(dockerfile, context string) string {
	return "df-" + Slug(context) + "-" + h6(dockerfile, context)
}

// ImageID returns the docker ID of an image, or "" when it does not exist.
// It feeds the config fingerprint so an out-of-band rebuild registers as drift.
func ImageID(r execx.Runner, name string) string {
	out, err := r.Output(execx.Cmd{
		Name:   "docker",
		Args:   []string{"image", "inspect", "-f", "{{.Id}}", name},
		Stderr: io.Discard,
	})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func imageExists(r execx.Runner, name string) bool {
	err := r.Run(execx.Cmd{Name: "docker", Args: []string{"image", "inspect", name}, Stdout: io.Discard, Stderr: io.Discard})
	return err == nil
}

func imageCreated(r execx.Runner, name string) time.Time {
	out, err := r.Output(execx.Cmd{Name: "docker", Args: []string{"image", "inspect", "-f", "{{.Created}}", name}, Stderr: io.Discard})
	if err != nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(out))
	if err != nil {
		return time.Time{}
	}
	return t
}

func mtimeAfter(path string, t time.Time) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	return fi.ModTime().After(t)
}

func anyNewer(root string, rels []string, t time.Time) bool {
	for _, rel := range rels {
		p := filepath.Join(root, rel)
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		if !fi.IsDir() {
			if fi.ModTime().After(t) {
				return true
			}
			continue
		}
		newer := false
		filepath.WalkDir(p, func(path string, d os.DirEntry, err error) error {
			if err != nil || newer || d.IsDir() {
				return nil
			}
			if info, ierr := d.Info(); ierr == nil && info.ModTime().After(t) {
				newer = true
			}
			return nil
		})
		if newer {
			return true
		}
	}
	return false
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
