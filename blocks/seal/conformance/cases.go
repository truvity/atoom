package conformance

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/blocks/seal"
	sealv1alpha1 "github.com/truvity/atoom/blocks/seal/gen/atoom/seal/v1alpha1"
	"github.com/truvity/atoom/common/refusal"
	"github.com/truvity/atoom/key/purpose"
	"github.com/truvity/atoom/key/signature"
)

// Cases are the kit's cases, in the order they run (docs/seal-block.md, "The kit").
func Cases() []Case {
	return []Case{
		{"capabilities", capabilities},
		{"seal_open_round_trip", roundTrip},
		{"bound_to_its_address", boundToAddress},
		{"a_changed_envelope_does_not_open", tampered},
		{"a_fresh_nonce_each_seal", freshNonce},
		{"open_is_batched_all_or_none", batched},
		{"each_scope_has_its_own_key", scopes},
		{"mac_is_keyed_and_bound_to_its_label", macCase},
		{"mac_by_the_callers_generation", macGeneration},
		{"rotate_seals_under_a_new_generation_and_the_old_opens", rotate},
		{"erase_makes_a_scope_unreadable", erase},
		{"the_hosts_rules_decide", rules},
		{"sign_verifies_under_the_public_key", sign},
		{"what_is_not_a_call_is_refused", refusals},
		{"seals_rotate_before_the_limit", sealLimit},
		{"a_compromised_generation_seals_no_more", compromised},
		{"rewrap", rewrapCase},
	}
}

func (k *Kit) seal(ctx context.Context, p, scope string, address []string, pt []byte) ([]byte, error) {
	res, err := k.Client.Seal(ctx, connect.NewRequest(&sealv1alpha1.SealRequest{Purpose: p, Scope: scope, Address: address, Plaintext: pt}))
	if err != nil {
		return nil, err
	}
	return res.Msg.GetEnvelope(), nil
}

func (k *Kit) open(ctx context.Context, items ...*sealv1alpha1.OpenItem) ([][]byte, error) {
	res, err := k.Client.Open(ctx, connect.NewRequest(&sealv1alpha1.OpenRequest{Items: items}))
	if err != nil {
		return nil, err
	}
	return res.Msg.GetPlaintexts(), nil
}

func item(p, scope string, address []string, env []byte) *sealv1alpha1.OpenItem {
	return &sealv1alpha1.OpenItem{Purpose: p, Scope: scope, Address: address, Envelope: env}
}

func (k *Kit) mac(ctx context.Context, p, scope, label string, input []byte, g uint32) ([]byte, error) {
	res, err := k.Client.Mac(ctx, connect.NewRequest(&sealv1alpha1.MacRequest{Purpose: p, Scope: scope, Label: label, Input: input, Generation: g}))
	if err != nil {
		return nil, err
	}
	return res.Msg.GetMac(), nil
}

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

// generationOf reads the generation of an envelope's header: bytes 2 to 5, big-endian.
func generationOf(env []byte) uint32 {
	if len(env) < 6 {
		return 0
	}
	return binary.BigEndian.Uint32(env[2:6])
}

var labelPattern = regexp.MustCompile(`^[a-z2-7]{52}$`)

func capabilities(ctx context.Context, k *Kit) error {
	res, err := k.Client.Capabilities(ctx, connect.NewRequest(&sealv1alpha1.CapabilitiesRequest{}))
	if err != nil {
		return err
	}
	if !labelPattern.MatchString(res.Msg.GetRecipient()) {
		return errors.New("the recipient is not the lower-case base32 of a thumbprint")
	}
	served := map[string]*sealv1alpha1.PurposeInfo{}
	for _, p := range res.Msg.GetPurposes() {
		served[p.GetName()] = p
	}
	uses := map[seal.Use]sealv1alpha1.Use{seal.UseSeal: sealv1alpha1.Use_USE_SEAL, seal.UseMac: sealv1alpha1.Use_USE_MAC, seal.UseSign: sealv1alpha1.Use_USE_SIGN}
	for _, want := range Purposes() {
		got := served[want.Name]
		if got == nil {
			return fmt.Errorf("the purpose %s of the kit is not served: start the block with conformance.Purposes", want.Name)
		}
		if got.GetUse() != uses[want.Use] || got.GetCode() != uint32(want.Code) || got.GetAlgorithm() != want.Algorithm || got.GetCallerGeneration() != want.CallerGeneration {
			return fmt.Errorf("the purpose %s is served as another one", want.Name)
		}
	}
	return nil
}

