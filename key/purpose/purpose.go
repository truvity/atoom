// Package purpose is the host's declaration of the purposes of a key store (decisions 0008 and 0013; docs/key-port.md, "The contract").
//
// A purpose is a name that the host gives to a key. The key port holds no list of them: the host declares each one it uses, with the
// algorithm of its key and the operations it allows, and gives the declaration to the driver it starts. The driver serves those names
// and refuses every other (reason purpose). A purpose keeps its algorithm for life, so a declaration that changes an algorithm is
// refused by the driver when it meets the key that was made under the old one.
//
// The declaration is a Go value (Declaration, checked by New) or a JSON file (Parse, the form of the thin mains' --purposes flag):
//
//	{"purposes": [
//	  {"name": "client-assertion", "algorithm": "ES256", "operations": ["sign"]},
//	  {"name": "identity-header",  "algorithm": "ES384", "operations": ["sign"]},
//	  {"name": "wrapping",         "algorithm": "ECDH-P384", "operations": ["agree"]}
//	]}
//
// Reference is the declaration of sluis, the first host, for a host that wants today's purposes as they were; no driver holds it.
package purpose

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
)

// The algorithms of the key port (docs/algorithms.md).
const (
	ES384    = "ES384"     // ECDSA over P-384 with SHA-384 (RFC 7518, section 3.4)
	ES256    = "ES256"     // ECDSA over P-256 with SHA-256 (RFC 7518, section 3.4)
	RS256    = "RS256"     // RSASSA-PKCS1-v1_5 over SHA-256 with an RSA-2048 key (RFC 7518, section 3.3)
	ECDHP384 = "ECDH-P384" // ECDH over P-384 (NIST SP 800-56A)
)

// Operation is what a host allows the callers of a purpose to do with its key. Ensure and PublicKey are allowed for every declared
// purpose, and are not operations: a key that no operation allows is made and read and never used (the PIN release key of a card).
type Operation string

// The operations.
const (
	Sign  Operation = "sign"  // Sign: an ECDSA or RSA signature over a digest (ES384, ES256, RS256)
	Agree Operation = "agree" // Ecdh: key agreement with a peer's public key (ECDH-P384)
)

// Declaration is one purpose of a host.
type Declaration struct {
	// Name is the purpose: 1 to 64 characters, lower-case letters, digits and '-', not beginning or ending with '-'. It becomes a file
	// name (the software driver) and a part of a KMS alias (awskms), so nothing else is allowed.
	Name string `json:"name"`
	// Algorithm is the algorithm of the key: ES384, ES256, RS256 or ECDH-P384. A driver that cannot hold it refuses the declaration.
	Algorithm string `json:"algorithm"`
	// Operations are the operations the host allows: sign for ES384, ES256 and RS256, agree for ECDH-P384, or none.
	Operations []Operation `json:"operations"`
	// Slot is the PIV slot of the purpose on a card, two lower-case hex digits. The card driver requires it (record 0021: no slot is
	// taken from a reference map), except for pin-release, whose slot is the PIN release's own, 87; the others ignore it.
	Slot string `json:"slot,omitempty"`
}

// Allows says whether the declaration allows the operation.
func (d Declaration) Allows(op Operation) bool { return slices.Contains(d.Operations, op) }

// DigestLen is the length in octets of the digest that Sign takes for the algorithm: 48 for ES384, 32 for ES256 and RS256, and 0 for a
// key that does not sign.
func DigestLen(algorithm string) int {
	switch algorithm {
	case ES384:
		return 48
	case ES256, RS256:
		return 32
	}
	return 0
}

// Info is the declaration as Capabilities says it.
func (d Declaration) Info() *keyv1alpha1.PurposeInfo {
	ops := make([]string, len(d.Operations))
	for i, op := range d.Operations {
		ops[i] = string(op)
	}
	return &keyv1alpha1.PurposeInfo{Name: d.Name, Algorithm: d.Algorithm, Operations: ops}
}

// Limits of a declaration.
const (
	MaxPurposes = 256
	MaxName     = 64
)

var namePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// Set is a checked declaration: the purposes of a host, by name. The zero Set is empty. It is read-only and safe for concurrent use.
type Set struct {
	list   []Declaration
	byName map[string]int
}

// ErrNone is a declaration with no purpose: a driver with nothing to serve is a mistake of the host, not a driver.
var ErrNone = errors.New("purpose: the host declared no purpose")

