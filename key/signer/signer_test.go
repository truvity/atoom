package signer_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
	"github.com/truvity/atoom/key/keyid"
	"github.com/truvity/atoom/key/purpose"
	"github.com/truvity/atoom/key/signature"
	"github.com/truvity/atoom/key/signer"
)

// verifies says whether the public key verifies sig over digest in the form of the algorithm, with the standard library alone.
func verifies(t testing.TB, alg string, pub crypto.PublicKey, digest, sig []byte) bool {
	t.Helper()
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		return ecdsa.VerifyASN1(k, digest, sig)
	case *rsa.PublicKey:
		return rsa.VerifyPKCS1v15(k, hashOf(alg), digest, sig) == nil
	}
	t.Fatalf("public key of type %T", pub)
	return false
}

func TestSignatureVerifiesWithPublicKeyOfTheStandardLibrary(t *testing.T) {
	for _, alg := range algorithms {
		t.Run(alg, func(t *testing.T) {
			f, k := newFake(t, alg)
			s := newSigner(t, f)
			if s.Algorithm() != alg {
				t.Errorf("Algorithm() = %q, want %q", s.Algorithm(), alg)
			}
			if s.KeyID() != k.id {
				t.Errorf("KeyID() = %q, want %q", s.KeyID(), k.id)
			}
			switch alg {
			case purpose.ES256, purpose.ES384:
				pub, ok := s.Public().(*ecdsa.PublicKey)
				if !ok {
					t.Fatalf("Public() is %T, want *ecdsa.PublicKey", s.Public())
				}
				want := map[string]elliptic.Curve{purpose.ES256: elliptic.P256(), purpose.ES384: elliptic.P384()}[alg]
				if pub.Curve != want {
					t.Errorf("curve %v, want %v", pub.Curve.Params().Name, want.Params().Name)
				}
			case purpose.RS256:
				pub, ok := s.Public().(*rsa.PublicKey)
				if !ok {
					t.Fatalf("Public() is %T, want *rsa.PublicKey", s.Public())
				}
				if pub.N.BitLen() != 2048 {
					t.Errorf("RSA size %d, want 2048", pub.N.BitLen())
				}
			}
			if !k.priv.Public().(interface{ Equal(crypto.PublicKey) bool }).Equal(s.Public()) {
				t.Error("Public() is not the key of the store")
			}
			digest := digestOf(alg, "a message")
			sig, err := s.Sign(nil, digest, hashOf(alg))
			if err != nil {
				t.Fatalf("Sign: %v", err)
			}
			if !verifies(t, alg, s.Public(), digest, sig) {
				t.Fatal("the standard library does not verify the signature with Public()")
			}
			// The same signature does not verify another message.
			if verifies(t, alg, s.Public(), digestOf(alg, "another message"), sig) {
				t.Fatal("the signature verifies another digest")
			}
		})
	}
}

func TestKeyIDIsReturnedAsTheStoreNamedIt(t *testing.T) {
	// The id of a key in a store is the fingerprint behind whatever places the key (a slot, an ARN): the Signer keeps it whole.
	for _, prefix := range []string{"", "9a:", "arn:example:kms:key/abc#"} {
		t.Run("prefix "+prefix, func(t *testing.T) {
			f, k := newFake(t, purpose.ES256)
			id := prefix + k.id
			f.mu.Lock()
			f.keyID = id
			f.mu.Unlock()
			f.setSign(func(_ context.Context, req *keyv1alpha1.SignRequest) (*keyv1alpha1.SignResponse, error) {
				return &keyv1alpha1.SignResponse{SignatureDer: k.sign(t, req.GetDigest()), KeyId: id}, nil
			})
			s := newSigner(t, f)
			if s.KeyID() != id {
				t.Fatalf("KeyID() = %q, want %q", s.KeyID(), id)
			}
			if _, err := s.Sign(nil, digestOf(purpose.ES256, "m"), crypto.SHA256); err != nil {
				t.Fatalf("Sign: %v", err)
			}
		})
	}
}

func TestPurposeIsFoundByNameAndMayHaveMoreOperations(t *testing.T) {
	f, k := newFake(t, purpose.ES384)
	// Another purpose first, with another algorithm; the purpose under test lists "agree" too, and the Signer still takes it.
	f.purposes = []*keyv1alpha1.PurposeInfo{
		{Name: "first", Algorithm: purpose.ES256, Operations: []string{"sign"}},
		{Name: purposeName, Algorithm: purpose.ES384, Operations: []string{"agree", "sign"}},
	}
	s := newSigner(t, f)
	if s.Algorithm() != purpose.ES384 || s.KeyID() != k.id {
		t.Fatalf("took the algorithm %q and the key %q", s.Algorithm(), s.KeyID())
	}
	if got := f.pubCount(); got != 1 {
		t.Fatalf("PublicKey asked %d times", got)
	}
	if p := f.pubPurposes[0]; p != purposeName {
		t.Fatalf("PublicKey asked for %q, want %q", p, purposeName)
	}
}

