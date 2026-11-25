// Package slotplan is the check of a card slot map (sluis ADR 0006, "The slot plan"): which PIV slot each enabled key purpose of a
// signed key plan lives in. Every program that takes a slot map asks it the same question, so that they refuse the same maps:
// sluis's key service when it starts and in its check command, and the commands that read a configuration before it is used.
//
// Reference is the reference map: the purposes of sluis, the first host (docs/slot-plan.md), as data a host may take. The mechanism
// (Resolve, Check, CheckCard) knows no purpose name (record 0021): every card-key purpose names its slot, and Resolve refuses one that
// names none. A host whose plans let a purpose take the reference slot (sluis's KeyPlan, REQ-PKI-042) fills them in first, explicitly,
// with WithReference; purpose.Reference() carries the reference slots already. The card driver builds its slot plan with Resolve.
//
// A purpose a plan does not list is not enabled, needs no slot and gets no key.
package slotplan

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// CardStore is the name a card key-store driver gives itself in Capabilities. the key service asks for it to know that a plan will be
// carried by a card, and the card driver says it with the same constant.
const CardStore = "yubikey-piv"

// Reference is the reference map, version 2 (decided 2026-11-18, maintainer decision S1 A): the slot of each purpose that has one.
// Version 2 changed only slots that version 1 left spare, so the first ten rows are version 1's. It is not a default of the mechanism:
// a host that wants a purpose to take its slot from here says so with WithReference, so that enabling a purpose later never renumbers
// the keys of a live card. The slots 90 to 95 have no holder: they are the rotation spares, and a purpose the map does not hold is named
// in a KeyPlan with one of them. Slot 9e is not used, and f9, the Yubico attestation slot, is no board slot at all
// (sluis docs/architecture/keys.yaml, slot_plan). Cloud federation keys and NATS keys are not card keys and have no row.
var Reference = map[string]string{
	"x509-intermediate":   "9c",
	"token-signing":       "9a",
	"wrapping":            "9d",
	"ssh-user":            "82",
	"ssh-host":            "83",
	"audit-log":           "84",
	"release-signing":     "85",
	"workload-issuer":     "86",
	"pin-release":         "87",
	"token-signing-rs256": "88",
	"issuance-log":        "89",
	"enrolment":           "8a",
	"token-signing-ps256": "8b",
	"token-signing-es256": "8c",
	"token-signing-eddsa": "8d",
	"witness":             "8e",
	"audit-log-second":    "8f",
}

// CardServed is the set of purposes of Reference whose key a card holds: every one whose algorithm is ES384, ES256, RS256 or ECDH-P384,
// which is the whole of what a YubiKey 5 with the firmware floor of the driver generates. The card driver serves any purpose that a host
// declares with such an algorithm and a slot (key/purpose), so this is no limit of the driver: it says which rows of the reference
// map a card can hold, and the three that it cannot are the ones with PS256 (8b) and EdDSA (8d, 8e), for which CheckCard refuses a plan.
var CardServed = map[string]bool{
	"x509-intermediate":   true,
	"token-signing":       true,
	"wrapping":            true,
	"ssh-user":            true,
	"ssh-host":            true,
	"audit-log":           true,
	"release-signing":     true,
	"workload-issuer":     true,
	"pin-release":         true,
	"token-signing-rs256": true,
	"issuance-log":        true,
	"enrolment":           true,
	"token-signing-es256": true,
	"audit-log-second":    true,
}

// cardless are the algorithms of a key that lives in a slot and that a card driver does not hold: RSA-PSS and Ed25519. A plan that
// names one for a card is refused by CheckCard. A YubiKey 5 with firmware 5.7 or newer does generate Ed25519 (docs/algorithms.md), but
// the port has no algorithm for it yet; both join onCard and the port together.
var cardless = map[string]bool{"PS256": true, "EdDSA": true}

// Purpose is one entry of a plan: a purpose, the algorithm of its key and the slot it names ("" names none, which Resolve refuses
// for a card-key purpose).
type Purpose struct {
	Name, Algorithm, Slot string
}

