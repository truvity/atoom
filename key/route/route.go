// Package route is a client of the key port that sends each call to the driver that holds its purpose: one client in front of several
// drivers, for a host that keeps each purpose on a driver of its own (a signing key in AWS KMS, another in a file) and gives a block one
// client (seal.Options.Keys). It is plain plumbing over the generated client: every call is still a call of the contract, and a driver
// sees only the purposes routed to it.
//
//	keys, err := route.New(map[string]keyv1alpha1connect.KeyStoreServiceClient{
//	    "wrapping": kms, "identity-header": kms, "client-assertion": software,
//	})
//
// A call that names a purpose (Ensure, Sign, PublicKey, Ecdh, Attest) goes to the client of that purpose; a purpose with no client is
// invalid_argument, the error a driver gives for a purpose it does not serve. Capabilities asks every distinct client once and answers
// with the purposes each serves of those routed to it, so a block that checks its declaration against Capabilities (seal.New) sees exactly
// what it may call. The calls that name no purpose (VerifyAttestation and the PIN release) belong to one card driver and are not routed:
// they are unimplemented here, and a host that needs them calls that driver's own client.
package route

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"

	"connectrpc.com/connect"

	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
	"github.com/truvity/atoom/key/gen/atoom/key/v1alpha1/keyv1alpha1connect"
)

// Store is the store name Capabilities answers when the purposes are on drivers of more than one store.
const Store = "routed"

// Client is the routing client. It is safe for concurrent use.
type Client struct {
	byPurpose map[string]keyv1alpha1connect.KeyStoreServiceClient
	distinct  []keyv1alpha1connect.KeyStoreServiceClient
}

var _ keyv1alpha1connect.KeyStoreServiceClient = (*Client)(nil)

// New returns a client that sends each purpose's calls to its client. Every purpose needs a client; one client may serve several
// purposes, and is then asked once by Capabilities.
func New(byPurpose map[string]keyv1alpha1connect.KeyStoreServiceClient) (*Client, error) {
	if len(byPurpose) == 0 {
		return nil, errors.New("route: no purpose")
	}
	c := &Client{byPurpose: make(map[string]keyv1alpha1connect.KeyStoreServiceClient, len(byPurpose))}
	names := make([]string, 0, len(byPurpose))
	for name := range byPurpose {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		k := byPurpose[name]
		if name == "" || k == nil {
			return nil, fmt.Errorf("route: the purpose %q has no client", name)
		}
		c.byPurpose[name] = k
		if !slices.Contains(c.distinct, k) {
			c.distinct = append(c.distinct, k)
		}
	}
	return c, nil
}

func (c *Client) to(name string) (keyv1alpha1connect.KeyStoreServiceClient, error) {
	k, ok := c.byPurpose[name]
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("route: no driver holds the purpose"))
	}
	return k, nil
}

// Capabilities asks every distinct client and answers with the purposes each serves of those routed to it, in the order of the
// purposes' names. The store is the one store of every client, or Store; the algorithms are those that every client supports; attests
// is true when every client attests.
func (c *Client) Capabilities(ctx context.Context, req *connect.Request[keyv1alpha1.CapabilitiesRequest]) (*connect.Response[keyv1alpha1.CapabilitiesResponse], error) {
	out := &keyv1alpha1.CapabilitiesResponse{Attests: true}
	var algorithms []string
	for i, k := range c.distinct {
		res, err := k.Capabilities(ctx, connect.NewRequest(req.Msg))
		if err != nil {
			return nil, err
		}
		m := res.Msg
		switch {
		case i == 0:
			out.Store = m.GetStore()
			algorithms = slices.Clone(m.GetAlgorithms())
		case out.Store != m.GetStore():
			out.Store = Store
		}
		if i > 0 {
			algorithms = slices.DeleteFunc(algorithms, func(a string) bool { return !slices.Contains(m.GetAlgorithms(), a) })
		}
		out.Attests = out.Attests && m.GetAttests()
		for _, p := range m.GetPurposes() {
			if c.byPurpose[p.GetName()] == k {
				out.Purposes = append(out.Purposes, p)
			}
		}
	}
	out.Algorithms = algorithms
	sort.Slice(out.Purposes, func(i, j int) bool { return out.Purposes[i].GetName() < out.Purposes[j].GetName() })
	return connect.NewResponse(out), nil
}

// Ensure goes to the client of the purpose.
func (c *Client) Ensure(ctx context.Context, req *connect.Request[keyv1alpha1.EnsureRequest]) (*connect.Response[keyv1alpha1.EnsureResponse], error) {
	k, err := c.to(req.Msg.GetPurpose())
	if err != nil {
		return nil, err
	}
	return k.Ensure(ctx, req)
}

// Sign goes to the client of the purpose.
func (c *Client) Sign(ctx context.Context, req *connect.Request[keyv1alpha1.SignRequest]) (*connect.Response[keyv1alpha1.SignResponse], error) {
	k, err := c.to(req.Msg.GetPurpose())
	if err != nil {
		return nil, err
	}
	return k.Sign(ctx, req)
}

// PublicKey goes to the client of the purpose.
func (c *Client) PublicKey(ctx context.Context, req *connect.Request[keyv1alpha1.PublicKeyRequest]) (*connect.Response[keyv1alpha1.PublicKeyResponse], error) {
	k, err := c.to(req.Msg.GetPurpose())
	if err != nil {
		return nil, err
	}
	return k.PublicKey(ctx, req)
}

// Ecdh goes to the client of the purpose.
func (c *Client) Ecdh(ctx context.Context, req *connect.Request[keyv1alpha1.EcdhRequest]) (*connect.Response[keyv1alpha1.EcdhResponse], error) {
	k, err := c.to(req.Msg.GetPurpose())
	if err != nil {
		return nil, err
	}
	return k.Ecdh(ctx, req)
}

// Attest goes to the client of the purpose.
func (c *Client) Attest(ctx context.Context, req *connect.Request[keyv1alpha1.AttestRequest]) (*connect.Response[keyv1alpha1.AttestResponse], error) {
	k, err := c.to(req.Msg.GetPurpose())
	if err != nil {
		return nil, err
	}
	return k.Attest(ctx, req)
}

var errNotRouted = connect.NewError(connect.CodeUnimplemented, errors.New("route: a call that names no purpose is not routed"))

// VerifyAttestation names no purpose and is not routed.
func (c *Client) VerifyAttestation(context.Context, *connect.Request[keyv1alpha1.VerifyAttestationRequest]) (*connect.Response[keyv1alpha1.VerifyAttestationResponse], error) {
	return nil, errNotRouted
}

// BeginPinRelease names no purpose and is not routed.
func (c *Client) BeginPinRelease(context.Context, *connect.Request[keyv1alpha1.BeginPinReleaseRequest]) (*connect.Response[keyv1alpha1.BeginPinReleaseResponse], error) {
	return nil, errNotRouted
}

// SealPin names no purpose and is not routed.
func (c *Client) SealPin(context.Context, *connect.Request[keyv1alpha1.SealPinRequest]) (*connect.Response[keyv1alpha1.SealPinResponse], error) {
	return nil, errNotRouted
}

// FinishPinRelease names no purpose and is not routed.
func (c *Client) FinishPinRelease(context.Context, *connect.Request[keyv1alpha1.FinishPinReleaseRequest]) (*connect.Response[keyv1alpha1.FinishPinReleaseResponse], error) {
	return nil, errNotRouted
}
