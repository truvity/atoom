package call_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/common/call"
	"github.com/truvity/atoom/common/call/calltest"
	commonv1alpha1 "github.com/truvity/atoom/common/gen/atoom/common/v1alpha1"
)

// The messages of the test service are Refusal, the one message every atoom library has. The handler answers what it saw: the id in
// reason and the milliseconds left of its deadline in field_path ("none" for either that is absent).
const (
	see   = "/atoom.test.v1.Call/See"
	relay = "/atoom.test.v1.Call/Relay"
	slow  = "/atoom.test.v1.Call/Slow"
)

type client = *connect.Client[commonv1alpha1.Refusal, commonv1alpha1.Refusal]

func newClient(hc connect.HTTPClient, url, procedure string) client {
	return connect.NewClient[commonv1alpha1.Refusal, commonv1alpha1.Refusal](hc, url+procedure, call.ClientOptions()...)
}

func saw(ctx context.Context) *commonv1alpha1.Refusal {
	out := &commonv1alpha1.Refusal{Reason: "none", FieldPath: "none"}
	if id := call.CorrelationID(ctx); id != "" {
		out.Reason = id
	}
	if d, ok := ctx.Deadline(); ok {
		out.FieldPath = strconv.FormatInt(time.Until(d).Milliseconds(), 10)
	}
	return out
}

func leftMS(t *testing.T, r *commonv1alpha1.Refusal) int64 {
	t.Helper()
	n, err := strconv.ParseInt(r.GetFieldPath(), 10, 64)
	if err != nil {
		t.Fatalf("the handler holds no deadline (%q)", r.GetFieldPath())
	}
	return n
}

// serve mounts the handlers of the test service on a server wrapped by Inherit, as socket.Serve does. next is the server that Relay
// calls on.
func serve(t *testing.T, next string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(see, connect.NewUnaryHandler(see, func(ctx context.Context, _ *connect.Request[commonv1alpha1.Refusal]) (*connect.Response[commonv1alpha1.Refusal], error) {
		return connect.NewResponse(saw(ctx)), nil
	}))
	mux.Handle(slow, connect.NewUnaryHandler(slow, func(ctx context.Context, _ *connect.Request[commonv1alpha1.Refusal]) (*connect.Response[commonv1alpha1.Refusal], error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return connect.NewResponse(&commonv1alpha1.Refusal{}), nil
		}
	}))
	mux.Handle(relay, connect.NewUnaryHandler(relay, func(ctx context.Context, _ *connect.Request[commonv1alpha1.Refusal]) (*connect.Response[commonv1alpha1.Refusal], error) {
		// The handler calls on with its own context, which is all the plumbing there is.
		res, err := newClient(http.DefaultClient, next, see).CallUnary(ctx, connect.NewRequest(&commonv1alpha1.Refusal{}))
		if err != nil {
			return nil, err
		}
		return res, nil
	}))
	srv := httptest.NewServer(call.Inherit(mux))
	t.Cleanup(srv.Close)
	return srv
}

