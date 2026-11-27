//go:build atoom_card

package card

import (
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"

	"github.com/go-piv/piv-go/v2/piv"
)

// HasCardSupport is true in a build that talks to cards.
const HasCardSupport = true

// pivSlot maps a key reference to piv-go's slot. A slot outside 9a, 9c, 9d and 82 to 95 is not known.
func pivSlot(s Slot) (piv.Slot, bool) {
	switch s {
	case 0x9a:
		return piv.SlotAuthentication, true
	case 0x9c:
		return piv.SlotSignature, true
	case 0x9d:
		return piv.SlotKeyManagement, true
	}
	return piv.RetiredKeyManagementSlot(uint32(s))
}

// Open opens the card that sel names (OpenWith has the rules): exclusively through piv-go (SCardConnect with
// SCARD_SHARE_EXCLUSIVE: while it is open, no other process can talk to the card), and only the card of one reader. It
// lists the readers first, which connects to no card.
func Open(sel Selector) (Card, error) { return OpenWith(pivBackend{}, sel) }

// ListReaders lists the PC/SC reader names (SCardListReaders). It connects to no card.
func ListReaders() ([]string, error) { return pivBackend{}.Readers() }

// pivBackend is the real PC/SC side: piv-go over pcsc-lite.
type pivBackend struct{}

func (pivBackend) Readers() ([]string, error) { return piv.Cards() }

func (pivBackend) Connect(reader string) (Card, error) {
	yk, err := piv.Open(reader)
	if err != nil {
		if isSharingViolation(err) {
			return nil, SharingError(reader)
		}
		return nil, fmt.Errorf("card: open reader %q: %w", reader, err)
	}
	return &pivCard{yk: yk, reader: reader}, nil
}

// isSharingViolation tells SCARD_E_SHARING_VIOLATION from the text of piv-go's error, whose type is not exported: its table
// says "the smart card cannot be accessed because of other connections outstanding" for 0x8010000B.
func isSharingViolation(err error) bool {
	return strings.Contains(err.Error(), "other connections outstanding")
}

// Release lets the connection go and leaves the card as it was: no reset (a card of another serial is not ours to reset).
func (c *pivCard) Release() error {
	if c.shut {
		return nil
	}
	c.shut = true
	return c.yk.Close()
}

type pivCard struct {
	yk     *piv.YubiKey
	reader string
	// shut is set when the connection was released and not taken again (a failed ResetSession, or Close).
	shut bool
}

func (c *pivCard) Serial() (uint32, error) {
	n, err := c.yk.Serial()
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrCardGone, err) //nolint:errorlint // as above
	}
	return n, nil
}

func (c *pivCard) Version() Version {
	v := c.yk.Version()
	return Version{v.Major, v.Minor, v.Patch}
}

func (c *pivCard) AttestationCertificate() (*x509.Certificate, error) {
	cert, err := c.yk.AttestationCertificate()
	return cert, c.gone(err)
}

func (c *pivCard) Attest(s Slot) (*x509.Certificate, error) {
	ps, ok := pivSlot(s)
	if !ok {
		return nil, fmt.Errorf("card: slot %s is not a key slot", s)
	}
	cert, err := c.yk.Attest(ps)
	if errors.Is(err, piv.ErrNotFound) {
		// The card does not attest this slot: it is empty, or its key was imported (an imported key cannot be
		// attested). The card's metadata (firmware 5.3 and later) says which; an imported key is a refusal, and a
		// slot whose metadata cannot be read is empty.
		if ki, kerr := c.yk.KeyInfo(ps); kerr == nil && ki.Origin == piv.OriginImported {
			return nil, ErrImportedKey
		}
		return nil, ErrNoKey
	}
	return cert, c.gone(err)
}

