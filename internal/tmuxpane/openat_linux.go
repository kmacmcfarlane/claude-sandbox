package tmuxpane

import "syscall"

// openRecord opens a registry record relative to the directory fd, so a
// directory swapped for a symlink after the open cannot redirect the read.
func openRecord(dirfd int, _ string, name string) (int, error) {
	return syscall.Openat(dirfd, name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
}
