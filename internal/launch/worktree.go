package launch

// Worktree mode (CS-LNCH-041..046): the launcher hands claude its own
// --worktree <name> so a session works in <repo-root>/.claude/worktrees/<name>
// on branch worktree-<name>. These helpers are the pieces the launcher needs
// on the host side: name validation (fail fast with exit 2 instead of inside
// the container), locating the git work tree (claude refuses --worktree
// outside one), and listing the worktrees that already exist (the noun picker
// must not reopen one by accident, CS-SESS-045).

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
)

// WorktreeDir is the harness-native location of worktrees, relative to the
// repository root.
const WorktreeDir = ".claude/worktrees"

// WorktreeNameMax mirrors claude's own limit.
const WorktreeNameMax = 64

var worktreeNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ValidateWorktreeName applies claude's naming rule ahead of docker
// (CS-LNCH-043): at most 64 characters from [A-Za-z0-9._-], and never a name
// that would collide with git's own metadata.
func ValidateWorktreeName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("worktree name must not be empty")
	case len(name) > WorktreeNameMax:
		return fmt.Errorf("worktree name %q is longer than %d characters", name, WorktreeNameMax)
	case !worktreeNameRe.MatchString(name):
		return fmt.Errorf("worktree name %q may only contain letters, digits, '.', '_' and '-'", name)
	case name == ".git", name == ".", name == "..":
		return fmt.Errorf("worktree name %q is reserved", name)
	}
	return nil
}

// GitRoot returns the top-level directory of the git work tree containing dir,
// or "" when dir is not inside one (CS-LNCH-046). It asks git rather than
// looking for a .git entry so a project that is a subdirectory of a repository
// still counts — claude anchors the worktree at the repository root either way.
func GitRoot(r execx.Runner, dir string) string {
	out, err := r.Output(execx.Cmd{
		Name:   "git",
		Args:   []string{"-C", dir, "rev-parse", "--show-toplevel"},
		Stderr: io.Discard,
	})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// ExistingWorktrees lists the names under <root>/.claude/worktrees, i.e. the
// worktrees claude would REOPEN rather than create. A missing directory is
// simply no worktrees.
func ExistingWorktrees(root string) []string {
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(root, WorktreeDir))
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

// LinkedWorktree describes a project that is a verified linked git worktree
// (CS-LNCH-070): its .git is a file naming <CommonDir>/worktrees/<name>, so
// the repository's metadata lives outside the worktree.
type LinkedWorktree struct {
	// Top is the worktree's top-level directory.
	Top string
	// GitDir is the worktree's own git dir, <CommonDir>/worktrees/<name>.
	GitDir string
	// CommonDir is the repository's shared git dir, mounted read-write at its
	// own path (CS-LNCH-071).
	CommonDir string
	// Main is the main checkout (CommonDir's parent when CommonDir is named
	// ".git"), whose .claude-sandbox/ becomes the project level of the
	// cascade (CS-CASC-031). "" for a bare repository or one made with
	// --separate-git-dir: its git dir does not record the main checkout.
	Main string
}

// DetectLinkedWorktree asks git, in one call, whether dir lies in a linked
// worktree, and verifies the answer (CS-LNCH-070). It returns nil — launch
// as a plain project — when git fails, when dir is a main checkout or not a
// repository at all, and when verification fails; warning is non-empty only
// for a worktree git knows but whose back-link does not match (moved).
//
// The verification is the security gate for the read-write mount of
// CommonDir: the .git file is data in the project tree, and a crafted one
// could name any other repository's git dir. git's back-link
// <GitDir>/gitdir, which only "git worktree add" (or "repair") in THAT
// repository writes, must name <Top>/.git.
func DetectLinkedWorktree(r execx.Runner, dir string) (lw *LinkedWorktree, warning string) {
	out, err := r.Output(execx.Cmd{
		Name:   "git",
		Args:   []string{"-C", dir, "rev-parse", "--git-dir", "--git-common-dir", "--show-toplevel"},
		Stderr: io.Discard,
	})
	if err != nil {
		return nil, ""
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		return nil, ""
	}
	var ps [3]string
	for i, l := range lines {
		p := strings.TrimSpace(l)
		if p == "" {
			return nil, ""
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		// Physical paths: the mount and every comparison use them, and git
		// records physical paths in the .git file and the back-link.
		if ps[i], err = filepath.EvalSymlinks(p); err != nil {
			return nil, ""
		}
	}
	gitDir, commonDir, top := ps[0], ps[1], ps[2]
	if gitDir == commonDir {
		return nil, "" // a main checkout (or a submodule): nothing to do
	}
	if filepath.Dir(gitDir) != filepath.Join(commonDir, "worktrees") {
		return nil, ""
	}
	// Containment: a real linked worktree's git dir lives in its repository,
	// never inside the worktree, and the worktree never lives inside the
	// repository's git dir. Without this a directory can declare ITSELF a
	// git dir (HEAD, commondir, gitdir, and a .git file naming itself) at
	// <clone>/worktrees/<n> of an untrusted clone whose root holds objects/
	// and refs/, pass every other check, and get the clone's root — its
	// .git/hooks included — mounted read-write.
	if within(gitDir, top) || within(commonDir, top) || within(top, commonDir) {
		return nil, ""
	}
	back, err := os.ReadFile(filepath.Join(gitDir, "gitdir"))
	if err != nil {
		return nil, ""
	}
	if !sameGitFile(strings.TrimSpace(string(back)), gitDir, filepath.Join(top, ".git")) {
		return nil, fmt.Sprintf("WARNING: %s is a git worktree of %s, but the repository's record of it (%s/gitdir) points elsewhere; "+
			"launching without the main checkout's config or git dir. Run 'git worktree repair' in %s to fix the record.",
			top, commonDir, gitDir, top)
	}
	lw = &LinkedWorktree{Top: top, GitDir: gitDir, CommonDir: commonDir}
	if filepath.Base(commonDir) == ".git" {
		lw.Main = filepath.Dir(commonDir)
	}
	return lw, ""
}

// within reports whether path is dir or lies inside it.
func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+"/")
}