func TestCertificateSignedThroughTheSignerIsValid(t *testing.T) {
	// The real use: x509.CreateCertificate takes a crypto.Signer.
	wantAlg := map[string]x509.SignatureAlgorithm{
		purpose.ES256: x509.ECDSAWithSHA256,
		purpose.ES384: x509.ECDSAWithSHA384,
		purpose.RS256: x509.SHA256WithRSA,
	}
	for _, alg := range algorithms {
		t.Run(alg, func(t *testing.T) {
			f, _ := newFake(t, alg)
			s := newSigner(t, f)
			tmpl := &x509.Certificate{
				SerialNumber:          big.NewInt(1),
				Subject:               pkix.Name{CommonName: "signer test root"},
				NotBefore:             time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
				NotAfter:              time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC),
				IsCA:                  true,
				BasicConstraintsValid: true,
				KeyUsage:              x509.KeyUsageCertSign,
			}
			der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, s.Public(), s)
			if err != nil {
				t.Fatalf("CreateCertificate: %v", err)
			}
			cert, err := x509.ParseCertificate(der)
			if err != nil {
				t.Fatal(err)
			}
			if cert.SignatureAlgorithm != wantAlg[alg] {
				t.Errorf("signature algorithm %v, want %v", cert.SignatureAlgorithm, wantAlg[alg])
			}
			if err := cert.CheckSignatureFrom(cert); err != nil {
				t.Fatalf("CheckSignatureFrom: %v", err)
			}
			if err := cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature); err != nil {
				t.Fatalf("CheckSignature: %v", err)
			}
			// The check is a real one: a certificate with another body does not verify under that signature.
			tampered := append([]byte(nil), cert.RawTBSCertificate...)
			tampered[len(tampered)-1] ^= 1
			if err := cert.CheckSignature(cert.SignatureAlgorithm, tampered, cert.Signature); err == nil {
				t.Fatal("a changed certificate body verifies")
			}
			reqs := f.requests()
			if len(reqs) == 0 {
				t.Fatal("the store was not asked to sign")
			}
			for _, r := range reqs {
				if r.GetAlgorithm() != alg || r.GetPurpose() != purposeName || len(r.GetDigest()) != hashOf(alg).Size() {
					t.Errorf("request %v", r)
				}
			}
		})
	}
}

func TestNewRefuses(t *testing.T) {
	// Each case changes what the honest fake says; the fingerprint-matching id is kept unless the case changes it.
	cases := []struct {
		name  string
		alg   string // the algorithm the fake serves the purpose with
		setup func(t *testing.T, f *fake)
		want  error
	}{
		{"purpose not served", purpose.ES256, func(_ *testing.T, f *fake) {
			f.purposes = f.purposes[:1] // only "wrapping"
		}, signer.ErrPurpose},
		{"no purpose at all", purpose.ES256, func(_ *testing.T, f *fake) { f.purposes = nil }, signer.ErrPurpose},
		{"purpose with no operation", purpose.ES256, func(_ *testing.T, f *fake) { f.purposes[1].Operations = nil }, signer.ErrOperation},
		{"purpose that only agrees", purpose.ES256, func(_ *testing.T, f *fake) { f.purposes[1].Operations = []string{"agree"} }, signer.ErrOperation},
		{"operation is spelled in lower case", purpose.ES256, func(_ *testing.T, f *fake) { f.purposes[1].Operations = []string{"Sign"} }, signer.ErrOperation},
		{"algorithm ECDH-P384", purpose.ECDHP384, nil, signer.ErrAlgorithm},
		{"algorithm PS256", "PS256", nil, signer.ErrAlgorithm},
		{"algorithm ES512", "ES512", nil, signer.ErrAlgorithm},
		{"algorithm in lower case", "es256", nil, signer.ErrAlgorithm},
		{"algorithm empty", "", nil, signer.ErrAlgorithm},

		{"P-256 key under ES384", purpose.ES384, func(t *testing.T, f *fake) { f.setKey(newKey(t, "P-256", 0)) }, signer.ErrKey},
		{"P-384 key under ES256", purpose.ES256, func(t *testing.T, f *fake) { f.setKey(newKey(t, "P-384", 0)) }, signer.ErrKey},
		{"P-521 key under ES384", purpose.ES384, func(t *testing.T, f *fake) { f.setKey(newKey(t, "P-521", 0)) }, signer.ErrKey},
		{"RSA-3072 key under RS256", purpose.RS256, func(t *testing.T, f *fake) { f.setKey(newKey(t, "RSA-3072", 0)) }, signer.ErrKey},
		{"EC key under RS256", purpose.RS256, func(t *testing.T, f *fake) { f.setKey(newKey(t, "P-256", 0)) }, signer.ErrKey},
		{"RSA key under ES256", purpose.ES256, func(t *testing.T, f *fake) { f.setKey(newKey(t, purpose.RS256, 0)) }, signer.ErrKey},
		{"Ed25519 key under ES256", purpose.ES256, func(t *testing.T, f *fake) { f.setKey(newKey(t, "Ed25519", 0)) }, signer.ErrKey},
		{"bytes that are not an SPKI", purpose.ES256, func(_ *testing.T, f *fake) {
			f.spki = []byte("not a SubjectPublicKeyInfo")
			f.keyID = keyid.Software(f.spki)
		}, signer.ErrKey},
		{"an empty SPKI", purpose.ES256, func(_ *testing.T, f *fake) {
			f.spki = nil
			f.keyID = keyid.Software(nil)
		}, signer.ErrKey},
		{"an SPKI cut short", purpose.ES256, func(_ *testing.T, f *fake) {
			f.spki = f.spki[:len(f.spki)-1]
			f.keyID = keyid.Software(f.spki)
		}, signer.ErrKey},
		{"an SPKI with trailing bytes", purpose.ES256, func(_ *testing.T, f *fake) {
			f.spki = append(append([]byte(nil), f.spki...), 0)
			f.keyID = keyid.Software(f.spki)
		}, signer.ErrKey},

		{"key_id of another key", purpose.ES256, func(t *testing.T, f *fake) { f.keyID = newKey(t, "P-256", 0).id }, signer.ErrKey},
		{"key_id empty", purpose.ES256, func(_ *testing.T, f *fake) { f.keyID = "" }, signer.ErrKey},
		{"key_id in upper case", purpose.ES256, func(_ *testing.T, f *fake) { f.keyID = strings.ToUpper(f.keyID) }, signer.ErrKey},
		{"key_id with a character after the fingerprint", purpose.ES256, func(_ *testing.T, f *fake) { f.keyID += "x" }, signer.ErrKey},
		{"key_id with the fingerprint cut short", purpose.ES256, func(_ *testing.T, f *fake) { f.keyID = f.keyID[1:] }, signer.ErrKey},
		{"key_id of the fingerprint of an empty key", purpose.ES256, func(_ *testing.T, f *fake) { f.keyID = keyid.Fingerprint(nil) }, signer.ErrKey},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, _ := newFake(t, purpose.ES256)
			f.purposes[1].Algorithm = c.alg
			if c.setup != nil {
				c.setup(t, f)
			}
			s, err := signer.New(t.Context(), start(t, f), purposeName)
			if s != nil {
				t.Fatalf("New returned a Signer with the refusal %v", err)
			}
			refused(t, nil, err, c.want)
		})
	}
}

