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

	"math/big"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/common/refusal"
	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
	"github.com/truvity/atoom/key/gen/atoom/key/v1alpha1/keyv1alpha1connect"
	"github.com/truvity/atoom/key/purpose"
	"github.com/truvity/atoom/key/signature"
)

// Client is the key-store service as a client sees it.
type Client = keyv1alpha1connect.KeyStoreServiceClient

// The algorithms of the contract.
const (
	ES384    = purpose.ES384    // ECDSA over P-384 with SHA-384 (RFC 7518, section 3.4)
	ES256    = purpose.ES256    // ECDSA over P-256 with SHA-256 (RFC 7518, section 3.4)
	RS256    = purpose.RS256    // RSASSA-PKCS1-v1_5 over SHA-256 with an RSA-2048 key
	ECDHP384 = purpose.ECDHP384 // ECDH over P-384 (NIST SP 800-56A)
)

// The purposes of the kit. The kit declares its own and uses no other: a host starts the driver under test with Declaration (the command
// prints it as JSON, `key-conformance --print-declaration`), and a case that needs a purpose says which. None of the names is one that a
// driver ever held in a list, so a pass shows that the driver serves what its host declares.
const (
	PurposeES384      = "conformance-es384"      // ES384, sign
	PurposeES256      = "conformance-es256"      // ES256, sign
	PurposeRS256      = "conformance-rs256"      // RS256, sign
	PurposeAgree      = "conformance-agree"      // ECDH-P384, agree
	PurposeMadeOnly   = "conformance-made-only"  // ES384, no operation: the key is made and read and never used
	PurposeRelease    = "pin-release"            // ES384: the name a card gives its PIN release key (docs/key-port.md); an ordinary name elsewhere
	PurposeUndeclared = "conformance-undeclared" // the one name that the declaration leaves out
)

// Declaration is the declaration of the kit's purposes, for the host that starts the driver under test. The slots are those of a card
// (the spare slots 90 to 94, which the reference map of key/slotplan gives to no purpose, and the PIN release key's own 87 by default);
// the other drivers ignore them.
func Declaration() []purpose.Declaration {
	return []purpose.Declaration{
		{Name: PurposeES384, Algorithm: ES384, Operations: []purpose.Operation{purpose.Sign}, Slot: "90"},
		{Name: PurposeES256, Algorithm: ES256, Operations: []purpose.Operation{purpose.Sign}, Slot: "91"},
		{Name: PurposeRS256, Algorithm: RS256, Operations: []purpose.Operation{purpose.Sign}, Slot: "92"},
		{Name: PurposeAgree, Algorithm: ECDHP384, Operations: []purpose.Operation{purpose.Agree}, Slot: "93"},
		{Name: PurposeMadeOnly, Algorithm: ES384, Operations: []purpose.Operation{}, Slot: "94"},
		{Name: PurposeRelease, Algorithm: ES384, Operations: []purpose.Operation{}},
	}
}

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
	// Purposes are the purposes of Declaration that the case uses. When the store lists the case's algorithm, it must serve them as
	// declared, and a store that does not fails the case (the host started the driver with another declaration), whereas a store
	// that does not list the algorithm is skipped.
	Purposes []string
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
		if err := servesTheKitsPurposes(caps, tc.Purposes); err != nil {
			r.Err = err
			out = append(out, r)
			continue
		}
		r.Err = tc.Run(ctx, c)
		out = append(out, r)
	}
	return out
}

// servesTheKitsPurposes is nil when the store declares each of the purposes as the kit's Declaration has it.
func servesTheKitsPurposes(caps *connect.Response[keyv1alpha1.CapabilitiesResponse], names []string) error {
	if caps == nil || len(names) == 0 {
		return nil
	}
	served := map[string]*keyv1alpha1.PurposeInfo{}
	for _, p := range caps.Msg.GetPurposes() {
		served[p.GetName()] = p
	}
	want := map[string]purpose.Declaration{}
	for _, d := range Declaration() {
		want[d.Name] = d
	}
	for _, n := range names {
		w, got := want[n], served[n]
		var ops []string
		for _, op := range w.Operations {
			ops = append(ops, string(op))
		}
		switch {
		case got == nil:
			return fmt.Errorf("the store does not serve the purpose %s of the kit's declaration: start the driver with it (key-conformance --print-declaration)", n)
		case got.GetAlgorithm() != w.Algorithm || !slices.Equal(got.GetOperations(), ops):
			return fmt.Errorf("the store serves the purpose %s with another algorithm or operations than the kit's declaration", n)
		}
	}
	return nil
}

