package source_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/common/call"
	"github.com/truvity/atoom/common/call/calltest"
	sourcev1alpha1 "github.com/truvity/atoom/source/gen/atoom/source/v1alpha1"
	"github.com/truvity/atoom/source/gen/atoom/source/v1alpha1/sourcev1alpha1connect"
)

// The client of this port carries the call: the deadline of the context of a call goes out as Connect-Timeout-Ms and the correlation id
// of the context as its header (docs/call-context.md), for every call of the service, unary and streaming, against a server that records
// what arrived.
func TestTheClientCarriesTheDeadlineAndTheCorrelationID(t *testing.T) {
	rec := calltest.New(t)
	c := sourcev1alpha1connect.NewSourceServiceClient(rec.HTTP(), rec.URL(), call.ClientOptions()...)
	ctx, cancel := context.WithTimeout(call.WithCorrelationID(context.Background(), "source-1"), 5*time.Second)
	defer cancel()

	_, _ = c.Capabilities(ctx, connect.NewRequest(&sourcev1alpha1.CapabilitiesRequest{}))
	rec.AssertCarried(t, "source-1", 5*time.Second)

	_, _ = c.Read(ctx, connect.NewRequest(&sourcev1alpha1.ReadRequest{}))
	rec.AssertCarried(t, "source-1", 5*time.Second)

	_, _ = c.Watch(ctx, connect.NewRequest(&sourcev1alpha1.SourceWatchRequest{}))
	rec.AssertCarried(t, "source-1", 5*time.Second)
	if n := len(rec.Sent()); n != 3 {
		t.Fatalf("%d calls reached the recorder, want 3", n)
	}

	// Without either, nothing goes out.
	_, _ = c.Capabilities(context.Background(), connect.NewRequest(&sourcev1alpha1.CapabilitiesRequest{}))
	rec.AssertBare(t)
}
