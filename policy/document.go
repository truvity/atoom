package policy

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"slices"
	"strconv"
	"unicode/utf8"
)

// MaxDocument is the largest signed document a loader reads: 3 MiB (record 0019). A larger one is refused before a byte of it is parsed.
const MaxDocument = 3 << 20

// seqLimit: seq is below 2^53, so that a JSON number holds it exactly in every reader.
const seqLimit = 1 << 53

// signatureLen is an ECDSA P-384 signature, r then s, 48 bytes each (RFC 7518, section 3.4).
const signatureLen = 96

// Publisher is a key the host pins: the kid that a signature names, the P-384 public key, and the kinds whose documents need this
// key's signature as well (a co-signer). A key is pinned by the host's own configuration and never by a document.
type Publisher struct {
	KID     string
	Key     *ecdsa.PublicKey
	CoSigns []string
}

// PublisherFromSPKI is a Publisher from the DER of a SubjectPublicKeyInfo of a P-384 key.
func PublisherFromSPKI(kid string, spkiDER []byte, coSigns ...string) (Publisher, error) {
	k, err := x509.ParsePKIXPublicKey(spkiDER)
	if err != nil {
		return Publisher{}, fmt.Errorf("policy: publisher %q: %w", kid, err)
	}
	ec, ok := k.(*ecdsa.PublicKey)
	if !ok || ec.Curve != elliptic.P384() {
		return Publisher{}, fmt.Errorf("policy: publisher %q: not a P-384 key", kid)
	}
	return Publisher{KID: kid, Key: ec, CoSigns: coSigns}, nil
}

// Refused is a document refused at a step of the signature layer (record 0019, "The signed document"): the step and the rule. It never
// holds a value of the document.
type Refused struct {
	Step int
	Rule string
}

func (r *Refused) Error() string {
	return fmt.Sprintf("policy: signed document: step %d: %s", r.Step, r.Rule)
}

func refuse(step int, rule string) error { return &Refused{Step: step, Rule: rule} }

// Verified is a document whose signatures verified under the pinned keys.
type Verified struct {
	Seq     uint64
	Payload []byte   // the signed bytes, as they were signed
	Digest  [48]byte // the SHA-384 of the payload
	KIDs    []string // the kids that signed it, in the order of the signatures
}

// DigestHex is the digest in lower-case hex, as an audit record and sha384sum show it.
func (v *Verified) DigestHex() string { return hex.EncodeToString(v.Digest[:]) }

// b64 is base64url without padding (RFC 7515, section 2). Strict refuses a final character whose unused bits are not zero.
var b64 = base64.RawURLEncoding.Strict()

// decode64 decodes base64url strictly. encoding/base64 skips a carriage return and a line feed anywhere in its input, which the Rust
// loader refuses, so a byte outside the alphabet is refused here before the decoder sees it.
func decode64(s string) ([]byte, error) {
	for i := range len(s) {
		c := s[i]
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return nil, errors.New("not base64url")
		}
	}
	return b64.DecodeString(s)
}

type signature struct {
	protected, signature string
	kid                  string
	seq                  uint64
}

