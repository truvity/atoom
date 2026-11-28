package pivtest

import (
	"bytes"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

// The nine faults that the review of this package found were fixed (see the commit that fixed them). Each test here proves one fix from the
// side of the review: what doc.go and the comments of the code promise, held to the code. Where the implementer's own test of the fix (in
// fixes_test.go) holds exactly the same thing, this file does not hold it twice.

// Fault 1. Entry says: "the header is the bytes that were there, the others zero". The tripwire sees the INS as soon as it is there.
func TestQaAShortCommandIsLoggedWithTheHeaderItHadAndTheTripwireSeesItsINS(t *testing.T) {
	for _, cla := range []byte{0x00, 0x10, 0x80} {
		for _, ins := range []byte{insReset, insSetRetries, insImportKey} {
			c := newSelected(t, Options{})
			for _, apdu := range [][]byte{{cla, ins}, {cla, ins, 0x07}, {ins}, nil} {
				wantSW(t, fmt.Sprintf("%x", apdu), qaSWOf(t, c, apdu), swWrongLength)
			}
			want := []Entry{
				{CLA: cla, INS: ins, SW: swWrongLength, Malformed: true},
				{CLA: cla, INS: ins, P1: 0x07, SW: swWrongLength, Malformed: true},
				{CLA: ins, SW: swWrongLength, Malformed: true},
				{SW: swWrongLength, Malformed: true},
			}
			log := c.Log()[1:]
			for i := range want {
				if log[i] != want[i] {
					t.Errorf("CLA %02X INS %02X: entry %d = %+v, want %+v", cla, ins, i, log[i], want[i])
				}
			}
			f := c.Forbidden()
			if len(f) != 2 {
				t.Fatalf("CLA %02X INS %02X: Forbidden = %+v, want the two commands that had an INS", cla, ins, f)
			}
			for i := range f {
				if f[i].Why == "" {
					t.Errorf("a forbidden entry has no reason: %+v", f[i])
				}
				f[i].Why = ""
				if f[i] != want[i] {
					t.Errorf("forbidden entry %d = %+v, want %+v", i, f[i], want[i])
				}
			}
			var ft fakeT
			RequireNoForbidden(&ft, c)
			if !ft.failed || !strings.Contains(ft.msg, fmt.Sprintf("INS %02X", ins)) {
				t.Errorf("RequireNoForbidden did not fail for a short command: %q", ft.msg)
			}
		}
	}
	t.Run("no other INS is recorded, however short the command", func(t *testing.T) {
		c := newSelected(t, Options{})
		for ins := 0; ins < 256; ins++ {
			if !qaIsTripwire(byte(ins)) {
				c.Exchange([]byte{0, byte(ins)})
				c.Exchange([]byte{0, byte(ins), 1})
			}
		}
		if f := c.Forbidden(); len(f) != 0 {
			t.Errorf("Forbidden = %+v", f[:min(len(f), 3)])
		}
	})
	t.Run("before SELECT, and not when the card is off", func(t *testing.T) {
		c := New(Options{})
		c.PowerOn()
		c.Exchange([]byte{0, insReset})
		c.PowerOff()
		c.Exchange([]byte{0, insReset})
		if f := c.Forbidden(); len(f) != 1 || f[0].SW != swWrongLength {
			t.Errorf("Forbidden = %+v", f)
		}
	})
}

// Fault 2. R21: "a chain of more than 65535 bytes, counting the last part, answers 6700".
func TestQaAChainOf65536BytesIsRefusedWhicheverPartMakesItSo(t *testing.T) { // assumed: R21
	seeded := func() *Card {
		c := newSelected(t, Options{})
		c.PutObject(ObjectSlot9A, []byte{0x53, 0x01, 0xAB})
		return c
	}
	last := short(claPlain, insGetData, 0x3F, 0xFF, []byte{0x5F, 0xC1, 0x05}, 0)
	for _, tc := range []struct {
		name  string
		parts []int // the sizes of the parts with CLA 10
		last  int   // the size of the last part
		want  uint16
	}{
		{"65535 in one part, an empty last part", []int{65535}, 0, swWrongData},
		{"65535 in one part, one byte in the last", []int{65535}, 1, swWrongLength},
		{"65535 in one part, 255 bytes in the last", []int{65535}, 255, swWrongLength},
		{"65535 in one part, 256 bytes in the last (extended)", []int{65535}, 256, swWrongLength},
		{"two parts that make 65535, an empty last part", []int{32768, 32767}, 0, swWrongData},
		{"two parts that make 65535, one byte in the last", []int{32768, 32767}, 1, swWrongLength},
		{"three parts, the last makes it 65536", []int{30000, 30000, 5535}, 1, swWrongLength},
		{"three parts, the last makes it 65535", []int{30000, 30000, 5534}, 1, swWrongData},
		{"no parts: a last part alone is not a chain", nil, 255, swWrongData},
	} {
		c := seeded()
		for _, n := range tc.parts {
			wantSW(t, tc.name+": a part", qaSWOf(t, c, extended(claChain, insGetData, 0x3F, 0xFF, make([]byte, n), -1)), swOK)
		}
		var apdu []byte
		switch {
		case tc.last == 0:
			apdu = []byte{claPlain, insGetData, 0x3F, 0xFF}
		case tc.last <= 255:
			apdu = short(claPlain, insGetData, 0x3F, 0xFF, make([]byte, tc.last), -1)
		default:
			apdu = extended(claPlain, insGetData, 0x3F, 0xFF, make([]byte, tc.last), -1)
		}
		wantSW(t, tc.name, qaSWOf(t, c, apdu), tc.want)
		if c.st().chain != nil {
			t.Errorf("%s: a chain is left open", tc.name)
		}
		// whatever came of it, the chain is used up: the same last part again runs alone
		wantSW(t, tc.name+": the chain is gone", qaSWOf(t, c, last), swWrongData)
	}
	t.Run("a last part with another header is not counted: it runs alone", func(t *testing.T) {
		c := seeded()
		wantSW(t, "a part", qaSWOf(t, c, extended(claChain, insGetData, 0x3F, 0xFF, make([]byte, 65535), -1)), swOK)
		data, sw := send(t, c, short(claPlain, insGetVersion, 0, 0, nil, 0))
		if sw != swOK || len(data) != 3 {
			t.Errorf("GET VERSION: %x %04X", data, sw)
		}
	})
	t.Run("the refused command spends nothing", func(t *testing.T) {
		c := newSelected(t, Options{})
		wantSW(t, "a part", qaSWOf(t, c, extended(claChain, insVerify, 0, refPIN, make([]byte, 65535), -1)), swOK)
		wantSW(t, "the last part", qaSWOf(t, c, short(claPlain, insVerify, 0, refPIN, pad(DefaultPIN), -1)), swWrongLength)
		if c.st().pin.left != 3 {
			t.Error("a refused chain spent a try")
		}
	})
}

// qaVerbs are the verbs a person might type, with the flags, width and precision that go with them.
func qaVerbs() []string {
	var out []string
	for r := 'a'; r <= 'z'; r++ {
		out = append(out, "%"+string(r), "%+"+string(r), "%#"+string(r), "%-12"+string(r), "%8.3"+string(r), "%08"+string(r), "% "+string(r))
	}
	for r := 'A'; r <= 'Z'; r++ {
		out = append(out, "%"+string(r), "%#"+string(r))
	}
	return append(out, "%[1]v", "%[1]s", "%*d", "%.*s", "%!", "%%v")
}

// qaOptionsBadVerb says whether fmt keeps a verb from the Format method of a value: %T and %p are answered by fmt itself, and %w is a verb
// of Errorf that, with something that is no error, is a bad verb, which fmt prints by the fields of the value.
func qaOptionsBadVerb(verb string) bool {
	return strings.HasSuffix(verb, "T") || strings.HasSuffix(verb, "p") || strings.HasSuffix(verb, "w")
}

// Fault 3, in its final shape. doc.go: "Printing a Card, by value or by pointer, with any verb, shows at most its firmware, serial number and
// counters and nothing of the PIN, the PUK or the objects: a Card is one func value that returns its state, a kind that fmt never follows". Every verb, including the bad ones (%p, %w,
// %T), a Card as a value, a pointer, a copy, in any, in a struct (exported field or not), a slice, an array, a map, and in the text of an error.
func TestQaNoPrintOfACardShowsASecret(t *testing.T) {
	mk := func(pin, puk string, body []byte) *Card {
		c := New(Options{PIN: pin, PUK: puk})
		c.PutObject(ObjectSlot9A, body)
		c.PutObject(ObjectSlot9C, body)
		return c
	}
	a := mk(string(qaPIN), string(qaPUK), qaSecretBody)
	b := mk("654321", "87654321", []byte("another body of two objects"))
	// A Card in a field that is not exported is never asked for its methods, and a verb that is bad for a pointer makes fmt print the value
	// again from depth zero: the state of a Card is held behind a func, which fmt never follows, so what it prints is an address.
	type holder struct {
		Exported *Card
		hidden   *Card
		Value    Card
		value    Card
		Any      any
		hiddenAn any
		hiddenAv any
		Fn       func() Card
		fn       func() Card
		ch       chan Card
		inner    struct{ cards []Card }
	}
	h := holder{Exported: a, hidden: a, Value: *a, value: *a, Any: a, hiddenAn: a, hiddenAv: *a, Fn: func() Card { return *a }, fn: func() Card { return *a },
		ch: make(chan Card, 1)}
	h.inner.cards = []Card{*a}
	h.ch <- *a
	type env struct{ card Card }
	type envAny struct{ card any }
	type envDeep struct {
		name string
		in   struct{ cards []Card }
	}
	deep := envDeep{name: "x"}
	deep.in.cards = []Card{*a}
	copied := *a
	targets := map[string]any{
		"a pointer": a, "a value": *a, "a copy": copied, "a pointer to a copy": &copied, "an any": any(a), "a pointer to a pointer": &a,
		"a slice of pointers": []*Card{a}, "a slice of values": []Card{*a}, "an array": [1]Card{*a}, "a map of values": map[string]Card{"card": *a},
		// A Card cannot be a map key or be compared (it holds a func, and a func is not comparable): `map[Card]int` does not compile. A map
		// whose values are maps of cards holds the same proof from the other side.
		"a map of maps of cards": map[int]map[string]Card{1: {"card": *a}}, "a struct": h,
		"a struct with a Card in an unexported field": env{*a}, "a pointer to such a struct": &env{*a}, "a struct with an any holding a Card": envAny{*a},
		"a struct with a slice of Cards in a struct": deep, "a slice of such structs": []env{{*a}}, "a map of such structs": map[string]env{"a": {*a}},
		"a map of pointers to such structs": map[string]*env{"a": {*a}}, "an array of such structs": [2]env{{*a}, {*a}}, "an any holding such a struct": any(env{*a}),
		"a pointer to a pointer to such a struct": func() any { p := &env{*a}; return &p }(), "a pointer to a struct": &h, "two levels": [][]Card{{*a}},
		"a channel of cards": make(chan Card), "a func": func() Card { return *a },
	}
	for name, v := range targets {
		for _, verb := range qaVerbs() {
			if got := qaLeaks(fmt.Sprintf(verb, v)); len(got) != 0 {
				t.Errorf("%s of %s prints %v", verb, name, got)
			}
		}
	}
	// Fprint* to a buffer and Append*, with every verb
	var buf bytes.Buffer
	for name, v := range targets {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%d", "%x", "%a", "%w", "%p", "%t", "%8.3s", "%-12v", "% x", "%#x", "%U"} {
			buf.Reset()
			_, _ = fmt.Fprintf(&buf, verb, v)
			fmt.Fprint(&buf, v)
			fmt.Fprintln(&buf, v, *a)
			buf.Write(fmt.Appendf(nil, verb, v))
			buf.Write(fmt.Append(nil, v))
			buf.Write(fmt.Appendln(nil, v))
			if got := qaLeaks(buf.String()); len(got) != 0 {
				t.Errorf("Fprint, Append and %s of %s print %v", verb, name, got)
			}
		}
	}
	// fmt's own bad verbs, and the text of an error that wraps a card by mistake
	w := "%w"
	for name, got := range map[string]string{
		"%w of a pointer": fmt.Sprintf(w, a), "%w of a value": fmt.Sprintf(w, *a), "Errorf %w of a pointer": fmt.Errorf(w, a).Error(),
		"Errorf %w of a value": fmt.Errorf(w, *a).Error(), "Errorf %w of a struct": fmt.Errorf(w, h).Error(), "Errorf %w of a struct with a hidden Card": fmt.Errorf(w, env{*a}).Error(),
		"Errorf %s of a struct with a hidden Card": fmt.Errorf("%s", env{*a}).Error(), "Errorf %q of a hidden Card in an any": fmt.Errorf("%q", envAny{*a}).Error(),
		"Sprintf %s of a slice": fmt.Sprintf("%s", []env{{*a}}), "Errorf %v": fmt.Errorf("x %v", a).Error(),
	} {
		if leaks := qaLeaks(got); len(leaks) != 0 {
			t.Errorf("%s prints %v: %.100s", name, leaks, got)
		}
	}
	for _, text := range []string{fmt.Sprint(a), fmt.Sprintln(a), fmt.Sprint("x", a, 1, *a), fmt.Sprintf("%v %v", a, b)} {
		if got := qaLeaks(text); len(got) != 0 {
			t.Errorf("Sprint of the card prints %v: %q", got, text)
		}
	}
	// What the methods of fmt give is the same text for two cards that differ only in their secrets, by value and by pointer.
	for _, verb := range qaVerbs() {
		if strings.HasSuffix(verb, "p") || strings.HasSuffix(verb, "w") {
			continue // an address, and a bad verb that prints one
		}
		for name, pair := range map[string][2]any{"a pointer": {a, b}, "a value": {*a, *b}} {
			if ta, tb := fmt.Sprintf(verb, pair[0]), fmt.Sprintf(verb, pair[1]); ta != tb {
				t.Errorf("%s of %s prints different texts for two cards that differ only in their secrets:\n%s\n%s", verb, name, ta, tb)
			}
		}
	}
	// What a bad verb prints of a card is an address: no byte of the state is in it.
	for _, verb := range []string{"%w", "%p"} {
		got := fmt.Sprintf(verb, *a)
		if strings.Contains(got, "[") || strings.Contains(got, "map") || strings.Contains(got, "false") || strings.Contains(got, "true") {
			t.Errorf("%s of a Card value prints more than an address: %q", verb, got)
		}
	}
	// It tells what a test needs to see, and follows the counters.
	a.PowerOn()
	selectPIV(t, a)
	a.Exchange(verifyCmd("000000"))
	if got := fmt.Sprintf("%v", *a); !strings.Contains(got, "PIN tries 2 of 3") || !strings.Contains(got, "2 objects") || !strings.Contains(got, "5.8.0") ||
		!strings.Contains(got, "7654321") {
		t.Errorf("the card prints %q", got)
	}
	var nilCard *Card
	if got := fmt.Sprintf("%v", nilCard); got == "" {
		t.Error("a nil card prints nothing")
	}
	var zero Card
	if got := fmt.Sprintf("%v", zero); !strings.Contains(got, "not made by New") {
		t.Errorf("a zero Card prints %q", got)
	}
}

