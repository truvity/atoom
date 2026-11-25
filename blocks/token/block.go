package token

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"

	"connectrpc.com/connect"

	sealv1alpha1 "github.com/truvity/atoom/blocks/seal/gen/atoom/seal/v1alpha1"
	tokenv1alpha1 "github.com/truvity/atoom/blocks/token/gen/atoom/token/v1alpha1"
	"github.com/truvity/atoom/blocks/token/gen/atoom/token/v1alpha1/tokenv1alpha1connect"
	"github.com/truvity/atoom/common/refusal"
)

// The reasons of the block's refusals: a closed list (docs/token-block.md, "Refusals").
const (
	ReasonPurpose = "purpose" // not a purpose of the declaration
	ReasonTyp     = "typ"     // not a type of the purpose
	ReasonClaims  = "claims"  // not one strict JSON object, or more than MaxClaims
	ReasonKeys    = "keys"    // a host's key that is not a public key of its algorithm, has no kid or a kid taken, or a chain too long
	ReasonSize    = "size"    // a token longer than MaxToken
	ReasonRule    = "rule"    // the host's rules refused the call
	ReasonToken   = "token"   // a token that does not verify, for any reason
	ReasonSeal    = "seal"    // the seal block did not answer, or refused for a reason of its own that is not a refusal of the caller
	ReasonKey     = "key"     // the seal block answered a key or a signature that does not hold together
	// The reasons of a ring (record 0024).
	ReasonNotActive = "not_active" // this node holds no active key of the ring purpose: still pre-published, revoked or dropped
	ReasonRing      = "ring"       // the ring cannot be read or written
	ReasonTime      = "time"       // a confirmation of a time after now
)

// The limits of a call.
const (
	MaxClaims   = 64 << 10
	MaxToken    = 96 << 10
	MaxHostKeys = 64
	MaxChain    = 8
	MaxKid      = 512
)

// Block is the token block: a handler of TokenService over the seal block. It is safe for concurrent use.
type Block struct {
	tokenv1alpha1connect.UnimplementedTokenServiceHandler

	opts     Options
	purposes map[string]Purpose

	mu      sync.Mutex
	current map[string]*currentKey
}

// currentKey is the current key of a purpose as the block last read it.
type currentKey struct {
	key   *publicKey
	keyID string
	kid   string
}

// New checks the options, reads the seal block's Capabilities and refuses a purpose that is not one of its signing purposes (or of
// another algorithm than the one named), and reads the public key of every purpose, so that a host learns at the start of a key that is
// missing.
func New(ctx context.Context, opts Options) (*Block, error) {
	if err := opts.check(); err != nil {
		return nil, err
	}
	caps, err := opts.Seal.Capabilities(ctx, connect.NewRequest(&sealv1alpha1.CapabilitiesRequest{}))
	if err != nil {
		return nil, fmt.Errorf("token: the seal block's Capabilities: %w", err)
	}
	served := map[string]*sealv1alpha1.PurposeInfo{}
	for _, p := range caps.Msg.GetPurposes() {
		served[p.GetName()] = p
	}
	b := &Block{opts: opts, purposes: map[string]Purpose{}, current: map[string]*currentKey{}}
	b.opts.Purposes = make([]Purpose, 0, len(opts.Purposes))
	for _, p := range opts.Purposes {
		algorithm := p.Algorithm
		for _, slot := range slotsOf(p) {
			s := served[slot]
			if s == nil || s.GetUse() != sealv1alpha1.Use_USE_SIGN {
				return nil, fmt.Errorf("token: the seal block does not serve the signing purpose %q", slot)
			}
			if algorithm != "" && algorithm != s.GetAlgorithm() {
				return nil, fmt.Errorf("token: the seal block serves the purpose %q as %s, not %s", slot, s.GetAlgorithm(), algorithm)
			}
			algorithm = s.GetAlgorithm()
		}
		p.Algorithm = algorithm
		p.Types = slices.Clone(p.Types)
		if p.Ring != nil {
			r := *p.Ring
			r.Slots = slices.Clone(r.slots(p.Name))
			r.Current = r.current(p.Name)
			p.Ring = &r
		}
		b.opts.Purposes = append(b.opts.Purposes, p)
		b.purposes[p.Name] = p
	}
	for _, p := range b.opts.Purposes {
		if p.Ring == nil {
			if _, err := b.refresh(ctx, p); err != nil {
				return nil, fmt.Errorf("token: the key of the purpose %q: %w", p.Name, err)
			}
			continue
		}
		held, err := b.observe(ctx, p, true)
		if err != nil {
			return nil, fmt.Errorf("token: the keys of the slots of %q: %w", p.Name, err)
		}
		if _, _, err := b.reconcile(ctx, OpReconcile, p, held); err != nil {
			return nil, fmt.Errorf("token: the ring of %q: %w", p.Name, err)
		}
	}
	return b, nil
}

