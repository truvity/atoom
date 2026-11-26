// Package signer is a crypto.Signer over the key port: the private key of one purpose stays in the key store, and the Signer asks the
// store to sign. It is small plumbing over the generated client and knows nothing of any driver or of any host; the purpose says which
// key and which algorithm, and the algorithm says which hash.
//
//	hc := &http.Client{ /* a transport to the key store's socket */ }
//	keys := keyv1alpha1connect.NewKeyStoreServiceClient(hc, "http://key", call.ClientOptions()...)
//
//	s, err := signer.New(ctx, keys, "client-assertion", signer.WithTimeout(time.Minute))
//	if err != nil {
//	    return err
//	}
//	digest := sha256.Sum256(content)
//	sig, err := s.Sign(nil, digest[:], crypto.SHA256) // ASN.1 DER for an ES256 purpose; s.Public() is the key that checks it
//
// New asks the store what it serves (Capabilities), finds the purpose, which must allow purpose.Sign and have one of the algorithms
// purpose.ES256, purpose.ES384 and purpose.RS256, and reads its public key (PublicKey), which must fit the algorithm and whose key_id
// must end with the fingerprint of that key (keyid.Fingerprint). Every Sign then makes one call of the port's Sign,
// checks that the key that answered is the key New saw, and verifies the signature with the public key before it returns it. A key that
// was rotated under the Signer is therefore an error (ErrKeyChanged) and never a signature by another key: make a new Signer.
//
// The form of what Sign returns is the one the standard library's own signers return for the key: ASN.1 DER for an ECDSA key (the form
// of crypto/ecdsa's Sign and of the port, RFC 3279; a JWS wants the raw r||s that key/signature.DERToRaw makes), and the plain
// RSASSA-PKCS1-v1_5 signature for an RSA key (RFC 8017, section 8.2.1).
//
// The context and the deadline. crypto.Signer's Sign has no context, so the Signer keeps the context given to New and makes every call
// of the port with it, narrowed by a deadline of its own (WithTimeout; the default is DefaultTimeout). A correlation id in that context
// goes with every call if the client was made with call.ClientOptions() (docs/call-context.md), and the Signer stops when that context
// is cancelled or its deadline passes: give New a context that lives as long as the Signer, not the context of the request that happened
// to build it. A Signer is safe for concurrent use.
//
// The limits. The Signer signs a digest and nothing else: a message is hashed by the caller (crypto.Signer's contract), and the digest
// must be the hash of the purpose's algorithm. It knows ES256 (SHA-256), ES384 (SHA-384) and RS256 (SHA-256, PKCS#1 v1.5, RSA-2048); RSA-PSS
// is refused. It does not decrypt, agree on keys (the port's Ecdh), make keys (Ensure), attest or close anything, and it keeps nothing
// but the public key and the key_id.
package signer

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"

	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
	"github.com/truvity/atoom/key/gen/atoom/key/v1alpha1/keyv1alpha1connect"
	"github.com/truvity/atoom/key/keyid"
	"github.com/truvity/atoom/key/purpose"
)

// DefaultTimeout is the deadline of each call of the port when WithTimeout is not given: two minutes. A key in hardware may wait for a
// person to touch it, so the default is long; a host that knows its store is quick sets less.
const DefaultTimeout = 2 * time.Minute

