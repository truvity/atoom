// Package envelope is the byte layout of a sealed value (docs/seal-block.md, "The envelope"):
//
//	envelope = version (1 byte, 0x01) | code (1 byte) | generation (4 bytes, big-endian)
//	         | nonce (12 bytes) | ciphertext | tag (16 bytes)
//	aad      = domain | version | code | generation | (u16 length | part) for each part of the address
//
// The cipher is AES-256-GCM. The nonce comes from the cryptographic library (cipher.NewGCMWithRandomNonce), never from the caller. The
// domain is the host's label (sluis's is "sluis-seal-v1", and its address is boundary, tenant or "-", kind and id), so that the layout and
// the bytes are those of sluis key@, from which it moved.
package envelope

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
)

const (
	// Version is the only version of the layout.
	Version byte = 0x01
	// HeaderLen is the length of version, code and generation.
	HeaderLen = 6
	// MinLen is the shortest envelope: the header, the 12-byte nonce, an empty ciphertext and the 16-byte tag.
	MinLen = HeaderLen + 12 + 16
	// KeyLen is the length of a data key.
	KeyLen = 32
	// MaxPart is the longest part of an address: its length is 16 bits in the AAD.
	MaxPart = 1024
)

// ErrOpen is the one error of Open: it does not say which part failed.
var ErrOpen = errors.New("envelope: cannot open")

// Header is the clear header of an envelope.
type Header struct {
	Code       byte
	Generation uint32
}

// ParseHeader reads the header of an envelope. It does not authenticate it: Open does, through the AAD.
func ParseHeader(env []byte) (Header, error) {
	if len(env) < MinLen || env[0] != Version {
		return Header{}, ErrOpen
	}
	return Header{Code: env[1], Generation: binary.BigEndian.Uint32(env[2:6])}, nil
}

// AAD returns the additional authenticated data of an envelope. Each part is at most MaxPart bytes; the caller checks.
func AAD(domain string, code byte, generation uint32, address []string) []byte {
	n := len(domain) + 6
	for _, p := range address {
		n += 2 + len(p)
	}
	b := make([]byte, 0, n)
	b = append(b, domain...)
	b = append(b, Version, code)
	b = binary.BigEndian.AppendUint32(b, generation)
	for _, p := range address {
		b = binary.BigEndian.AppendUint16(b, uint16(len(p))) //nolint:gosec // bounded by MaxPart, checked by the caller
		b = append(b, p...)
	}
	return b
}

func gcm(key []byte) (cipher.AEAD, error) {
	if len(key) != KeyLen {
		return nil, errors.New("envelope: data key is not 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	// The library draws the nonce from crypto/rand and puts it before the ciphertext.
	return cipher.NewGCMWithRandomNonce(block)
}

// Seal seals plaintext under key, bound to the domain and the address. The header is part of the authenticated data.
func Seal(key []byte, domain string, code byte, generation uint32, address []string, plaintext []byte) ([]byte, error) {
	aead, err := gcm(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, MinLen+len(plaintext))
	out = append(out, Version, code)
	out = binary.BigEndian.AppendUint32(out, generation)
	// The nil nonce is by design: an AEAD from NewGCMWithRandomNonce draws the nonce itself.
	return aead.Seal(out, nil, plaintext, AAD(domain, code, generation, address)), nil //nolint:gosec // G407: see above
}

// Open opens an envelope under key, bound to the domain and the address. Any failure, a changed header byte included, is ErrOpen.
func Open(key []byte, domain string, env []byte, address []string) ([]byte, error) {
	h, err := ParseHeader(env)
	if err != nil {
		return nil, err
	}
	aead, err := gcm(key)
	if err != nil {
		return nil, err
	}
	pt, err := aead.Open(nil, nil, env[HeaderLen:], AAD(domain, h.Code, h.Generation, address))
	if err != nil {
		return nil, ErrOpen
	}
	return pt, nil
}
