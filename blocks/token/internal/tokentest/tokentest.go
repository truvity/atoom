// Package tokentest holds the stand-ins of the key and store ports that the tests of the token block and of its thin main use.
package tokentest

import (
	"context"
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"sync"
	"sync/atomic"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/common/refusal"
	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
	"github.com/truvity/atoom/key/gen/atoom/key/v1alpha1/keyv1alpha1connect"
	"github.com/truvity/atoom/key/keyid"
	"github.com/truvity/atoom/key/purpose"
	"github.com/truvity/atoom/store/gen/atoom/store/v1alpha1/storev1alpha1connect"
)

// Keys is a stand-in of the key port: the keys of the declared purposes in memory, made on Ensure. A test counts its calls, makes
// them fail, replaces a key behind the block, or has it sign with a key that is not the published one. The kit also runs over the real
// software driver (`just conformance`).
type Keys struct {
	keyv1alpha1connect.UnimplementedKeyStoreServiceHandler
	Store string
	Decl  []purpose.Declaration
	mu    sync.Mutex
	keys  map[string]crypto.Signer
	agree map[string]*ecdh.PrivateKey
	liar  map[string]crypto.Signer // signs in place of the purpose's key

	PublicKeys atomic.Int64
	Signs      atomic.Int64
	Fail       atomic.Bool
}

// NewKeys is a stand-in that serves the declared purposes, with no key until Ensure.
func NewKeys(decl []purpose.Declaration) *Keys {
	return &Keys{Store: "fake", Decl: decl, keys: map[string]crypto.Signer{}, agree: map[string]*ecdh.PrivateKey{}, liar: map[string]crypto.Signer{}}
}

func (f *Keys) declared(name string) (purpose.Declaration, bool) {
	for _, d := range f.Decl {
		if d.Name == name {
			return d, true
		}
	}
	return purpose.Declaration{}, false
}

// Capabilities serves the declaration.
func (f *Keys) Capabilities(context.Context, *connect.Request[keyv1alpha1.CapabilitiesRequest]) (*connect.Response[keyv1alpha1.CapabilitiesResponse], error) {
	out := &keyv1alpha1.CapabilitiesResponse{Store: f.Store, Algorithms: []string{purpose.ES384, purpose.ES256, purpose.RS256, purpose.ECDHP384}}
	for _, d := range f.Decl {
		out.Purposes = append(out.Purposes, d.Info())
	}
	return connect.NewResponse(out), nil
}

func newSigner(algorithm string) crypto.Signer {
	var k crypto.Signer
	switch algorithm {
	case purpose.ES384:
		k, _ = ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	case purpose.ES256:
		k, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case purpose.RS256:
		k, _ = rsa.GenerateKey(rand.Reader, 2048)
	}
	return k
}

func (f *Keys) spki(name string) []byte {
	if k, ok := f.agree[name]; ok {
		der, _ := x509.MarshalPKIXPublicKey(k.PublicKey())
		return der
	}
	der, _ := x509.MarshalPKIXPublicKey(f.keys[name].Public())
	return der
}

// Ensure makes the key of a declared purpose when it has none.
func (f *Keys) Ensure(_ context.Context, req *connect.Request[keyv1alpha1.EnsureRequest]) (*connect.Response[keyv1alpha1.EnsureResponse], error) {
	d, ok := f.declared(req.Msg.GetPurpose())
	if !ok || d.Algorithm != req.Msg.GetAlgorithm() {
		return nil, refusal.New(connect.CodeInvalidArgument, "purpose", "purpose")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	created := false
	if d.Algorithm == purpose.ECDHP384 {
		if _, ok := f.agree[d.Name]; !ok {
			f.agree[d.Name], _ = ecdh.P384().GenerateKey(rand.Reader)
			created = true
		}
	} else if _, ok := f.keys[d.Name]; !ok {
		f.keys[d.Name] = newSigner(d.Algorithm)
		created = true
	}
	return connect.NewResponse(&keyv1alpha1.EnsureResponse{SpkiDer: f.spki(d.Name), Created: created}), nil
}

// PublicKey gives the key and a key_id of the store's name, "#" and the fingerprint; counted.
func (f *Keys) PublicKey(_ context.Context, req *connect.Request[keyv1alpha1.PublicKeyRequest]) (*connect.Response[keyv1alpha1.PublicKeyResponse], error) {
	f.PublicKeys.Add(1)
	if f.Fail.Load() {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("fake: down"))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.keys[req.Msg.GetPurpose()]; !ok {
		return nil, refusal.New(connect.CodeNotFound, "purpose", "purpose")
	}
	der := f.spki(req.Msg.GetPurpose())
	return connect.NewResponse(&keyv1alpha1.PublicKeyResponse{SpkiDer: der, KeyId: f.Store + "#" + keyid.Fingerprint(der)}), nil
}

// Sign signs the digest in DER, by the purpose's key or the one Lie put in its place; counted.
func (f *Keys) Sign(_ context.Context, req *connect.Request[keyv1alpha1.SignRequest]) (*connect.Response[keyv1alpha1.SignResponse], error) {
	f.Signs.Add(1)
	if f.Fail.Load() {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("fake: down"))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	name := req.Msg.GetPurpose()
	k, ok := f.keys[name]
	if !ok {
		return nil, refusal.New(connect.CodeNotFound, "purpose", "purpose")
	}
	der := f.spki(name)
	if l, ok := f.liar[name]; ok {
		k = l
	}
	var opts crypto.SignerOpts = crypto.SHA256
	if len(req.Msg.GetDigest()) == 48 {
		opts = crypto.SHA384
	}
	sig, err := k.Sign(rand.Reader, req.Msg.GetDigest(), opts)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&keyv1alpha1.SignResponse{SignatureDer: sig, KeyId: f.Store + "#" + keyid.Fingerprint(der)}), nil
}

// Replace makes a new key for a purpose behind the block: a rotation the block learns of only from the key_id of a signature.
func (f *Keys) Replace(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, _ := f.declared(name)
	f.keys[name] = newSigner(d.Algorithm)
}

// Lie has the purpose sign with another key than the one it publishes.
func (f *Keys) Lie(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, _ := f.declared(name)
	f.liar[name] = newSigner(d.Algorithm)
}

// NoStore is the store port for a seal block that only signs: no call reaches it.
type NoStore struct {
	storev1alpha1connect.UnimplementedItemServiceHandler
}
