package token

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// The declaration file of the thin main (--purposes): the purposes of the block as JSON, the twin of the key port's purpose file.
//
//	{"purposes": [
//	  {"name": "token-signing", "algorithm": "ES384", "types": ["JWT", "at+jwt"], "kid": "thumbprint"},
//	  {"name": "identity-header", "algorithm": "ES256", "types": ["session-identity+jwt"], "kid": "key_id",
//	   "ring": {"slots": ["identity-header-a", "identity-header-b"], "current": "identity-header-a",
//	            "lead": "10m", "retain": "15m", "cache_max_age": "5m", "token_lifetime": "6m", "overlap": "5m"}}
//	]}
//
// "kid" is "key_id" (the default when absent) or "thumbprint". "algorithm" is required when the thin main hosts the seal block itself,
// and checked against the seal block's when it is given one. "ring" is the purpose's rotation ring (record 0024), absent for none: its
// durations are Go durations ("90s", "30m", "1h5m"), and "bootstrap", "overlap", "at_once" and "confirm" may be left out.

type fileDeclaration struct {
	Purposes []filePurpose `json:"purposes"`
}

type filePurpose struct {
	Name      string    `json:"name"`
	Algorithm string    `json:"algorithm,omitempty"`
	Types     []string  `json:"types"`
	Kid       string    `json:"kid,omitempty"`
	Ring      *fileRing `json:"ring,omitempty"`
}

type fileRing struct {
	Slots         []string `json:"slots,omitempty"`
	Current       string   `json:"current,omitempty"`
	Lead          string   `json:"lead"`
	Retain        string   `json:"retain"`
	CacheMaxAge   string   `json:"cache_max_age"`
	TokenLifetime string   `json:"token_lifetime"`
	Bootstrap     string   `json:"bootstrap,omitempty"`
	Overlap       string   `json:"overlap,omitempty"`
	AtOnce        bool     `json:"at_once,omitempty"`
	Confirm       bool     `json:"confirm,omitempty"`
}

func (f *fileRing) ring(i int) (*Ring, error) {
	r := &Ring{Slots: f.Slots, Current: f.Current, AtOnce: f.AtOnce, Confirm: f.Confirm}
	for _, d := range []struct {
		name     string
		text     string
		to       *time.Duration
		optional bool
	}{
		{"lead", f.Lead, &r.Lead, false}, {"retain", f.Retain, &r.Retain, false}, {"cache_max_age", f.CacheMaxAge, &r.CacheMaxAge, false},
		{"token_lifetime", f.TokenLifetime, &r.TokenLifetime, false}, {"bootstrap", f.Bootstrap, &r.Bootstrap, true}, {"overlap", f.Overlap, &r.Overlap, true},
	} {
		if d.text == "" && d.optional {
			continue
		}
		v, err := time.ParseDuration(d.text)
		if err != nil {
			return nil, fmt.Errorf("token: the declaration: purposes[%d].ring.%s is not a duration", i, d.name)
		}
		*d.to = v
	}
	return r, nil
}

func durationText(d time.Duration) string {
	if d == 0 {
		return ""
	}
	return d.String()
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
		return nil, errors.New("token: the declaration is not {\"purposes\": [{\"name\", \"algorithm\", \"types\", \"kid\", \"ring\"}]}")
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
		var ring *Ring
		if p.Ring != nil {
			var err error
			if ring, err = p.Ring.ring(i); err != nil {
				return nil, err
			}
		}
		out = append(out, Purpose{Name: p.Name, Algorithm: p.Algorithm, Types: p.Types, Kid: k, Ring: ring})
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
		fp := filePurpose{Name: p.Name, Algorithm: p.Algorithm, Types: p.Types, Kid: p.Kid.String()}
		if r := p.Ring; r != nil {
			fp.Ring = &fileRing{
				Slots: r.Slots, Current: r.Current, Lead: r.Lead.String(), Retain: r.Retain.String(), CacheMaxAge: r.CacheMaxAge.String(),
				TokenLifetime: r.TokenLifetime.String(), Bootstrap: durationText(r.Bootstrap), Overlap: durationText(r.Overlap),
				AtOnce: r.AtOnce, Confirm: r.Confirm,
			}
		}
		f.Purposes = append(f.Purposes, fp)
	}
	out, _ := json.MarshalIndent(f, "", "  ") // strings and slices of strings
	return out
}
