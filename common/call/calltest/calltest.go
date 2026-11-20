// Package calltest is for the tests of a client of a port: a server that records what each call carried.
//
// A [Recorder] answers every call unimplemented and keeps the headers that matter to the call context. A test builds the generated
// client of its port over [Recorder.URL] with [call.ClientOptions], makes a call with a context that has a deadline and a correlation
// id, and asks what went out. It needs no port, so the same few lines test the client of every port.
package calltest

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/truvity/atoom/common/call"
)

// Sent is what one call carried.
type Sent struct {
	// Correlation is the correlation id header, or "".
	Correlation string
	// TimeoutMS is the Connect-Timeout-Ms header, or -1 when there was none.
	TimeoutMS int64
}

// Recorder is a server that records the calls it gets and refuses them all.
type Recorder struct {
	srv  *httptest.Server
	mu   sync.Mutex
	sent []Sent
}

// New starts a recorder on a TCP port of the loopback; it stops with the test.
func New(t testing.TB) *Recorder {
	t.Helper()
	r := &Recorder{}
	r.srv = httptest.NewServer(r.handler())
	t.Cleanup(r.srv.Close)
	return r
}

// NewOnSocket starts a recorder on a UNIX socket (HTTP/1.1 and HTTP/2 without TLS), for the client of a driver that is dialled by its
// socket. Keep the path under 100 bytes: a UNIX socket path is at most 104 on macOS. It stops with the test.
func NewOnSocket(t testing.TB, path string) *Recorder {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	r := &Recorder{}
	r.srv = httptest.NewUnstartedServer(r.handler())
	r.srv.Listener = ln
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	r.srv.Config.Protocols = &protocols
	r.srv.Start()
	t.Cleanup(r.srv.Close)
	return r
}

func (r *Recorder) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		s := Sent{Correlation: req.Header.Get(call.HeaderCorrelation), TimeoutMS: -1}
		if v := req.Header.Get("Connect-Timeout-Ms"); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				s.TimeoutMS = n
			}
		}
		r.mu.Lock()
		r.sent = append(r.sent, s)
		r.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write([]byte(`{"code":"unimplemented","message":"recorder"}`))
	})
}

// URL is the base URL for a generated client.
func (r *Recorder) URL() string { return r.srv.URL }

// HTTP is the HTTP client for a generated client.
func (r *Recorder) HTTP() *http.Client { return r.srv.Client() }

// Sent returns every call recorded so far, in order.
func (r *Recorder) Sent() []Sent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Sent(nil), r.sent...)
}

// AssertCarried fails the test unless the last call carried the correlation id id and a timeout of more than 0 and at most atMost.
func (r *Recorder) AssertCarried(t testing.TB, id string, atMost time.Duration) {
	t.Helper()
	sent := r.Sent()
	if len(sent) == 0 {
		t.Fatal("the recorder got no call")
	}
	last := sent[len(sent)-1]
	if last.Correlation != id {
		t.Errorf("the correlation id is %q, want %q", last.Correlation, id)
	}
	if last.TimeoutMS < 1 || last.TimeoutMS > atMost.Milliseconds() {
		t.Errorf("the timeout is %d ms, want 1 to %d", last.TimeoutMS, atMost.Milliseconds())
	}
}

// AssertBare fails the test unless the last call carried neither.
func (r *Recorder) AssertBare(t testing.TB) {
	t.Helper()
	sent := r.Sent()
	if len(sent) == 0 {
		t.Fatal("the recorder got no call")
	}
	if last := sent[len(sent)-1]; last.Correlation != "" || last.TimeoutMS != -1 {
		t.Errorf("the call carried %+v, want neither", last)
	}
}
