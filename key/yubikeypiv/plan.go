package yubikeypiv

import (
	"fmt"
	"slices"
	"strconv"

	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
	"github.com/truvity/atoom/key/pinrelease"
	"github.com/truvity/atoom/key/purpose"
	"github.com/truvity/atoom/key/slotplan"
	"github.com/truvity/atoom/key/yubikeypiv/internal/card"
)

// The algorithms of the key-store port (keystore.proto).
const (
	ES384    = purpose.ES384    // ECDSA over P-384 with SHA-384 (RFC 7518, section 3.4)
	ES256    = purpose.ES256    // ECDSA over P-256 with SHA-256 (RFC 7518, section 3.4)
	ECDHP384 = purpose.ECDHP384 // ECDH over P-384 (NIST SP 800-56A)
	RS256    = purpose.RS256    // RSASSA-PKCS1-v1_5 over SHA-256 with an RSA-2048 key (RFC 7518, section 3.3)
)

// FirmwareFloor is the oldest firmware a board's card may run: 5.7 fixes the EUCLEAK flaw (ADR 0006, "Touch and
// PIN"). The driver refuses a card below it, by what the card says and by what its attestations say.
var FirmwareFloor = card.Version{Major: 5, Minor: 7, Patch: 0}

// entry is one row of the slot plan: what a purpose of the host is on a card.
type entry struct {
	purpose   string
	slot      card.Slot
	algorithm string // the port's name
	spec      card.KeySpec
	curveBits int // 256 or 384; 0 for the RSA key
	sign      bool
	agree     bool
}

// optional says that a card whose slot holds a key that is not the plan's still starts, the purpose left absent: the RS256 key,
// which only some installations use. Every other key refuses the start.
func (e entry) optional() bool { return e.algorithm == RS256 }

// digestLen is the length of the digest the key signs: the curve's size for an EC key, SHA-256 for the RSA key.
func (e entry) digestLen() int { return purpose.DigestLen(e.algorithm) }

// cardSpec is how a key of an algorithm of the port is a key on a PIV card, and false for an algorithm that a card does not hold. A
// YubiKey 5 with the firmware floor of this driver (5.7) generates and uses ECDSA over P-256 and P-384, ECDH over P-384 and RSA-2048 in
// any of the slots of a board, so every algorithm of the port is held; an algorithm that the port gains later and the card lacks has no
// row here, and a plan that names it is refused with this reason (docs/slots-pin-touch.md), and the kit skips its cases.
func cardSpec(algorithm string, pin card.PINPolicy) (spec card.KeySpec, curveBits int, ok bool) {
	switch algorithm {
	case ES384, ECDHP384:
		return card.KeySpec{Algorithm: card.EC384, PIN: pin, Touch: card.TouchNever}, 384, true
	case ES256:
		return card.KeySpec{Algorithm: card.EC256, PIN: pin, Touch: card.TouchNever}, 256, true
	case RS256:
		// One private-key operation of the card takes about 0.17 s (measured, firmware 5.8; the driver admits four at once, signs them
		// one at a time, and holds the card meanwhile; see signRS256).
		return card.KeySpec{Algorithm: card.RSA2048, PIN: pin, Touch: card.TouchNever}, 0, true
	}
	return card.KeySpec{}, 0, false
}

// buildPlan makes the slot plan of the host's purposes (ADR 0006, "The slot plan"; key/slotplan). A purpose names its slot in its
// declaration, or takes the slot of the reference map (slotplan.Default); one with neither is refused, and so are two purposes in one
// slot, a slot that is no slot of a board, and an algorithm that a card does not hold. Every key is generated with PIN once and touch
// never, except the PIN release key (the purpose pin-release, ES384, slot 87), which signs while the board is locked: PIN never, touch
// never. The refusal is a *Problems that names an entry and a word and never a value.
func buildPlan(set *purpose.Set) ([]entry, error) {
	decls := set.List()
	var problems []string
	sp := make([]slotplan.Purpose, len(decls))
	for i, d := range decls {
		sp[i] = slotplan.Purpose{Name: d.Name, Algorithm: d.Algorithm, Slot: d.Slot}
		if _, _, ok := cardSpec(d.Algorithm, card.PINOnce); !ok {
			problems = append(problems, fmt.Sprintf("purposes[%d].algorithm: a card holds no key of it", i))
		}
		if d.Name == pinrelease.PurposePinRelease {
			if d.Algorithm != ES384 {
				problems = append(problems, fmt.Sprintf("purposes[%d].algorithm: the PIN release key is ES384", i))
			}
			if d.Slot != "" && d.Slot != pinrelease.SlotPinRelease {
				problems = append(problems, fmt.Sprintf("purposes[%d].slot: the PIN release key lives in slot %s", i, pinrelease.SlotPinRelease))
			}
		}
	}
	if len(problems) > 0 {
		return nil, &Problems{problems}
	}
	slots, err := slotplan.Resolve(sp)
	if err != nil {
		return nil, &Problems{[]string{err.Error()}}
	}
	out := make([]entry, 0, len(decls))
	for _, d := range decls {
		pin := card.PINOnce
		if d.Name == pinrelease.PurposePinRelease {
			pin = card.PINNever
		}
		spec, bits, _ := cardSpec(d.Algorithm, pin)
		n, err := strconv.ParseUint(slots[d.Name], 16, 8)
		if err != nil {
			return nil, &Problems{[]string{"purposes: a slot that is not hex"}}
		}
		e := entry{purpose: d.Name, slot: card.Slot(n), algorithm: d.Algorithm, spec: spec, curveBits: bits,
			sign: d.Allows(purpose.Sign), agree: d.Allows(purpose.Agree)}
		if d.Name == pinrelease.PurposePinRelease {
			// It signs only the texts of the PIN release, which the driver writes itself: never a digest of the caller's.
			e.sign = false
		}
		out = append(out, e)
	}
	return out, nil
}

// Row is one row of the slot plan, for a caller that checks it against its own registry.
type Row struct {
	Purpose   string
	Slot      string // two lower-case hex digits
	Algorithm string // the port's name
}

// SlotPlan returns the slot plan that the driver makes of a host's purposes: what each is on a card, in the order of the declaration.
// It refuses what New refuses, before a card is opened, so that a host can check its declaration against the card's rules.
func SlotPlan(purposes []purpose.Declaration) ([]Row, error) {
	set, err := purpose.New(purposes)
	if err != nil {
		return nil, err
	}
	plan, err := buildPlan(set)
	if err != nil {
		return nil, err
	}
	out := make([]Row, 0, len(plan))
	for _, e := range plan {
		out = append(out, Row{Purpose: e.purpose, Slot: e.slot.String(), Algorithm: e.algorithm})
	}
	return out, nil
}

// planInfos are the purposes as Capabilities says them: the PIN release key has no operation, whatever its declaration allowed.
func planInfos(plan []entry) []*keyv1alpha1.PurposeInfo {
	out := make([]*keyv1alpha1.PurposeInfo, len(plan))
	for i, e := range plan {
		var ops []string
		if e.sign {
			ops = append(ops, string(purpose.Sign))
		}
		if e.agree {
			ops = append(ops, string(purpose.Agree))
		}
		slices.Sort(ops)
		out[i] = &keyv1alpha1.PurposeInfo{Name: e.purpose, Algorithm: e.algorithm, Operations: ops}
	}
	return out
}
