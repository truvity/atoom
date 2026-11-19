//go:build !linux && !darwin && !freebsd

package socket

import (
	"errors"
	"net"
)

// peerCred is not available here: a server that does not admit every uid refuses every call (no peer, so no caller).
func peerCred(*net.UnixConn) (Peer, error) {
	return Peer{}, errors.New("peer credentials are not available on this system")
}
