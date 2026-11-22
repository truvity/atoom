package seal

import (
	"context"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"unicode/utf8"

	"connectrpc.com/connect"

	sealv1alpha1 "github.com/truvity/atoom/blocks/seal/gen/atoom/seal/v1alpha1"
	"github.com/truvity/atoom/blocks/seal/gen/atoom/seal/v1alpha1/sealv1alpha1connect"
	"github.com/truvity/atoom/blocks/seal/internal/envelope"
	"github.com/truvity/atoom/common/refusal"
	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
	"github.com/truvity/atoom/key/purpose"
	"github.com/truvity/atoom/key/signature"
)

// The reasons of the block's refusals: a closed list (docs/seal-block.md, "Refusals").
const (
	ReasonPurpose    = "purpose"    // not a purpose of the declaration, or not one of the call's use
	ReasonScope      = "scope"      // not a scope
	ReasonAddress    = "address"    // no address, more than 16 parts, or a part longer than 1024 octets
	ReasonItems      = "items"      // an Open of no item or of more than 64
	ReasonLabel      = "label"      // a Mac label that is empty, longer than 1024 octets or not UTF-8
	ReasonGeneration = "generation" // a generation where the purpose takes none, or none where it needs one
	ReasonSize       = "size"       // a plaintext or a content longer than 1 MiB
	ReasonOpen       = "open"       // an envelope that does not open: the one answer, whatever the cause
	ReasonRule       = "rule"       // the host's rules refused the call
	ReasonCreate     = "create"     // the key does not exist and the rules did not allow the call to make it
	ReasonWrap       = "wrap"       // the key exists and is not wrapped for this instance yet, or this instance is not a recipient
	ReasonStore      = "store"      // the store port did not answer
	ReasonKey        = "key"        // the key port did not answer
)

// The limits of a call.
const (
	MaxOpenItems    = 64
	MaxAddressParts = 16
	MaxValue        = 1 << 20
	MaxLabel        = 1024
)

// Block is the seal block: a handler of SealService over the key and store ports. It is safe for concurrent use.
type Block struct {
	sealv1alpha1connect.UnimplementedSealServiceHandler

	opts      Options
	purposes  map[string]Purpose
	wrapping  *ecdh.PublicKey
	recipient string
	cache     *cache

	flightMu sync.Mutex
	flights  map[keyRef]*flight
	makeMu   sync.Mutex
}

// New checks the options, makes sure the wrapping key and the signing keys exist in the key store (Ensure, which never exports), and
// returns the block. It reads the key store's Capabilities first and refuses one that does not serve the block's declaration
// (KeyPurposes), so a host that started its driver without it learns at the start and not at the first seal.
func New(ctx context.Context, opts Options) (*Block, error) {
	opts, byName, err := opts.withDefaults()
	if err != nil {
		return nil, err
	}
	caps, err := opts.Keys.Capabilities(ctx, connect.NewRequest(&keyv1alpha1.CapabilitiesRequest{}))
	if err != nil {
		return nil, fmt.Errorf("seal: the key port's Capabilities: %w", err)
	}
	served := map[string]*keyv1alpha1.PurposeInfo{}
	for _, p := range caps.Msg.GetPurposes() {
		served[p.GetName()] = p
	}
	for _, d := range KeyPurposes(opts.WrappingPurpose, opts.Purposes) {
		p := served[d.Name]
		if p == nil || p.GetAlgorithm() != d.Algorithm || !slices.Contains(p.GetOperations(), string(d.Operations[0])) {
			return nil, fmt.Errorf("seal: the key store does not serve the purpose %q as %s with %s: start it with seal.KeyPurposes", d.Name, d.Algorithm, d.Operations[0])
		}
	}
	res, err := opts.Keys.Ensure(ctx, connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: opts.WrappingPurpose, Algorithm: purpose.ECDHP384}))
	if err != nil {
		return nil, fmt.Errorf("seal: the wrapping key: %w", err)
	}
	wrapping, err := parseP384(res.Msg.GetSpkiDer())
	if err != nil {
		return nil, err
	}
	for _, p := range opts.Purposes {
		if p.Use != UseSign {
			continue
		}
		if _, err := opts.Keys.Ensure(ctx, connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: p.Name, Algorithm: p.Algorithm})); err != nil {
			return nil, fmt.Errorf("seal: the key of the purpose %q: %w", p.Name, err)
		}
	}
	return &Block{
		opts: opts, purposes: byName, wrapping: wrapping, recipient: recipientLabel(wrapping),
		cache: newCache(opts.CacheTTL, opts.Now), flights: map[keyRef]*flight{},
	}, nil
}

