// Package initcmd implements the `init` / `init-ralph` bootstrap subcommands.
// Idempotent: existing files are never overwritten. The seeded config is
// sparse (fully commented) so it overrides nothing in the cascade, and init
// never creates a real env — it seeds env.example, which the launcher never
// reads (CS-INIT-004, CS-INIT-030).
// Spec: spec/init.feature (CS-INIT), spec/init-ralph.feature (CS-INITR).
package initcmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/layout"
	"github.com/kmacmcfarlane/claude-sandbox/internal/paths"
	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
	"github.com/kmacmcfarlane/claude-sandbox/internal/scaffold"
)

// Flags are the init/init-ralph options. Pointer fields are tri-state:
// nil = not passed (prompt or default applies).
type Flags struct {
	Ralph                bool  // init-ralph: also seed the ralph scaffold
	TrackInHost          *bool // --track-in-host / --no-track-in-host
	Gitignore            *bool // --gitignore / --no-gitignore (override; default true, CS-INIT-028)
	CopyParentDockerfile *bool // --copy-parent-dockerfile / --no-copy-parent-dockerfile (override; default copy when found)
	Yes                  bool  // --yes: accept the trackInHost prompt's default
}

// Deps are the injected environment.
type Deps struct {
	Runner   execx.Runner
	Prompter prompt.Prompter
	Out      io.Writer
	Err      io.Writer
}

const promptTimeout = 60 * time.Second

