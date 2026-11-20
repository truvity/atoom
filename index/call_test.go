package index_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/common/call"
	"github.com/truvity/atoom/common/call/calltest"
	indexv1alpha1 "github.com/truvity/atoom/index/gen/atoom/index/v1alpha1"
	"github.com/truvity/atoom/index/gen/atoom/index/v1alpha1/indexv1alpha1connect"
)

// The clients of this port carry the call: the deadline of the context of a call goes out as Connect-Timeout-Ms and the correlation id
// of the context as its header (docs/call-context.md), for every call of both services, against a server that records what arrived.
func TestTheClientsCarryTheDeadlineAndTheCorrelationID(t *testing.T) {
	rec := calltest.New(t)
	w := indexv1alpha1connect.NewIndexServiceClient(rec.HTTP(), rec.URL(), call.ClientOptions()...)
	r := indexv1alpha1connect.NewIndexReadServiceClient(rec.HTTP(), rec.URL(), call.ClientOptions()...)
	ctx, cancel := context.WithTimeout(call.WithCorrelationID(context.Background(), "index-1"), 5*time.Second)
	defer cancel()

	_, _ = w.GetCursor(ctx, connect.NewRequest(&indexv1alpha1.GetCursorRequest{}))
	rec.AssertCarried(t, "index-1", 5*time.Second)
	_, _ = w.Apply(ctx, connect.NewRequest(&indexv1alpha1.ApplyRequest{}))
	rec.AssertCarried(t, "index-1", 5*time.Second)
	_, _ = r.Search(ctx, connect.NewRequest(&indexv1alpha1.SearchRequest{}))
	rec.AssertCarried(t, "index-1", 5*time.Second)
	if n := len(rec.Sent()); n != 3 {
		t.Fatalf("%d calls reached the recorder, want 3", n)
	}

	// Without either, nothing goes out.
	_, _ = w.GetCursor(context.Background(), connect.NewRequest(&indexv1alpha1.GetCursorRequest{}))
	rec.AssertBare(t)
}