func ask(ctx context.Context, srv *httptest.Server, procedure string) (*commonv1alpha1.Refusal, error) {
	res, err := newClient(srv.Client(), srv.URL, procedure).CallUnary(ctx, connect.NewRequest(&commonv1alpha1.Refusal{}))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

func TestTheClientSendsTheIDAndTheDeadlineOfItsContext(t *testing.T) {
	srv := serve(t, "")
	ctx, cancel := context.WithTimeout(call.WithCorrelationID(context.Background(), "req-42"), 5*time.Second)
	defer cancel()
	got, err := ask(ctx, srv, see)
	if err != nil {
		t.Fatal(err)
	}
	if got.GetReason() != "req-42" {
		t.Errorf("the handler saw the id %q, want req-42", got.GetReason())
	}
	if n := leftMS(t, got); n < 3000 || n > 5000 {
		t.Errorf("the handler has %d ms left of 5000", n)
	}
}

func TestACallWithNeitherCarriesNeither(t *testing.T) {
	srv := serve(t, "")
	got, err := ask(context.Background(), srv, see)
	if err != nil {
		t.Fatal(err)
	}
	if got.GetReason() != "none" || got.GetFieldPath() != "none" {
		t.Errorf("the handler saw %q and %q, want none and none", got.GetReason(), got.GetFieldPath())
	}
}

func TestAHandlerHandsTheIDAndWhatIsLeftOfTheDeadlineToTheCallItMakes(t *testing.T) {
	b := serve(t, "")
	a := serve(t, b.URL)
	ctx, cancel := context.WithTimeout(call.WithCorrelationID(context.Background(), "chain-1"), 4*time.Second)
	defer cancel()
	got, err := ask(ctx, a, relay)
	if err != nil {
		t.Fatal(err)
	}
	if got.GetReason() != "chain-1" {
		t.Errorf("the second hop saw the id %q, want chain-1", got.GetReason())
	}
	if n := leftMS(t, got); n < 1 || n > 4000 {
		t.Errorf("the second hop has %d ms, want what is left of 4000, never more", n)
	}
}

func TestACalleeNeverExtendsTheDeadlineOfItsCaller(t *testing.T) {
	b := serve(t, "")
	a := serve(t, b.URL)
	// The handler of the first hop runs with a deadline of 2 s and calls on with its own context: the second hop never has more than
	// what is left, because a context can only be narrowed.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := ask(ctx, a, relay)
	if err != nil {
		t.Fatal(err)
	}
	if n := leftMS(t, got); n > 2000 {
		t.Errorf("the second hop has %d ms of 2000", n)
	}
}

func TestTheServerHonoursTheDeadlineOfTheCaller(t *testing.T) {
	srv := serve(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := ask(ctx, srv, slow)
	if connect.CodeOf(err) != connect.CodeDeadlineExceeded {
		t.Fatalf("code %v, want deadline_exceeded (%v)", connect.CodeOf(err), err)
	}
	if time.Since(started) > 2*time.Second {
		t.Error("the call took longer than its deadline")
	}
}

func TestAnInvalidIDIsDroppedByTheServerAndNeverSent(t *testing.T) {
	srv := serve(t, "")
	for _, bad := range []string{"not an id", "a/b", "é", strings.Repeat("x", 65), "a\tb"} {
		// On the wire: a header with a bad id.
		req := connect.NewRequest(&commonv1alpha1.Refusal{})
		req.Header().Set(call.HeaderCorrelation, bad)
		c := connect.NewClient[commonv1alpha1.Refusal, commonv1alpha1.Refusal](srv.Client(), srv.URL+see)
		res, err := c.CallUnary(context.Background(), req)
		if err != nil {
			// A header value that HTTP itself refuses never reaches the server: nothing was read either.
			continue
		}
		if res.Msg.GetReason() != "none" {
			t.Errorf("the server read the bad id %q as %q", bad, res.Msg.GetReason())
		}
		// In a context: dropped when it is set.
		if call.CorrelationID(call.WithCorrelationID(context.Background(), bad)) != "" {
			t.Errorf("the id %q was kept", bad)
		}
	}
	if !call.ValidCorrelationID(strings.Repeat("x", 64)) || call.ValidCorrelationID(strings.Repeat("x", 65)) || call.ValidCorrelationID("") {
		t.Error("the bounds are 1 to 64 characters")
	}
	if !call.ValidCorrelationID("0f8fad5b-d9cb-469f-a165-70867728950e") || !call.ValidCorrelationID("Req_1-A") {
		t.Error("a UUID and a token are ids")
	}
}

func TestAStreamingCallCarriesTheIDAndTheDeadlineToo(t *testing.T) {
	rec := calltest.New(t)
	c := connect.NewClient[commonv1alpha1.Refusal, commonv1alpha1.Refusal](rec.HTTP(), rec.URL()+"/atoom.test.v1.Call/Stream", call.ClientOptions()...)
	ctx, cancel := context.WithTimeout(call.WithCorrelationID(context.Background(), "stream-1"), 5*time.Second)
	defer cancel()
	_, _ = c.CallServerStream(ctx, connect.NewRequest(&commonv1alpha1.Refusal{}))
	rec.AssertCarried(t, "stream-1", 5*time.Second)
}

func TestTheDefaultDeadlineIsGivenToAClientCallThatHasNone(t *testing.T) {
	srv := serve(t, "")
	opts := append(call.ClientOptions(), connect.WithInterceptors(call.DefaultDeadline(3*time.Second)))
	c := connect.NewClient[commonv1alpha1.Refusal, commonv1alpha1.Refusal](srv.Client(), srv.URL+see, opts...)
	res, err := c.CallUnary(context.Background(), connect.NewRequest(&commonv1alpha1.Refusal{}))
	if err != nil {
		t.Fatal(err)
	}
	if n := leftMS(t, res.Msg); n < 1000 || n > 3000 {
		t.Errorf("the handler has %d ms left of the default 3000", n)
	}
	// A call that has a deadline keeps it, longer or shorter.
	for _, own := range []time.Duration{time.Second, 10 * time.Second} {
		ctx, cancel := context.WithTimeout(context.Background(), own)
		res, err := c.CallUnary(ctx, connect.NewRequest(&commonv1alpha1.Refusal{}))
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if n := leftMS(t, res.Msg); n > own.Milliseconds() || n < own.Milliseconds()-2000 {
			t.Errorf("own %v: the handler has %d ms left", own, n)
		}
	}
	// A zero default gives nothing.
	c = connect.NewClient[commonv1alpha1.Refusal, commonv1alpha1.Refusal](srv.Client(), srv.URL+see, connect.WithInterceptors(call.DefaultDeadline(0)))
	res, err = c.CallUnary(context.Background(), connect.NewRequest(&commonv1alpha1.Refusal{}))
	if err != nil || res.Msg.GetFieldPath() != "none" {
		t.Fatalf("a zero default: %v %v", res, err)
	}
}

func TestTheDefaultDeadlineOnAHandlerCoversACallThatHasNone(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(see, connect.NewUnaryHandler(see, func(ctx context.Context, _ *connect.Request[commonv1alpha1.Refusal]) (*connect.Response[commonv1alpha1.Refusal], error) {
		return connect.NewResponse(saw(ctx)), nil
	}, connect.WithInterceptors(call.DefaultDeadline(2*time.Second))))
	srv := httptest.NewServer(call.Inherit(mux))
	t.Cleanup(srv.Close)
	got, err := ask(context.Background(), srv, see)
	if err != nil {
		t.Fatal(err)
	}
	if n := leftMS(t, got); n < 500 || n > 2000 {
		t.Errorf("the handler has %d ms left of the default 2000", n)
	}
}

// Step B14: for one release a server also reads the name the header had before 0.7.0, after the new one; a client sends the new
// name only.
func TestTheServerReadsTheLegacyHeaderForOneReleaseAndTheNewNameFirst(t *testing.T) {
	srv := serve(t, "")
	c := connect.NewClient[commonv1alpha1.Refusal, commonv1alpha1.Refusal](srv.Client(), srv.URL+see)
	for name, tc := range map[string]struct{ newID, oldID, want string }{
		"legacy only":       {"", "old-name", "old-name"},
		"both":              {"new-name", "old-name", "new-name"},
		"an invalid new id": {"not an id", "old-name", "old-name"},
	} {
		req := connect.NewRequest(&commonv1alpha1.Refusal{})
		if tc.newID != "" {
			req.Header().Set(call.HeaderCorrelation, tc.newID)
		}
		req.Header().Set(call.HeaderCorrelationLegacy, tc.oldID)
		res, err := c.CallUnary(context.Background(), req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := res.Msg.GetReason(); got != tc.want {
			t.Errorf("%s: read %q, want %q", name, got, tc.want)
		}
	}
	if call.HeaderCorrelation != "Atoom-Correlation-Id" {
		t.Errorf("a client sends %q", call.HeaderCorrelation)
	}
}
