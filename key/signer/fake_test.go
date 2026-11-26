package signer_test

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
	"github.com/truvity/atoom/key/gen/atoom/key/v1alpha1/keyv1alpha1connect"
	"github.com/truvity/atoom/key/keyid"
	"github.com/truvity/atoom/key/purpose"
	"github.com/truvity/atoom/key/signer"
)

// purposeName is the purpose that the fake store serves for the key under test.
const purposeName = "sig"

// algorithms are the algorithms that the Signer knows.
var algorithms = []string{purpose.ES256, purpose.ES384, purpose.RS256}

// testKey is a key made at run time: nothing here is a constant. kind is an algorithm of the port ("ES256", "ES384", "RS256") or the
// name of a key that fits none of them ("P-521", "RSA-3072", "Ed25519").
type testKey struct {
	kind string
	priv crypto.Signer
	spki []byte
	id   string      // the id of a software key: the fingerprint of spki
	hash crypto.Hash // the hash of the algorithm, for a key that has one
}

var (
	rsaMu   sync.Mutex
	rsaPool = map[[2]int]*rsa.PrivateKey{}
)

// rsaKey is the n-th RSA key of the given size, generated on first use and shared by the tests of the run (generating takes a while).
func rsaKey(t testing.TB, bits, n int) *rsa.PrivateKey {
	t.Helper()
	rsaMu.Lock()
	defer rsaMu.Unlock()
	if k, ok := rsaPool[[2]int{bits, n}]; ok {
		return k
	}
	k, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatalf("rsa.GenerateKey(%d): %v", bits, err)
	}
	rsaPool[[2]int{bits, n}] = k
	return k
}

// newKey makes a key of the kind. n tells two RSA keys of one kind apart (EC keys are new every time).
func newKey(t testing.TB, kind string, n int) *testKey {
	t.Helper()
	k := &testKey{kind: kind}
	switch kind {
	case purpose.ES256, "P-256":
		k.hash = crypto.SHA256
		k.priv = mustEC(t, elliptic.P256())
	case purpose.ES384, "P-384":
		k.hash = crypto.SHA384
		k.priv = mustEC(t, elliptic.P384())
	case "P-521":
		k.hash = crypto.SHA512
		k.priv = mustEC(t, elliptic.P521())
	case purpose.RS256:
		k.hash = crypto.SHA256
		k.priv = rsaKey(t, 2048, n)
	case "RSA-3072":
		k.hash = crypto.SHA256
		k.priv = rsaKey(t, 3072, n)
	case "Ed25519":
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		k.priv = priv
	default:
		t.Fatalf("unknown kind %q", kind)
	}
	spki, err := x509.MarshalPKIXPublicKey(k.priv.Public())
	if err != nil {
		t.Fatal(err)
	}
	k.spki = spki
	k.id = keyid.Software(spki)
	return k
}

func mustEC(t testing.TB, c elliptic.Curve) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(c, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// sign signs a digest as the store would: ASN.1 DER for an EC key, PKCS#1 v1.5 for an RSA key.
func (k *testKey) sign(t testing.TB, digest []byte) []byte {
	t.Helper()
	sig, err := k.priv.Sign(rand.Reader, digest, k.hash)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return sig
}

// hashOf is the hash of the algorithm.
func hashOf(alg string) crypto.Hash {
	if alg == purpose.ES384 {
		return crypto.SHA384
	}
	return crypto.SHA256
}

// digestOf is the digest of msg in the hash of the algorithm.
func digestOf(alg, msg string) []byte {
	if alg == purpose.ES384 {
		d := sha512.Sum384([]byte(msg))
		return d[:]
	}
	d := sha256.Sum256([]byte(msg))
	return d[:]
}

// signFunc is what the fake answers to Sign.
type signFunc func(ctx context.Context, req *keyv1alpha1.SignRequest) (*keyv1alpha1.SignResponse, error)

// fake is an in-memory key store that a test scripts: which purposes Capabilities lists, which public key and key_id PublicKey returns
// and what Sign answers. It records what reaches it. Fields are set before the fake is served, or through the methods.
type fake struct {
	keyv1alpha1connect.UnimplementedKeyStoreServiceHandler

	mu          sync.Mutex
	purposes    []*keyv1alpha1.PurposeInfo
	spki        []byte
	keyID       string
	capsErr     error
	pubErr      error
	blockCaps   bool // Capabilities waits until the caller gives up
	blockPub    bool // PublicKey waits until the caller gives up
	sign        signFunc
	capsCalls   int
	pubPurposes []string // the purpose of each PublicKey request
	signReqs    []*keyv1alpha1.SignRequest

	entered     chan struct{} // one value each time Sign is entered
	release     chan struct{} // closed at the end of the test: lets a blocked handler go
	releaseOnce sync.Once
}

// newFake is a store that serves purposeName with the algorithm and a key of it, and signs honestly.
func newFake(t testing.TB, alg string) (*fake, *testKey) {
	t.Helper()
	k := newKey(t, alg, 0)
	f := &fake{entered: make(chan struct{}, 4096), release: make(chan struct{})}
	f.purposes = []*keyv1alpha1.PurposeInfo{
		{Name: "wrapping", Algorithm: purpose.ECDHP384, Operations: []string{string(purpose.Agree)}},
		{Name: purposeName, Algorithm: alg, Operations: []string{string(purpose.Sign)}},
	}
	f.setKey(k)
	f.sign = honest(t, k)
	return f, k
}

// setKey makes PublicKey return the key and its id.
func (f *fake) setKey(k *testKey) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spki, f.keyID = k.spki, k.id
}

func (f *fake) setSign(fn signFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sign = fn
}

