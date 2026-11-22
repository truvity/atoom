package seal

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"strings"
)

// recipientLabel is the label of a wrapping public key in the keys of its wraps and in their key derivation: the lower-case base32,
// without padding, of its RFC 7638 thumbprint (52 characters). It is sluis key@'s label, so a wrap that sluis made keeps its name.
func recipientLabel(pub *ecdh.PublicKey) string {
	sum := thumbprint(pub)
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:]))
}

// thumbprint is the JWK thumbprint of RFC 7638, section 3.2, of a P-384 key: SHA-256 of the JSON object with the members crv, kty, x and
// y in that order and no white space, the coordinates at the full length of the curve (RFC 7518, section 6.2.1.2).
func thumbprint(pub *ecdh.PublicKey) [32]byte {
	point := pub.Bytes() // 0x04 | X | Y
	n := (len(point) - 1) / 2
	enc := base64.RawURLEncoding
	j := `{"crv":"P-384","kty":"EC","x":"` + enc.EncodeToString(point[1:1+n]) + `","y":"` + enc.EncodeToString(point[1+n:]) + `"}`
	return sha256.Sum256([]byte(j))
}

// parseP384 reads a SubjectPublicKeyInfo in DER that holds a P-384 key, as the key port gives the wrapping key.
func parseP384(spki []byte) (*ecdh.PublicKey, error) {
	k, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		return nil, errors.New("seal: the wrapping key does not parse")
	}
	ec, ok := k.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("seal: the wrapping key is not an EC key")
	}
	pub, err := ec.ECDH()
	if err != nil || pub.Curve() != ecdh.P384() {
		return nil, errors.New("seal: the wrapping key is not on P-384")
	}
	return pub, nil
}
