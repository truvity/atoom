package seal

import (
	"context"
	"crypto/ecdh"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/truvity/atoom/key/gen/atoom/key/v1alpha1/keyv1alpha1connect"
	"github.com/truvity/atoom/key/purpose"
	"github.com/truvity/atoom/store/gen/atoom/store/v1alpha1/storev1alpha1connect"
)

// Use is what a purpose of the host's declaration is for.
type Use int

// The uses.
const (
	UseSeal Use = iota + 1 // Seal and Open, under a data key of the purpose
	UseMac                 // Mac, under a data key of the purpose
	UseSign                // Sign and PublicKey, by the purpose's key in the key store
)

func (u Use) String() string {
	switch u {
	case UseSeal:
		return "seal"
	case UseMac:
		return "mac"
	case UseSign:
		return "sign"
	}
	return fmt.Sprintf("Use(%d)", int(u))
}

// Purpose is one purpose that the host declares to the block. The block serves those names and refuses every other (reason purpose).
type Purpose struct {
	// Name is the purpose: 1 to 64 characters of a-z, 0-9 and '-', not beginning or ending with '-' (the names of the key port).
	Name string
	// Use is what it is for.
	Use Use
	// Code names a UseSeal purpose in its envelopes: 1 to 255, one per purpose. It is part of the envelope's authenticated data, so a
	// code must never be given to another purpose while an envelope sealed under it lives. sluis's codes are data 1, evidence 2,
	// cookie 3, grant 4, chain 5 and credential 6.
	Code byte
	// Algorithm is the algorithm of a UseSign purpose's key: purpose.ES384, purpose.ES256 or purpose.RS256.
	Algorithm string
	// Slot is the PIV slot of a UseSign purpose on a card (purpose.Declaration.Slot); empty takes the slot of the reference map.
	Slot string
	// CallerGeneration says that the caller of Mac names the generation of a UseMac purpose's key, at least 1 (sluis's pseudonym key,
	// whose generation is an epoch). Without it Mac uses the current generation, as Seal does.
	CallerGeneration bool
}

// dataKey says whether the purpose has data keys: UseSeal and UseMac.
func (p Purpose) dataKey() bool { return p.Use == UseSeal || p.Use == UseMac }

// Op is what a call asks of the block, as the rules see it.
type Op string

// The operations.
const (
	OpSeal      Op = "seal"
	OpOpen      Op = "open"
	OpMac       Op = "mac"
	OpSign      Op = "sign"
	OpPublicKey Op = "public_key"
	OpRotate    Op = "rotate"
	OpErase     Op = "erase"
	OpRewrap    Op = "rewrap"
)

// Request is what the rules decide on: one call, or one item of an Open. The block has checked the form of every field before it asks.
type Request struct {
	Op      Op
	Purpose string // empty for OpRewrap
	// Scope is the scope of the data key: empty for the installation's own, or the host's name (a tenant, a store).
	Scope string
	// Address is the address an envelope is bound to (OpSeal, OpOpen).
	Address []string
	// Label is the label of OpMac.
	Label string
	// Generation is the generation the caller named (OpMac with a caller generation), or 0.
	Generation uint32
	// Content is what OpSign signs, for a host whose rules check what is signed (sluis signs only what parses for the purpose).
	Content []byte
	// Authorization is the caller's opaque proof (a sealed grant, say); the block never reads it.
	Authorization []byte
}

// Decision is what the rules allow beyond the call itself.
type Decision struct {
	// MayCreate lets the call make the data key of its purpose and scope when none exists (OpSeal, and OpMac). Without it, a call that
	// needs a key that does not exist is refused (permission_denied, reason create), so that a caller cannot plant a key for a scope
	// that the host never made. Open never makes a key.
	MayCreate bool
}

// Rules is the host's rules hook (record 0008: the host supplies the rules). It is asked before every call and for every item of an
// Open. An error refuses the call: a *connect.Error is returned to the caller as it is, and any other error is permission_denied with
// the reason rule. The caller's identity is the host's to find in ctx (a peer from the socket, or what the host's interceptor put
// there); the block knows none.
type Rules interface {
	Decide(ctx context.Context, r Request) (Decision, error)
}

// RulesFunc is a function as Rules.
type RulesFunc func(ctx context.Context, r Request) (Decision, error)

// Decide calls f.
func (f RulesFunc) Decide(ctx context.Context, r Request) (Decision, error) { return f(ctx, r) }

