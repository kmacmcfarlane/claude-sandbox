//go:build !linux

package clipboard

import (
	"errors"
	"net"
)

// peerUID is unsupported off Linux: the bridge stands down on darwin (v1),
// and a helper that ran anyway would refuse every peer (fail closed).
func peerUID(*net.UnixConn) (int, error) { return -1, errors.New("no SO_PEERCRED") }
