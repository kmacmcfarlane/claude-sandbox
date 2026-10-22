package clients

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
)

// isRealStateRoot reports whether root (cleaned, symlinks resolved as far as
// it exists) is at or inside the real state root of the real home
// (os.UserHomeDir and user.Current's), default or XDG — globalcfg.OpenStore's
// rule (CS-DIR-007). checkRoot panics on it under go test.
func isRealStateRoot(root string) bool {
	var homes []string
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		homes = append(homes, h)
	}
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		homes = append(homes, u.HomeDir)
	}
	root = resolvePath(root)
	for _, h := range homes {
		for _, r := range []string{hostdirs.StateRoot(h, os.Getenv), hostdirs.StateRoot(h, nil)} {
			r = resolvePath(r)
			if root == r || strings.HasPrefix(root, r+string(filepath.Separator)) {
				return true
			}
		}
	}
	return false
}

// resolvePath is p with symlinks resolved as far as it exists: the deepest
// existing ancestor is resolved and the rest appended.
func resolvePath(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(p, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}
