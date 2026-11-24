package policy

import (
	"fmt"
	"slices"
	"sort"
)

// The classes of a Fault: what part of a bundle failed. The decision test set compares the class and the id, never the text of the
// rule, which is each engine's own.
const (
	// ClassPayload: the payload is not a bundle (not a JSON object of the members of record 0019, a bad or duplicate id).
	ClassPayload = "payload"
	// ClassSchema: the schema does not parse.
	ClassSchema = "schema"
	// ClassPolicy: a policy does not parse, or is not one static policy.
	ClassPolicy = "policy"
	// ClassTemplate: a template does not parse, or is not one template.
	ClassTemplate = "template"
	// ClassLink: a link names no template, or its values do not fill the template's slots.
	ClassLink = "link"
	// ClassValidation: a policy or a linked template fails strict validation against the schema.
	ClassValidation = "validation"
	// ClassUnsupported: the bundle uses what this engine cannot do (cedar-go has no templates: a known gap).
	ClassUnsupported = "unsupported"
)

// Fault is why a bundle was refused as a whole: the class, the id of the policy, template or link at fault (or the member of the
// payload), and the rule. It names an id or a member and never quotes the text.
type Fault struct {
	Class string
	ID    string
	Rule  string
}

func (f *Fault) Error() string {
	if f.ID == "" {
		return fmt.Sprintf("policy: bundle: %s: %s", f.Class, f.Rule)
	}
	return fmt.Sprintf("policy: bundle: %s %s: %s", f.Class, f.ID, f.Rule)
}

func fault(class, id, rule string) error { return &Fault{Class: class, ID: id, Rule: rule} }

// EntityRef is an entity named by its type and id, as a link fills a slot with it.
type EntityRef struct {
	Type string
	ID   string
}

// Link is a template link: the id of the linked policy, the template it links, and the entity of each slot (`?principal`,
// `?resource`).
type Link struct {
	ID       string
	Template string
	Values   map[string]EntityRef
}

// Bundle is a payload read but not compiled: the text of the schema, of each policy and template by id, and the links.
type Bundle struct {
	Schema    string
	Policies  map[string]string
	Templates map[string]string
	Links     []Link
}

// maxID is the longest id of a policy, a template or a link.
const maxID = 128

// validID: 1 to 128 characters, a letter or a digit first, then letters, digits and `.`, `_`, `-`, `:`, `/`.
func validID(s string) bool {
	if s == "" || len(s) > maxID {
		return false
	}
	for i := range len(s) {
		c := s[i]
		alnum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !alnum && (i == 0 || (c != '.' && c != '_' && c != '-' && c != ':' && c != '/')) {
			return false
		}
	}
	return true
}

// ParseBundle reads the payload of a verified document (record 0019, "The payload"). It checks the shape and the ids and parses no
// Cedar: that is Compile's.
func ParseBundle(payload []byte) (*Bundle, error) {
	top, ok := parseObject(payload)
	if !ok {
		return nil, fault(ClassPayload, "", "not a JSON object")
	}
	if !top.only("schema", "policies", "templates", "links") {
		return nil, fault(ClassPayload, "", "a member that is not schema, policies, templates or links")
	}
	b := &Bundle{Policies: map[string]string{}, Templates: map[string]string{}}
	schema, ok := top.str("schema")
	if !ok {
		return nil, fault(ClassPayload, "schema", "needs the schema, a string")
	}
	b.Schema = schema
	if _, present := top.get("policies"); !present {
		return nil, fault(ClassPayload, "policies", "needs the policies, an object")
	}
	if err := texts(top, "policies", b.Policies); err != nil {
		return nil, err
	}
	if _, present := top.get("templates"); present {
		if err := texts(top, "templates", b.Templates); err != nil {
			return nil, err
		}
	}
	if v, present := top.get("links"); present {
		arr, isArr := v.([]any)
		if !isArr {
			return nil, fault(ClassPayload, "links", "links is a list")
		}
		for _, x := range arr {
			l, err := readLink(x)
			if err != nil {
				return nil, err
			}
			b.Links = append(b.Links, l)
		}
	}
	// One name space for policies, templates and links, as Cedar has: the first id seen twice, in the order policies, templates (each
	// sorted), links (as listed).
	seen := map[string]bool{}
	ids := append(sortedKeys(b.Policies), sortedKeys(b.Templates)...)
	for _, l := range b.Links {
		ids = append(ids, l.ID)
	}
	for _, id := range ids {
		if seen[id] {
			return nil, fault(ClassPayload, id, "an id is used twice")
		}
		seen[id] = true
	}
	return b, nil
}

func texts(top *object, member string, into map[string]string) error {
	v, _ := top.get(member)
	o, ok := v.(*object)
	if !ok {
		return fault(ClassPayload, member, member+" is an object of id to text")
	}
	for i, id := range o.keys {
		text, isStr := o.vals[i].(string)
		if !validID(id) || !isStr {
			return fault(ClassPayload, member, member+" is an object of id to text, each id of the form of record 0019")
		}
		into[id] = text
	}
	return nil
}

func readLink(x any) (Link, error) {
	o, ok := x.(*object)
	if !ok || !o.exactly("id", "template", "values") {
		return Link{}, fault(ClassPayload, "links", "a link has id, template and values, and no other member")
	}
	id, ok1 := o.str("id")
	tpl, ok2 := o.str("template")
	vals, _ := o.get("values")
	vo, ok3 := vals.(*object)
	if !ok1 || !ok2 || !ok3 || !validID(id) || !validID(tpl) {
		return Link{}, fault(ClassPayload, "links", "a link has id, template and values, and no other member")
	}
	l := Link{ID: id, Template: tpl, Values: map[string]EntityRef{}}
	for i, slot := range vo.keys {
		e, isObj := vo.vals[i].(*object)
		if (slot != "?principal" && slot != "?resource") || !isObj || !e.exactly("type", "id") {
			return Link{}, fault(ClassPayload, "links", "the values of a link map ?principal and ?resource to an entity of type and id")
		}
		t, okT := e.str("type")
		eid, okI := e.str("id")
		if !okT || !okI {
			return Link{}, fault(ClassPayload, "links", "the values of a link map ?principal and ?resource to an entity of type and id")
		}
		l.Values[slot] = EntityRef{Type: t, ID: eid}
	}
	return l, nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return slices.Clip(out)
}
