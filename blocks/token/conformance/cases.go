package conformance

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/blocks/token"
	tokenv1alpha1 "github.com/truvity/atoom/blocks/token/gen/atoom/token/v1alpha1"
	"github.com/truvity/atoom/common/refusal"
	"github.com/truvity/atoom/key/purpose"
)

// Cases are the kit's cases, in the order they run (docs/token-block.md, "The kit").
func Cases() []Case {
	return []Case{
		{"capabilities", capabilities},
		{"sign_is_a_compact_jws_of_the_claims", signCase},
		{"the_kid_follows_the_purposes_rule", kidRule},
		{"the_key_set_publishes_the_current_and_the_hosts_keys", keySet},
		{"verify_accepts_what_sign_made", verifyRoundTrip},
		{"verify_refuses_any_change", verifyTampered},
		{"verify_with_a_hosts_key", verifyHostKey},
		{"the_claims_are_one_strict_object", strictClaims},
		{RulesCase, rules},
		{"what_is_not_a_call_is_refused", refusals},
	}
}

// RulesCase is the name of the one case that needs the kit's rules in the block under test.
const RulesCase = "the_hosts_rules_decide"

// CasesWithoutRules are the cases that hold for a block of any rules that allow the kit's calls: every case but RulesCase.
func CasesWithoutRules() []Case {
	var out []Case
	for _, c := range Cases() {
		if c.Name != RulesCase {
			out = append(out, c)
		}
	}
	return out
}

var b64 = base64.RawURLEncoding.Strict()

// refused checks that err is the refusal with code and reason.
func refused(err error, code connect.Code, reason string) error {
	if err == nil {
		return fmt.Errorf("answered, want %s/%s", code, reason)
	}
	r, _ := refusal.Reason(err)
	if connect.CodeOf(err) != code || r != reason {
		return fmt.Errorf("refused %s/%s, want %s/%s", connect.CodeOf(err), r, code, reason)
	}
	return nil
}

func (k *Kit) claims(extra string) []byte {
	return []byte(`{"kit":"` + k.run + `"` + extra + `}`)
}

func (k *Kit) sign(ctx context.Context, p, typ string, claims []byte) (*tokenv1alpha1.SignResponse, error) {
	res, err := k.Client.Sign(ctx, connect.NewRequest(&tokenv1alpha1.SignRequest{Purpose: p, Typ: typ, ClaimsJson: claims}))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

func (k *Kit) keySet(ctx context.Context, req *tokenv1alpha1.KeySetRequest) (*tokenv1alpha1.KeySetResponse, error) {
	res, err := k.Client.KeySet(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

// current is the published key of a purpose.
func (k *Kit) current(ctx context.Context, p string) (*tokenv1alpha1.PublishedKey, error) {
	set, err := k.keySet(ctx, &tokenv1alpha1.KeySetRequest{Purposes: []string{p}})
	if err != nil {
		return nil, err
	}
	if len(set.GetKeys()) != 1 || set.GetKeys()[0].GetPurpose() != p {
		return nil, fmt.Errorf("the key set of %s is not its one key", p)
	}
	return set.GetKeys()[0], nil
}

// verifyJWS verifies a JWS signature with the standard library: raw r||s for ECDSA, PKCS#1 v1.5 for RS256.
func verifyJWS(alg string, spki, input, sig []byte) error {
	pub, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		return errors.New("the public key does not parse")
	}
	switch alg {
	case purpose.ES256, purpose.ES384:
		ec, ok := pub.(*ecdsa.PublicKey)
		if !ok {
			return errors.New("not an EC key")
		}
		n := 32
		d := sha256.Sum256(input)
		h := d[:]
		if alg == purpose.ES384 {
			n = 48
			d := sha512.Sum384(input)
			h = d[:]
		}
		if len(sig) != 2*n {
			return fmt.Errorf("a raw signature of %d octets, want %d", len(sig), 2*n)
		}
		if !ecdsa.Verify(ec, h, new(big.Int).SetBytes(sig[:n]), new(big.Int).SetBytes(sig[n:])) {
			return errors.New("the signature does not verify")
		}
	case purpose.RS256:
		r, ok := pub.(*rsa.PublicKey)
		if !ok {
			return errors.New("not an RSA key")
		}
		d := sha256.Sum256(input)
		if rsa.VerifyPKCS1v15(r, crypto.SHA256, d[:], sig) != nil {
			return errors.New("the signature does not verify")
		}
	default:
		return fmt.Errorf("the algorithm %q", alg)
	}
	return nil
}

// jwkMembers are the members of the JWK of a public key, as RFC 7518 (section 6) spells them.
func jwkMembers(spki []byte) (map[string]string, error) {
	pub, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		return nil, err
	}
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		point, err := k.Bytes()
		if err != nil {
			return nil, err
		}
		n := (len(point) - 1) / 2
		return map[string]string{"kty": "EC", "crv": k.Curve.Params().Name, "x": b64.EncodeToString(point[1 : 1+n]), "y": b64.EncodeToString(point[1+n:])}, nil
	case *rsa.PublicKey:
		return map[string]string{"kty": "RSA", "n": b64.EncodeToString(k.N.Bytes()), "e": b64.EncodeToString(big.NewInt(int64(k.E)).Bytes())}, nil
	}
	return nil, errors.New("not an EC or RSA key")
}

