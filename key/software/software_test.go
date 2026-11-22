package software_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/common/refusal"
	"github.com/truvity/atoom/key/conformance"
	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
	"github.com/truvity/atoom/key/gen/atoom/key/v1alpha1/keyv1alpha1connect"
	"github.com/truvity/atoom/key/purpose"
	"github.com/truvity/atoom/key/signature"
	"github.com/truvity/atoom/key/software"
)

// shortDir returns a short directory.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "k")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

type running struct {
	client keyv1alpha1connect.KeyStoreServiceClient
	state  string
}

// start serves the library on an in-process HTTP server and a new state directory, as a consumer that links the driver does, and
// stops it at the end of the test.
func start(t *testing.T, environment string) running {
	t.Helper()
	return startWith(t, software.Options{Environment: environment})
}

func startWith(t *testing.T, opts software.Options) running {
	t.Helper()
	r := running{state: filepath.Join(shortDir(t), "state")}
	opts.StateDir = r.state
	if opts.Purposes == nil {
		opts.Purposes = purpose.Reference()
	}
	svc, err := software.New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle(svc.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	r.client = keyv1alpha1connect.NewKeyStoreServiceClient(srv.Client(), srv.URL)
	return r
}

func ensure(t *testing.T, c keyv1alpha1connect.KeyStoreServiceClient, purpose, alg string) *keyv1alpha1.EnsureResponse {
	t.Helper()
	res, err := c.Ensure(context.Background(), connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: purpose, Algorithm: alg}))
	if err != nil {
		t.Fatalf("Ensure(%s, %s): %v", purpose, alg, err)
	}
	return res.Msg
}

func wantRefused(t *testing.T, err error, code connect.Code, reason string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want %s/%s, got success", code, reason)
	}
	if connect.CodeOf(err) != code {
		t.Fatalf("code = %s, want %s (%v)", connect.CodeOf(err), code, err)
	}
	if got, _ := refusal.Reason(err); got != reason {
		t.Fatalf("reason = %q, want %q", got, reason)
	}
}

func TestSignVerifyAndEnsureTwice(t *testing.T) {
	r := start(t, "development")
	ctx := context.Background()
	first := ensure(t, r.client, "token-signing", "ES384")
	if !first.GetCreated() {
		t.Fatal("the first Ensure did not make the key")
	}
	second := ensure(t, r.client, "token-signing", "ES384")
	if second.GetCreated() || !bytes.Equal(first.GetSpkiDer(), second.GetSpkiDer()) {
		t.Fatal("the second Ensure made a key or returned another public key")
	}
	k, err := x509.ParsePKIXPublicKey(first.GetSpkiDer())
	if err != nil {
		t.Fatal(err)
	}
	digest := sha512.Sum384([]byte("a content"))
	res, err := r.client.Sign(ctx, connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: "token-signing", Digest: digest[:], Algorithm: "ES384"}))
	if err != nil {
		t.Fatal(err)
	}
	if !ecdsa.VerifyASN1(k.(*ecdsa.PublicKey), digest[:], res.Msg.GetSignatureDer()) {
		t.Fatal("the signature does not verify with the returned SPKI")
	}
	pk, err := r.client.PublicKey(ctx, connect.NewRequest(&keyv1alpha1.PublicKeyRequest{Purpose: "token-signing"}))
	if err != nil || !bytes.Equal(pk.Msg.GetSpkiDer(), first.GetSpkiDer()) {
		t.Fatalf("PublicKey: %v", err)
	}
}

// Ensure says when the key was made, the first call and the second alike: a caller compares it with the time of a purpose
// revocation, so that a replacement made after it signs (ADR 0041).
func TestEnsureSaysWhenTheKeyWasMade(t *testing.T) {
	r := start(t, "development")
	before := time.Now().Add(-2 * time.Second)
	first := ensure(t, r.client, "token-signing", "ES384")
	after := time.Now().Add(2 * time.Second)
	at := first.GetCreatedAt().AsTime()
	if first.GetCreatedAt() == nil || at.Before(before) || at.After(after) {
		t.Fatalf("created_at = %v, want a time between %v and %v", at, before, after)
	}
	second := ensure(t, r.client, "token-signing", "ES384")
	if !second.GetCreatedAt().AsTime().Equal(at) {
		t.Fatalf("the second Ensure says %v, the first %v", second.GetCreatedAt().AsTime(), at)
	}
}