// onCard says whether the algorithm is one of an asymmetric key that lives in a card slot, and the card holds. A data key purpose names
// AES-256-GCM and has no slot. RS256 is listed: a purpose that is left out here skips every slot check, so two purposes
// could be planned in one slot and a card would be given a plan it silently does not follow. Listed, it has the slot 88 by default
// and a slot of its own like the others. PS256 and EdDSA are not listed: the port has no algorithm for them yet, and the keys of NATS
// and of the cloud federations are Ed25519 and are not card keys at all; they join this list with the port and the card driver's rows
// for the slots 8b, 8d and 8e.
func onCard(algorithm string) bool {
	switch algorithm {
	case "ES384", "ES256", "RS256", "ECDH-P384", "ECDH-P-384":
		return true
	}
	return false
}

// validSlot says whether s is a PIV slot a board uses: 9a, 9c, 9d, or a retired slot 82 to 95, in lower-case hex. 9e, the
// card-authentication slot, is not one (it signs without a PIN by default).
func validSlot(s string) bool {
	if len(s) != 2 || s != strings.ToLower(s) {
		return false
	}
	switch s {
	case "9a", "9c", "9d":
		return true
	}
	n, err := strconv.ParseUint(s, 16, 8)
	return err == nil && n >= 0x82 && n <= 0x95
}

// WithReference returns a copy of the list in which every purpose that names no slot takes its slot from Reference, when Reference
// has one. A host whose plans let a purpose take the reference slot calls it before Resolve or Check; the mechanism never does.
func WithReference(purposes []Purpose) []Purpose {
	out := slices.Clone(purposes)
	for i, p := range out {
		if p.Slot == "" {
			out[i].Slot = Reference[p.Name]
		}
	}
	return out
}

// Resolve returns the slot of each card-key purpose of the list: the one it names. It refuses, with a path and a word and never a
// value, a slot that is no board slot, a purpose that names no slot, two purposes in one slot and a purpose listed twice. Refusals
// are collected in the order of the list. It takes no slot from Reference (WithReference does, for a host that asks).
func Resolve(purposes []Purpose) (map[string]string, error) {
	slots := map[string]string{}
	bySlot := map[string]string{}
	var problems []string
	for i, p := range purposes {
		if !onCard(p.Algorithm) {
			continue
		}
		where := fmt.Sprintf("purposes[%d]", i)
		if _, dup := slots[p.Name]; dup {
			problems = append(problems, where+": listed twice")
			continue
		}
		slot := p.Slot
		if slot == "" {
			problems = append(problems, where+": no slot (name one of the spare slots 90 to 95)")
			continue
		}
		if !validSlot(slot) {
			problems = append(problems, where+".slot: not a slot of a board")
			continue
		}
		if other, taken := bySlot[slot]; taken {
			problems = append(problems, fmt.Sprintf("%s.slot: shared with %s", where, other))
			continue
		}
		slots[p.Name], bySlot[slot] = slot, p.Name
	}
	if len(problems) > 0 {
		slices.Sort(problems)
		return nil, fmt.Errorf("card slot map: %s", strings.Join(problems, "; "))
	}
	return slots, nil
}

// Check is Resolve without the map.
func Check(purposes []Purpose) error {
	_, err := Resolve(purposes)
	return err
}

// CheckCard is the check of a plan against the card driver. The driver serves any purpose that a host declares with an algorithm that a
// card holds, in any slot of a board that Check accepts, so what is left of its limit is the algorithm: a plan that enables a purpose
// whose algorithm a card does not hold (PS256, EdDSA) is refused. The refusal names the entry and the word, never a key. Call Check
// first; this adds the card's own limit.
func CheckCard(purposes []Purpose) error {
	var problems []string
	for i, p := range purposes {
		if cardless[p.Algorithm] {
			problems = append(problems, fmt.Sprintf("purposes[%d].algorithm: the card driver holds no key of it", i))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("card slot map: %s", strings.Join(problems, "; "))
	}
	return nil
}
