// Package wrap wraps a data key for a recipient and unwraps it (docs/seal-block.md, "Wraps"): ECDH-ES on P-384 with an ephemeral sender
// key, HKDF-SHA384 for the key-encryption key, and AES key wrap (RFC 3394). Moved from sluis key@; the bytes are the same for the same
// context.
//
// Making a wrap needs only the recipient's public key. Opening one needs the recipient's private key, which stays in the key store: the
// caller does the ECDH there, through the key port, and passes the shared secret to Unwrap.
package wrap

import (
	"crypto/aes"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha512"
	"encoding/binary"
	"errors"

	josecipher "github.com/go-jose/go-jose/v4/cipher"
)

const (
	kekLen = 32
	// DataKeyLen is the length of a data key.
	DataKeyLen = 32
)

// Context is what a wrap is bound to. It is the info of the key derivation, so a wrap cannot be opened as another key, in another
// installation or scope, or by another recipient.
type Context struct {
	Domain       string // the host's label: "sluis-wrap-v1" for sluis
	Installation string // the host's name of the installation: sluis's boundary
	Scope        string // the scope of the data key, "-" for the installation's own
	Purpose      string
	Generation   uint32
	Recipient    string
}

func appendString(b []byte, s string) []byte {
	b = binary.BigEndian.AppendUint16(b, uint16(len(s))) //nolint:gosec // every part is bounded well below 64 KiB by the block
	return append(b, s...)
}

// info is domain | u16 len | installation | u16 len | scope | u16 len | purpose | u32 generation | u16 len | recipient.
func (c Context) info() []byte {
	b := []byte(c.Domain)
	b = appendString(b, c.Installation)
	b = appendString(b, c.Scope)
	b = appendString(b, c.Purpose)
	b = binary.BigEndian.AppendUint32(b, c.Generation)
	return appendString(b, c.Recipient)
}

// kek derives the key-encryption key: HKDF-SHA384 (RFC 5869) with an empty salt.
func (c Context) kek(secret []byte) ([]byte, error) {
	return hkdf.Key(sha512.New384, secret, nil, string(c.info()), kekLen)
}

// ParsePublic reads a SEC1 uncompressed P-384 point.
func ParsePublic(sec1 []byte) (*ecdh.PublicKey, error) {
	return ecdh.P384().NewPublicKey(sec1)
}

// Wrap wraps dataKey for the recipient's public key. It returns the ephemeral public key (SEC1 uncompressed) and the wrapped key.
func Wrap(c Context, recipient *ecdh.PublicKey, dataKey []byte) (ephemeralPublic, wrapped []byte, err error) {
	if len(dataKey) != DataKeyLen {
		return nil, nil, errors.New("wrap: data key is not 32 bytes")
	}
	eph, err := ecdh.P384().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	secret, err := eph.ECDH(recipient)
	if err != nil {
		return nil, nil, err
	}
	defer clear(secret)
	kek, err := c.kek(secret)
	if err != nil {
		return nil, nil, err
	}
	defer clear(kek)
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, nil, err
	}
	wrapped, err = josecipher.KeyWrap(block, dataKey)
	if err != nil {
		return nil, nil, err
	}
	return eph.PublicKey().Bytes(), wrapped, nil
}

// Unwrap opens a wrapped key from the shared secret that the recipient's key made with the ephemeral public key. A wrong secret,
// context or wrapped key is an error that says nothing more.
func Unwrap(c Context, secret, wrapped []byte) ([]byte, error) {
	kek, err := c.kek(secret)
	if err != nil {
		return nil, err
	}
	defer clear(kek)
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	key, err := josecipher.KeyUnwrap(block, wrapped)
	if err != nil {
		return nil, errors.New("wrap: cannot unwrap")
	}
	if len(key) != DataKeyLen {
		clear(key)
		return nil, errors.New("wrap: unwrapped key has the wrong length")
	}
	return key, nil
}