func TestNewTimeoutThatIsNotPositiveIsRefusedBeforeAnyCall(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Nanosecond, -time.Hour} {
		t.Run(d.String(), func(t *testing.T) {
			f, _ := newFake(t, purpose.ES256)
			s, err := signer.New(t.Context(), start(t, f), purposeName, signer.WithTimeout(d))
			if s != nil {
				t.Fatal("New returned a Signer")
			}
			refused(t, nil, err, signer.ErrTimeout)
			if f.capsCount() != 0 || f.pubCount() != 0 {
				t.Fatalf("the store was asked (%d, %d) though the timeout is refused", f.capsCount(), f.pubCount())
			}
		})
	}
}

func TestNewNeedsAClientAndAContextAndDoesNotPanic(t *testing.T) {
	f, _ := newFake(t, purpose.ES256)
	client := start(t, f)
	var nilCtx context.Context
	for name, call := range map[string]func() (*signer.Signer, error){
		"nil context": func() (*signer.Signer, error) { return signer.New(nilCtx, client, purposeName) },
		"nil client":  func() (*signer.Signer, error) { return signer.New(t.Context(), nil, purposeName) },
		"both nil":    func() (*signer.Signer, error) { return signer.New(nilCtx, nil, purposeName) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("New panicked: %v", r)
				}
			}()
			s, err := call()
			if s != nil || err == nil {
				t.Fatalf("New returned %v, %v", s, err)
			}
		})
	}
	if f.capsCount() != 0 {
		t.Fatal("the store was asked though the client or the context was nil")
	}
}

func TestNewReturnsAFailureOfThePortAsItCame(t *testing.T) {
	codes := []connect.Code{connect.CodeUnavailable, connect.CodePermissionDenied, connect.CodeInternal, connect.CodeFailedPrecondition, connect.CodeUnauthenticated}
	for _, code := range codes {
		t.Run("Capabilities "+code.String(), func(t *testing.T) {
			f, _ := newFake(t, purpose.ES256)
			f.capsErr = connect.NewError(code, errors.New("the store refuses"))
			s, err := signer.New(t.Context(), start(t, f), purposeName)
			if s != nil {
				t.Fatal("New returned a Signer")
			}
			isPortError(t, err, code)
			if f.pubCount() != 0 {
				t.Error("PublicKey was asked after Capabilities failed")
			}
		})
		t.Run("PublicKey "+code.String(), func(t *testing.T) {
			f, _ := newFake(t, purpose.ES256)
			f.pubErr = connect.NewError(code, errors.New("the store refuses"))
			s, err := signer.New(t.Context(), start(t, f), purposeName)
			if s != nil {
				t.Fatal("New returned a Signer")
			}
			isPortError(t, err, code)
		})
	}
}

// Sign, refused before the port: no call is made.

func TestSignRefusedLocallyNeverCallsThePort(t *testing.T) {
	hashes := []crypto.Hash{crypto.SHA1, crypto.SHA224, crypto.SHA256, crypto.SHA384, crypto.SHA512, crypto.Hash(0)}
	for _, alg := range algorithms {
		t.Run(alg, func(t *testing.T) {
			f, _ := newFake(t, alg)
			s := newSigner(t, f)
			right := hashOf(alg)
			digest := digestOf(alg, "m")

			wrong := []crypto.SignerOpts{nil}
			for _, h := range hashes {
				if h != right {
					wrong = append(wrong, h)
				}
			}
			for _, o := range wrong {
				sig, err := s.Sign(nil, digest, o)
				refused(t, sig, err, signer.ErrHash)
			}

			// RSASSA-PSS is refused for an RSA purpose and for an EC purpose alike, with the right hash and with the wrong one.
			for _, o := range []*rsa.PSSOptions{
				{Hash: right},
				{Hash: right, SaltLength: rsa.PSSSaltLengthEqualsHash},
				{Hash: crypto.SHA512},
				{},
				nil,
			} {
				sig, err := s.Sign(nil, digest, o)
				refused(t, sig, err, signer.ErrPSS)
			}

			// A digest that is not the size of the hash, with the right options.
			size := right.Size()
			for _, d := range [][]byte{nil, {}, make([]byte, 1), make([]byte, size-1), make([]byte, size+1), make([]byte, 2*size), make([]byte, 64), make([]byte, 20)} {
				if len(d) == size {
					continue
				}
				sig, err := s.Sign(nil, d, right)
				refused(t, sig, err, signer.ErrDigest)
			}

			// Which refusal wins when two apply: the options come before the digest.
			sig, err := s.Sign(nil, []byte{1}, crypto.SHA512)
			refused(t, sig, err, signer.ErrHash)
			sig, err = s.Sign(nil, []byte{1}, &rsa.PSSOptions{Hash: right})
			refused(t, sig, err, signer.ErrPSS)

			if n := f.signCount(); n != 0 {
				t.Fatalf("the port was called %d times for requests that are refused locally", n)
			}
			// The Signer is not spoilt by the refusals.
			sig, err = s.Sign(nil, digest, right)
			if err != nil || !verifies(t, alg, s.Public(), digest, sig) {
				t.Fatalf("Sign after the refusals: %v", err)
			}
			if n := f.signCount(); n != 1 {
				t.Fatalf("the port was called %d times for one good request", n)
			}
		})
	}
}

