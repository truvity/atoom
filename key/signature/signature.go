// Package signature converts an ECDSA signature between the two forms that meet at the key port: the ASN.1 DER form that Sign
// returns (RFC 3279, section 2.2.3: SEQUENCE { r INTEGER, s INTEGER }) and the raw form that a JWS carries (RFC 7518, section 3.4:
// r then s, each as a fixed-length big-endian octet string, 64 octets for ES256 and 96 for ES384).
//
// The port returns DER and stays so (docs/algorithms.md, "DER and raw signatures"): every driver and every verifier already speaks it, and
// the conversion is a few lines that a JWS signer calls once. DERToRaw is that call. RS256 has no DER form and needs no conversion: its
// signature is the octet string of RFC 8017 that a JWS carries as it is.
package signature

import (
	"encoding/asn1"
	"errors"
	"math/big"
)

// The algorithms the conversion knows.
const (
	ES256 = "ES256"
	ES384 = "ES384"
)

// ErrAlgorithm is an algorithm that is not ES256 or ES384.
var ErrAlgorithm = errors.New("signature: the algorithm is not ES256 or ES384")

// ErrSignature is a signature that is not of the form it is said to be, or whose values do not fit the curve.
var ErrSignature = errors.New("signature: not a well-formed ECDSA signature for the algorithm")

// Size is the length in octets of each of r and s for the algorithm: 32 for ES256, 48 for ES384, and 0 for any other.
func Size(algorithm string) int {
	switch algorithm {
	case ES256:
		return 32
	case ES384:
		return 48
	}
	return 0
}

// ecdsaSig is the DER form.
type ecdsaSig struct{ R, S *big.Int }

// DERToRaw converts the DER signature that Sign returns to the raw r||s form of a JWS: 2 x Size(algorithm) octets. It refuses a
// signature that is not strict DER, that has trailing bytes, or whose r or s is zero, negative or wider than the curve.
func DERToRaw(algorithm string, der []byte) ([]byte, error) {
	n := Size(algorithm)
	if n == 0 {
		return nil, ErrAlgorithm
	}
	var sig ecdsaSig
	rest, err := asn1.Unmarshal(der, &sig)
	if err != nil || len(rest) != 0 || sig.R == nil || sig.S == nil {
		return nil, ErrSignature
	}
	if sig.R.Sign() <= 0 || sig.S.Sign() <= 0 || sig.R.BitLen() > n*8 || sig.S.BitLen() > n*8 {
		return nil, ErrSignature
	}
	raw := make([]byte, 2*n)
	sig.R.FillBytes(raw[:n])
	sig.S.FillBytes(raw[n:])
	return raw, nil
}

// RawToDER is the other way: it converts the raw r||s of a JWS (2 x Size(algorithm) octets) to the DER form that crypto/ecdsa's
// VerifyASN1 and the port take. It refuses a raw signature of another length, and one whose r or s is zero.
func RawToDER(algorithm string, raw []byte) ([]byte, error) {
	n := Size(algorithm)
	if n == 0 {
		return nil, ErrAlgorithm
	}
	if len(raw) != 2*n {
		return nil, ErrSignature
	}
	sig := ecdsaSig{R: new(big.Int).SetBytes(raw[:n]), S: new(big.Int).SetBytes(raw[n:])}
	if sig.R.Sign() == 0 || sig.S.Sign() == 0 {
		return nil, ErrSignature
	}
	der, err := asn1.Marshal(sig)
	if err != nil {
		return nil, ErrSignature
	}
	return der, nil
}
