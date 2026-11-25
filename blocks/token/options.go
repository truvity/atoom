package token

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"

	"github.com/truvity/atoom/blocks/seal"
	"github.com/truvity/atoom/blocks/seal/gen/atoom/seal/v1alpha1/sealv1alpha1connect"
	tokenv1alpha1 "github.com/truvity/atoom/blocks/token/gen/atoom/token/v1alpha1"
	"github.com/truvity/atoom/key/purpose"
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
}

// Op is what a call asks of the block, as the rules see it.
type Op string

// The operations.
const (
	OpSign   Op = "sign"
	OpKeySet Op = "key_set"
	OpVerify Op = "verify"
)

// Request is what the rules decide on. The block has checked the form of every field before it asks.
type Request struct {
	Op      Op
	Purpose string // OpSign: the purpose
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
	Action  string // EventSign or EventRefuse
	Op      Op
	Purpose string
	Kid     string // EventSign: the kid of the token
	Reason  string // EventRefuse: the refusal's reason
}

// The actions of an Event.
const (
	EventSign   = "token.sign"
	EventRefuse = "refusal"
)

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
}

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
	}
	return problems
}

// SealPurposes is what a host that hosts the seal block for the token block declares to it: a signing purpose for each purpose, with its
// algorithm, which each purpose must then name. The host adds its own seal purposes, and starts its key driver with seal.KeyPurposes of
// the whole list.
func SealPurposes(purposes []Purpose) ([]seal.Purpose, error) {
	out := make([]seal.Purpose, 0, len(purposes))
	for _, p := range purposes {
		if purpose.DigestLen(p.Algorithm) == 0 {
			return nil, fmt.Errorf("token: the purpose %q names no algorithm (ES384, ES256 or RS256)", p.Name)
		}
		out = append(out, seal.Purpose{Name: p.Name, Use: seal.UseSign, Algorithm: p.Algorithm})
	}
	return out, nil
}