// customOpts is a SignerOpts of its own that is not an *rsa.PSSOptions: it names a hash and nothing else.
type customOpts struct{ h crypto.Hash }

func (o customOpts) HashFunc() crypto.Hash { return o.h }

func TestSignAcceptsAnyOptionsThatNameTheRightHash(t *testing.T) {
	f, _ := newFake(t, purpose.ES256)
	s := newSigner(t, f)
	digest := digestOf(purpose.ES256, "m")
	sig, err := s.Sign(nil, digest, customOpts{crypto.SHA256})
	if err != nil || !verifies(t, purpose.ES256, s.Public(), digest, sig) {
		t.Fatalf("Sign with custom options: %v", err)
	}
}

// Sign, refused after the port answered.

func TestSignRefusesAnAnswerThatIsNotTheKeyOrNotAValidSignature(t *testing.T) {
	for _, alg := range algorithms {
		t.Run(alg, func(t *testing.T) {
			f, k := newFake(t, alg)
			s := newSigner(t, f)
			digest := digestOf(alg, "the message")
			good := k.sign(t, digest)
			stranger := newKey(t, alg, 1)

			flip := func(i int, bit uint) []byte {
				b := append([]byte(nil), good...)
				b[i] ^= 1 << bit
				return b
			}
			junk := make([]byte, len(good))
			if _, err := rand.Read(junk); err != nil {
				t.Fatal(err)
			}
			type answerCase struct {
				name string
				sig  []byte
				id   string
				want error
			}
			cases := []answerCase{
				{"another key_id", good, stranger.id, signer.ErrKeyChanged},
				{"an empty key_id", good, "", signer.ErrKeyChanged},
				{"a key_id with a prefix added", good, "9a:" + k.id, signer.ErrKeyChanged},
				{"the key_id in upper case", good, strings.ToUpper(k.id), signer.ErrKeyChanged},
				{"another key_id and a bad signature", junk, stranger.id, signer.ErrKeyChanged},
				{"a signature by another key", stranger.sign(t, digest), k.id, signer.ErrVerify},
				{"a signature over another digest", k.sign(t, digestOf(alg, "another message")), k.id, signer.ErrVerify},
				{"a signature cut by one byte", good[:len(good)-1], k.id, signer.ErrVerify},
				{"a signature cut to half", good[:len(good)/2], k.id, signer.ErrVerify},
				{"a signature of one byte", good[:1], k.id, signer.ErrVerify},
				{"an empty signature", []byte{}, k.id, signer.ErrVerify},
				{"no signature", nil, k.id, signer.ErrVerify},
				{"a signature with a byte after it", append(append([]byte(nil), good...), 0), k.id, signer.ErrVerify},
				{"random bytes", junk, k.id, signer.ErrVerify},
				{"a flipped bit in the first byte", flip(0, 0), k.id, signer.ErrVerify},
				{"a flipped bit in the middle", flip(len(good)/2, 3), k.id, signer.ErrVerify},
				{"a flipped bit in the last byte", flip(len(good)-1, 0), k.id, signer.ErrVerify},
				{"a flipped top bit in the last byte", flip(len(good)-1, 7), k.id, signer.ErrVerify},
			}
			if alg == purpose.RS256 {
				pss, err := rsa.SignPSS(rand.Reader, k.priv.(*rsa.PrivateKey), crypto.SHA256, digest, nil)
				if err != nil {
					t.Fatal(err)
				}
				cases = append(cases, answerCase{"an RSA-PSS signature", pss, k.id, signer.ErrVerify})
				cases = append(cases, answerCase{"a signature with a zero byte in front", append([]byte{0}, good...), k.id, signer.ErrVerify})
			} else {
				raw, err := signature.DERToRaw(alg, good)
				if err != nil {
					t.Fatal(err)
				}
				cases = append(cases, answerCase{"the raw r||s form of a JWS", raw, k.id, signer.ErrVerify})
			}

			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					before := f.signCount()
					f.answer(c.sig, c.id)
					sig, err := s.Sign(nil, digest, hashOf(alg))
					refused(t, sig, err, c.want)
					if c.want == signer.ErrKeyChanged && errors.Is(err, signer.ErrVerify) {
						t.Error("a key change is also reported as a bad signature")
					}
					if got := f.signCount() - before; got != 1 {
						t.Errorf("%d calls of the port for one Sign", got)
					}
				})
			}
		})
	}
}

func TestSignReturnsAFailureOfThePortAsItCame(t *testing.T) {
	codes := []connect.Code{connect.CodeUnavailable, connect.CodePermissionDenied, connect.CodeInvalidArgument, connect.CodeInternal, connect.CodeResourceExhausted, connect.CodeFailedPrecondition}
	for _, alg := range algorithms {
		for _, code := range codes {
			t.Run(alg+" "+code.String(), func(t *testing.T) {
				f, _ := newFake(t, alg)
				s := newSigner(t, f)
				f.fail(code)
				sig, err := s.Sign(nil, digestOf(alg, "m"), hashOf(alg))
				if sig != nil {
					t.Errorf("a signature came back with the failure: %x", sig)
				}
				isPortError(t, err, code)
			})
		}
	}
}

