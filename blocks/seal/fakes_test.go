package seal_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/common/refusal"
	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
	"github.com/truvity/atoom/key/gen/atoom/key/v1alpha1/keyv1alpha1connect"
	"github.com/truvity/atoom/key/purpose"
	storev1alpha1 "github.com/truvity/atoom/store/gen/atoom/store/v1alpha1"
	"github.com/truvity/atoom/store/gen/atoom/store/v1alpha1/storev1alpha1connect"
)

// fakeKeys is a stand-in of the key port for the block's tests: the keys of the declared purposes in memory. The kit also runs over the
// real software driver (`just conformance`); this one lets a test count the calls and make them fail.
type fakeKeys struct {
	keyv1alpha1connect.UnimplementedKeyStoreServiceHandler
	decl  []purpose.Declaration
	mu    sync.Mutex
	keys  map[string]crypto.Signer
	agree map[string]*ecdh.PrivateKey
	ecdhN atomic.Int64
	fail  atomic.Bool
}

func newFakeKeys(decl []purpose.Declaration) *fakeKeys {
	return &fakeKeys{decl: decl, keys: map[string]crypto.Signer{}, agree: map[string]*ecdh.PrivateKey{}}
}

func (f *fakeKeys) declared(name string) (purpose.Declaration, bool) {
	for _, d := range f.decl {
		if d.Name == name {
			return d, true
		}
	}
	return purpose.Declaration{}, false
}

func (f *fakeKeys) Capabilities(context.Context, *connect.Request[keyv1alpha1.CapabilitiesRequest]) (*connect.Response[keyv1alpha1.CapabilitiesResponse], error) {
	out := &keyv1alpha1.CapabilitiesResponse{Store: "fake", Algorithms: []string{purpose.ES384, purpose.ES256, purpose.RS256, purpose.ECDHP384}}
	for _, d := range f.decl {
		out.Purposes = append(out.Purposes, d.Info())
	}
	return connect.NewResponse(out), nil
}

func (f *fakeKeys) spki(name string) ([]byte, bool) {
	if k, ok := f.agree[name]; ok {
		pub, _ := ecdsa.ParseUncompressedPublicKey(elliptic.P384(), k.PublicKey().Bytes())
		der, _ := x509.MarshalPKIXPublicKey(pub)
		return der, true
	}
	if k, ok := f.keys[name]; ok {
		der, _ := x509.MarshalPKIXPublicKey(k.Public())
		return der, true
	}
	return nil, false
}

func (f *fakeKeys) Ensure(_ context.Context, req *connect.Request[keyv1alpha1.EnsureRequest]) (*connect.Response[keyv1alpha1.EnsureResponse], error) {
	d, ok := f.declared(req.Msg.GetPurpose())
	if !ok || d.Algorithm != req.Msg.GetAlgorithm() {
		return nil, refusal.New(connect.CodeInvalidArgument, "purpose", "purpose")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	created := false
	if _, ok := f.spki(d.Name); !ok {
		created = true
		switch d.Algorithm {
		case purpose.ECDHP384:
			k, _ := ecdh.P384().GenerateKey(rand.Reader)
			f.agree[d.Name] = k
		case purpose.ES384:
			k, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
			f.keys[d.Name] = k
		case purpose.ES256:
			k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			f.keys[d.Name] = k
		case purpose.RS256:
			k, _ := rsa.GenerateKey(rand.Reader, 2048)
			f.keys[d.Name] = k
		}
	}
	der, _ := f.spki(d.Name)
	return connect.NewResponse(&keyv1alpha1.EnsureResponse{SpkiDer: der, Created: created}), nil
}

func keyID(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

func (f *fakeKeys) PublicKey(_ context.Context, req *connect.Request[keyv1alpha1.PublicKeyRequest]) (*connect.Response[keyv1alpha1.PublicKeyResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	der, ok := f.spki(req.Msg.GetPurpose())
	if !ok {
		return nil, refusal.New(connect.CodeNotFound, "absent", "purpose")
	}
	return connect.NewResponse(&keyv1alpha1.PublicKeyResponse{SpkiDer: der, KeyId: keyID(der)}), nil
}

func (f *fakeKeys) Sign(_ context.Context, req *connect.Request[keyv1alpha1.SignRequest]) (*connect.Response[keyv1alpha1.SignResponse], error) {
	f.mu.Lock()
	k, ok := f.keys[req.Msg.GetPurpose()]
	der, _ := f.spki(req.Msg.GetPurpose())
	f.mu.Unlock()
	if !ok || len(req.Msg.GetDigest()) != purpose.DigestLen(req.Msg.GetAlgorithm()) {
		return nil, refusal.New(connect.CodeInvalidArgument, "purpose", "purpose")
	}
	h := crypto.SHA256
	if req.Msg.GetAlgorithm() == purpose.ES384 {
		h = crypto.SHA384
	}
	sig, err := k.Sign(rand.Reader, req.Msg.GetDigest(), h)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&keyv1alpha1.SignResponse{SignatureDer: sig, KeyId: keyID(der)}), nil
}

func (f *fakeKeys) Ecdh(_ context.Context, req *connect.Request[keyv1alpha1.EcdhRequest]) (*connect.Response[keyv1alpha1.EcdhResponse], error) {
	f.ecdhN.Add(1)
	if f.fail.Load() {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("down"))
	}
	f.mu.Lock()
	k, ok := f.agree[req.Msg.GetPurpose()]
	f.mu.Unlock()
	if !ok {
		return nil, refusal.New(connect.CodeInvalidArgument, "purpose", "purpose")
	}
	peer, err := ecdh.P384().NewPublicKey(req.Msg.GetPeerPublic())
	if err != nil {
		return nil, refusal.New(connect.CodeInvalidArgument, "peer", "peer_public")
	}
	s, err := k.ECDH(peer)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&keyv1alpha1.EcdhResponse{SharedSecret: s}), nil
}

