package awskms_test

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha512"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/truvity/atoom/common/refusal"
	"github.com/truvity/atoom/key/awskms"
	"github.com/truvity/atoom/key/awskms/internal/kmsfake"
	"github.com/truvity/atoom/key/conformance"
	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
	"github.com/truvity/atoom/key/gen/atoom/key/v1alpha1/keyv1alpha1connect"
)

// kmsClient is a KMS client of the real AWS SDK that talks to the fake over HTTP.
func kmsClient(t *testing.T, f *kmsfake.Fake) *kms.Client {
	t.Helper()
	client, stop := f.Serve()
	t.Cleanup(stop)
	return client
}

// start serves a driver over the fake on an in-process HTTP server and returns a client of the key-store service, as a consumer that links
// the driver does.
func start(t *testing.T, f *kmsfake.Fake, opts awskms.Options) (keyv1alpha1connect.KeyStoreServiceClient, *awskms.Service) {
	t.Helper()
	if opts.KMS == nil {
		opts.KMS = kmsClient(t, f)
	}
	svc, err := awskms.New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle(svc.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return keyv1alpha1connect.NewKeyStoreServiceClient(srv.Client(), srv.URL), svc
}

func ensure(t *testing.T, c keyv1alpha1connect.KeyStoreServiceClient, purpose, alg string) *keyv1alpha1.EnsureResponse {
	t.Helper()
	res, err := c.Ensure(context.Background(), connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: purpose, Algorithm: alg}))
	if err != nil {
		t.Fatalf("Ensure(%s, %s): %v", purpose, alg, err)
	}
	return res.Msg
}

// want fails the test unless err is the refusal with the code and the reason.
func want(t *testing.T, err error, code connect.Code, reason string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want %s/%s, got success", code, reason)
	}
	if got := connect.CodeOf(err); got != code {
		t.Fatalf("want code %s, got %s: %v", code, got, err)
	}
	if got, _ := refusal.Reason(err); got != reason {
		t.Fatalf("want reason %q, got %q", reason, got)
	}
}

func digest384() []byte {
	d := sha512.Sum384([]byte("atoom awskms"))
	return d[:]
}

// TestConformance runs the key kit of the port against the driver over the fake KMS, through the driver's Connect handler.
func TestConformance(t *testing.T) {
	f := kmsfake.New()
	c, _ := start(t, f, awskms.Options{CreateKeys: true})
	var out bytes.Buffer
	err := conformance.Run(context.Background(), c, &out)
	t.Log("\n" + out.String())
	if err != nil {
		t.Fatal(err)
	}
	// The case of the PIN release needs a store that attests, and this one says it does not: the kit skips it and says so.
	if !strings.Contains(out.String(), "skip the PIN release key signs no digest of the caller's: the store does not attest") {
		t.Fatalf("the kit did not skip the PIN release case for a store that does not attest:\n%s", out.String())
	}
	if strings.Contains(out.String(), "FAIL") {
		t.Fatalf("a case failed:\n%s", out.String())
	}
}