// New checks a declaration and returns the set. Every problem is collected, each as a path and a word (never a value): a name that is not
// one, a duplicate, an algorithm that is not the port's, operations that the algorithm cannot do or that are listed twice. A list with
// no purpose is ErrNone. The operations are copied and sorted, so that a set reads the same however the host wrote it.
func New(decls []Declaration) (*Set, error) {
	if len(decls) == 0 {
		return nil, ErrNone
	}
	var problems []string
	if len(decls) > MaxPurposes {
		problems = append(problems, fmt.Sprintf("purposes: more than %d", MaxPurposes))
	}
	s := &Set{byName: make(map[string]int, len(decls))}
	for i, d := range decls {
		where := fmt.Sprintf("purposes[%d]", i)
		if len(d.Name) > MaxName || !namePattern.MatchString(d.Name) {
			problems = append(problems, where+".name: not a name (1 to 64 of a-z, 0-9 and '-', no '-' at either end)")
		} else if _, dup := s.byName[d.Name]; dup {
			problems = append(problems, where+".name: declared twice")
		}
		problems = append(problems, checkAlgorithm(where, d)...)
		d.Operations = slices.Clone(d.Operations)
		slices.Sort(d.Operations)
		s.byName[d.Name] = len(s.list)
		s.list = append(s.list, d)
	}
	if len(problems) > 0 {
		return nil, &Error{Problems: problems}
	}
	return s, nil
}

func checkAlgorithm(where string, d Declaration) []string {
	var allowed []Operation
	switch d.Algorithm {
	case ES384, ES256, RS256:
		allowed = []Operation{Sign}
	case ECDHP384:
		allowed = []Operation{Agree}
	default:
		return []string{where + ".algorithm: not ES384, ES256, RS256 or ECDH-P384"}
	}
	var problems []string
	seen := map[Operation]bool{}
	for j, op := range d.Operations {
		switch {
		case !slices.Contains(allowed, op):
			problems = append(problems, fmt.Sprintf("%s.operations[%d]: the algorithm cannot do it (it allows %s)", where, j, allowed[0]))
		case seen[op]:
			problems = append(problems, fmt.Sprintf("%s.operations[%d]: listed twice", where, j))
		}
		seen[op] = true
	}
	return problems
}

// Error is a declaration that New or Parse refuses.
type Error struct{ Problems []string }

func (e *Error) Error() string { return "purposes refused: " + strings.Join(e.Problems, "; ") }

// MustNew is New for a declaration written in code (a test, a program's own table): it panics on a refusal.
func MustNew(decls []Declaration) *Set {
	s, err := New(decls)
	if err != nil {
		panic(err)
	}
	return s
}

// Lookup returns the declaration of a name, and false when the host declared none.
func (s *Set) Lookup(name string) (Declaration, bool) {
	if s == nil {
		return Declaration{}, false
	}
	i, ok := s.byName[name]
	if !ok {
		return Declaration{}, false
	}
	return s.list[i], true
}

// List returns the declarations in the order the host gave them, as a copy.
func (s *Set) List() []Declaration {
	if s == nil {
		return nil
	}
	out := make([]Declaration, len(s.list))
	for i, d := range s.list {
		d.Operations = slices.Clone(d.Operations)
		out[i] = d
	}
	return out
}

// Names returns the declared names, sorted.
func (s *Set) Names() []string {
	if s == nil {
		return nil
	}
	out := make([]string, 0, len(s.list))
	for _, d := range s.list {
		out = append(out, d.Name)
	}
	slices.Sort(out)
	return out
}

// Len is the number of purposes.
func (s *Set) Len() int {
	if s == nil {
		return 0
	}
	return len(s.list)
}

// Infos are the declarations as Capabilities says them.
func (s *Set) Infos() []*keyv1alpha1.PurposeInfo {
	if s == nil {
		return nil
	}
	out := make([]*keyv1alpha1.PurposeInfo, len(s.list))
	for i, d := range s.list {
		out[i] = d.Info()
	}
	return out
}

// MaxFile is the largest declaration file that Parse reads.
const MaxFile = 1 << 20

// file is the JSON form of a declaration.
type file struct {
	Purposes []Declaration `json:"purposes"`
}

// Parse reads a declaration from JSON (the form in the package comment) and checks it with New. An unknown field, a second document and
// a file over MaxFile are refused.
func Parse(r io.Reader) (*Set, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxFile+1))
	if err != nil {
		return nil, fmt.Errorf("purpose: read: %w", err)
	}
	if len(raw) > MaxFile {
		return nil, &Error{Problems: []string{"file: larger than 1 MiB"}}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var f file
	if err := dec.Decode(&f); err != nil {
		return nil, &Error{Problems: []string{"file: not a declaration of purposes (" + jsonWord(err) + ")"}}
	}
	if dec.More() {
		return nil, &Error{Problems: []string{"file: more than one document"}}
	}
	return New(f.Purposes)
}

// jsonWord names what is wrong with a JSON error without repeating any value of the file.
func jsonWord(err error) string {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syn):
		return "syntax"
	case errors.As(err, &typ):
		return "type of " + typ.Field
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "empty or cut short"
	}
	return "unknown field or shape"
}

// Marshal writes a set as the JSON that Parse reads.
func Marshal(s *Set) ([]byte, error) {
	f := file{Purposes: s.List()}
	for i := range f.Purposes {
		if f.Purposes[i].Operations == nil {
			f.Purposes[i].Operations = []Operation{}
		}
	}
	return json.MarshalIndent(f, "", "  ")
}