// verifies: REQ-KEYS-265
// The RS256 purpose has an RSA-2048 key with the exponent 65537 in a file of its own, a signature of the RSASSA-PKCS1-v1_5
// form that verifies over the SHA-256 digest, and refuses every other digest, algorithm and use of the key. The key does
// not change when Ensure is asked again.
func TestTheRS256KeySignsPKCS1v15OverSHA256(t *testing.T) {
	r := start(t, "development")
	ctx := context.Background()
	first := ensure(t, r.client, "token-signing-rs256", "RS256")
	if !first.GetCreated() {
		t.Fatal("the first Ensure did not make the key")
	}
	second := ensure(t, r.client, "token-signing-rs256", "RS256")
	if second.GetCreated() || !bytes.Equal(first.GetSpkiDer(), second.GetSpkiDer()) {
		t.Fatal("the second Ensure made a key or returned another public key")
	}
	k, err := x509.ParsePKIXPublicKey(first.GetSpkiDer())
	if err != nil {
		t.Fatal(err)
	}
	pub, ok := k.(*rsa.PublicKey)
	if !ok || pub.N.BitLen() != 2048 || pub.E != 65537 {
		t.Fatalf("the public key is %T, want RSA-2048 with the exponent 65537", k)
	}
	digest := sha256.Sum256([]byte("a content"))
	sign := func(purpose string, digest []byte, alg string) (*keyv1alpha1.SignResponse, error) {
		res, err := r.client.Sign(ctx, connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: purpose, Digest: digest, Algorithm: alg}))
		if err != nil {
			return nil, err
		}
		return res.Msg, nil
	}
	res, err := sign("token-signing-rs256", digest[:], "RS256")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.GetSignatureDer()) != 256 {
		t.Fatalf("signature of %d bytes, want the 256 of RSASSA-PKCS1-v1_5 over a 2048-bit modulus", len(res.GetSignatureDer()))
	}
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], res.GetSignatureDer()); err != nil {
		t.Fatalf("the signature does not verify as PKCS#1 v1.5 over SHA-256: %v", err)
	}
	if rsa.VerifyPSS(pub, crypto.SHA256, digest[:], res.GetSignatureDer(), nil) == nil {
		t.Fatal("the signature also verifies as PSS: it is not PKCS#1 v1.5")
	}
	other := sha256.Sum256([]byte("another content"))
	if rsa.VerifyPKCS1v15(pub, crypto.SHA256, other[:], res.GetSignatureDer()) == nil {
		t.Fatal("the signature verifies another digest")
	}
	// The refusals of the key.
	d48 := make([]byte, 48)
	_, err = sign("token-signing-rs256", d48, "RS256")
	wantRefused(t, err, connect.CodeInvalidArgument, "digest")
	_, err = sign("token-signing-rs256", digest[:0], "RS256")
	wantRefused(t, err, connect.CodeInvalidArgument, "digest")
	_, err = sign("token-signing-rs256", digest[:], "ES384")
	wantRefused(t, err, connect.CodeInvalidArgument, "algorithm")
	_, err = sign("token-signing-rs256", digest[:], "ES256")
	wantRefused(t, err, connect.CodeInvalidArgument, "algorithm")
	_, err = sign("token-signing-rs256", digest[:], "")
	wantRefused(t, err, connect.CodeInvalidArgument, "algorithm")
	_, err = sign("token-signing", digest[:], "RS256")
	wantRefused(t, err, connect.CodeInvalidArgument, "algorithm")
	_, err = r.client.Ecdh(ctx, connect.NewRequest(&keyv1alpha1.EcdhRequest{Purpose: "token-signing-rs256", PeerPublic: ecdhPoint(t)}))
	wantRefused(t, err, connect.CodeFailedPrecondition, "algorithm")
	pk, err := r.client.PublicKey(ctx, connect.NewRequest(&keyv1alpha1.PublicKeyRequest{Purpose: "token-signing-rs256"}))
	if err != nil || !bytes.Equal(pk.Msg.GetSpkiDer(), first.GetSpkiDer()) {
		t.Fatalf("PublicKey: %v", err)
	}
	caps, err := r.client.Capabilities(ctx, connect.NewRequest(&keyv1alpha1.CapabilitiesRequest{}))
	if err != nil || !slices.Contains(caps.Msg.GetAlgorithms(), "RS256") {
		t.Fatalf("Capabilities = %v, %v; want RS256 declared", caps, err)
	}
}

