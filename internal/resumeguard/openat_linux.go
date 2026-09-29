//go:build linux

package resumeguard

import "syscall"

// openat opens name relative to the directory fd, so a directory whose
// parent is swapped for a symlink cannot redirect the read (07 § 7).
func openat(dirfd int, _ string, name string, flags int) (int, error) {
	return syscall.Openat(dirfd, name, flags, 0)
}
