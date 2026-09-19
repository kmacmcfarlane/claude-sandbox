package execx

import (
	"io"
	"os"
	"syscall"
	"unsafe"
)

// IsTerminal reports whether w is a terminal (a file whose termios can be
// read). A character device alone is not enough: /dev/null is one.
func IsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok || f == nil {
		return false
	}
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(ioctlGetTermios), uintptr(unsafe.Pointer(&t)))
	return errno == 0
}