// answer makes Sign return the signature and the key_id, whatever the request.
func (f *fake) answer(sig []byte, id string) {
	f.setSign(func(context.Context, *keyv1alpha1.SignRequest) (*keyv1alpha1.SignResponse, error) {
		return &keyv1alpha1.SignResponse{SignatureDer: sig, KeyId: id}, nil
	})
}

// fail makes Sign return a Connect error with the code.
func (f *fake) fail(code connect.Code) {
	f.setSign(func(context.Context, *keyv1alpha1.SignRequest) (*keyv1alpha1.SignResponse, error) {
		return nil, connect.NewError(code, errors.New("the store refuses"))
	})
}

// blockSign makes Sign wait until the caller gives up.
func (f *fake) blockSign() {
	f.setSign(func(ctx context.Context, _ *keyv1alpha1.SignRequest) (*keyv1alpha1.SignResponse, error) {
		return nil, f.block(ctx)
	})
}

// block waits for the caller to give up, or for the end of the test.
func (f *fake) block(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-f.release:
		return connect.NewError(connect.CodeUnavailable, errors.New("the test is over"))
	}
}

func (f *fake) unblock() { f.releaseOnce.Do(func() { close(f.release) }) }

func (f *fake) signCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.signReqs)
}

func (f *fake) capsCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.capsCalls
}

func (f *fake) pubCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pubPurposes)
}

// requests returns the Sign requests that reached the fake, in order.
func (f *fake) requests() []*keyv1alpha1.SignRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*keyv1alpha1.SignRequest(nil), f.signReqs...)
}

func (f *fake) Capabilities(ctx context.Context, _ *connect.Request[keyv1alpha1.CapabilitiesRequest]) (*connect.Response[keyv1alpha1.CapabilitiesResponse], error) {
	f.mu.Lock()
	f.capsCalls++
	err, block, purposes := f.capsErr, f.blockCaps, f.purposes
	f.mu.Unlock()
	if block {
		return nil, f.block(ctx)
	}
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&keyv1alpha1.CapabilitiesResponse{
		Store:      "memory",
		Algorithms: []string{purpose.ES256, purpose.ES384, purpose.RS256, purpose.ECDHP384},
		Purposes:   purposes,
	}), nil
}

func (f *fake) PublicKey(ctx context.Context, req *connect.Request[keyv1alpha1.PublicKeyRequest]) (*connect.Response[keyv1alpha1.PublicKeyResponse], error) {
	f.mu.Lock()
	f.pubPurposes = append(f.pubPurposes, req.Msg.GetPurpose())
	err, block, spki, id := f.pubErr, f.blockPub, f.spki, f.keyID
	f.mu.Unlock()
	if block {
		return nil, f.block(ctx)
	}
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&keyv1alpha1.PublicKeyResponse{SpkiDer: spki, KeyId: id}), nil
}

func (f *fake) Sign(ctx context.Context, req *connect.Request[keyv1alpha1.SignRequest]) (*connect.Response[keyv1alpha1.SignResponse], error) {
	f.mu.Lock()
	f.signReqs = append(f.signReqs, proto.Clone(req.Msg).(*keyv1alpha1.SignRequest))
	fn := f.sign
	f.mu.Unlock()
	select {
	case f.entered <- struct{}{}:
	default:
	}
	if fn == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("no script"))
	}
	res, err := fn(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(res), nil
}

// honest signs the digest of the request with the key and names the key.
func honest(t testing.TB, k *testKey) signFunc {
	return func(_ context.Context, req *keyv1alpha1.SignRequest) (*keyv1alpha1.SignResponse, error) {
		sig, err := k.priv.Sign(rand.Reader, req.GetDigest(), k.hash)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		return &keyv1alpha1.SignResponse{SignatureDer: sig, KeyId: k.id}, nil
	}
}

// start serves the fake with the generated handler on a loopback listener and returns the generated client.
func start(t testing.TB, f *fake, opts ...connect.ClientOption) keyv1alpha1connect.KeyStoreServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(keyv1alpha1connect.NewKeyStoreServiceHandler(f))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(f.unblock) // runs before srv.Close: a handler that waits is let go
	return keyv1alpha1connect.NewKeyStoreServiceClient(srv.Client(), srv.URL, opts...)
}

// newSigner makes the Signer of purposeName and fails the test if New refuses.
func newSigner(t testing.TB, f *fake, opts ...signer.Option) *signer.Signer {
	t.Helper()
	s, err := signer.New(t.Context(), start(t, f), purposeName, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// refused fails the test unless the call returned no signature and an error that is want.
func refused(t testing.TB, sig []byte, err, want error) {
	t.Helper()
	if sig != nil {
		t.Errorf("a signature came back with the refusal: %x", sig)
	}
	if err == nil {
		t.Fatalf("no error, want %v", want)
	}
	if !errors.Is(err, want) {
		t.Fatalf("error %q is not %q", err, want)
	}
}

// sentinels are the refusals of the Signer itself.
var sentinels = []error{
	signer.ErrPurpose, signer.ErrOperation, signer.ErrAlgorithm, signer.ErrKey, signer.ErrTimeout, signer.ErrHash, signer.ErrPSS,
	signer.ErrDigest, signer.ErrKeyChanged, signer.ErrVerify,
}

// isPortError fails the test unless err is the Connect error of the port with the code and is none of the Signer's own refusals.
func isPortError(t testing.TB, err error, code connect.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want a Connect error with code %s", code)
	}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("error %q does not hold a *connect.Error", err)
	}
	if ce.Code() != code || connect.CodeOf(err) != code {
		t.Fatalf("code %s, want %s: %v", ce.Code(), code, err)
	}
	for _, s := range sentinels {
		if errors.Is(err, s) {
			t.Fatalf("a failure of the port is also %q", s)
		}
	}
}
