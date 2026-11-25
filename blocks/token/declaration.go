package token

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// The declaration file of the thin main (--purposes): the purposes of the block as JSON, the twin of the key port's purpose file.
//
//	{"purposes": [
//	  {"name": "token-signing", "algorithm": "ES384", "types": ["JWT", "at+jwt"], "kid": "thumbprint"},
//	  {"name": "identity-header", "algorithm": "ES256", "types": ["session-identity+jwt"], "kid": "key_id"}
//	]}
//
// "kid" is "key_id" (the default when absent) or "thumbprint". "algorithm" is required when the thin main hosts the seal block itself,
// and checked against the seal block's when it is given one.

type fileDeclaration struct {
	Purposes []filePurpose `json:"purposes"`
}

type filePurpose struct {
	Name      string   `json:"name"`
	Algorithm string   `json:"algorithm,omitempty"`
	Types     []string `json:"types"`
	Kid       string   `json:"kid,omitempty"`
}

// MaxDeclaration is the largest declaration file that ParseDeclaration reads.
const MaxDeclaration = 1 << 20

// ParseDeclaration reads a declaration file: strict JSON, no unknown member, at most MaxDeclaration bytes. The purposes are checked as
// New checks them, apart from the seal block.
func ParseDeclaration(r io.Reader) ([]Purpose, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxDeclaration+1))
	if err != nil {
		return nil, errors.New("token: the declaration cannot be read")
	}
	if len(raw) > MaxDeclaration || strictObject(raw) != nil {
		return nil, errors.New("token: the declaration is not one strict JSON object of at most 1 MiB")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var f fileDeclaration
	if err := dec.Decode(&f); err != nil {
		return nil, errors.New("token: the declaration is not {\"purposes\": [{\"name\", \"algorithm\", \"types\", \"kid\"}]}")
	}
	out := make([]Purpose, 0, len(f.Purposes))
	for i, p := range f.Purposes {
		k := KidKeyID
		switch p.Kid {
		case "", "key_id":
		case "thumbprint":
			k = KidThumbprint
		default:
			return nil, fmt.Errorf("token: the declaration: purposes[%d].kid is key_id or thumbprint", i)
		}
		out = append(out, Purpose{Name: p.Name, Algorithm: p.Algorithm, Types: p.Types, Kid: k})
	}
	if problems := checkPurposes(out); len(problems) > 0 {
		return nil, fmt.Errorf("token: the declaration: %w", errors.Join(problems...))
	}
	return out, nil
}

// MarshalDeclaration writes purposes as a declaration file.
func MarshalDeclaration(purposes []Purpose) []byte {
	f := fileDeclaration{Purposes: []filePurpose{}}
	for _, p := range purposes {
		f.Purposes = append(f.Purposes, filePurpose{Name: p.Name, Algorithm: p.Algorithm, Types: p.Types, Kid: p.Kid.String()})
	}
	out, _ := json.MarshalIndent(f, "", "  ") // strings and slices of strings
	return out
}
