package pivtest

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
)

// The reference model of this file is written from doc.go, from ISO 7816-4 and from NIST SP 800-73 (the AID and the application property
// template), not from the code of the card: it keeps its own copy of the card state and of the session state, and it parses an APDU on its
// own. A long random sequence is run on the card and on the model, and after every step the card must agree with the model and with the
// invariants that the model does not need.

const (
	qaAIDShort = "\xA0\x00\x00\x03\x08"
	qaAIDFull  = "\xA0\x00\x00\x03\x08\x00\x00\x10\x00\x01\x00"
	// qaSelectTemplate is the application property template of SP 800-73: 61 { 4F 06 <AID without prefix>, 79 { 4F 05 <prefix> } }.
	qaSelectTemplate = "\x61\x11\x4F\x06\x00\x00\x10\x00\x01\x00\x79\x07\x4F\x05\xA0\x00\x00\x03\x08"
)

type qaRefState struct {
	val       string // the value, without padding
	left, max int
	def       bool
}

// qaCmd is a command as ISO 7816-4 reads it.
type qaCmd struct {
	cla, ins, p1, p2 byte
	data             []byte
	ext              bool
	hasLe            bool
	le               int
}

func qaLe(b byte) int {
	if b == 0 {
		return 256
	}
	return int(b)
}

func qaLe2(hi, lo byte) int {
	if hi == 0 && lo == 0 {
		return 65536
	}
	return int(hi)<<8 | int(lo)
}

// qaParse reads the cases 1, 2S, 3S, 4S, 2E, 3E and 4E. ok is false for a size that fits none of them.
func qaParse(b []byte) (c qaCmd, ok bool) {
	if len(b) < 4 {
		return c, false
	}
	c = qaCmd{cla: b[0], ins: b[1], p1: b[2], p2: b[3]}
	body := b[4:]
	switch {
	case len(body) == 0:
		return c, true
	case len(body) == 1:
		c.hasLe, c.le = true, qaLe(body[0])
		return c, true
	case body[0] != 0:
		n := int(body[0])
		switch len(body) {
		case 1 + n:
			c.data = body[1:]
		case 2 + n:
			c.data = body[1 : 1+n]
			c.hasLe, c.le = true, qaLe(body[1+n])
		default:
			return c, false
		}
		return c, true
	}
	c.ext = true
	switch {
	case len(body) < 3:
		return c, false
	case len(body) == 3:
		c.hasLe, c.le = true, qaLe2(body[1], body[2])
		return c, true
	}
	n := int(body[1])<<8 | int(body[2])
	switch {
	case n == 0:
		return c, false
	case len(body) == 3+n:
		c.data = body[3:]
	case len(body) == 5+n:
		c.data = body[3 : 3+n]
		c.hasLe, c.le = true, qaLe2(body[3+n], body[4+n])
	default:
		return c, false
	}
	return c, true
}

type qaChain struct {
	ins, p1, p2 byte
	data        []byte
}

// qaReply is what the model says of one answer: sw -1 means "refused, and the model does not say how".
type qaReply struct {
	sw   int
	data []byte
}

// qaStep is what the model tells the harness about one command beyond the answer.
type qaStep struct {
	qaReply
	ran bool // a command ran (it was not refused before it was read, and it was not a part of a chain)
	ins byte // of the command that ran, after a chain joined it
	p1  byte
	p2  byte
	n   int // the length of its data, after a chain joined it
}

type qaModel struct {
	on, selected, verified bool
	pin, puk               qaRefState
	obj                    map[uint32][]byte
	chain                  *qaChain
	pending                []byte
	fw                     [3]byte
	serial                 uint32
	extMode, noExt         bool
}

func newQaModel(o Options) *qaModel {
	m := &qaModel{obj: map[uint32][]byte{}, fw: o.Firmware, serial: o.Serial, extMode: o.ResponseMode == Extended, noExt: o.NoExtendedAPDU}
	if m.fw == [3]byte{} {
		m.fw = [3]byte{5, 8, 0}
	}
	if m.serial == 0 {
		m.serial = 7654321
	}
	mk := func(given, factory string, most byte) qaRefState {
		if most == 0 {
			most = 3
		}
		if given == "" {
			return qaRefState{val: factory, left: int(most), max: int(most), def: true}
		}
		return qaRefState{val: given, left: int(most), max: int(most)}
	}
	m.pin = mk(o.PIN, "123456", o.MaxPINTries)
	m.puk = mk(o.PUK, "12345678", o.MaxPUKTries)
	return m
}

func (m *qaModel) power(on bool) {
	m.on = on
	m.selected, m.verified, m.chain, m.pending = false, false, nil, nil
}

func (m *qaModel) ref(p2 byte) *qaRefState {
	switch p2 {
	case 0x80:
		return &m.pin
	case 0x81:
		return &m.puk
	}
	return nil
}