func roundTrip(ctx context.Context, k *Kit) error {
	scope := k.Scope("round-trip")
	every := make([]byte, 256)
	for i := range every {
		every[i] = byte(i)
	}
	for _, pt := range [][]byte{nil, {0}, every, bytes.Repeat([]byte{0xa5}, 64<<10)} {
		for _, addr := range [][]string{{"one"}, {"b", "t", "kind", "id"}, {""}} {
			env, err := k.seal(ctx, PurposeSeal, scope, addr, pt)
			if err != nil {
				return fmt.Errorf("seal: %w", err)
			}
			if generationOf(env) == 0 || env[1] != codeSeal || env[0] != 1 {
				return errors.New("the envelope's header is not version 1, the purpose's code and a generation")
			}
			got, err := k.open(ctx, item(PurposeSeal, scope, addr, env))
			if err != nil {
				return fmt.Errorf("open: %w", err)
			}
			if len(got) != 1 || !bytes.Equal(got[0], pt) {
				return fmt.Errorf("a value of %d bytes did not come back as it was", len(pt))
			}
		}
	}
	// The installation's own scope is a scope.
	env, err := k.seal(ctx, PurposeSeal, "", []string{k.Scope("own")}, []byte("v"))
	if err != nil {
		return fmt.Errorf("seal in the installation's scope: %w", err)
	}
	if _, err := k.open(ctx, item(PurposeSeal, "", []string{k.Scope("own")}, env)); err != nil {
		return fmt.Errorf("open in the installation's scope: %w", err)
	}
	return nil
}

func boundToAddress(ctx context.Context, k *Kit) error {
	scope := k.Scope("bound")
	addr := []string{"b", "acme", "person", "id-1"}
	env, err := k.seal(ctx, PurposeSeal, scope, addr, []byte("secret"))
	if err != nil {
		return err
	}
	other := k.Scope("bound-other")
	if _, err := k.seal(ctx, PurposeSeal, other, addr, []byte("x")); err != nil {
		return err
	}
	for name, it := range map[string]*sealv1alpha1.OpenItem{
		"another id":         item(PurposeSeal, scope, []string{"b", "acme", "person", "id-2"}, env),
		"another tenant":     item(PurposeSeal, scope, []string{"b", "globex", "person", "id-1"}, env),
		"a part fewer":       item(PurposeSeal, scope, addr[:3], env),
		"a part more":        item(PurposeSeal, scope, append(append([]string{}, addr...), ""), env),
		"parts run together": item(PurposeSeal, scope, []string{"bacme", "", "person", "id-1"}, env),
		"another scope":      item(PurposeSeal, other, addr, env),
		"another purpose":    item(PurposeOther, scope, addr, env),
	} {
		if _, err := k.open(ctx, it); refused(err, connect.CodePermissionDenied, seal.ReasonOpen) != nil {
			return fmt.Errorf("opened at %s, or refused otherwise: %w", name, refused(err, connect.CodePermissionDenied, seal.ReasonOpen))
		}
	}
	return nil
}

func tampered(ctx context.Context, k *Kit) error {
	scope := k.Scope("tampered")
	addr := []string{"a"}
	env, err := k.seal(ctx, PurposeSeal, scope, addr, []byte("hello, world"))
	if err != nil {
		return err
	}
	for i := range env {
		bad := bytes.Clone(env)
		bad[i] ^= 0x01
		if _, err := k.open(ctx, item(PurposeSeal, scope, addr, bad)); refused(err, connect.CodePermissionDenied, seal.ReasonOpen) != nil {
			return fmt.Errorf("byte %d changed: %w", i, refused(err, connect.CodePermissionDenied, seal.ReasonOpen))
		}
	}
	for _, n := range []int{0, 5, 33, len(env) - 1} {
		if _, err := k.open(ctx, item(PurposeSeal, scope, addr, env[:n])); refused(err, connect.CodePermissionDenied, seal.ReasonOpen) != nil {
			return fmt.Errorf("cut to %d bytes: %w", n, refused(err, connect.CodePermissionDenied, seal.ReasonOpen))
		}
	}
	return nil
}

