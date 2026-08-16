// Package imagebuild manages the two-layer image lifecycle: base image
// staleness + rebuild, the Claude Code update check, and child Dockerfile
// resolution + build. Spec: spec/image-build.feature (CS-IMG).
package imagebuild

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/paths"
	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
)

const BaseImageName = "claude-sandbox"

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
	err = o.Runner.Run(execx.Cmd{Name: "docker", Args: args, Stdout: o.Out, Stderr: o.Err})
	return true, err
}

var semverRe = regexp.MustCompile(`[0-9]+\.[0-9]+\.[0-9]+`)

// UpdateCheck compares the baked Claude Code version against the npm registry
// and offers a rebuild (CS-IMG-006..009). baseRebuilt skips the check.
// Returns whether the base was rebuilt here.
func UpdateCheck(o Options, baseRebuilt bool) bool {
	if o.NoUpdateCheck || baseRebuilt {
		return false
	}
	baked, _ := o.Runner.Output(execx.Cmd{
		Name:   "docker",
		Args:   []string{"run", "--rm", "--entrypoint", "cat", BaseImageName, "/opt/claude-sandbox/claude-version"},
		Stderr: io.Discard,
	})
	bakedV := semverRe.FindString(baked)
	latest, _ := o.Runner.Output(execx.Cmd{Name: "npm", Args: []string{"view", "@anthropic-ai/claude-code", "version"}, Stderr: io.Discard})
	latestV := strings.TrimSpace(latest)
	if bakedV == "" || latestV == "" || bakedV == latestV {
		return false
	}
	fmt.Fprintf(o.Out, "\nClaude Code update available: %s → %s\n", bakedV, latestV)
	accept := o.AutoUpdate
	if !accept {
		accept = o.Prompter.Confirm("", "Rebuild base image to update?", false, 5*time.Second)
	}
	if !accept {
		fmt.Fprintln(o.Out)
		return false
	}
	fmt.Fprintf(o.Out, "Rebuilding %s base image...\n", BaseImageName)
	err := o.Runner.Run(execx.Cmd{
		Name:   "docker",
		Args:   []string{"build", "--no-cache", "-t", BaseImageName, "--build-arg", "CLAUDE_SANDBOX_VERSION=" + o.Version, o.RepoRoot},
		Stdout: o.Out, Stderr: o.Err,
	})
	fmt.Fprintln(o.Out)
	return err == nil
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
// Returns the image to run.
func EnsureChild(o Options, spec ChildSpec, baseRebuilt bool, baseOnly bool) (string, error) {
	if !spec.Use {
		if !baseOnly {
			fmt.Fprintf(o.Err, "WARNING: No child Dockerfile found at %s\n", spec.Dockerfile)
			fmt.Fprintf(o.Err, "  Project-specific tools (Go, TypeScript LSP, etc.) will not be available.\n")
			fmt.Fprintf(o.Err, "  Create .claude-sandbox/Dockerfile to add them, or set baseOnly: true\n")
			fmt.Fprintf(o.Err, "  in your config to suppress this warning.\n\n")
		}
		return BaseImageName, nil
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
	if need {
		fmt.Fprintf(o.Out, "Building %s child image from %s (context: %s)...\n", spec.ImageName, spec.Dockerfile, spec.Context)
		err := o.Runner.Run(execx.Cmd{
			Name:   "docker",
			Args:   []string{"build", "-t", spec.ImageName, "-f", spec.Dockerfile, spec.Context},
			Stdout: o.Out, Stderr: o.Err,
		})
		if err != nil {
			return "", err
		}
	}
	return spec.ImageName, nil
}

// PrintVersion implements --version (CS-LNCH-030).
func PrintVersion(o Options) {
	fmt.Fprintf(o.Out, "claude-sandbox %s  (host: %s)\n", o.Version, o.RepoRoot)
	if !imageExists(o.Runner, BaseImageName) {
		fmt.Fprintln(o.Out, "  image:        (not built yet)")
		return
	}
	baked, _ := o.Runner.Output(execx.Cmd{
		Name:   "docker",
		Args:   []string{"image", "inspect", "-f", `{{ index .Config.Labels "org.opencontainers.image.revision" }}`, BaseImageName},
		Stderr: io.Discard,
	})
	baked = strings.TrimSpace(baked)
	created := imageCreated(o.Runner, BaseImageName)
	createdStr := "?"
	if !created.IsZero() {
		createdStr = created.Format("2006-01-02")
	}
	if baked == "" {
		baked = "unknown"
	}
	fmt.Fprintf(o.Out, "  image:        %s  (built %s)\n", baked, createdStr)
	if baked != "unknown" && baked != o.Version {
		fmt.Fprintln(o.Out, "  note: image differs from host scripts — it will auto-rebuild on next launch (or run --rebuild).")
	}
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