func ecdhPoint(t *testing.T) []byte {
	t.Helper()
	k, err := ecdh.P384().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k.PublicKey().Bytes()
}

func TestSignRefusals(t *testing.T) {
	r := start(t, "development")
	ctx := context.Background()
	ensure(t, r.client, "token-signing", "ES384")
	ensure(t, r.client, "wrapping", "ECDH-P384")
	digest := make([]byte, 48)
	sign := func(purpose string, digest []byte, alg string) error {
		_, err := r.client.Sign(ctx, connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: purpose, Digest: digest, Algorithm: alg}))
		return err
	}
	wantRefused(t, sign("token-signing", digest[:32], "ES384"), connect.CodeInvalidArgument, "digest")
	wantRefused(t, sign("token-signing", digest, "ES256"), connect.CodeInvalidArgument, "algorithm")
	wantRefused(t, sign("token-signing", digest, ""), connect.CodeInvalidArgument, "algorithm")
	wantRefused(t, sign("registry", digest, "ES384"), connect.CodeInvalidArgument, "purpose")
	wantRefused(t, sign("audit-log", digest, "ES384"), connect.CodeInvalidArgument, "algorithm")
	wantRefused(t, sign("audit-log", digest[:32], "ES256"), connect.CodeNotFound, "absent")
	wantRefused(t, sign("wrapping", digest, "ES384"), connect.CodeFailedPrecondition, "algorithm")

	_, err := r.client.PublicKey(ctx, connect.NewRequest(&keyv1alpha1.PublicKeyRequest{Purpose: "audit-log"}))
	wantRefused(t, err, connect.CodeNotFound, "absent")
	_, err = r.client.PublicKey(ctx, connect.NewRequest(&keyv1alpha1.PublicKeyRequest{Purpose: "registry"}))
	wantRefused(t, err, connect.CodeInvalidArgument, "purpose")
}

func TestEnsureRefusals(t *testing.T) {
	r := start(t, "development")
	ctx := context.Background()
	e := func(purpose, alg string) error {
		_, err := r.client.Ensure(ctx, connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: purpose, Algorithm: alg}))
		return err
	}
	wantRefused(t, e("registry", "ES384"), connect.CodeInvalidArgument, "purpose")
	wantRefused(t, e("", "ES384"), connect.CodeInvalidArgument, "purpose")
	wantRefused(t, e("token-signing", "PS256"), connect.CodeInvalidArgument, "algorithm")
	// RS256 is declared, for the RS256 purpose alone: no other purpose is given an RSA key, and the RS256 purpose is
	// given no EC key.
	wantRefused(t, e("token-signing", "RS256"), connect.CodeFailedPrecondition, "algorithm")
	wantRefused(t, e("audit-log", "RS256"), connect.CodeFailedPrecondition, "algorithm")
	wantRefused(t, e("wrapping", "RS256"), connect.CodeFailedPrecondition, "algorithm")
	wantRefused(t, e("token-signing-rs256", "ES384"), connect.CodeFailedPrecondition, "algorithm")
	wantRefused(t, e("token-signing-rs256", "ES256"), connect.CodeFailedPrecondition, "algorithm")
	wantRefused(t, e("token-signing-rs256", "ECDH-P384"), connect.CodeFailedPrecondition, "algorithm")
	wantRefused(t, e("token-signing-rs256", "PS256"), connect.CodeInvalidArgument, "algorithm")
	// A purpose has one algorithm; an existing key with another is not replaced.
	ensure(t, r.client, "token-signing", "ES384")
	wantRefused(t, e("token-signing", "ECDH-P384"), connect.CodeFailedPrecondition, "algorithm")
	wantRefused(t, e("wrapping", "ES384"), connect.CodeFailedPrecondition, "algorithm")
	// The audit-log key is ES256 (D-25), not ES384.
	wantRefused(t, e("audit-log", "ES384"), connect.CodeFailedPrecondition, "algorithm")
	wantRefused(t, e("token-signing", "ES256"), connect.CodeFailedPrecondition, "algorithm")
}