const capabilitiesCase = "capabilities"

// Suite returns the cases of the key port.
func Suite() []Case {
	return []Case{
		{Name: capabilitiesCase, Run: caseCapabilities},
		{Name: "ensure, sign and verify (ES384), and the raw form of a JWS", Algorithm: ES384, Purposes: []string{PurposeES384}, Run: caseSign384},
		{Name: "a host-declared ES256 purpose signs SHA-256 digests, and the raw form of a JWS", Algorithm: ES256, Purposes: []string{PurposeES256}, Run: caseSign256},
		{Name: "RS256 key is RSA-2048 and signs PKCS#1 v1.5 over SHA-256", Algorithm: RS256, Purposes: []string{PurposeRS256}, Run: caseSignRS256},
		{Name: "the PIN release key signs no digest of the caller's", Algorithm: ES384, Attests: true, Purposes: []string{PurposeRelease}, Run: casePinRelease},
		{Name: "public key", Algorithm: ES384, Purposes: []string{PurposeES384}, Run: casePublicKey},
		{Name: "ecdh against a reference", Algorithm: ECDHP384, Purposes: []string{PurposeAgree}, Run: caseEcdh},
		{Name: "an undeclared purpose is refused", Algorithm: ES384, Purposes: []string{PurposeES384}, Run: caseUndeclaredPurpose},
		{Name: "unknown algorithm is refused", Algorithm: ES384, Purposes: []string{PurposeES384}, Run: caseUnknownAlgorithm},
		{Name: "a digest of the wrong length is refused", Algorithm: ES384, Purposes: []string{PurposeES384}, Run: caseDigest},
		{Name: "the algorithm of another purpose is refused", Algorithm: ES256, Purposes: []string{PurposeES256}, Run: caseOtherAlgorithm},
		{Name: "an operation the host did not allow is refused", Algorithm: ES384, Purposes: []string{PurposeES384, PurposeMadeOnly}, Run: caseOperations},
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
	// The purposes the store says it serves are a declaration that the host could have made: no name twice, none that is not a name,
	// and an algorithm and operations that go together.
	var decls []purpose.Declaration
	for _, p := range res.Msg.GetPurposes() {
		d := purpose.Declaration{Name: p.GetName(), Algorithm: p.GetAlgorithm()}
		for _, op := range p.GetOperations() {
			d.Operations = append(d.Operations, purpose.Operation(op))
		}
		decls = append(decls, d)
	}
	if len(decls) > 0 {
		if _, err := purpose.New(decls); err != nil {
			return fmt.Errorf("the purposes of the store are not a declaration: %w", err)
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
	first, err := ensure(ctx, c, PurposeES384, ES384)
	if err != nil {
		return err
	}
	again, err := ensure(ctx, c, PurposeES384, ES384)
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
	sig, err := sign(ctx, c, PurposeES384, ES384, digest[:])
	if err != nil {
		return err
	}
	if !ecdsa.VerifyASN1(pub, digest[:], sig.GetSignatureDer()) {
		return errors.New("the signature does not verify with the public key")
	}
	return verifyRaw(ES384, pub, digest[:], sig.GetSignatureDer())
}

// verifyRaw converts the DER signature of the port to the raw r||s that a JWS carries (key/signature) and verifies that.
func verifyRaw(alg string, pub *ecdsa.PublicKey, digest, der []byte) error {
	raw, err := signature.DERToRaw(alg, der)
	if err != nil {
		return fmt.Errorf("the DER signature does not convert to the raw form: %w", err)
	}
	n := signature.Size(alg)
	if len(raw) != 2*n || !ecdsa.Verify(pub, digest, new(big.Int).SetBytes(raw[:n]), new(big.Int).SetBytes(raw[n:])) {
		return errors.New("the raw r||s form of the signature does not verify with the public key")
	}
	return nil
}

// caseSign256 is a purpose that the host declared as ES256, which no driver knew: a P-256 key that signs SHA-256 digests, which refuses
// the SHA-384 form of an ES384 purpose.
func caseSign256(ctx context.Context, c Client) error {
	ens, err := ensure(ctx, c, PurposeES256, ES256)
	if err != nil {
		return err
	}
	pub, err := parseECDSA(ens.GetSpkiDer(), "P-256")
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte("atoom key conformance"))
	sig, err := sign(ctx, c, PurposeES256, ES256, digest[:])
	if err != nil {
		return err
	}
	if !ecdsa.VerifyASN1(pub, digest[:], sig.GetSignatureDer()) {
		return errors.New("the signature does not verify with the public key")
	}
	if err := verifyRaw(ES256, pub, digest[:], sig.GetSignatureDer()); err != nil {
		return err
	}
	if slices.Contains(capabilities(ctx, c), ES384) {
		d384 := sha512.Sum384([]byte("atoom key conformance"))
		_, err = c.Sign(ctx, connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: PurposeES256, Digest: d384[:], Algorithm: ES384}))
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

// caseSignRS256 is the RSA purpose: its key is RSA-2048 with the exponent 65537 and signs a SHA-256 digest with RSASSA-PKCS1-v1_5 (not
// PSS); it refuses the SHA-384 form and another algorithm.
func caseSignRS256(ctx context.Context, c Client) error {
	ens, err := ensure(ctx, c, PurposeRS256, RS256)
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
	sig, err := sign(ctx, c, PurposeRS256, RS256, digest[:])
	if err != nil {
		return err
	}
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig.GetSignatureDer()); err != nil {
		return errors.New("the signature does not verify as PKCS#1 v1.5 over SHA-256")
	}
	d384 := sha512.Sum384([]byte("atoom key conformance"))
	_, err = c.Sign(ctx, connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: PurposeRS256, Digest: d384[:], Algorithm: RS256}))
	if err := wantRefusal(err, connect.CodeInvalidArgument, reasonDigest); err != nil {
		return err
	}
	_, err = c.Sign(ctx, connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: PurposeRS256, Digest: digest[:], Algorithm: ES384}))
	return wantRefusal(err, connect.CodeInvalidArgument, reasonAlgorithm)
}

