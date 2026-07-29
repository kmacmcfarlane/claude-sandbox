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
	root := "scaffold-ralph"
	err = fs.WalkDir(assets.ScaffoldRalph, root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			if d.Name() == "__pycache__" {
				return fs.SkipDir
			}
			return nil
		}
		rel := strings.TrimPrefix(path, root+"/")
		dest := filepath.Join(sandboxDir, rel)
		if _, serr := os.Stat(dest); serr == nil {
			skipped++
			return nil
		}
		raw, rerr := assets.ScaffoldRalph.ReadFile(path)
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