// Handler returns the path and the handler of SealService, to mount on a socket's mux or on an in-memory transport.
func (b *Block) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return sealv1alpha1connect.NewSealServiceHandler(b, opts...)
}

// Recipient is this instance's recipient label: the lower-case base32 of the RFC 7638 thumbprint of its wrapping key.
func (b *Block) Recipient() string { return b.recipient }

// WrappingKey is this instance's wrapping public key, for a host that lists the recipients of its installation.
func (b *Block) WrappingKey() *ecdh.PublicKey { return b.wrapping }

// Forget drops every cached data key and wipes it, and forgets the current generations: the next call opens its key again from its wrap.
// A host calls it when it learns that keys were erased elsewhere and will not wait for its next Rewrap.
func (b *Block) Forget() { b.cache.forget() }

// Close wipes every cached data key and keeps none after it: the end of the host. A call after Close still works, opening its key from
// its wrap each time.
func (b *Block) Close() { b.cache.close() }

func (b *Block) observe(ctx context.Context, e Event) {
	if b.opts.Observe != nil {
		b.opts.Observe(ctx, e)
	}
}

// refuse reports a refusal to the host and returns it: a Connect error with the Refusal detail, its reason and field, never a value.
func (b *Block) refuse(ctx context.Context, op Op, p string, code connect.Code, reason, field string) *connect.Error {
	b.observe(ctx, Event{Action: EventRefuse, Op: op, Purpose: p, Reason: reason})
	return refusal.New(code, reason, field)
}

// decide asks the host's rules. An error of the rules that is a Connect error goes to the caller as it is; any other is rule.
func (b *Block) decide(ctx context.Context, r Request, field string) (Decision, error) {
	d, err := b.opts.Rules.Decide(ctx, r)
	if err == nil {
		return d, nil
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		b.observe(ctx, Event{Action: EventRefuse, Op: r.Op, Purpose: r.Purpose, Reason: ReasonRule})
		return Decision{}, ce
	}
	return Decision{}, b.refuse(ctx, r.Op, r.Purpose, connect.CodePermissionDenied, ReasonRule, field)
}

// failure turns the error of a key lookup into the refusal of a call. forOpen: a key that is not there means the value cannot open,
// which is the one answer for every cause.
func (b *Block) failure(ctx context.Context, op Op, p string, err error, forOpen bool) error {
	var ce *connect.Error
	switch {
	case errors.As(err, &ce):
		return ce
	case errors.Is(err, envelope.ErrOpen), forOpen && errors.Is(err, errNoKey):
		return b.refuse(ctx, op, p, connect.CodePermissionDenied, ReasonOpen, "")
	case errors.Is(err, errNoKey):
		return b.refuse(ctx, op, p, connect.CodePermissionDenied, ReasonCreate, "")
	case errors.Is(err, errNotWrapped), errors.Is(err, errNoSelf):
		return b.refuse(ctx, op, p, connect.CodeUnavailable, ReasonWrap, "")
	case errors.Is(err, errStore):
		return b.refuse(ctx, op, p, connect.CodeUnavailable, ReasonStore, "")
	case errors.Is(err, errKeyPort):
		return b.refuse(ctx, op, p, connect.CodeUnavailable, ReasonKey, "")
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, errors.New("canceled"))
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, errors.New("deadline exceeded"))
	}
	return b.refuse(ctx, op, p, connect.CodeInternal, ReasonWrap, "")
}

// purposeOf returns the declared purpose of a call when it has one of the uses.
func (b *Block) purposeOf(name string, uses ...Use) (Purpose, bool) {
	p, ok := b.purposes[name]
	return p, ok && slices.Contains(uses, p.Use)
}

func validAddress(a []string) bool {
	if len(a) == 0 || len(a) > MaxAddressParts {
		return false
	}
	for _, p := range a {
		if len(p) > envelope.MaxPart {
			return false
		}
	}
	return true
}

