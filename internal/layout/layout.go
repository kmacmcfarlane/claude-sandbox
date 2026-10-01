// Package layout manages the .claude-sandbox/ layout lifecycle: directory
// skeleton, seeded CLAUDE.md, host .gitignore entries, and the sidecar git
// repo. Spec: spec/layout.feature (CS-LAY).
package layout

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
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
	// CS-LAY-020: files the host already tracks under .claude-sandbox/ while
	// trackInHost is false. Probed only in that mode; a failed probe is 0.
	hostTracked := 0
	if !trackInHost && hostIsGit {
		hostTracked = HostTrackedCount(opts.Runner, project)
	}

	// CS-LAY-002: seed once, never overwrite. Not in the CS-LAY-020 conflict
	// state: the seed describes a host-ignored directory, which it is not,
	// and would only add a wrong, untracked host file.
	// CS-LAY-023: created O_EXCL|O_NOFOLLOW, so whatever appears at the path
	// after the Stat (a FIFO a session planted) is left alone, never opened.
	claudeMD := filepath.Join(sb, "CLAUDE.md")
	if _, err := os.Lstat(claudeMD); os.IsNotExist(err) && hostTracked == 0 {
		if err := seedFile(claudeMD, []byte(claudeMDSeed)); err != nil {
			return err
		}
	}

	hostGI := filepath.Join(project, ".gitignore")
	sideGI := filepath.Join(sb, ".gitignore")

	// CS-LAY-023: the sidecar .gitignore is written in this mode (CS-LAY-004);
	// one that exists but is not a readable regular file fails the setup
	// here, before any git probe could open it (git check-ignore reads it,
	// blocking, for a probe path under .claude-sandbox/).
	if !trackInHost {
		if _, err := cascade.ReadRegularFile(sideGI); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	// CS-LAY-023: every git check-ignore probe below opens the host
	// .gitignore and, for a probe path under .claude-sandbox/, the sidecar
	// one, with a blocking open. When either exists but is not a readable
	// regular file, no probe runs and the host .gitignore is not touched: one
	// warning, and the steps that need a probe's answer are skipped. Other
	// files git reads (.git/info/exclude, core.excludesFile, the .git file)
	// are the bounded-git-calls follow-up, not this check.
	if hostIsGit {
		if p, err := unreadableIgnoreFile(hostGI, sideGI); p != "" {
			fmt.Fprintf(opts.errw(), "WARNING: cannot read %s (%v); skipping the .gitignore update and the git ignore checks, which would read it.\n", p, unwrapPathErr(err))
			if !trackInHost && hostTracked > 0 {
				warnHostTracked(opts.errw(), hostTracked, false, dirExists(filepath.Join(sb, ".git")))
			}
			if !trackInHost {
				// CS-LAY-004, as on the ordinary path.
				if err := ensureLines(sideGI, "temp/", "env", "ralph/"); err != nil {
					return err
				}
			}
			// The sidecar init (CS-LAY-005/006) needs dirIgnored's answer.
			return nil
		}
	}

	if trackInHost {
		// CS-LAY-009: host-tracked — ignore only ephemeral content. The
		// negations defensively re-include config/Dockerfile against broad
		// host ignore rules; no-ops otherwise.
		if hostIsGit {
			// CS-LAY-018: over a rule excluding the directory itself or a
			// sidecar repo these lines are dead (git cannot re-include inside
			// an excluded dir) and only leave the tree dirty. A rule excluding
			// only the children (".claude-sandbox/*") is not a conflict: the
			// negations work beneath it (CS-LAY-021). Warn, never switch modes, and still
			// propose the worktrees line alone.
			if conflict := hostTrackConflict(opts.Runner, project, sb); conflict != "" {
				fmt.Fprintf(opts.errw(), "WARNING: trackInHost is true but %s; skipping the host-tracked .gitignore entries, which would be dead there.\n", conflict)
				fmt.Fprintln(opts.errw(), "  Either set trackInHost: false in .claude-sandbox/config.yaml (and delete any .claude-sandbox/env, temp/, ralph/, !config.yaml or !Dockerfile lines already in .gitignore — they are dead),")
				fmt.Fprintln(opts.errw(), "  or drop the ignore rule (`git check-ignore -v --no-index "+dirProbe+"` names it) and .claude-sandbox/.git to track the directory in the host.")
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
	// CS-LAY-020: over files the host already tracks, the whole-dir ignore
	// would silently drop every new file from `git add`. Warn, never switch
	// modes, still propose the worktrees line, and skip the sidecar init.
	if hostIsGit {
		if hostTracked > 0 {
			warnHostTracked(opts.errw(), hostTracked, dirIgnored(opts.Runner, project), dirExists(filepath.Join(sb, ".git")))
			gitignoreAdd(hostGI, opts, withWorktreesLine(hostGI)...)
		} else {
			gitignoreAdd(hostGI, opts, withWorktreesLine(hostGI, "/.claude-sandbox/")...)
		}
	}
	// CS-LAY-004: sidecar's own .gitignore — append-only, no prompt.
	if err := ensureLines(filepath.Join(sb, ".gitignore"), "temp/", "env", "ralph/"); err != nil {
		return err
	}
	// CS-LAY-005..008: sidecar git init.
	if _, err := os.Stat(filepath.Join(sb, ".git")); err == nil {
		return nil
	}
	if hostTracked > 0 {
		// CS-LAY-020: a nested repo over host-tracked files would split their
		// history; the warning above already names the remedies.
		return nil
	}
	if !hostIsGit || dirIgnored(opts.Runner, project) {
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

// unreadableIgnoreFile returns the first of paths that exists but is not a
// readable regular file, and why; "" when every one is regular or absent.
// Read through cascade.ReadRegularFile, so the check itself never blocks.
func unreadableIgnoreFile(paths ...string) (string, error) {
	for _, p := range paths {
		if _, err := cascade.ReadRegularFile(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return p, err
		}
	}
	return "", nil
}

// withWorktreesLine appends worktreesLine to lines unless the host .gitignore
// already carries a covering rule.
func withWorktreesLine(gi string, lines ...string) []string {
	// CS-LAY-023: non-blocking, regular files only; anything else reads as no
	// covering rule (gitignoreAdd then warns about it).
	raw, _ := cascade.ReadRegularFile(gi)
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
// entries would be dead — CS-LAY-018: the host repo excludes the
// .claude-sandbox directory itself, a sidecar .git exists inside it, or both.
// Returns "" when neither condition holds.
func hostTrackConflict(r execx.Runner, project, sb string) string {
	ignored := dirExcluded(r, project)
	sidecar := dirExists(filepath.Join(sb, ".git"))
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

// HostTrackedCount returns how many files the host repo tracks under
// .claude-sandbox/ (CS-LAY-020). A failed probe returns 0 (including a
// project outside any git work tree), so behaviour is exactly as before: a
// probe failure never counts as "tracked". init shares it to default its
// greenfield trackInHost prompt to true in that state (CS-INIT-031).
func HostTrackedCount(r execx.Runner, project string) int {
	out, err := r.Output(execx.Cmd{Name: "git", Args: []string{"-C", project, "ls-files", "-z", "--", ".claude-sandbox"}, Stderr: io.Discard})
	if err != nil {
		return 0
	}
	n := 0
	for _, f := range strings.Split(out, "\x00") {
		if f != "" {
			n++
		}
	}
	return n
}

// dirProbe is the directory path asked by dirExcluded — no trailing slash:
// with one, git matches it against "dir/*" rules as if it were a child.
const dirProbe = ".claude-sandbox"

// dirExcluded reports whether the host repo excludes the .claude-sandbox
// directory ITSELF — the one state in which no "!" rule can re-include
// anything beneath it (CS-LAY-018, CS-LAY-021). --no-index is required: git
// otherwise reports a directory holding tracked files as NOT ignored even
// beneath a "/.claude-sandbox/" rule. A rule excluding only the children
// (".claude-sandbox/*", "/.claude-sandbox/**") does not match the directory.
func dirExcluded(r execx.Runner, project string) bool {
	err := r.Run(execx.Cmd{Name: "git", Args: []string{"-C", project, "check-ignore", "-q", "--no-index", "--", dirProbe}, Stdout: io.Discard, Stderr: io.Discard})
	return err == nil
}

// ignoreProbes are two unlike paths under .claude-sandbox/ that never exist
// (CS-LAY-022). Whether new files there would be hidden is asked of children,
// never of the directory: git reports a directory holding tracked files as
// NOT ignored even beneath a "/.claude-sandbox/" rule (CS-LAY-020). Two
// shapes, because one name is fooled by rules aimed at some names only: a
// whitelist ("*", "!*/", "!*.*") hides the dot-less name alone, "*.md" the
// other alone. ignoreProbe, the first, is the one the warnings name.
// Known limitation: a rule negating a probe path by name defeats this.
const ignoreProbe = ".claude-sandbox/ignore-probe"

var ignoreProbes = []string{ignoreProbe, ignoreProbe + ".md"}

// DirIgnored is dirIgnored for callers outside layout: init's greenfield
// trackInHost prompt defaults to true only when new files under
// .claude-sandbox/ are NOT hidden (CS-INIT-031). dirExcluded implies it, so
// it also covers the whole-dir half of CS-LAY-018.
func DirIgnored(r execx.Runner, project string) bool {
	return dirIgnored(r, project)
}

// dirIgnored reports whether the host repo ignores new files under
// .claude-sandbox/: true only when every ignoreProbes path is ignored.
func dirIgnored(r execx.Runner, project string) bool {
	for _, p := range ignoreProbes {
		err := r.Run(execx.Cmd{Name: "git", Args: []string{"-C", project, "check-ignore", "-q", "--", p}, Stdout: io.Discard, Stderr: io.Discard})
		if err != nil {
			return false
		}
	}
	return true
}

func dirExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// warnHostTracked prints the one CS-LAY-020 warning: trackInHost is false
// but the host tracks n files under .claude-sandbox/. When a rule already
// ignores the directory, new files are being hidden now and the rule must go
// whichever remedy is chosen; an existing sidecar .git gets one more clause.
func warnHostTracked(w io.Writer, n int, ruleExists, sidecar bool) {
	noun := "files"
	if n == 1 {
		noun = "file"
	}
	if ruleExists {
		fmt.Fprintf(w, "WARNING: trackInHost is false and the host repo tracks %d %s under .claude-sandbox/, but a host ignore rule already covers the directory: new files there are being hidden from git NOW.\n", n, noun)
		fmt.Fprintf(w, "  Remove that rule whichever remedy you choose (`git check-ignore -v %s` names it). Then either:\n", ignoreProbe)
	} else {
		fmt.Fprintf(w, "WARNING: trackInHost is false but the host repo already tracks %d %s under .claude-sandbox/; skipping the /.claude-sandbox/ .gitignore entry, which would silently hide new files there.\n", n, noun)
		fmt.Fprintln(w, "  Either:")
	}
	fmt.Fprint(w, "  - set trackInHost: true in .claude-sandbox/config.yaml to keep tracking the directory in the host")
	if sidecar {
		fmt.Fprint(w, " (and remove .claude-sandbox/.git, which CS-LAY-018 refuses in that mode)")
	}
	fmt.Fprintln(w, ", or")
	fmt.Fprintln(w, "  - adopt the sidecar layout: copy .claude-sandbox/ aside (or into the sidecar) first, then `git rm -r --cached .claude-sandbox` and commit.")
	fmt.Fprintln(w, "    That commit deletes .claude-sandbox/ from every other clone and worktree that pulls or merges it.")
}

// gitignoreAdd appends missing lines to a .gitignore-style file, prompting
// first (CS-LAY-010..014). Resolution order: Options.Gitignore flag,
// CS_GITIGNORE_ASSUME env var, interactive prompt (default yes), no-tty skip.
// Returns true when the lines were added.
func gitignoreAdd(gi string, opts Options, lines ...string) bool {
	existing := map[string]bool{}
	// CS-LAY-023: the project tree is session-writable, so the read never
	// blocks (a FIFO, device or socket there) and a file that exists but
	// cannot be read as a regular file skips the update with one warning,
	// before any prompt: appending to it could block or write elsewhere.
	raw, err := cascade.ReadRegularFile(gi)
	switch {
	case err == nil:
		for _, l := range strings.Split(string(raw), "\n") {
			existing[l] = true
		}
	case !errors.Is(err, fs.ErrNotExist):
		fmt.Fprintf(opts.errw(), "WARNING: cannot read %s (%v); skipping the .gitignore update.\n", gi, unwrapPathErr(err))
		return false
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
	// CS-LAY-023: a missing file is empty; one that exists but is not a
	// readable regular file (a FIFO, device, socket, directory) is an error,
	// never waited on and never written.
	raw, err := cascade.ReadRegularFile(file)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
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
	return writeRegularFile(file, []byte(b.String()))
}

// writeRegularFile replaces file's content like os.WriteFile (0644 when
// created), but opens it O_NONBLOCK|O_NOCTTY and writes only when the opened
// descriptor is a regular file (CS-LAY-023): a FIFO swapped in after the read
// fails open(2) with ENXIO (no reader) or is refused here, never blocking.
// O_TRUNC is applied only after the check, so nothing else is truncated.
func writeRegularFile(file string, data []byte) error {
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return &fs.PathError{Op: "open", Path: file, Err: fmt.Errorf("%w (%s)", cascade.ErrNotRegular, fi.Mode().Type())}
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Close()
}

// seedFile creates file with data, 0644, only when nothing is at the path
// (O_EXCL|O_NOFOLLOW): an entry that appeared meanwhile is kept (CS-LAY-002,
// CS-LAY-023).
func seedFile(file string, data []byte) error {
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// unwrapPathErr drops a *fs.PathError's path for a message that already
// names the file.
func unwrapPathErr(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