func TestEcdhAgainstReference(t *testing.T) {
	r := start(t, "development")
	ctx := context.Background()
	drv := ensure(t, r.client, "wrapping", "ECDH-P384")
	pk, err := x509.ParsePKIXPublicKey(drv.GetSpkiDer())
	if err != nil {
		t.Fatal(err)
	}
	driverPub, err := pk.(*ecdsa.PublicKey).ECDH()
	if err != nil {
		t.Fatal(err)
	}
	eph, err := ecdh.P384().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	want, err := eph.ECDH(driverPub)
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.client.Ecdh(ctx, connect.NewRequest(&keyv1alpha1.EcdhRequest{Purpose: "wrapping", PeerPublic: eph.PublicKey().Bytes()}))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, res.Msg.GetSharedSecret()) {
		t.Fatal("the shared secret differs from ephemeral.ECDH(driverPub)")
	}

	e := func(purpose string, peer []byte) error {
		_, err := r.client.Ecdh(ctx, connect.NewRequest(&keyv1alpha1.EcdhRequest{Purpose: purpose, PeerPublic: peer}))
		return err
	}
	off := bytes.Clone(eph.PublicKey().Bytes())
	off[len(off)-1] ^= 0x01
	wantRefused(t, e("wrapping", off), connect.CodeInvalidArgument, "peer_public")
	wantRefused(t, e("wrapping", nil), connect.CodeInvalidArgument, "peer_public")
	wantRefused(t, e("wrapping", eph.PublicKey().Bytes()[:49]), connect.CodeInvalidArgument, "peer_public")
	wantRefused(t, e("registry", eph.PublicKey().Bytes()), connect.CodeInvalidArgument, "purpose")
	wantRefused(t, e("audit-log", eph.PublicKey().Bytes()), connect.CodeNotFound, "absent")
	ensure(t, r.client, "token-signing", "ES384")
	wantRefused(t, e("token-signing", eph.PublicKey().Bytes()), connect.CodeFailedPrecondition, "algorithm")
}

