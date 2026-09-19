//go:build linux

package execx

import "syscall"

// sessionAttr kills the session child when the thread that forked it exits
// (CS-LNCH-086); RunSession keeps that thread locked for the child's life.
func sessionAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}

// tetherAttr is Start's DieWithParent: its own process group, so terminal
// signals never reach it, and the parent-death signal (CS-LNCH-098).
func tetherAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}

const ioctlGetTermios = syscall.TCGETS
