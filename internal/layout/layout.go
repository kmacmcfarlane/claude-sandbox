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
	// CS-LAY-025: a .claude-sandbox directory that is a symlink out of the
	// project would put every write below — the skeleton, the CLAUDE.md seed,
	// the sidecar .gitignore and repo — outside it. Checked FIRST, before any
	// mkdir, in both trackInHost modes. The whole layout is skipped with one
	// warning rather than failing the launch: nothing on the launch path
	// reads what the layout creates (the cascade reads config.yaml/env
	// through the link as before; ralph makes its own runtime dirs), so a
	// refusal would only stop a deliberately linked directory from launching.
	if _, _, err := resolveInProject(project, sb); err != nil {
		fmt.Fprintf(opts.errw(), "WARNING: %v; skipping the layout setup (the temp/ and reports/ skeleton, the CLAUDE.md seed, the .gitignore entries and the sidecar git repo), which would write there.\n", err)
		return nil
	}
	// The skeleton is temp/ and reports/ only. investigations/ is a claude-kit
	// investigate/implement convention, not a sandbox path: never created here,
	// and an existing one is user data that is left alone.
	for _, d := range []string{filepath.Join(sb, "temp"), filepath.Join(sb, "reports")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	sidecarGitExists := dirExists(filepath.Join(sb, ".git"))
	hostGI := filepath.Join(project, ".gitignore")
	sideGI := filepath.Join(sb, ".gitignore")

	// CS-LAY-024: every git call here is bounded (execx.GitTimeout). A probe
	// that times out is an UNKNOWN answer, never a "no": one warning, and
	// the host .gitignore update and the sidecar git init — the steps that
	// act on the answers — are skipped; the sidecar .gitignore (CS-LAY-004)
	// is still written. gitUnknown is that path; hostTracked is the count
	// known so far (0 when not probed).
	hostTracked := 0
	gitUnknown := func(gerr error) error {
		instead := "skipping the .gitignore update and the git ignore checks"
		if !trackInHost && !sidecarGitExists && hostTracked == 0 {
			instead += ", and so the sidecar git init"
		}
		fmt.Fprintln(opts.errw(), execx.GitTimeoutWarning(gerr, instead))
		if !trackInHost && hostTracked > 0 {
			warnHostTracked(opts.errw(), hostTracked, false, sidecarGitExists)
		}
		if !trackInHost {
			return ensureLines(project, sideGI, "temp/", "env", "ralph/")
		}
		return nil
	}

	hostIsGit, gerr := isGitWorkTree(opts.Runner, project)
	if gerr != nil {
		// Whether the host tracks files here is unknown too, so the
		// CLAUDE.md seed (which waits on that answer) waits for a launch
		// on which git answers.
		return gitUnknown(gerr)
	}
	// CS-LAY-020: files the host already tracks under .claude-sandbox/ while
	// trackInHost is false. Probed only in that mode; a failed probe is 0.
	if !trackInHost && hostIsGit {
		n, gerr := hostTrackedCount(opts.Runner, project)
		if gerr != nil {
			return gitUnknown(gerr)
		}
		hostTracked = n
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
			// The sidecar init (CS-LAY-005/006) needs dirIgnored's answer, so
			// it is skipped too, and the warning says so when it would have
			// run (9eab review).
			also := ""
			if !trackInHost && !sidecarGitExists && hostTracked == 0 {
				also = ", and so the sidecar git init"
			}
			fmt.Fprintf(opts.errw(), "WARNING: cannot read %s (%v); skipping the .gitignore update and the git ignore checks, which would read it%s.\n", p, unwrapPathErr(err), also)
			if !trackInHost && hostTracked > 0 {
				warnHostTracked(opts.errw(), hostTracked, false, sidecarGitExists)
			}
			if !trackInHost {
				// CS-LAY-004, as on the ordinary path.
				if err := ensureLines(project, sideGI, "temp/", "env", "ralph/"); err != nil {
					return err
				}
			}
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
			conflict, gerr := hostTrackConflict(opts.Runner, project, sb)
			if gerr != nil {
				return gitUnknown(gerr)
			}
			if conflict != "" {
				fmt.Fprintf(opts.errw(), "WARNING: trackInHost is true but %s; skipping the host-tracked .gitignore entries, which would be dead there.\n", conflict)
				fmt.Fprintln(opts.errw(), "  Either set trackInHost: false in .claude-sandbox/config.yaml (and delete any .claude-sandbox/env, temp/, ralph/, !config.yaml or !Dockerfile lines already in .gitignore — they are dead),")
				fmt.Fprintln(opts.errw(), "  or drop the ignore rule (`git check-ignore -v --no-index "+dirProbe+"` names it) and .claude-sandbox/.git to track the directory in the host.")
				gitignoreAdd(project, hostGI, opts, withWorktreesLine(hostGI)...)
				return nil
			}
			proposed := gitignoreAdd(project, hostGI, opts, withWorktreesLine(hostGI,
				".claude-sandbox/env", ".claude-sandbox/temp/", ".claude-sandbox/ralph/",
				"!.claude-sandbox/config.yaml", "!.claude-sandbox/Dockerfile")...)
			// CS-LAY-026: only while the host-tracked entries are being
			// decided — proposed (whatever the answer) or init, which passes
			// Options.Gitignore (the launch path never does) — so an ordinary
			// launch with nothing to decide stays quiet.
			if hostTrackedEntryIn(proposed) || opts.Gitignore != nil {
				noteChildrenHidden(project, opts)
			}
		}
		return nil
	}

	// Foreign-safe: whole dir ignored in host; sidecar repo holds history.
	// CS-LAY-020: over files the host already tracks, the whole-dir ignore
	// would silently drop every new file from `git add`. Warn, never switch
	// modes, still propose the worktrees line, and skip the sidecar init.
	if hostIsGit {
		if hostTracked > 0 {
			ignored, gerr := dirIgnored(opts.Runner, project)
			if gerr != nil {
				return gitUnknown(gerr)
			}
			warnHostTracked(opts.errw(), hostTracked, ignored, sidecarGitExists)
			gitignoreAdd(project, hostGI, opts, withWorktreesLine(hostGI)...)
		} else {
			gitignoreAdd(project, hostGI, opts, withWorktreesLine(hostGI, "/.claude-sandbox/")...)
		}
	}
	// CS-LAY-004: sidecar's own .gitignore — append-only, no prompt.
	if err := ensureLines(project, sideGI, "temp/", "env", "ralph/"); err != nil {
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
	ignored := !hostIsGit
	if hostIsGit {
		var gerr error
		if ignored, gerr = dirIgnored(opts.Runner, project); gerr != nil {
			// CS-LAY-024: the host .gitignore step is already done; only
			// the init waits on this answer.
			fmt.Fprintln(opts.errw(), execx.GitTimeoutWarning(gerr, "skipping the sidecar git init"))
			return nil
		}
	}
	if ignored {
		// CS-LAY-024: bounded too; a timed-out init may leave a partial
		// .git, which the next launch would take for a sidecar repo.
		_, err := execx.Git(opts.Runner, execx.Cmd{Name: "git", Args: []string{"-C", sb, "init", "-q"}})
		switch {
		case errors.Is(err, execx.ErrTimedOut):
			fmt.Fprintln(opts.errw(), execx.GitTimeoutWarning(err,
				"no sidecar git repo; remove any partial "+filepath.Join(sb, ".git")+" and run 'git -C "+sb+" init' to create it"))
		case err == nil:
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

// isGitWorkTree asks git whether dir is inside a work tree; any failure is
// "no". The error is non-nil only when git did not answer within
// execx.GitTimeout (CS-LAY-024): the answer is unknown.
func isGitWorkTree(r execx.Runner, dir string) (bool, error) {
	_, err := execx.Git(r, execx.Cmd{Name: "git", Args: []string{"-C", dir, "rev-parse", "--is-inside-work-tree"}})
	if errors.Is(err, execx.ErrTimedOut) {
		return false, err
	}
	return err == nil, nil
}

// hostTrackConflict reports why host-tracked (trackInHost: true) .gitignore
// entries would be dead — CS-LAY-018: the host repo excludes the
// .claude-sandbox directory itself, a sidecar .git exists inside it, or both.
// Returns "" when neither condition holds.
// The error is non-nil only when the probe timed out (CS-LAY-024).
func hostTrackConflict(r execx.Runner, project, sb string) (string, error) {
	ignored, err := dirExcluded(r, project)
	if err != nil {
		return "", err
	}
	sidecar := dirExists(filepath.Join(sb, ".git"))
	switch {
	case ignored && sidecar:
		return "the host repo already ignores .claude-sandbox/ and .claude-sandbox/.git exists", nil
	case ignored:
		return "the host repo already ignores .claude-sandbox/", nil
	case sidecar:
		return ".claude-sandbox/.git exists", nil
	}
	return "", nil
}

// HostTrackedCount returns how many files the host repo tracks under
// .claude-sandbox/ (CS-LAY-020). A failed probe returns 0 (including a
// project outside any git work tree), so behaviour is exactly as before: a
// probe failure never counts as "tracked". init shares it to default its
// greenfield trackInHost prompt to true in that state (CS-INIT-031).
//
// The call is bounded (CS-LAY-024); a timeout is 0 here, the old failure
// semantic, while Setup reads it through hostTrackedCount as unknown.
func HostTrackedCount(r execx.Runner, project string) int {
	n, _ := hostTrackedCount(r, project)
	return n
}

// hostTrackedCount is HostTrackedCount with the timeout reported: the error
// is non-nil only when git did not answer within execx.GitTimeout.
func hostTrackedCount(r execx.Runner, project string) (int, error) {
	out, err := execx.Git(r, execx.Cmd{Name: "git", Args: []string{"-C", project, "ls-files", "-z", "--", ".claude-sandbox"}})
	if errors.Is(err, execx.ErrTimedOut) {
		return 0, err
	}
	if err != nil {
		return 0, nil
	}
	n := 0
	for _, f := range strings.Split(out, "\x00") {
		if f != "" {
			n++
		}
	}
	return n, nil
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
// The error is non-nil only when the probe timed out (CS-LAY-024).
func dirExcluded(r execx.Runner, project string) (bool, error) {
	_, err := execx.Git(r, execx.Cmd{Name: "git", Args: []string{"-C", project, "check-ignore", "-q", "--no-index", "--", dirProbe}})
	if errors.Is(err, execx.ErrTimedOut) {
		return false, err
	}
	return err == nil, nil
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
// A timed-out probe (CS-LAY-024) is false here, the old failure semantic.
func DirIgnored(r execx.Runner, project string) bool {
	ignored, _ := dirIgnored(r, project)
	return ignored
}

// dirIgnored reports whether the host repo ignores new files under
// .claude-sandbox/: true only when every ignoreProbes path is ignored. The
// error is non-nil only when a probe timed out (CS-LAY-024); no later probe
// runs then.
func dirIgnored(r execx.Runner, project string) (bool, error) {
	for _, p := range ignoreProbes {
		_, err := execx.Git(r, execx.Cmd{Name: "git", Args: []string{"-C", project, "check-ignore", "-q", "--", p}})
		if errors.Is(err, execx.ErrTimedOut) {
			return false, err
		}
		if err != nil {
			return false, nil
		}
	}
	return true, nil
}

// hostTrackedEntryIn reports whether lines holds any entry other than the
// worktrees line, i.e. a CS-LAY-009 host-tracked entry.
func hostTrackedEntryIn(lines []string) bool {
	for _, l := range lines {
		if l != worktreesLine {
			return true
		}
	}
	return false
}

// noteChildrenHidden prints the one CS-LAY-026 note: trackInHost is true, the
// directory itself is not excluded (CS-LAY-018 passed), but a rule hides new
// files there (both CS-LAY-022 probes ignored). It claims only what the
// probes prove: which named paths a user's own "!" lines, an inner
// .claude-sandbox/.gitignore or already-tracked files leave visible is not
// probed. The probes run here only — nothing on this path asked them
// before — after the host .gitignore step, so a timeout (unknown) changes
// nothing already done and prints no note.
func noteChildrenHidden(project string, opts Options) {
	hidden, err := dirIgnored(opts.Runner, project)
	if err != nil || !hidden {
		return
	}
	fmt.Fprintln(opts.errw(), "Note: trackInHost is true but a host ignore rule hides new files under .claude-sandbox/ (only paths your rules re-include are tracked); `git check-ignore -v --no-index .claude-sandbox/<path>` names the rule.")
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
// Returns the lines it proposed (missing from the file), whatever the answer;
// nil when none was missing or the file was refused before any proposal.
func gitignoreAdd(project, gi string, opts Options, lines ...string) []string {
	// CS-LAY-025: a symlink leading out of the project tree is refused before
	// any prompt: a session could point it at another file the user can
	// write, and the default-yes prompt would append the lines there.
	if _, _, err := resolveInProject(project, gi); err != nil {
		fmt.Fprintf(opts.errw(), "WARNING: %v; skipping the .gitignore update (the launcher writes .gitignore lines only inside the project).\n", err)
		return nil
	}
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
		return nil
	}
	var missing []string
	for _, l := range lines {
		if !existing[l] {
			missing = append(missing, l)
		}
	}
	if len(missing) == 0 {
		return nil
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
		return missing
	}
	if !add {
		fmt.Fprintln(opts.errw(), "Skipped .gitignore update.")
		return missing
	}
	if err := ensureLines(project, gi, missing...); err != nil {
		fmt.Fprintf(opts.errw(), "failed to update %s: %v\n", gi, err)
		return missing
	}
	fmt.Fprintf(opts.errw(), "Updated %s\n", gi)
	return missing
}

// ensureLines appends each line not already present verbatim, keeping the
// file newline-terminated (CS-LAY-011). file lies inside project, and is
// written only through project (CS-LAY-025): a symlink at it may lead
// anywhere inside the project tree, never out of it.
func ensureLines(project, file string, lines ...string) error {
	if _, _, err := resolveInProject(project, file); err != nil {
		return fmt.Errorf("%v; refusing to write through it", err)
	}
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
	return writeRegularFile(project, file, []byte(b.String()))
}

// resolveInProject resolves file — every symlink on the way, absolute or
// relative, and a dangling final link to the path it would create — and
// returns the physical project root and file's path relative to it
// (CS-LAY-025). A target outside the project, or a path that cannot be
// resolved (a symlink loop, a missing directory on the way), is an error
// naming it: the launcher writes .gitignore lines only inside the project.
func resolveInProject(project, file string) (root, rel string, err error) {
	root = filepath.Clean(project)
	if r, err := filepath.EvalSymlinks(project); err == nil {
		root = r
	}
	target, err := filepath.EvalSymlinks(file)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return "", "", fmt.Errorf("%s cannot be resolved inside the project %s (%v)", file, project, unwrapPathErr(err))
		}
		// Missing: the file itself, or the end of a (chain of) dangling
		// link(s). Follow the links by hand; the directory each lands in
		// must exist and is resolved physically.
		t := file
		for i := 0; ; i++ {
			if i == 40 {
				return "", "", fmt.Errorf("%s cannot be resolved inside the project %s (too many levels of symbolic links)", file, project)
			}
			dir, derr := filepath.EvalSymlinks(filepath.Dir(t))
			if derr != nil {
				return "", "", fmt.Errorf("%s cannot be resolved inside the project %s (%v)", file, project, unwrapPathErr(derr))
			}
			t = filepath.Join(dir, filepath.Base(t))
			fi, lerr := os.Lstat(t)
			if lerr != nil || fi.Mode()&os.ModeSymlink == 0 {
				break
			}
			l, rerr := os.Readlink(t)
			if rerr != nil {
				return "", "", fmt.Errorf("%s cannot be resolved inside the project %s (%v)", file, project, unwrapPathErr(rerr))
			}
			if !filepath.IsAbs(l) {
				l = filepath.Join(filepath.Dir(t), l)
			}
			t = filepath.Clean(l)
		}
		target = t
	}
	if !within(target, root) {
		return "", "", fmt.Errorf("%s is a symlink to %s, outside the project %s", file, target, project)
	}
	rel, err = filepath.Rel(root, target)
	if err != nil || rel == "." {
		return "", "", fmt.Errorf("%s cannot be resolved inside the project %s", file, project)
	}
	return root, rel, nil
}

// within reports whether path is dir or lies inside it.
func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+"/")
}

// writeRegularFile replaces file's content like os.WriteFile (0644 when
// created), but opens it O_NONBLOCK|O_NOCTTY and writes only when the opened
// descriptor is a regular file (CS-LAY-023): a FIFO swapped in after the read
// fails open(2) with ENXIO (no reader) or is refused here, never blocking.
// O_TRUNC is applied only after the check, so nothing else is truncated.
// file must resolve inside dir (resolveInProject, CS-LAY-025) and is opened
// as that resolved relative path through os.OpenRoot of dir's physical path:
// a symlink to a file inside the project, absolute or relative, is followed,
// and a link re-pointed out of the project after the resolution fails the
// open (os.Root refuses any escape) instead of writing elsewhere.
func writeRegularFile(dir, file string, data []byte) error {
	rootDir, rel, err := resolveInProject(dir, file)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0o644)
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