// verifies: REQ-KEYS-004
func TestCapabilities(t *testing.T) {
	r := start(t, "development")
	res, err := r.client.Capabilities(context.Background(), connect.NewRequest(&keyv1alpha1.CapabilitiesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	m := res.Msg
	if m.GetStore() != "software" || m.GetAttests() || strings.Join(m.GetAlgorithms(), ",") != "ES384,ES256,RS256,ECDH-P384" {
		t.Fatalf("capabilities = %v", m)
	}
	// The purposes are the host's declaration, in its order, with the operations.
	var got []string
	for _, p := range m.GetPurposes() {
		got = append(got, p.GetName()+":"+p.GetAlgorithm()+":"+strings.Join(p.GetOperations(), "+"))
	}
	want := []string{"wrapping:ECDH-P384:agree", "token-signing:ES384:sign", "audit-log:ES256:sign"}
	if len(got) != 11 || !slices.Equal(got[:3], want) {
		t.Fatalf("purposes = %v", got)
	}
}

// The purposes of a host that no driver ever knew (the target of step B13): ES256 and ES384 for the client assertions and the identity
// header of session, with the names session gives them. The keys sign, verify, come back after a restart as the same keys, and the
// JWS form converts.
func TestAHostDeclaredPurposeIsServedWithES256AndES384(t *testing.T) {
	decls := []purpose.Declaration{
		{Name: "client-assertion", Algorithm: purpose.ES256, Operations: []purpose.Operation{purpose.Sign}},
		{Name: "identity-header", Algorithm: purpose.ES384, Operations: []purpose.Operation{purpose.Sign}},
		{Name: "sealed-cookie", Algorithm: purpose.ECDHP384, Operations: []purpose.Operation{purpose.Agree}},
	}
	state := filepath.Join(shortDir(t), "state")
	serve := func() keyv1alpha1connect.KeyStoreServiceClient {
		svc, err := software.New(software.Options{StateDir: state, Environment: "development", Purposes: decls})
		if err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		mux.Handle(svc.Handler())
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		return keyv1alpha1connect.NewKeyStoreServiceClient(srv.Client(), srv.URL)
	}
	c := serve()
	e256 := ensure(t, c, "client-assertion", "ES256")
	e384 := ensure(t, c, "identity-header", "ES384")
	if !e256.GetCreated() || !e384.GetCreated() {
		t.Fatal("the keys were not made")
	}
	k256, _ := x509.ParsePKIXPublicKey(e256.GetSpkiDer())
	k384, _ := x509.ParsePKIXPublicKey(e384.GetSpkiDer())
	pub256, pub384 := k256.(*ecdsa.PublicKey), k384.(*ecdsa.PublicKey)
	if pub256.Curve.Params().Name != "P-256" || pub384.Curve.Params().Name != "P-384" {
		t.Fatalf("curves %s and %s", pub256.Curve.Params().Name, pub384.Curve.Params().Name)
	}
	d256, d384 := sha256.Sum256([]byte("jws signing input")), sha512.Sum384([]byte("jws signing input"))
	for _, tc := range []struct {
		purpose, alg string
		digest       []byte
		pub          *ecdsa.PublicKey
	}{{"client-assertion", "ES256", d256[:], pub256}, {"identity-header", "ES384", d384[:], pub384}} {
		res, err := c.Sign(context.Background(), connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: tc.purpose, Algorithm: tc.alg, Digest: tc.digest}))
		if err != nil {
			t.Fatalf("%s: %v", tc.purpose, err)
		}
		if !ecdsa.VerifyASN1(tc.pub, tc.digest, res.Msg.GetSignatureDer()) {
			t.Fatalf("%s: the DER signature does not verify", tc.purpose)
		}
		raw, err := signature.DERToRaw(tc.alg, res.Msg.GetSignatureDer())
		if err != nil || len(raw) != 2*signature.Size(tc.alg) {
			t.Fatalf("%s: raw form: %v", tc.purpose, err)
		}
	}
	// A restart with the same declaration finds the same keys; the names of the old closed list are no longer known.
	again := ensure(t, serve(), "client-assertion", "ES256")
	if again.GetCreated() || !bytes.Equal(again.GetSpkiDer(), e256.GetSpkiDer()) {
		t.Fatal("a restart made another key")
	}
	_, err := c.Ensure(context.Background(), connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: "token-signing", Algorithm: "ES384"}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a name of the old list, undeclared: %v", err)
	}
}