// Capabilities says what the block serves.
func (b *Block) Capabilities(context.Context, *connect.Request[sealv1alpha1.CapabilitiesRequest]) (*connect.Response[sealv1alpha1.CapabilitiesResponse], error) {
	out := &sealv1alpha1.CapabilitiesResponse{Recipient: b.recipient}
	for _, p := range b.opts.Purposes {
		info := &sealv1alpha1.PurposeInfo{Name: p.Name, Algorithm: p.Algorithm, CallerGeneration: p.CallerGeneration, Code: uint32(p.Code)}
		switch p.Use {
		case UseSeal:
			info.Use = sealv1alpha1.Use_USE_SEAL
		case UseMac:
			info.Use = sealv1alpha1.Use_USE_MAC
		case UseSign:
			info.Use = sealv1alpha1.Use_USE_SIGN
		}
		out.Purposes = append(out.Purposes, info)
	}
	return connect.NewResponse(out), nil
}

// Seal seals a value under the current data key of its purpose and scope, bound to its address. A key that does not exist is made when
// the rules allow it.
func (b *Block) Seal(ctx context.Context, req *connect.Request[sealv1alpha1.SealRequest]) (*connect.Response[sealv1alpha1.SealResponse], error) {
	m := req.Msg
	p, ok := b.purposeOf(m.GetPurpose(), UseSeal)
	if !ok {
		return nil, b.refuse(ctx, OpSeal, m.GetPurpose(), connect.CodeInvalidArgument, ReasonPurpose, "purpose")
	}
	switch {
	case !validScope(m.GetScope()):
		return nil, b.refuse(ctx, OpSeal, p.Name, connect.CodeInvalidArgument, ReasonScope, "scope")
	case !validAddress(m.GetAddress()):
		return nil, b.refuse(ctx, OpSeal, p.Name, connect.CodeInvalidArgument, ReasonAddress, "address")
	case len(m.GetPlaintext()) > MaxValue:
		return nil, b.refuse(ctx, OpSeal, p.Name, connect.CodeInvalidArgument, ReasonSize, "plaintext")
	}
	d, err := b.decide(ctx, Request{Op: OpSeal, Purpose: p.Name, Scope: m.GetScope(), Address: m.GetAddress(), Authorization: m.GetAuthorization()}, "purpose")
	if err != nil {
		return nil, err
	}
	l := line{m.GetScope(), p.Name}
	g, err := b.current(ctx, l)
	if err != nil {
		return nil, b.failure(ctx, OpSeal, p.Name, err, false)
	}
	if g == 0 {
		if !d.MayCreate {
			return nil, b.refuse(ctx, OpSeal, p.Name, connect.CodePermissionDenied, ReasonCreate, "")
		}
		g = 1
	}
	var env []byte
	err = b.withKey(ctx, keyRef{l.scope, l.purpose, g}, d.MayCreate, func(key []byte) error {
		var e error
		env, e = envelope.Seal(key, b.opts.SealDomain, p.Code, g, m.GetAddress(), m.GetPlaintext())
		return e
	})
	if err != nil {
		return nil, b.failure(ctx, OpSeal, p.Name, err, false)
	}
	if g == 1 {
		b.cache.setGeneration(l, g)
	}
	return connect.NewResponse(&sealv1alpha1.SealResponse{Envelope: env}), nil
}

// opening is one item of an Open that passed its checks.
type opening struct {
	ref     keyRef
	address []string
	env     []byte
}