// doc.go: "A Card is a handle: it holds one unexported func value that returns the state ... A copy of a Card is a second handle to the SAME card ... The zero
// Card is no card: its methods panic."
func TestQaACardIsOneHandleACopyIsTheSameCardAndTheZeroCardPanics(t *testing.T) {
	typ := reflect.TypeOf(Card{})
	if typ.NumField() != 1 || typ.Field(0).Type.Kind() != reflect.Func || typ.Field(0).IsExported() {
		t.Errorf("a Card is not one unexported func: %v", typ)
	}
	if typ.Comparable() {
		t.Error("a Card is comparable: it holds a func, which fmt never follows, and the doc says it cannot be compared")
	}
	a := New(Options{})
	b := *a // a second handle
	if a.st() != b.st() {
		t.Error("a copy of a Card is not the same card")
	}
	b.PowerOn()
	selectPIV(t, &b)
	wantSW(t, "a wrong PIN through the copy", qaSWOf(t, &b, verifyCmd("000000")), 0x63C2)
	wantSW(t, "the counter, seen through the first handle", qaSWOf(t, a, emptyVerify()), 0x63C2)
	if len(a.Log()) != len(b.Log()) || len(a.Log()) != 3 {
		t.Errorf("the log of the two handles: %d and %d entries", len(a.Log()), len(b.Log()))
	}
	if other := New(Options{}); other.st() == a.st() {
		t.Error("two cards made by New are the same")
	}
	var zero Card
	for name, f := range map[string]func(){
		"PowerOn":   func() { zero.PowerOn() },
		"PowerOff":  func() { zero.PowerOff() },
		"Exchange":  func() { zero.Exchange([]byte{0, 0xA4, 4, 0}) },
		"PutObject": func() { zero.PutObject(ObjectSlot9A, []byte{1}) },
		"Log":       func() { zero.Log() },
		"Forbidden": func() { zero.Forbidden() },
	} {
		if text := panicText(f); !strings.Contains(text, "the zero Card is no card") {
			t.Errorf("%s of the zero Card: panic %q", name, text)
		}
	}
}