func qaUnpad(b []byte) string { return string(bytes.TrimRight(b, "\xff")) }

// shape cuts a successful answer to the command's Le and keeps the rest for GET RESPONSE.
func (m *qaModel) shape(c qaCmd, data []byte) qaReply {
	lim := 256
	if c.ext && m.extMode {
		lim = 65536
	}
	if c.hasLe && c.le < lim {
		lim = c.le
	}
	if len(data) <= lim {
		return qaReply{0x9000, data}
	}
	rest := data[lim:]
	m.pending = append([]byte(nil), rest...)
	low := len(rest)
	if low >= 256 {
		low = 0
	}
	return qaReply{0x6100 | low, data[:lim]}
}

func (m *qaModel) step(c qaCmd, ok bool) (s qaStep) {
	s.sw = -1
	switch {
	case !ok:
		m.chain, m.pending = nil, nil
		s.sw = 0x6700
		return s
	case c.ext && m.noExt:
		m.chain, m.pending = nil, nil
		s.sw = 0x6700
		return s
	case c.cla != 0x00 && c.cla != 0x10:
		m.chain, m.pending = nil, nil
		s.sw = 0x6E00
		return s
	}
	if c.cla != 0 || c.ins != 0xC0 {
		m.pending = nil
	}
	if !m.selected && c.ins != 0xA4 {
		m.chain = nil
		s.sw = 0x6985
		return s
	}
	if c.cla == 0x10 {
		ch := m.chain
		if ch == nil {
			ch = &qaChain{ins: c.ins, p1: c.p1, p2: c.p2}
		}
		if ch.ins != c.ins || ch.p1 != c.p1 || ch.p2 != c.p2 || len(ch.data)+len(c.data) > 65535 {
			m.chain = nil
			s.sw = 0x6700
			return s
		}
		ch.data = append(ch.data, c.data...)
		m.chain = ch
		s.sw = 0x9000
		return s
	}
	if ch := m.chain; ch != nil {
		m.chain = nil
		if ch.ins == c.ins && ch.p1 == c.p1 && ch.p2 == c.p2 {
			if len(ch.data)+len(c.data) > 65535 { // the last part counts too
				s.sw = 0x6700
				return s
			}
			c.data = append(append([]byte(nil), ch.data...), c.data...)
		}
	}
	s.ran, s.ins, s.p1, s.p2, s.n = true, c.ins, c.p1, c.p2, len(c.data)
	s.qaReply = m.exec(c)
	return s
}

func (m *qaModel) exec(c qaCmd) qaReply {
	d := c.data
	switch c.ins {
	case 0xA4:
		if c.p1 == 4 && c.p2 == 0 && (string(d) == qaAIDShort || string(d) == qaAIDFull) {
			m.selected, m.verified = true, false
			return m.shape(c, []byte(qaSelectTemplate))
		}
	case 0xFD:
		if c.p1 == 0 && c.p2 == 0 && len(d) == 0 {
			return m.shape(c, m.fw[:])
		}
	case 0xF8:
		if c.p1 == 0 && c.p2 == 0 && len(d) == 0 {
			return m.shape(c, binary.BigEndian.AppendUint32(nil, m.serial))
		}
	case 0x20:
		if (c.p1 != 0 && c.p1 != 0xFF) || c.p2 != 0x80 {
			break
		}
		if c.p1 == 0xFF { // SP 800-73-4: the reset of the security status: no data, no try spent, 9000 whatever the counter; with data 6700 (R8)
			if len(d) != 0 {
				return qaReply{0x6700, nil}
			}
			m.verified = false
			return qaReply{0x9000, nil}
		}
		if len(d) == 0 {
			if m.verified {
				return qaReply{0x9000, nil}
			}
			return qaReply{0x63C0 | m.pin.left, nil}
		}
		if len(d) != 8 {
			break
		}
		if m.pin.left == 0 {
			m.verified = false
			return qaReply{0x6983, nil}
		}
		if qaUnpad(d) == m.pin.val {
			m.pin.left, m.verified = m.pin.max, true
			return qaReply{0x9000, nil}
		}
		m.pin.left--
		m.verified = false
		return qaReply{0x63C0 | m.pin.left, nil}
	case 0x24:
		r := m.ref(c.p2)
		if c.p1 != 0 || r == nil || len(d) != 16 {
			break
		}
		next := qaUnpad(d[8:])
		if len(next) < 6 || len(next) > 8 {
			return qaReply{0x6A80, nil}
		}
		if r.left == 0 {
			return qaReply{0x6983, nil}
		}
		if qaUnpad(d[:8]) != r.val {
			r.left--
			if r == &m.pin {
				m.verified = false
			}
			return qaReply{0x63C0 | r.left, nil}
		}
		r.val, r.def, r.left = next, false, r.max
		return qaReply{0x9000, nil}
	case 0xF7:
		r := m.ref(c.p2)
		if c.p1 != 0 || r == nil || len(d) != 0 {
			break
		}
		def := byte(0)
		if r.def {
			def = 1
		}
		return m.shape(c, []byte{1, 1, 0xFF, 5, 1, def, 6, 2, qaByte(r.max), qaByte(r.left)})
	case 0xCB:
		if c.p1 != 0x3F || c.p2 != 0xFF || len(d) != 5 || d[0] != 0x5C || d[1] != 3 {
			break
		}
		body, found := m.obj[uint32(d[2])<<16|uint32(d[3])<<8|uint32(d[4])]
		if !found {
			return qaReply{0x6A82, nil}
		}
		return m.shape(c, body)
	case 0xC0:
		rest := m.pending
		m.pending = nil
		if c.p1 != 0 || c.p2 != 0 || len(rest) == 0 {
			return qaReply{0x6A86, nil}
		}
		if len(d) != 0 {
			return qaReply{0x6700, nil}
		}
		return m.shape(c, rest)
	}
	return qaReply{-1, nil}
}