// gone turns an error of the transport into ErrCardGone. piv-go starts one transaction at Open and never reconnects, so
// a card that was removed, reset or powered off fails every later call with an error of the PC/SC layer that this
// package cannot name (its type is not exported). An answer of the applet (a status word, a wrong PIN, a missing
// object) leaves the transaction alive: the test is whether the card still answers a call that needs nothing.
func (c *pivCard) gone(err error) error {
	if err == nil {
		return nil
	}
	var pe *PINError
	var ae piv.AuthErr
	if errors.As(err, &pe) || errors.As(err, &ae) || errors.Is(err, piv.ErrNotFound) || errors.Is(err, ErrManagementKey) {
		return err
	}
	if _, serr := c.yk.Serial(); serr != nil {
		return fmt.Errorf("%w: %v", ErrCardGone, err) //nolint:errorlint // as above
	}
	return err
}

// VerifyAttestation checks the chain of the slot certificate through the card's attestation certificate to Yubico's root with the library
// key/pivattest over its pinned certificates, and reads the extensions (attestation.go).
func (c *pivCard) VerifyAttestation(attCert, slotCert *x509.Certificate) (Attestation, error) {
	return verifyPinned(attCert, slotCert)
}

func (c *pivCard) Generate(managementKey []byte, s Slot, spec KeySpec) (crypto.PublicKey, error) {
	ps, ok := pivSlot(s)
	if !ok {
		return nil, fmt.Errorf("card: slot %s is not a key slot", s)
	}
	k := piv.Key{}
	switch spec.Algorithm {
	case EC256:
		k.Algorithm = piv.AlgorithmEC256
	case EC384:
		k.Algorithm = piv.AlgorithmEC384
	case RSA2048:
		k.Algorithm = piv.AlgorithmRSA2048
	default:
		return nil, errors.New("card: unsupported algorithm")
	}
	switch spec.PIN {
	case PINNever:
		k.PINPolicy = piv.PINPolicyNever
	case PINOnce:
		k.PINPolicy = piv.PINPolicyOnce
	default:
		return nil, errors.New("card: unsupported PIN policy")
	}
	if spec.Touch != TouchNever {
		return nil, errors.New("card: unsupported touch policy")
	}
	k.TouchPolicy = piv.TouchPolicyNever
	pub, err := c.yk.GenerateKey(managementKey, ps, k)
	if err != nil {
		var ae piv.AuthErr
		if errors.As(err, &ae) {
			return nil, ErrManagementKey
		}
		return nil, c.gone(err)
	}
	return pub, nil
}

func (c *pivCard) Login(pin []byte) error {
	// piv-go takes a string: one copy, for this call.
	err := c.yk.VerifyPIN(string(pin))
	return c.gone(pinError(err))
}

func (c *pivCard) PINRetries() (int, error) {
	n, err := c.yk.Retries()
	if isVerifiedAnswer(err) {
		return 0, ErrVerified
	}
	return n, c.gone(err)
}

// verifiedAnswer is what piv-go says when the empty VERIFY that reads the counter is answered with success, that is,
// when the card is logged in (piv.ykPINRetries; it has no error value of its own). The text is pinned by a test that
// builds with the tag.
const verifiedAnswer = "expected error code from empty pin"

func isVerifiedAnswer(err error) bool {
	return err != nil && strings.Contains(err.Error(), verifiedAnswer)
}

// ResetSession resets the session of the card: piv-go's exclusive connection is released, the reader is connected to
// exclusively and disconnected with SCARD_RESET_CARD (resetReader), and the card is opened again. If another process holds
// the card the reset fails with ErrSharing. If the card cannot be opened again, this Card is unusable.
func (c *pivCard) ResetSession() error {
	if c.shut {
		return ErrCardGone
	}
	_ = c.yk.Close()
	c.shut = true
	if err := resetReader(c.reader); err != nil {
		return err
	}
	yk, err := piv.Open(c.reader)
	if err != nil {
		return fmt.Errorf("%w: open again after the reset: %v", ErrCardGone, err) //nolint:errorlint // as above
	}
	c.yk, c.shut = yk, false
	return nil
}