func TestQaPrintingOptionsWithEveryVerbShowsNeitherThePINNorThePUK(t *testing.T) {
	o := Options{PIN: string(qaPIN), PUK: string(qaPUK), MaxPINTries: 9, Serial: 42}
	other := Options{PIN: "654321", PUK: "87654321", MaxPINTries: 9, Serial: 42}
	for _, verb := range qaVerbs() {
		if qaOptionsBadVerb(verb) {
			continue // fmt does not hand them to a Formatter, and Options keeps its secrets in the clear: see TestQaOptionsCarryThePINAndThePUKAndTheDocSaysSo
		}
		for name, pair := range map[string][2]any{"a value": {o, other}, "a pointer": {&o, &other}, "a slice": {[]Options{o}, []Options{other}},
			"a map": {map[string]Options{"a": o}, map[string]Options{"a": other}}} {
			ta, tb := fmt.Sprintf(verb, pair[0]), fmt.Sprintf(verb, pair[1])
			if got := qaLeaks(ta); len(got) != 0 {
				t.Errorf("%s of %s prints %v: %s", verb, name, got, ta)
			}
			if ta != tb {
				t.Errorf("%s of %s prints the PIN or the PUK: %q and %q differ", verb, name, ta, tb)
			}
		}
	}
	if got := fmt.Sprint(o); !strings.Contains(got, "serial 42") || !strings.Contains(got, "max tries 9") || strings.Contains(got, "default") && strings.Contains(got, "PIN default") {
		t.Errorf("options print %q", got)
	}
}