// ---- the generator

type qaKind int

const (
	qaExchange qaKind = iota
	qaOn
	qaOff
	qaPut
)

type qaOp struct {
	kind qaKind
	apdu []byte
	id   uint32
	body []byte
}

var qaIDs = []uint32{ObjectSlot9A, ObjectSlot9C, ObjectSlot9D, ObjectSlot9E, ObjectRetired1, ObjectRetired1 + 19, ObjectAttestation}

type qaGen struct {
	r    *rand.Rand
	m    *qaModel // read only: the generator uses it to know the right values; nil is allowed
	hist [][]byte
	prev string
	n    int
}

func qaByte(n int) byte { return byte(n & 0xFF) }

func newQaGen(seed uint64, m *qaModel) *qaGen {
	//nolint:gosec // a seeded generator of a test: the same sequence on every run is the point
	return &qaGen{r: rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15)), m: m, prev: "654321"}
}

func (g *qaGen) value(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = "0123456789"[g.r.IntN(10)]
	}
	return string(b)
}

func (g *qaGen) pinValue() string {
	cur := "123456"
	if g.m != nil {
		cur = g.m.pin.val
	}
	switch x := g.r.IntN(100); {
	case x < 55:
		return cur
	case x < 70:
		return g.prev
	case x < 75: // a block whose value, with the padding off, is shorter than 6 bytes: a wrong value that spends a try (R8)
		return g.value(1 + g.r.IntN(5))
	case x < 77:
		return "\xff\xff\xff\xff\xff\xff\xff\xff"
	default:
		return g.value(6 + g.r.IntN(3))
	}
}

func (g *qaGen) refValue(ref byte) string {
	cur := "123456"
	if ref == 0x81 {
		cur = "12345678"
	}
	if g.m != nil && g.m.ref(ref) != nil {
		cur = g.m.ref(ref).val
	}
	if g.r.IntN(100) < 65 {
		return cur
	}
	return g.value(6 + g.r.IntN(3))
}

func (g *qaGen) encode(cla, ins, p1, p2 byte, data []byte) []byte {
	ext := g.r.IntN(4) == 0 || len(data) > 255
	le := -1
	switch g.r.IntN(4) {
	case 0:
		le = 0
	case 1:
		le = 1 + g.r.IntN(255)
	}
	if ext {
		return extended(cla, ins, p1, p2, data, le)
	}
	return short(cla, ins, p1, p2, data, le)
}

func (g *qaGen) one(b []byte) []qaOp {
	g.hist = append(g.hist, b)
	if len(g.hist) > 16 {
		g.hist = g.hist[1:]
	}
	return []qaOp{{kind: qaExchange, apdu: b}}
}

func (g *qaGen) idList(id uint32) []byte {
	return binary.BigEndian.AppendUint32([]byte{0x5C, 0x03}, id<<8)[:5]
}

func (g *qaGen) selectCmd() []byte {
	switch g.r.IntN(10) {
	case 0:
		return g.encode(0, 0xA4, 4, 0, []byte{0xA0, 0, 0, 3, 8, 0, 0, 0x20}) // another application
	case 1:
		return g.encode(0, 0xA4, qaByte(g.r.IntN(256)), qaByte(g.r.IntN(256)), []byte(qaAIDShort))
	case 2, 3, 4:
		return g.encode(0, 0xA4, 4, 0, []byte(qaAIDFull))
	}
	return g.encode(0, 0xA4, 4, 0, []byte(qaAIDShort))
}