// fakeItems is a stand-in of the store port: items in a map, with the conditions, the key order and the paging of the contract, and
// no expiry or change log (the block uses neither).
type fakeItems struct {
	storev1alpha1connect.UnimplementedItemServiceHandler
	mu    sync.Mutex
	items map[string]*storev1alpha1.DriverItem
	fail  atomic.Bool
}

func newFakeItems() *fakeItems { return &fakeItems{items: map[string]*storev1alpha1.DriverItem{}} }

func (f *fakeItems) down() error { return refusal.New(connect.CodeUnavailable, "engine", "") }

func (f *fakeItems) Get(_ context.Context, req *connect.Request[storev1alpha1.ItemGetRequest]) (*connect.Response[storev1alpha1.ItemGetResponse], error) {
	if f.fail.Load() {
		return nil, f.down()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.items[string(req.Msg.GetKey())]
	if !ok {
		return nil, refusal.New(connect.CodeNotFound, "absent", "key")
	}
	return connect.NewResponse(&storev1alpha1.ItemGetResponse{Item: it}), nil
}

func (f *fakeItems) Query(_ context.Context, req *connect.Request[storev1alpha1.QueryRequest]) (*connect.Response[storev1alpha1.QueryResponse], error) {
	if f.fail.Load() {
		return nil, f.down()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []string
	for k := range f.items {
		if strings.HasPrefix(k, string(req.Msg.GetPrefix())) && k > string(req.Msg.GetPageToken()) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	size := int(req.Msg.GetPageSize())
	if size <= 0 || size > 500 {
		size = 100
	}
	out := &storev1alpha1.QueryResponse{}
	for i, k := range keys {
		if i == size {
			out.NextPageToken = []byte(keys[i-1])
			break
		}
		out.Items = append(out.Items, f.items[k])
	}
	return connect.NewResponse(out), nil
}

func (f *fakeItems) Transact(_ context.Context, req *connect.Request[storev1alpha1.TransactRequest]) (*connect.Response[storev1alpha1.TransactResponse], error) {
	if f.fail.Load() {
		return nil, f.down()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range req.Msg.GetConditions() {
		_, exists := f.items[string(c.GetKey())]
		if c.GetAbsent() && exists {
			return nil, refusal.New(connect.CodeAborted, "version", "conditions[0]")
		}
	}
	for _, d := range req.Msg.GetDeletes() {
		delete(f.items, string(d.GetKey()))
	}
	out := &storev1alpha1.TransactResponse{}
	for _, p := range req.Msg.GetPuts() {
		v := uint64(1)
		if old, ok := f.items[string(p.GetKey())]; ok {
			v = old.GetVersion() + 1
		}
		it := &storev1alpha1.DriverItem{Key: bytes.Clone(p.GetKey()), Version: v, Value: bytes.Clone(p.GetValue())}
		f.items[string(p.GetKey())] = it
		out.Items = append(out.Items, it)
	}
	return connect.NewResponse(out), nil
}

// keysWith returns the keys of the items under a prefix, sorted.
func (f *fakeItems) keysWith(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for k := range f.items {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