func TestCapabilitiesDeclareWhatIsOffered(t *testing.T) {
	c, _ := start(t, kmsfake.New(), awskms.Options{})
	res, err := c.Capabilities(context.Background(), connect.NewRequest(&keyv1alpha1.CapabilitiesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Msg.GetStore() != "aws-kms" || res.Msg.GetAttests() {
		t.Fatalf("capabilities %v: want store aws-kms and attests false", res.Msg)
	}
	for _, a := range []string{"ES384", "ES256", "RS256", "ECDH-P384"} {
		found := false
		for _, have := range res.Msg.GetAlgorithms() {
			found = found || have == a
		}
		if !found {
			t.Errorf("algorithms %v do not list %s", res.Msg.GetAlgorithms(), a)
		}
	}
}

// The calls that a KMS key cannot serve answer unimplemented.
func TestUnofferedCallsAreUnimplemented(t *testing.T) {
	c, _ := start(t, kmsfake.New(), awskms.Options{})
	ctx := context.Background()
	_, err := c.Attest(ctx, connect.NewRequest(&keyv1alpha1.AttestRequest{Purpose: "token-signing"}))
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Errorf("Attest: %v", err)
	}
	_, err = c.VerifyAttestation(ctx, connect.NewRequest(&keyv1alpha1.VerifyAttestationRequest{}))
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Errorf("VerifyAttestation: %v", err)
	}
	_, err = c.BeginPinRelease(ctx, connect.NewRequest(&keyv1alpha1.BeginPinReleaseRequest{}))
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Errorf("BeginPinRelease: %v", err)
	}
	_, err = c.SealPin(ctx, connect.NewRequest(&keyv1alpha1.SealPinRequest{}))
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Errorf("SealPin: %v", err)
	}
	_, err = c.FinishPinRelease(ctx, connect.NewRequest(&keyv1alpha1.FinishPinReleaseRequest{}))
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Errorf("FinishPinRelease: %v", err)
	}
	// The key of a card's PIN release is not held.
	_, err = c.Ensure(ctx, connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: "pin-release", Algorithm: "ES384"}))
	want(t, err, connect.CodeInvalidArgument, "purpose")
}

// Ensure makes the key under the alias with the tags, once; a second driver (a restart) finds it and makes nothing.
func TestEnsureMakesOneKeyUnderItsAliasWithTags(t *testing.T) {
	f := kmsfake.New()
	c, svc := start(t, f, awskms.Options{CreateKeys: true, AliasPrefix: "site/prod"})
	first := ensure(t, c, "token-signing", "ES384")
	if !first.GetCreated() || first.GetCreatedAt() == nil {
		t.Fatalf("the first Ensure: created %v, created_at %v", first.GetCreated(), first.GetCreatedAt())
	}
	if again := ensure(t, c, "token-signing", "ES384"); again.GetCreated() || !bytes.Equal(again.GetSpkiDer(), first.GetSpkiDer()) {
		t.Fatal("a second Ensure made a key or returned another public key")
	}
	if got := svc.Alias("token-signing"); got != "alias/site/prod/token-signing" {
		t.Fatalf("alias %q", got)
	}
	k := f.KeyByAlias("alias/site/prod/token-signing")
	if k == nil || k.Spec != "ECC_NIST_P384" || k.Usage != "SIGN_VERIFY" {
		t.Fatalf("the key under the alias: %+v", k)
	}
	if k.Tags[awskms.TagPurpose] != "token-signing" || k.Tags[awskms.TagAlgorithm] != "ES384" || k.Tags[awskms.TagManagedBy] == "" {
		t.Fatalf("the tags of the key: %v", k.Tags)
	}
	if len(f.Keys()) != 1 {
		t.Fatalf("%d keys in KMS, want 1", len(f.Keys()))
	}
	// A new driver on the same KMS (a restart, or another Lambda instance) finds the key.
	c2, _ := start(t, f, awskms.Options{CreateKeys: true, AliasPrefix: "site/prod", KMS: nil})
	restarted := ensure(t, c2, "token-signing", "ES384")
	if restarted.GetCreated() || !bytes.Equal(restarted.GetSpkiDer(), first.GetSpkiDer()) || len(f.Keys()) != 1 {
		t.Fatalf("the restarted driver made a key or found another: created %v, %d keys", restarted.GetCreated(), len(f.Keys()))
	}
}

// The key of each algorithm is the KMS key of its spec and usage.
func TestKeySpecsAndUsages(t *testing.T) {
	f := kmsfake.New()
	c, svc := start(t, f, awskms.Options{CreateKeys: true})
	for purpose, tc := range map[string]struct{ alg, spec, usage string }{
		"token-signing":       {"ES384", "ECC_NIST_P384", "SIGN_VERIFY"},
		"audit-log":           {"ES256", "ECC_NIST_P256", "SIGN_VERIFY"},
		"token-signing-rs256": {"RS256", "RSA_2048", "SIGN_VERIFY"},
		"wrapping":            {"ECDH-P384", "ECC_NIST_P384", "KEY_AGREEMENT"},
	} {
		ensure(t, c, purpose, tc.alg)
		k := f.KeyByAlias(svc.Alias(purpose))
		if k == nil || k.Spec != tc.spec || k.Usage != tc.usage {
			t.Errorf("%s: the KMS key is %+v, want %s %s", purpose, k, tc.spec, tc.usage)
		}
	}
}

