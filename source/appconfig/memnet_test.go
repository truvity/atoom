package appconfig_test

import (
	"context"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/truvity/atoom/common/call"
)

// inMemory serves h at path on an in-memory transport and returns the http.Client that reaches it: the way a Go consumer links a driver in
// its own process (socket/docs/in-process.md has this code). There is no socket and no port: the client dials a net.Pipe, and the server
// accepts the other end. call.Inherit puts the correlation id of the request in the context of the handler; the Connect handler reads the
// deadline of the caller on its own.
func inMemory(t *testing.T, path string, h http.Handler) *http.Client {
	t.Helper()
	l := &memListener{conns: make(chan net.Conn), done: make(chan struct{})}
	mux := http.NewServeMux()
	mux.Handle(path, h)
	srv := &http.Server{Handler: call.Inherit(mux), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return &http.Client{Transport: &http.Transport{DialContext: l.dial}}
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
func (memAddr) String() string  { return "memory" }
