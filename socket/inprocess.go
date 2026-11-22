package socket

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/truvity/atoom/common/call"
)

// InProcess serves h at path on an in-memory transport and returns the http.Client that reaches it, and the function that stops it: the
// way a Go host links a driver or a block in its own process (docs/in-process.md). There is no socket and no port: the client dials a
// net.Pipe and the server accepts the other end, with HTTP/1.1 and HTTP/2 without TLS on the server and HTTP/2 on the client, so a
// bidirectional stream works. call.Inherit puts the correlation id of the request in the context of the handler, and the Connect handler
// reads the deadline of the caller on its own, as on a socket. There is no peer and no guard: who may call is the host's rule (an
// interceptor of the handler).
//
// A generated client takes it with any base URL: sealv1alpha1connect.NewSealServiceClient(hc, "http://seal", call.ClientOptions()...).
func InProcess(path string, h http.Handler) (client *http.Client, stop func()) {
	l := &memListener{conns: make(chan net.Conn), done: make(chan struct{})}
	mux := http.NewServeMux()
	mux.Handle(path, h)
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	srv := &http.Server{Handler: call.Inherit(mux), Protocols: &protocols, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(l) }()
	var h2 http.Protocols
	h2.SetUnencryptedHTTP2(true)
	tr := &http.Transport{DialContext: l.dial, Protocols: &h2}
	var once sync.Once
	return &http.Client{Transport: tr}, func() {
		once.Do(func() {
			tr.CloseIdleConnections()
			_ = srv.Close()
		})
	}
}

// Client returns an http.Client that reaches a server on the UNIX socket at path with HTTP/2 without TLS, as Serve and the Rust thin
// mains serve. A generated client takes it with any base URL.
func Client(path string) *http.Client {
	var h2 http.Protocols
	h2.SetUnencryptedHTTP2(true)
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
		Protocols: &h2,
	}}
}

// memListener is a net.Listener whose connections are net.Pipe pairs.
type memListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func (l *memListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *memListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *memListener) Addr() net.Addr { return memAddr{} }

func (l *memListener) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	client, server := net.Pipe()
	select {
	case l.conns <- server:
		return client, nil
	case <-l.done:
		return nil, net.ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type memAddr struct{}

func (memAddr) Network() string { return "memory" }
func (memAddr) String() string  { return "in-process" }