// Without CreateKeys the driver makes nothing: an absent key is not_found, and a key that the deployment made is used.
func TestKeysMadeByTheDeployment(t *testing.T) {
	f := kmsfake.New()
	c, _ := start(t, f, awskms.Options{})
	_, err := c.Ensure(context.Background(), connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: "token-signing", Algorithm: "ES384"}))
	want(t, err, connect.CodeNotFound, "absent")
	_, err = c.Sign(context.Background(), connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: "token-signing", Digest: digest384(), Algorithm: "ES384"}))
	want(t, err, connect.CodeNotFound, "absent")
	_, err = c.PublicKey(context.Background(), connect.NewRequest(&keyv1alpha1.PublicKeyRequest{Purpose: "token-signing"}))
	want(t, err, connect.CodeNotFound, "absent")
	if len(f.Keys()) != 0 || f.Calls("CreateKey") != 0 {
		t.Fatal("a driver that may not make keys made one")
	}
	_, err = f.AddKey("alias/atoom/token-signing", "ECC_NIST_P384", "SIGN_VERIFY")
	if err != nil {
		t.Fatal(err)
	}
	res := ensure(t, c, "token-signing", "ES384")
	if res.GetCreated() {
		t.Fatal("Ensure says it made a key that the deployment made")
	}
	pub, err := x509.ParsePKIXPublicKey(res.GetSpkiDer())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pub.(*ecdsa.PublicKey); !ok || res.GetCreatedAt() == nil {
		t.Fatalf("public key %T, created_at %v", pub, res.GetCreatedAt())
	}
}

// A host that names the key of a purpose is served by that key, found once by its ARN; an alias that is moved afterwards is not followed.
func TestKeyIDsAreUsedAndKept(t *testing.T) {
	f := kmsfake.New()
	k1, _ := f.AddKey("", "ECC_NIST_P384", "SIGN_VERIFY")
	c, _ := start(t, f, awskms.Options{CreateKeys: true, KeyIDs: map[string]string{"token-signing": k1.ARN()}})
	first := ensure(t, c, "token-signing", "ES384")
	if first.GetCreated() || len(f.Keys()) != 1 {
		t.Fatalf("created %v, %d keys: the named key is the deployment's", first.GetCreated(), len(f.Keys()))
	}
	// The named key is never made by the driver, even with CreateKeys: it is the deployment's to make.
	_, err := c.Ensure(context.Background(), connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: "audit-log", Algorithm: "ES256"}))
	if err != nil {
		t.Fatalf("a purpose that is not named is found or made by its alias: %v", err)
	}
	c2, _ := start(t, f, awskms.Options{CreateKeys: true, KeyIDs: map[string]string{"ssh-user": "alias/elsewhere/ssh-user"}})
	_, err = c2.Ensure(context.Background(), connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: "ssh-user", Algorithm: "ES384"}))
	want(t, err, connect.CodeNotFound, "absent")
	if f.Calls("CreateKey") != 1 {
		t.Fatalf("CreateKey was called %d times, want 1 (the audit-log key)", f.Calls("CreateKey"))
	}
}

