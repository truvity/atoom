package token

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"unicode/utf8"

	"github.com/truvity/atoom/key/purpose"
)

// b64 is base64url without padding (RFC 7515, section 2), strict on decoding: no padding, no white space, no stray bits.
var b64 = base64.RawURLEncoding.Strict()

// publicKey is a parsed public key of one of the three algorithms.
type publicKey struct {
	algorithm string
	spki      []byte
	ec        *ecdsa.PublicKey
	rsa       *rsa.PublicKey
}

var errKey = errors.New("token: not a public key of its algorithm")

// parsePublic parses a SubjectPublicKeyInfo and holds it to the algorithm: P-256 for ES256, P-384 for ES384, RSA of 2048 bits or more
// with an odd exponent for RS256.
func parsePublic(algorithm string, spki []byte) (*publicKey, error) {
	k, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		return nil, errKey
	}
	out := &publicKey{algorithm: algorithm, spki: bytes.Clone(spki)}
	switch algorithm {
	case purpose.ES256, purpose.ES384:
		ec, ok := k.(*ecdsa.PublicKey)
		want := elliptic.P256()
		if algorithm == purpose.ES384 {
			want = elliptic.P384()
		}
		if !ok || ec.Curve != want {
			return nil, errKey
		}
		out.ec = ec
	case purpose.RS256:
		r, ok := k.(*rsa.PublicKey)
		if !ok || r.N.BitLen() < 2048 || r.E < 3 || r.E%2 == 0 {
			return nil, errKey
		}
		out.rsa = r
	default:
		return nil, errKey
	}
	return out, nil
}

// size is the length of each of r and s in the raw signature of an ECDSA key.
func (k *publicKey) size() int { return (k.ec.Curve.Params().BitSize + 7) / 8 }

// digest hashes the signing input for the algorithm.
func digest(algorithm string, input []byte) ([]byte, crypto.Hash) {
	if algorithm == purpose.ES384 {
		d := sha512.Sum384(input)
		return d[:], crypto.SHA384
	}
	d := sha256.Sum256(input)
	return d[:], crypto.SHA256
}

// verify checks a JWS signature (raw r||s for ECDSA, RFC 7518 section 3.4; the PKCS#1 v1.5 octets for RS256, section 3.3) over input.
func (k *publicKey) verify(input, sig []byte) bool {
	d, h := digest(k.algorithm, input)
	if k.rsa != nil {
		return rsa.VerifyPKCS1v15(k.rsa, h, d, sig) == nil
	}
	n := k.size()
	if len(sig) != 2*n {
		return false
	}
	r, s := new(big.Int).SetBytes(sig[:n]), new(big.Int).SetBytes(sig[n:])
	return ecdsa.Verify(k.ec, d, r, s)
}

// jwk is one entry of a key set, with its members in a fixed order: kty, the key's members, kid, alg, use, x5c.
type jwk struct {
	Kty string   `json:"kty"`
	Crv string   `json:"crv,omitempty"`
	X   string   `json:"x,omitempty"`
	Y   string   `json:"y,omitempty"`
	N   string   `json:"n,omitempty"`
	E   string   `json:"e,omitempty"`
	Kid string   `json:"kid"`
	Alg string   `json:"alg"`
	Use string   `json:"use"`
	X5c []string `json:"x5c,omitempty"`
}

// jwk is the key as a JWK (RFC 7518, sections 6.2.1 and 6.3.1), with its kid and chain.
func (k *publicKey) jwk(kid string, x5c [][]byte) jwk {
	out := jwk{Kid: kid, Alg: k.algorithm, Use: "sig"}
	if k.rsa != nil {
		out.Kty, out.N, out.E = "RSA", b64.EncodeToString(k.rsa.N.Bytes()), b64.EncodeToString(big.NewInt(int64(k.rsa.E)).Bytes())
	} else {
		// The uncompressed point is 0x04, then x and y, each of the curve's size (SEC 1, section 2.3.3).
		point, _ := k.ec.Bytes() // a key that x509 parsed is on its curve
		n := k.size()
		out.Kty, out.Crv = "EC", k.ec.Curve.Params().Name
		out.X, out.Y = b64.EncodeToString(point[1:1+n]), b64.EncodeToString(point[1+n:])
	}
	for _, c := range x5c {
		out.X5c = append(out.X5c, base64.StdEncoding.EncodeToString(c))
	}
	return out
}

