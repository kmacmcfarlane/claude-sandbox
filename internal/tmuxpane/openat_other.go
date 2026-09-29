//go:build !linux

package tmuxpane

import (
	"path/filepath"
	"syscall"
)

// openRecord falls back to a path open where the syscall package has no
// openat (darwin); the record itself is still never followed.
func openRecord(_ int, dir, name string) (int, error) {
	return syscall.Open(filepath.Join(dir, name), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
}