// casePinRelease: the PIN release key signs only the texts the driver writes itself (BeginPinRelease, SealPin), never a digest of
// the caller's, because it signs without the PIN.
func casePinRelease(ctx context.Context, c Client) error {
	if _, err := ensure(ctx, c, PurposeRelease, ES384); err != nil {
		return err
	}
	d := sha512.Sum384([]byte("atoom key conformance"))
	_, err := c.Sign(ctx, connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: PurposeRelease, Digest: d[:], Algorithm: ES384}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		return fmt.Errorf("sign with the pin-release key: want invalid_argument, got %s", connect.CodeOf(err))
	}
	return nil
}

func casePublicKey(ctx context.Context, c Client) error {
	ens, err := ensure(ctx, c, PurposeES384, ES384)
	if err != nil {
		return err
	}
	res, err := c.PublicKey(ctx, connect.NewRequest(&keyv1alpha1.PublicKeyRequest{Purpose: PurposeES384}))
	if err != nil {
		return fmt.Errorf("public key: %w", err)
	}
	if !bytes.Equal(ens.GetSpkiDer(), res.Msg.GetSpkiDer()) {
		return errors.New("PublicKey differs from Ensure")
	}
	return nil
}

func caseEcdh(ctx context.Context, c Client) error {
	ens, err := ensure(ctx, c, PurposeAgree, ECDHP384)
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
	res, err := c.Ecdh(ctx, connect.NewRequest(&keyv1alpha1.EcdhRequest{Purpose: PurposeAgree, PeerPublic: eph.PublicKey().Bytes()}))
	if err != nil {
		return fmt.Errorf("ecdh: %w", err)
	}
	if !bytes.Equal(want, res.Msg.GetSharedSecret()) {
		return errors.New("the shared secret differs from the reference computation")
	}
	off := bytes.Clone(eph.PublicKey().Bytes())
	off[len(off)-1] ^= 0x01 // a point off the curve
	_, err = c.Ecdh(ctx, connect.NewRequest(&keyv1alpha1.EcdhRequest{Purpose: PurposeAgree, PeerPublic: off}))
	return wantRefusal(err, connect.CodeInvalidArgument, reasonPeer)
}