// The key and its public key are read once and kept: signing again calls KMS Sign and nothing that reads a key.
func TestKeyIsReadOnce(t *testing.T) {
	f := kmsfake.New()
	c, _ := start(t, f, awskms.Options{CreateKeys: true})
	ensure(t, c, "token-signing", "ES384")
	d, g := f.Calls("DescribeKey"), f.Calls("GetPublicKey")
	for range 3 {
		if _, err := c.Sign(context.Background(), connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: "token-signing", Digest: digest384(), Algorithm: "ES384"})); err != nil {
			t.Fatal(err)
		}
		if _, err := c.PublicKey(context.Background(), connect.NewRequest(&keyv1alpha1.PublicKeyRequest{Purpose: "token-signing"})); err != nil {
			t.Fatal(err)
		}
	}
	if f.Calls("DescribeKey") != d || f.Calls("GetPublicKey") != g || f.Calls("Sign") != 3 {
		t.Fatalf("DescribeKey %d->%d, GetPublicKey %d->%d, Sign %d: the key was read again", d, f.Calls("DescribeKey"), g, f.Calls("GetPublicKey"), f.Calls("Sign"))
	}
}

// Two drivers that make the key of one purpose at once: the alias is the lock. One keeps its key; the other schedules its own for deletion
// and answers the winner's public key.
func TestTwoDriversMakeTheKeyAtOnce(t *testing.T) {
	f := kmsfake.New()
	var gate sync.WaitGroup
	gate.Add(2)
	f.SetHook(func(op string) {
		if op == "CreateAlias" {
			// Both callers hold their new key and wait for each other before they name it.
			gate.Done()
			gate.Wait()
		}
	})
	cA, _ := start(t, f, awskms.Options{CreateKeys: true})
	cB, _ := start(t, f, awskms.Options{CreateKeys: true})
	var wg sync.WaitGroup
	results := make([]*keyv1alpha1.EnsureResponse, 2)
	for i, c := range []keyv1alpha1connect.KeyStoreServiceClient{cA, cB} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = ensure(t, c, "token-signing", "ES384")
		}()
	}
	wg.Wait()
	if results[0].GetCreated() == results[1].GetCreated() {
		t.Fatalf("created %v and %v: exactly one of two racing Ensure calls makes the key", results[0].GetCreated(), results[1].GetCreated())
	}
	if !bytes.Equal(results[0].GetSpkiDer(), results[1].GetSpkiDer()) {
		t.Fatal("the two callers got different public keys")
	}
	var live, pending int
	for _, k := range f.Keys() {
		switch k.State {
		case "Enabled":
			live++
		case "PendingDeletion":
			pending++
			if k.DeletionDays != 7 {
				t.Errorf("the lost key waits %d days, want the shortest, 7", k.DeletionDays)
			}
		}
	}
	if live != 1 || pending != 1 {
		t.Fatalf("%d live and %d pending keys, want 1 and 1", live, pending)
	}
}

// A key under the alias that has another algorithm is refused, and never replaced.
func TestExistingKeyOfAnotherAlgorithmIsRefused(t *testing.T) {
	f := kmsfake.New()
	if _, err := f.AddKey("alias/atoom/token-signing", "ECC_NIST_P256", "SIGN_VERIFY"); err != nil {
		t.Fatal(err)
	}
	c, _ := start(t, f, awskms.Options{CreateKeys: true})
	_, err := c.Ensure(context.Background(), connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: "token-signing", Algorithm: "ES384"}))
	want(t, err, connect.CodeFailedPrecondition, "algorithm")
	_, err = c.Sign(context.Background(), connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: "token-signing", Digest: digest384(), Algorithm: "ES384"}))
	want(t, err, connect.CodeFailedPrecondition, "algorithm")
	if len(f.Keys()) != 1 || f.Calls("CreateKey") != 0 {
		t.Fatal("the driver replaced a key")
	}
	// A key-agreement key under the alias of a signing purpose is another shape too.
	if _, err := f.AddKey("alias/atoom/ssh-user", "ECC_NIST_P384", "KEY_AGREEMENT"); err != nil {
		t.Fatal(err)
	}
	_, err = c.Ensure(context.Background(), connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: "ssh-user", Algorithm: "ES384"}))
	want(t, err, connect.CodeFailedPrecondition, "algorithm")
}