func freshNonce(ctx context.Context, k *Kit) error {
	scope := k.Scope("nonce")
	seen := map[string]bool{}
	for range 32 {
		env, err := k.seal(ctx, PurposeSeal, scope, []string{"a"}, []byte("same"))
		if err != nil {
			return err
		}
		n := string(env[6:18])
		if seen[n] {
			return errors.New("two seals have the same nonce")
		}
		seen[n] = true
	}
	return nil
}

func batched(ctx context.Context, k *Kit) error {
	scope := k.Scope("batch")
	var items []*sealv1alpha1.OpenItem
	for i := range seal.MaxOpenItems {
		addr := []string{fmt.Sprint(i)}
		env, err := k.seal(ctx, PurposeSeal, scope, addr, []byte(fmt.Sprint("v", i)))
		if err != nil {
			return err
		}
		items = append(items, item(PurposeSeal, scope, addr, env))
	}
	got, err := k.open(ctx, items...)
	if err != nil {
		return fmt.Errorf("a batch of %d: %w", len(items), err)
	}
	for i, pt := range got {
		if string(pt) != fmt.Sprint("v", i) {
			return errors.New("the plaintexts are not in the order of the items")
		}
	}
	bad := append([]*sealv1alpha1.OpenItem{}, items[:3]...)
	bad = append(bad, item(PurposeSeal, scope, []string{"nope"}, items[0].GetEnvelope()))
	if _, err := k.open(ctx, bad...); refused(err, connect.CodePermissionDenied, seal.ReasonOpen) != nil {
		return fmt.Errorf("a batch with one bad item: %w", refused(err, connect.CodePermissionDenied, seal.ReasonOpen))
	}
	if _, err := k.open(ctx); refused(err, connect.CodeInvalidArgument, seal.ReasonItems) != nil {
		return fmt.Errorf("an empty batch: %w", refused(err, connect.CodeInvalidArgument, seal.ReasonItems))
	}
	if _, err := k.open(ctx, append(items, items[0])...); refused(err, connect.CodeInvalidArgument, seal.ReasonItems) != nil {
		return fmt.Errorf("a batch of %d: %w", len(items)+1, refused(err, connect.CodeInvalidArgument, seal.ReasonItems))
	}
	return nil
}

func scopes(ctx context.Context, k *Kit) error {
	a, b := k.Scope("scope-a"), k.Scope("scope-b")
	addr := []string{"same"}
	ea, err := k.seal(ctx, PurposeSeal, a, addr, []byte("a"))
	if err != nil {
		return err
	}
	eb, err := k.seal(ctx, PurposeSeal, b, addr, []byte("b"))
	if err != nil {
		return err
	}
	if _, err := k.open(ctx, item(PurposeSeal, b, addr, ea)); refused(err, connect.CodePermissionDenied, seal.ReasonOpen) != nil {
		return fmt.Errorf("a's envelope under b: %w", refused(err, connect.CodePermissionDenied, seal.ReasonOpen))
	}
	got, err := k.open(ctx, item(PurposeSeal, a, addr, ea), item(PurposeSeal, b, addr, eb))
	if err != nil {
		return fmt.Errorf("two scopes in one batch: %w", err)
	}
	if string(got[0]) != "a" || string(got[1]) != "b" {
		return errors.New("two scopes in one batch came back as other values")
	}
	ma, err := k.mac(ctx, PurposeMac, a, "l", []byte("x"), 0)
	if err != nil {
		return err
	}
	mb, err := k.mac(ctx, PurposeMac, b, "l", []byte("x"), 0)
	if err != nil {
		return err
	}
	if bytes.Equal(ma, mb) {
		return errors.New("two scopes give the same keyed hash")
	}
	return nil
}