// Fault 4. "A value never ends in FF (New refuses one that does)": for every length, for the PIN and for the PUK, with no echo of the value.
func TestQaNewRefusesAValueThatEndsInFFAndAllowsOneThatHasFFInside(t *testing.T) {
	for n := 6; n <= 8; n++ {
		for _, name := range []string{"PIN", "PUK"} {
			for _, v := range []string{strings.Repeat("\x31", n-1) + "\xff", strings.Repeat("\xff", n), "\x91\x82\x73\x64\x55\x46\x37"[:n-1] + "\xff"} {
				o := Options{PIN: v}
				if name == "PUK" {
					o = Options{PUK: v}
				}
				text := panicText(func() { New(o) })
				if !strings.Contains(text, name) {
					t.Errorf("%s of %d bytes ending in FF (%x): panic text %q", name, n, v, text)
				}
				if strings.Contains(text, v) || strings.Contains(text, fmt.Sprintf("%x", v)) || strings.Contains(text, listed(v)) {
					t.Errorf("the panic text holds the value: %q", text)
				}
			}
		}
	}
	for n := 6; n <= 8; n++ {
		for pos := 0; pos < n-1; pos++ {
			v := []byte(strings.Repeat("7", n))
			v[pos] = 0xFF
			c := newSelected(t, Options{PIN: string(v), PUK: string(v)})
			wantSW(t, fmt.Sprintf("VERIFY, an FF at %d of %d", pos, n), qaSWOf(t, c, short(0, insVerify, 0, refPIN, pad(string(v)), -1)), swOK)
			wantSW(t, fmt.Sprintf("CHANGE of a PUK, an FF at %d of %d", pos, n), qaSWOf(t, c, changeCmd(refPUK, string(v), "87654321")), swOK)
			// and as a new value
			wantSW(t, "CHANGE to a value with an FF inside", qaSWOf(t, c, changeCmd(refPIN, string(v), string(v[:n-1])[:0]+"ab\xffcde")), swOK)
			wantSW(t, "VERIFY of it", qaSWOf(t, c, verifyCmd("ab\xffcde")), swOK)
		}
	}
	// A new value that ends in FF on the wire is shorter than it looks: it is the value without the padding, and is stored so.
	c := newSelected(t, Options{})
	wantSW(t, "CHANGE", qaSWOf(t, c, short(0, insChange, 0, refPIN, join(pad(DefaultPIN), []byte{'a', 'b', 'c', 'd', 'e', 'f', 0xFF, 0xFF}), -1)), swOK)
	wantSW(t, "VERIFY of the 6 bytes", qaSWOf(t, c, verifyCmd("abcdef")), swOK)
}

// Fault 5. R8: GET DATA "takes the one form of the tag list 5C 03 <id>", in every form the APDU may take and in a chain.
func TestQaTheTagListIsServedInItsOneFormInEveryShapeOfTheAPDU(t *testing.T) { // assumed: R8
	obj := []byte{0x53, 0x01, 0xAB}
	list := []byte{0x5C, 0x03, 0x5F, 0xC1, 0x05}
	c := newSelected(t, Options{})
	c.PutObject(ObjectSlot9A, obj)
	for _, f := range qaForms(insGetData, 0x3F, 0xFF, list, 0, 0)[3:] {
		if data, sw := send(t, c, f.apdu); sw != swOK || !bytes.Equal(data, obj) {
			t.Errorf("%s: %x %04X", f.name, data, sw)
		}
	}
	wantSW(t, "a part", qaSWOf(t, c, short(claChain, insGetData, 0x3F, 0xFF, list[:3], -1)), swOK)
	if data, sw := send(t, c, short(claPlain, insGetData, 0x3F, 0xFF, list[3:], 0)); sw != swOK || !bytes.Equal(data, obj) {
		t.Errorf("a chain: %x %04X", data, sw)
	}
	for name, bad := range map[string][]byte{
		"81":                {0x5C, 0x81, 0x03, 0x5F, 0xC1, 0x05},
		"82":                {0x5C, 0x82, 0x00, 0x03, 0x5F, 0xC1, 0x05},
		"83":                {0x5C, 0x83, 0x00, 0x00, 0x03, 0x5F, 0xC1, 0x05},
		"80":                {0x5C, 0x80, 0x5F, 0xC1, 0x05},
		"a list of two ids": {0x5C, 0x06, 0x5F, 0xC1, 0x05, 0x5F, 0xC1, 0x0A},
	} {
		for _, form := range []string{"short", "extended", "chained"} {
			var apdu []byte
			switch form {
			case "short":
				apdu = short(0, insGetData, 0x3F, 0xFF, bad, 0)
			case "extended":
				apdu = extended(0, insGetData, 0x3F, 0xFF, bad, 0)
			default:
				send(t, c, short(claChain, insGetData, 0x3F, 0xFF, bad[:2], -1))
				apdu = short(claPlain, insGetData, 0x3F, 0xFF, bad[2:], 0)
			}
			wantSW(t, "the length form "+name+", "+form, qaSWOf(t, c, apdu), swWrongData)
		}
	}
}

