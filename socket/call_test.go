package socket_test

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/common/call"
	commonv1alpha1 "github.com/truvity/atoom/common/gen/atoom/common/v1alpha1"
	"github.com/truvity/atoom/socket"
)

// The server of the thin mains hands the correlation id and the deadline of a call to its handler: a handler that calls on with its own
// context hands both down (docs/call-context.md). The handler answers what it saw: the id in Reason and the milliseconds left of its
// deadline in FieldPath.
const seeProcedure = "/atoom.test.v1.Call/See"

func serveSee(t *testing.T, cfg socket.Config) string {
	t.Helper()
	cfg.Socket = filepath.Join(shortDir(t), "c")
	h := connect.NewUnaryHandler(seeProcedure, func(ctx context.Context, _ *connect.Request[commonv1alpha1.Refusal]) (*connect.Response[commonv1alpha1.Refusal], error) {
		out := &commonv1alpha1.Refusal{Reason: call.CorrelationID(ctx), FieldPath: "none"}
		if d, ok := ctx.Deadline(); ok {
			out.FieldPath = strconv.FormatInt(time.Until(d).Milliseconds(), 10)
		}
		return connect.NewResponse(out), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- socket.Serve(ctx, cfg, seeProcedure, h) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
	for i := 0; i < 500; i++ {
		if c, err := net.Dial("unix", cfg.Socket); err == nil {
			_ = c.Close()
			return cfg.Socket
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no socket in 5 seconds")
	return ""
}

func seeClient(path string) *connect.Client[commonv1alpha1.Refusal, commonv1alpha1.Refusal] {
	var protocols http.Protocols
	protocols.SetUnencryptedHTTP2(true)
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
		Protocols: &protocols,
	}}
	return connect.NewClient[commonv1alpha1.Refusal, commonv1alpha1.Refusal](hc, "http://unix"+seeProcedure, call.ClientOptions()...)
}

func TestAHandlerOfServeSeesTheCorrelationIDAndTheDeadlineOfTheCaller(t *testing.T) {
	for name, allow := range map[string]socket.Allow{"same uid": socket.AllowSameUID(), "any uid": socket.AllowAnyUID()} {
		t.Run(name, func(t *testing.T) {
			c := seeClient(serveSee(t, socket.Config{Allow: allow}))
			ctx, cancel := context.WithTimeout(call.WithCorrelationID(context.Background(), "req-7"), 5*time.Second)
			defer cancel()
			res, err := c.CallUnary(ctx, connect.NewRequest(&commonv1alpha1.Refusal{}))
			if err != nil {
				t.Fatal(err)
			}
			if res.Msg.GetReason() != "req-7" {
				t.Errorf("the handler saw the id %q, want req-7", res.Msg.GetReason())
			}
			n, err := strconv.ParseInt(res.Msg.GetFieldPath(), 10, 64)
			if err != nil || n < 3000 || n > 5000 {
				t.Errorf("the handler has %q ms left of 5000", res.Msg.GetFieldPath())
			}
		})
	}
}

func TestACallWithNoIDGivesTheHandlerNone(t *testing.T) {
	c := seeClient(serveSee(t, socket.Config{}))
	res, err := c.CallUnary(context.Background(), connect.NewRequest(&commonv1alpha1.Refusal{}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Msg.GetReason() != "" || res.Msg.GetFieldPath() != "none" {
		t.Errorf("the handler saw %q and %q, want nothing", res.Msg.GetReason(), res.Msg.GetFieldPath())
	}
}

func TestABadIDOnTheWireIsNotTheIDOfTheHandler(t *testing.T) {
	c := seeClient(serveSee(t, socket.Config{}))
	req := connect.NewRequest(&commonv1alpha1.Refusal{})
	req.Header().Set(call.HeaderCorrelation, "not an id")
	res, err := c.CallUnary(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Msg.GetReason() != "" {
		t.Errorf("the handler saw the bad id %q", res.Msg.GetReason())
	}
}
