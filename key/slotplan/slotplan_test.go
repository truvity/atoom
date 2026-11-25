package slotplan

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

type vectorFile struct {
	Cases []struct {
		Name  string `json:"name"`
		Check []struct {
			Purpose   string `json:"purpose"`
			Algorithm string `json:"algorithm"`
			Slot      string `json:"slot"`
		} `json:"check"`
		Error string `json:"error"`
		Card  string `json:"card"`
	} `json:"cases"`
}

// verifies: REQ-PKI-035, REQ-PKI-037, REQ-PKI-038, REQ-PKI-042, REQ-KEYS-268, REQ-KEYS-279
// The vectors of the slot map check (testdata/vectors.json, shared with the other programs that take a slot map): two
// purposes in one slot (by name or by default) are refused, so are an enabled purpose with no slot, a slot that is no board
// slot, a purpose listed twice and the token-signing key in the intermediate's slot; a map without the slots of purposes that
// are not enabled is accepted. The card check adds the card driver's limit: no override of a slot, no purpose outside the
// map. The RS256 purpose is a card-key purpose like the others: it needs a slot and shares none. The purposes of version 2 take
// their own slots, 89 to 8f, by default and are refused on a card, whose driver has no row for them yet (an algorithm the
// check does not list skips every one of these). Since step B13 the card driver serves any purpose a host declares, so the card check
// refuses only an algorithm that a card does not hold (PS256 and EdDSA).
func TestTheSlotMapVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var f vectorFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) < 31 {
		t.Fatalf("%d vectors", len(f.Cases))
	}
	want := func(name, got string, err error) {
		t.Helper()
		switch {
		case got == "" && err != nil:
			t.Errorf("%s: refused: %v", name, err)
		case got != "" && (err == nil || !strings.Contains(err.Error(), got)):
			t.Errorf("%s: err = %v, want %q", name, err, got)
		}
	}
	for _, c := range f.Cases {
		var ps []Purpose
		for _, x := range c.Check {
			ps = append(ps, Purpose{x.Purpose, x.Algorithm, x.Slot})
		}
		ps = WithReference(ps)
		want(c.Name+" (check)", c.Error, Check(ps))
		if c.Error == "" {
			want(c.Name+" (card)", c.Card, CheckCard(ps))
		}
	}
}

// The default map is the reference map of sluis, version 2: the slots 89 to 8f hold their purposes, the rotation spares 90 to 95 hold
// none, and a card holds every row but the three of PS256 and EdDSA. That it is also the one in sluis's
// docs/architecture/keys.yaml is checked by sluis, which holds that file.
//
// verifies: REQ-KEYS-046, REQ-KEYS-278
func TestTheReferenceIsTheReferenceMap(t *testing.T) {
	if len(Reference) < 9 {
		t.Fatalf("the default map has %d slots", len(Reference))
	}
	// Version 2 changed only slots that version 1 left spare: the ten rows of version 1 are where they were.
	v1 := map[string]string{"x509-intermediate": "9c", "token-signing": "9a", "wrapping": "9d", "ssh-user": "82", "ssh-host": "83",
		"audit-log": "84", "release-signing": "85", "workload-issuer": "86", "pin-release": "87", "token-signing-rs256": "88"}
	for purpose, slot := range v1 {
		if Reference[purpose] != slot {
			t.Errorf("%s: version 1 had slot %s, the default has %q", purpose, slot, Reference[purpose])
		}
	}
	// The map holds nothing in the rotation spares 90 to 95.
	for purpose, slot := range Reference {
		if n, err := strconv.ParseUint(slot, 16, 8); err != nil || n >= 0x90 && n <= 0x95 {
			t.Errorf("%s: slot %s lies in the rotation spares", purpose, slot)
		}
		if !validSlot(slot) {
			t.Errorf("%s: slot %s is no board slot", purpose, slot)
		}
	}
	// What a card holds is part of the map: every row but the three of PS256 (8b) and EdDSA (8d, 8e), which a card driver does not hold.
	for purpose := range CardServed {
		if _, ok := Reference[purpose]; !ok {
			t.Errorf("the card serves %s, which the default map lacks", purpose)
		}
	}
	for purpose, slot := range Reference {
		if served, notServed := CardServed[purpose], slot == "8b" || slot == "8d" || slot == "8e"; served == notServed {
			t.Errorf("%s (slot %s): served by a card is %v", purpose, slot, served)
		}
	}
	if len(CardServed) != len(Reference)-3 {
		t.Errorf("a card holds %d purposes of %d; the three of PS256 and EdDSA are the ones it does not", len(CardServed), len(Reference))
	}
}

// The mechanism knows no purpose name (record 0021): Resolve takes no slot from the reference map. A card-key purpose that names no
// slot is refused, even one the map holds; WithReference fills it in when the host asks, and leaves a named slot and a purpose the map
// does not hold as they are.
func TestResolveTakesNoSlotFromTheReferenceMap(t *testing.T) {
	plan := []Purpose{{Name: "token-signing", Algorithm: "ES384"}}
	if _, err := Resolve(plan); err == nil || !strings.Contains(err.Error(), "no slot") {
		t.Fatalf("Resolve without a slot: %v, want the refusal", err)
	}
	filled := WithReference(append(plan, Purpose{Name: "wrapping", Algorithm: "ECDH-P384", Slot: "92"}, Purpose{Name: "other", Algorithm: "ES384"}))
	if filled[0].Slot != Reference["token-signing"] || filled[1].Slot != "92" || filled[2].Slot != "" {
		t.Fatalf("WithReference = %+v", filled)
	}
	if plan[0].Slot != "" {
		t.Fatal("WithReference changed the host's list")
	}
}