// Fault 6. doc.go says what VERIFY and CHANGE REFERENCE DATA answer: 63Cx, 6983 and 9000. Each statement, held to the code for the PIN by
// VERIFY and for the PIN and the PUK by CHANGE, for several maxima.
func TestQaVerifyAndChangeAnswer63Cx6983And9000AsTheDocSays(t *testing.T) {
	text := strings.Join(strings.Fields(strings.ReplaceAll(qaRead(t, "doc.go"), "//", " ")), " ")
	for _, want := range []string{"VERIFY and CHANGE REFERENCE DATA answer 63Cx for a wrong value, with x the tries left", "6983 for a PIN or a PUK that is blocked",
		"also for the right value", "an empty VERIFY of a blocked PIN reads the counter: 63C0", "9000 for a right value, which brings the counter back to its maximum"} {
		if !strings.Contains(text, want) {
			t.Errorf("doc.go does not say %q", want)
		}
	}
	type op struct {
		name string
		cmd  func(value string) []byte
		def  string
		left func(c *Card) byte
		max  func(c *Card) byte
	}
	ops := []op{
		{"VERIFY", verifyCmd, DefaultPIN, func(c *Card) byte { return c.st().pin.left }, func(c *Card) byte { return c.st().pin.max }},
		{"CHANGE PIN", func(v string) []byte { return changeCmd(refPIN, v, "654321") }, DefaultPIN, func(c *Card) byte { return c.st().pin.left }, func(c *Card) byte { return c.st().pin.max }},
		{"CHANGE PUK", func(v string) []byte { return changeCmd(refPUK, v, "87654321") }, DefaultPUK, func(c *Card) byte { return c.st().puk.left }, func(c *Card) byte { return c.st().puk.max }},
	}
	for _, o := range ops {
		for _, most := range []byte{1, 3, 15} {
			c := newSelected(t, Options{MaxPINTries: most, MaxPUKTries: most})
			for want := int(most) - 1; want >= 0; want-- {
				wantSW(t, fmt.Sprintf("%s, a wrong value, %d tries left", o.name, want), qaSWOf(t, c, o.cmd("000000")), swTriesLeft|qaLow(want))
				if o.left(c) != qaByte(want) {
					t.Errorf("%s: the counter is %d, want %d", o.name, o.left(c), want)
				}
			}
			wantSW(t, o.name+", blocked, the right value", qaSWOf(t, c, o.cmd(o.def)), swBlocked)
			wantSW(t, o.name+", blocked, a wrong value", qaSWOf(t, c, o.cmd("000000")), swBlocked)
			if o.left(c) != 0 {
				t.Errorf("%s: a blocked counter moved to %d", o.name, o.left(c))
			}
		}
		c := newSelected(t, Options{})
		qaSWOf(t, c, o.cmd("000000"))
		wantSW(t, o.name+", the right value", qaSWOf(t, c, o.cmd(o.def)), swOK)
		if o.left(c) != o.max(c) {
			t.Errorf("%s: the counter is %d after a right value, want %d", o.name, o.left(c), o.max(c))
		}
	}
	// An empty VERIFY of a blocked PIN reads the counter.
	c := newSelected(t, Options{MaxPINTries: 1})
	qaSWOf(t, c, verifyCmd("000000"))
	wantSW(t, "an empty VERIFY of a blocked PIN", qaSWOf(t, c, emptyVerify()), swTriesLeft)
}

// Fault 7. "CertificateObject ... panics for a certificate longer than MaxCertificateDER (65526 bytes) ... and a corrupt object is never
// returned."
func TestQaCertificateObjectPanicsAboveTheMaximumAndNeverReturnsACorruptObject(t *testing.T) {
	if MaxCertificateDER != 65526 {
		t.Fatalf("MaxCertificateDER = %d, doc.go says 65526", MaxCertificateDER)
	}
	for n := MaxCertificateDER - 300; n <= MaxCertificateDER+300; n++ {
		if n > MaxCertificateDER-6 && n < MaxCertificateDER+6 || n%97 == 0 {
			der := bytes.Repeat([]byte{0xA7}, n)
			var obj []byte
			text := panicText(func() { obj = CertificateObject(der) })
			if n > MaxCertificateDER {
				if obj != nil || !strings.Contains(text, fmt.Sprint(n)) || !strings.Contains(text, fmt.Sprint(MaxCertificateDER)) {
					t.Errorf("n = %d: panic %q, object of %d bytes", n, text, len(obj))
				}
				if strings.Contains(text, "a7a7") || strings.Contains(text, "167 167") {
					t.Errorf("n = %d: the panic text holds the certificate: %q", n, text)
				}
				continue
			}
			if text != "" {
				t.Errorf("n = %d: panic %q", n, text)
				continue
			}
			tag, outer, rest, ok := readTLV(obj)
			if !ok || tag != 0x53 || len(rest) != 0 {
				t.Errorf("n = %d: the object is not one 53", n)
				continue
			}
			tag, cert, rest, ok := readTLV(outer)
			if !ok || tag != 0x70 || !bytes.Equal(cert, der) || !bytes.Equal(rest, []byte{0x71, 0x01, 0x00, 0xFE, 0x00}) {
				t.Errorf("n = %d: the certificate in the object is corrupt", n)
			}
		}
	}
	// The largest object, through the card, in both forms of the answer.
	der := qaPattern(MaxCertificateDER)
	obj := CertificateObject(der)
	if len(obj) != 0xFFFF+4 {
		t.Errorf("the largest object has %d bytes, want %d", len(obj), 0xFFFF+4)
	}
	for _, mode := range []ResponseMode{Extended, Short61xx} {
		c := newSelected(t, Options{ResponseMode: mode})
		c.PutObject(ObjectSlot9A, obj)
		first := extended(0, insGetData, 0x3F, 0xFF, []byte{0x5C, 3, 0x5F, 0xC1, 5}, 0)
		qaDrain(t, fmt.Sprintf("the largest certificate object, mode %d", mode), c, first, obj, map[ResponseMode]int{Extended: 65536, Short61xx: 256}[mode],
			func() ([]byte, int) { return getResponseCmd(0), 256 })
	}
}

