package route_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"

	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
	"github.com/truvity/atoom/key/gen/atoom/key/v1alpha1/keyv1alpha1connect"
	"github.com/truvity/atoom/key/route"
)

// driver is a stand-in of one key driver: it serves its purposes and says which it is in every answer. A handler of the contract is a
// client of it too (the unary methods have one signature), so it needs no transport.
type driver struct {
	keyv1alpha1connect.UnimplementedKeyStoreServiceHandler
	name       string
	purposes   []string
	algorithms []string
	attests    bool
	calls      int
}

func (d *driver) Capabilities(context.Context, *connect.Request[keyv1alpha1.CapabilitiesRequest]) (*connect.Response[keyv1alpha1.CapabilitiesResponse], error) {
	d.calls++
	out := &keyv1alpha1.CapabilitiesResponse{Store: d.name, Algorithms: d.algorithms, Attests: d.attests}
	for _, p := range d.purposes {
		out.Purposes = append(out.Purposes, &keyv1alpha1.PurposeInfo{Name: p, Algorithm: "ES256", Operations: []string{"sign"}})
	}
	return connect.NewResponse(out), nil
}

func (d *driver) Sign(_ context.Context, req *connect.Request[keyv1alpha1.SignRequest]) (*connect.Response[keyv1alpha1.SignResponse], error) {
	return connect.NewResponse(&keyv1alpha1.SignResponse{KeyId: d.name + "/" + req.Msg.GetPurpose()}), nil
}

func (d *driver) PublicKey(_ context.Context, req *connect.Request[keyv1alpha1.PublicKeyRequest]) (*connect.Response[keyv1alpha1.PublicKeyResponse], error) {
	return connect.NewResponse(&keyv1alpha1.PublicKeyResponse{KeyId: d.name + "/" + req.Msg.GetPurpose()}), nil
}

func (d *driver) Ensure(_ context.Context, req *connect.Request[keyv1alpha1.EnsureRequest]) (*connect.Response[keyv1alpha1.EnsureResponse], error) {
	return connect.NewResponse(&keyv1alpha1.EnsureResponse{SpkiDer: []byte(d.name + "/" + req.Msg.GetPurpose())}), nil
}

func TestEachPurposeGoesToItsDriver(t *testing.T) {
	ctx := context.Background()
	file := &driver{name: "software", purposes: []string{"client-assertion", "spare"}, algorithms: []string{"ES256", "ES384", "RSA"}, attests: true}
	kms := &driver{name: "aws-kms", purposes: []string{"identity-header", "wrapping", "client-assertion"}, algorithms: []string{"ES384", "ES256"}}
	c, err := route.New(map[string]keyv1alpha1connect.KeyStoreServiceClient{"client-assertion": file, "identity-header": kms, "wrapping": kms})
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{"client-assertion": "software", "identity-header": "aws-kms", "wrapping": "aws-kms"} {
		s, err := c.Sign(ctx, connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: p}))
		if err != nil || s.Msg.GetKeyId() != want+"/"+p {
			t.Fatalf("Sign %s: %v %v", p, s, err)
		}
		k, err := c.PublicKey(ctx, connect.NewRequest(&keyv1alpha1.PublicKeyRequest{Purpose: p}))
		if err != nil || k.Msg.GetKeyId() != want+"/"+p {
			t.Fatalf("PublicKey %s: %v %v", p, k, err)
		}
		e, err := c.Ensure(ctx, connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: p}))
		if err != nil || string(e.Msg.GetSpkiDer()) != want+"/"+p {
			t.Fatalf("Ensure %s: %v %v", p, e, err)
		}
	}
	caps, err := c.Capabilities(ctx, connect.NewRequest(&keyv1alpha1.CapabilitiesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range caps.Msg.GetPurposes() {
		names = append(names, p.GetName())
	}
	// Each driver's purposes that are routed to it, and no other: the file driver's spare and KMS's client-assertion are not.
	if got := len(names); got != 3 || names[0] != "client-assertion" || names[1] != "identity-header" || names[2] != "wrapping" {
		t.Fatalf("purposes %v", names)
	}
	if caps.Msg.GetStore() != route.Store || caps.Msg.GetAttests() {
		t.Fatalf("store %q, attests %v", caps.Msg.GetStore(), caps.Msg.GetAttests())
	}
	if a := caps.Msg.GetAlgorithms(); len(a) != 2 || a[0] != "ES256" || a[1] != "ES384" {
		t.Fatalf("algorithms %v, want those of both drivers", a)
	}
	if file.calls != 1 || kms.calls != 1 {
		t.Fatalf("Capabilities asked the drivers %d and %d times, want once each", file.calls, kms.calls)
	}
}

func TestOneDriverIsAskedOnceAndNamedAsItIs(t *testing.T) {
	d := &driver{name: "software", purposes: []string{"a", "b"}, algorithms: []string{"ES256"}, attests: true}
	c, err := route.New(map[string]keyv1alpha1connect.KeyStoreServiceClient{"a": d, "b": d})
	if err != nil {
		t.Fatal(err)
	}
	caps, err := c.Capabilities(context.Background(), connect.NewRequest(&keyv1alpha1.CapabilitiesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if d.calls != 1 || caps.Msg.GetStore() != "software" || !caps.Msg.GetAttests() || len(caps.Msg.GetPurposes()) != 2 {
		t.Fatalf("calls %d, %v", d.calls, caps.Msg)
	}
}

func TestWhatIsNotRouted(t *testing.T) {
	ctx := context.Background()
	if _, err := route.New(nil); err == nil {
		t.Fatal("New took no purpose")
	}
	if _, err := route.New(map[string]keyv1alpha1connect.KeyStoreServiceClient{"a": nil}); err == nil {
		t.Fatal("New took a purpose with no client")
	}
	c, err := route.New(map[string]keyv1alpha1connect.KeyStoreServiceClient{"a": &driver{name: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Sign(ctx, connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: "b"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a purpose with no driver: %v", err)
	}
	_, err = c.VerifyAttestation(ctx, connect.NewRequest(&keyv1alpha1.VerifyAttestationRequest{}))
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("VerifyAttestation: %v", err)
	}
	var ce *connect.Error
	if _, err := c.BeginPinRelease(ctx, connect.NewRequest(&keyv1alpha1.BeginPinReleaseRequest{})); !errors.As(err, &ce) || ce.Code() != connect.CodeUnimplemented {
		t.Fatalf("BeginPinRelease: %v", err)
	}
}