// thumbprint is the RFC 7638 thumbprint, made here: SHA-256 of the required members in lexicographic order, no white space.
func thumbprint(spki []byte) (string, error) {
	m, err := jwkMembers(spki)
	if err != nil {
		return "", err
	}
	var s string
	if m["kty"] == "EC" {
		s = fmt.Sprintf(`{"crv":%q,"kty":"EC","x":%q,"y":%q}`, m["crv"], m["x"], m["y"])
	} else {
		s = fmt.Sprintf(`{"e":%q,"kty":"RSA","n":%q}`, m["e"], m["n"])
	}
	sum := sha256.Sum256([]byte(s))
	return b64.EncodeToString(sum[:]), nil
}

// headerJSON is the header the block writes: exactly alg, kid and typ, in that order, no white space, no HTML escaping.
func headerJSON(alg, kid, typ string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
		Typ string `json:"typ"`
	}{alg, kid, typ}) // three strings
	return strings.TrimSuffix(buf.String(), "\n")
}

func capabilities(ctx context.Context, k *Kit) error {
	res, err := k.Client.Capabilities(ctx, connect.NewRequest(&tokenv1alpha1.CapabilitiesRequest{}))
	if err != nil {
		return err
	}
	served := map[string]*tokenv1alpha1.PurposeInfo{}
	for _, p := range res.Msg.GetPurposes() {
		served[p.GetName()] = p
	}
	kids := map[token.Kid]tokenv1alpha1.Kid{token.KidKeyID: tokenv1alpha1.Kid_KID_KEY_ID, token.KidThumbprint: tokenv1alpha1.Kid_KID_THUMBPRINT}
	for _, want := range Purposes() {
		got := served[want.Name]
		if got == nil {
			return fmt.Errorf("the purpose %s of the kit is not served: start the block with conformance.Purposes", want.Name)
		}
		if got.GetAlgorithm() != want.Algorithm || got.GetKid() != kids[want.Kid] || strings.Join(got.GetTypes(), ",") != strings.Join(want.Types, ",") {
			return fmt.Errorf("the purpose %s is served as another one", want.Name)
		}
	}
	return nil
}

func signCase(ctx context.Context, k *Kit) error {
	// Claims written in ways a re-encoding would change: white space, an exponent, an escape, a nested object. They are signed as given.
	claims := []byte(` {"kit" : "` + k.run + `", "n": 1.0e2, "s": "\u00e9\ud83d\ude00", "o": {"a": [1, 2, {"b": null}]}} `)
	for _, p := range Purposes() {
		for _, typ := range p.Types {
			res, err := k.sign(ctx, p.Name, typ, claims)
			if err != nil {
				return fmt.Errorf("%s %s: %w", p.Name, typ, err)
			}
			parts := strings.Split(res.GetToken(), ".")
			if len(parts) != 3 {
				return fmt.Errorf("%s: not three parts", p.Name)
			}
			h, err1 := b64.DecodeString(parts[0])
			payload, err2 := b64.DecodeString(parts[1])
			sig, err3 := b64.DecodeString(parts[2])
			if err1 != nil || err2 != nil || err3 != nil {
				return fmt.Errorf("%s: a part is not strict base64url", p.Name)
			}
			if want := headerJSON(p.Algorithm, res.GetKid(), typ); string(h) != want {
				return fmt.Errorf("%s: the header is %s, want %s", p.Name, h, want)
			}
			if !bytes.Equal(payload, claims) {
				return fmt.Errorf("%s: the payload is not the claims as given", p.Name)
			}
			if res.GetAlgorithm() != p.Algorithm {
				return fmt.Errorf("%s: the algorithm %q", p.Name, res.GetAlgorithm())
			}
			pub, err := k.current(ctx, p.Name)
			if err != nil {
				return err
			}
			if pub.GetKid() != res.GetKid() || pub.GetKeyId() != res.GetKeyId() {
				return fmt.Errorf("%s: the token's kid or key_id is not the key set's", p.Name)
			}
			if err := verifyJWS(p.Algorithm, pub.GetSpkiDer(), []byte(parts[0]+"."+parts[1]), sig); err != nil {
				return fmt.Errorf("%s: %w", p.Name, err)
			}
			if verifyJWS(p.Algorithm, pub.GetSpkiDer(), []byte(parts[0]+"."+parts[1]+"x"), sig) == nil {
				return fmt.Errorf("%s: the signature verifies over another input", p.Name)
			}
		}
	}
	return nil
}

