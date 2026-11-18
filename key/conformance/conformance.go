package conformance

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
	"io"
	"slices"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/common/refusal"
	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
	"github.com/truvity/atoom/key/gen/atoom/key/v1alpha1/keyv1alpha1connect"
)

// Client is the key-store service as a client sees it.
type Client = keyv1alpha1connect.KeyStoreServiceClient

// The algorithms of the contract.
const (
	ES384    = "ES384"     // ECDSA over P-384 with SHA-384 (RFC 7518, section 3.4)
	ES256    = "ES256"     // ECDSA over P-256 with SHA-256; the audit-log purpose only
	RS256    = "RS256"     // RSASSA-PKCS1-v1_5 over SHA-256 with an RSA-2048 key; the token-signing-rs256 purpose only
	ECDHP384 = "ECDH-P384" // ECDH over P-384 (NIST SP 800-56A)
)

// The reasons of the refusals of the contract (a closed list).
const (
	reasonPurpose   = "purpose"
	reasonAlgorithm = "algorithm"
	reasonDigest    = "digest"
	reasonPeer      = "peer_public"
)

// Case is one case of the suite. A case that needs something the driver may not offer names it, and is skipped when the driver's
// Capabilities say it does not.
type Case struct {
	Name string
	// Algorithm, when not empty, is the algorithm the case needs: the case is skipped when Capabilities does not list it.
	Algorithm string
	// Attests says that the case needs a store that attests where its keys live and takes part in the PIN release.
	Attests bool
	Run     func(ctx context.Context, c Client) error
}

// Result is how one case ended.
type Result struct {
	Name    string
	Skipped string // why, when the case was skipped
	Err     error  // why, when it failed
}

// Run runs the suite against c and writes one line for each case to out.
func Run(ctx context.Context, c Client, out io.Writer) error {
	return RunCases(ctx, c, Suite(), out)
}

// RunCases runs the cases in order after one Capabilities call that decides which are skipped. A failed case fails the run, and the run
// goes on so that the report is complete. A run in which no case ran fails.
func RunCases(ctx context.Context, c Client, cases []Case, out io.Writer) error {
	results := Results(ctx, c, cases)
	var failed, skipped, ran int
	var names []string
	for _, r := range results {
		switch {
		case r.Err != nil:
			_, _ = fmt.Fprintf(out, "FAIL %s: %v\n", r.Name, r.Err)
			failed++
			names = append(names, r.Name)
		case r.Skipped != "":
			_, _ = fmt.Fprintf(out, "skip %s: %s\n", r.Name, r.Skipped)
			skipped++
		default:
			_, _ = fmt.Fprintf(out, "ok   %s\n", r.Name)
			ran++
		}
	}
	_, _ = fmt.Fprintf(out, "%d cases, %d ok, %d failed, %d skipped\n", len(results), ran, failed, skipped)
	switch {
	case failed > 0:
		return fmt.Errorf("conformance: failed: %v", names)
	case ran == 0:
		return errors.New("conformance: no case ran")
	}
	return nil
}

// Results runs the cases and returns how each ended, in order.
func Results(ctx context.Context, c Client, cases []Case) []Result {
	caps, capErr := c.Capabilities(ctx, connect.NewRequest(&keyv1alpha1.CapabilitiesRequest{}))
	out := make([]Result, 0, len(cases))
	for _, tc := range cases {
		r := Result{Name: tc.Name}
		switch {
		case tc.Name == capabilitiesCase:
			// Capabilities is the one case that runs whatever it says.
		case capErr != nil:
			r.Err = fmt.Errorf("the Capabilities call: %w", capErr)
			out = append(out, r)
			continue
		case tc.Algorithm != "" && !slices.Contains(caps.Msg.GetAlgorithms(), tc.Algorithm):
			r.Skipped = "the store does not list " + tc.Algorithm
			out = append(out, r)
			continue
		case tc.Attests && !caps.Msg.GetAttests():
			r.Skipped = "the store does not attest"
			out = append(out, r)
			continue
		}
		r.Err = tc.Run(ctx, c)
		out = append(out, r)
	}
	return out
}

