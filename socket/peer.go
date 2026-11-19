package socket

import (
	"context"
	"net"
)

// Peer is the caller of a call: what the kernel said about the process at the other end of the connection, read once when the
// connection was accepted.
type Peer struct {
	UID, GID uint32
	PID      int32 // 0 where the platform does not say (macOS)
}

type peerKey struct{}

// connPeer is what ConnContext stores: the credentials, and whether they could be read.
type connPeer struct {
	Peer
	known bool
}

// PeerFromContext returns the caller of the call that ctx belongs to. It is false outside a handler of a server made by [Serve],
// and when the platform could not read the credentials (only a server that admits every uid serves such a call).
func PeerFromContext(ctx context.Context) (Peer, bool) {
	p, ok := ctx.Value(peerKey{}).(connPeer)
	if !ok || !p.known {
		return Peer{}, false
	}
	return p.Peer, true
}

// peerOf reads the credentials of a connection once.
func peerOf(c net.Conn) connPeer {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return connPeer{}
	}
	p, err := peerCred(uc)
	if err != nil {
		return connPeer{}
	}
	return connPeer{Peer: p, known: true}
}