func (g *qaGen) changeData() (ref byte, data []byte) {
	ref = qaByte(0x80 + g.r.IntN(2))
	if g.r.IntN(30) == 0 {
		ref = qaByte(g.r.IntN(256))
	}
	next := g.value(4 + g.r.IntN(6)) // 4 to 9 digits: 4, 5 and 9 are refused
	if g.r.IntN(3) == 0 {
		g.prev = g.m.refOr(ref, g.prev)
	}
	return ref, join(pad(g.refValue(ref)), pad(next))
}

func (m *qaModel) refOr(ref byte, d string) string {
	if m == nil || m.ref(ref) == nil {
		return d
	}
	return m.ref(ref).val
}

func (g *qaGen) simple() []qaOp {
	switch x := g.r.IntN(100); {
	case x < 8:
		return g.one(g.selectCmd())
	case x < 28:
		p := g.pinValue()
		return g.one(g.encode(0, 0x20, 0, 0x80, pad(p)))
	case x < 34:
		switch g.r.IntN(4) {
		case 0: // the reset of the security status, with no data, with a PIN, with another reference, with another P1
			return g.one(g.encode(0, 0x20, 0xFF, 0x80, nil))
		case 1:
			return g.one(g.encode(0, 0x20, 0xFF, 0x80, pad(g.pinValue())))
		case 2:
			return g.one(g.encode(0, 0x20, []byte{0xFF, 0xFE, 0x01}[g.r.IntN(3)], []byte{0x80, 0x81}[g.r.IntN(2)], nil))
		}
		return g.one(g.encode(0, 0x20, 0, 0x80, nil))
	case x < 48:
		ref, data := g.changeData()
		return g.one(g.encode(0, 0x24, 0, ref, data))
	case x < 56:
		ref := qaByte(0x80 + g.r.IntN(2))
		if g.r.IntN(12) == 0 {
			ref = qaByte(g.r.IntN(256))
		}
		return g.one(g.encode(0, 0xF7, 0, ref, nil))
	case x < 64:
		id := qaIDs[g.r.IntN(len(qaIDs))]
		if g.r.IntN(8) == 0 {
			id ^= 0x000100
		}
		list := g.idList(id)
		switch g.r.IntN(12) {
		case 0:
			list[1] = 2
		case 1:
			list[0] = 0x5D
		}
		p1, p2 := byte(0x3F), byte(0xFF)
		if g.r.IntN(15) == 0 {
			p2 = qaByte(g.r.IntN(256))
		}
		return g.one(g.encode(0, 0xCB, p1, p2, list))
	case x < 69:
		return g.one(g.encode(0, 0xC0, 0, 0, nil))
	case x < 73:
		return g.one(g.encode(0, []byte{0xFD, 0xF8}[g.r.IntN(2)], 0, 0, nil))
	case x < 79:
		cla := []byte{0, 0, 0x10, 0x80, 0x00}[g.r.IntN(5)]
		ins := []byte{0xFB, 0xFA, 0xFE}[g.r.IntN(3)]
		data := make([]byte, g.r.IntN(4))
		for i := range data {
			data[i] = qaByte(g.r.IntN(256))
		}
		b := g.encode(cla, ins, qaByte(g.r.IntN(3)), qaByte(g.r.IntN(3)), data)
		if g.r.IntN(5) == 0 {
			b = b[:4]
		}
		return g.one(b)
	}
	return g.mutated()
}

func (g *qaGen) mutated() []qaOp {
	if len(g.hist) == 0 || g.r.IntN(4) == 0 {
		b := make([]byte, g.r.IntN(30))
		for i := range b {
			b[i] = qaByte(g.r.IntN(256))
		}
		return g.one(b)
	}
	b := append([]byte(nil), g.hist[g.r.IntN(len(g.hist))]...)
	switch g.r.IntN(8) {
	case 0:
		if len(b) > 0 {
			b[g.r.IntN(len(b))] = qaByte(g.r.IntN(256))
		}
	case 1:
		if len(b) > 0 {
			i := g.r.IntN(len(b))
			b = append(b[:i], b[i+1:]...)
		}
	case 2:
		i := g.r.IntN(len(b) + 1)
		b = append(b[:i], append([]byte{qaByte(g.r.IntN(256))}, b[i:]...)...)
	case 3:
		b = append(b, qaByte(g.r.IntN(256)), qaByte(g.r.IntN(256)))
	case 4:
		b = b[:g.r.IntN(len(b)+1)]
	case 5:
		if len(b) > 0 {
			b[0] = []byte{0x10, 0x80, 0x01, 0x0C, 0xFF}[g.r.IntN(5)]
		}
	case 6:
		if len(b) > 2 {
			b[2] = qaByte(g.r.IntN(3))
		}
	case 7:
		if len(b) > 3 {
			b[3] = qaByte(g.r.IntN(256))
		}
	}
	return g.one(b)
}

