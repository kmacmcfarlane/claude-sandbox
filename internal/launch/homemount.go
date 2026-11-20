package launch

import (
	"path/filepath"
	"strings"
)

// CoversHome reports whether a cascade mount's container path is the
// container's home or an ancestor of it, "/" included (CS-LNCH-241). The
// entrypoint relocates the image's build-time home into that path, so a bind
// mount there either fails every move (read-only) or takes the build-time
// files into the host's home (read-write). Both paths are compared cleaned; a
// relative container path, or a home that is not absolute, is never covered
// (docker rejects the former, and there is no home to protect in the latter).
func CoversHome(container, home string) bool {
	if !filepath.IsAbs(container) || !filepath.IsAbs(home) {
		return false
	}
	c, h := filepath.Clean(container), filepath.Clean(home)
	if c == "/" || c == h {
		return true
	}
	return strings.HasPrefix(h, c+"/")
}