// Open opens a batch of envelopes, all or none, with the checks of Seal for each item. The generation comes from the envelope, so an
// older generation still opens. An open never makes a key. Every failure to open is the one refusal permission_denied/open: it says
// nothing of which part failed.
func (b *Block) Open(ctx context.Context, req *connect.Request[sealv1alpha1.OpenRequest]) (*connect.Response[sealv1alpha1.OpenResponse], error) {
	m := req.Msg
	if n := len(m.GetItems()); n == 0 || n > MaxOpenItems {
		return nil, b.refuse(ctx, OpOpen, "", connect.CodeInvalidArgument, ReasonItems, "items")
	}
	jobs := make([]opening, 0, len(m.GetItems()))
	for _, it := range m.GetItems() {
		p, ok := b.purposeOf(it.GetPurpose(), UseSeal)
		switch {
		case !ok:
			return nil, b.refuse(ctx, OpOpen, it.GetPurpose(), connect.CodeInvalidArgument, ReasonPurpose, "items.purpose")
		case !validScope(it.GetScope()):
			return nil, b.refuse(ctx, OpOpen, p.Name, connect.CodeInvalidArgument, ReasonScope, "items.scope")
		case !validAddress(it.GetAddress()):
			return nil, b.refuse(ctx, OpOpen, p.Name, connect.CodeInvalidArgument, ReasonAddress, "items.address")
		}
		if _, err := b.decide(ctx, Request{Op: OpOpen, Purpose: p.Name, Scope: it.GetScope(), Address: it.GetAddress(), Authorization: m.GetAuthorization()}, "items.purpose"); err != nil {
			return nil, err
		}
		h, err := envelope.ParseHeader(it.GetEnvelope())
		if err != nil || h.Code != p.Code || h.Generation == 0 {
			return nil, b.refuse(ctx, OpOpen, p.Name, connect.CodePermissionDenied, ReasonOpen, "")
		}
		jobs = append(jobs, opening{ref: keyRef{it.GetScope(), p.Name, h.Generation}, address: it.GetAddress(), env: it.GetEnvelope()})
	}
	plaintexts := make([][]byte, 0, len(jobs))
	for _, j := range jobs {
		var pt []byte
		err := b.withKey(ctx, j.ref, false, func(key []byte) error {
			var e error
			pt, e = envelope.Open(key, b.opts.SealDomain, j.env, j.address)
			return e
		})
		if err != nil {
			for _, p := range plaintexts {
				clear(p)
			}
			return nil, b.failure(ctx, OpOpen, j.ref.purpose, err, true)
		}
		plaintexts = append(plaintexts, pt)
	}
	return connect.NewResponse(&sealv1alpha1.OpenResponse{Plaintexts: plaintexts}), nil
}

// macTag is HMAC-SHA384 under the data key over the label's length as 16 bits big-endian, the label and the input (sluis key@'s
// construction, unchanged).
func macTag(key []byte, label string, input []byte) []byte {
	h := hmac.New(sha512.New384, key)
	h.Write(binary.BigEndian.AppendUint16(nil, uint16(len(label)))) //nolint:gosec // bounded by MaxLabel
	h.Write([]byte(label))
	h.Write(input)
	return h.Sum(nil)
}

// Mac makes a keyed hash under the data key of its purpose and scope. The label is bound into the input, so two uses never produce the
// same hash for the same input. The generation is the current one, or the caller's for a purpose declared with CallerGeneration.
func (b *Block) Mac(ctx context.Context, req *connect.Request[sealv1alpha1.MacRequest]) (*connect.Response[sealv1alpha1.MacResponse], error) {
	m := req.Msg
	p, ok := b.purposeOf(m.GetPurpose(), UseMac)
	if !ok {
		return nil, b.refuse(ctx, OpMac, m.GetPurpose(), connect.CodeInvalidArgument, ReasonPurpose, "purpose")
	}
	label := m.GetLabel()
	switch {
	case !validScope(m.GetScope()):
		return nil, b.refuse(ctx, OpMac, p.Name, connect.CodeInvalidArgument, ReasonScope, "scope")
	case label == "" || len(label) > MaxLabel || !utf8.ValidString(label):
		return nil, b.refuse(ctx, OpMac, p.Name, connect.CodeInvalidArgument, ReasonLabel, "label")
	case p.CallerGeneration != (m.GetGeneration() != 0):
		return nil, b.refuse(ctx, OpMac, p.Name, connect.CodeInvalidArgument, ReasonGeneration, "generation")
	case len(m.GetInput()) > MaxValue:
		return nil, b.refuse(ctx, OpMac, p.Name, connect.CodeInvalidArgument, ReasonSize, "input")
	}
	d, err := b.decide(ctx, Request{Op: OpMac, Purpose: p.Name, Scope: m.GetScope(), Label: label, Generation: m.GetGeneration(), Authorization: m.GetAuthorization()}, "purpose")
	if err != nil {
		return nil, err
	}
	l := line{m.GetScope(), p.Name}
	g := m.GetGeneration()
	if !p.CallerGeneration {
		if g, err = b.current(ctx, l); err != nil {
			return nil, b.failure(ctx, OpMac, p.Name, err, false)
		}
		if g == 0 {
			if !d.MayCreate {
				return nil, b.refuse(ctx, OpMac, p.Name, connect.CodePermissionDenied, ReasonCreate, "")
			}
			g = 1
		}
	}
	var tag []byte
	err = b.withKey(ctx, keyRef{l.scope, l.purpose, g}, d.MayCreate, func(key []byte) error {
		tag = macTag(key, label, m.GetInput())
		return nil
	})
	if err != nil {
		return nil, b.failure(ctx, OpMac, p.Name, err, false)
	}
	if !p.CallerGeneration && g == 1 {
		b.cache.setGeneration(l, g)
	}
	return connect.NewResponse(&sealv1alpha1.MacResponse{Mac: tag}), nil
}

