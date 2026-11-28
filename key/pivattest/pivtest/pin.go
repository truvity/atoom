package pivtest

import (
	"bytes"
	"fmt"
)

// A reference is the PIN or the PUK: a value of 6 to 8 bytes, a counter of the tries left and its maximum, and whether the value is the
// factory one. On the wire a value is padded with FF to 8 bytes.
type reference struct {
	value     []byte
	isDefault bool
	left, max byte
}

func newReference(name, given, factory string, most byte) reference {
	if most == 0 {
		most = 3
	}
	if most > 15 {
		panic("pivtest: the most tries of " + name + " is 15, the 63Cx status word has four bits")
	}
	r := reference{value: []byte(factory), isDefault: true, left: most, max: most}
	if given != "" {
		if !validValue([]byte(given)) {
			panic("pivtest: the " + name + " must be 6 to 8 bytes")
		}
		if given[len(given)-1] == 0xFF {
			panic("pivtest: the " + name + " must not end in the byte FF, which is the padding of a value on the wire: it could never be sent")
		}
		r.value, r.isDefault = []byte(given), false
	}
	return r
}

func validValue(v []byte) bool { return len(v) >= 6 && len(v) <= 8 }

// unpad removes the FF padding.
func unpad(b []byte) []byte { return bytes.TrimRight(b, "\xff") }

// matches compares a block of 8 bytes with the value, as the card does: padded to 8 bytes.
func (r *reference) matches(block []byte) bool {
	return len(block) == 8 && bytes.Equal(unpad(block), r.value)
}

// Format prints the counters and the flag, and not the value, for every verb.
func (r reference) Format(f fmt.State, _ rune) {
	_, _ = f.Write(fmt.Appendf(nil, "reference{value not shown, default %t, tries %d of %d}", r.isDefault, r.left, r.max))
}

// wrong spends one try and gives the status word of the counter.
func (r *reference) wrong() uint16 {
	if r.left > 0 {
		r.left--
	}
	return r.counter()
}

func (r *reference) counter() uint16 { return swTriesLeft | uint16(r.left) }

func (c *state) ref(p2 byte) *reference {
	switch p2 {
	case refPIN:
		return &c.pin
	case refPUK:
		return &c.puk
	}
	return nil
}

// verify is VERIFY of the PIN: P2 80 and eight bytes. With no data it reads the counter and spends no try: 63Cx when the PIN is not
// verified, 9000 when it is (measured on two cards, research page "PIN, touch, delete, move and reset"). A wrong value spends a try and
// leaves the PIN not verified (assumed: R20).
func (c *state) verify(cmd *command) reply {
	if cmd.p1 != 0 {
		return status(swWrongP1P2) // assumed: R8
	}
	if cmd.p2 != refPIN {
		return status(swRefNotFound) // assumed: R8
	}
	switch len(cmd.data) {
	case 0:
		if c.verified {
			return status(swOK)
		}
		return status(c.pin.counter())
	case 8:
	default:
		return status(swWrongLength) // assumed: R8
	}
	if c.pin.left == 0 {
		c.verified = false
		return status(swBlocked)
	}
	if !c.pin.matches(cmd.data) {
		c.verified = false // assumed: R20
		return status(c.pin.wrong())
	}
	c.pin.left = c.pin.max
	c.verified = true
	return status(swOK)
}

// changeReference is CHANGE REFERENCE DATA: P2 80 for the PIN or 81 for the PUK, the old value and the new one, 8 bytes each. A wrong old
// value spends a try of that reference's own counter. A new value of fewer than 6 or more than 8 bytes is refused before any try is
// spent (assumed: R8).
func (c *state) changeReference(cmd *command) reply {
	if cmd.p1 != 0 {
		return status(swWrongP1P2) // assumed: R8
	}
	r := c.ref(cmd.p2)
	if r == nil {
		return status(swRefNotFound) // assumed: R8
	}
	if len(cmd.data) != 16 {
		return status(swWrongLength) // assumed: R8
	}
	next := unpad(cmd.data[8:])
	if !validValue(next) {
		return status(swWrongData) // assumed: R8
	}
	if r.left == 0 {
		return status(swBlocked)
	}
	if !r.matches(cmd.data[:8]) {
		if r == &c.pin {
			c.verified = false // assumed: R20
		}
		return status(r.wrong())
	}
	r.value = append([]byte(nil), next...)
	r.isDefault = false
	r.left = r.max
	return status(swOK)
}