// A key that exists and is not enabled is not used, and the caller is told only the word store.
func TestKeyInAStateThatRefusesUse(t *testing.T) {
	for _, state := range []string{"Disabled", "PendingDeletion"} {
		f := kmsfake.New()
		k, _ := f.AddKey("alias/atoom/token-signing", "ECC_NIST_P384", "SIGN_VERIFY")
		f.SetState(k.ID, state)
		c, _ := start(t, f, awskms.Options{CreateKeys: true})
		_, err := c.Ensure(context.Background(), connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: "token-signing", Algorithm: "ES384"}))
		want(t, err, connect.CodeFailedPrecondition, "store")
		if strings.Contains(err.Error(), state) {
			t.Errorf("the refusal names the state %q: %v", state, err)
		}
		if f.Calls("CreateKey") != 0 {
			t.Errorf("%s: the driver made a key to replace one it cannot use", state)
		}
	}
}

// What KMS says goes to the journal; the caller gets a code and the word store.
func TestKMSErrorsBecomeRefusals(t *testing.T) {
	for _, tc := range []struct {
		kmsCode string
		want    connect.Code
	}{
		{"ThrottlingException", connect.CodeUnavailable},
		{"KMSInternalException", connect.CodeUnavailable},
		{"DisabledException", connect.CodeFailedPrecondition},
		{"AccessDeniedException", connect.CodeInternal},
	} {
		f := kmsfake.New()
		c, _ := start(t, f, awskms.Options{CreateKeys: true})
		ensure(t, c, "token-signing", "ES384")
		f.Fail("Sign", tc.kmsCode)
		_, err := c.Sign(context.Background(), connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: "token-signing", Digest: digest384(), Algorithm: "ES384"}))
		want(t, err, tc.want, "store")
		if strings.Contains(err.Error(), tc.kmsCode) {
			t.Errorf("the refusal carries what KMS said: %v", err)
		}
	}
}

// The checks the driver can make itself are made before KMS is called.
func TestRefusalsBeforeKMS(t *testing.T) {
	f := kmsfake.New()
	c, _ := start(t, f, awskms.Options{CreateKeys: true})
	ctx := context.Background()
	ensure(t, c, "token-signing", "ES384")
	ensure(t, c, "wrapping", "ECDH-P384")
	signs := f.Calls("Sign")
	_, err := c.Sign(ctx, connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: "token-signing", Digest: digest384()[:47], Algorithm: "ES384"}))
	want(t, err, connect.CodeInvalidArgument, "digest")
	_, err = c.Sign(ctx, connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: "token-signing", Digest: digest384(), Algorithm: "ES256"}))
	want(t, err, connect.CodeInvalidArgument, "algorithm")
	_, err = c.Sign(ctx, connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: "wrapping", Digest: digest384(), Algorithm: "ES384"}))
	want(t, err, connect.CodeFailedPrecondition, "algorithm")
	_, err = c.Sign(ctx, connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: "nope", Digest: digest384(), Algorithm: "ES384"}))
	want(t, err, connect.CodeInvalidArgument, "purpose")
	if f.Calls("Sign") != signs {
		t.Fatal("KMS was asked to sign a request that the driver could refuse")
	}
	derives := f.Calls("DeriveSharedSecret")
	eph, _ := ecdh.P384().GenerateKey(rand.Reader)
	off := bytes.Clone(eph.PublicKey().Bytes())
	off[len(off)-1] ^= 1
	_, err = c.Ecdh(ctx, connect.NewRequest(&keyv1alpha1.EcdhRequest{Purpose: "wrapping", PeerPublic: off}))
	want(t, err, connect.CodeInvalidArgument, "peer_public")
	_, err = c.Ecdh(ctx, connect.NewRequest(&keyv1alpha1.EcdhRequest{Purpose: "token-signing", PeerPublic: eph.PublicKey().Bytes()}))
	want(t, err, connect.CodeFailedPrecondition, "algorithm")
	if f.Calls("DeriveSharedSecret") != derives {
		t.Fatal("KMS was asked to derive a secret for a request that the driver could refuse")
	}
}