func TestSignDoesNotNormaliseTheSignature(t *testing.T) {
	// The port normalises nothing (no low-S form), and the Signer returns what verifies: the high-S twin of an ECDSA signature
	// verifies under crypto/ecdsa, so it is returned as it came.
	for _, alg := range []string{purpose.ES256, purpose.ES384} {
		t.Run(alg, func(t *testing.T) {
			f, k := newFake(t, alg)
			s := newSigner(t, f)
			digest := digestOf(alg, "m")
			var rs struct{ R, S *big.Int }
			if _, err := asn1.Unmarshal(k.sign(t, digest), &rs); err != nil {
				t.Fatal(err)
			}
			n := k.priv.Public().(*ecdsa.PublicKey).Curve.Params().N
			twin, err := asn1.Marshal(struct{ R, S *big.Int }{rs.R, new(big.Int).Sub(n, rs.S)})
			if err != nil {
				t.Fatal(err)
			}
			f.answer(twin, k.id)
			sig, err := s.Sign(nil, digest, hashOf(alg))
			if err != nil {
				t.Fatalf("Sign: %v", err)
			}
			if !bytes.Equal(sig, twin) {
				t.Fatal("the signature was changed")
			}
		})
	}
}

// What reaches the port.

type noRandom struct{ t *testing.T }

func (r noRandom) Read([]byte) (int, error) {
	r.t.Error("Sign read from rand")
	return 0, io.ErrNoProgress
}

func TestWhatReachesThePortIsExactlyWhatWasGiven(t *testing.T) {
	for _, alg := range algorithms {
		t.Run(alg, func(t *testing.T) {
			f, _ := newFake(t, alg)
			s := newSigner(t, f)
			if f.capsCount() != 1 || f.pubCount() != 1 {
				t.Fatalf("New made %d calls of Capabilities and %d of PublicKey, want one of each", f.capsCount(), f.pubCount())
			}
			for i := range 3 {
				digest := digestOf(alg, fmt.Sprintf("message %d", i))
				given := append([]byte(nil), digest...)
				sig, err := s.Sign(noRandom{t}, digest, hashOf(alg))
				if err != nil || !verifies(t, alg, s.Public(), digest, sig) {
					t.Fatalf("Sign: %v", err)
				}
				if !bytes.Equal(digest, given) {
					t.Error("Sign changed the digest of the caller")
				}
				reqs := f.requests()
				if len(reqs) != i+1 {
					t.Fatalf("%d requests after %d signatures", len(reqs), i+1)
				}
				got := reqs[i]
				if got.GetPurpose() != purposeName || got.GetAlgorithm() != alg || !bytes.Equal(got.GetDigest(), given) {
					t.Fatalf("the store got purpose %q, algorithm %q, digest %x; want %q, %q, %x", got.GetPurpose(), got.GetAlgorithm(), got.GetDigest(), purposeName, alg, given)
				}
			}
			if f.capsCount() != 1 || f.pubCount() != 1 {
				t.Errorf("Sign asked Capabilities or PublicKey again (%d, %d)", f.capsCount(), f.pubCount())
			}
		})
	}
}

type ctxKey struct{}

func TestEveryCallCarriesTheContextOfNewAndTheDeadlineOfWithTimeout(t *testing.T) {
	type seen struct {
		procedure string
		id        any
		remaining time.Duration
		hasDL     bool
	}
	var mu sync.Mutex
	var calls []seen
	ic := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			dl, ok := ctx.Deadline()
			mu.Lock()
			calls = append(calls, seen{req.Spec().Procedure, ctx.Value(ctxKey{}), time.Until(dl), ok})
			mu.Unlock()
			return next(ctx, req)
		}
	})
	check := func(t *testing.T, timeout time.Duration, opts ...signer.Option) {
		mu.Lock()
		calls = nil
		mu.Unlock()
		f, _ := newFake(t, purpose.ES256)
		ctx := context.WithValue(t.Context(), ctxKey{}, "correlation-1")
		s, err := signer.New(ctx, start(t, f, connect.WithInterceptors(ic)), purposeName, opts...)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Sign(nil, digestOf(purpose.ES256, "m"), crypto.SHA256); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(calls) != 3 {
			t.Fatalf("%d calls, want Capabilities, PublicKey and Sign", len(calls))
		}
		for _, c := range calls {
			if c.id != "correlation-1" {
				t.Errorf("%s: the value of the context of New did not arrive (%v)", c.procedure, c.id)
			}
			if !c.hasDL || c.remaining <= 0 || c.remaining > timeout || c.remaining < timeout-time.Minute/2 {
				t.Errorf("%s: deadline in %v (present: %v), want within %v", c.procedure, c.remaining, c.hasDL, timeout)
			}
		}
	}
	t.Run("WithTimeout", func(t *testing.T) { check(t, 90*time.Second, signer.WithTimeout(90*time.Second)) })
	t.Run("default", func(t *testing.T) {
		if signer.DefaultTimeout != 2*time.Minute {
			t.Fatalf("DefaultTimeout is %v", signer.DefaultTimeout)
		}
		check(t, signer.DefaultTimeout)
	})
}

// Deadline and cancellation.

func isDeadline(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || connect.CodeOf(err) == connect.CodeDeadlineExceeded
}

func isCanceled(err error) bool {
	return errors.Is(err, context.Canceled) || connect.CodeOf(err) == connect.CodeCanceled
}

// describe says which of the two forms of a deadline an error has.
func describe(err error) string {
	return fmt.Sprintf("errors.Is(DeadlineExceeded)=%v, connect code=%s, error=%q", errors.Is(err, context.DeadlineExceeded), connect.CodeOf(err), err)
}

