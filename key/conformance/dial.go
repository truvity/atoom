package conformance

import (
	"context"
	"net"
	"net/http"

	"github.com/truvity/atoom/common/call"
	"github.com/truvity/atoom/key/gen/atoom/key/v1alpha1/keyv1alpha1connect"
)

// Dial returns a client of the key-store service of the driver that listens on a UNIX socket. It speaks HTTP/2 without TLS (h2c), which
// is what the thin mains and sluis's hosts serve beside HTTP/1.1; a socket that serves only HTTP/1.1 is reached with DialHTTP1. The
// client carries the call: the deadline of the context of a call goes out as Connect-Timeout-Ms, and the correlation id of the context
// (call.WithCorrelationID) as its header (docs/call-context.md).
func Dial(socket string) Client {
	return dial(socket, false)
}

// DialHTTP1 is Dial for a driver that serves HTTP/1.1 only.
func DialHTTP1(socket string) Client {
	return dial(socket, true)
}

func dial(socket string, http1 bool) Client {
	var protocols http.Protocols
	if http1 {
		protocols.SetHTTP1(true)
	} else {
		protocols.SetUnencryptedHTTP2(true)
	}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		Protocols: &protocols,
	}
	return keyv1alpha1connect.NewKeyStoreServiceClient(&http.Client{Transport: tr}, "http://key", call.ClientOptions()...)
}
