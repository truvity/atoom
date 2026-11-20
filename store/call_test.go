package store_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/common/call"
	"github.com/truvity/atoom/common/call/calltest"
	storev1alpha1 "github.com/truvity/atoom/store/gen/atoom/store/v1alpha1"
	"github.com/truvity/atoom/store/gen/atoom/store/v1alpha1/storev1alpha1connect"
)

// The client of this port carries the call: the deadline of the context of a call goes out as Connect-Timeout-Ms and the correlation id
// of the context as its header (docs/call-context.md), for every call of the service, unary and streaming, against a server that records
// what arrived.
func TestTheClientCarriesTheDeadlineAndTheCorrelationID(t *testing.T) {
	rec := calltest.New(t)
	c := storev1alpha1connect.NewItemServiceClient(rec.HTTP(), rec.URL(), call.ClientOptions()...)
	ctx, cancel := context.WithTimeout(call.WithCorrelationID(context.Background(), "store-1"), 5*time.Second)
	defer cancel()

	_, _ = c.Capabilities(ctx, connect.NewRequest(&storev1alpha1.CapabilitiesRequest{}))
	rec.AssertCarried(t, "store-1", 5*time.Second)

	_, _ = c.Get(ctx, connect.NewRequest(&storev1alpha1.ItemGetRequest{}))
	rec.AssertCarried(t, "store-1", 5*time.Second)

	_, _ = c.Query(ctx, connect.NewRequest(&storev1alpha1.QueryRequest{}))
	rec.AssertCarried(t, "store-1", 5*time.Second)

	_, _ = c.Transact(ctx, connect.NewRequest(&storev1alpha1.TransactRequest{}))
	rec.AssertCarried(t, "store-1", 5*time.Second)

	_, _ = c.TailLog(ctx, connect.NewRequest(&storev1alpha1.TailLogRequest{}))
	rec.AssertCarried(t, "store-1", 5*time.Second)
	if n := len(rec.Sent()); n != 5 {
		t.Fatalf("%d calls reached the recorder, want 5", n)
	}

	// Without either, nothing goes out.
	_, _ = c.Capabilities(context.Background(), connect.NewRequest(&storev1alpha1.CapabilitiesRequest{}))
	rec.AssertBare(t)
}