// sameGitFile compares the back-link with <top>/.git physically: the
// back-link may have been written through a symlinked path, and git 2.48+
// writes it relative to the git dir under worktree.useRelativePaths.
func sameGitFile(recorded, gitDir, want string) bool {
	if recorded == "" {
		return false
	}
	if !filepath.IsAbs(recorded) {
		recorded = filepath.Join(gitDir, recorded)
	}
	if filepath.Clean(recorded) == want {
		return true
	}
	resolved, err := filepath.EvalSymlinks(recorded)
	return err == nil && resolved == want
}

// Banner is the one launcher line that makes a linked worktree visible
// (CS-LNCH-072). mounted reports whether the launch mounts the common git dir
// (CS-LNCH-071).
func (lw *LinkedWorktree) Banner(mounted bool) string {
	var b strings.Builder
	if lw.Main != "" {
		fmt.Fprintf(&b, "Linked worktree: main checkout %s (its .claude-sandbox/ config, env and Dockerfile apply)", lw.Main)
	} else {
		// Bare, or --separate-git-dir: the git dir does not record where a
		// main checkout is, so there is none to cascade from.
		fmt.Fprintf(&b, "Linked worktree: repository git dir %s (no main checkout found)", lw.CommonDir)
	}
	if mounted {
		fmt.Fprintf(&b, "; git dir %s mounted", lw.CommonDir)
	}
	return b.String()
}

// MountsCommonDir reports whether a launch of projectDir mounts the common
// git dir (CS-LNCH-071), before cascade mounts are considered: only when the
// worktree root is the project directory or under it. From a subdirectory the
// worktree root, and with it the .git file, is not in the container.
func (lw *LinkedWorktree) MountsCommonDir(projectDir string) bool {
	return lw != nil && underSamePathMount([]string{projectDir + ":" + projectDir}, lw.Top)
}
