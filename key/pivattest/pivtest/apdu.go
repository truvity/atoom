package pivtest

import "encoding/binary"

// The command and response APDUs of ISO 7816-4: parsing of the short and extended forms, the length of an answer (Le), the status words
// 61xx and GET RESPONSE, and command chaining.

const (
	claPlain = 0x00
	claChain = 0x10 // "more follows": the data is collected and the last part, with CLA 00, runs the command

	maxShortLe    = 256
	maxExtendedLe = 65536
	maxChain      = 65535 // the most data a chained command may collect: assumed: R21
)

// command is one command APDU as read. A header that is shorter than four bytes is left as it is.
type command struct {
	cla, ins, p1, p2 byte
	data             []byte
	hasLe            bool
	le               int // the Le as the standard reads it: 00 is 256 (short) or 65536 (extended)
	extended         bool
	why              string // set when the tripwire matched
}

// parseCommand reads a command APDU. ok is false when it is shorter than four bytes or its lengths do not match its size.
func parseCommand(b []byte) (c command, ok bool) {
	// The header as far as it was there, so that the log and the tripwire see what a short command did say.
	for i, p := range []*byte{&c.cla, &c.ins, &c.p1, &c.p2} {
		if i < len(b) {
			*p = b[i]
		}
	}
	switch {
	case len(b) < 4:
		return c, false
	case len(b) == 4:
		return c, true
	case len(b) == 5: // case 2, short
		c.hasLe, c.le = true, shortLe(b[4])
		return c, true
	case b[4] != 0: // short Lc
		n := int(b[4])
		switch len(b) {
		case 5 + n:
			c.data = b[5:]
		case 6 + n:
			c.data = b[5 : 5+n]
			c.hasLe, c.le = true, shortLe(b[5+n])
		default:
			return c, false
		}
		return c, true
	}
	// b[4] == 0 and more than five bytes: the extended form, with a three-byte Lc and a two-byte Le
	c.extended = true
	if len(b) < 7 {
		return c, false
	}
	if len(b) == 7 { // case 2, extended
		c.hasLe, c.le = true, extendedLe(b[5], b[6])
		return c, true
	}
	n := int(b[5])<<8 | int(b[6])
	switch {
	case n == 0:
		return c, false
	case len(b) == 7+n:
		c.data = b[7:]
	case len(b) == 9+n:
		c.data = b[7 : 7+n]
		c.hasLe, c.le = true, extendedLe(b[7+n], b[8+n])
	default:
		return c, false
	}
	return c, true
}

func shortLe(b byte) int {
	if b == 0 {
		return maxShortLe
	}
	return int(b)
}

func extendedLe(hi, lo byte) int {
	if hi == 0 && lo == 0 {
		return maxExtendedLe
	}
	return int(hi)<<8 | int(lo)
}

// reply is an answer before it is cut to the length the command allows.
type reply struct {
	data []byte
	sw   uint16
}

func status(sw uint16) reply { return reply{sw: sw} }

func (r reply) bytes() []byte {
	out := make([]byte, 0, len(r.data)+2)
	out = append(out, r.data...)
	return binary.BigEndian.AppendUint16(out, r.sw)
}

// limit is the most data the answer to c may carry now. A command that came extended, on a card in the mode Extended, may have up to
// 65536 bytes; every other command up to 256. A command with no Le is read as one with the largest (assumed: R22).
func (c *state) limit(cmd *command) int {
	most := maxShortLe
	if cmd.extended && c.mode == Extended {
		most = maxExtendedLe
	}
	if cmd.hasLe && cmd.le < most {
		return cmd.le
	}
	return most
}

// shape cuts a successful answer to the length the command allows. What does not fit is kept for GET RESPONSE and the status word tells
// how much is left: 61 followed by the number of bytes, 00 for 256 or more. An Le shorter than the answer is always cut and followed by
// 61xx, never answered 6Cxx, which ISO 7816-4 also allows (assumed: R24).
func (c *state) shape(cmd *command, r reply) reply {
	if r.sw != swOK {
		return r
	}
	n := c.limit(cmd)
	if len(r.data) <= n {
		return r
	}
	rest := r.data[n:] // the answer is cut, and followed by 61xx, never 6Cxx (assumed: R24)
	c.pending = append([]byte(nil), rest...)
	left := len(rest)
	if left >= maxShortLe {
		left = 0
	}
	return reply{data: r.data[:n], sw: swMore | uint16(left)}
}

// getResponse gives the next part of an answer that did not fit. With nothing pending it answers 6A86 (assumed: R10).
func (c *state) getResponse(cmd *command) reply {
	pending := c.pending
	c.pending = nil
	if cmd.p1 != 0 || cmd.p2 != 0 || len(pending) == 0 { // assumed: R10
		return status(swWrongP1P2)
	}
	if len(cmd.data) != 0 {
		return status(swWrongLength) // assumed: R8
	}
	return c.shape(cmd, reply{data: pending, sw: swOK})
}

// chain is the data of a command that came in parts.
type chain struct {
	ins, p1, p2 byte
	data        []byte
}

// chained takes one part of a chained command (CLA 10). It answers 9000 with no data. A part with another INS, P1 or P2 than the first, and
// data beyond maxChain, drop the chain and answer 6700 (assumed: R21).
func (c *state) chained(cmd *command) reply {
	ch := c.chain
	if ch == nil {
		ch = &chain{ins: cmd.ins, p1: cmd.p1, p2: cmd.p2}
	}
	if ch.ins != cmd.ins || ch.p1 != cmd.p1 || ch.p2 != cmd.p2 || len(ch.data)+len(cmd.data) > maxChain { // assumed: R21
		c.chain = nil
		return status(swWrongLength)
	}
	ch.data = append(ch.data, cmd.data...)
	c.chain = ch
	return status(swOK)
}

// complete joins the data of an open chain to the last part. A last part with another INS, P1 or P2 than the chain runs alone, and one that
// would make the chain longer than maxChain is refused: ok is false and the answer is 6700 (assumed: R21).
func (c *state) complete(cmd *command) (ok bool) {
	ch := c.chain
	c.chain = nil
	if ch == nil || ch.ins != cmd.ins || ch.p1 != cmd.p1 || ch.p2 != cmd.p2 {
		return true
	}
	if len(ch.data)+len(cmd.data) > maxChain { // assumed: R21
		return false
	}
	cmd.data = append(ch.data, cmd.data...)
	if cmd.why == "" { // a rule that reads the data may match only the whole of it
		cmd.why = trip(cmd)
	}
	return true
}