// Handler returns the path and the handler of TokenService, to mount on a socket's mux or on an in-memory transport.
func (b *Block) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return tokenv1alpha1connect.NewTokenServiceHandler(b, opts...)
}

func (b *Block) observeEvent(ctx context.Context, e Event) {
	if b.opts.Observe != nil {
		b.opts.Observe(ctx, e)
	}
}

func (b *Block) refuse(ctx context.Context, op Op, p string, code connect.Code, reason, field string) *connect.Error {
	b.observeEvent(ctx, Event{Action: EventRefuse, Op: op, Purpose: p, Reason: reason})
	return refusal.New(code, reason, field)
}

func (b *Block) decide(ctx context.Context, r Request) error {
	err := b.opts.Rules.Decide(ctx, r)
	if err == nil {
		return nil
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		b.observeEvent(ctx, Event{Action: EventRefuse, Op: r.Op, Purpose: r.Purpose, Reason: ReasonRule})
		return ce
	}
	return b.refuse(ctx, r.Op, r.Purpose, connect.CodePermissionDenied, ReasonRule, "")
}

// errInconsistent is a key or a signature of the seal block that does not hold together.
var errInconsistent = errors.New("token: the seal block's key does not hold together")

// sealFailure turns an error of the seal block into the refusal of a call. A permission_denied of seal is the refusal of its host's
// rules (sluis key@ checks what it signs) and goes to the caller as it is; anything else is unavailable, reason seal.
func (b *Block) sealFailure(ctx context.Context, op Op, p string, err error) error {
	switch {
	case errors.Is(err, errInconsistent):
		return b.refuse(ctx, op, p, connect.CodeInternal, ReasonKey, "")
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, errors.New("canceled"))
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, errors.New("deadline exceeded"))
	case connect.CodeOf(err) == connect.CodePermissionDenied:
		b.observeEvent(ctx, Event{Action: EventRefuse, Op: op, Purpose: p, Reason: ReasonSeal})
		var ce *connect.Error
		if errors.As(err, &ce) {
			return ce
		}
	}
	return b.refuse(ctx, op, p, connect.CodeUnavailable, ReasonSeal, "")
}

// refresh reads the current key of a purpose from the seal block and keeps it.
func (b *Block) refresh(ctx context.Context, p Purpose) (*currentKey, error) {
	return b.refreshSlot(ctx, p, p.Name)
}

