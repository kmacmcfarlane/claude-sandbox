// Package scaffold seeds bootstrap files from the embedded scaffold trees.
// Seeding is always gap-filling: existing files are never overwritten.
// Spec: spec/init.feature, spec/init-ralph.feature.
package scaffold

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	assets "github.com/kmacmcfarlane/claude-sandbox"
)

// ReadBase returns an embedded base-scaffold file (config.yaml, env,
// Dockerfile.example).
func ReadBase(name string) ([]byte, error) {
	return assets.Scaffold.ReadFile("scaffold/" + name)
}

// SeedFile writes content to dest unless dest already exists.
// Returns true when the file was created.
func SeedFile(dest string, content []byte) (bool, error) {
	if _, err := os.Stat(dest); err == nil {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(dest, content, 0o644)
}

// SeedRalph copies the embedded ralph scaffold (agent/ + scripts/) into
// sandboxDir, never overwriting an existing file. The __PROJECT_NAME__
// placeholder is substituted (literally) in newly created files only, and
// seeded .py files are made executable. Returns created/skipped counts.
func SeedRalph(sandboxDir, projectName string, out io.Writer) (created, skipped int, err error) {
	return seedRalphFrom(assets.ScaffoldRalph, "scaffold-ralph", sandboxDir, projectName, out)
}

// skipEntry reports whether a scaffold entry is tooling debris that must
// never be seeded (CS-INITR-007): __pycache__ and .pytest_cache directories,
// any dot-prefixed entry, and compiled Python bytecode (.pyc/.pyo, any case,
// file or directory). assets.go embeds the tree with the `all:` prefix (no
// exclusion syntax), so a binary built from a working tree that ran the
// backlog tests carries whatever pytest left on disk — this walk is the
// enforceable filter, not the embed pattern.
func skipEntry(d fs.DirEntry) bool {
	name := d.Name()
	if strings.HasPrefix(name, ".") || name == "__pycache__" {
		return true
	}
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".pyc") || strings.HasSuffix(lower, ".pyo")
}

// seedRalphFrom is SeedRalph over an arbitrary fs.FS rooted at root, so tests
// can plant debris the embedded tree never carries in a clean checkout.
func seedRalphFrom(fsys fs.FS, root, sandboxDir, projectName string, out io.Writer) (created, skipped int, err error) {
	err = fs.WalkDir(fsys, root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if path != root && skipEntry(d) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(path, root+"/")
		dest := filepath.Join(sandboxDir, rel)
		if _, serr := os.Stat(dest); serr == nil {
			skipped++
			return nil
		}
		raw, rerr := fs.ReadFile(fsys, path)
		if rerr != nil {
			return rerr
		}
		// Literal substitution: project names may contain regex/replacement
		// metacharacters.
		raw = []byte(strings.ReplaceAll(string(raw), "__PROJECT_NAME__", projectName))
		if merr := os.MkdirAll(filepath.Dir(dest), 0o755); merr != nil {
			return merr
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(dest, ".py") {
			mode = 0o755
		}
		if werr := os.WriteFile(dest, raw, mode); werr != nil {
			return werr
		}
		created++
		return nil
	})
	if err != nil {
		return created, skipped, err
	}
	if out != nil {
		fmt.Fprintf(out, "Ralph scaffolding: %d created, %d skipped (under %s/agent, %s/scripts)\n",
			created, skipped, sandboxDir, sandboxDir)
	}
	return created, skipped, nil
}
