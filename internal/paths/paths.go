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

// FindUp walks from start to the filesystem root checking the logical path at
// each level; returns the first (nearest) hit or "" when none exists.
func FindUp(start, logical string) (string, error) {
	rel, ok := foreignMap[logical]
	if !ok {
		return "", fmt.Errorf("paths: unknown logical %q", logical)
	}
	for dir := start; dir != "/" && dir != "."; dir = filepath.Dir(dir) {
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
	rel, ok := foreignMap[logical]
	if !ok {
		return nil, fmt.Errorf("paths: unknown logical %q", logical)
	}
	var leafFirst []string
	for dir := start; dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		p := filepath.Join(dir, rel)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			leafFirst = append(leafFirst, p)
		}
	}
	out := make([]string, 0, len(leafFirst))
	for i := len(leafFirst) - 1; i >= 0; i-- {
		out = append(out, leafFirst[i])
	}
	return out, nil
}

// FindUpFile walks from start to the root looking for an arbitrary filename
// (not a logical key). Used for explicit Dockerfile overrides.
func FindUpFile(start, name string) string {
	for dir := start; dir != "/" && dir != "."; dir = filepath.Dir(dir) {
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
	var leafFirst []string
	for dir := start; dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		if fi, err := os.Stat(SandboxDir(dir)); err == nil && fi.IsDir() {
			leafFirst = append(leafFirst, dir)
		}
	}
	out := make([]string, 0, len(leafFirst))
	for i := len(leafFirst) - 1; i >= 0; i-- {
		out = append(out, leafFirst[i])
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