// Fault 9. The scanners that were bypassed (the review found 15 forms) are now caught by the allow-list.
var qaOldBypasses = map[string]string{
	"a dot import of time": `package x
import . "time"
func f() { _ = Now(); Sleep(1) }`,
	"time imported twice under two names": `package x
import (
	"time"
	t "time"
)
func f() { _ = time.Now(); _ = t.Duration(0) }`,
	"context.WithTimeout": `package x
import "context"
func f() { _, _ = context.WithTimeout(context.Background(), 1) }`,
	"context.WithDeadline": `package x
import (
	"context"
	"time"
)
func f() { _, _ = context.WithDeadline(context.Background(), time.Time{}) }`,
	"a go statement": `package x
func f() { go func() {}() }`,
	"a device path in two strings": `package x
var p = "/dev/" + "bus/usb"`,
	"a device path of a reader": `package x
var p = "/dev/hidraw0"`,
	"a serial device": `package x
var p = "/dev/ttyUSB0"`,
	"the socket of the smart-card service": `package x
var p = "/run/pcscd/pcscd.comm"`,
	"a clock library of another module": `package x
import "example.com/clock"
var f = clock.Now`,
	"io/ioutil": `package x
import "io/ioutil"
var f = ioutil.ReadFile`,
	"path/filepath": `package x
import "path/filepath"
var f = filepath.Glob`,
	"log": `package x
import "log"
func f() { log.Println("x") }`,
	"cgo": `package x
import "C"`,
	"time.LoadLocation": `package x
import "time"
var f = time.LoadLocation`,
	"a channel made of a type parameter": `package x
type C[T any] chan T`,
	"a select that waits for ever": `package x
func f() { select {} }`,
	"runtime": `package x
import "runtime"
func f() { runtime.Gosched() }`,
	"unique": `package x
import "unique"
var _ = unique.Make[int]`,
	"an alias of an allowed package, twice": `package x
import (
	"fmt"
	f2 "fmt"
)
var _, _ = fmt.Sprint, f2.Sprint`,
	"a dot import of an allowed package": `package x
import . "fmt"
var _ = Sprint`,
	"time.After in a value": `package x
import "time"
var _ = time.After`,
}

func TestQaTheAllowListCatchesWhatGotPastTheOldScanner(t *testing.T) {
	for name, src := range qaOldBypasses {
		problems, err := sourceProblems("x.go", []byte(src))
		if err != nil {
			t.Errorf("%s: %v", name, err)
		} else if len(problems) == 0 {
			t.Errorf("not caught: %s", name)
		}
	}
	// What the allow-list lets through, so that it is not a list of everything.
	for name, src := range map[string]string{
		"time.Duration and every constant of it": `package x
import "time"
var _ = []time.Duration{time.Nanosecond, time.Microsecond, time.Millisecond, time.Second, time.Minute, time.Hour}`,
		"fmt, bytes, binary and sync": `package x
import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sync"
)
var _ = fmt.Sprintf("%d", binary.BigEndian.Uint16([]byte{1, 2}))
var _ = bytes.Equal(nil, nil)
var _ sync.Mutex`,
		"a source with no import": "package x\nvar a = 1\n",
		"a string with a slash":   "package x\nvar a = \"a/b\"\nvar b = \"/usr/bin\"\n",
	} {
		if problems, err := sourceProblems("x.go", []byte(src)); err != nil || len(problems) != 0 {
			t.Errorf("%s: %q %v", name, problems, err)
		}
	}
	// The list of the allowed is the list that the doc of the check says, and nothing more.
	if strings.Join(allowedImports, " ") != "bytes encoding/binary fmt sync time" {
		t.Errorf("allowedImports = %v", allowedImports)
	}
	if strings.Join(durationNames, " ") != "Duration Nanosecond Microsecond Millisecond Second Minute Hour" {
		t.Errorf("durationNames = %v", durationNames)
	}
}

// The ruling on Options: "Options is a plain struct, the caller's own input, and it carries the PIN and the PUK in the clear: a test that
// prints its Options, or a struct that holds them, prints them, whatever verb it uses." The doc says so, and this test shows it by example, so
// that nobody is surprised. It is DECIDED and not a fault: Options is what the caller wrote, and no method can reach every route of fmt (a
// bad verb is printed with the methods switched off, and a struct field that is not exported is never asked for its methods). A Card is
// different because it is one func value that returns its state, and fmt prints a func as an address, with every verb, in a field or not: see TestQaNoPrintOfACardShowsASecret.
func TestQaOptionsCarryThePINAndThePUKAndTheDocSaysSo(t *testing.T) {
	text := strings.Join(strings.Fields(strings.ReplaceAll(qaRead(t, "doc.go")+qaRead(t, "card.go"), "//", " ")), " ")
	for _, want := range []string{
		"Options is a plain struct, the caller's own input, and it carries the PIN and the PUK in the clear",
		"a test that prints its Options, or a struct that holds them, prints them, whatever verb it uses",
		"Its Format method keeps them out of the verbs that reach it, and no method can do more",
		"A Card never gives them out by any print",
		"Options is different: it is the caller's own input and carries the PIN and the PUK in the clear",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("doc.go and card.go do not say %q", want)
		}
	}
	o := Options{PIN: string(qaPIN), PUK: string(qaPUK)}
	// By example: what Format keeps out ...
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%d", "%x", "%q"} {
		for _, v := range []any{o, &o, []Options{o}, map[string]Options{"a": o}} {
			if got := qaLeaks(fmt.Sprintf(verb, v)); len(got) != 0 {
				t.Errorf("%s of Options prints %v, which Format is there to keep out", verb, got)
			}
		}
	}
	// ... and what no method can: a bad verb, and a field that is not exported.
	w := "%w"
	type env struct {
		opts Options
		card *Card
	}
	for name, got := range map[string]string{
		"%p of an Options value": fmt.Sprintf("%p", o),
		"%w of an Options value": fmt.Sprintf(w, o),
		"Errorf %w of Options":   fmt.Errorf(w, o).Error(),
		"%+v of a struct that holds Options in an unexported field": fmt.Sprintf("%+v", env{opts: o, card: New(o)}),
	} {
		if leaks := qaLeaks(got); len(leaks) != 2 {
			t.Errorf("%s prints %v: this is the decided behaviour, the PIN and the PUK are in it", name, leaks)
		}
	}
	// A Card made from the same Options prints none of them, by the same routes.
	c := New(o)
	for name, got := range map[string]string{
		"%p of a Card value": fmt.Sprintf("%p", *c), "%w of a Card value": fmt.Sprintf(w, *c), "Errorf %w of a Card": fmt.Errorf(w, c).Error(),
		"%+v of a struct that holds a Card in an unexported field": fmt.Sprintf("%+v", env{opts: Options{}, card: c}),
	} {
		if leaks := qaLeaks(got); len(leaks) != 0 {
			t.Errorf("%s prints %v", name, leaks)
		}
	}
}