func kidRule(ctx context.Context, k *Kit) error {
	for _, p := range Purposes() {
		pub, err := k.current(ctx, p.Name)
		if err != nil {
			return err
		}
		switch p.Kid {
		case token.KidKeyID:
			sum := sha256.Sum256(pub.GetSpkiDer())
			if pub.GetKid() != pub.GetKeyId() || !strings.HasSuffix(pub.GetKeyId(), hex.EncodeToString(sum[:])) {
				return fmt.Errorf("%s: the kid is not the key port's key_id of the key", p.Name)
			}
		case token.KidThumbprint:
			want, err := thumbprint(pub.GetSpkiDer())
			if err != nil {
				return err
			}
			if pub.GetKid() != want {
				return fmt.Errorf("%s: the kid is not the RFC 7638 thumbprint of the key", p.Name)
			}
		}
	}
	return nil
}

// hostKey is a key the kit holds, as a host hands it in.
func hostKey(kid string, x5c ...[]byte) (*ecdsa.PrivateKey, *tokenv1alpha1.HostKey, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	spki, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return nil, nil, err
	}
	return priv, &tokenv1alpha1.HostKey{Kid: kid, Algorithm: purpose.ES256, SpkiDer: spki, X5C: x5c}, nil
}

func keySet(ctx context.Context, k *Kit) error {
	chain := [][]byte{[]byte("kit leaf certificate"), []byte("kit issuer")}
	_, host, err := hostKey("kit-host-"+k.run, []byte("kit host certificate"))
	if err != nil {
		return err
	}
	set, err := k.keySet(ctx, &tokenv1alpha1.KeySetRequest{Keys: []*tokenv1alpha1.HostKey{host}, Chains: map[string]*tokenv1alpha1.CertificateChain{PurposeES384: {X5C: chain}}})
	if err != nil {
		return err
	}
	var doc struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(set.GetJwksJson(), &doc); err != nil {
		return fmt.Errorf("the key set is not JSON: %w", err)
	}
	want := []string{PurposeES384, PurposeES256, PurposeRS256, ""}
	if len(doc.Keys) != len(want) || len(set.GetKeys()) != len(want) {
		return fmt.Errorf("%d keys in the set, want %d", len(doc.Keys), len(want))
	}
	for i, pub := range set.GetKeys() {
		if pub.GetPurpose() != want[i] {
			return fmt.Errorf("key %d is of %q, want %q", i, pub.GetPurpose(), want[i])
		}
		entry := doc.Keys[i]
		members, err := jwkMembers(pub.GetSpkiDer())
		if err != nil {
			return err
		}
		for name, v := range members {
			if entry[name] != v {
				return fmt.Errorf("key %d: the member %s is not the key's", i, name)
			}
		}
		if entry["kid"] != pub.GetKid() || entry["alg"] != pub.GetAlgorithm() || entry["use"] != "sig" {
			return fmt.Errorf("key %d: kid, alg or use", i)
		}
		var x5c [][]byte
		switch i {
		case 0:
			x5c = chain
		case 3:
			x5c = host.GetX5C()
		}
		got, _ := entry["x5c"].([]any)
		if len(got) != len(x5c) {
			return fmt.Errorf("key %d: %d certificates, want %d", i, len(got), len(x5c))
		}
		for j, c := range x5c {
			if got[j] != base64.StdEncoding.EncodeToString(c) {
				return fmt.Errorf("key %d: x5c[%d] is not the standard base64 of the certificate", i, j)
			}
		}
	}
	last := set.GetKeys()[3]
	if last.GetKid() != host.GetKid() || !bytes.Equal(last.GetSpkiDer(), host.GetSpkiDer()) || last.GetKeyId() != "" {
		return errors.New("the host's key is not published as it was handed in")
	}
	some, err := k.keySet(ctx, &tokenv1alpha1.KeySetRequest{Purposes: []string{PurposeRS256, PurposeES384}})
	if err != nil {
		return err
	}
	if len(some.GetKeys()) != 2 || some.GetKeys()[0].GetPurpose() != PurposeRS256 || some.GetKeys()[1].GetPurpose() != PurposeES384 {
		return errors.New("the key set of two named purposes is not theirs, in their order")
	}
	// A host's key that is the current key of a purpose, under its kid, is one entry.
	cur := some.GetKeys()[1]
	same := &tokenv1alpha1.HostKey{Kid: cur.GetKid(), Algorithm: cur.GetAlgorithm(), SpkiDer: cur.GetSpkiDer()}
	one, err := k.keySet(ctx, &tokenv1alpha1.KeySetRequest{Purposes: []string{PurposeES384}, Keys: []*tokenv1alpha1.HostKey{same}})
	if err != nil {
		return err
	}
	if len(one.GetKeys()) != 1 {
		return errors.New("the current key handed in again is published twice")
	}
	return nil
}

