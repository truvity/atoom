package conformance_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/common/call"
	"github.com/truvity/atoom/common/call/calltest"
	"github.com/truvity/atoom/key/conformance"
	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
)

// The client of the kit carries the call, over h2c and over HTTP/1.1, for the unary calls of the key-store service: the deadline of the
// context as Connect-Timeout-Ms and the correlation id as its header (docs/call-context.md).
func TestTheClientOfTheKitCarriesTheDeadlineAndTheCorrelationID(t *testing.T) {
	dir, err := os.MkdirTemp("", "k")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	rec := calltest.NewOnSocket(t, filepath.Join(dir, "r"))
	for name, dial := range map[string]func(string) conformance.Client{"h2c": conformance.Dial, "http1": conformance.DialHTTP1} {
		c := dial(filepath.Join(dir, "r"))
		ctx, cancel := context.WithTimeout(call.WithCorrelationID(context.Background(), "key-"+name), 5*time.Second)
		for _, one := range []func() error{
			func() error {
				_, err := c.Capabilities(ctx, connect.NewRequest(&keyv1alpha1.CapabilitiesRequest{}))
				return err
			},
			func() error { _, err := c.Ensure(ctx, connect.NewRequest(&keyv1alpha1.EnsureRequest{})); return err },
			func() error { _, err := c.Sign(ctx, connect.NewRequest(&keyv1alpha1.SignRequest{})); return err },
			func() error {
				_, err := c.PublicKey(ctx, connect.NewRequest(&keyv1alpha1.PublicKeyRequest{}))
				return err
			},
			func() error { _, err := c.Ecdh(ctx, connect.NewRequest(&keyv1alpha1.EcdhRequest{})); return err },
		} {
			_ = one()
			rec.AssertCarried(t, "key-"+name, 5*time.Second)
		}
		cancel()
		// Without either, nothing goes out.
		_, _ = c.Capabilities(context.Background(), connect.NewRequest(&keyv1alpha1.CapabilitiesRequest{}))
		rec.AssertBare(t)
	}
}
