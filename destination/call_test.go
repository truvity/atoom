package destination_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/common/call"
	"github.com/truvity/atoom/common/call/calltest"
	destinationv1alpha1 "github.com/truvity/atoom/destination/gen/atoom/destination/v1alpha1"
	"github.com/truvity/atoom/destination/gen/atoom/destination/v1alpha1/destinationv1alpha1connect"
)

// The client of this port carries the call: the deadline of the context of a call goes out as Connect-Timeout-Ms and the correlation id
// of the context as its header (docs/call-context.md), for every call of the service, unary and streaming, against a server that records
// what arrived.
func TestTheClientCarriesTheDeadlineAndTheCorrelationID(t *testing.T) {
	rec := calltest.New(t)
	c := destinationv1alpha1connect.NewDestinationServiceClient(rec.HTTP(), rec.URL(), call.ClientOptions()...)
	ctx, cancel := context.WithTimeout(call.WithCorrelationID(context.Background(), "destination-1"), 5*time.Second)
	defer cancel()

	_, _ = c.Put(ctx, connect.NewRequest(&destinationv1alpha1.PutRequest{}))
	rec.AssertCarried(t, "destination-1", 5*time.Second)

	_, _ = c.Delete(ctx, connect.NewRequest(&destinationv1alpha1.DeleteRequest{}))
	rec.AssertCarried(t, "destination-1", 5*time.Second)

	_, _ = c.List(ctx, connect.NewRequest(&destinationv1alpha1.ListRequest{}))
	rec.AssertCarried(t, "destination-1", 5*time.Second)

	_, _ = c.Purge(ctx, connect.NewRequest(&destinationv1alpha1.PurgeRequest{}))
	rec.AssertCarried(t, "destination-1", 5*time.Second)

	_, _ = c.ReadBack(ctx, connect.NewRequest(&destinationv1alpha1.ReadBackRequest{}))
	rec.AssertCarried(t, "destination-1", 5*time.Second)
	if n := len(rec.Sent()); n != 5 {
		t.Fatalf("%d calls reached the recorder, want 5", n)
	}

	// Without either, nothing goes out.
	_, _ = c.Put(context.Background(), connect.NewRequest(&destinationv1alpha1.PutRequest{}))
	rec.AssertBare(t)
}