// thumbprint is the RFC 7638 thumbprint of the key: the SHA-256 of its required members in lexicographic order with no white space,
// base64url without padding.
func (k *publicKey) thumbprint() string {
	j := k.jwk("", nil)
	// The members are base64url and curve names, which JSON spells as they are; the struct fields are in lexicographic order.
	var canonical []byte
	if k.rsa != nil {
		canonical = marshal(struct {
			E   string `json:"e"`
			Kty string `json:"kty"`
			N   string `json:"n"`
		}{j.E, "RSA", j.N})
	} else {
		canonical = marshal(struct {
			Crv string `json:"crv"`
			Kty string `json:"kty"`
			X   string `json:"x"`
			Y   string `json:"y"`
		}{j.Crv, "EC", j.X, j.Y})
	}
	sum := sha256.Sum256(canonical)
	return b64.EncodeToString(sum[:])
}

// marshal writes v as JSON without HTML escaping, so a kid of '<', '>' or '&' is spelled as it is.
func marshal(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v) // the values are strings and slices of strings
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// header is the protected header of every token of the block: exactly alg, kid and typ, in that order.
type header struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

var errJSON = errors.New("token: not one strict JSON object")

// strictObject checks that raw is one JSON object (RFC 8259) and nothing else: valid UTF-8, no escape of a lone surrogate, no member
// name twice in any object, at any depth, and nothing after the object but white space.
func strictObject(raw []byte) error {
	if !utf8.Valid(raw) || loneSurrogate(raw) {
		return errJSON
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	t, err := dec.Token()
	if err != nil || t != json.Delim('{') {
		return errJSON
	}
	if err := object(dec); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errJSON
	}
	return nil
}

// object reads the members of an object whose '{' was read, through its '}'.
func object(dec *json.Decoder) error {
	seen := map[string]bool{}
	for {
		t, err := dec.Token()
		if err != nil {
			return errJSON
		}
		if t == json.Delim('}') {
			return nil
		}
		name, ok := t.(string)
		if !ok || seen[name] {
			return errJSON
		}
		seen[name] = true
		if err := value(dec); err != nil {
			return err
		}
	}
}

// value reads one value.
func value(dec *json.Decoder) error {
	t, err := dec.Token()
	if err != nil {
		return errJSON
	}
	switch t {
	case json.Delim('{'):
		return object(dec)
	case json.Delim('['):
		for dec.More() {
			if err := value(dec); err != nil {
				return err
			}
		}
		if t, err := dec.Token(); err != nil || t != json.Delim(']') {
			return errJSON
		}
	}
	return nil
}

// loneSurrogate reports whether a string of raw holds a \u escape of a surrogate that is not a high surrogate followed by an escaped
// low one. encoding/json would read such an escape as U+FFFD, so two parsers could read the claims apart.
func loneSurrogate(raw []byte) bool {
	in := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case !in:
			in = c == '"'
		case c == '"':
			in = false
		case c == '\\' && i+1 < len(raw):
			i++
			if raw[i] != 'u' {
				continue
			}
			u, ok := hex4(raw, i+1)
			if !ok {
				return false // not an escape at all: the decoder refuses it
			}
			i += 4
			switch {
			case u >= 0xdc00 && u <= 0xdfff:
				return true
			case u >= 0xd800 && u <= 0xdbff:
				if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
					return true
				}
				lo, ok := hex4(raw, i+3)
				if !ok || lo < 0xdc00 || lo > 0xdfff {
					return true
				}
				i += 6
			}
		}
	}
	return false
}

func hex4(raw []byte, at int) (uint16, bool) {
	if at+4 > len(raw) {
		return 0, false
	}
	var v uint16
	for _, c := range raw[at : at+4] {
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v |= uint16(c - '0')
		case c >= 'a' && c <= 'f':
			v |= uint16(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v |= uint16(c-'A') + 10
		default:
			return 0, false
		}
	}
	return v, true
}

// parseHeader reads a protected header: one strict JSON object of exactly alg, kid and typ, each a non-empty string.
func parseHeader(raw []byte) (header, bool) {
	if strictObject(raw) != nil {
		return header{}, false
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil || len(m) != 3 {
		return header{}, false
	}
	var h header
	for name, dst := range map[string]*string{"alg": &h.Alg, "kid": &h.Kid, "typ": &h.Typ} {
		s, ok := m[name].(string)
		if !ok || s == "" {
			return header{}, false
		}
		*dst = s
	}
	return h, true
}