// What the host declared decides what may be done: a purpose declared without an operation is made and read and never used; an
// agreement purpose does not sign and a signing purpose does not agree.
func TestTheOperationsOfADeclarationAreEnforced(t *testing.T) {
	r := startWith(t, software.Options{Environment: "development", Purposes: []purpose.Declaration{
		{Name: "made-only", Algorithm: purpose.ES384},
		{Name: "signs", Algorithm: purpose.ES384, Operations: []purpose.Operation{purpose.Sign}},
		{Name: "no-agree", Algorithm: purpose.ECDHP384},
		{Name: "agrees", Algorithm: purpose.ECDHP384, Operations: []purpose.Operation{purpose.Agree}},
	}})
	for _, n := range []string{"made-only", "signs"} {
		ensure(t, r.client, n, "ES384")
	}
	ensure(t, r.client, "no-agree", "ECDH-P384")
	ensure(t, r.client, "agrees", "ECDH-P384")
	d := sha512.Sum384([]byte("x"))
	sign := func(name string) error {
		_, err := r.client.Sign(context.Background(), connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: name, Algorithm: "ES384", Digest: d[:]}))
		return err
	}
	peer, _ := ecdh.P384().GenerateKey(rand.Reader)
	agree := func(name string) error {
		_, err := r.client.Ecdh(context.Background(), connect.NewRequest(&keyv1alpha1.EcdhRequest{Purpose: name, PeerPublic: peer.PublicKey().Bytes()}))
		return err
	}
	if err := sign("signs"); err != nil {
		t.Fatalf("signs: %v", err)
	}
	if err := agree("agrees"); err != nil {
		t.Fatalf("agrees: %v", err)
	}
	for name, err := range map[string]error{"made-only sign": sign("made-only"), "no-agree agree": agree("no-agree")} {
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		} else if reason, _ := refusal.Reason(err); reason != "purpose" {
			t.Errorf("%s: reason %q", name, reason)
		}
	}
	for name, err := range map[string]error{"agree with a signing purpose": agree("signs"), "sign with an agreement purpose": sign("agrees")} {
		if connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Errorf("%s: %v", name, err)
		} else if reason, _ := refusal.Reason(err); reason != "algorithm" {
			t.Errorf("%s: reason %q", name, reason)
		}
	}
	if _, err := r.client.PublicKey(context.Background(), connect.NewRequest(&keyv1alpha1.PublicKeyRequest{Purpose: "made-only"})); err != nil {
		t.Errorf("public key of a made-only purpose: %v", err)
	}
}