// chained makes one command in two parts, now and then with another command between the parts, or with a changed header at the last part.
func (g *qaGen) chained() []qaOp {
	var ins, p1, p2 byte
	var data []byte
	switch g.r.IntN(4) {
	case 0:
		ins, p2, data = 0x20, 0x80, pad(g.pinValue())
	case 1:
		ins = 0x24
		p2, data = g.changeData()
	case 2:
		ins, p1, p2, data = 0xCB, 0x3F, 0xFF, g.idList(qaIDs[g.r.IntN(len(qaIDs))])
	default:
		ins, p1, data = 0xA4, 4, []byte(qaAIDFull)
	}
	k := g.r.IntN(len(data) + 1)
	ops := g.one(g.encode(0x10, ins, p1, p2, data[:k]))
	if g.r.IntN(4) == 0 {
		ops = append(ops, g.simple()...)
	}
	if g.r.IntN(8) == 0 {
		p2 ^= 1
	}
	return append(ops, g.one(g.encode(0, ins, p1, p2, data[k:]))...)
}

func qaBody(n int) []byte {
	body := make([]byte, n)
	for i := range body {
		body[i] = qaByte(i * 7)
	}
	return body
}

func (g *qaGen) next() []qaOp {
	g.n++
	if g.n == 1 {
		return append([]qaOp{{kind: qaOn}, {kind: qaPut, id: ObjectSlot9A, body: qaBody(300)}, {kind: qaPut, id: ObjectSlot9C, body: qaBody(40)},
			{kind: qaPut, id: ObjectRetired1, body: qaBody(700)}}, g.one(g.encode(0, 0xA4, 4, 0, []byte(qaAIDShort)))...)
	}
	switch x := g.r.IntN(100); {
	case x < 84:
		return g.simple()
	case x < 91:
		return g.chained()
	case x < 94:
		ops := []qaOp{{kind: qaOff}, {kind: qaOn}}
		if g.r.IntN(5) != 0 {
			ops = append(ops, g.one(g.encode(0, 0xA4, 4, 0, []byte(qaAIDShort)))...)
		}
		return ops
	case x < 95:
		return []qaOp{{kind: qaOff}}
	case x < 96:
		return []qaOp{{kind: qaOn}}
	case x < 99:
		return []qaOp{{kind: qaPut, id: qaIDs[g.r.IntN(len(qaIDs))], body: qaBody(1 + g.r.IntN(700))}}
	}
	return []qaOp{{kind: qaPut, id: qaIDs[g.r.IntN(len(qaIDs))]}}
}

// ---- the harness

func qaReplay(o Options, ops []qaOp) *Card {
	c := New(o)
	for _, op := range ops {
		switch op.kind {
		case qaOn:
			c.PowerOn()
		case qaOff:
			c.PowerOff()
		case qaPut:
			c.PutObject(op.id, op.body)
		default:
			c.Exchange(op.apdu)
		}
	}
	return c
}

func qaIsTripwire(ins byte) bool { return ins == 0xFA || ins == 0xFB || ins == 0xFE }

func qaSW(resp []byte) int { return int(resp[len(resp)-2])<<8 | int(resp[len(resp)-1]) }

type qaRun struct {
	stats   map[string]int // what the sequence reached that the coverage test asks for
	ops     []qaOp
	answers [][]byte // nil for a card that is off, and for the ops that are not an exchange
}