func (k *Kit) verify(ctx context.Context, req *tokenv1alpha1.VerifyRequest) (*tokenv1alpha1.VerifyResponse, error) {
	res, err := k.Client.Verify(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

func verifyRoundTrip(ctx context.Context, k *Kit) error {
	for _, p := range Purposes() {
		claims := k.claims(`,"p":"` + p.Name + `"`)
		res, err := k.sign(ctx, p.Name, p.Types[0], claims)
		if err != nil {
			return err
		}
		got, err := k.verify(ctx, &tokenv1alpha1.VerifyRequest{Token: res.GetToken()})
		if err != nil {
			return fmt.Errorf("%s: %w", p.Name, err)
		}
		if got.GetPurpose() != p.Name || got.GetKid() != res.GetKid() || got.GetTyp() != p.Types[0] || got.GetAlgorithm() != p.Algorithm || !bytes.Equal(got.GetClaimsJson(), claims) {
			return fmt.Errorf("%s: the verification names another key, type or claims", p.Name)
		}
		if _, err := k.verify(ctx, &tokenv1alpha1.VerifyRequest{Token: res.GetToken(), Purposes: []string{p.Name}, Types: []string{p.Types[0]}}); err != nil {
			return fmt.Errorf("%s, named: %w", p.Name, err)
		}
		other := PurposeES256
		if p.Name == other {
			other = PurposeES384
		}
		if err := refused(second(k.verify(ctx, &tokenv1alpha1.VerifyRequest{Token: res.GetToken(), Purposes: []string{other}})), connect.CodePermissionDenied, token.ReasonToken); err != nil {
			return fmt.Errorf("%s against another purpose's key: %w", p.Name, err)
		}
		if err := refused(second(k.verify(ctx, &tokenv1alpha1.VerifyRequest{Token: res.GetToken(), Types: []string{"other+jwt"}})), connect.CodePermissionDenied, token.ReasonToken); err != nil {
			return fmt.Errorf("%s of a type not accepted: %w", p.Name, err)
		}
	}
	return nil
}

func second[T any](_ T, err error) error { return err }

func verifyTampered(ctx context.Context, k *Kit) error {
	res, err := k.sign(ctx, PurposeES384, TypeJWT, k.claims(`,"sub":"a"`))
	if err != nil {
		return err
	}
	tok := res.GetToken()
	parts := strings.Split(tok, ".")
	h, _ := b64.DecodeString(parts[0])
	otherTyp := b64.EncodeToString(bytes.Replace(h, []byte(`"typ":"JWT"`), []byte(`"typ":"at+jwt"`), 1))
	otherClaims := b64.EncodeToString(k.claims(`,"sub":"b"`))
	sig, _ := b64.DecodeString(parts[2])
	sig[len(sig)/2] ^= 1
	changed := map[string]string{
		"the typ of the header":     otherTyp + "." + parts[1] + "." + parts[2],
		"the claims":                parts[0] + "." + otherClaims + "." + parts[2],
		"a bit of the signature":    parts[0] + "." + parts[1] + "." + b64.EncodeToString(sig),
		"no signature":              parts[0] + "." + parts[1] + ".",
		"two parts":                 parts[0] + "." + parts[1],
		"four parts":                tok + "." + parts[2],
		"padding":                   tok + "==",
		"white space":               parts[0] + " ." + parts[1] + "." + parts[2],
		"a header of another field": b64.EncodeToString(bytes.Replace(h, []byte(`}`), []byte(`,"crit":["x"]}`), 1)) + "." + parts[1] + "." + parts[2],
		"nothing":                   "",
	}
	for what, t := range changed {
		if err := refused(second(k.verify(ctx, &tokenv1alpha1.VerifyRequest{Token: t})), connect.CodePermissionDenied, token.ReasonToken); err != nil {
			return fmt.Errorf("%s changed: %w", what, err)
		}
	}
	return nil
}

func verifyHostKey(ctx context.Context, k *Kit) error {
	priv, host, err := hostKey("kit-host-" + k.run)
	if err != nil {
		return err
	}
	claims := k.claims(`,"by":"host"`)
	input := b64.EncodeToString([]byte(headerJSON(purpose.ES256, host.GetKid(), TypeKit))) + "." + b64.EncodeToString(claims)
	d := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, priv, d[:])
	if err != nil {
		return err
	}
	tok := input + "." + b64.EncodeToString(append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...))
	got, err := k.verify(ctx, &tokenv1alpha1.VerifyRequest{Token: tok, Keys: []*tokenv1alpha1.HostKey{host}})
	if err != nil {
		return fmt.Errorf("a token of the host's key: %w", err)
	}
	if got.GetPurpose() != "" || got.GetKid() != host.GetKid() || !bytes.Equal(got.GetClaimsJson(), claims) {
		return errors.New("the verification by a host's key names a purpose, another kid or other claims")
	}
	if err := refused(second(k.verify(ctx, &tokenv1alpha1.VerifyRequest{Token: tok})), connect.CodePermissionDenied, token.ReasonToken); err != nil {
		return fmt.Errorf("without the host's key: %w", err)
	}
	if err := refused(second(k.verify(ctx, &tokenv1alpha1.VerifyRequest{Token: tok, Keys: []*tokenv1alpha1.HostKey{host}, Types: []string{TypeJWT}})), connect.CodePermissionDenied, token.ReasonToken); err != nil {
		return fmt.Errorf("a host's key and a type not accepted: %w", err)
	}
	return nil
}

