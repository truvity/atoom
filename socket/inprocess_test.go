package socket_test

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvity/atoom/common/call"
	"github.com/truvity/atoom/socket"
)

// A call through InProcess reaches the handler over HTTP/2 with the correlation id of the caller's context and its deadline, many at
// once, and nothing after stop.
func TestInProcessReachesTheHandlerWithTheCallAndStops(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]bool{}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			http.Error(w, "not HTTP/2", http.StatusBadRequest)
			return
		}
		mu.Lock()
		seen[call.CorrelationID(r.Context())] = true
		mu.Unlock()
		_, _ = io.WriteString(w, "ok")
	})
	hc, stop := socket.InProcess("/x/", h)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			id := "corr-" + strings.Repeat("a", i+1)
			req, _ := http.NewRequestWithContext(call.WithCorrelationID(ctx, id), http.MethodGet, "http://any/x/y", nil)
			req.Header.Set(call.HeaderCorrelation, id)
			res, err := hc.Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			b, _ := io.ReadAll(res.Body)
			_ = res.Body.Close()
			if string(b) != "ok" {
				t.Errorf("answer %q (%s)", b, res.Status)
			}
		})
	}
	wg.Wait()
	if len(seen) != 20 {
		t.Fatalf("the handler saw %d correlation ids, want 20", len(seen))
	}
	stop()
	stop() // twice is fine
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://any/x/y", nil)
	if res, err := hc.Do(req); err == nil {
		_ = res.Body.Close()
		t.Fatal("a call after stop was answered")
	}
}

// Client reaches a server of Serve on its socket.
func TestClientReachesServe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- socket.Serve(ctx, socket.Config{Socket: path}, "/x/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			answer := "not HTTP/2"
			if r.ProtoMajor == 2 {
				answer = "HTTP/2"
			}
			_, _ = io.WriteString(w, answer)
		}))
	}()
	hc := socket.Client(path)
	var got string
	for range 200 {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://any/x/", nil)
		res, err := hc.Do(req)
		if err == nil {
			b, _ := io.ReadAll(res.Body)
			_ = res.Body.Close()
			got = string(b)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got != "HTTP/2" {
		t.Fatalf("the server saw %s, want HTTP/2", got)
	}
}
