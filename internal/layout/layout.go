// Package layout manages the .claude-sandbox/ layout lifecycle: directory
// skeleton, seeded CLAUDE.md, host .gitignore entries, and the sidecar git
// repo. Spec: spec/layout.feature (CS-LAY).
package layout

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/paths"
	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
)

const claudeMDSeed = `# .claude-sandbox/

claude-sandbox's per-project "foreign" files, consolidated out of the host tree.

- ` + "`config.yaml`" + ` — sandbox config
- ` + "`Dockerfile`" + ` — child image
- ` + "`env`" + ` — environment variables, secret, never committed
- ` + "`ralph/`" + ` — ralph loop runtime + logs
- ` + "`agent/`" + ` — workflow docs + backlog
- ` + "`temp/`" + ` — scratch (uncommittable)
- ` + "`reports/`" + ` — durable outputs (bench, parity diffs, QA logs)

## Committing changes here

When ` + "`trackInHost`" + ` is false (default), this directory is gitignored in the host
repo and keeps its OWN sidecar git repo for history. After grooming the backlog
or changing the agent flow, PROMPT the user to commit in the sidecar — do not
auto-commit:

    git -C .claude-sandbox add -A && git -C .claude-sandbox commit -m "..."
`

// GitignoreAnswer captures how the gitignore prompt should be resolved:
// nil = ask (or fall back to CS_GITIGNORE_ASSUME / no-tty skip).
type Options struct {
	Runner   execx.Runner
	Prompter prompt.Prompter
	Out      io.Writer // progress messages (stdout)
	Err      io.Writer // notes/warnings (stderr)
	// Gitignore forces the host-gitignore prompt outcome: nil = prompt.
	Gitignore *bool
}

func (o *Options) out() io.Writer {
	if o.Out != nil {
		return o.Out
	}
	return os.Stdout
}

func (o *Options) errw() io.Writer {
	if o.Err != nil {
		return o.Err
	}
	return os.Stderr
}

