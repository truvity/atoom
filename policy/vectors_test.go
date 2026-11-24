package policy

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/cedar-policy/cedar-go/types"
)

// The decision test set (record 0019): the same files run by the Rust crate (tests/vectors.rs). A case whose engine differs names the
// gap; the Go verdict is then expect_go, so that a gap that closes fails here until the file says so.

type signedVectors struct {
	Kind          string `json:"kind"`
	PublisherSets map[string][]struct {
		KID     string   `json:"kid"`
		SPKIDER string   `json:"spki_der"`
		CoSigns []string `json:"co_signs"`
	} `json:"publisher_sets"`
	Cases []struct {
		Name        string `json:"name"`
		Document    string `json:"document"`
		DocumentB64 string `json:"document_base64"`
		Publishers  string `json:"publishers"`
		RefusedStep int    `json:"refused_step"`
		Read        *struct {
			Seq     uint64 `json:"seq"`
			SHA384  string `json:"sha384"`
			Payload string `json:"payload"`
		} `json:"read"`
	} `json:"cases"`
	Sequence []struct {
		Name    string `json:"name"`
		InForce *held  `json:"in_force"`
		Doc     held   `json:"document"`
		Verdict string `json:"verdict"`
	} `json:"sequence"`
}

type held struct {
	Seq    uint64 `json:"seq"`
	SHA384 string `json:"sha384"`
}

func readJSON(t *testing.T, file string, into any) {
	t.Helper()
	raw, err := os.ReadFile(file) // #nosec G304 -- a test vector of this package
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
}

func digest48(t *testing.T, h string) [48]byte {
	t.Helper()
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != 48 {
		t.Fatalf("not a SHA-384: %q", h)
	}
	return [48]byte(b)
}

func TestSignedDocumentVectors(t *testing.T) {
	var v signedVectors
	readJSON(t, "testdata/signed-document.json", &v)
	sets := map[string][]Publisher{}
	for name, ps := range v.PublisherSets {
		for _, p := range ps {
			der, err := base64.StdEncoding.DecodeString(p.SPKIDER)
			if err != nil {
				t.Fatal(err)
			}
			pub, err := PublisherFromSPKI(p.KID, der, p.CoSigns...)
			if err != nil {
				t.Fatal(err)
			}
			sets[name] = append(sets[name], pub)
		}
	}
	if len(v.Cases) < 60 {
		t.Fatalf("only %d cases", len(v.Cases))
	}
	for _, c := range v.Cases {
		t.Run(c.Name, func(t *testing.T) {
			doc := []byte(c.Document)
			if c.DocumentB64 != "" {
				var err error
				if doc, err = base64.StdEncoding.DecodeString(c.DocumentB64); err != nil {
					t.Fatal(err)
				}
			}
			ps, ok := sets[c.Publishers]
			if !ok {
				t.Fatalf("no publisher set %q", c.Publishers)
			}
			got, err := VerifyDocument(doc, v.Kind, ps)
			if c.Read != nil {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if got.Seq != c.Read.Seq || got.DigestHex() != c.Read.SHA384 || string(got.Payload) != c.Read.Payload {
					t.Fatalf("read seq %d sha384 %s payload %q", got.Seq, got.DigestHex(), got.Payload)
				}
				return
			}
			var r *Refused
			if !errors.As(err, &r) {
				t.Fatalf("want a refusal at step %d, got %v", c.RefusedStep, err)
			}
			if r.Step != c.RefusedStep {
				t.Fatalf("refused at step %d (%s), want step %d", r.Step, r.Rule, c.RefusedStep)
			}
		})
	}
	for _, c := range v.Sequence {
		t.Run("sequence/"+c.Name, func(t *testing.T) {
			doc := &Verified{Seq: c.Doc.Seq, Digest: digest48(t, c.Doc.SHA384)}
			var got Sequence
			if c.InForce == nil {
				got = Compare(false, 0, [48]byte{}, doc)
			} else {
				got = Compare(true, c.InForce.Seq, digest48(t, c.InForce.SHA384), doc)
			}
			if got.String() != c.Verdict {
				t.Fatalf("got %s, want %s", got, c.Verdict)
			}
		})
	}
}

type verdict struct {
	Loaded  []string `json:"loaded"`
	Refused *struct {
		Class string `json:"class"`
		ID    string `json:"id"`
	} `json:"refused"`
}

type bundleVectors struct {
	Cases []struct {
		Name        string          `json:"name"`
		Bundle      json.RawMessage `json:"bundle"`
		PayloadText *string         `json:"payload_text"`
		Expect      verdict         `json:"expect"`
		ExpectGo    *verdict        `json:"expect_go"`
		Gap         string          `json:"gap"`
	} `json:"cases"`
}

func compileText(payload []byte) (*Set, error) {
	b, err := ParseBundle(payload)
	if err != nil {
		return nil, err
	}
	return Compile(b)
}