// digest hashes content for the algorithm of a signing purpose: SHA-384 for ES384, SHA-256 for ES256 and RS256.
func digest(algorithm string, content []byte) []byte {
	if algorithm == purpose.ES384 {
		d := sha512.Sum384(content)
		return d[:]
	}
	d := sha256.Sum256(content)
	return d[:]
}

// Sign signs content in place by the key of a signing purpose, after the host's rules, which may check what is signed. The block hashes
// the content for the algorithm and the key port signs the digest (record 0016); the response carries the DER and, for ECDSA, the raw
// r||s of a JWS.
func (b *Block) Sign(ctx context.Context, req *connect.Request[sealv1alpha1.SignRequest]) (*connect.Response[sealv1alpha1.SignResponse], error) {
	m := req.Msg
	p, ok := b.purposeOf(m.GetPurpose(), UseSign)
	if !ok {
		return nil, b.refuse(ctx, OpSign, m.GetPurpose(), connect.CodeInvalidArgument, ReasonPurpose, "purpose")
	}
	if len(m.GetContent()) > MaxValue {
		return nil, b.refuse(ctx, OpSign, p.Name, connect.CodeInvalidArgument, ReasonSize, "content")
	}
	if _, err := b.decide(ctx, Request{Op: OpSign, Purpose: p.Name, Content: m.GetContent(), Authorization: m.GetAuthorization()}, "purpose"); err != nil {
		return nil, err
	}
	res, err := b.opts.Keys.Sign(ctx, connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: p.Name, Digest: digest(p.Algorithm, m.GetContent()), Algorithm: p.Algorithm}))
	if err != nil {
		return nil, b.refuse(ctx, OpSign, p.Name, connect.CodeUnavailable, ReasonKey, "")
	}
	der := res.Msg.GetSignatureDer()
	raw := der
	if p.Algorithm != purpose.RS256 {
		if raw, err = signature.DERToRaw(p.Algorithm, der); err != nil {
			return nil, b.refuse(ctx, OpSign, p.Name, connect.CodeInternal, ReasonKey, "")
		}
	}
	return connect.NewResponse(&sealv1alpha1.SignResponse{SignatureDer: der, SignatureRaw: raw, KeyId: res.Msg.GetKeyId(), Algorithm: p.Algorithm}), nil
}

// PublicKey gives the public key of a signing purpose.
func (b *Block) PublicKey(ctx context.Context, req *connect.Request[sealv1alpha1.PublicKeyRequest]) (*connect.Response[sealv1alpha1.PublicKeyResponse], error) {
	p, ok := b.purposeOf(req.Msg.GetPurpose(), UseSign)
	if !ok {
		return nil, b.refuse(ctx, OpPublicKey, req.Msg.GetPurpose(), connect.CodeInvalidArgument, ReasonPurpose, "purpose")
	}
	if _, err := b.decide(ctx, Request{Op: OpPublicKey, Purpose: p.Name}, "purpose"); err != nil {
		return nil, err
	}
	res, err := b.opts.Keys.PublicKey(ctx, connect.NewRequest(&keyv1alpha1.PublicKeyRequest{Purpose: p.Name}))
	if err != nil {
		return nil, b.refuse(ctx, OpPublicKey, p.Name, connect.CodeUnavailable, ReasonKey, "")
	}
	return connect.NewResponse(&sealv1alpha1.PublicKeyResponse{SpkiDer: res.Msg.GetSpkiDer(), KeyId: res.Msg.GetKeyId(), Algorithm: p.Algorithm}), nil
}