func strictClaims(ctx context.Context, k *Kit) error {
	for what, c := range map[string]string{
		"an array":                   `[]`,
		"a string":                   `"x"`,
		"nothing":                    ``,
		"a member twice":             `{"a":1,"a":2}`,
		"a member twice, nested":     `{"a":{"b":1,"b":2}}`,
		"a member twice in an array": `{"a":[{"b":1,"b":2}]}`,
		"text after the object":      `{} x`,
		"two objects":                `{}{}`,
		"a lone high surrogate":      `{"a":"\ud800"}`,
		"a lone low surrogate":       `{"a":"\udc00"}`,
		"not UTF-8":                  "{\"a\":\"\xff\"}",
		"cut short":                  `{"a":`,
		"too long":                   `{"a":"` + strings.Repeat("x", token.MaxClaims) + `"}`,
	} {
		if err := refused(second(k.sign(ctx, PurposeES384, TypeJWT, []byte(c))), connect.CodeInvalidArgument, token.ReasonClaims); err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
	}
	for _, c := range []string{`{}`, " {\"a\":\"\\ud83d\\ude00\"}\n", `{"a":{"a":1},"b":{"a":2}}`} {
		if _, err := k.sign(ctx, PurposeES384, TypeJWT, []byte(c)); err != nil {
			return fmt.Errorf("%q: %w", c, err)
		}
	}
	return nil
}

func rules(ctx context.Context, k *Kit) error {
	return refused(second(k.sign(ctx, PurposeES256, TypeKit, k.claims(`,"`+RefusedClaim+`":true`))), connect.CodePermissionDenied, token.ReasonRule)
}

