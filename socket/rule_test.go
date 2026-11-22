package socket_test

import (
	"context"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	commonv1alpha1 "github.com/truvity/atoom/common/gen/atoom/common/v1alpha1"
	"github.com/truvity/atoom/common/refusal"
	"github.com/truvity/atoom/socket"
)

// The host's rule (AllowFunc), the observer of refusals (Config.OnRefused) and the default deadline (Config.DefaultDeadline).

type asked struct {
	peer      socket.Peer
	procedure string
}

func TestAHostRuleAdmitsFromTheWholePeerAndTheProcedure(t *testing.T) {
	var mu sync.Mutex
	var got []asked
	own := me()
	r := start(t, socket.Config{Allow: socket.AllowFunc(func(p socket.Peer, procedure string) string {
		mu.Lock()
		got = append(got, asked{p, procedure})
		mu.Unlock()
		if p.UID == own {
			return ""
		}
		return "call_graph"
	})})
	if _, err := r.ask(t); err != nil {
		t.Fatalf("the rule refused the own uid: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].peer.UID != me() || got[0].procedure != procedure {
		t.Fatalf("the rule was asked %+v", got)
	}
	if runtime.GOOS == "linux" && got[0].peer.PID == 0 {
		t.Fatalf("the rule did not see the pid: %+v", got[0])
	}
}

func TestAHostRuleRefusesWithItsOwnReasonBeforeTheHandlerRunsAndTheObserverHearsIt(t *testing.T) {
	var mu sync.Mutex
	var heard []socket.Refused
	r := start(t, socket.Config{
		Allow:     socket.AllowFunc(func(socket.Peer, string) string { return "call_graph" }),
		OnRefused: func(f socket.Refused) { mu.Lock(); heard = append(heard, f); mu.Unlock() },
	})
	_, err := r.ask(t)
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("code %v; want permission_denied (%v)", connect.CodeOf(err), err)
	}
	if reason, _ := refusal.Reason(err); reason != "call_graph" {
		t.Fatalf("reason %q; want call_graph (%v)", reason, err)
	}
	// The message is not read either: a body that is no message is still a refusal, not invalid_argument.
	req, _ := http.NewRequest(http.MethodPost, "http://unix"+procedure, strings.NewReader("\xff\xff not a message"))
	req.Header.Set("Content-Type", "application/proto")
	req.Header.Set("Connect-Protocol-Version", "1")
	res, err := r.client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d; want 403", res.StatusCode)
	}
	if r.calls.Load() != 0 {
		t.Fatal("a refused call reached the handler")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(heard) != 2 || heard[0].Reason != "call_graph" || !heard[0].Known || heard[0].Peer.UID != me() || heard[0].Procedure != procedure {
		t.Fatalf("the observer heard %+v", heard)
	}
}

func TestTheObserverHearsAUIDOutsideTheSetAndNotAnAdmittedCall(t *testing.T) {
	var mu sync.Mutex
	var heard []socket.Refused
	observe := func(f socket.Refused) { mu.Lock(); heard = append(heard, f); mu.Unlock() }
	r := start(t, socket.Config{Allow: socket.AllowUIDs(me() + 1), OnRefused: observe})
	_, err := r.ask(t)
	wantCallerRefusal(t, err)
	ok := start(t, socket.Config{OnRefused: observe})
	if _, err := ok.ask(t); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(heard) != 1 || heard[0].Reason != socket.ReasonCaller {
		t.Fatalf("the observer heard %+v", heard)
	}
}

func TestARuleIsAskedByAdmitsWithTheUID(t *testing.T) {
	a := socket.AllowFunc(func(p socket.Peer, _ string) string {
		if p.UID == 7 {
			return ""
		}
		return "x"
	})
	if !a.Admits(7, 5) || a.Admits(5, 5) {
		t.Fatal("the rule decides, and the own uid is not implied")
	}
	if socket.AllowFunc(nil).Admits(5, 5) {
		t.Fatal("a nil rule admits nobody")
	}
}

// deadlineLeft serves a handler that answers, in Reason, whether its context has a deadline and, in FieldPath, how far off it is.
func deadlineLeft(t *testing.T, cfg socket.Config) *connect.Client[commonv1alpha1.Refusal, commonv1alpha1.Refusal] {
	t.Helper()
	cfg.Socket = filepath.Join(shortDir(t), "d")
	h := connect.NewUnaryHandler(procedure, func(ctx context.Context, _ *connect.Request[commonv1alpha1.Refusal]) (*connect.Response[commonv1alpha1.Refusal], error) {
		out := &commonv1alpha1.Refusal{Reason: "none"}
		if d, ok := ctx.Deadline(); ok {
			out.Reason = "set"
			out.FieldPath = time.Until(d).Round(time.Second).String()
		}
		return connect.NewResponse(out), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- socket.Serve(ctx, cfg, procedure, h) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
	hc := socket.Client(cfg.Socket)
	c := connect.NewClient[commonv1alpha1.Refusal, commonv1alpha1.Refusal](hc, "http://unix"+procedure)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := c.CallUnary(context.Background(), connect.NewRequest(&commonv1alpha1.Refusal{})); err == nil {
			return c
		} else if time.Now().After(deadline) {
			t.Fatalf("the server did not answer: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTheDefaultDeadlineIsGivenToACallThatCarriesNone(t *testing.T) {
	// With no default, a call without a deadline has none.
	c := deadlineLeft(t, socket.Config{})
	res, err := c.CallUnary(context.Background(), connect.NewRequest(&commonv1alpha1.Refusal{}))
	if err != nil || res.Msg.GetReason() != "none" {
		t.Fatalf("no default: %v %v", res, err)
	}
	// With one, a call without a deadline gets it.
	c = deadlineLeft(t, socket.Config{DefaultDeadline: 30 * time.Second})
	res, err = c.CallUnary(context.Background(), connect.NewRequest(&commonv1alpha1.Refusal{}))
	if err != nil || res.Msg.GetReason() != "set" || res.Msg.GetFieldPath() != "30s" {
		t.Fatalf("the default: %v %v", res, err)
	}
	// A call that carries a deadline keeps it, shorter or longer.
	for _, own := range []time.Duration{5 * time.Second, 90 * time.Second} {
		ctx, cancel := context.WithTimeout(context.Background(), own)
		res, err = c.CallUnary(ctx, connect.NewRequest(&commonv1alpha1.Refusal{}))
		cancel()
		if err != nil || res.Msg.GetReason() != "set" || res.Msg.GetFieldPath() != own.String() {
			t.Fatalf("own deadline %v: %v %v", own, res, err)
		}
	}
}
