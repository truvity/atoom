package policy

import (
	"fmt"
	"sort"

	"github.com/cedar-policy/cedar-go"
	"github.com/cedar-policy/cedar-go/types"
	xast "github.com/cedar-policy/cedar-go/x/exp/ast"
	"github.com/cedar-policy/cedar-go/x/exp/schema"
	"github.com/cedar-policy/cedar-go/x/exp/schema/resolved"
	"github.com/cedar-policy/cedar-go/x/exp/schema/validate"
)

// Set is a compiled policy set: the schema, the policies checked against it in strict mode, and the digest and seq of the document it
// came from. A Set never changes; a reload builds a new one. Decide does no I/O.
type Set struct {
	digest    string
	seq       uint64
	schema    *resolved.Schema
	validator *validate.Validator
	policies  *cedar.PolicySet
	ids       []string
	actions   types.EntityMap
}

// Digest is the SHA-384 of the payload the set was built from, in lower-case hex: what each decision is audited with.
func (s *Set) Digest() string { return s.digest }

// Seq is the seq of the document the set was built from.
func (s *Set) Seq() uint64 { return s.seq }

// PolicyIDs are the ids of the policies of the set, sorted.
func (s *Set) PolicyIDs() []string { return append([]string(nil), s.ids...) }

// Schema is the resolved schema of the set, for a host that builds its entities with it.
func (s *Set) Schema() *resolved.Schema { return s.schema }

// CompileVerified reads the payload of a verified document and compiles it (ParseBundle, then Compile), with the document's digest and
// seq.
func CompileVerified(v *Verified) (*Set, error) {
	b, err := ParseBundle(v.Payload)
	if err != nil {
		return nil, err
	}
	s, err := Compile(b)
	if err != nil {
		return nil, err
	}
	s.digest, s.seq = v.DigestHex(), v.Seq
	return s, nil
}

// Compile parses the schema and every policy, and validates every policy against the schema in strict mode. Any fault refuses the
// whole bundle. The order of the checks is the record's: the schema, the policies (by id), the templates, the links, then validation.
//
// cedar-go has no templates: a bundle with a template or a link is refused with ClassUnsupported, a known gap of this engine that the
// decision test set lists.
func Compile(b *Bundle) (*Set, error) {
	var sc schema.Schema
	if err := sc.UnmarshalCedar([]byte(b.Schema)); err != nil {
		return nil, fault(ClassSchema, "", "the schema does not parse")
	}
	res, err := sc.Resolve()
	if err != nil {
		return nil, fault(ClassSchema, "", "the schema does not resolve")
	}
	ps := cedar.NewPolicySet()
	ids := sortedKeys(b.Policies)
	compiled := make([]*cedar.Policy, len(ids))
	for i, id := range ids {
		p := onePolicy(b.Policies[id])
		if p == nil {
			return nil, fault(ClassPolicy, id, "the policy does not parse as one static policy")
		}
		compiled[i] = p
		ps.Add(cedar.PolicyID(id), p)
	}
	if t := sortedKeys(b.Templates); len(t) > 0 {
		return nil, fault(ClassUnsupported, t[0], "cedar-go has no templates")
	}
	if len(b.Links) > 0 {
		return nil, fault(ClassUnsupported, b.Links[0].ID, "cedar-go has no templates")
	}
	v := validate.New(res, validate.WithStrict())
	for i, id := range ids {
		if err := v.Policy(id, (*xast.Policy)(compiled[i].AST())); err != nil {
			return nil, fault(ClassValidation, id, "the policy fails strict validation against the schema")
		}
	}
	actions := types.EntityMap{}
	for uid, a := range res.Actions {
		actions[uid] = a.Entity
	}
	return &Set{schema: res, validator: v, policies: ps, ids: ids, actions: actions}, nil
}

// onePolicy parses text as exactly one static policy, or returns nil. Policy.UnmarshalCedar reads the first statement and ignores the
// rest, so the text is read as a policy set, which must hold one.
func onePolicy(text string) *cedar.Policy {
	set, err := cedar.NewPolicySetFromBytes("", []byte(text))
	if err != nil {
		return nil
	}
	var only *cedar.Policy
	n := 0
	for _, p := range set.All() {
		only = p
		n++
	}
	if n != 1 {
		return nil
	}
	return only
}

// Request is what the caller gives for one decision: the principal, the action, the resource, the context and the entities the request
// touches (the principal, the resource, their ancestors). The engine fetches nothing (record 0011). The action entities come from the
// schema; the caller does not give them.
type Request struct {
	Principal types.EntityUID
	Action    types.EntityUID
	Resource  types.EntityUID
	Context   types.Record
	Entities  types.EntityMap
}

// Decision is what the set decided: allow or deny, the policies that determined it (sorted), the policies that errored (sorted; an
// erroring policy counts as neither permit nor forbid), and the digest and seq of the set, for the audit record.
type Decision struct {
	Allow   bool
	Reasons []string
	Errors  []string
	Digest  string
	Seq     uint64
}

// Misfit is a request or an entity that does not fit the schema. The decision is a denial, and the caller treats it as one.
type Misfit struct {
	Rule string
}

func (m *Misfit) Error() string { return "policy: the request does not fit the schema: " + m.Rule }

// Decide evaluates one request against the set. It validates the request and the entities against the schema first; a misfit is a
// *Misfit error with a denial. It does no I/O.
func (s *Set) Decide(r Request) (Decision, error) {
	d := Decision{Digest: s.digest, Seq: s.seq}
	req := cedar.Request{Principal: r.Principal, Action: r.Action, Resource: r.Resource, Context: r.Context}
	if err := s.validator.Request(req); err != nil {
		return d, &Misfit{Rule: "the request"}
	}
	for uid := range r.Entities {
		if _, isAction := s.actions[uid]; isAction {
			return d, &Misfit{Rule: "the caller gives an action entity"}
		}
	}
	if err := s.validator.Entities(r.Entities); err != nil {
		return d, &Misfit{Rule: "the entities"}
	}
	decision, diag := cedar.Authorize(s.policies, layered{r.Entities, s.actions}, req)
	d.Allow = decision == cedar.Allow
	for _, x := range diag.Reasons {
		d.Reasons = append(d.Reasons, string(x.PolicyID))
	}
	for _, x := range diag.Errors {
		d.Errors = append(d.Errors, string(x.PolicyID))
	}
	sort.Strings(d.Reasons)
	sort.Strings(d.Errors)
	return d, nil
}

// layered finds an entity among the caller's, then among the actions of the schema.
type layered struct {
	caller, actions types.EntityMap
}

func (l layered) Get(uid types.EntityUID) (types.Entity, bool) {
	if e, ok := l.caller[uid]; ok {
		return e, true
	}
	e, ok := l.actions[uid]
	return e, ok
}

// String names the set by its seq and digest.
func (s *Set) String() string { return fmt.Sprintf("policy set seq %d sha384 %s", s.seq, s.digest) }
