package yubikeypiv

import (
	"errors"
	"strings"
	"testing"

	"github.com/truvity/atoom/key/purpose"
	"github.com/truvity/atoom/key/slotplan"
	"github.com/truvity/atoom/key/yubikeypiv/internal/card"
)

func mustPlan(t *testing.T, decls []purpose.Declaration) []entry {
	t.Helper()
	plan, err := buildPlan(purpose.MustNew(decls))
	if err != nil {
		t.Fatalf("buildPlan: %v", err)
	}
	return plan
}

// The slot plan that the driver makes of sluis's purposes gives each key its own slot (sluis ADR 0006, "The slot plan"): no slot holds two
// purposes and no purpose sits in two slots, and every slot is one that a board uses. That the slots and algorithms are those of sluis's
// registry (docs/architecture/keys.yaml) is checked by sluis, through SlotPlan; an installation's own slot map is checked where it is
// read, by sluis's slot map reader.
//
// verifies: REQ-KEYS-046
func TestTheSlotPlanGivesEveryKeyASlotOfItsOwn(t *testing.T) {
	plan := mustPlan(t, purpose.Reference())
	bySlot, byPurpose := map[uint8]string{}, map[string]bool{}
	for _, e := range plan {
		if other, dup := bySlot[uint8(e.slot)]; dup {
			t.Errorf("slot %02x holds both %s and %s", uint8(e.slot), other, e.purpose)
		}
		bySlot[uint8(e.slot)] = e.purpose
		if byPurpose[e.purpose] {
			t.Errorf("purpose %s has two rows", e.purpose)
		}
		byPurpose[e.purpose] = true
		n := uint8(e.slot)
		if board := n == 0x9a || n == 0x9c || n == 0x9d || n >= 0x82 && n <= 0x95; !board {
			t.Errorf("%s: slot %s is not one that a board uses", e.purpose, e.slot)
		}
	}
	if len(plan) != 11 {
		t.Fatalf("the plan has %d rows: the sweep checked the wrong thing", len(plan))
	}
}

// The plan of the reference purposes is the reference map (key/slotplan), slot for slot, with the policies of ADR 0006: PIN once and touch
// never, except the PIN release key, which signs while the board is locked.
//
// verifies: REQ-KEYS-278
func TestThePlanOfTheReferencePurposesIsTheReferenceMap(t *testing.T) {
	for _, e := range mustPlan(t, purpose.Reference()) {
		if got := slotplan.Default[e.purpose]; got != e.slot.String() {
			t.Errorf("%s: the driver's slot %s, the reference map's %q", e.purpose, e.slot, got)
		}
		if !slotplan.CardServed[e.purpose] {
			t.Errorf("%s: the reference map does not list it as held by a card", e.purpose)
		}
		wantPIN := card.PINOnce
		if e.purpose == "pin-release" {
			wantPIN = card.PINNever
		}
		if e.spec.PIN != wantPIN || e.spec.Touch != card.TouchNever {
			t.Errorf("%s: policies %v/%v", e.purpose, e.spec.PIN, e.spec.Touch)
		}
		switch e.algorithm {
		case ES256:
			if e.spec.Algorithm != card.EC256 || e.curveBits != 256 {
				t.Errorf("%s: ES256 is %v", e.purpose, e.spec.Algorithm)
			}
		case ES384, ECDHP384:
			if e.spec.Algorithm != card.EC384 || e.curveBits != 384 {
				t.Errorf("%s: %s is %v", e.purpose, e.algorithm, e.spec.Algorithm)
			}
		case RS256:
			if e.spec.Algorithm != card.RSA2048 || e.curveBits != 0 {
				t.Errorf("%s: RS256 is %v", e.purpose, e.spec.Algorithm)
			}
		default:
			t.Errorf("%s: algorithm %s", e.purpose, e.algorithm)
		}
		// The operations are the declaration's, except that the PIN release key signs nothing for a caller.
		wantSign := e.algorithm != ECDHP384 && e.purpose != "pin-release"
		if e.sign != wantSign || e.agree != (e.algorithm == ECDHP384) {
			t.Errorf("%s: sign %v agree %v", e.purpose, e.sign, e.agree)
		}
	}
}