func TestSignOfASlowStoreEndsAtTheTimeout(t *testing.T) {
	f, k := newFake(t, purpose.ES384)
	s := newSigner(t, f, signer.WithTimeout(50*time.Millisecond))
	f.blockSign()
	begin := time.Now()
	sig, err := s.Sign(nil, digestOf(purpose.ES384, "m"), crypto.SHA384)
	if elapsed := time.Since(begin); elapsed > 5*time.Second {
		t.Fatalf("Sign took %v with a timeout of 50ms", elapsed)
	}
	if sig != nil || err == nil || !isDeadline(err) {
		t.Fatalf("sig %x, err %v: not a deadline", sig, err)
	}
	t.Log(describe(err))
	// A timeout is not a state: the next call, to a store that answers, signs.
	f.setSign(honest(t, k))
	if sig, err := s.Sign(nil, digestOf(purpose.ES384, "m"), crypto.SHA384); err != nil || sig == nil {
		t.Fatalf("Sign after a timeout: %v", err)
	}
}

func TestNewOfASlowStoreEndsAtTheTimeout(t *testing.T) {
	for _, call := range []string{"Capabilities", "PublicKey"} {
		t.Run(call, func(t *testing.T) {
			f, _ := newFake(t, purpose.ES256)
			f.blockCaps = call == "Capabilities"
			f.blockPub = call == "PublicKey"
			begin := time.Now()
			s, err := signer.New(t.Context(), start(t, f), purposeName, signer.WithTimeout(50*time.Millisecond))
			if elapsed := time.Since(begin); elapsed > 5*time.Second {
				t.Fatalf("New took %v with a timeout of 50ms", elapsed)
			}
			if s != nil || err == nil || !isDeadline(err) {
				t.Fatalf("signer %v, err %v: not a deadline", s, err)
			}
			t.Log(describe(err))
		})
	}
}