// Run bootstraps .claude-sandbox/ in project and returns without launching.
func Run(project string, f Flags, d Deps) error {
	if f.Yes {
		// --yes: the one prompt init has (trackInHost) resolves to its
		// default, non-interactively (CS-INIT-025).
		d.Prompter = &prompt.Fixed{Out: d.Err}
	}
	sb := paths.SandboxDir(project)
	cfgPath := filepath.Join(sb, "config.yaml")
	envExamplePath := filepath.Join(sb, paths.EnvExampleName)
	if err := os.MkdirAll(sb, 0o755); err != nil {
		return err
	}

	fmt.Fprintf(d.Out, "Initializing .claude-sandbox/ in %s\n", project)
	// CS-INIT-019: make inheritance visible up front.
	cascade.PrintReport(d.Out, project)

	// Upstream trackInHost (ancestors only — exclude the project's own file).
	upstreamConfigs, err := paths.CollectUp(filepath.Dir(project), paths.Config)
	if err != nil {
		return err
	}
	upstreamVal, upstreamSet := cascade.TrackInHostExplicit(upstreamConfigs)
	upstreamSrc := cascade.TrackInHostSource(upstreamConfigs)

	// Resolve the local trackInHost decision: flag > prompt. When an ancestor
	// defines it, the prompt shows the inherited value as the default and
	// Enter inherits (writes nothing locally) — CS-INIT-014..017.
	cfgExists := fileExists(cfgPath)
	var track *bool // value to write locally (nil = inherit, write nothing)
	switch {
	case f.TrackInHost != nil:
		track = f.TrackInHost
	case cfgExists:
		// CS-INIT-013: existing config, no flag — leave it alone entirely.
	case upstreamSet:
		pre := fmt.Sprintf("Parent config sets trackInHost: %v (inherited from %s)", upstreamVal, upstreamSrc)
		ans := d.Prompter.Ask(pre, fmt.Sprintf("Track in host repo? [Enter=inherit %v, y/n=override]", upstreamVal), promptTimeout)
		if ans != "" {
			v := prompt.Parse(ans, upstreamVal)
			track = &v
		}
	default:
		v := promptTrackInHost(d.Prompter)
		track = &v
	}

	// --- config (sparse seed) ---
	switch {
	case cfgExists && f.TrackInHost != nil:
		// Explicit flag on an existing config: keep the file consistent with
		// the layout we're about to (re)build (CS-INIT-012).
		if err := SetTrackInHost(cfgPath, *f.TrackInHost); err != nil {
			return err
		}
		fmt.Fprintf(d.Out, "  updated  config.yaml (trackInHost: %v)\n", *f.TrackInHost)
	case cfgExists:
		fmt.Fprintln(d.Out, "  skipped  config.yaml (exists)")
	default:
		seed, err := scaffold.ReadBase("config.yaml")
		if err != nil {
			return err
		}
		if _, err := scaffold.SeedFile(cfgPath, seed); err != nil {
			return err
		}
		if track != nil {
			if err := SetTrackInHost(cfgPath, *track); err != nil {
				return err
			}
			fmt.Fprintf(d.Out, "  created  config.yaml (sparse; trackInHost: %v)\n", *track)
		} else {
			// CS-INIT-016: the commented hint must reflect the inherited
			// value and name its source.
			if err := setTrackInHostHint(cfgPath, upstreamVal, upstreamSrc); err != nil {
				return err
			}
			fmt.Fprintf(d.Out, "  created  config.yaml (sparse; trackInHost inherited: %v)\n", upstreamVal)
		}
	}

	// --- env.example (template only; a real env is never created, so
	// nothing project-level shadows an upstream env — CS-INIT-004/030). An
	// existing real env is left alone: not read, moved or reported here.
	if fileExists(envExamplePath) {
		fmt.Fprintf(d.Out, "  skipped  %s (exists)\n", paths.EnvExampleName)
	} else {
		seed, err := scaffold.ReadBase(paths.EnvExampleName)
		if err != nil {
			return err
		}
		if _, err := scaffold.SeedFile(envExamplePath, seed); err != nil {
			return err
		}
		fmt.Fprintf(d.Out, "  created  %s (template; copy to env only for a project-only override)\n", paths.EnvExampleName)
	}
	// CS-INIT-020: inherited env files are reported, never copied.
	if upstreamEnvs, _ := paths.CollectUp(filepath.Dir(project), paths.Env); len(upstreamEnvs) > 0 {
		for _, e := range upstreamEnvs {
			fmt.Fprintf(d.Out, "  note: %s is inherited by this project (a project env would override its keys)\n", e)
		}
	}

	// --- Dockerfile.example (optional; inactive until renamed) ---
	if err := seedDockerfileExample(project, sb, f, d); err != nil {
		return err
	}

	// Layout: effective trackInHost = CLI flag first, else most-local
	// definition across the full cascade (local file included).
	var effective bool
	if f.TrackInHost != nil {
		effective = *f.TrackInHost
	} else {
		all, err := paths.CollectUp(project, paths.Config)
		if err != nil {
			return err
		}
		effective = cascade.TrackInHost(all)
	}
	// The trackInHost answer already chose the shape of the host .gitignore,
	// so init writes the entries without a further prompt (CS-INIT-028):
	// --gitignore/--no-gitignore override, else CS_GITIGNORE_ASSUME when set
	// (left to layout, CS-LAY-014), else yes. Passing a non-nil Gitignore is
	// what keeps layout.Setup from prompting; the launch path still passes nil.
	gi := f.Gitignore
	if gi == nil && os.Getenv("CS_GITIGNORE_ASSUME") == "" {
		t := true
		gi = &t
	}
	if err := layout.Setup(project, effective, layout.Options{
		Runner: d.Runner, Prompter: d.Prompter, Out: d.Out, Err: d.Err, Gitignore: gi,
	}); err != nil {
		return err
	}

	if f.Ralph {
		if _, _, err := scaffold.SeedRalph(sb, filepath.Base(project), d.Out); err != nil {
			return err
		}
	}

	printNextSteps(d.Out, f.Ralph)
	return nil
}

func promptTrackInHost(p prompt.Prompter) bool {
	pre := "How should .claude-sandbox/ be version-controlled?\n" +
		"  [y] Track in THIS repo   — own project; env/temp/ralph gitignored, rest committed\n" +
		"  [N] Keep out of the repo — gitignore /.claude-sandbox/ + internal sidecar repo (default)"
	return p.Confirm(pre, "Track in host repo?", false, promptTimeout)
}