// qaDrive runs one seeded sequence of about steps operations on a new card and on the model, and checks the invariants after every step.
func qaDrive(t *testing.T, seed uint64, o Options, steps int) qaRun {
	t.Helper()
	c := New(o)
	m := newQaModel(o)
	g := newQaGen(seed, m)
	run := qaRun{stats: map[string]int{}}
	var prevLog []Entry
	entries, forbidden := 0, 0
	okVerify := false // a right VERIFY answered 9000 in this session since the last SELECT answered 9000
	fail := func(i int, format string, args ...any) {
		t.Helper()
		t.Fatalf("seed %d op %d: %s", seed, i, fmt.Sprintf(format, args...))
	}
	for len(run.ops) < steps {
		for _, op := range g.next() {
			i := len(run.ops)
			run.ops = append(run.ops, op)
			run.answers = append(run.answers, nil)
			pin0, puk0 := c.st().pin, c.st().puk
			var step qaStep
			var resp []byte
			switch op.kind {
			case qaOn:
				c.PowerOn()
				m.power(true)
				okVerify = false
			case qaOff:
				c.PowerOff()
				m.power(false)
				okVerify = false
			case qaPut:
				c.PutObject(op.id, op.body)
				if len(op.body) == 0 {
					delete(m.obj, op.id)
				} else {
					m.obj[op.id] = append([]byte(nil), op.body...)
				}
			default:
				r, wait := c.Exchange(op.apdu)
				resp = r
				if wait != 0 {
					fail(i, "wait %v", wait)
				}
				run.answers[i] = resp
				if !m.on {
					if resp != nil {
						fail(i, "a card that is off answered %x", resp)
					}
					break
				}
				if len(resp) < 2 {
					fail(i, "answer %x to %x is shorter than a status word", resp, op.apdu)
				}
				entries++
				// The doc: the tripwire records an instruction as soon as the INS byte is there (two bytes or more).
				if len(op.apdu) >= 2 && qaIsTripwire(op.apdu[1]) {
					forbidden++
				}
				cmd, ok := qaParse(op.apdu)
				wasVerified, wasBlocked := m.verified, m.pin.left == 0
				step = m.step(cmd, ok)
				if qaSW(resp)>>8 == 0x6C { // R24: an Le shorter than the answer is cut and followed by 61xx, never 6Cxx
					fail(i, "answer %04X to %x", qaSW(resp), op.apdu)
				}
				if step.ran && step.ins == 0x20 && step.p1 == 0 && step.n == 8 && len(cmd.data) == 8 && len(qaUnpad(cmd.data)) < 6 && !wasBlocked && qaSW(resp)>>4 == 0x63C {
					run.stats["a VERIFY block shorter than 6 bytes, a wrong value"]++ // R8
				}
				if step.ran && step.ins == 0x20 && step.p1 == 0xFF && step.p2 == 0x80 {
					kind := map[bool]string{false: "an unverified PIN", true: "a verified PIN"}[wasVerified]
					if wasBlocked {
						kind = "a blocked PIN"
					}
					if step.n == 0 {
						run.stats["reset of "+kind]++
						if qaSW(resp) != 0x9000 || c.st().pin.left != pin0.left {
							fail(i, "the reset of %s answered %04X, counter %d to %d", kind, qaSW(resp), pin0.left, c.st().pin.left)
						}
					} else {
						run.stats["reset with data, "+kind]++
					}
				}
				if step.sw >= 0 {
					if got := qaSW(resp); got != step.sw {
						fail(i, "SW %04X, the model says %04X, for %x", got, step.sw, op.apdu)
					}
					if !bytes.Equal(resp[:len(resp)-2], step.data) {
						fail(i, "data %x, the model says %x, for %x", resp[:len(resp)-2], step.data, op.apdu)
					}
				}
				if step.ran && ((step.ins == 0xA4 && qaSW(resp) == 0x9000) || (step.ins == 0x20 && step.p1 == 0xFF && step.p2 == 0x80 && step.n == 0)) {
					okVerify = false
				}
				if step.ran && step.ins == 0x20 && step.n == 8 && qaSW(resp) == 0x9000 {
					okVerify = true
				}
				// The counters.
				for k, pair := range []struct {
					before, after qaRefState
					touched       bool
				}{
					{qaRefState{left: int(pin0.left), max: int(pin0.max)}, qaRefState{left: int(c.st().pin.left), max: int(c.st().pin.max)}, step.ran && ((step.ins == 0x20 && step.p1 != 0xFF) || (step.ins == 0x24 && step.p2 == 0x80))},
					{qaRefState{left: int(puk0.left), max: int(puk0.max)}, qaRefState{left: int(c.st().puk.left), max: int(c.st().puk.max)}, step.ran && step.ins == 0x24 && step.p2 == 0x81},
				} {
					name := []string{"PIN", "PUK"}[k]
					b, a := pair.before, pair.after
					switch {
					case a.max != b.max || a.left > a.max || a.left < 0:
						fail(i, "%s counter %d of %d after %d of %d", name, a.left, a.max, b.left, b.max)
					case a.left == b.left:
					case !pair.touched:
						fail(i, "the %s counter moved (%d to %d) with no VERIFY or CHANGE for it: %x", name, b.left, a.left, op.apdu)
					case a.left == b.left-1:
						if qaSW(resp) != 0x63C0|a.left {
							fail(i, "a wrong %s went down by one but answered %04X", name, qaSW(resp))
						}
					case a.left == a.max:
						if qaSW(resp) != 0x9000 {
							fail(i, "the %s counter is back at its maximum but the answer is %04X", name, qaSW(resp))
						}
					default:
						fail(i, "the %s counter went from %d to %d (maximum %d)", name, b.left, a.left, a.max)
					}
				}
				if step.ran && step.ins == 0x20 && step.n == 0 && (c.st().pin.left != pin0.left || c.st().puk.left != puk0.left) {
					fail(i, "an empty VERIFY moved a counter")
				}
			}
			qaCheckState(t, seed, i, c, m)
			// The log and the tripwire.
			log := c.Log()
			if len(log) != entries {
				fail(i, "%d log entries, want %d", len(log), entries)
			}
			if len(prevLog) > len(log) || (len(prevLog) > 0 && !reflect.DeepEqual(prevLog, log[:len(prevLog)])) {
				fail(i, "the log changed behind its tail: %+v then %+v", prevLog, log)
			}
			if op.kind == qaExchange && m.on && len(log) > 0 {
				e := log[len(log)-1]
				cmd, ok := qaParse(op.apdu)
				if int(e.SW) != qaSW(resp) || e.Malformed == ok || e.Why != "" {
					fail(i, "entry %+v for %x (answer %04X)", e, op.apdu, qaSW(resp))
				}
				// The header as far as the command had one, and zero for the rest.
				hdr := make([]byte, 4)
				copy(hdr, op.apdu)
				if e.CLA != hdr[0] || e.INS != hdr[1] || e.P1 != hdr[2] || e.P2 != hdr[3] {
					fail(i, "entry %+v for %x", e, op.apdu)
				}
				if ok && e.DataLen != len(cmd.data) || !ok && e.DataLen != 0 {
					fail(i, "entry %+v for %x: the data is %d bytes", e, op.apdu, len(cmd.data))
				}
			}
			prevLog = log
			fl := c.Forbidden()
			if len(fl) != forbidden {
				fail(i, "%d forbidden entries, want %d", len(fl), forbidden)
			}
			for _, e := range fl {
				if !qaIsTripwire(e.INS) || e.Why == "" {
					fail(i, "forbidden entry %+v", e)
				}
			}
			if forbidden > 0 {
				var want []Entry
				for _, e := range log {
					if qaIsTripwire(e.INS) {
						e.Why = fl[len(want)].Why
						want = append(want, e)
					}
				}
				if !reflect.DeepEqual(want, fl) {
					fail(i, "Forbidden %+v does not match the log %+v", fl, log)
				}
			}
			// "PIN verified", seen only through the answers of a card that has had the same history: GET VERSION breaks any chain, then
			// the empty VERIFY.
			twin := qaReplay(o, run.ops)
			twin.Exchange([]byte{0, 0xFD, 0, 0})
			probe, _ := twin.Exchange([]byte{0, 0x20, 0, 0x80})
			switch {
			case !m.on:
				if probe != nil {
					fail(i, "the twin of a card that is off answered %x", probe)
				}
			case !m.selected:
				if qaSW(probe) != 0x6985 {
					fail(i, "the twin before SELECT answered %04X", qaSW(probe))
				}
			case m.verified:
				if qaSW(probe) != 0x9000 || !okVerify {
					fail(i, "the model has the PIN verified: the twin answered %04X, a right VERIFY seen in this session: %v", qaSW(probe), okVerify)
				}
			default:
				if qaSW(probe) != 0x63C0|m.pin.left {
					fail(i, "the PIN is not verified: the twin answered %04X, want %04X", qaSW(probe), 0x63C0|m.pin.left)
				}
			}
		}
	}
	return run
}