// The refusals of the Signer itself. A refusal of the port is not one of these: it is the Connect error of the port, wrapped, and
// errors.As finds it (connect.Error, and with it the refusal detail of common/refusal).
var (
	// ErrPurpose is a purpose that the store does not serve (it is not in Capabilities).
	ErrPurpose = errors.New("signer: the store does not serve the purpose")
	// ErrOperation is a purpose that the host declared without the operation "sign".
	ErrOperation = errors.New("signer: the purpose does not allow sign")
	// ErrAlgorithm is a purpose whose algorithm is not ES256, ES384 or RS256.
	ErrAlgorithm = errors.New("signer: the algorithm of the purpose is not ES256, ES384 or RS256")
	// ErrKey is a public key that is not one SubjectPublicKeyInfo of the algorithm (another key type, another curve or size), or a key_id
	// that does not name the public key it came with.
	ErrKey = errors.New("signer: the public key does not fit the algorithm of the purpose")
	// ErrTimeout is a WithTimeout that is not positive.
	ErrTimeout = errors.New("signer: the timeout is not positive")
	// ErrHash is a hash in the options of Sign that is not the hash of the purpose's algorithm (SHA-256 for ES256 and RS256, SHA-384 for
	// ES384), or no options at all.
	ErrHash = errors.New("signer: the hash is not the hash of the algorithm of the purpose")
	// ErrPSS is an *rsa.PSSOptions: RS256 is RSASSA-PKCS1-v1_5, and the Signer signs nothing else.
	ErrPSS = errors.New("signer: RSA-PSS is not offered")
	// ErrDigest is a digest whose length is not the size of the hash of the algorithm.
	ErrDigest = errors.New("signer: the digest has the wrong length for the algorithm")
	// ErrKeyChanged is an answer of the port from another key than the one New saw: the key of the purpose was rotated or replaced.
	ErrKeyChanged = errors.New("signer: the key of the purpose changed")
	// ErrVerify is a signature that the public key does not verify. It is never returned as a signature.
	ErrVerify = errors.New("signer: the signature does not verify with the public key")
)

// Option changes how New makes a Signer.
type Option func(*config)

type config struct {
	timeout time.Duration
}

// WithTimeout sets the deadline of each call of the port that the Signer makes (the calls of New, and each Sign), counted from the call
// and never beyond the deadline of the context given to New. The default is DefaultTimeout. A timeout that is not positive makes New
// return ErrTimeout.
func WithTimeout(d time.Duration) Option { return func(c *config) { c.timeout = d } }

// Signer is a crypto.Signer whose key is the key of one purpose in a key store. It is safe for concurrent use.
type Signer struct {
	ctx       context.Context // crypto.Signer.Sign has no context: this is the base of every call of the port
	client    keyv1alpha1connect.KeyStoreServiceClient
	timeout   time.Duration
	purpose   string
	algorithm string
	hash      crypto.Hash
	pub       crypto.PublicKey
	keyID     string
}

var _ crypto.Signer = (*Signer)(nil)

// New asks the store for the purpose (Capabilities and PublicKey, each with the deadline of WithTimeout) and returns the Signer of its
// key. It refuses a purpose that the store does not serve (ErrPurpose), that is declared without "sign" (ErrOperation) or whose
// algorithm is not ES256, ES384 or RS256 (ErrAlgorithm), and a public key that does not fit the algorithm (ErrKey): P-256 for ES256,
// P-384 for ES384, a 2048-bit RSA key for RS256. It also checks that the key_id of the answer ends with keyid.Fingerprint of the
// SubjectPublicKeyInfo it came with, as the contract says, and refuses an id that does not name the public key (ErrKey). A failure of the port is returned wrapped, as it came. ctx is kept as the base of every
// later call (see the package comment).
func New(ctx context.Context, client keyv1alpha1connect.KeyStoreServiceClient, name string, opts ...Option) (*Signer, error) {
	cfg := config{timeout: DefaultTimeout}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.timeout <= 0 {
		return nil, ErrTimeout
	}
	if ctx == nil || client == nil {
		return nil, errors.New("signer: New needs a context and a client")
	}
	s := &Signer{ctx: ctx, client: client, timeout: cfg.timeout, purpose: name}

	cctx, cancel := s.callContext()
	caps, err := client.Capabilities(cctx, connect.NewRequest(&keyv1alpha1.CapabilitiesRequest{}))
	cancel()
	if err != nil {
		return nil, fmt.Errorf("signer: Capabilities: %w", err)
	}
	var info *keyv1alpha1.PurposeInfo
	for _, p := range caps.Msg.GetPurposes() {
		if p.GetName() == name {
			info = p
			break
		}
	}
	if info == nil {
		return nil, ErrPurpose
	}
	if !slices.Contains(info.GetOperations(), string(purpose.Sign)) {
		return nil, ErrOperation
	}
	s.algorithm = info.GetAlgorithm()
	switch s.algorithm {
	case purpose.ES256, purpose.RS256:
		s.hash = crypto.SHA256
	case purpose.ES384:
		s.hash = crypto.SHA384
	default:
		return nil, ErrAlgorithm
	}

	cctx, cancel = s.callContext()
	pk, err := client.PublicKey(cctx, connect.NewRequest(&keyv1alpha1.PublicKeyRequest{Purpose: name}))
	cancel()
	if err != nil {
		return nil, fmt.Errorf("signer: PublicKey: %w", err)
	}
	s.keyID = pk.Msg.GetKeyId()
	if !strings.HasSuffix(s.keyID, keyid.Fingerprint(pk.Msg.GetSpkiDer())) {
		return nil, fmt.Errorf("%w: the key_id does not name the public key", ErrKey)
	}
	if s.pub, err = parsePublic(s.algorithm, pk.Msg.GetSpkiDer()); err != nil {
		return nil, err
	}
	return s, nil
}