// refreshSlot reads the key of a slot of a purpose (a seal purpose: the purpose's own name, or a slot of its ring) and keeps it.
func (b *Block) refreshSlot(ctx context.Context, p Purpose, slot string) (*currentKey, error) {
	res, err := b.opts.Seal.PublicKey(ctx, connect.NewRequest(&sealv1alpha1.PublicKeyRequest{Purpose: slot}))
	if err != nil {
		return nil, err
	}
	if res.Msg.GetAlgorithm() != p.Algorithm || res.Msg.GetKeyId() == "" {
		return nil, errInconsistent
	}
	k, err := parsePublic(p.Algorithm, res.Msg.GetSpkiDer())
	if err != nil {
		return nil, errInconsistent
	}
	c := &currentKey{key: k, keyID: res.Msg.GetKeyId(), kid: res.Msg.GetKeyId()}
	if p.Kid == KidThumbprint {
		c.kid = k.thumbprint()
	}
	if len(c.kid) > MaxKid {
		return nil, errInconsistent
	}
	b.mu.Lock()
	b.current[slot] = c
	b.mu.Unlock()
	return c, nil
}

// cached is the kept current key of a purpose, read when there is none.
func (b *Block) cached(ctx context.Context, p Purpose) (*currentKey, error) {
	return b.cachedSlot(ctx, p, p.Name)
}

// cachedSlot is the kept key of a slot, read when there is none.
func (b *Block) cachedSlot(ctx context.Context, p Purpose, slot string) (*currentKey, error) {
	b.mu.Lock()
	c := b.current[slot]
	b.mu.Unlock()
	if c != nil {
		return c, nil
	}
	return b.refreshSlot(ctx, p, slot)
}

// Capabilities says what the block serves.
func (b *Block) Capabilities(context.Context, *connect.Request[tokenv1alpha1.CapabilitiesRequest]) (*connect.Response[tokenv1alpha1.CapabilitiesResponse], error) {
	out := &tokenv1alpha1.CapabilitiesResponse{}
	for _, p := range b.opts.Purposes {
		info := &tokenv1alpha1.PurposeInfo{Name: p.Name, Algorithm: p.Algorithm, Types: slices.Clone(p.Types), Kid: p.Kid.proto()}
		if r := p.Ring; r != nil {
			info.Ring = &tokenv1alpha1.RingInfo{
				Slots: slices.Clone(r.Slots), Current: r.Current, LeadSeconds: seconds(r.Lead), RetainSeconds: seconds(r.Retain),
				CacheMaxAgeSeconds: seconds(r.CacheMaxAge), TokenLifetimeSeconds: seconds(r.TokenLifetime), BootstrapSeconds: seconds(r.Bootstrap),
				OverlapSeconds: seconds(r.Overlap), AtOnce: r.AtOnce, Confirm: r.Confirm, Node: b.opts.Node,
			}
		}
		out.Purposes = append(out.Purposes, info)
	}
	return connect.NewResponse(out), nil
}

// Sign makes a compact JWS of the claims by the key of a purpose: the header is exactly alg, kid and typ, the payload the claims' own
// bytes. The seal block hashes the signing input and the key port signs the digest; the block checks the signature under the purpose's
// public key before it answers. The public key is kept between calls: when the key that signed is not the kept one (a key rotated
// behind the block), the block reads the key again and signs once more with the new kid.
func (b *Block) Sign(ctx context.Context, req *connect.Request[tokenv1alpha1.SignRequest]) (*connect.Response[tokenv1alpha1.SignResponse], error) {
	m := req.Msg
	p, ok := b.purposes[m.GetPurpose()]
	if !ok {
		return nil, b.refuse(ctx, OpSign, m.GetPurpose(), connect.CodeInvalidArgument, ReasonPurpose, "purpose")
	}
	if !slices.Contains(p.Types, m.GetTyp()) {
		return nil, b.refuse(ctx, OpSign, p.Name, connect.CodeInvalidArgument, ReasonTyp, "typ")
	}
	claims := m.GetClaimsJson()
	if len(claims) > MaxClaims || strictObject(claims) != nil {
		return nil, b.refuse(ctx, OpSign, p.Name, connect.CodeInvalidArgument, ReasonClaims, "claims_json")
	}
	if err := b.decide(ctx, Request{Op: OpSign, Purpose: p.Name, Typ: m.GetTyp(), Claims: claims, Authorization: m.GetAuthorization()}); err != nil {
		return nil, err
	}
	if p.Ring != nil {
		res, err := b.signRing(ctx, p, m.GetTyp(), claims, m.GetAuthorization())
		if err != nil {
			return nil, err
		}
		return connect.NewResponse(res), nil
	}
	c, err := b.cached(ctx, p)
	if err != nil {
		return nil, b.sealFailure(ctx, OpSign, p.Name, err)
	}
	for attempt := 0; ; attempt++ {
		res, err := b.signWith(ctx, p, p.Name, c, m.GetTyp(), claims, m.GetAuthorization())
		if errors.Is(err, errReplaced) {
			if attempt > 0 {
				return nil, b.sealFailure(ctx, OpSign, p.Name, errInconsistent)
			}
			if c, err = b.refresh(ctx, p); err != nil {
				return nil, b.sealFailure(ctx, OpSign, p.Name, err)
			}
			continue
		}
		if err != nil {
			return nil, b.sealFailure(ctx, OpSign, p.Name, err)
		}
		return connect.NewResponse(res), nil
	}
}