// AllowAll allows every call and lets Seal and Mac make a missing key. It is for tests, and for a host that has decided who may call by
// giving the client to one consumer (an in-process consumer that is the block's only caller).
func AllowAll() Rules {
	return RulesFunc(func(context.Context, Request) (Decision, error) { return Decision{MayCreate: true}, nil })
}

// Recipients is the list of the wrapping public keys that every data key is wrapped for (sluis: the signed list of its KeyPlan). The
// block calls it when it makes a key and at each Rewrap. It must hold this instance's own wrapping key, or no key is made. A nil
// Recipients is this instance alone.
type Recipients func(ctx context.Context) ([]*ecdh.PublicKey, error)

// Event is a fact the block reports to the host's Observe: an audit fact for a host that keeps them. It names no value and no key.
type Event struct {
	Action     string // EventMake, EventWrap, EventErase, EventRotate or EventRefuse
	Op         Op     // the call, for EventRefuse
	Purpose    string
	Scope      string
	Generation uint32
	Recipient  string // EventWrap: the recipient the wrap is for
	Reason     string // EventRefuse: the refusal's reason
	Count      int    // EventMake: the wraps written; EventErase: the wraps deleted
}

// The actions of an Event.
const (
	EventMake   = "data_key.make" // a data key was made, with its wraps
	EventWrap   = "wrap.write"    // a wrap was written for a recipient
	EventErase  = "wrap.erase"    // the wraps of a scope and purpose were deleted
	EventRotate = "data_key.rotate"
	EventRefuse = "refusal"
)

// Options is what a host gives the block.
type Options struct {
	// Keys is a client of the key port: the driver that holds the wrapping key and the signing keys. The host starts it with the
	// declaration of KeyPurposes.
	Keys keyv1alpha1connect.KeyStoreServiceClient
	// Items is a client of the store port, where the wraps are kept. A host may give the block a namespace of its own.
	Items storev1alpha1connect.ItemServiceClient
	// Purposes is the host's declaration.
	Purposes []Purpose
	// Rules is the host's rules hook. It is required: use AllowAll for a test.
	Rules Rules
	// Installation names the installation in every wrap's key derivation (sluis: the boundary), so that a wrap cannot be used in
	// another. 1 to 1024 bytes.
	Installation string
	// WrappingPurpose is the key port's purpose of the wrapping key (ECDH-P384); default DefaultWrappingPurpose.
	WrappingPurpose string
	// SealDomain is the first part of every envelope's authenticated data; default DefaultSealDomain (sluis's is "sluis-seal-v1").
	SealDomain string
	// WrapDomain is the first part of every wrap's key derivation; default DefaultWrapDomain (sluis's is "sluis-wrap-v1").
	WrapDomain string
	// KeyPrefix starts the key of every item the block writes; default DefaultKeyPrefix. Printable ASCII without a space, ending with
	// '/', at most 128 bytes.
	KeyPrefix string
	// Recipients lists who every data key is wrapped for; nil is this instance alone.
	Recipients Recipients
	// CacheTTL is how long an opened data key, and the current generation of a key, stay in memory: DefaultCacheTTL when zero, at most
	// MaxCacheTTL. It is also the delay until an Erase on another instance takes effect here, when no Rewrap runs in between.
	CacheTTL time.Duration
	// Observe receives the block's facts; none when nil. It is called on the path of the call, so it must be quick.
	Observe func(context.Context, Event)
	// Now is the clock of the cache; time.Now when nil.
	Now func() time.Time
}

// The defaults and limits of Options.
const (
	DefaultWrappingPurpose = "wrapping"
	DefaultSealDomain      = "atoom-seal-v1"
	DefaultWrapDomain      = "atoom-wrap-v1"
	DefaultKeyPrefix       = "seal/"
	DefaultCacheTTL        = 15 * time.Minute
	MaxCacheTTL            = time.Hour
)

var (
	scopePattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	prefixPattern = regexp.MustCompile(`^[!-~]{0,127}/$`)
	namePattern   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
)

// validScope says whether s is a scope: empty (the installation's own), or 1 to 128 of A-Z, a-z, 0-9, '.', '_' and '-', and not "-",
// which stands for the empty scope in keys and key derivations.
func validScope(s string) bool { return s == "" || (s != "-" && scopePattern.MatchString(s)) }

