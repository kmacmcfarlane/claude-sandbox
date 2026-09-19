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

// foregroundOfTTY reports whether this process's group is the foreground
// process group of its controlling terminal: tcgetpgrp(open("/dev/tty")) ==
// getpgrp(). With no controlling terminal /dev/tty cannot be opened (ENXIO),
// and the answer is false.
func foregroundOfTTY() bool {
	tty, err := os.OpenFile("/dev/tty", os.O_RDONLY, 0)
	if err != nil {
		return false
	}
	defer tty.Close()
	var pgrp int32
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, tty.Fd(), uintptr(syscall.TIOCGPGRP), uintptr(unsafe.Pointer(&pgrp)))
	return errno == 0 && int(pgrp) == syscall.Getpgrp()
}