// qaCheckState compares the card state, which the model keeps on its own, field by field.
func qaCheckState(t *testing.T, seed uint64, i int, c *Card, m *qaModel) {
	t.Helper()
	for k, p := range []struct {
		got *reference
		ref *qaRefState
	}{{&c.st().pin, &m.pin}, {&c.st().puk, &m.puk}} {
		if string(p.got.value) != p.ref.val || int(p.got.left) != p.ref.left || int(p.got.max) != p.ref.max || p.got.isDefault != p.ref.def {
			t.Fatalf("seed %d op %d: reference %d: card {left %d max %d default %v}, model %+v", seed, i, k, p.got.left, p.got.max, p.got.isDefault, *p.ref)
		}
	}
	if len(c.st().objects) != len(m.obj) {
		t.Fatalf("seed %d op %d: %d objects, the model has %d", seed, i, len(c.st().objects), len(m.obj))
	}
	for id, body := range m.obj {
		if !bytes.Equal(c.st().objects[id], body) {
			t.Fatalf("seed %d op %d: object %06X differs", seed, i, id)
		}
	}
	if c.st().fw != m.fw || c.st().serial != m.serial {
		t.Fatalf("seed %d op %d: firmware or serial changed", seed, i)
	}
}

var qaSeeds = []struct {
	seed uint64
	o    Options
}{
	{1, Options{}},
	{2, Options{ResponseMode: Short61xx}},
	{3, Options{MaxPINTries: 15, MaxPUKTries: 1}},
	{4, Options{NoExtendedAPDU: true, PIN: "7654321", MaxPINTries: 5}},
	{5, Options{PUK: "99999999", MaxPUKTries: 9, ResponseMode: Short61xx}},
	{6, Options{Firmware: [3]byte{5, 7, 2}, Serial: 0x01020304, MaxPINTries: 2, MaxPUKTries: 2}},
	{7, Options{MaxPINTries: 8, NoExtendedAPDU: true, ResponseMode: Short61xx}},
	{8, Options{PIN: "135790", PUK: "24680135", MaxPINTries: 12, MaxPUKTries: 12}},
}