// VerifyDocument takes steps 1 to 4 of record 0019 for the document doc of the kind `kind` under the pinned publishers. Nothing before
// the signatures reads the payload. The sequence (step 5) is Compare's, and the payload is ParseBundle's.
func VerifyDocument(doc []byte, kind string, publishers []Publisher) (*Verified, error) {
	if len(doc) > MaxDocument {
		return nil, refuse(1, "the document is larger than the bound")
	}
	// Step 1: structure. The general JSON serialisation; the compact and the flattened ones, an unprotected `header` and any other
	// member are all refused here.
	top, ok := parseObject(doc)
	if !ok {
		return nil, refuse(1, "not a JSON object in the general serialisation")
	}
	payload, okP := top.str("payload")
	sigs, okS := top.arr("signatures")
	if !top.exactly("payload", "signatures") || !okP || !okS {
		return nil, refuse(1, "needs a payload and signatures, and no other member")
	}
	if len(sigs) == 0 {
		return nil, refuse(1, "needs at least one signature")
	}
	type pair struct{ protected, signature string }
	parsed := make([]pair, 0, len(sigs))
	for _, s := range sigs {
		o, isObj := s.(*object)
		if !isObj || !o.exactly("protected", "signature") {
			return nil, refuse(1, "a signature has protected and signature, and no other member")
		}
		p, ok1 := o.str("protected")
		g, ok2 := o.str("signature")
		if !ok1 || !ok2 {
			return nil, refuse(1, "a signature has protected and signature, and no other member")
		}
		parsed = append(parsed, pair{p, g})
	}

	// Step 2: headers.
	signatures := make([]signature, 0, len(parsed))
	for _, p := range parsed {
		raw, err := decode64(p.protected)
		if err != nil {
			return nil, refuse(2, "the protected header is not base64url JSON")
		}
		h, ok := parseObject(raw)
		if !ok {
			return nil, refuse(2, "the protected header is not base64url JSON")
		}
		alg, okA := h.str("alg")
		kid, okK := h.str("kid")
		seqNum, okN := h.num("seq")
		named, okD := h.str("kind")
		if !h.exactly("alg", "kid", "seq", "kind") || !okA || !okK || !okN || !okD {
			return nil, refuse(2, "the protected header needs alg, kid, seq and kind, and no other member")
		}
		if alg != "ES384" {
			return nil, refuse(2, "alg must be ES384")
		}
		// The signed header names the kind, so a document of one kind cannot be served as another's.
		if named != kind {
			return nil, refuse(2, "kind is not the kind of the document asked for")
		}
		seq, err := strconv.ParseUint(seqNum, 10, 64)
		if err != nil || seq < 1 || seq >= seqLimit {
			return nil, refuse(2, "seq must be a positive integer below 2^53")
		}
		signatures = append(signatures, signature{protected: p.protected, signature: p.signature, kid: kid, seq: seq})
	}
	seq := signatures[0].seq
	for _, s := range signatures {
		if s.seq != seq {
			return nil, refuse(2, "the signatures carry different seq")
		}
	}

	// Step 3: keys. A kid names a pinned key; nothing in the document names or carries a key.
	used := map[string]bool{}
	keys := make([]Publisher, 0, len(signatures))
	for _, s := range signatures {
		i := slices.IndexFunc(publishers, func(p Publisher) bool { return p.KID == s.kid })
		if i < 0 {
			return nil, refuse(3, "a kid is not pinned")
		}
		if used[s.kid] {
			return nil, refuse(3, "a kid is used twice")
		}
		used[s.kid] = true
		keys = append(keys, publishers[i])
	}

	// Step 4: signatures, over the bytes as they were received (RFC 7515, section 5.2: the signing input is ASCII(protected) "."
	// payload).
	for i, s := range signatures {
		raw, err := decode64(s.signature)
		if err != nil || len(raw) != signatureLen {
			return nil, refuse(4, "a signature is not 96 bytes of r then s")
		}
		k := keys[i].Key
		if k == nil || k.Curve != elliptic.P384() {
			return nil, refuse(4, "a signature does not verify")
		}
		digest := sha512.Sum384([]byte(s.protected + "." + payload))
		r := new(big.Int).SetBytes(raw[:48])
		sv := new(big.Int).SetBytes(raw[48:])
		if !ecdsa.Verify(k, digest[:], r, sv) {
			return nil, refuse(4, "a signature does not verify")
		}
	}
	// One publisher key without co-signs, and every key whose co-signs name this kind.
	if !slices.ContainsFunc(keys, func(p Publisher) bool { return len(p.CoSigns) == 0 }) {
		return nil, refuse(4, "no publisher key signed it")
	}
	if slices.ContainsFunc(publishers, func(p Publisher) bool { return slices.Contains(p.CoSigns, kind) && !used[p.KID] }) {
		return nil, refuse(4, "a co-signer's signature is missing")
	}
	body, err := decode64(payload)
	if err != nil {
		return nil, refuse(4, "the payload is not base64url")
	}
	kids := make([]string, len(signatures))
	for i, s := range signatures {
		kids[i] = s.kid
	}
	return &Verified{Seq: seq, Payload: body, Digest: sha512.Sum384(body), KIDs: kids}, nil
}

// Sequence is what the sequence number says about a verified document against the one in force (record 0019, step 5).
type Sequence int

// Newer: a higher seq than the one in force, or nothing in force; it replaces it. Same: the same seq and the same payload; nothing
// changes. Conflict: the same seq with another payload; refused, two documents must not share a number. Older: a lower seq; refused, an
// older document must not come back.
const (
	Newer Sequence = iota
	Same
	Conflict
	Older
)

func (s Sequence) String() string {
	switch s {
	case Newer:
		return "newer"
	case Same:
		return "same"
	case Conflict:
		return "conflict"
	case Older:
		return "older"
	}
	return "unknown"
}

// Compare is step 5 for v against the seq and payload digest in force; inForce false means nothing is in force.
func Compare(inForce bool, seq uint64, digest [48]byte, v *Verified) Sequence {
	switch {
	case !inForce, v.Seq > seq:
		return Newer
	case v.Seq < seq:
		return Older
	case v.Digest == digest:
		return Same
	}
	return Conflict
}