// Rotate makes a new generation of the data key of a purpose and scope. New seals use it; envelopes of the older generations keep
// opening, because their generation is in their header. A Mac purpose whose caller names the generation has no rotation here.
func (b *Block) Rotate(ctx context.Context, req *connect.Request[sealv1alpha1.RotateRequest]) (*connect.Response[sealv1alpha1.RotateResponse], error) {
	m := req.Msg
	p, ok := b.purposeOf(m.GetPurpose(), UseSeal, UseMac)
	if !ok || p.CallerGeneration {
		return nil, b.refuse(ctx, OpRotate, m.GetPurpose(), connect.CodeInvalidArgument, ReasonPurpose, "purpose")
	}
	if !validScope(m.GetScope()) {
		return nil, b.refuse(ctx, OpRotate, p.Name, connect.CodeInvalidArgument, ReasonScope, "scope")
	}
	if _, err := b.decide(ctx, Request{Op: OpRotate, Purpose: p.Name, Scope: m.GetScope(), Authorization: m.GetAuthorization()}, "purpose"); err != nil {
		return nil, err
	}
	g, err := b.rotate(ctx, line{m.GetScope(), p.Name})
	if err != nil {
		return nil, b.failure(ctx, OpRotate, p.Name, err, false)
	}
	return connect.NewResponse(&sealv1alpha1.RotateResponse{Generation: g}), nil
}

// Erase deletes every wrap of a scope, for the purposes named or for every data-key purpose: every generation and every recipient. What
// was sealed under them no longer opens, because the wrap was the only route to the key. A later Seal that may make a key makes a new
// one, which opens nothing sealed under the erased one. Another instance stops opening at its next Rewrap, or when its cache entry ends.
func (b *Block) Erase(ctx context.Context, req *connect.Request[sealv1alpha1.EraseRequest]) (*connect.Response[sealv1alpha1.EraseResponse], error) {
	m := req.Msg
	if !validScope(m.GetScope()) {
		return nil, b.refuse(ctx, OpErase, "", connect.CodeInvalidArgument, ReasonScope, "scope")
	}
	names := m.GetPurposes()
	if len(names) == 0 {
		for _, p := range b.opts.Purposes {
			if p.dataKey() {
				names = append(names, p.Name)
			}
		}
	}
	for _, n := range names {
		if _, ok := b.purposeOf(n, UseSeal, UseMac); !ok {
			return nil, b.refuse(ctx, OpErase, n, connect.CodeInvalidArgument, ReasonPurpose, "purposes")
		}
	}
	for _, n := range names {
		if _, err := b.decide(ctx, Request{Op: OpErase, Purpose: n, Scope: m.GetScope(), Authorization: m.GetAuthorization()}, "purposes"); err != nil {
			return nil, err
		}
	}
	total := 0
	for _, n := range names {
		c, err := b.erase(ctx, line{m.GetScope(), n})
		total += c
		if err != nil {
			return nil, b.failure(ctx, OpErase, n, err, false)
		}
	}
	return connect.NewResponse(&sealv1alpha1.EraseResponse{Wraps: uint32(min(total, 1<<31))}), nil
}

// Rewrap runs one rewrap pass: every data key that this instance opens is wrapped for each recipient that lacks a wrap, and keys whose
// wrap for this instance is gone are dropped from the cache. A host runs it on a timer, and after a change of its recipients.
func (b *Block) Rewrap(ctx context.Context, _ *connect.Request[sealv1alpha1.RewrapRequest]) (*connect.Response[sealv1alpha1.RewrapResponse], error) {
	if _, err := b.decide(ctx, Request{Op: OpRewrap}, ""); err != nil {
		return nil, err
	}
	n, err := b.rewrap(ctx)
	if err != nil {
		return nil, b.failure(ctx, OpRewrap, "", err, false)
	}
	return connect.NewResponse(&sealv1alpha1.RewrapResponse{Written: uint32(min(n, 1<<31))}), nil //nolint:gosec // bounded by the min
}