func macCase(ctx context.Context, k *Kit) error {
	scope := k.Scope("mac")
	m1, err := k.mac(ctx, PurposeMac, scope, "use-a", []byte("bc"), 0)
	if err != nil {
		return err
	}
	if len(m1) != 48 {
		return fmt.Errorf("a keyed hash of %d bytes, want 48 (HMAC-SHA384)", len(m1))
	}
	m2, err := k.mac(ctx, PurposeMac, scope, "use-a", []byte("bc"), 0)
	if err != nil {
		return err
	}
	if !bytes.Equal(m1, m2) {
		return errors.New("the same input gave another hash")
	}
	for name, got := range map[string]func() ([]byte, error){
		"another label":         func() ([]byte, error) { return k.mac(ctx, PurposeMac, scope, "use-b", []byte("bc"), 0) },
		"a label moved":         func() ([]byte, error) { return k.mac(ctx, PurposeMac, scope, "use-ab", []byte("c"), 0) },
		"another input":         func() ([]byte, error) { return k.mac(ctx, PurposeMac, scope, "use-a", []byte("bd"), 0) },
		"another purpose's key": func() ([]byte, error) { return k.mac(ctx, PurposeEpoch, scope, "use-a", []byte("bc"), 1) },
	} {
		m, err := got()
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if bytes.Equal(m, m1) {
			return fmt.Errorf("%s gave the same hash", name)
		}
	}
	for _, label := range []string{"", strings.Repeat("l", seal.MaxLabel+1)} {
		if _, err := k.mac(ctx, PurposeMac, scope, label, nil, 0); refused(err, connect.CodeInvalidArgument, seal.ReasonLabel) != nil {
			return fmt.Errorf("a label of %d bytes: %w", len(label), refused(err, connect.CodeInvalidArgument, seal.ReasonLabel))
		}
	}
	if _, err := k.mac(ctx, PurposeMac, scope, "l", nil, 7); refused(err, connect.CodeInvalidArgument, seal.ReasonGeneration) != nil {
		return fmt.Errorf("a generation for a purpose that takes none: %w", refused(err, connect.CodeInvalidArgument, seal.ReasonGeneration))
	}
	return nil
}

func macGeneration(ctx context.Context, k *Kit) error {
	scope := k.Scope("epoch")
	a, err := k.mac(ctx, PurposeEpoch, scope, "pseudonym", []byte("subject"), 202611)
	if err != nil {
		return err
	}
	again, err := k.mac(ctx, PurposeEpoch, scope, "pseudonym", []byte("subject"), 202611)
	if err != nil {
		return err
	}
	if !bytes.Equal(a, again) {
		return errors.New("the same generation gave another hash")
	}
	b, err := k.mac(ctx, PurposeEpoch, scope, "pseudonym", []byte("subject"), 202612)
	if err != nil {
		return err
	}
	if bytes.Equal(a, b) {
		return errors.New("two generations gave the same hash")
	}
	if _, err := k.mac(ctx, PurposeEpoch, scope, "pseudonym", nil, 0); refused(err, connect.CodeInvalidArgument, seal.ReasonGeneration) != nil {
		return fmt.Errorf("no generation for a purpose that needs one: %w", refused(err, connect.CodeInvalidArgument, seal.ReasonGeneration))
	}
	return nil
}

func rotate(ctx context.Context, k *Kit) error {
	scope := k.Scope("rotate")
	addr := []string{"r"}
	e1, err := k.seal(ctx, PurposeSeal, scope, addr, []byte("before"))
	if err != nil {
		return err
	}
	m1, err := k.mac(ctx, PurposeMac, scope, "l", []byte("x"), 0)
	if err != nil {
		return err
	}
	res, err := k.Client.Rotate(ctx, connect.NewRequest(&sealv1alpha1.RotateRequest{Purpose: PurposeSeal, Scope: scope}))
	if err != nil {
		return fmt.Errorf("rotate: %w", err)
	}
	if res.Msg.GetGeneration() != generationOf(e1)+1 {
		return fmt.Errorf("rotate gave generation %d after %d", res.Msg.GetGeneration(), generationOf(e1))
	}
	e2, err := k.seal(ctx, PurposeSeal, scope, addr, []byte("after"))
	if err != nil {
		return err
	}
	if generationOf(e2) != res.Msg.GetGeneration() {
		return fmt.Errorf("a seal after the rotation is at generation %d, want %d", generationOf(e2), res.Msg.GetGeneration())
	}
	got, err := k.open(ctx, item(PurposeSeal, scope, addr, e1), item(PurposeSeal, scope, addr, e2))
	if err != nil {
		return fmt.Errorf("the two generations do not both open: %w", err)
	}
	if string(got[0]) != "before" || string(got[1]) != "after" {
		return errors.New("the two generations open to other values")
	}
	if _, err := k.Client.Rotate(ctx, connect.NewRequest(&sealv1alpha1.RotateRequest{Purpose: PurposeMac, Scope: scope})); err != nil {
		return fmt.Errorf("rotate a mac purpose: %w", err)
	}
	m2, err := k.mac(ctx, PurposeMac, scope, "l", []byte("x"), 0)
	if err != nil {
		return err
	}
	if bytes.Equal(m1, m2) {
		return errors.New("a mac purpose hashes the same after its rotation")
	}
	_, err = k.Client.Rotate(ctx, connect.NewRequest(&sealv1alpha1.RotateRequest{Purpose: PurposeEpoch, Scope: scope}))
	if refused(err, connect.CodeInvalidArgument, seal.ReasonPurpose) != nil {
		return fmt.Errorf("rotate a purpose whose caller names the generation: %w", refused(err, connect.CodeInvalidArgument, seal.ReasonPurpose))
	}
	return nil
}