// object is a JSON object read with the one rule encoding/json does not have: a duplicate key is an error, at any depth. Its members are
// kept in order.
type object struct {
	keys []string
	vals []any // *object, []any, json.Number, string, bool or nil
}

func (o *object) get(k string) (any, bool) {
	i := slices.Index(o.keys, k)
	if i < 0 {
		return nil, false
	}
	return o.vals[i], true
}

// exactly reports whether the keys of the object are exactly names, in any order.
func (o *object) exactly(names ...string) bool {
	if len(o.keys) != len(names) {
		return false
	}
	for _, n := range names {
		if !slices.Contains(o.keys, n) {
			return false
		}
	}
	return true
}

// only reports whether every key of the object is one of names.
func (o *object) only(names ...string) bool {
	for _, k := range o.keys {
		if !slices.Contains(names, k) {
			return false
		}
	}
	return true
}

func (o *object) str(k string) (string, bool) {
	v, _ := o.get(k)
	s, ok := v.(string)
	return s, ok
}

func (o *object) arr(k string) ([]any, bool) {
	v, _ := o.get(k)
	a, ok := v.([]any)
	return a, ok
}

// num returns the text of a number member. A number that is not a plain non-negative integer fails to parse as one where it is used,
// which is how a float, an exponent or a negative number is refused.
func (o *object) num(k string) (string, bool) {
	v, _ := o.get(k)
	n, ok := v.(json.Number)
	return string(n), ok
}

// parseObject reads bytes as one JSON object, and nothing after it. Bytes that are not UTF-8 are refused, as the Rust loader does;
// encoding/json would replace them.
func parseObject(b []byte) (*object, bool) {
	if !utf8.Valid(b) || loneSurrogate(b) {
		return nil, false
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	v, err := readAt(d, 0)
	if err != nil {
		return nil, false
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return nil, false
	}
	o, ok := v.(*object)
	return o, ok
}

// loneSurrogate reports whether the JSON text has a \u escape of a surrogate that is not half of a pair. The Rust reader refuses it;
// encoding/json would read it as U+FFFD, and the two would then disagree about a document.
func loneSurrogate(b []byte) bool {
	hex4 := func(i int) (rune, bool) {
		if i+6 > len(b) || b[i] != '\\' || b[i+1] != 'u' {
			return 0, false
		}
		var r rune
		for _, c := range b[i+2 : i+6] {
			switch {
			case c >= '0' && c <= '9':
				r = r<<4 | rune(c-'0')
			case c >= 'a' && c <= 'f':
				r = r<<4 | rune(c-'a'+10)
			case c >= 'A' && c <= 'F':
				r = r<<4 | rune(c-'A'+10)
			default:
				return 0, false
			}
		}
		return r, true
	}
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' {
			continue
		}
		if i+1 < len(b) && b[i+1] == 'u' {
			r, ok := hex4(i)
			switch {
			case ok && r >= 0xD800 && r <= 0xDBFF:
				low, ok2 := hex4(i + 6)
				if !ok2 || low < 0xDC00 || low > 0xDFFF {
					return true
				}
				i += 11
			case ok && r >= 0xDC00 && r <= 0xDFFF:
				return true
			default:
				i += 5
			}
			continue
		}
		i++ // the escaped character: \\ and \" are skipped as a pair
	}
	return false
}

// maxDepth bounds the nesting that the reader follows: the stack, not the format.
const maxDepth = 64

func readAt(d *json.Decoder, depth int) (any, error) {
	if depth > maxDepth {
		return nil, errors.New("too deep")
	}
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	delim, isDelim := t.(json.Delim)
	if !isDelim {
		return t, nil // string, json.Number, bool or nil
	}
	switch delim {
	case '{':
		o := &object{}
		seen := map[string]struct{}{}
		for d.More() {
			kt, err := d.Token()
			if err != nil {
				return nil, err
			}
			k, ok := kt.(string)
			if !ok {
				return nil, errors.New("key")
			}
			if _, dup := seen[k]; dup {
				return nil, errors.New("duplicate key")
			}
			seen[k] = struct{}{}
			v, err := readAt(d, depth+1)
			if err != nil {
				return nil, err
			}
			o.keys = append(o.keys, k)
			o.vals = append(o.vals, v)
		}
		if _, err := d.Token(); err != nil {
			return nil, err
		}
		return o, nil
	case '[':
		a := []any{}
		for d.More() {
			v, err := readAt(d, depth+1)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		if _, err := d.Token(); err != nil {
			return nil, err
		}
		return a, nil
	}
	return nil, errors.New("unexpected delimiter")
}
