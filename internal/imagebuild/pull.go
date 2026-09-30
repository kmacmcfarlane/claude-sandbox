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

// Pulls records which external parents one launch has already handled, so a
// parent shared by the base, tools and CLI images is pulled (and a failure
// warned about) once (CS-IMG-055/056).
type Pulls struct{ seen map[string]bool }

// NewPulls returns an empty per-launch pull record.
func NewPulls() *Pulls { return &Pulls{seen: map[string]bool{}} }

// claim reports whether parent is still to be handled, marking it handled.
func (p *Pulls) claim(parent string) bool {
	if p == nil {
		return true
	}
	if p.seen[parent] {
		return false
	}
	p.seen[parent] = true
	return true
}

// pullParents runs "docker pull" on each external parent of dockerfile before
// a from-scratch build (a missing image or --rebuild), so the build — and
// every later one, pulled or not — resolves the current upstream image.
// BuildKit's own --pull is never used: on the classic image store it is per
// build and leaves the local tag alone, so the next build without it goes back
// to the old parent (CS-IMG-055). A failed pull warns one line and the build
// goes ahead on the local copy (CS-IMG-056). A headless launch never pulls.
func pullParents(o Options, dockerfile string) {
	var todo []string
	for _, parent := range ExternalParents[dockerfile] {
		if o.Pulls.claim(parent) {
			todo = append(todo, parent)
		}
	}
	if len(todo) == 0 {
		return
	}
	if o.Headless {
		fmt.Fprintf(o.Err, "Note: headless launch — not pulling %s; building on the local copy.\n", strings.Join(todo, ", "))
		return
	}
	for _, parent := range todo {
		fmt.Fprintf(o.Out, "Pulling %s...\n", parent)
		var stderr bytes.Buffer
		err := o.Runner.Run(execx.Cmd{Name: "docker", Args: []string{"pull", "-q", parent}, Stdout: io.Discard, Stderr: &stderr})
		if err != nil {
			fmt.Fprintf(o.Err, "WARNING: could not pull %s (%s); building on the local copy.\n", parent, pullError(stderr.String(), err))
		}
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
