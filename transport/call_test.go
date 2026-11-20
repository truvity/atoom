package transport_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/common/call"
	"github.com/truvity/atoom/common/call/calltest"
	transportv1alpha1 "github.com/truvity/atoom/transport/gen/atoom/transport/v1alpha1"
	"github.com/truvity/atoom/transport/gen/atoom/transport/v1alpha1/transportv1alpha1connect"
)

// The client of this port carries the call: the deadline of the context of a call goes out as Connect-Timeout-Ms and the correlation id
// of the context as its header (docs/call-context.md), for every call of the service, unary and streaming, against a server that records
// what arrived.
func TestTheClientCarriesTheDeadlineAndTheCorrelationID(t *testing.T) {
	rec := calltest.New(t)
	c := transportv1alpha1connect.NewTransportServiceClient(rec.HTTP(), rec.URL(), call.ClientOptions()...)
	ctx, cancel := context.WithTimeout(call.WithCorrelationID(context.Background(), "transport-1"), 5*time.Second)
	defer cancel()

	_, _ = c.Emit(ctx, connect.NewRequest(&transportv1alpha1.EmitRequest{}))
	rec.AssertCarried(t, "transport-1", 5*time.Second)

	_, _ = c.Receive(ctx, connect.NewRequest(&transportv1alpha1.ReceiveRequest{}))
	rec.AssertCarried(t, "transport-1", 5*time.Second)

	_, _ = c.Ack(ctx, connect.NewRequest(&transportv1alpha1.AckRequest{}))
	rec.AssertCarried(t, "transport-1", 5*time.Second)
	if n := len(rec.Sent()); n != 3 {
		t.Fatalf("%d calls reached the recorder, want 3", n)
	}

	// Without either, nothing goes out.
	_, _ = c.Emit(context.Background(), connect.NewRequest(&transportv1alpha1.EmitRequest{}))
	rec.AssertBare(t)
}
