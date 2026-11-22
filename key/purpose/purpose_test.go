package purpose

import (
	"errors"
	"strings"
	"testing"
)

func TestAGoodDeclarationIsKept(t *testing.T) {
	s, err := New([]Declaration{
		{Name: "client-assertion", Algorithm: ES256, Operations: []Operation{Sign}},
		{Name: "identity-header", Algorithm: ES384, Operations: []Operation{Sign}},
		{Name: "agree", Algorithm: ECDHP384, Operations: []Operation{Agree}},
		{Name: "only-made", Algorithm: ES384},
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Len() != 4 || strings.Join(s.Names(), ",") != "agree,client-assertion,identity-header,only-made" {
		t.Fatalf("names: %v", s.Names())
	}
	d, ok := s.Lookup("client-assertion")
	if !ok || !d.Allows(Sign) || d.Allows(Agree) || d.Algorithm != ES256 {
		t.Fatalf("lookup: %+v %v", d, ok)
	}
	if d, _ := s.Lookup("only-made"); d.Allows(Sign) {
		t.Fatal("a purpose with no operation allows sign")
	}
	if _, ok := s.Lookup("other"); ok {
		t.Fatal("an undeclared name was found")
	}
	if _, ok := (*Set)(nil).Lookup("x"); ok {
		t.Fatal("a nil set holds a purpose")
	}
	infos := s.Infos()
	if len(infos) != 4 || infos[0].GetName() != "client-assertion" || infos[0].GetOperations()[0] != "sign" || len(infos[3].GetOperations()) != 0 {
		t.Fatalf("infos: %v", infos)
	}
	// The list is a copy.
	l := s.List()
	l[0].Operations[0] = Agree
	if d, _ := s.Lookup("client-assertion"); !d.Allows(Sign) {
		t.Fatal("List returned the set's own operations")
	}
}

func TestEveryProblemIsCollectedWithoutAValue(t *testing.T) {
	_, err := New([]Declaration{
		{Name: "Bad_Name", Algorithm: ES384, Operations: []Operation{Sign}},
		{Name: "dup", Algorithm: ES384},
		{Name: "dup", Algorithm: ES256},
		{Name: "no-alg", Algorithm: "PS256"},
		{Name: "twice", Algorithm: ES384, Operations: []Operation{Sign, Sign}},
		{Name: "wrong-op", Algorithm: ES384, Operations: []Operation{Agree}},
		{Name: "wrong-op-2", Algorithm: ECDHP384, Operations: []Operation{Sign}},
		{Name: "-lead", Algorithm: ES384},
		{Name: "", Algorithm: ES384},
		{Name: strings.Repeat("a", 65), Algorithm: ES384},
	})
	var pe *Error
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v", err)
	}
	got := err.Error()
	for _, want := range []string{
		"purposes[0].name: not a name", "purposes[2].name: declared twice", "purposes[3].algorithm: not ES384",
		"purposes[4].operations[1]: listed twice", "purposes[5].operations[0]: the algorithm cannot do it",
		"purposes[6].operations[0]: the algorithm cannot do it", "purposes[7].name", "purposes[8].name", "purposes[9].name",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in: %s", want, got)
		}
	}
	if strings.Contains(got, "Bad_Name") || strings.Contains(got, "no-alg") {
		t.Errorf("the refusal repeats a value: %s", got)
	}
	if _, err := New(nil); !errors.Is(err, ErrNone) {
		t.Errorf("an empty declaration: %v", err)
	}
	many := make([]Declaration, MaxPurposes+1)
	for i := range many {
		many[i] = Declaration{Name: "p" + strings.Repeat("a", i%5) + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676)), Algorithm: ES384}
	}
	if _, err := New(many); err == nil || !strings.Contains(err.Error(), "more than 256") {
		t.Errorf("too many: %v", err)
	}
}

func TestTheJSONFormRoundTripsAndRefusesWhatItDoesNotKnow(t *testing.T) {
	const good = `{"purposes":[{"name":"a","algorithm":"ES256","operations":["sign"],"slot":"90"},{"name":"b","algorithm":"ECDH-P384","operations":["agree"]}]}`
	s, err := Parse(strings.NewReader(good))
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := s.Lookup("a"); d.Slot != "90" {
		t.Fatalf("slot: %+v", d)
	}
	raw, err := Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(strings.NewReader(string(raw)))
	if err != nil || again.Len() != 2 {
		t.Fatalf("round trip: %v", err)
	}
	for name, in := range map[string]string{
		"empty":         ``,
		"syntax":        `{"purposes":`,
		"unknown field": `{"purposes":[{"name":"a","algorithm":"ES256","operation":["sign"]}]}`,
		"unknown top":   `{"purposes":[{"name":"a","algorithm":"ES256"}],"extra":1}`,
		"two documents": good + good,
		"wrong type":    `{"purposes":[{"name":5,"algorithm":"ES256"}]}`,
		"no purposes":   `{"purposes":[]}`,
		"a bad purpose": `{"purposes":[{"name":"A","algorithm":"ES256"}]}`,
	} {
		if _, err := Parse(strings.NewReader(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Parse(strings.NewReader(`{"purposes":[],"x":"` + strings.Repeat("a", MaxFile) + `"}`)); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Errorf("an oversize file: %v", err)
	}
}

// The reference is sluis's eleven purposes with the algorithm each had in the closed lists, and it is a valid declaration.
func TestTheReferenceIsSluisAsItWas(t *testing.T) {
	s, err := New(Reference())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"wrapping": ECDHP384, "token-signing": ES384, "audit-log": ES256, "pin-release": ES384, "workload-issuer": ES384,
		"x509-intermediate": ES384, "ssh-user": ES384, "ssh-host": ES384, "issuance-log": ES384, "release-signing": ES384,
		"token-signing-rs256": RS256,
	}
	if s.Len() != len(want) {
		t.Fatalf("%d purposes", s.Len())
	}
	for name, alg := range want {
		d, ok := s.Lookup(name)
		if !ok || d.Algorithm != alg {
			t.Errorf("%s: %+v %v", name, d, ok)
		}
		if alg == ECDHP384 != d.Allows(Agree) || alg != ECDHP384 != d.Allows(Sign) {
			t.Errorf("%s: operations %v", name, d.Operations)
		}
	}
	a, b := Reference(), Reference()
	a[0].Name = "changed"
	if b[0].Name == "changed" {
		t.Error("Reference shares its slice")
	}
}

func TestDigestLen(t *testing.T) {
	for alg, n := range map[string]int{ES384: 48, ES256: 32, RS256: 32, ECDHP384: 0, "x": 0} {
		if DigestLen(alg) != n {
			t.Errorf("%s: %d", alg, DigestLen(alg))
		}
	}
}