// qaSrc writes a small source: the imports, and then the body.
func qaSrc(imports string, body string) string {
	src := "package x\n"
	for _, p := range strings.Fields(imports) {
		src += "import " + strings.ReplaceAll(p, "=", " ") + "\n"
	}
	return src + body + "\n"
}

// N4, the identifiers. Of each allowed package only the listed identifiers may be selected: in every way that a source may name one.
func TestQaTheListsOfIdentifiersAreHeldInEveryWayANameIsWritten(t *testing.T) {
	const (
		fmtImp  = `"fmt"`
		syncImp = `"sync"`
	)
	caught := map[string]string{
		"fmt.Println":                    qaSrc(fmtImp, `func f() { fmt.Println("x") }`),
		"fmt.Printf":                     qaSrc(fmtImp, `func f() { fmt.Printf("x") }`),
		"fmt.Print":                      qaSrc(fmtImp, `func f() { fmt.Print("x") }`),
		"fmt.Scan":                       qaSrc(fmtImp, `func f() { var s string; _, _ = fmt.Scan(&s) }`),
		"fmt.Scanln":                     qaSrc(fmtImp, `func f() { var s string; _, _ = fmt.Scanln(&s) }`),
		"fmt.Fprintf":                    qaSrc(fmtImp, `var f = fmt.Fprintf`),
		"fmt.Sprint":                     qaSrc(fmtImp, `var f = fmt.Sprint`),
		"fmt.Errorf":                     qaSrc(fmtImp, `var f = fmt.Errorf`),
		"fmt.Stringer":                   qaSrc(fmtImp, `type P = fmt.Stringer`),
		"fmt.Formatter as a type":        qaSrc(fmtImp, `var _ fmt.Formatter`),
		"fmt under an alias":             qaSrc(`f="fmt"`, `var _ = f.Println`),
		"fmt under the name of bytes":    qaSrc(`bytes="fmt"`, `var _ = bytes.Println`),
		"swapped aliases":                qaSrc(`fmt="bytes" bytes="fmt"`, `var _ = bytes.Println; var _ = fmt.Repeat`),
		"sync.WaitGroup":                 qaSrc(syncImp, `func f() { var wg sync.WaitGroup; wg.Go(func() {}) }`),
		"sync.WaitGroup as a field":      qaSrc(syncImp, `type T struct{ sync.WaitGroup }`),
		"sync.Once":                      qaSrc(syncImp, `var o sync.Once`),
		"sync.OnceFunc":                  qaSrc(syncImp, `var f = sync.OnceFunc`),
		"sync.RWMutex":                   qaSrc(syncImp, `var m sync.RWMutex`),
		"sync.Pool":                      qaSrc(syncImp, `var m sync.Pool`),
		"sync.Map":                       qaSrc(syncImp, `var m sync.Map`),
		"bytes.Repeat":                   qaSrc(`"bytes"`, `var _ = bytes.Repeat`),
		"bytes.Buffer":                   qaSrc(`"bytes"`, `var b bytes.Buffer`),
		"bytes.NewReader":                qaSrc(`"bytes"`, `var _ = bytes.NewReader`),
		"binary.Read":                    qaSrc(`"encoding/binary"`, `var _ = binary.Read`),
		"binary.LittleEndian":            qaSrc(`"encoding/binary"`, `var _ = binary.LittleEndian`),
		"binary.ByteOrder as a type":     qaSrc(`"encoding/binary"`, `var _ binary.ByteOrder`),
		"a type alias of a bad name":     qaSrc(syncImp, `type W = sync.WaitGroup`),
		"a generic type with a bad name": qaSrc(fmtImp, `type G[T fmt.Stringer] struct{ v T }`),
		"an embedded bad name":           qaSrc(fmtImp, `type T struct{ fmt.Stringer }`),
		"in a method expression":         qaSrc(syncImp, `var _ = (*sync.Once).Do`),
		"time.Month in a conversion":     qaSrc(`"time"`, `var _ = time.Month(1)`),
		"time.Weekday":                   qaSrc(`"time"`, `var _ time.Weekday`),
		"time.ParseDuration":             qaSrc(`"time"`, `var _ = time.ParseDuration`),
	}
	for name, src := range caught {
		problems, err := sourceProblems("x.go", []byte(src))
		if err != nil {
			t.Errorf("%s: %v", name, err)
		} else if len(problems) == 0 {
			t.Errorf("not caught: %s", name)
		}
	}
	// What a list lets through, in every way that the list allows it: a method value or a promoted method of a listed type reaches no
	// identifier of the package that is not listed, and the plain ways around the list are harmless.
	allowed := map[string]string{
		"a Mutex in a struct":              qaSrc(syncImp, "type T struct{ mu sync.Mutex }\nfunc (t *T) f() { t.mu.Lock(); defer t.mu.Unlock() }"),
		"an embedded Mutex":                qaSrc(syncImp, "type T struct{ sync.Mutex }\nfunc (t *T) f() { t.Lock(); t.Unlock() }"),
		"a method value of a Mutex":        qaSrc(syncImp, "func f() { var m sync.Mutex; l := m.Lock; l(); m.Unlock() }"),
		"a type alias of a Mutex":          qaSrc(syncImp, "type M = sync.Mutex\nvar _ M"),
		"a defined type of a Mutex":        qaSrc(syncImp, "type M sync.Mutex\nvar _ M"),
		"a generic struct with a Mutex":    qaSrc(syncImp, "type G[T any] struct {\n\tmu sync.Mutex\n\tv  T\n}\nvar _ G[int]"),
		"a type alias of a Duration":       qaSrc(`"time"`, "type D = time.Duration\nvar _ D = 5 * time.Second"),
		"a method of a Duration":           qaSrc(`"time"`, "var _ = time.Second.String() + time.Minute.Round(time.Hour).String()"),
		"fmt.State in a Format method":     qaSrc(fmtImp, "type T int\nfunc (T) Format(f fmt.State, _ rune) { _, _ = f.Write(fmt.Appendf(nil, \"%d\", 1)) }"),
		"fmt.Sprintf in a generic":         qaSrc(fmtImp, "func f[T any](v T) string { return fmt.Sprintf(\"%v\", v) }"),
		"bytes.Equal and TrimRight":        qaSrc(`"bytes"`, `var _ = bytes.Equal(bytes.TrimRight(nil, "x"), nil)`),
		"binary.BigEndian and its methods": qaSrc(`"encoding/binary"`, `var _ = binary.BigEndian.AppendUint32(nil, 1)`),
		"package names used as fields":     "package x\ntype T struct{ fmt, bytes int }\nfunc f(t T) int { return t.fmt + t.bytes }\n",
	}
	for name, src := range allowed {
		if problems, err := sourceProblems("x.go", []byte(src)); err != nil || len(problems) != 0 {
			t.Errorf("%s: %q %v", name, problems, err)
		}
	}
	// No identifier on a list is one that opens, reads, writes, prints, scans, dials, waits or starts anything.
	for pkg, ids := range allowedIdentifiers {
		for _, id := range ids {
			for _, bad := range []string{"Open", "Read", "Write", "Print", "Fprint", "Scan", "Fscan", "Dial", "Listen", "Now", "Sleep", "After", "Tick", "Timer", "Go", "Wait",
				"Create", "Exec", "Rand", "Time", "Since", "Until", "Local", "Load"} {
				if strings.HasPrefix(id, bad) {
					t.Errorf("%s.%s is on the list of allowed identifiers", pkg, id)
				}
			}
		}
	}
}

