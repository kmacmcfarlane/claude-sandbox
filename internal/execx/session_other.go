//go:build !linux

package execx

import "syscall"

// sessionAttr: parent-death signals are Linux-only. Elsewhere a launcher
// killed outright leaves the docker client running, as an exec'd one would
// have been left by whatever killed it.
func sessionAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{} }

const ioctlGetTermios = syscall.TIOCGETA