func erase(ctx context.Context, k *Kit) error {
	scope := k.Scope("erase")
	keep := k.Scope("erase-kept")
	addr := []string{"e"}
	env, err := k.seal(ctx, PurposeSeal, scope, addr, []byte("gone"))
	if err != nil {
		return err
	}
	kept, err := k.seal(ctx, PurposeSeal, keep, addr, []byte("kept"))
	if err != nil {
		return err
	}
	if _, err := k.mac(ctx, PurposeMac, scope, "l", nil, 0); err != nil {
		return err
	}
	res, err := k.Client.Erase(ctx, connect.NewRequest(&sealv1alpha1.EraseRequest{Scope: scope}))
	if err != nil {
		return fmt.Errorf("erase: %w", err)
	}
	if res.Msg.GetWraps() < 2 {
		return fmt.Errorf("erase deleted %d wraps, want at least the seal key's and the mac key's", res.Msg.GetWraps())
	}
	if _, err := k.open(ctx, item(PurposeSeal, scope, addr, env)); refused(err, connect.CodePermissionDenied, seal.ReasonOpen) != nil {
		return fmt.Errorf("an envelope of the erased scope: %w", refused(err, connect.CodePermissionDenied, seal.ReasonOpen))
	}
	if _, err := k.open(ctx, item(PurposeSeal, keep, addr, kept)); err != nil {
		return fmt.Errorf("another scope after the erase: %w", err)
	}
	again, err := k.seal(ctx, PurposeSeal, scope, addr, []byte("new"))
	if err != nil {
		return fmt.Errorf("a seal after the erase: %w", err)
	}
	if _, err := k.open(ctx, item(PurposeSeal, scope, addr, env)); refused(err, connect.CodePermissionDenied, seal.ReasonOpen) != nil {
		return fmt.Errorf("the old envelope under the new key: %w", refused(err, connect.CodePermissionDenied, seal.ReasonOpen))
	}
	got, err := k.open(ctx, item(PurposeSeal, scope, addr, again))
	if err != nil {
		return fmt.Errorf("the new envelope: %w", err)
	}
	if string(got[0]) != "new" {
		return errors.New("the new envelope opens to another value")
	}
	_, err = k.Client.Erase(ctx, connect.NewRequest(&sealv1alpha1.EraseRequest{Scope: scope, Purposes: []string{PurposeES384}}))
	if refused(err, connect.CodeInvalidArgument, seal.ReasonPurpose) != nil {
		return fmt.Errorf("erase a signing purpose: %w", refused(err, connect.CodeInvalidArgument, seal.ReasonPurpose))
	}
	return nil
}