func refusals(ctx context.Context, k *Kit) error {
	_, host, err := hostKey("kit-host-" + k.run)
	if err != nil {
		return err
	}
	cur, err := k.current(ctx, PurposeES384)
	if err != nil {
		return err
	}
	bad := func(h *tokenv1alpha1.HostKey) []*tokenv1alpha1.HostKey { return []*tokenv1alpha1.HostKey{h} }
	wrongAlg := &tokenv1alpha1.HostKey{Kid: host.GetKid(), Algorithm: purpose.ES384, SpkiDer: host.GetSpkiDer()}
	noKid := &tokenv1alpha1.HostKey{Algorithm: purpose.ES256, SpkiDer: host.GetSpkiDer()}
	notKey := &tokenv1alpha1.HostKey{Kid: host.GetKid(), Algorithm: purpose.ES256, SpkiDer: []byte("not a key")}
	takenKid := &tokenv1alpha1.HostKey{Kid: cur.GetKid(), Algorithm: purpose.ES256, SpkiDer: host.GetSpkiDer()}
	cases := []struct {
		what   string
		err    error
		code   connect.Code
		reason string
	}{
		{"sign by no purpose", second(k.sign(ctx, "kit-no-such-purpose", TypeJWT, k.claims(""))), connect.CodeInvalidArgument, token.ReasonPurpose},
		{"sign of a type of another purpose", second(k.sign(ctx, PurposeES384, TypeKit, k.claims(""))), connect.CodeInvalidArgument, token.ReasonTyp},
		{"sign of no type", second(k.sign(ctx, PurposeES384, "", k.claims(""))), connect.CodeInvalidArgument, token.ReasonTyp},
		{"the key set of no purpose", second(k.keySet(ctx, &tokenv1alpha1.KeySetRequest{Purposes: []string{"kit-no-such-purpose"}})), connect.CodeInvalidArgument, token.ReasonPurpose},
		{"the key set of a purpose twice", second(k.keySet(ctx, &tokenv1alpha1.KeySetRequest{Purposes: []string{PurposeES384, PurposeES384}})), connect.CodeInvalidArgument, token.ReasonPurpose},
		{"a chain of no purpose", second(k.keySet(ctx, &tokenv1alpha1.KeySetRequest{Chains: map[string]*tokenv1alpha1.CertificateChain{"kit-no-such-purpose": {}}})), connect.CodeInvalidArgument, token.ReasonKeys},
		{"a host's key of another algorithm", second(k.keySet(ctx, &tokenv1alpha1.KeySetRequest{Keys: bad(wrongAlg)})), connect.CodeInvalidArgument, token.ReasonKeys},
		{"a host's key with no kid", second(k.keySet(ctx, &tokenv1alpha1.KeySetRequest{Keys: bad(noKid)})), connect.CodeInvalidArgument, token.ReasonKeys},
		{"a host's key that is no key", second(k.keySet(ctx, &tokenv1alpha1.KeySetRequest{Keys: bad(notKey)})), connect.CodeInvalidArgument, token.ReasonKeys},
		{"a host's key under a current kid", second(k.keySet(ctx, &tokenv1alpha1.KeySetRequest{Keys: bad(takenKid)})), connect.CodeInvalidArgument, token.ReasonKeys},
		{"two host's keys of one kid", second(k.keySet(ctx, &tokenv1alpha1.KeySetRequest{Keys: []*tokenv1alpha1.HostKey{host, host}})), connect.CodeInvalidArgument, token.ReasonKeys},
		{"a verification with a bad host's key", second(k.verify(ctx, &tokenv1alpha1.VerifyRequest{Token: "a.b.c", Keys: bad(notKey)})), connect.CodeInvalidArgument, token.ReasonKeys},
		{"a verification of no purpose", second(k.verify(ctx, &tokenv1alpha1.VerifyRequest{Token: "a.b.c", Purposes: []string{"kit-no-such-purpose"}})), connect.CodeInvalidArgument, token.ReasonPurpose},
		{"a verification of a bad type", second(k.verify(ctx, &tokenv1alpha1.VerifyRequest{Token: "a.b.c", Types: []string{"no type"}})), connect.CodeInvalidArgument, token.ReasonTyp},
		{"a verification of a token too long", second(k.verify(ctx, &tokenv1alpha1.VerifyRequest{Token: strings.Repeat("a", token.MaxToken+1)})), connect.CodeInvalidArgument, token.ReasonSize},
	}
	for _, c := range cases {
		if err := refused(c.err, c.code, c.reason); err != nil {
			return fmt.Errorf("%s: %w", c.what, err)
		}
	}
	return nil
}