func TestQaRandomSequencesAgreeWithTheReferenceModel(t *testing.T) {
	steps := 300
	if testing.Short() {
		steps = 40
	}
	for _, s := range qaSeeds {
		t.Run(fmt.Sprintf("seed %d", s.seed), func(t *testing.T) {
			qaDrive(t, s.seed, s.o, steps)
		})
	}
}

// TestQaTheSequencesReachWhatTheyAreMeantToReach holds the generator to account: a fixed seed that never reached a blocked PIN, a right
// VERIFY, a chain, a 61xx answer or a forbidden instruction would prove nothing.
func TestQaTheSequencesReachWhatTheyAreMeantToReach(t *testing.T) {
	seen := map[string]int{}
	for _, s := range qaSeeds {
		run := qaDrive(t, s.seed, s.o, 300)
		c := qaReplay(s.o, run.ops)
		if c.st().pin.left == 0 {
			seen["blocked PIN"]++
		}
		if c.st().puk.left == 0 {
			seen["blocked PUK"]++
		}
		if !c.st().pin.isDefault {
			seen["changed PIN"]++
		}
		for i, op := range run.ops {
			if op.kind != qaExchange || run.answers[i] == nil {
				continue
			}
			sw := qaSW(run.answers[i])
			switch {
			case sw == 0x9000 && len(op.apdu) > 1 && op.apdu[1] == 0x20 && len(op.apdu) > 8:
				seen["right VERIFY"]++
			case sw == 0x9000 && len(op.apdu) > 0 && op.apdu[0] == 0x10:
				seen["chained part"]++
			case sw>>8 == 0x61:
				seen["61xx"]++
			case sw == 0x6A82:
				seen["6A82"]++
			case sw == 0x6700:
				seen["6700"]++
			case sw == 0x6E00:
				seen["6E00"]++
			case sw == 0x6985:
				seen["6985"]++
			case sw>>4 == 0x63C:
				seen["63Cx"]++
			}
		}
		for k, n := range run.stats {
			seen[k] += n
		}
		seen["forbidden"] += len(c.Forbidden())
		seen["entries"] += len(c.Log())
	}
	for _, k := range []string{"blocked PIN", "changed PIN", "right VERIFY", "chained part", "61xx", "6A82", "6700", "6E00", "6985", "63Cx", "forbidden",
		"reset of a verified PIN", "reset of an unverified PIN", "reset of a blocked PIN", "reset with data, a verified PIN", "a VERIFY block shorter than 6 bytes, a wrong value"} {
		if seen[k] == 0 {
			t.Errorf("no sequence reached %q; seen: %v", k, seen)
		}
	}
	t.Logf("reached: %v", seen)
}

// TestQaTwoCardsWithTheSameOptionsAndTheSameSequenceAreIdentical: the same options and the same commands give byte-identical answers, logs
// and forbidden lists, and the card has no state that a second card does not.
func TestQaTwoCardsWithTheSameOptionsAndTheSameSequenceAreIdentical(t *testing.T) {
	for _, s := range qaSeeds {
		run := qaDrive(t, s.seed, s.o, 200)
		a, b := New(s.o), New(s.o)
		for i, op := range run.ops {
			switch op.kind {
			case qaOn:
				if !bytes.Equal(a.PowerOn(), b.PowerOn()) {
					t.Fatalf("seed %d op %d: different answers to reset", s.seed, i)
				}
			case qaOff:
				a.PowerOff()
				b.PowerOff()
			case qaPut:
				a.PutObject(op.id, op.body)
				b.PutObject(op.id, op.body)
			default:
				ra, wa := a.Exchange(op.apdu)
				rb, wb := b.Exchange(op.apdu)
				if !bytes.Equal(ra, rb) || wa != wb || !bytes.Equal(ra, run.answers[i]) || (ra == nil) != (run.answers[i] == nil) {
					t.Fatalf("seed %d op %d: %x, %x and %x for %x", s.seed, i, ra, rb, run.answers[i], op.apdu)
				}
			}
		}
		if !reflect.DeepEqual(a.Log(), b.Log()) || !reflect.DeepEqual(a.Forbidden(), b.Forbidden()) {
			t.Errorf("seed %d: the logs differ", s.seed)
		}
		// The same sequence again, from the same seed, gives the same operations.
		again := qaDrive(t, s.seed, s.o, 200)
		if len(again.ops) != len(run.ops) || !reflect.DeepEqual(again.answers, run.answers) {
			t.Errorf("seed %d: a second run of the generator differs", s.seed)
		}
	}
}