func pinError(err error) error {
	var ae piv.AuthErr
	if errors.As(err, &ae) {
		return &PINError{Retries: ae.Retries}
	}
	return err
}

func (c *pivCard) key(s Slot, pub crypto.PublicKey, policy PINPolicy, pin PINSource) (crypto.PrivateKey, error) {
	ps, ok := pivSlot(s)
	if !ok {
		return nil, fmt.Errorf("card: slot %s is not a key slot", s)
	}
	auth := piv.KeyAuth{PINPolicy: piv.PINPolicyNever}
	if policy == PINOnce {
		// The policy is given, so piv-go does not ask the card for it on every call. PINPrompt runs only when the
		// card is logged out, and an error from it ends the call before any PIN is sent.
		prompt := func() (string, error) {
			b, err := pin()
			if err != nil {
				return "", err
			}
			s := string(b) // piv-go takes a string: one copy, for this call
			clear(b)
			return s, nil
		}
		auth = piv.KeyAuth{PINPolicy: piv.PINPolicyOnce, PINPrompt: prompt}
	}
	return c.yk.PrivateKey(ps, pub, auth)
}

// ecKey is the ECDSA key of a slot, and refuses a slot whose key piv-go would handle as another type.
func (c *pivCard) ecKey(s Slot, pub *ecdsa.PublicKey, policy PINPolicy, pin PINSource) (*piv.ECDSAPrivateKey, error) {
	priv, err := c.key(s, pub, policy, pin)
	if err != nil {
		return nil, err
	}
	ek, ok := priv.(*piv.ECDSAPrivateKey)
	if !ok {
		return nil, errors.New("card: the slot does not hold an ECDSA key")
	}
	return ek, nil
}

func (c *pivCard) Sign(s Slot, pub *ecdsa.PublicKey, policy PINPolicy, pin PINSource, digest []byte) ([]byte, error) {
	k, err := c.ecKey(s, pub, policy, pin)
	if err != nil {
		return nil, err
	}
	sig, err := k.Sign(nil, digest, crypto.Hash(0))
	return sig, c.gone(pinError(err))
}

// SignRSA signs a SHA-256 digest with the RSA key of the slot: piv-go pads it on the host (PKCS#1 v1.5, RFC 8017 section 9.2)
// and the card does one private-key operation.
func (c *pivCard) SignRSA(s Slot, pub *rsa.PublicKey, policy PINPolicy, pin PINSource, digest []byte) ([]byte, error) {
	if len(digest) != sha256.Size {
		return nil, errors.New("card: an RS256 digest is 32 octets")
	}
	priv, err := c.key(s, pub, policy, pin)
	if err != nil {
		return nil, err
	}
	signer, ok := priv.(crypto.Signer)
	if !ok {
		return nil, errors.New("card: the slot does not hold an RSA key")
	}
	if _, ok := signer.Public().(*rsa.PublicKey); !ok {
		return nil, errors.New("card: the slot does not hold an RSA key")
	}
	sig, err := signer.Sign(nil, digest, crypto.SHA256)
	return sig, c.gone(pinError(err))
}

func (c *pivCard) ECDH(s Slot, pub *ecdsa.PublicKey, policy PINPolicy, pin PINSource, peer *ecdh.PublicKey) ([]byte, error) {
	k, err := c.ecKey(s, pub, policy, pin)
	if err != nil {
		return nil, err
	}
	secret, err := k.ECDH(peer)
	return secret, c.gone(pinError(err))
}

// Close releases the card and leaves it logged out: the reset is made after the exclusive connection of piv-go is
// released, and a reset that fails (another process holds the card already) is reported, not retried.
func (c *pivCard) Close() error {
	if c.shut {
		return nil
	}
	c.shut = true
	err := c.yk.Close()
	if rerr := resetReader(c.reader); rerr != nil && err == nil {
		err = rerr
	}
	return err
}