// The declaration is the host's to get right: none, or a bad one, starts nothing and makes no state directory; and a state directory
// that holds a key of a purpose the host no longer declares, or of another algorithm than it declares now, is refused.
func TestTheDeclarationIsCheckedBeforeAnythingIsOpened(t *testing.T) {
	base := shortDir(t)
	for name, decls := range map[string][]purpose.Declaration{
		"none":            nil,
		"a bad name":      {{Name: "Bad", Algorithm: purpose.ES256}},
		"a bad algorithm": {{Name: "ok", Algorithm: "PS256"}},
	} {
		_, err := software.New(software.Options{StateDir: filepath.Join(base, name), Purposes: decls})
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
		if _, err := os.Lstat(filepath.Join(base, name)); err == nil {
			t.Errorf("%s: the state directory was made", name)
		}
	}
	state := filepath.Join(base, "state")
	svc, err := software.New(software.Options{StateDir: state, Purposes: []purpose.Declaration{
		{Name: "a", Algorithm: purpose.ES384, Operations: []purpose.Operation{purpose.Sign}},
		{Name: "b", Algorithm: purpose.ES384, Operations: []purpose.Operation{purpose.Sign}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(svc.Handler())
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := keyv1alpha1connect.NewKeyStoreServiceClient(srv.Client(), srv.URL)
	ensure(t, c, "a", "ES384")
	ensure(t, c, "b", "ES384")
	var pe *software.Problems
	if _, err := software.New(software.Options{StateDir: state, Purposes: []purpose.Declaration{{Name: "a", Algorithm: purpose.ES384}}}); !errors.As(err, &pe) || !strings.Contains(err.Error(), "not the file of a declared purpose") {
		t.Errorf("a key of an undeclared purpose: %v", err)
	}
	if _, err := software.New(software.Options{StateDir: state, Purposes: []purpose.Declaration{{Name: "a", Algorithm: purpose.ES256}, {Name: "b", Algorithm: purpose.ES384}}}); !errors.As(err, &pe) || !strings.Contains(err.Error(), "never re-made") {
		t.Errorf("a key of another algorithm than the declared one: %v", err)
	}
}

// The kit of the port passes against the library, in-process. Every case runs but the one that needs a store that attests.
func TestConformancePassesAgainstItself(t *testing.T) {
	r := startWith(t, software.Options{Environment: "development", Purposes: conformance.Declaration()})
	var out bytes.Buffer
	if err := conformance.Run(context.Background(), r.client, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "15 cases, 14 ok, 0 failed, 1 skipped") || !strings.Contains(out.String(), "skip the PIN release key") {
		t.Fatalf("report: %s", out.String())
	}
}

// capture is a slog handler that records the messages of the records it gets.
type capture struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (c *capture) Enabled(context.Context, slog.Level) bool { return true }
func (c *capture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recs = append(c.recs, r)
	return nil
}
func (c *capture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *capture) WithGroup(string) slog.Handler      { return c }
func (c *capture) has(value string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.recs {
		found := false
		r.Attrs(func(a slog.Attr) bool {
			if a.Value.String() == value {
				found = true
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}

func (c *capture) warned() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.recs {
		if r.Level == slog.LevelWarn && strings.Contains(r.Message, "staging") {
			return true
		}
	}
	return false
}

func TestStagingStartsWithAWarning(t *testing.T) {
	h := &capture{}
	old := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(old) })
	r := start(t, "staging")
	ensure(t, r.client, "token-signing", "ES384") // it serves
	if !h.warned() {
		t.Fatal("no warning at the start in staging")
	}
}

// The floor is the host's choice: with it on, production serves nothing and opens nothing.
func TestProductionServesNothingWhenTheFloorIsOn(t *testing.T) {
	base := shortDir(t)
	served := false
	host := software.Host{Serve: func(context.Context, string, http.Handler) error { served = true; return nil }}
	err := software.Run(context.Background(), software.Options{Environment: "production", StateDir: filepath.Join(base, "state"), RefuseProduction: true, Purposes: purpose.Reference()}, host)
	if !errors.Is(err, software.ErrProduction) || served {
		t.Fatalf("production with the floor: err=%v served=%v", err, served)
	}
	if _, err := os.Lstat(filepath.Join(base, "state")); err == nil {
		t.Fatal("the state directory was made")
	}
}

// Without the floor (atoom's own main), production runs: a consumer may keep a key in a file.
func TestProductionRunsWithoutTheFloor(t *testing.T) {
	r := startWith(t, software.Options{Environment: "production"})
	if !ensure(t, r.client, "token-signing", "ES384").GetCreated() {
		t.Fatal("no key was made")
	}
}

func TestTheFloorNamesTheEnvironments(t *testing.T) {
	for _, env := range []string{"", "development", "staging"} {
		if err := software.Floor(env, true); err != nil {
			t.Errorf("Floor(%q): %v", env, err)
		}
	}
	if err := software.Floor("production", true); !errors.Is(err, software.ErrProduction) {
		t.Errorf("Floor(production, true): %v", err)
	}
	if err := software.Floor("production", false); err != nil {
		t.Errorf("Floor(production, false): %v", err)
	}
	if err := software.Floor("prod", false); !errors.Is(err, software.ErrEnvironment) {
		t.Errorf("Floor(prod): %v", err)
	}
}

func TestARefusedStateDirectoryIsAProblemsError(t *testing.T) {
	base := shortDir(t)
	state := filepath.Join(base, "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(state, 0o755); err != nil { //nolint:gosec // the point of the test
		t.Fatal(err)
	}
	served := false
	host := software.Host{Serve: func(context.Context, string, http.Handler) error { served = true; return nil }}
	err := software.Run(context.Background(), software.Options{Environment: "development", StateDir: state, Purposes: purpose.Reference()}, host)
	var pe *software.Problems
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), state) || served {
		t.Fatalf("0755 state directory: err=%v served=%v", err, served)
	}
	if err := software.Check(state, purpose.Reference()); !errors.As(err, &pe) {
		t.Fatalf("Check of a 0755 state directory: %v", err)
	}
}

// The host names its callers: the journal line of a refusal carries the name.
func TestTheCallerIsNamedInTheJournal(t *testing.T) {
	h := &capture{}
	old := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(old) })
	r := startWith(t, software.Options{Environment: "development", Caller: func(context.Context) string { return "a-host-caller" }})
	_, err := r.client.Ensure(context.Background(), connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: "nope", Algorithm: "ES384"}))
	wantRefused(t, err, connect.CodeInvalidArgument, "purpose")
	if !h.has("a-host-caller") {
		t.Fatal("the refusal was logged without the caller")
	}
}

