package blob_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	blobv1alpha1 "github.com/truvity/atoom/blob/gen/atoom/blob/v1alpha1"
	"github.com/truvity/atoom/blob/gen/atoom/blob/v1alpha1/blobv1alpha1connect"
	"github.com/truvity/atoom/common/call"
	"github.com/truvity/atoom/common/call/calltest"
)

// The client of this port carries the call: the deadline of the context of a call goes out as Connect-Timeout-Ms and the correlation id
// of the context as its header (docs/call-context.md), for every call of the service, unary and streaming, against a server that records
// what arrived.
func TestTheClientCarriesTheDeadlineAndTheCorrelationID(t *testing.T) {
	rec := calltest.New(t)
	c := blobv1alpha1connect.NewBlobServiceClient(rec.HTTP(), rec.URL(), call.ClientOptions()...)
	ctx, cancel := context.WithTimeout(call.WithCorrelationID(context.Background(), "blob-1"), 5*time.Second)
	defer cancel()

	_, _ = c.Create(ctx, connect.NewRequest(&blobv1alpha1.CreateRequest{}))
	rec.AssertCarried(t, "blob-1", 5*time.Second)

	_, _ = c.Put(ctx, connect.NewRequest(&blobv1alpha1.PutRequest{}))
	rec.AssertCarried(t, "blob-1", 5*time.Second)

	_, _ = c.Get(ctx, connect.NewRequest(&blobv1alpha1.BlobGetRequest{}))
	rec.AssertCarried(t, "blob-1", 5*time.Second)

	_, _ = c.List(ctx, connect.NewRequest(&blobv1alpha1.BlobListRequest{}))
	rec.AssertCarried(t, "blob-1", 5*time.Second)
	if n := len(rec.Sent()); n != 4 {
		t.Fatalf("%d calls reached the recorder, want 4", n)
	}

	// Without either, nothing goes out.
	_, _ = c.Create(context.Background(), connect.NewRequest(&blobv1alpha1.CreateRequest{}))
	rec.AssertBare(t)
}