// errReplaced is a signature by another key than the slot's kept one: the key was replaced behind the block.
var errReplaced = errors.New("token: the slot's key was replaced")

// signWith signs the claims by the seal purpose slot, whose key the block keeps as c, and checks the signature under it.
func (b *Block) signWith(ctx context.Context, p Purpose, slot string, c *currentKey, typ string, claims, authorization []byte) (*tokenv1alpha1.SignResponse, error) {
	input := b64.EncodeToString(marshal(header{Alg: p.Algorithm, Kid: c.kid, Typ: typ})) + "." + b64.EncodeToString(claims)
	res, err := b.opts.Seal.Sign(ctx, connect.NewRequest(&sealv1alpha1.SignRequest{Purpose: slot, Content: []byte(input), Authorization: authorization}))
	if err != nil {
		return nil, err
	}
	if res.Msg.GetKeyId() != c.keyID {
		return nil, errReplaced
	}
	sig := res.Msg.GetSignatureRaw()
	if res.Msg.GetAlgorithm() != p.Algorithm || !c.key.verify([]byte(input), sig) {
		return nil, errInconsistent
	}
	b.observeEvent(ctx, Event{Action: EventSign, Op: OpSign, Purpose: p.Name, Kid: c.kid})
	return &tokenv1alpha1.SignResponse{Token: input + "." + b64.EncodeToString(sig), Kid: c.kid, Algorithm: p.Algorithm, KeyId: c.keyID}, nil
}

// entry is one key that a key set publishes or a verification may use.
type entry struct {
	kid     string
	key     *publicKey
	x5c     [][]byte
	purpose string
	keyID   string
	types   []string
	state   tokenv1alpha1.KeyState // a ring key's; 0 for any other
	node    string
}

// purposesOf is the named purposes, or every purpose of the declaration.
func (b *Block) purposesOf(ctx context.Context, op Op, names []string) ([]Purpose, error) {
	if len(names) == 0 {
		return b.opts.Purposes, nil
	}
	out := make([]Purpose, 0, len(names))
	for i, n := range names {
		p, ok := b.purposes[n]
		if !ok || slices.Index(names, n) != i {
			return nil, b.refuse(ctx, op, n, connect.CodeInvalidArgument, ReasonPurpose, "purposes")
		}
		out = append(out, p)
	}
	return out, nil
}

