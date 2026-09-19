// Package paths is the single resolver for claude-sandbox "foreign" file
// locations under .claude-sandbox/, and the parent-directory walks that power
// the config cascade. No hardcoded foreign paths should live anywhere else.
// Spec: spec/paths.feature (CS-PATH).
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

// Logical file/directory keys.
const (
	Config     = "config"
	Dockerfile = "dockerfile"
	Env        = "env"
	Ralph      = "ralph"
	Agent      = "agent"
	Scripts    = "scripts"
)

var foreignMap = map[string]string{
	Config:     ".claude-sandbox/config.yaml",
	Dockerfile: ".claude-sandbox/Dockerfile",
	Env:        ".claude-sandbox/env",
	Ralph:      ".claude-sandbox/ralph",
	Agent:      ".claude-sandbox/agent",
	Scripts:    ".claude-sandbox/scripts",
}

// EnvExampleName is the env template `init` seeds beside config.yaml
// (CS-INIT-004). It is deliberately NOT a logical key: it is never part of the
// env cascade, never an --env-file and never linted (CS-CASC-030) — only a
// file named exactly "env" is.
const EnvExampleName = "env.example"

// SandboxDir returns <project>/.claude-sandbox.
func SandboxDir(project string) string { return filepath.Join(project, ".claude-sandbox") }

// Resolve maps a logical key to its absolute path under the project.
func Resolve(project, logical string) (string, error) {
	rel, ok := foreignMap[logical]
	if !ok {
		return "", fmt.Errorf("paths: unknown logical %q", logical)
	}
	return filepath.Join(project, rel), nil
}

// Chain is the list of directories searched for .claude-sandbox/ files,
// most-local first. With main == "" it is start and each of its ancestors up
// to (not including) the filesystem root — the plain parent walk.
//
// With main set (start is a linked git worktree whose main checkout is main,
// CS-CASC-031) it is start's own ancestors that are NOT main or an ancestor of
// main, followed by main and its ancestors. A worktree outside the repository
// (~/.paseo/worktrees/<id>/<name>) thereby reaches the main checkout's
// .claude-sandbox/ as its project level, below anything only the worktree
// sits under; a worktree inside the repository (.claude/worktrees/<name>)
// gets exactly its plain parent walk. No directory appears twice.
func Chain(start, main string) []string {
	walk := func(from string) []string {
		var out []string
		for dir := from; dir != "/" && dir != "."; dir = filepath.Dir(dir) {
			out = append(out, dir)
		}
		return out
	}
	own := walk(start)
	if main == "" {
		return own
	}
	upstream := walk(main)
	onMain := make(map[string]bool, len(upstream))
	for _, d := range upstream {
		onMain[d] = true
	}
	out := make([]string, 0, len(own)+len(upstream))
	for _, d := range own {
		if !onMain[d] {
			out = append(out, d)
		}
	}
	return append(out, upstream...)
}

// FindUp walks from start to the filesystem root checking the logical path at
// each level; returns the first (nearest) hit or "" when none exists.
func FindUp(start, logical string) (string, error) {
	return FindChain(Chain(start, ""), logical)
}

// FindChain returns the first (most-local) hit of the logical path along a
// Chain, or "" when none exists.
func FindChain(chain []string, logical string) (string, error) {
	rel, ok := foreignMap[logical]
	if !ok {
		return "", fmt.Errorf("paths: unknown logical %q", logical)
	}
	for _, dir := range chain {
		p := filepath.Join(dir, rel)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
	}
	return "", nil
}

// CollectUp returns every match of the logical path walking from the
// filesystem root down to start (root-first: outermost defaults first,
// most-local last). This is the cascade order.
func CollectUp(start, logical string) ([]string, error) {
	return CollectChain(Chain(start, ""), logical)
}

// CollectChain returns every match of the logical path along a Chain,
// root-first (the chain reversed): the cascade order.
func CollectChain(chain []string, logical string) ([]string, error) {
	rel, ok := foreignMap[logical]
	if !ok {
		return nil, fmt.Errorf("paths: unknown logical %q", logical)
	}
	out := []string{}
	for i := len(chain) - 1; i >= 0; i-- {
		p := filepath.Join(chain[i], rel)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			out = append(out, p)
		}
	}
	return out, nil
}

// FindUpFile walks from start to the root looking for an arbitrary filename
// (not a logical key). Used for explicit Dockerfile overrides.
func FindUpFile(start, name string) string {
	return FindChainFile(Chain(start, ""), name)
}

// FindChainFile returns the first (most-local) match of name along a Chain,
// or "" when none exists.
func FindChainFile(chain []string, name string) string {
	for _, dir := range chain {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

// SandboxLevels returns every ancestor directory (root-first, including start)
// that contains a .claude-sandbox/ directory. Used for the cascade report.
func SandboxLevels(start string) []string {
	return SandboxLevelsChain(Chain(start, ""))
}

// SandboxLevelsChain returns every Chain directory that contains a
// .claude-sandbox/ directory, root-first.
func SandboxLevelsChain(chain []string) []string {
	out := []string{}
	for i := len(chain) - 1; i >= 0; i-- {
		if fi, err := os.Stat(SandboxDir(chain[i])); err == nil && fi.IsDir() {
			out = append(out, chain[i])
		}
	}
	return out
}

// LayoutMode reports whether the project has adopted the layout:
// "new" when .claude-sandbox/ exists, "none" otherwise.
func LayoutMode(project string) string {
	if fi, err := os.Stat(SandboxDir(project)); err == nil && fi.IsDir() {
		return "new"
	}
	return "none"
}
