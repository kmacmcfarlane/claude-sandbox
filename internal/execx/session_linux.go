//go:build linux

package execx

import "syscall"

// sessionAttr kills the session child when the thread that forked it exits
// (CS-LNCH-086); RunSession keeps that thread locked for the child's life.
func sessionAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}

const ioctlGetTermios = syscall.TCGETS