// Setup ensures the .claude-sandbox/ skeleton, seeded CLAUDE.md, host
// .gitignore, and (when trackInHost is false) the sidecar git repo.
func Setup(project string, trackInHost bool, opts Options) error {
	sb := paths.SandboxDir(project)
	// The skeleton is temp/ and reports/ only. investigations/ is a claude-kit
	// investigate/implement convention, not a sandbox path: never created here,
	// and an existing one is user data that is left alone.
	for _, d := range []string{filepath.Join(sb, "temp"), filepath.Join(sb, "reports")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	hostIsGit := isGitWorkTree(opts.Runner, project)

	// CS-LAY-002: seed once, never overwrite.
	claudeMD := filepath.Join(sb, "CLAUDE.md")
	if _, err := os.Stat(claudeMD); os.IsNotExist(err) {
		if err := os.WriteFile(claudeMD, []byte(claudeMDSeed), 0o644); err != nil {
			return err
		}
	}

	hostGI := filepath.Join(project, ".gitignore")

	if trackInHost {
		// CS-LAY-009: host-tracked — ignore only ephemeral content. The
		// negations defensively re-include config/Dockerfile against broad
		// host ignore rules; no-ops otherwise.
		if hostIsGit {
			// CS-LAY-018: over a whole-dir ignore or a sidecar repo these
			// lines are dead (git cannot re-include inside an ignored dir) and
			// only leave the tree dirty. Warn, never switch modes, and still
			// propose the worktrees line alone.
			if conflict := hostTrackConflict(opts.Runner, project, sb); conflict != "" {
				fmt.Fprintf(opts.errw(), "WARNING: trackInHost is true but %s; skipping the host-tracked .gitignore entries, which would be dead there.\n", conflict)
				fmt.Fprintln(opts.errw(), "  Either set trackInHost: false in .claude-sandbox/config.yaml (and delete any .claude-sandbox/env, temp/, ralph/, !config.yaml or !Dockerfile lines already in .gitignore — they are dead),")
				fmt.Fprintln(opts.errw(), "  or drop the ignore rule (`git check-ignore -v .claude-sandbox` names it) and .claude-sandbox/.git to track the directory in the host.")
				gitignoreAdd(hostGI, opts, withWorktreesLine(hostGI)...)
				return nil
			}
			gitignoreAdd(hostGI, opts, withWorktreesLine(hostGI,
				".claude-sandbox/env", ".claude-sandbox/temp/", ".claude-sandbox/ralph/",
				"!.claude-sandbox/config.yaml", "!.claude-sandbox/Dockerfile")...)
		}
		return nil
	}

	// Foreign-safe: whole dir ignored in host; sidecar repo holds history.
	if hostIsGit {
		gitignoreAdd(hostGI, opts, withWorktreesLine(hostGI, "/.claude-sandbox/")...)
	}
	// CS-LAY-004: sidecar's own .gitignore — append-only, no prompt.
	if err := ensureLines(filepath.Join(sb, ".gitignore"), "temp/", "env", "ralph/"); err != nil {
		return err
	}
	// CS-LAY-005..008: sidecar git init.
	if _, err := os.Stat(filepath.Join(sb, ".git")); err == nil {
		return nil
	}
	if !hostIsGit || gitIgnores(opts.Runner, project, sb) {
		if err := opts.Runner.Run(execx.Cmd{Name: "git", Args: []string{"-C", sb, "init", "-q"}}); err == nil {
			fmt.Fprintf(opts.out(), "Initialized sidecar git repo at %s\n", sb)
		}
	} else {
		fmt.Fprintf(opts.errw(), "Note: %s is not gitignored by the host repo; skipping sidecar git init.\n", sb)
		fmt.Fprintf(opts.errw(), "  Add /.claude-sandbox/ to .gitignore to enable sidecar history.\n")
	}
	return nil
}

// worktreesLine ignores Claude Code's harness-native worktrees
// (.claude/worktrees/<name>, branch worktree-<name>) — CS-LAY-017. Written in
// both trackInHost modes, in the same prompt as the .claude-sandbox/ entries.
const worktreesLine = ".claude/worktrees/"

// worktreesCoveringRules are existing .gitignore lines that already ignore
// .claude/worktrees/; when one is present the line is neither proposed nor
// added. Matched exactly after trimming whitespace.
var worktreesCoveringRules = []string{
	worktreesLine, "/" + worktreesLine, ".claude/worktrees", "/.claude/worktrees",
	".claude/", "/.claude/", ".claude", "/.claude",
	".claude/*", "/.claude/*", ".claude/**", "/.claude/**",
}

// withWorktreesLine appends worktreesLine to lines unless the host .gitignore
// already carries a covering rule.
func withWorktreesLine(gi string, lines ...string) []string {
	raw, _ := os.ReadFile(gi)
	for _, l := range strings.Split(string(raw), "\n") {
		l = strings.TrimSpace(l)
		for _, rule := range worktreesCoveringRules {
			if l == rule {
				return lines
			}
		}
	}
	return append(lines, worktreesLine)
}

func isGitWorkTree(r execx.Runner, dir string) bool {
	err := r.Run(execx.Cmd{Name: "git", Args: []string{"-C", dir, "rev-parse", "--is-inside-work-tree"}, Stdout: io.Discard, Stderr: io.Discard})
	return err == nil
}

// hostTrackConflict reports why host-tracked (trackInHost: true) .gitignore
// entries would be dead — CS-LAY-018: the host repo already ignores the whole
// .claude-sandbox/ directory, a sidecar .git exists inside it, or both.
// Returns "" when neither condition holds.
func hostTrackConflict(r execx.Runner, project, sb string) string {
	ignored := gitIgnores(r, project, sb)
	_, err := os.Stat(filepath.Join(sb, ".git"))
	sidecar := err == nil
	switch {
	case ignored && sidecar:
		return "the host repo already ignores .claude-sandbox/ and .claude-sandbox/.git exists"
	case ignored:
		return "the host repo already ignores .claude-sandbox/"
	case sidecar:
		return ".claude-sandbox/.git exists"
	}
	return ""
}

func gitIgnores(r execx.Runner, project, path string) bool {
	err := r.Run(execx.Cmd{Name: "git", Args: []string{"-C", project, "check-ignore", "-q", path}, Stdout: io.Discard, Stderr: io.Discard})
	return err == nil
}

// gitignoreAdd appends missing lines to a .gitignore-style file, prompting
// first (CS-LAY-010..014). Resolution order: Options.Gitignore flag,
// CS_GITIGNORE_ASSUME env var, interactive prompt (default yes), no-tty skip.
// Returns true when the lines were added.
func gitignoreAdd(gi string, opts Options, lines ...string) bool {
	existing := map[string]bool{}
	if raw, err := os.ReadFile(gi); err == nil {
		for _, l := range strings.Split(string(raw), "\n") {
			existing[l] = true
		}
	}
	var missing []string
	for _, l := range lines {
		if !existing[l] {
			missing = append(missing, l)
		}
	}
	if len(missing) == 0 {
		return true
	}

	fmt.Fprintf(opts.errw(), "These entries are missing from %s:\n", gi)
	for _, l := range missing {
		fmt.Fprintf(opts.errw(), "  %s\n", l)
	}

	add := false
	switch {
	case opts.Gitignore != nil:
		add = *opts.Gitignore
	case os.Getenv("CS_GITIGNORE_ASSUME") != "":
		add = prompt.Parse(os.Getenv("CS_GITIGNORE_ASSUME"), false)
	case opts.Prompter != nil && opts.Prompter.Interactive():
		add = opts.Prompter.Confirm("", "Add them?", true, 30*time.Second)
	default:
		fmt.Fprintln(opts.errw(), "(no tty; skipping .gitignore update)")
		return false
	}
	if !add {
		fmt.Fprintln(opts.errw(), "Skipped .gitignore update.")
		return false
	}
	if err := ensureLines(gi, missing...); err != nil {
		fmt.Fprintf(opts.errw(), "failed to update %s: %v\n", gi, err)
		return false
	}
	fmt.Fprintf(opts.errw(), "Updated %s\n", gi)
	return true
}

// ensureLines appends each line not already present verbatim, keeping the
// file newline-terminated (CS-LAY-011).
func ensureLines(file string, lines ...string) error {
	raw, _ := os.ReadFile(file)
	content := string(raw)
	existing := map[string]bool{}
	for _, l := range strings.Split(content, "\n") {
		existing[l] = true
	}
	var b strings.Builder
	b.WriteString(content)
	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		b.WriteString("\n")
	}
	changed := false
	for _, l := range lines {
		if !existing[l] {
			b.WriteString(l + "\n")
			changed = true
		}
	}
	if !changed && len(raw) > 0 {
		return nil
	}
	return os.WriteFile(file, []byte(b.String()), 0o644)
}