// A software store takes no part in the PIN release: it is never locked and attests nothing, and says so by reason.
func TestThePINReleaseCallsAreRefusedByReason(t *testing.T) {
	r := start(t, "development")
	ctx := context.Background()
	_, err := r.client.Attest(ctx, connect.NewRequest(&keyv1alpha1.AttestRequest{Purpose: "pin-release"}))
	wantRefused(t, err, connect.CodeFailedPrecondition, "no_attestation")
	_, err = r.client.VerifyAttestation(ctx, connect.NewRequest(&keyv1alpha1.VerifyAttestationRequest{}))
	wantRefused(t, err, connect.CodeFailedPrecondition, "no_attestation")
	_, err = r.client.BeginPinRelease(ctx, connect.NewRequest(&keyv1alpha1.BeginPinReleaseRequest{}))
	wantRefused(t, err, connect.CodeFailedPrecondition, "unlocked")
	_, err = r.client.SealPin(ctx, connect.NewRequest(&keyv1alpha1.SealPinRequest{}))
	wantRefused(t, err, connect.CodeFailedPrecondition, "no_pin")
	_, err = r.client.FinishPinRelease(ctx, connect.NewRequest(&keyv1alpha1.FinishPinReleaseRequest{}))
	wantRefused(t, err, connect.CodeFailedPrecondition, "unlocked")
}

// The key_id names the key that signed: the hex SHA-256 of its public key (the whole id for a key in a file), the same in Sign and in
// PublicKey, and another for another key. The signatures are verified and never compared byte for byte: the driver signs with RFC 6979
// nonces, which is its own business and not the port's, so no test of any driver may depend on the bytes.
func TestTheKeyIDIsTheFingerprintOfThePublicKey(t *testing.T) {
	r := start(t, "development")
	e := ensure(t, r.client, "token-signing", "ES384")
	want := fmt.Sprintf("%x", sha256.Sum256(e.GetSpkiDer()))
	pub, err := r.client.PublicKey(context.Background(), connect.NewRequest(&keyv1alpha1.PublicKeyRequest{Purpose: "token-signing"}))
	if err != nil || pub.Msg.GetKeyId() != want {
		t.Fatalf("PublicKey key_id %q, want %q (%v)", pub.Msg.GetKeyId(), want, err)
	}
	d := sha512.Sum384([]byte("x"))
	res, err := r.client.Sign(context.Background(), connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: "token-signing", Algorithm: "ES384", Digest: d[:]}))
	if err != nil || res.Msg.GetKeyId() != want {
		t.Fatalf("Sign key_id %q, want %q (%v)", res.Msg.GetKeyId(), want, err)
	}
	ensure(t, r.client, "ssh-user", "ES384")
	other, _ := r.client.PublicKey(context.Background(), connect.NewRequest(&keyv1alpha1.PublicKeyRequest{Purpose: "ssh-user"}))
	if other.Msg.GetKeyId() == want || other.Msg.GetKeyId() == "" {
		t.Fatalf("another key has key_id %q", other.Msg.GetKeyId())
	}
}
