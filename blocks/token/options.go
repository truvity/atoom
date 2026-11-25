package token

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/truvity/atoom/blocks/seal"
	"github.com/truvity/atoom/blocks/seal/gen/atoom/seal/v1alpha1/sealv1alpha1connect"
	tokenv1alpha1 "github.com/truvity/atoom/blocks/token/gen/atoom/token/v1alpha1"
	"github.com/truvity/atoom/key/purpose"
	"github.com/truvity/atoom/store/gen/atoom/store/v1alpha1/storev1alpha1connect"
)

// Kid is how the kid of a purpose's key is made (record 0020).
type Kid int

// The kid rules.
const (
	// KidKeyID is the key port's key_id as it is (record 0016): the default, and session's rule. On AWS KMS it carries the key ARN.
	KidKeyID Kid = iota
	// KidThumbprint is the RFC 7638 thumbprint of the key, base64url without padding: sluis's rule.
	KidThumbprint
)

func (k Kid) String() string {
	switch k {
	case KidKeyID:
		return "key_id"
	case KidThumbprint:
		return "thumbprint"
	}
	return fmt.Sprintf("Kid(%d)", int(k))
}

func (k Kid) proto() tokenv1alpha1.Kid {
	if k == KidThumbprint {
		return tokenv1alpha1.Kid_KID_THUMBPRINT
	}
	return tokenv1alpha1.Kid_KID_KEY_ID
}

// Purpose is one purpose that the host declares to the block: a signing purpose (seal.UseSign) of the seal block it is given.
type Purpose struct {
	// Name is the seal block's purpose.
	Name string
	// Types are the values of the typ header that the purpose signs: at least one, each 1 to 64 of A-Z, a-z, 0-9, '.', '+', '_' and
	// '-', starting with a letter or a digit (JWT, at+jwt, session-identity+jwt). One key for several types is the host's choice; a
	// type per key keeps a verifier from taking one kind of token for another.
	Types []string
	// Kid is the rule of the kid; KidKeyID when zero.
	Kid Kid
	// Algorithm is the purpose's algorithm (ES384, ES256 or RS256). The block reads it from the seal block's Capabilities; when given,
	// it must be that one. SealPurposes needs it.
	Algorithm string
	// Ring is the purpose's rotation ring (record 0024); nil for a purpose without one, whose key set is its current key and the host's
	// keys. A ring needs Options.Items.
	Ring *Ring
}

// Ring is the rotation ring of a purpose (record 0024, docs/token-block.md "The ring"). Its keys live in one item of the store port;
// each key moves one way, through pre-published, active, retiring and dropped, and may be revoked at any time. The two figures that the
// timings rest on are the host's: the block holds Lead to CacheMaxAge and Retain to TokenLifetime, and has no defaults.
type Ring struct {
	// Slots are the seal block's signing purposes that hold the purpose's keys on this node, in order; none is the purpose's own name.
	// The host rotates with a key published before it signs by putting a new key in a spare slot and making it Current.
	Slots []string
	// Current is the slot whose key is to sign; the first slot when empty. Only its key joins the ring.
	Current string
	// Lead is how long a new key is in the key set before it signs: at least CacheMaxAge.
	Lead time.Duration
	// Retain is how long a key that stopped signing stays in the key set: at least TokenLifetime.
	Retain time.Duration
	// CacheMaxAge is the longest time a copy of the published key set may be cached, as the host sums it (its max-age, an edge copy, a
	// verifier's refetch, the skew). More than 0.
	CacheMaxAge time.Duration
	// TokenLifetime is the longest life of a token of the purpose, with the skew. More than 0.
	TokenLifetime time.Duration
	// Bootstrap: while every key of the ring joined less than this ago, a node's first key signs at once (a first deploy of several
	// nodes). 0 turns it off.
	Bootstrap time.Duration
	// Overlap is how long a node's earlier active key still signs after a newer key of the node became active: the replicas that have
	// not restarted yet, in a rolling restart. 0 by default.
	Overlap time.Duration
	// AtOnce lets a new key of the current slot sign at once when this node holds no other active key in its slots: a rotation that is
	// a restart onto a new key, the previous key staying in the key set. Without it the new key waits for Lead, and this node signs
	// nothing meanwhile.
	AtOnce bool
	// Confirm holds activation until the host confirms the key published (ConfirmPublished), and Lead counts from that confirmation.
	// It cannot go with AtOnce.
	Confirm bool
}

// slots are the ring's slots, its own name when none is named.
func (r *Ring) slots(name string) []string {
	if len(r.Slots) == 0 {
		return []string{name}
	}
	return r.Slots
}

// current is the current slot.
func (r *Ring) current(name string) string {
	if r.Current == "" {
		return r.slots(name)[0]
	}
	return r.Current
}

// seconds is a duration as whole seconds, rounded up.
func seconds(d time.Duration) int64 { return int64((d + time.Second - 1) / time.Second) }