const capabilitiesCase = "capabilities"

// Suite returns the cases of the key port.
func Suite() []Case {
	return []Case{
		{Name: capabilitiesCase, Run: caseCapabilities},
		{Name: "ensure, sign and verify (token-signing, ES384)", Algorithm: ES384, Run: caseSign384},
		{Name: "audit-log key is P-256 and signs SHA-256 (ES256)", Algorithm: ES256, Run: caseSign256},
		{Name: "RS256 key is RSA-2048 and signs PKCS#1 v1.5 over SHA-256", Algorithm: RS256, Run: caseSignRS256},
		{Name: "the PIN release key signs no digest of the caller's", Algorithm: ES384, Attests: true, Run: casePinRelease},
		{Name: "public key", Algorithm: ES384, Run: casePublicKey},
		{Name: "ecdh against a reference", Algorithm: ECDHP384, Run: caseEcdh},
		{Name: "unknown purpose is refused", Algorithm: ES384, Run: caseUnknownPurpose},
		{Name: "unknown algorithm is refused", Algorithm: ES384, Run: caseUnknownAlgorithm},
		{Name: "a digest of the wrong length is refused", Algorithm: ES384, Run: caseDigest},
		{Name: "the algorithm of another purpose is refused", Algorithm: ES256, Run: caseOtherAlgorithm},
	}
}

func caseCapabilities(ctx context.Context, c Client) error {
	res, err := c.Capabilities(ctx, connect.NewRequest(&keyv1alpha1.CapabilitiesRequest{}))
	if err != nil {
		return err
	}
	if res.Msg.GetStore() == "" {
		return errors.New("the store has no name")
	}
	algs := res.Msg.GetAlgorithms()
	if len(algs) == 0 {
		return errors.New("the store lists no algorithm")
	}
	for _, a := range algs {
		if !slices.Contains([]string{ES384, ES256, RS256, ECDHP384}, a) {
			return fmt.Errorf("the store lists %q, which is not an algorithm of the contract", a)
		}
	}
	return nil
}

// ensure makes the key of a purpose when it is absent and returns the response.
func ensure(ctx context.Context, c Client, purpose, alg string) (*keyv1alpha1.EnsureResponse, error) {
	res, err := c.Ensure(ctx, connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: purpose, Algorithm: alg}))
	if err != nil {
		return nil, fmt.Errorf("ensure %s: %w", purpose, err)
	}
	return res.Msg, nil
}

func sign(ctx context.Context, c Client, purpose, alg string, digest []byte) (*keyv1alpha1.SignResponse, error) {
	res, err := c.Sign(ctx, connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: purpose, Digest: digest, Algorithm: alg}))
	if err != nil {
		return nil, fmt.Errorf("sign %s: %w", purpose, err)
	}
	return res.Msg, nil
}

func caseSign384(ctx context.Context, c Client) error {
	first, err := ensure(ctx, c, "token-signing", ES384)
	if err != nil {
		return err
	}
	again, err := ensure(ctx, c, "token-signing", ES384)
	if err != nil {
		return err
	}
	if again.GetCreated() || !bytes.Equal(first.GetSpkiDer(), again.GetSpkiDer()) {
		return errors.New("a second Ensure made a key or returned another public key")
	}
	pub, err := parseECDSA(again.GetSpkiDer(), "P-384")
	if err != nil {
		return err
	}
	digest := sha512.Sum384([]byte("atoom key conformance"))
	sig, err := sign(ctx, c, "token-signing", ES384, digest[:])
	if err != nil {
		return err
	}
	if !ecdsa.VerifyASN1(pub, digest[:], sig.GetSignatureDer()) {
		return errors.New("the signature does not verify with the public key")
	}
	return nil
}

