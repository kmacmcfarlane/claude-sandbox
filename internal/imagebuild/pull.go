package imagebuild

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
)

// ExternalParents are the registry images each sandbox-owned Dockerfile
// builds FROM: every FROM that names neither an earlier stage nor a local
// sandbox image (CS-IMG-055). A test parses the three Dockerfiles so this
// cannot drift from them. Child and cap builds have no entry: their FROM and
// COPY --from name local-only images, which are never pulled.
var ExternalParents = map[string][]string{
	BaseDockerfile:  {"debian:bookworm-slim"},
	ToolsDockerfile: {"golang:1.25-bookworm", "node:22-bookworm-slim", "debian:bookworm-slim"},
	CLIDockerfile:   {"debian:bookworm-slim"},
}

// PullOutcome is what happened to one external parent this launch.
type PullOutcome string

const (
	// PullPulled: docker pull succeeded, so the local tag is upstream's.
	PullPulled PullOutcome = "pulled"
	// PullFailed: docker pull failed; builds use the local copy (CS-IMG-056).
	PullFailed PullOutcome = "failed"
	// PullSkipped: not attempted this launch (no from-scratch build needed it).
	PullSkipped PullOutcome = "skipped"
)

// Pulls records each external parent's outcome for one launch, keyed by
// image, so a parent shared by the base, tools and CLI images is pulled (and
// a failure warned about) once, and a later step can tell whether a build ran
// on a freshly pulled parent (CS-IMG-055/056; the refresh stamp of
// base-image-refresh F2 reads it).
type Pulls struct{ outcome map[string]PullOutcome }

// NewPulls returns an empty per-launch pull record.
func NewPulls() *Pulls { return &Pulls{outcome: map[string]PullOutcome{}} }

// Outcome reports what happened to image this launch; PullSkipped when it
// was never attempted (or p is nil).
func (p *Pulls) Outcome(image string) PullOutcome {
	if p == nil {
		return PullSkipped
	}
	if o, ok := p.outcome[image]; ok {
		return o
	}
	return PullSkipped
}

// Pulled reports whether every external parent of dockerfile was pulled
// successfully this launch.
func (p *Pulls) Pulled(dockerfile string) bool {
	parents := ExternalParents[dockerfile]
	for _, parent := range parents {
		if p.Outcome(parent) != PullPulled {
			return false
		}
	}
	return len(parents) > 0
}

// attempted reports whether parent already has an outcome this launch.
func (p *Pulls) attempted(parent string) bool {
	if p == nil {
		return false
	}
	_, ok := p.outcome[parent]
	return ok
}

func (p *Pulls) record(parent string, o PullOutcome) {
	if p != nil {
		p.outcome[parent] = o
	}
}

// pullParents runs "docker pull" on each external parent of dockerfile before
// a from-scratch build (a missing image or --rebuild), so the build — and
// every later one, pulled or not — resolves the current upstream image.
// BuildKit's own --pull is never used: on the classic image store it is per
// build and leaves the local tag alone, so the next build without it goes back
// to the old parent (CS-IMG-055). A failed pull warns one line and the build
// goes ahead on the local copy (CS-IMG-056). Headless launches pull too: this
// only ever precedes a from-scratch build, and their Out is stderr.
func pullParents(o Options, dockerfile string) {
	for _, parent := range ExternalParents[dockerfile] {
		if o.Pulls.attempted(parent) {
			continue
		}
		fmt.Fprintf(o.Out, "Pulling %s...\n", parent)
		var stderr bytes.Buffer
		err := o.Runner.Run(execx.Cmd{Name: "docker", Args: []string{"pull", "-q", parent}, Stdout: io.Discard, Stderr: &stderr})
		if err != nil {
			o.Pulls.record(parent, PullFailed)
			fmt.Fprintf(o.Err, "WARNING: could not pull %s (%s); building on the local copy.\n", parent, pullError(stderr.String(), err))
			continue
		}
		o.Pulls.record(parent, PullPulled)
	}
}

// pullError is the one-line reason a pull failed: docker's last stderr line,
// else the process error.
func pullError(stderr string, err error) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		return last
	}
	return err.Error()
}