// Ecdh answers the raw secret that a reference computation gives (the conformance case checks the same; this one names the encoding).
func TestEcdhIsTheRawSecret(t *testing.T) {
	f := kmsfake.New()
	c, _ := start(t, f, awskms.Options{CreateKeys: true})
	pubDER := ensure(t, c, "wrapping", "ECDH-P384").GetSpkiDer()
	parsed, err := x509.ParsePKIXPublicKey(pubDER)
	if err != nil {
		t.Fatal(err)
	}
	driverPub, _ := parsed.(*ecdsa.PublicKey).ECDH()
	eph, _ := ecdh.P384().GenerateKey(rand.Reader)
	ref, _ := eph.ECDH(driverPub)
	res, err := c.Ecdh(context.Background(), connect.NewRequest(&keyv1alpha1.EcdhRequest{Purpose: "wrapping", PeerPublic: eph.PublicKey().Bytes()}))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Msg.GetSharedSecret()) != 48 || !bytes.Equal(res.Msg.GetSharedSecret(), ref) {
		t.Fatalf("the shared secret is %d bytes and differs from the reference", len(res.Msg.GetSharedSecret()))
	}
}

func TestNewRefusesWhatKMSWouldRefuse(t *testing.T) {
	f := kmsfake.New()
	client := kmsClient(t, f)
	for _, prefix := range []string{"aws", "aws/x", "a b", "/x", "x/", "x//y", "é"} {
		if _, err := awskms.New(awskms.Options{KMS: client, AliasPrefix: prefix}); !errors.Is(err, awskms.ErrAliasPrefix) {
			t.Errorf("prefix %q: %v", prefix, err)
		}
	}
	for _, prefix := range []string{"", "atoom", "site/prod", "a_b-c/d"} {
		if _, err := awskms.New(awskms.Options{KMS: client, AliasPrefix: prefix}); err != nil {
			t.Errorf("prefix %q: %v", prefix, err)
		}
	}
	if _, err := awskms.New(awskms.Options{}); err == nil {
		t.Error("New without a KMS client succeeded")
	}
	if _, err := awskms.New(awskms.Options{KMS: client, KeyIDs: map[string]string{"pin-release": "alias/x"}}); err == nil {
		t.Error("New took a key for a purpose that the driver does not hold")
	}
	if _, err := awskms.New(awskms.Options{KMS: client, KeyIDs: map[string]string{"token-signing": ""}}); err == nil {
		t.Error("New took an empty key")
	}
}

// Run serves the handler on the host's server.
func TestRunServesOnTheHost(t *testing.T) {
	f := kmsfake.New()
	served := make(chan string, 1)
	err := awskms.Run(context.Background(), awskms.Options{KMS: kmsClient(t, f)}, awskms.Host{
		Serve: func(_ context.Context, path string, h http.Handler) error {
			if h == nil {
				t.Error("no handler")
			}
			served <- path
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if p := <-served; !strings.HasSuffix(p, "/") || !strings.Contains(p, "KeyStoreService") {
		t.Fatalf("served at %q", p)
	}
	if err := awskms.Run(context.Background(), awskms.Options{KMS: kmsClient(t, f)}, awskms.Host{}); err == nil {
		t.Fatal("Run without Serve succeeded")
	}
}

// KMS is eventually consistent: the calls that follow CreateKey may not see the key for a moment, and the driver waits and asks again.
func TestANewKeyThatIsNotVisibleYet(t *testing.T) {
	f := kmsfake.New()
	f.Lag("CreateAlias", 2)
	f.Lag("GetPublicKey", 2)
	c, _ := start(t, f, awskms.Options{CreateKeys: true})
	res := ensure(t, c, "token-signing", "ES384")
	if !res.GetCreated() || len(f.Keys()) != 1 {
		t.Fatalf("created %v, %d keys", res.GetCreated(), len(f.Keys()))
	}
	if f.Calls("CreateAlias") != 3 || f.Calls("GetPublicKey") != 3 {
		t.Fatalf("CreateAlias %d times and GetPublicKey %d times, want 3 and 3", f.Calls("CreateAlias"), f.Calls("GetPublicKey"))
	}
}
