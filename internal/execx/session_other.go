//go:build !linux

package execx

import "syscall"

// sessionAttr: parent-death signals are Linux-only. Elsewhere a launcher
// killed outright leaves the docker client running, as an exec'd one would
// have been left by whatever killed it.
func sessionAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{} }

// tetherAttr: its own process group; no parent-death signal off Linux.
func tetherAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

const ioctlGetTermios = syscall.TIOCGETA
