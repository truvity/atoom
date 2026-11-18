package yubikeypiv

import (
	"testing"

	"github.com/truvity/atoom/key/slotplan"
)

// The default slot plan of the card driver gives each key its own slot (sluis ADR 0006, "The slot plan"): no slot holds two purposes
// and no purpose sits in two slots, so no key shares a card slot with another; and no row is a slot that a board does not use. That the
// slots and algorithms are those of sluis's registry (docs/architecture/keys.yaml) is checked by sluis, through SlotPlan; an
// installation's own slot map is checked where it is read, by sluis's slot map reader.
//
// verifies: REQ-KEYS-046
func TestTheDefaultSlotPlanGivesEveryKeyASlotOfItsOwn(t *testing.T) {
	bySlot, byPurpose := map[uint8]string{}, map[string]bool{}
	for _, e := range slotPlan {
		if other, dup := bySlot[uint8(e.slot)]; dup {
			t.Errorf("slot %02x holds both %s and %s", uint8(e.slot), other, e.purpose)
		}
		bySlot[uint8(e.slot)] = e.purpose
		if byPurpose[e.purpose] {
			t.Errorf("purpose %s has two rows", e.purpose)
		}
		byPurpose[e.purpose] = true
	}
	if len(slotPlan) < 9 {
		t.Fatalf("the plan has %d rows: the sweep checked nothing", len(slotPlan))
	}
	// No row of the driver is a slot that a board does not use: the Yubico attestation slot f9 is never touched, and the
	// rotation spares 90 to 95 have no row (a rotation puts its key there by the installation's plan, not by the default map).
	for _, e := range slotPlan {
		n := uint8(e.slot)
		if board := n == 0x9a || n == 0x9c || n == 0x9d || n >= 0x82 && n <= 0x8f; !board {
			t.Errorf("%s: the driver has a row in slot %s, which the default map does not use for a purpose", e.purpose, e.slot)
		}
	}
}

// SlotPlan is the table of the driver, read-only, for the check against a registry.
func TestSlotPlanIsTheDriversTable(t *testing.T) {
	rows := SlotPlan()
	if len(rows) != len(slotPlan) {
		t.Fatalf("%d rows, the table has %d", len(rows), len(slotPlan))
	}
	for i, r := range rows {
		if r.Purpose != slotPlan[i].purpose || r.Slot != slotPlan[i].slot.String() || r.Algorithm != slotPlan[i].algorithm {
			t.Errorf("row %d: %+v differs from the table", i, r)
		}
	}
	rows[0].Purpose = "changed"
	if SlotPlan()[0].Purpose == "changed" {
		t.Error("SlotPlan returned the table itself, not a copy")
	}
}

// The card driver's table is the default map that a caller checks a key plan against (key/slotplan), slot for slot: the caller
// refuses a plan the card would not follow only if the two tables are one.
//
// The kit's map is version 2 and holds more than the card serves: the driver's table is the part the kit lists as served
// (slotplan.CardServed), slot for slot, and has no row for the other slots of the map.
//
// verifies: REQ-KEYS-278
func TestTheDriversTableIsTheSlotPlanKitsDefault(t *testing.T) {
	if len(slotPlan) != len(slotplan.CardServed) {
		t.Fatalf("the driver has %d purposes, the kit lists %d as served", len(slotPlan), len(slotplan.CardServed))
	}
	if len(slotplan.Default) <= len(slotPlan) {
		t.Fatalf("the kit's map holds %d purposes, the driver %d: version 2 holds more than the card serves", len(slotplan.Default), len(slotPlan))
	}
	for _, e := range slotPlan {
		if !slotplan.CardServed[e.purpose] {
			t.Errorf("%s: the driver has a row the kit does not list as served", e.purpose)
		}
		if got := slotplan.Default[e.purpose]; got != e.slot.String() {
			t.Errorf("%s: the driver's slot %s, the kit's %q", e.purpose, e.slot, got)
		}
	}
	for purpose, slot := range slotplan.Default {
		if _, ok := planFor(purpose); !ok && slotplan.CardServed[purpose] {
			t.Errorf("%s: the kit lists slot %s as served, and the driver has no row", purpose, slot)
		}
	}
}