// hostKeys checks the host's keys and adds them after the current keys. A kid of a current key is taken; a host's key that is the same
// key under the same kid as a current one is the same entry and is not added twice.
func (b *Block) hostKeys(ctx context.Context, op Op, set []entry, keys []*tokenv1alpha1.HostKey) ([]entry, error) {
	if len(keys) > MaxHostKeys {
		return nil, b.refuse(ctx, op, "", connect.CodeInvalidArgument, ReasonKeys, "keys")
	}
	for _, h := range keys {
		k, err := parsePublic(h.GetAlgorithm(), h.GetSpkiDer())
		if err != nil || !validKid(h.GetKid()) || len(h.GetX5C()) > MaxChain {
			return nil, b.refuse(ctx, op, "", connect.CodeInvalidArgument, ReasonKeys, "keys")
		}
		at := slices.IndexFunc(set, func(e entry) bool { return e.kid == h.GetKid() })
		if at >= 0 {
			if set[at].key.algorithm == k.algorithm && bytes.Equal(set[at].key.spki, k.spki) && set[at].purpose != "" {
				continue
			}
			return nil, b.refuse(ctx, op, "", connect.CodeInvalidArgument, ReasonKeys, "keys")
		}
		set = append(set, entry{kid: h.GetKid(), key: k, x5c: h.GetX5C()})
	}
	return set, nil
}

// validKid is 1 to MaxKid printable ASCII characters.
func validKid(kid string) bool {
	if kid == "" || len(kid) > MaxKid {
		return false
	}
	return !strings.ContainsFunc(kid, func(r rune) bool { return r < 0x20 || r > 0x7e })
}

// KeySet answers the JWK Set of the current key of each named purpose, read again from the seal block, and of the host's keys after
// them. Two purposes on one key are one entry.
func (b *Block) KeySet(ctx context.Context, req *connect.Request[tokenv1alpha1.KeySetRequest]) (*connect.Response[tokenv1alpha1.KeySetResponse], error) {
	m := req.Msg
	ps, err := b.purposesOf(ctx, OpKeySet, m.GetPurposes())
	if err != nil {
		return nil, err
	}
	for name, chain := range m.GetChains() {
		if _, ok := b.purposes[name]; !ok || len(chain.GetX5C()) > MaxChain {
			return nil, b.refuse(ctx, OpKeySet, name, connect.CodeInvalidArgument, ReasonKeys, "chains")
		}
	}
	if err := b.decide(ctx, Request{Op: OpKeySet}); err != nil {
		return nil, err
	}
	var set []entry
	for _, p := range ps {
		if p.Ring != nil {
			keys, signingKid, err := b.ringEntries(ctx, OpKeySet, p, true, published)
			if err != nil {
				return nil, b.ringFailure(ctx, OpKeySet, p.Name, err)
			}
			for _, e := range keys {
				if slices.ContainsFunc(set, func(o entry) bool { return o.kid == e.kid }) {
					continue
				}
				if e.kid == signingKid {
					e.x5c = m.GetChains()[p.Name].GetX5C()
				}
				set = append(set, e)
			}
			continue
		}
		c, err := b.refresh(ctx, p)
		if err != nil {
			return nil, b.sealFailure(ctx, OpKeySet, p.Name, err)
		}
		if slices.ContainsFunc(set, func(e entry) bool { return e.kid == c.kid }) {
			continue
		}
		set = append(set, entry{kid: c.kid, key: c.key, x5c: m.GetChains()[p.Name].GetX5C(), purpose: p.Name, keyID: c.keyID})
	}
	if err := b.keyChains(ctx, set, m.GetKeyChains()); err != nil {
		return nil, err
	}
	if set, err = b.hostKeys(ctx, OpKeySet, set, m.GetKeys()); err != nil {
		return nil, err
	}
	out := &tokenv1alpha1.KeySetResponse{}
	jwks := struct {
		Keys []jwk `json:"keys"`
	}{Keys: []jwk{}}
	for _, e := range set {
		jwks.Keys = append(jwks.Keys, e.key.jwk(e.kid, e.x5c))
		out.Keys = append(out.Keys, &tokenv1alpha1.PublishedKey{Kid: e.kid, Algorithm: e.key.algorithm, SpkiDer: e.key.spki, Purpose: e.purpose, KeyId: e.keyID, State: e.state, Node: e.node})
	}
	out.JwksJson = marshal(jwks)
	return connect.NewResponse(out), nil
}

