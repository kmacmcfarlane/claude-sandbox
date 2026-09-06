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