// A purpose that no driver knew, with ES256 or ES384, takes a slot that its host names (a spare, 90 to 95) or the one of the reference map,
// and the plan follows the order of the declaration.
func TestAHostDeclaredPurposeTakesTheSlotItNames(t *testing.T) {
	plan := mustPlan(t, []purpose.Declaration{
		{Name: "client-assertion", Algorithm: purpose.ES256, Operations: []purpose.Operation{purpose.Sign}, Slot: "90"},
		{Name: "identity-header", Algorithm: purpose.ES384, Operations: []purpose.Operation{purpose.Sign}, Slot: "91"},
		{Name: "token-signing", Algorithm: purpose.ES256, Operations: []purpose.Operation{purpose.Sign}},
	})
	want := []struct {
		name, slot, alg string
		bits            int
	}{{"client-assertion", "90", ES256, 256}, {"identity-header", "91", ES384, 384}, {"token-signing", "9a", ES256, 256}}
	for i, w := range want {
		e := plan[i]
		if e.purpose != w.name || e.slot.String() != w.slot || e.algorithm != w.alg || e.curveBits != w.bits || !e.sign || e.agree {
			t.Errorf("row %d: %+v, want %+v", i, e, w)
		}
	}
	rows, err := SlotPlan([]purpose.Declaration{{Name: "client-assertion", Algorithm: purpose.ES256, Slot: "90"}})
	if err != nil || len(rows) != 1 || rows[0] != (Row{Purpose: "client-assertion", Slot: "90", Algorithm: "ES256"}) {
		t.Errorf("SlotPlan: %v %v", rows, err)
	}
}

// What a card cannot follow is refused before any card is opened, with a path and a word and no value.
func TestADeclarationThatACardCannotFollowIsRefused(t *testing.T) {
	sign := []purpose.Operation{purpose.Sign}
	for name, tc := range map[string]struct {
		decls []purpose.Declaration
		want  string
	}{
		"a purpose with no slot":          {[]purpose.Declaration{{Name: "mine", Algorithm: purpose.ES256, Operations: sign}}, "purposes[0]: no slot"},
		"two purposes in one slot":        {[]purpose.Declaration{{Name: "a", Algorithm: purpose.ES256, Slot: "90"}, {Name: "b", Algorithm: purpose.ES384, Slot: "90"}}, "shared with a"},
		"a slot of the default holder":    {[]purpose.Declaration{{Name: "token-signing", Algorithm: purpose.ES384}, {Name: "mine", Algorithm: purpose.ES384, Slot: "9a"}}, "shared with token-signing"},
		"the card authentication slot":    {[]purpose.Declaration{{Name: "mine", Algorithm: purpose.ES384, Slot: "9e"}}, "not a slot of a board"},
		"the attestation slot":            {[]purpose.Declaration{{Name: "mine", Algorithm: purpose.ES384, Slot: "f9"}}, "not a slot of a board"},
		"the PIN release key as ES256":    {[]purpose.Declaration{{Name: "pin-release", Algorithm: purpose.ES256}}, "the PIN release key is ES384"},
		"the PIN release key in a spare":  {[]purpose.Declaration{{Name: "pin-release", Algorithm: purpose.ES384, Slot: "90"}}, "lives in slot 87"},
		"a name of the reference twice":   {[]purpose.Declaration{{Name: "wrapping", Algorithm: purpose.ECDHP384}, {Name: "wrapping", Algorithm: purpose.ECDHP384}}, "declared twice"},
		"an algorithm that is not a port": {[]purpose.Declaration{{Name: "mine", Algorithm: "EdDSA", Slot: "90"}}, "not ES384, ES256, RS256 or ECDH-P384"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := SlotPlan(tc.decls)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			var p *Problems
			var pe *purpose.Error
			if !errors.As(err, &p) && !errors.As(err, &pe) {
				t.Errorf("the refusal is %T", err)
			}
		})
	}
	// The card holds every algorithm of the port: the table has a row for each, so a new one that it lacks fails here first.
	for _, alg := range []string{ES384, ES256, RS256, ECDHP384} {
		if _, _, ok := cardSpec(alg, card.PINOnce); !ok {
			t.Errorf("a card holds no key of %s", alg)
		}
	}
	if _, _, ok := cardSpec("EdDSA", card.PINOnce); ok {
		t.Error("a card holds a key of EdDSA")
	}
}