// caseUndeclaredPurpose: a name the host did not declare is refused by every call that names a purpose, whatever the algorithm and
// whatever name a driver might once have held.
func caseUndeclaredPurpose(ctx context.Context, c Client) error {
	d := sha512.Sum384([]byte("x"))
	for name, call := range map[string]func() error{
		"Ensure": func() error {
			_, err := c.Ensure(ctx, connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: PurposeUndeclared, Algorithm: ES384}))
			return err
		},
		"Sign": func() error {
			_, err := c.Sign(ctx, connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: PurposeUndeclared, Digest: d[:], Algorithm: ES384}))
			return err
		},
		"PublicKey": func() error {
			_, err := c.PublicKey(ctx, connect.NewRequest(&keyv1alpha1.PublicKeyRequest{Purpose: PurposeUndeclared}))
			return err
		},
		"Ecdh": func() error {
			_, err := c.Ecdh(ctx, connect.NewRequest(&keyv1alpha1.EcdhRequest{Purpose: PurposeUndeclared, PeerPublic: make([]byte, 97)}))
			return err
		},
	} {
		if err := wantRefusal(call(), connect.CodeInvalidArgument, reasonPurpose); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func caseUnknownAlgorithm(ctx context.Context, c Client) error {
	_, err := c.Ensure(ctx, connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: PurposeES384, Algorithm: "PS256"}))
	return wantRefusal(err, connect.CodeInvalidArgument, reasonAlgorithm)
}

func caseDigest(ctx context.Context, c Client) error {
	if _, err := ensure(ctx, c, PurposeES384, ES384); err != nil {
		return err
	}
	d := sha512.Sum384([]byte("x"))
	_, err := c.Sign(ctx, connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: PurposeES384, Digest: d[:47], Algorithm: ES384}))
	if e := wantRefusal(err, connect.CodeInvalidArgument, reasonDigest); e != nil {
		return e
	}
	if slices.Contains(capabilities(ctx, c), ES256) {
		// A SHA-384 digest is not an ES256 digest: that key is P-256 and signs SHA-256.
		if _, err := ensure(ctx, c, PurposeES256, ES256); err != nil {
			return err
		}
		_, err = c.Sign(ctx, connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: PurposeES256, Digest: d[:], Algorithm: ES256}))
		return wantRefusal(err, connect.CodeInvalidArgument, reasonDigest)
	}
	return nil
}

func caseOtherAlgorithm(ctx context.Context, c Client) error {
	_, err := c.Ensure(ctx, connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: PurposeES256, Algorithm: ES384}))
	return wantRefusal(err, connect.CodeFailedPrecondition, reasonAlgorithm)
}

// caseOperations: a key whose purpose the host declared without an operation is made and read, and the operation is refused (reason
// purpose); a signing purpose does not agree, and an agreement purpose does not sign (reason algorithm).
func caseOperations(ctx context.Context, c Client) error {
	ens, err := ensure(ctx, c, PurposeMadeOnly, ES384)
	if err != nil {
		return err
	}
	res, err := c.PublicKey(ctx, connect.NewRequest(&keyv1alpha1.PublicKeyRequest{Purpose: PurposeMadeOnly}))
	if err != nil {
		return fmt.Errorf("public key of a purpose with no operation: %w", err)
	}
	if !bytes.Equal(ens.GetSpkiDer(), res.Msg.GetSpkiDer()) {
		return errors.New("PublicKey of a purpose with no operation differs from Ensure")
	}
	d := sha512.Sum384([]byte("atoom key conformance"))
	_, err = c.Sign(ctx, connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: PurposeMadeOnly, Digest: d[:], Algorithm: ES384}))
	if err := wantRefusal(err, connect.CodeInvalidArgument, reasonPurpose); err != nil {
		return fmt.Errorf("sign without the operation: %w", err)
	}
	if _, err := ensure(ctx, c, PurposeES384, ES384); err != nil {
		return err
	}
	_, err = c.Ecdh(ctx, connect.NewRequest(&keyv1alpha1.EcdhRequest{Purpose: PurposeES384, PeerPublic: make([]byte, 97)}))
	if err := wantAlgorithmRefusal(err); err != nil {
		return fmt.Errorf("ecdh with a signing purpose: %w", err)
	}
	if slices.Contains(capabilities(ctx, c), ECDHP384) {
		if _, err := ensure(ctx, c, PurposeAgree, ECDHP384); err != nil {
			return err
		}
		_, err = c.Sign(ctx, connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: PurposeAgree, Digest: d[:], Algorithm: ES384}))
		if err := wantAlgorithmRefusal(err); err != nil {
			return fmt.Errorf("sign with an agreement purpose: %w", err)
		}
	}
	return nil
}

// wantAlgorithmRefusal is nil when err is the refusal of an operation that the key's algorithm cannot do: the reason algorithm, with
// invalid_argument or failed_precondition (the contract allows both; the drivers differ).
func wantAlgorithmRefusal(err error) error {
	if connect.CodeOf(err) == connect.CodeInvalidArgument {
		return wantRefusal(err, connect.CodeInvalidArgument, reasonAlgorithm)
	}
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