func rules(ctx context.Context, k *Kit) error {
	addr := []string{"x"}
	if _, err := k.seal(ctx, PurposeSeal, RefusedPrefix+k.Scope("r"), addr, nil); refused(err, connect.CodePermissionDenied, seal.ReasonRule) != nil {
		return fmt.Errorf("a seal the rules refuse: %w", refused(err, connect.CodePermissionDenied, seal.ReasonRule))
	}
	if _, err := k.mac(ctx, PurposeMac, RefusedPrefix+k.Scope("r"), "l", nil, 0); refused(err, connect.CodePermissionDenied, seal.ReasonRule) != nil {
		return fmt.Errorf("a mac the rules refuse: %w", refused(err, connect.CodePermissionDenied, seal.ReasonRule))
	}
	nc := NoCreatePrefix + k.Scope("n")
	if _, err := k.seal(ctx, PurposeSeal, nc, addr, nil); refused(err, connect.CodePermissionDenied, seal.ReasonCreate) != nil {
		return fmt.Errorf("a seal that would make a key the rules do not allow: %w", refused(err, connect.CodePermissionDenied, seal.ReasonCreate))
	}
	if _, err := k.mac(ctx, PurposeMac, nc, "l", nil, 0); refused(err, connect.CodePermissionDenied, seal.ReasonCreate) != nil {
		return fmt.Errorf("a mac that would make a key the rules do not allow: %w", refused(err, connect.CodePermissionDenied, seal.ReasonCreate))
	}
	// An envelope moved into a scope with no key does not open, and opening makes no key.
	env, err := k.seal(ctx, PurposeSeal, k.Scope("rules-src"), addr, []byte("v"))
	if err != nil {
		return err
	}
	if _, err := k.open(ctx, item(PurposeSeal, nc, addr, env)); refused(err, connect.CodePermissionDenied, seal.ReasonOpen) != nil {
		return fmt.Errorf("an open in a scope with no key: %w", refused(err, connect.CodePermissionDenied, seal.ReasonOpen))
	}
	if _, err := k.open(ctx, item(PurposeSeal, RefusedPrefix+k.Scope("r"), addr, env)); refused(err, connect.CodePermissionDenied, seal.ReasonRule) != nil {
		return fmt.Errorf("an open the rules refuse: %w", refused(err, connect.CodePermissionDenied, seal.ReasonRule))
	}
	return nil
}

func sign(ctx context.Context, k *Kit) error {
	content := []byte("the content the host checked")
	for _, p := range []struct{ name, alg string }{{PurposeES384, purpose.ES384}, {PurposeES256, purpose.ES256}, {PurposeRS256, purpose.RS256}} {
		res, err := k.Client.Sign(ctx, connect.NewRequest(&sealv1alpha1.SignRequest{Purpose: p.name, Content: content}))
		if err != nil {
			return fmt.Errorf("%s: sign: %w", p.alg, err)
		}
		pk, err := k.Client.PublicKey(ctx, connect.NewRequest(&sealv1alpha1.PublicKeyRequest{Purpose: p.name}))
		if err != nil {
			return fmt.Errorf("%s: public key: %w", p.alg, err)
		}
		if res.Msg.GetAlgorithm() != p.alg || pk.Msg.GetAlgorithm() != p.alg {
			return fmt.Errorf("%s: the algorithm is named %q and %q", p.alg, res.Msg.GetAlgorithm(), pk.Msg.GetAlgorithm())
		}
		if res.Msg.GetKeyId() == "" || res.Msg.GetKeyId() != pk.Msg.GetKeyId() {
			return fmt.Errorf("%s: the key_id of the signature is not the public key's", p.alg)
		}
		pub, err := x509.ParsePKIXPublicKey(pk.Msg.GetSpkiDer())
		if err != nil {
			return fmt.Errorf("%s: the public key does not parse", p.alg)
		}
		if err := verify(p.alg, pub, content, res.Msg.GetSignatureDer(), res.Msg.GetSignatureRaw()); err != nil {
			return fmt.Errorf("%s: %w", p.alg, err)
		}
		if err := verify(p.alg, pub, []byte("other content"), res.Msg.GetSignatureDer(), res.Msg.GetSignatureRaw()); err == nil {
			return fmt.Errorf("%s: the signature verifies over other content", p.alg)
		}
	}
	return nil
}