func TestBundleVectors(t *testing.T) {
	var v bundleVectors
	readJSON(t, "testdata/bundles.json", &v)
	gaps := 0
	for _, c := range v.Cases {
		t.Run(c.Name, func(t *testing.T) {
			payload := []byte(c.Bundle)
			if c.PayloadText != nil {
				payload = []byte(*c.PayloadText)
			}
			want := c.Expect
			if c.ExpectGo != nil {
				if c.Gap == "" {
					t.Fatal("expect_go without a gap")
				}
				want = *c.ExpectGo
				gaps++
			}
			set, err := compileText(payload)
			if want.Refused == nil {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				ids := set.PolicyIDs()
				if !slices.Equal(ids, want.Loaded) && (len(ids) != 0 || len(want.Loaded) != 0) {
					t.Fatalf("loaded %v, want %v", ids, want.Loaded)
				}
				return
			}
			var f *Fault
			if !errors.As(err, &f) {
				t.Fatalf("want the fault %s %q, got %v", want.Refused.Class, want.Refused.ID, err)
			}
			if f.Class != want.Refused.Class || f.ID != want.Refused.ID {
				t.Fatalf("fault %s %q (%s), want %s %q", f.Class, f.ID, f.Rule, want.Refused.Class, want.Refused.ID)
			}
		})
	}
	if gaps == 0 {
		t.Fatal("no gap of cedar-go is listed: the file and this test disagree")
	}
}

type decisionVectors struct {
	Bundles map[string]json.RawMessage   `json:"bundles"`
	Gaps    map[string]map[string]string `json:"gaps"`
	Cases   []struct {
		Name      string           `json:"name"`
		Bundle    string           `json:"bundle"`
		Principal entityJSON       `json:"principal"`
		Action    entityJSON       `json:"action"`
		Resource  entityJSON       `json:"resource"`
		Context   json.RawMessage  `json:"context"`
		Entities  json.RawMessage  `json:"entities"`
		Expect    decisionVerdict  `json:"expect"`
		ExpectGo  *decisionVerdict `json:"expect_go"`
		Gap       string           `json:"gap"`
	} `json:"cases"`
}

type decisionVerdict struct {
	Decision string   `json:"decision"`
	Reasons  []string `json:"reasons"`
	Errors   []string `json:"errors"`
	Misfit   bool     `json:"misfit"`
}

type entityJSON struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func (e entityJSON) uid() types.EntityUID {
	return types.NewEntityUID(types.EntityType(e.Type), types.String(e.ID))
}

func TestDecisionVectors(t *testing.T) {
	var v decisionVectors
	readJSON(t, "testdata/decisions.json", &v)
	sets := map[string]*Set{}
	for name, raw := range v.Bundles {
		set, err := compileText(raw)
		if gap := v.Gaps[name]["go"]; gap != "" {
			var f *Fault
			if !errors.As(err, &f) || f.Class != ClassUnsupported {
				t.Fatalf("bundle %s: the gap %q is listed, but cedar-go gives %v", name, gap, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("bundle %s: %v", name, err)
		}
		sets[name] = set
	}
	ran := 0
	for _, c := range v.Cases {
		set, ok := sets[c.Bundle]
		if !ok {
			if v.Gaps[c.Bundle]["go"] == "" {
				t.Fatalf("%s: no bundle %q", c.Name, c.Bundle)
			}
			t.Logf("SKIP %s: %s", c.Name, v.Gaps[c.Bundle]["go"])
			continue
		}
		ran++
		want := c.Expect
		if c.ExpectGo != nil {
			if c.Gap == "" {
				t.Fatalf("%s: expect_go without a gap", c.Name)
			}
			want = *c.ExpectGo
		}
		t.Run(c.Name, func(t *testing.T) {
			var ctx types.Record
			if err := json.Unmarshal(c.Context, &ctx); err != nil {
				t.Fatalf("context: %v", err)
			}
			var ents types.EntityMap
			if err := json.Unmarshal(c.Entities, &ents); err != nil {
				t.Fatalf("entities: %v", err)
			}
			d, err := set.Decide(Request{Principal: c.Principal.uid(), Action: c.Action.uid(), Resource: c.Resource.uid(), Context: ctx, Entities: ents})
			if d.Digest != set.Digest() {
				t.Fatal("the decision does not carry the digest of its set")
			}
			if want.Misfit {
				var m *Misfit
				if !errors.As(err, &m) || d.Allow {
					t.Fatalf("want a misfit and a denial, got %+v, %v", d, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("decide: %v", err)
			}
			got := "deny"
			if d.Allow {
				got = "allow"
			}
			if got != want.Decision || !equalIDs(d.Reasons, want.Reasons) || !equalIDs(d.Errors, want.Errors) {
				t.Fatalf("got %s reasons %v errors %v, want %s reasons %v errors %v", got, d.Reasons, d.Errors, want.Decision, want.Reasons, want.Errors)
			}
		})
	}
	if ran < 20 {
		t.Fatalf("only %d decision cases ran", ran)
	}
}

func equalIDs(a, b []string) bool { return len(a) == len(b) && (len(a) == 0 || slices.Equal(a, b)) }
