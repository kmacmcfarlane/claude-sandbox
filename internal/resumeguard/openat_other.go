//go:build !linux

package resumeguard

import (
	"path/filepath"
	"syscall"
)

// openat falls back to a path open where the syscall package has no openat
// (darwin): the host the guard protects is Linux, and O_NOFOLLOW still
// refuses a symlinked record.
func openat(_ int, dir string, name string, flags int) (int, error) {
	return syscall.Open(filepath.Join(dir, name), flags, 0)
}