// verify checks a signature of the block in both its forms: DER (and the octet string of RS256), and the raw r||s of a JWS.
func verify(alg string, pub any, content, der, raw []byte) error {
	switch alg {
	case purpose.RS256:
		k, ok := pub.(*rsa.PublicKey)
		if !ok {
			return errors.New("the public key is not RSA")
		}
		d := sha256.Sum256(content)
		if rsa.VerifyPKCS1v15(k, crypto.SHA256, d[:], der) != nil || !bytes.Equal(der, raw) {
			return errors.New("the signature does not verify")
		}
		return nil
	}
	k, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return errors.New("the public key is not EC")
	}
	var d []byte
	if alg == purpose.ES384 {
		s := sha512.Sum384(content)
		d = s[:]
	} else {
		s := sha256.Sum256(content)
		d = s[:]
	}
	if !ecdsa.VerifyASN1(k, d, der) {
		return errors.New("the DER signature does not verify")
	}
	fromRaw, err := signature.RawToDER(alg, raw)
	if err != nil || !ecdsa.VerifyASN1(k, d, fromRaw) || len(raw) != 2*signature.Size(alg) {
		return errors.New("the raw r||s does not verify")
	}
	return nil
}

func refusals(ctx context.Context, k *Kit) error {
	scope := k.Scope("refusals")
	checks := []struct {
		name   string
		err    error
		reason string
	}{}
	add := func(name string, reason string, err error) {
		checks = append(checks, struct {
			name   string
			err    error
			reason string
		}{name, err, reason})
	}
	_, err := k.seal(ctx, "kit-undeclared", scope, []string{"a"}, nil)
	add("seal of an undeclared purpose", seal.ReasonPurpose, err)
	_, err = k.seal(ctx, PurposeMac, scope, []string{"a"}, nil)
	add("seal of a mac purpose", seal.ReasonPurpose, err)
	_, err = k.mac(ctx, PurposeSeal, scope, "l", nil, 0)
	add("mac of a seal purpose", seal.ReasonPurpose, err)
	_, err = k.open(ctx, item(PurposeES384, scope, []string{"a"}, make([]byte, 40)))
	add("open of a signing purpose", seal.ReasonPurpose, err)
	_, err = k.Client.Sign(ctx, connect.NewRequest(&sealv1alpha1.SignRequest{Purpose: PurposeSeal, Content: []byte("x")}))
	add("sign of a seal purpose", seal.ReasonPurpose, err)
	_, err = k.Client.PublicKey(ctx, connect.NewRequest(&sealv1alpha1.PublicKeyRequest{Purpose: PurposeMac}))
	add("public key of a mac purpose", seal.ReasonPurpose, err)
	for _, s := range []string{"-", "a/b", "a b", strings.Repeat("s", 129), "é"} {
		_, err = k.seal(ctx, PurposeSeal, s, []string{"a"}, nil)
		add(fmt.Sprintf("a scope of %d bytes", len(s)), seal.ReasonScope, err)
	}
	for _, a := range [][]string{nil, make([]string, seal.MaxAddressParts+1), {strings.Repeat("p", 1025)}} {
		_, err = k.seal(ctx, PurposeSeal, scope, a, nil)
		add(fmt.Sprintf("an address of %d parts", len(a)), seal.ReasonAddress, err)
	}
	_, err = k.seal(ctx, PurposeSeal, scope, []string{"a"}, make([]byte, seal.MaxValue+1))
	add("a plaintext above 1 MiB", seal.ReasonSize, err)
	for _, c := range checks {
		if e := refused(c.err, connect.CodeInvalidArgument, c.reason); e != nil {
			return fmt.Errorf("%s: %w", c.name, e)
		}
	}
	return nil
}

func rewrapCase(ctx context.Context, k *Kit) error {
	if _, err := k.seal(ctx, PurposeSeal, k.Scope("rewrap"), []string{"a"}, []byte("v")); err != nil {
		return err
	}
	// The recipients are the host's, so the kit holds a pass only to succeed; the block's own tests prove the wraps it writes for others.
	if _, err := k.Client.Rewrap(ctx, connect.NewRequest(&sealv1alpha1.RewrapRequest{})); err != nil {
		return fmt.Errorf("rewrap: %w", err)
	}
	return nil
}