// Verify checks a compact JWS of the block's form against the current keys of the named purposes and the host's keys: the header of
// exactly alg, kid and typ, the kid of one of those keys and its algorithm, a typ the caller accepts (or, for a purpose's key, one of the
// purpose's types), the signature over the signing input, and a payload that is one strict JSON object. Any failure is the one refusal
// permission_denied, reason token. The claims are not read: exp, aud and the rest are the host's.
func (b *Block) Verify(ctx context.Context, req *connect.Request[tokenv1alpha1.VerifyRequest]) (*connect.Response[tokenv1alpha1.VerifyResponse], error) {
	m := req.Msg
	if len(m.GetToken()) > MaxToken {
		return nil, b.refuse(ctx, OpVerify, "", connect.CodeInvalidArgument, ReasonSize, "token")
	}
	for i, t := range m.GetTypes() {
		if !typPattern.MatchString(t) || slices.Index(m.GetTypes(), t) != i {
			return nil, b.refuse(ctx, OpVerify, "", connect.CodeInvalidArgument, ReasonTyp, "types")
		}
	}
	ps, err := b.purposesOf(ctx, OpVerify, m.GetPurposes())
	if err != nil {
		return nil, err
	}
	if err := b.decide(ctx, Request{Op: OpVerify}); err != nil {
		return nil, err
	}
	build := func(fresh bool) ([]entry, error) {
		read := b.cached
		if fresh {
			read = b.refresh
		}
		var set []entry
		for _, p := range ps {
			if p.Ring != nil {
				keys, _, err := b.ringEntries(ctx, OpVerify, p, fresh, verifies)
				if err != nil {
					return nil, b.ringFailure(ctx, OpVerify, p.Name, err)
				}
				set = append(set, keys...)
				continue
			}
			c, err := read(ctx, p)
			if err != nil {
				return nil, b.sealFailure(ctx, OpVerify, p.Name, err)
			}
			set = append(set, entry{kid: c.kid, key: c.key, purpose: p.Name, keyID: c.keyID, types: p.Types})
		}
		return b.hostKeys(ctx, OpVerify, set, m.GetKeys())
	}
	set, err := build(false)
	if err != nil {
		return nil, err
	}
	bad := func() error { return b.refuse(ctx, OpVerify, "", connect.CodePermissionDenied, ReasonToken, "token") }
	parts := strings.Split(m.GetToken(), ".")
	if len(parts) != 3 {
		return nil, bad()
	}
	rawHeader, err1 := b64.DecodeString(parts[0])
	payload, err2 := b64.DecodeString(parts[1])
	sig, err3 := b64.DecodeString(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return nil, bad()
	}
	h, ok := parseHeader(rawHeader)
	if !ok {
		return nil, bad()
	}
	find := func(set []entry) []entry {
		var out []entry
		for _, e := range set {
			if e.kid == h.Kid {
				out = append(out, e)
			}
		}
		return out
	}
	found := find(set)
	if len(found) == 0 && len(ps) > 0 {
		// A key may have changed behind the block since it was read: read the purposes' keys again, once.
		if set, err = build(true); err != nil {
			return nil, err
		}
		found = find(set)
	}
	input := []byte(parts[0] + "." + parts[1])
	for _, e := range found {
		if e.key.algorithm != h.Alg {
			continue
		}
		accepted := m.GetTypes()
		if len(accepted) == 0 {
			accepted = e.types
		}
		if accepted != nil && !slices.Contains(accepted, h.Typ) {
			continue
		}
		if !e.key.verify(input, sig) || strictObject(payload) != nil {
			continue
		}
		return connect.NewResponse(&tokenv1alpha1.VerifyResponse{Kid: e.kid, Algorithm: h.Alg, Typ: h.Typ, Purpose: e.purpose, ClaimsJson: payload}), nil
	}
	return nil, bad()
}