// Op is what a call asks of the block, as the rules see it.
type Op string

// The operations.
const (
	OpSign   Op = "sign"
	OpKeySet Op = "key_set"
	OpVerify Op = "verify"
	// The calls of a ring (record 0024).
	OpRing      Op = "ring"
	OpReconcile Op = "reconcile"
	OpRevoke    Op = "revoke"
	OpConfirm   Op = "confirm_published"
)

// Request is what the rules decide on. The block has checked the form of every field before it asks.
type Request struct {
	Op      Op
	Purpose string // OpSign, OpRevoke, OpConfirm: the purpose
	Kid     string // OpRevoke: the kid
	Typ     string // OpSign: the typ header
	// Claims is the payload of OpSign, one strict JSON object, as it will be signed. A host that checks its claims in its own code
	// before the call (sluis does, in Rust) needs no rule here.
	Claims []byte
	// Authorization is the caller's opaque proof; the block never reads it, and hands it to the seal block's Sign too.
	Authorization []byte
}

// Rules is the host's rules hook (record 0008). It is asked before every call; an error refuses it: a *connect.Error goes to the caller
// as it is, any other error is permission_denied with the reason rule. The caller is the host's to find in ctx.
type Rules interface {
	Decide(ctx context.Context, r Request) error
}

// RulesFunc is a function as Rules.
type RulesFunc func(ctx context.Context, r Request) error

// Decide calls f.
func (f RulesFunc) Decide(ctx context.Context, r Request) error { return f(ctx, r) }

// AllowAll allows every call: for tests, and for a host whose callers the socket guard or the client it hands out already decides.
func AllowAll() Rules { return RulesFunc(func(context.Context, Request) error { return nil }) }

// Event is a fact the block reports to the host's Observe. It names no claim and no key.
type Event struct {
	Action  string // EventSign, EventRing or EventRefuse
	Op      Op
	Purpose string
	Kid     string // EventSign: the kid of the token; EventRing: the key that changed
	Reason  string // EventRefuse: the refusal's reason; EventRing: ReasonShortcut when the key skipped its lead
	Change  string // EventRing: what happened to the key (ChangeJoined and the rest)
	State   string // EventRing: the state the key is in after it
}

// The actions of an Event.
const (
	EventSign   = "token.sign"
	EventRing   = "token.ring"
	EventRefuse = "refusal"
)

// What happened to a key of a ring, in an EventRing and a RingChange.
const (
	ChangeJoined    = "joined"
	ChangeActivated = "activated"
	ChangeRetired   = "retired"
	ChangeDropped   = "dropped"
	ChangeForgotten = "forgotten" // a tombstone left the ring, beyond MaxDropped: a driver that presents the key again adds it as new
	ChangeRevoked   = "revoked"
	ChangeConfirmed = "confirmed"
)

// ReasonShortcut is the reason of a key that became active before its lead: no key of the ring was active (or the first deploy, or a
// restart rotation with AtOnce).
const ReasonShortcut = "shortcut"

// Options is what a host gives the block.
type Options struct {
	// Seal is a client of the seal block that holds the purposes' keys (in its process through socket.InProcess, or on a socket).
	Seal sealv1alpha1connect.SealServiceClient
	// Purposes is the host's declaration.
	Purposes []Purpose
	// Rules is the host's rules hook. It is required: use AllowAll for a test.
	Rules Rules
	// Observe receives the block's facts; none when nil. It is called on the path of the call, so it must be quick.
	Observe func(context.Context, Event)
	// Items is a client of the store port that holds the rings (record 0024); required when a purpose has a ring, unused otherwise.
	Items storev1alpha1connect.ItemServiceClient
	// RingPrefix is the prefix of the rings' keys in the store; DefaultRingPrefix when empty. A ring is the item RingPrefix + purpose.
	RingPrefix string
	// Node is the node of this instance: the holder of the keys in its slots, as the host names it. The replicas that share a key share
	// a node. The block compares it and never reads it.
	Node string
	// Clock is the time; time.Now when nil.
	Clock func() time.Time
}

// DefaultRingPrefix is the prefix of the rings' keys when the host names none.
const DefaultRingPrefix = "token-ring/"

var typPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_-]{0,63}$`)

func (o Options) check() error {
	var problems []error
	add := func(format string, args ...any) { problems = append(problems, fmt.Errorf(format, args...)) }
	if o.Seal == nil {
		add("Seal: no client of the seal block")
	}
	if o.Rules == nil {
		add("Rules: the host gives the rules (AllowAll for a test)")
	}
	problems = append(problems, checkPurposes(o.Purposes)...)
	rings := slices.ContainsFunc(o.Purposes, func(p Purpose) bool { return p.Ring != nil })
	if rings && o.Items == nil {
		add("Items: a purpose has a ring, and there is no client of the store port")
	}
	if o.RingPrefix != "" && !validKey(o.RingPrefix) {
		add("RingPrefix: not a key prefix of the store port (printable ASCII, no space)")
	}
	if len(o.Node) > MaxKid || (o.Node != "" && !validKid(o.Node)) {
		add("Node: 1 to 512 printable ASCII characters, or empty")
	}
	if len(problems) > 0 {
		return fmt.Errorf("token: the options: %w", errors.Join(problems...))
	}
	return nil
}

// checkPurposes names every problem of a declaration, never a value.
func checkPurposes(purposes []Purpose) []error {
	var problems []error
	add := func(format string, args ...any) { problems = append(problems, fmt.Errorf(format, args...)) }
	if len(purposes) == 0 {
		add("Purposes: the host declared no purpose")
	}
	seen := map[string]bool{}
	for i, p := range purposes {
		where := fmt.Sprintf("Purposes[%d]", i)
		if p.Name == "" || len(p.Name) > purpose.MaxName {
			add("%s.Name: not a purpose name", where)
		} else if seen[p.Name] {
			add("%s.Name: declared twice", where)
		}
		seen[p.Name] = true
		if len(p.Types) == 0 {
			add("%s.Types: no type", where)
		}
		for j, t := range p.Types {
			if !typPattern.MatchString(t) {
				add("%s.Types[%d]: not a type", where, j)
			} else if slices.Index(p.Types, t) != j {
				add("%s.Types[%d]: named twice", where, j)
			}
		}
		if p.Kid != KidKeyID && p.Kid != KidThumbprint {
			add("%s.Kid: KidKeyID or KidThumbprint", where)
		}
		if p.Algorithm != "" && purpose.DigestLen(p.Algorithm) == 0 {
			add("%s.Algorithm: ES384, ES256 or RS256", where)
		}
		if p.Ring != nil {
			problems = append(problems, checkRing(where+".Ring", p)...)
		}
	}
	held := map[string]string{}
	for _, p := range purposes {
		for _, s := range slotsOf(p) {
			if other, ok := held[s]; ok && other != p.Name {
				add("the slot %q is held by two purposes", s)
			}
			held[s] = p.Name
		}
	}
	return problems
}

// slotsOf are the seal purposes of a purpose: its slots, or its own name.
func slotsOf(p Purpose) []string {
	if p.Ring == nil {
		return []string{p.Name}
	}
	return p.Ring.slots(p.Name)
}

func checkRing(where string, p Purpose) []error {
	var problems []error
	add := func(format string, args ...any) { problems = append(problems, fmt.Errorf(format, args...)) }
	r := p.Ring
	for i, s := range r.Slots {
		if s == "" || len(s) > purpose.MaxName {
			add("%s.Slots[%d]: not a purpose name", where, i)
		} else if slices.Index(r.Slots, s) != i {
			add("%s.Slots[%d]: named twice", where, i)
		}
	}
	if r.Current != "" && !slices.Contains(r.slots(p.Name), r.Current) {
		add("%s.Current: not one of its slots", where)
	}
	if r.CacheMaxAge <= 0 || r.TokenLifetime <= 0 {
		add("%s: CacheMaxAge and TokenLifetime are the host's figures, more than 0", where)
	}
	if r.Lead < r.CacheMaxAge {
		add("%s.Lead: less than CacheMaxAge, so a verifier could meet a kid its cached key set does not have", where)
	}
	if r.Retain < r.TokenLifetime {
		add("%s.Retain: less than TokenLifetime, so a token could outlive its key in the key set", where)
	}
	if r.Bootstrap < 0 || r.Overlap < 0 {
		add("%s: Bootstrap and Overlap are 0 or more", where)
	}
	if r.AtOnce && r.Confirm {
		add("%s: AtOnce and Confirm do not go together (a key that signs at once cannot wait for a confirmation)", where)
	}
	return problems
}

// validKey is a key prefix of the store port: printable ASCII without a space, at most 256 bytes.
func validKey(s string) bool {
	return len(s) <= 256 && !strings.ContainsFunc(s, func(r rune) bool { return r <= 0x20 || r > 0x7e })
}

// SealPurposes is what a host that hosts the seal block for the token block declares to it: a signing purpose for each purpose (for a
// ring, one for each of its slots), with its algorithm, which each purpose must then name. The host adds its own seal purposes, and starts its key driver with seal.KeyPurposes of
// the whole list.
func SealPurposes(purposes []Purpose) ([]seal.Purpose, error) {
	out := make([]seal.Purpose, 0, len(purposes))
	for _, p := range purposes {
		if purpose.DigestLen(p.Algorithm) == 0 {
			return nil, fmt.Errorf("token: the purpose %q names no algorithm (ES384, ES256 or RS256)", p.Name)
		}
		for _, s := range slotsOf(p) {
			out = append(out, seal.Purpose{Name: s, Use: seal.UseSign, Algorithm: p.Algorithm})
		}
	}
	return out, nil
}