// seedDockerfileExample seeds Dockerfile.example without prompting: a copy of
// the nearest parent .claude-sandbox/Dockerfile when one exists, the generic
// scaffold example otherwise; --copy-parent-dockerfile /
// --no-copy-parent-dockerfile pin the choice (CS-INIT-021..024).
func seedDockerfileExample(project, sb string, f Flags, d Deps) error {
	if fileExists(filepath.Join(sb, "Dockerfile")) || fileExists(filepath.Join(sb, "Dockerfile.example")) {
		fmt.Fprintln(d.Out, "  skipped  Dockerfile.example (exists)")
		return nil
	}
	parentDockerfile, err := paths.FindUp(filepath.Dir(project), paths.Dockerfile)
	if err != nil {
		return err
	}
	copyParent := parentDockerfile != ""
	if copyParent && f.CopyParentDockerfile != nil {
		copyParent = *f.CopyParentDockerfile
	}
	var seed []byte
	source := "rename to Dockerfile to activate"
	if copyParent {
		seed, err = os.ReadFile(parentDockerfile)
		if err != nil {
			return err
		}
		source = fmt.Sprintf("copied from %s; rename to Dockerfile to activate", parentDockerfile)
	} else {
		seed, err = scaffold.ReadBase("Dockerfile.example")
		if err != nil {
			return err
		}
	}
	if _, err := scaffold.SeedFile(filepath.Join(sb, "Dockerfile.example"), seed); err != nil {
		return err
	}
	fmt.Fprintf(d.Out, "  created  Dockerfile.example (%s)\n", source)
	return nil
}

func printNextSteps(w io.Writer, ralph bool) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Done. Next steps:")
	fmt.Fprintln(w, "  1. Put secrets / env vars (e.g. DISCORD_WEBHOOK_URL, API keys) in an upstream .claude-sandbox/env")
	fmt.Fprintln(w, "     (a parent/workspace dir, shared by every project below it), or copy")
	fmt.Fprintln(w, "     .claude-sandbox/env.example → .claude-sandbox/env for a project-only override")
	fmt.Fprintln(w, "  2. Review .claude-sandbox/config.yaml — host access, mounts, model, memoryLimit")
	fmt.Fprintln(w, "  3. (optional) Rename .claude-sandbox/Dockerfile.example → Dockerfile and add project tools (FROM claude-sandbox)")
	if ralph {
		fmt.Fprintln(w, "  4. Fill .claude-sandbox/agent/PRD.md and DEVELOPMENT_PRACTICES.md / TEST_PRACTICES.md")
		fmt.Fprintln(w, "  5. Groom the backlog: python3 .claude-sandbox/scripts/backlog/backlog.py add")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Run the loop:  claude-sandbox --ralph")
		fmt.Fprintln(w, "Stop it:       touch .claude-sandbox/ralph/stop")
	} else {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Launch:  claude-sandbox")
	}
}

var trackLineRe = regexp.MustCompile(`(?m)^[ \t]*#?[ \t]*trackInHost:.*$`)

// SetTrackInHost forces the config's trackInHost to val (uncommented),
// replacing any existing commented or uncommented line, or appending when
// absent (CS-INIT-012).
func SetTrackInHost(cfgPath string, val bool) error {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	line := fmt.Sprintf("trackInHost: %v", val)
	content := string(raw)
	if trackLineRe.MatchString(content) {
		content = trackLineRe.ReplaceAllString(content, line)
	} else {
		content += fmt.Sprintf("\n%s\n", line)
	}
	return os.WriteFile(cfgPath, []byte(content), 0o644)
}

// setTrackInHostHint rewrites the commented hint line so it reflects the
// inherited effective value and its source (CS-INIT-016).
func setTrackInHostHint(cfgPath string, val bool, source string) error {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	line := fmt.Sprintf("# trackInHost: %v   # inherited from %s", val, source)
	content := string(raw)
	if trackLineRe.MatchString(content) {
		content = trackLineRe.ReplaceAllString(content, line)
	} else {
		content += fmt.Sprintf("\n%s\n", line)
	}
	return os.WriteFile(cfgPath, []byte(content), 0o644)
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