// withDefaults fills the defaults and checks the options. Every problem is named, never a value.
func (o Options) withDefaults() (Options, map[string]Purpose, error) {
	if o.WrappingPurpose == "" {
		o.WrappingPurpose = DefaultWrappingPurpose
	}
	if o.SealDomain == "" {
		o.SealDomain = DefaultSealDomain
	}
	if o.WrapDomain == "" {
		o.WrapDomain = DefaultWrapDomain
	}
	if o.KeyPrefix == "" {
		o.KeyPrefix = DefaultKeyPrefix
	}
	if o.CacheTTL == 0 {
		o.CacheTTL = DefaultCacheTTL
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	var problems []error
	add := func(format string, args ...any) { problems = append(problems, fmt.Errorf(format, args...)) }
	if o.Keys == nil {
		add("Keys: no client of the key port")
	}
	if o.Items == nil {
		add("Items: no client of the store port")
	}
	if o.Rules == nil {
		add("Rules: the host gives the rules (AllowAll for a test)")
	}
	if o.Installation == "" || len(o.Installation) > 1024 {
		add("Installation: 1 to 1024 bytes")
	}
	if len(o.SealDomain) > 64 || len(o.WrapDomain) > 64 {
		add("SealDomain, WrapDomain: at most 64 bytes")
	}
	if !prefixPattern.MatchString(o.KeyPrefix) {
		add("KeyPrefix: printable ASCII without a space, ending with '/', at most 128 bytes")
	}
	if o.CacheTTL < 0 || o.CacheTTL > MaxCacheTTL {
		add("CacheTTL: 0 to %s", MaxCacheTTL)
	}
	if !namePattern.MatchString(o.WrappingPurpose) || len(o.WrappingPurpose) > purpose.MaxName {
		add("WrappingPurpose: not a purpose name")
	}
	byName := map[string]Purpose{}
	codes := map[byte]string{}
	if len(o.Purposes) == 0 {
		add("Purposes: the host declared no purpose")
	}
	for i, p := range o.Purposes {
		where := fmt.Sprintf("Purposes[%d]", i)
		switch {
		case len(p.Name) > purpose.MaxName || !namePattern.MatchString(p.Name):
			add("%s.Name: not a purpose name (1 to 64 of a-z, 0-9 and '-', no '-' at either end)", where)
		case p.Name == o.WrappingPurpose:
			add("%s.Name: the name of the wrapping purpose", where)
		default:
			if _, dup := byName[p.Name]; dup {
				add("%s.Name: declared twice", where)
			}
		}
		switch p.Use {
		case UseSeal:
			if p.Code == 0 {
				add("%s.Code: a seal purpose needs a code, 1 to 255", where)
			} else if other, dup := codes[p.Code]; dup && other != p.Name {
				add("%s.Code: the code of another purpose", where)
			}
			codes[p.Code] = p.Name
			if p.Algorithm != "" || p.CallerGeneration || p.Slot != "" {
				add("%s: a seal purpose has a code and nothing else", where)
			}
		case UseMac:
			if p.Code != 0 || p.Algorithm != "" || p.Slot != "" {
				add("%s: a mac purpose has no code, algorithm or slot", where)
			}
		case UseSign:
			if purpose.DigestLen(p.Algorithm) == 0 {
				add("%s.Algorithm: ES384, ES256 or RS256", where)
			}
			if p.Code != 0 || p.CallerGeneration {
				add("%s: a sign purpose has an algorithm, and a slot or none", where)
			}
		default:
			add("%s.Use: UseSeal, UseMac or UseSign", where)
		}
		byName[p.Name] = p
	}
	if len(problems) > 0 {
		return o, nil, fmt.Errorf("seal: the options: %w", errors.Join(problems...))
	}
	return o, byName, nil
}

// KeyPurposes is the block's declaration to its key driver (record 0016, the B13 API): the wrapping purpose (ECDH-P384, agree) and each
// signing purpose of the host's declaration with its algorithm and slot. The data-key purposes are not the key store's: a data key is
// made by the block and kept only wrapped. The host passes the result, with any purposes of its own, to the driver it starts
// (Options.Purposes of key/software, key/awskms and key/yubikeypiv, or the --purposes file of a thin main).
func KeyPurposes(wrappingPurpose string, purposes []Purpose) []purpose.Declaration {
	if wrappingPurpose == "" {
		wrappingPurpose = DefaultWrappingPurpose
	}
	out := []purpose.Declaration{{Name: wrappingPurpose, Algorithm: purpose.ECDHP384, Operations: []purpose.Operation{purpose.Agree}}}
	for _, p := range purposes {
		if p.Use == UseSign {
			out = append(out, purpose.Declaration{Name: p.Name, Algorithm: p.Algorithm, Operations: []purpose.Operation{purpose.Sign}, Slot: p.Slot})
		}
	}
	return out
}