// caseSign256 is the exception to P-384 for the audit-log purpose: a P-256 key that signs SHA-256 digests (the C2SP signed-note
// signature type 0x02), which refuses the SHA-384 form of every other purpose.
func caseSign256(ctx context.Context, c Client) error {
	ens, err := ensure(ctx, c, "audit-log", ES256)
	if err != nil {
		return err
	}
	pub, err := parseECDSA(ens.GetSpkiDer(), "P-256")
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte("atoom key conformance"))
	sig, err := sign(ctx, c, "audit-log", ES256, digest[:])
	if err != nil {
		return err
	}
	if !ecdsa.VerifyASN1(pub, digest[:], sig.GetSignatureDer()) {
		return errors.New("the signature does not verify with the public key")
	}
	if slices.Contains(capabilities(ctx, c), ES384) {
		d384 := sha512.Sum384([]byte("atoom key conformance"))
		_, err = c.Sign(ctx, connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: "audit-log", Digest: d384[:], Algorithm: ES384}))
		if err := wantRefusal(err, connect.CodeInvalidArgument, reasonAlgorithm); err != nil {
			return err
		}
	}
	return nil
}

// capabilities lists the algorithms of the store; nothing when it cannot be asked (the run reports that elsewhere).
func capabilities(ctx context.Context, c Client) []string {
	res, err := c.Capabilities(ctx, connect.NewRequest(&keyv1alpha1.CapabilitiesRequest{}))
	if err != nil {
		return nil
	}
	return res.Msg.GetAlgorithms()
}

// caseSignRS256 is the per-audience exception: the key of the RS256 purpose is RSA-2048 with the exponent 65537 and signs a SHA-256 digest
// with RSASSA-PKCS1-v1_5 (not PSS); it refuses the SHA-384 form and another algorithm.
func caseSignRS256(ctx context.Context, c Client) error {
	ens, err := ensure(ctx, c, "token-signing-rs256", RS256)
	if err != nil {
		return err
	}
	k, err := x509.ParsePKIXPublicKey(ens.GetSpkiDer())
	if err != nil {
		return errors.New("the public key is not SubjectPublicKeyInfo in DER")
	}
	pub, ok := k.(*rsa.PublicKey)
	if !ok || pub.N.BitLen() != 2048 || pub.E != 65537 {
		return errors.New("the public key is not RSA-2048 with the exponent 65537")
	}
	digest := sha256.Sum256([]byte("atoom key conformance"))
	sig, err := sign(ctx, c, "token-signing-rs256", RS256, digest[:])
	if err != nil {
		return err
	}
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig.GetSignatureDer()); err != nil {
		return errors.New("the signature does not verify as PKCS#1 v1.5 over SHA-256")
	}
	d384 := sha512.Sum384([]byte("atoom key conformance"))
	_, err = c.Sign(ctx, connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: "token-signing-rs256", Digest: d384[:], Algorithm: RS256}))
	if err := wantRefusal(err, connect.CodeInvalidArgument, reasonDigest); err != nil {
		return err
	}
	_, err = c.Sign(ctx, connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: "token-signing-rs256", Digest: digest[:], Algorithm: ES384}))
	return wantRefusal(err, connect.CodeInvalidArgument, reasonAlgorithm)
}

// casePinRelease: the PIN release key signs only the texts the driver writes itself (BeginPinRelease, SealPin), never a digest of
// the caller's, because it signs without the PIN.
func casePinRelease(ctx context.Context, c Client) error {
	if _, err := ensure(ctx, c, "pin-release", ES384); err != nil {
		return err
	}
	d := sha512.Sum384([]byte("atoom key conformance"))
	_, err := c.Sign(ctx, connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: "pin-release", Digest: d[:], Algorithm: ES384}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		return fmt.Errorf("sign with the pin-release key: want invalid_argument, got %s", connect.CodeOf(err))
	}
	return nil
}

func casePublicKey(ctx context.Context, c Client) error {
	ens, err := ensure(ctx, c, "token-signing", ES384)
	if err != nil {
		return err
	}
	res, err := c.PublicKey(ctx, connect.NewRequest(&keyv1alpha1.PublicKeyRequest{Purpose: "token-signing"}))
	if err != nil {
		return fmt.Errorf("public key: %w", err)
	}
	if !bytes.Equal(ens.GetSpkiDer(), res.Msg.GetSpkiDer()) {
		return errors.New("PublicKey differs from Ensure")
	}
	return nil
}