// parsePublic reads a SubjectPublicKeyInfo and checks that the key is the key of the algorithm.
func parsePublic(algorithm string, spki []byte) (crypto.PublicKey, error) {
	key, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		return nil, fmt.Errorf("%w: not a SubjectPublicKeyInfo", ErrKey)
	}
	switch k := key.(type) {
	case *ecdsa.PublicKey:
		if (algorithm == purpose.ES256 && k.Curve == elliptic.P256()) || (algorithm == purpose.ES384 && k.Curve == elliptic.P384()) {
			return k, nil
		}
	case *rsa.PublicKey:
		if algorithm == purpose.RS256 && k.N.BitLen() == 2048 {
			return k, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrKey, algorithm)
}

// callContext is the context of one call of the port: the base context, narrowed by the timeout. A base deadline that is sooner stays.
func (s *Signer) callContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(s.ctx, s.timeout)
}

// Public returns the public key of the purpose, read once by New: an *ecdsa.PublicKey for ES256 and ES384, an *rsa.PublicKey for RS256.
func (s *Signer) Public() crypto.PublicKey { return s.pub }

// Algorithm returns the algorithm of the purpose: ES256, ES384 or RS256.
func (s *Signer) Algorithm() string { return s.algorithm }

// KeyID returns the key_id of the key that New saw, the same string that every signature of the Signer was checked against: the kid
// that a JWS names the key by.
func (s *Signer) KeyID() string { return s.keyID }

// Sign signs digest with the key of the purpose in the key store and returns the signature: ASN.1 DER for ES256 and ES384, the
// RSASSA-PKCS1-v1_5 signature for RS256.
//
// rand is ignored: the key store makes its own randomness (and an ECDSA nonce is the driver's business). opts says which hash made the
// digest and must be that of the algorithm, crypto.SHA256 for ES256 and RS256 and crypto.SHA384 for ES384 (ErrHash otherwise, and for
// nil opts); an *rsa.PSSOptions is refused (ErrPSS). The digest must have the length of that hash (ErrDigest).
//
// The signature is returned only after the key's answer is checked: it must name the key that New saw (ErrKeyChanged) and the public key
// must verify the signature (ErrVerify). A refusal of the port is returned wrapped, as it came, and errors.As finds the connect.Error in
// it. The call has the deadline of WithTimeout and ends when the context given to New does.
func (s *Signer) Sign(_ io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	if opts == nil {
		return nil, ErrHash
	}
	if _, pss := opts.(*rsa.PSSOptions); pss {
		return nil, ErrPSS
	}
	if opts.HashFunc() != s.hash {
		return nil, ErrHash
	}
	if len(digest) != s.hash.Size() {
		return nil, ErrDigest
	}

	cctx, cancel := s.callContext()
	defer cancel()
	res, err := s.client.Sign(cctx, connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: s.purpose, Digest: digest, Algorithm: s.algorithm}))
	if err != nil {
		return nil, fmt.Errorf("signer: Sign: %w", err)
	}
	if got := res.Msg.GetKeyId(); got != s.keyID {
		return nil, fmt.Errorf("%w: it was %q and the signature names %q", ErrKeyChanged, s.keyID, got)
	}
	sig := res.Msg.GetSignatureDer()
	if !s.verify(digest, sig) {
		return nil, ErrVerify
	}
	return sig, nil
}

// verify checks sig over digest with the public key, in the form the algorithm returns.
func (s *Signer) verify(digest, sig []byte) bool {
	switch k := s.pub.(type) {
	case *ecdsa.PublicKey:
		return ecdsa.VerifyASN1(k, digest, sig)
	case *rsa.PublicKey:
		return rsa.VerifyPKCS1v15(k, s.hash, digest, sig) == nil
	}
	return false
}