// N5. The folder: only Go files, README, README.md and testdata, by exact name.
func TestQaOnlyGoFilesAndTheExactNamesOfAREADMEAndTestdataAreInTheFolder(t *testing.T) {
	for _, name := range []string{"README.s", "README.S", "README.c", "README.h", "README.syso", "README.cc", "README.go.s", "README_amd64.s", "README.o", "README.md.s",
		"README.md.c", "README.txt", "readme.s", "readme.md", "README.MD", "README ", " README", "testdata.s", "testdata2", "x.s", "x.S", "x.c", "x.h", "x.syso", "x.o",
		"x.a", "x.so", "x.cc", "x.cpp", "x.m", "x.f", "x.swig", "x.swigcxx", "go.mod", "go.work", ".hidden", "x.go.s", "x.gox", "Makefile", "main.rs", "x"} {
		if got := folderProblems([]string{"card.go", "card_test.go", name}); len(got) != 1 {
			t.Errorf("a folder with %q is not refused: %q", name, got)
		}
	}
	if got := folderProblems([]string{"card.go", "card_test.go", "qa_x_test.go", "README", "README.md", "testdata", "_x.go"}); len(got) != 0 {
		t.Errorf("a clean folder is refused: %q", got)
	}
	entries, err := os.ReadDir(".") // and the folder itself, with every file of it
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if got := folderProblems(names); len(got) != 0 {
		t.Errorf("the folder is refused: %q", got)
	}
}

// What the comment of the scanner says: a device path built in pieces gets past the check of the strings, and the import list is the real
// guard, because no allowed package can open a file, a device or a socket.
func TestQaAPathInPiecesGetsPastTheStringCheckAndTheImportListStillRefusesAnyWayToOpenIt(t *testing.T) {
	comment := strings.Join(strings.Fields(strings.ReplaceAll(qaRead(t, "source_test.go"), "//", " ")), " ")
	for _, want := range []string{"The import list is the real guard", "no allowed package can open a file", "it is not a proof"} {
		if !strings.Contains(comment, want) {
			t.Errorf("the comment of the scanner does not say %q", want)
		}
	}
	for name, pathExpr := range map[string]string{
		"two strings":   `"/" + "dev/hidraw0"`,
		"three strings": `"/d" + "ev/" + "bus"`,
		"bytes":         `string([]byte{47, 100, 101, 118, 47, 116, 116, 121})`,
		"a Sprintf":     `fmt.Sprintf("/%s/hidraw0", "dev")`,
		"run":           `"/r" + "un/pcscd/pcscd.comm"`,
		"sys":           `"/s" + "ys/bus/usb"`,
		"proc":          `"/pr" + "oc/self/mem"`,
	} {
		alone := qaSrc(`"fmt"`, "var _ = fmt.Sprintf\nvar p = "+pathExpr)
		if problems, err := sourceProblems("x.go", []byte(alone)); err != nil || len(problems) != 0 {
			t.Errorf("%s: the path in pieces is caught by the string check, which the comment says it is not: %q %v", name, problems, err)
		}
		// with anything that could open it, the import is refused
		for _, open := range []struct{ imp, use string }{
			{"os", "os.Open(p)"}, {"io/ioutil", "ioutil.ReadFile(p)"}, {"syscall", "syscall.Open(p, 0, 0)"}, {"io/fs", "fs.ValidPath(p)"},
			{"path/filepath", "filepath.Glob(p)"}, {"net", `net.Dial("unix", p)`}, {"os/exec", "exec.Command(p)"}, {"embed", "embed.FS{}"},
			{"golang.org/x/sys/unix", "unix.Open(p, 0, 0)"}, {"runtime/debug", "debug.SetGCPercent(1)"}, {"log", "log.Println(p)"},
		} {
			src := qaSrc(`"fmt" "`+open.imp+`"`, "var _ = fmt.Sprintf\nvar p = "+pathExpr+"\nfunc f() { _ = "+open.use+" }")
			problems, err := sourceProblems("x.go", []byte(src))
			if err != nil {
				t.Errorf("%s with %s: %v", name, open.imp, err)
				continue
			}
			refused := false
			for _, p := range problems {
				refused = refused || strings.Contains(p, "imports "+open.imp+", which is not in the list of allowed imports")
			}
			if !refused {
				t.Errorf("%s with %s: the import is not refused: %q", name, open.imp, problems)
			}
		}
	}
	// and the list holds the five packages that cannot open anything, and no other
	if strings.Join(allowedImports, " ") != "bytes encoding/binary fmt sync time" {
		t.Errorf("allowedImports = %v: this test knows that these five cannot open a file, and nothing else", allowedImports)
	}
}