func sealLimit(ctx context.Context, k *Kit) error {
	caps, err := k.Client.Capabilities(ctx, connect.NewRequest(&sealv1alpha1.CapabilitiesRequest{}))
	if err != nil {
		return err
	}
	if caps.Msg.GetSealsPerGeneration() != SealsPerGeneration || caps.Msg.GetSealReservation() != SealReservation {
		return fmt.Errorf("the block's seal limit is %d with reservations of %d: start it with conformance.SealsPerGeneration and conformance.SealReservation",
			caps.Msg.GetSealsPerGeneration(), caps.Msg.GetSealReservation())
	}
	scope := k.Scope("limit")
	addr := []string{"l"}
	perGeneration := map[uint32]int{}
	var last uint32
	var envs [][]byte
	for i := range 3*SealsPerGeneration + 1 {
		env, err := k.seal(ctx, PurposeSeal, scope, addr, []byte(fmt.Sprint(i)))
		if err != nil {
			return fmt.Errorf("seal %d: %w", i, err)
		}
		g := generationOf(env)
		if g < last {
			return fmt.Errorf("seal %d went back from generation %d to %d", i, last, g)
		}
		last = g
		perGeneration[g]++
		if perGeneration[g] > SealsPerGeneration {
			return fmt.Errorf("generation %d made more than %d seals", g, SealsPerGeneration)
		}
		envs = append(envs, env)
	}
	if len(perGeneration) < 4 {
		return fmt.Errorf("%d seals used %d generations, want at least 4", len(envs), len(perGeneration))
	}
	for i := 0; i < len(envs); i += SealsPerGeneration / 2 {
		got, err := k.open(ctx, item(PurposeSeal, scope, addr, envs[i]))
		if err != nil {
			return fmt.Errorf("an envelope of generation %d after the rotations: %w", generationOf(envs[i]), err)
		}
		if string(got[0]) != fmt.Sprint(i) {
			return errors.New("an envelope opened to another value after the rotations")
		}
	}
	return nil
}

func compromised(ctx context.Context, k *Kit) error {
	scope := k.Scope("compromised")
	addr := []string{"c"}
	before, err := k.seal(ctx, PurposeSeal, scope, addr, []byte("before"))
	if err != nil {
		return err
	}
	g := generationOf(before)
	res, err := k.Client.MarkCompromised(ctx, connect.NewRequest(&sealv1alpha1.MarkCompromisedRequest{Purpose: PurposeSeal, Scope: scope, Generation: g}))
	if err != nil {
		return fmt.Errorf("mark: %w", err)
	}
	if res.Msg.GetGeneration() != g+1 {
		return fmt.Errorf("the mark of generation %d left generation %d current, want %d", g, res.Msg.GetGeneration(), g+1)
	}
	for range 3 {
		after, err := k.seal(ctx, PurposeSeal, scope, addr, []byte("after"))
		if err != nil {
			return fmt.Errorf("a seal after the mark: %w", err)
		}
		if generationOf(after) == g {
			return errors.New("a seal under the compromised generation")
		}
	}
	got, err := k.open(ctx, item(PurposeSeal, scope, addr, before))
	if err != nil {
		return fmt.Errorf("an envelope of the compromised generation: %w", err)
	}
	if string(got[0]) != "before" {
		return errors.New("an envelope of the compromised generation opened to another value")
	}
	_, err = k.Client.MarkCompromised(ctx, connect.NewRequest(&sealv1alpha1.MarkCompromisedRequest{Purpose: PurposeSeal, Scope: scope, Generation: g + 9}))
	if e := refused(err, connect.CodeInvalidArgument, seal.ReasonGeneration); e != nil {
		return fmt.Errorf("a mark of a generation never made: %w", e)
	}
	_, err = k.Client.MarkCompromised(ctx, connect.NewRequest(&sealv1alpha1.MarkCompromisedRequest{Purpose: PurposeMac, Scope: scope, Generation: 1}))
	if e := refused(err, connect.CodeInvalidArgument, seal.ReasonPurpose); e != nil {
		return fmt.Errorf("a mark of a mac purpose: %w", e)
	}
	_, err = k.Client.MarkCompromised(ctx, connect.NewRequest(&sealv1alpha1.MarkCompromisedRequest{Purpose: PurposeSeal, Scope: RefusedPrefix + scope, Generation: 1}))
	if e := refused(err, connect.CodePermissionDenied, seal.ReasonRule); e != nil {
		return fmt.Errorf("a mark the rules refuse: %w", e)
	}
	return nil
}