func caseEcdh(ctx context.Context, c Client) error {
	ens, err := ensure(ctx, c, "wrapping", ECDHP384)
	if err != nil {
		return err
	}
	pub, err := parseECDSA(ens.GetSpkiDer(), "P-384")
	if err != nil {
		return err
	}
	driverPub, err := pub.ECDH()
	if err != nil {
		return err
	}
	eph, err := ecdh.P384().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	want, err := eph.ECDH(driverPub)
	if err != nil {
		return err
	}
	res, err := c.Ecdh(ctx, connect.NewRequest(&keyv1alpha1.EcdhRequest{Purpose: "wrapping", PeerPublic: eph.PublicKey().Bytes()}))
	if err != nil {
		return fmt.Errorf("ecdh: %w", err)
	}
	if !bytes.Equal(want, res.Msg.GetSharedSecret()) {
		return errors.New("the shared secret differs from the reference computation")
	}
	off := bytes.Clone(eph.PublicKey().Bytes())
	off[len(off)-1] ^= 0x01 // a point off the curve
	_, err = c.Ecdh(ctx, connect.NewRequest(&keyv1alpha1.EcdhRequest{Purpose: "wrapping", PeerPublic: off}))
	return wantRefusal(err, connect.CodeInvalidArgument, reasonPeer)
}

func caseUnknownPurpose(ctx context.Context, c Client) error {
	_, err := c.Ensure(ctx, connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: "no-such-purpose", Algorithm: ES384}))
	return wantRefusal(err, connect.CodeInvalidArgument, reasonPurpose)
}

func caseUnknownAlgorithm(ctx context.Context, c Client) error {
	_, err := c.Ensure(ctx, connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: "token-signing", Algorithm: "PS256"}))
	return wantRefusal(err, connect.CodeInvalidArgument, reasonAlgorithm)
}

func caseDigest(ctx context.Context, c Client) error {
	if _, err := ensure(ctx, c, "token-signing", ES384); err != nil {
		return err
	}
	d := sha512.Sum384([]byte("x"))
	_, err := c.Sign(ctx, connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: "token-signing", Digest: d[:47], Algorithm: ES384}))
	if e := wantRefusal(err, connect.CodeInvalidArgument, reasonDigest); e != nil {
		return e
	}
	if slices.Contains(capabilities(ctx, c), ES256) {
		// A SHA-384 digest is not an audit-log digest: that key is P-256 and signs SHA-256.
		if _, err := ensure(ctx, c, "audit-log", ES256); err != nil {
			return err
		}
		_, err = c.Sign(ctx, connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: "audit-log", Digest: d[:], Algorithm: ES256}))
		return wantRefusal(err, connect.CodeInvalidArgument, reasonDigest)
	}
	return nil
}

func caseOtherAlgorithm(ctx context.Context, c Client) error {
	_, err := c.Ensure(ctx, connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: "audit-log", Algorithm: ES384}))
	return wantRefusal(err, connect.CodeFailedPrecondition, reasonAlgorithm)
}

// wantRefusal is nil when err is the refusal with the code and reason, and an error that says what came instead.
func wantRefusal(err error, code connect.Code, reason string) error {
	if err == nil {
		return fmt.Errorf("want %s/%s, got success", code, reason)
	}
	if got := connect.CodeOf(err); got != code {
		return fmt.Errorf("want code %s, got %s: %w", code, got, err)
	}
	if got, _ := refusal.Reason(err); got != reason {
		return fmt.Errorf("want reason %q, got %q", reason, got)
	}
	return nil
}

func parseECDSA(spki []byte, curve string) (*ecdsa.PublicKey, error) {
	k, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		return nil, errors.New("the public key is not SubjectPublicKeyInfo in DER")
	}
	pub, ok := k.(*ecdsa.PublicKey)
	if !ok || pub.Curve.Params().Name != curve {
		return nil, fmt.Errorf("the public key is not a %s key", curve)
	}
	return pub, nil
}