func TestCancellingTheContextOfNewStopsLaterSigns(t *testing.T) {
	f, _ := newFake(t, purpose.ES256)
	ctx, cancel := context.WithCancel(t.Context())
	s, err := signer.New(ctx, start(t, f), purposeName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sign(nil, digestOf(purpose.ES256, "m"), crypto.SHA256); err != nil {
		t.Fatalf("Sign before the cancel: %v", err)
	}
	cancel()
	sig, err := s.Sign(nil, digestOf(purpose.ES256, "m"), crypto.SHA256)
	if sig != nil || err == nil || !isCanceled(err) {
		t.Fatalf("sig %x, err %v: not a cancellation", sig, err)
	}
	t.Logf("errors.Is(Canceled)=%v, connect code=%s", errors.Is(err, context.Canceled), connect.CodeOf(err))
}

func TestCancellingWhileTheStoreWaitsReturnsAtOnce(t *testing.T) {
	f, _ := newFake(t, purpose.ES256)
	ctx, cancel := context.WithCancel(t.Context())
	s, err := signer.New(ctx, start(t, f), purposeName)
	if err != nil {
		t.Fatal(err)
	}
	f.blockSign()
	type result struct {
		sig []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		sig, err := s.Sign(nil, digestOf(purpose.ES256, "m"), crypto.SHA256)
		done <- result{sig, err}
	}()
	select {
	case <-f.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the store was not reached")
	}
	cancel()
	select {
	case r := <-done:
		if r.sig != nil || r.err == nil || !isCanceled(r.err) {
			t.Fatalf("sig %x, err %v: not a cancellation", r.sig, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Sign did not return after the cancel")
	}
}

func TestContextOfNewThatEndedBeforeNewMakesNewFail(t *testing.T) {
	f, _ := newFake(t, purpose.ES256)
	past, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	s, err := signer.New(past, start(t, f), purposeName)
	if s != nil || err == nil || !isDeadline(err) {
		t.Fatalf("signer %v, err %v: not a deadline", s, err)
	}
}

func TestDeadlineOfTheContextOfNewWinsWhenItIsSoonerThanTheTimeout(t *testing.T) {
	f, _ := newFake(t, purpose.ES256)
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	s, err := signer.New(ctx, start(t, f), purposeName, signer.WithTimeout(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	f.blockSign()
	begin := time.Now()
	sig, err := s.Sign(nil, digestOf(purpose.ES256, "m"), crypto.SHA256)
	if elapsed := time.Since(begin); elapsed > 10*time.Second {
		t.Fatalf("Sign took %v: the hour of WithTimeout won over the 500ms of the context", elapsed)
	}
	if sig != nil || err == nil || !isDeadline(err) {
		t.Fatalf("sig %x, err %v: not a deadline", sig, err)
	}
	t.Log(describe(err))
}

// Concurrency.

func TestManyGoroutinesSignThroughOneSigner(t *testing.T) {
	const goroutines, each = 16, 8
	for _, alg := range []string{purpose.ES256, purpose.ES384, purpose.RS256} {
		t.Run(alg, func(t *testing.T) {
			f, _ := newFake(t, alg)
			s := newSigner(t, f)
			var wg sync.WaitGroup
			for g := range goroutines {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := range each {
						digest := digestOf(alg, fmt.Sprintf("goroutine %d, message %d", g, i))
						sig, err := s.Sign(nil, digest, hashOf(alg))
						if err != nil {
							t.Errorf("Sign: %v", err)
							return
						}
						if !verifies(t, alg, s.Public(), digest, sig) {
							t.Errorf("goroutine %d, message %d: the signature does not verify", g, i)
						}
					}
				}()
			}
			wg.Wait()
			if got := f.signCount(); got != goroutines*each {
				t.Errorf("%d calls of the port, want %d", got, goroutines*each)
			}
		})
	}
}

func TestNewIgnoresANilOptionAndAppliesTheOthers(t *testing.T) {
	// The real options on both sides of the nil one apply, in order: the last timeout is the one that holds.
	t.Run("only nil options", func(t *testing.T) {
		f, _ := newFake(t, purpose.ES256)
		if _, err := signer.New(t.Context(), start(t, f), purposeName, nil, nil); err != nil {
			t.Fatalf("New: %v", err)
		}
	})
	t.Run("the timeout after the nil option applies", func(t *testing.T) {
		f, _ := newFake(t, purpose.ES256)
		s, err := signer.New(t.Context(), start(t, f), purposeName, signer.WithTimeout(-time.Second), nil, signer.WithTimeout(50*time.Millisecond))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		f.blockSign()
		begin := time.Now()
		sig, err := s.Sign(nil, digestOf(purpose.ES256, "m"), crypto.SHA256)
		if elapsed := time.Since(begin); elapsed > 5*time.Second {
			t.Fatalf("Sign took %v: the timeout of 50ms was not applied", elapsed)
		}
		if sig != nil || !isDeadline(err) {
			t.Fatalf("sig %x, err %v: not a deadline", sig, err)
		}
	})
	t.Run("the timeout before the nil option applies", func(t *testing.T) {
		f, _ := newFake(t, purpose.ES256)
		s, err := signer.New(t.Context(), start(t, f), purposeName, signer.WithTimeout(time.Minute), nil, signer.WithTimeout(0))
		if s != nil {
			t.Fatal("New returned a Signer")
		}
		refused(t, nil, err, signer.ErrTimeout)
	})
}

func TestRS256RefusesAnExponentThatIsNot65537(t *testing.T) {
	base := &rsaKey(t, 2048, 0).PublicKey
	for _, e := range []int{3, 17, 257, 65539} {
		t.Run(fmt.Sprint("E=", e), func(t *testing.T) {
			f, _ := newFake(t, purpose.RS256)
			spki, err := x509.MarshalPKIXPublicKey(&rsa.PublicKey{N: base.N, E: e})
			if err != nil {
				t.Fatal(err)
			}
			// The id names this very SPKI, so the exponent is the only reason to refuse.
			f.spki, f.keyID = spki, keyid.Software(spki)
			s, err := signer.New(t.Context(), start(t, f), purposeName)
			if s != nil {
				t.Fatal("New returned a Signer")
			}
			refused(t, nil, err, signer.ErrKey)
		})
	}
	t.Run("65537 is taken", func(t *testing.T) {
		f, _ := newFake(t, purpose.RS256)
		if base.E != 65537 {
			t.Fatalf("the test key has the exponent %d", base.E)
		}
		if s := newSigner(t, f); s.Public().(*rsa.PublicKey).E != 65537 {
			t.Fatal("the exponent is not 65537")
		}
	})
}

// Sign copies the digest.

func TestSignCopiesTheDigest(t *testing.T) {
	for _, alg := range algorithms {
		t.Run(alg, func(t *testing.T) {
			f, k := newFake(t, alg)
			s := newSigner(t, f)
			digest := digestOf(alg, "m")
			given := append([]byte(nil), digest...)
			_, err := s.Sign(nil, digest, hashOf(alg))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(digest, given) {
				t.Error("Sign changed the slice of the caller")
			}
			if got := f.requests()[0].GetDigest(); !bytes.Equal(got, given) {
				t.Errorf("the store got %x, the caller gave %x", got, given)
			}

			// The caller changes its slice while the store works: it still gets a valid signature of the first value. The fake
			// signals that it has the request, the test changes the slice, and only then the fake answers.
			proceed := make(chan struct{})
			f.setSign(func(_ context.Context, req *keyv1alpha1.SignRequest) (*keyv1alpha1.SignResponse, error) {
				<-proceed
				return honest(t, k)(nil, req)
			})
			for len(f.entered) > 0 {
				<-f.entered
			}
			type result struct {
				sig []byte
				err error
			}
			done := make(chan result, 1)
			go func() {
				sig, err := s.Sign(nil, digest, hashOf(alg))
				done <- result{sig, err}
			}()
			select {
			case <-f.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("the store was not reached")
			}
			for i := range digest {
				digest[i] ^= 0xff
			}
			close(proceed)
			r := <-done
			if r.err != nil {
				t.Fatalf("Sign with a slice that changed meanwhile: %v", r.err)
			}
			if !verifies(t, alg, s.Public(), given, r.sig) {
				t.Fatal("the signature is not a valid signature of the value at the time of the call")
			}
		})
	}
}

// ErrKeyChanged carries no text from the store.

func TestKeyChangedCarriesNoIdOfTheStore(t *testing.T) {
	f, k := newFake(t, purpose.ES256)
	oldID, newID := "OLD-SLOT-MARKER:"+k.id, "NEW-SLOT-MARKER:"+newKey(t, purpose.ES256, 0).id
	f.keyID = oldID
	f.answer(k.sign(t, digestOf(purpose.ES256, "m")), newID)
	s := newSigner(t, f)
	if s.KeyID() != oldID {
		t.Fatalf("KeyID() = %q", s.KeyID())
	}
	sig, err := s.Sign(nil, digestOf(purpose.ES256, "m"), crypto.SHA256)
	refused(t, sig, err, signer.ErrKeyChanged)
	for _, text := range []string{"OLD-SLOT-MARKER", "NEW-SLOT-MARKER", k.id, newID, oldID} {
		if strings.Contains(err.Error(), text) {
			t.Errorf("the error %q names %q", err, text)
		}
	}
}

// WithContext.

func TestWithContextMakesNoCallAndKeepsTheKey(t *testing.T) {
	f, _ := newFake(t, purpose.ES384)
	s := newSigner(t, f)
	caps, pubs, signs := f.capsCount(), f.pubCount(), f.signCount()
	c := s.WithContext(t.Context())
	if c == s {
		t.Fatal("WithContext returned the Signer itself")
	}
	if f.capsCount() != caps || f.pubCount() != pubs || f.signCount() != signs {
		t.Fatal("WithContext called the port")
	}
	if c.Public() != s.Public() || c.KeyID() != s.KeyID() || c.Algorithm() != s.Algorithm() {
		t.Fatal("the copy has another key, id or algorithm")
	}
	digest := digestOf(purpose.ES384, "m")
	sig, err := c.Sign(nil, digest, crypto.SHA384)
	if err != nil || !verifies(t, purpose.ES384, c.Public(), digest, sig) {
		t.Fatalf("Sign of the copy: %v", err)
	}
}

func TestWithContextCallsCarryTheNewContext(t *testing.T) {
	var mu sync.Mutex
	var values []any
	ic := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if req.Spec().Procedure == "/atoom.key.v1alpha1.KeyStoreService/Sign" {
				mu.Lock()
				values = append(values, ctx.Value(ctxKey{}))
				mu.Unlock()
			}
			return next(ctx, req)
		}
	})
	f, _ := newFake(t, purpose.ES256)
	s, err := signer.New(context.WithValue(t.Context(), ctxKey{}, "original"), start(t, f, connect.WithInterceptors(ic)), purposeName)
	if err != nil {
		t.Fatal(err)
	}
	digest := digestOf(purpose.ES256, "m")
	if _, err := s.WithContext(context.WithValue(t.Context(), ctxKey{}, "request")).Sign(nil, digest, crypto.SHA256); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sign(nil, digest, crypto.SHA256); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(values) != 2 || values[0] != "request" || values[1] != "original" {
		t.Fatalf("the values that reached the port: %v, want [request original]", values)
	}
}

func TestWithContextDeadlineWinsWhenSoonerThanTheTimeout(t *testing.T) {
	f, _ := newFake(t, purpose.ES256)
	s := newSigner(t, f, signer.WithTimeout(time.Hour))
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	f.blockSign()
	begin := time.Now()
	sig, err := s.WithContext(ctx).Sign(nil, digestOf(purpose.ES256, "m"), crypto.SHA256)
	if elapsed := time.Since(begin); elapsed > 10*time.Second {
		t.Fatalf("Sign took %v: the deadline of the new context did not win", elapsed)
	}
	if sig != nil || !isDeadline(err) {
		t.Fatalf("sig %x, err %v: not a deadline", sig, err)
	}
}

func TestWithContextCancellationsAreIndependent(t *testing.T) {
	digest := digestOf(purpose.ES256, "m")
	sign := func(s *signer.Signer) ([]byte, error) { return s.Sign(nil, digest, crypto.SHA256) }

	t.Run("cancelling the copy stops the copy and not the original", func(t *testing.T) {
		f, _ := newFake(t, purpose.ES256)
		s := newSigner(t, f)
		ctx, cancel := context.WithCancel(t.Context())
		c := s.WithContext(ctx)
		cancel()
		if sig, err := sign(c); sig != nil || !isCanceled(err) {
			t.Fatalf("copy: sig %x, err %v: not a cancellation", sig, err)
		}
		if _, err := sign(s); err != nil {
			t.Fatalf("the original after the copy was cancelled: %v", err)
		}
	})
	t.Run("cancelling the original does not stop the copy", func(t *testing.T) {
		f, _ := newFake(t, purpose.ES256)
		ctx, cancel := context.WithCancel(t.Context())
		s, err := signer.New(ctx, start(t, f), purposeName)
		if err != nil {
			t.Fatal(err)
		}
		c := s.WithContext(t.Context())
		cancel()
		if sig, err := sign(s); sig != nil || !isCanceled(err) {
			t.Fatalf("original: sig %x, err %v: not a cancellation", sig, err)
		}
		if sig, err := sign(c); err != nil || !verifies(t, purpose.ES256, c.Public(), digest, sig) {
			t.Fatalf("the copy after the original was cancelled: %v", err)
		}
	})
}

func TestWithContextOfNilPanicsAndSaysWhy(t *testing.T) {
	f, _ := newFake(t, purpose.ES256)
	s := newSigner(t, f)
	var nilCtx context.Context
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("WithContext(nil) did not panic")
		}
		if msg := fmt.Sprint(r); !strings.Contains(msg, "nil context") {
			t.Fatalf("the panic says %q, want it to name the nil context", msg)
		}
	}()
	s.WithContext(nilCtx)
}

func TestCopiesAndTheOriginalSignTogether(t *testing.T) {
	const goroutines, each = 8, 8
	for _, alg := range []string{purpose.ES256, purpose.RS256} {
		t.Run(alg, func(t *testing.T) {
			f, _ := newFake(t, alg)
			s := newSigner(t, f)
			var wg sync.WaitGroup
			for g := range goroutines {
				// Half of the goroutines sign through the original, the others each through a copy of their own, and some copy a copy.
				sg := s
				if g%2 == 1 {
					sg = s.WithContext(context.WithValue(t.Context(), ctxKey{}, g))
					if g%4 == 3 {
						sg = sg.WithContext(t.Context())
					}
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := range each {
						digest := digestOf(alg, fmt.Sprintf("goroutine %d, message %d", g, i))
						sig, err := sg.Sign(nil, digest, hashOf(alg))
						if err != nil || !verifies(t, alg, sg.Public(), digest, sig) {
							t.Errorf("goroutine %d, message %d: %v", g, i, err)
							return
						}
					}
				}()
			}
			wg.Wait()
			if got := f.signCount(); got != goroutines*each {
				t.Errorf("%d calls of the port, want %d", got, goroutines*each)
			}
		})
	}
}
