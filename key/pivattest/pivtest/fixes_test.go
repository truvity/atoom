package pivtest

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// panicText runs f and returns what it panicked with, or "" if it did not.
func panicText(f func()) (text string) {
	defer func() {
		if r := recover(); r != nil {
			text = fmt.Sprint(r)
		}
	}()
	f()
	return ""
}

// listed is how fmt prints a byte slice with %v: [1 2 3].
func listed(s string) string {
	var parts []string
	for _, b := range []byte(s) {
		parts = append(parts, strconv.Itoa(int(b)))
	}
	return "[" + strings.Join(parts, " ") + "]"
}

func TestTheEntryOfAShortCommandHoldsTheHeaderThatWasThere(t *testing.T) {
	c := newSelected(t, Options{})
	for _, apdu := range [][]byte{{0x80, 0xFD, 0x07}, {0x00, 0xA4}, {0x10}, {}} {
		resp, _ := c.Exchange(apdu)
		if !bytes.Equal(resp, []byte{0x67, 0x00}) {
			t.Errorf("%x answered %x", apdu, resp)
		}
	}
	c.Exchange([]byte{0x00, 0xFD, 0x00, 0x00}) // a neighbour that is well formed
	log := c.Log()[1:]
	want := []Entry{
		{CLA: 0x80, INS: 0xFD, P1: 0x07, SW: swWrongLength, Malformed: true},
		{CLA: 0x00, INS: 0xA4, SW: swWrongLength, Malformed: true},
		{CLA: 0x10, SW: swWrongLength, Malformed: true},
		{SW: swWrongLength, Malformed: true},
		{INS: 0xFD, SW: swOK},
	}
	if len(log) != len(want) {
		t.Fatalf("log = %+v", log)
	}
	for i := range want {
		if log[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, log[i], want[i])
		}
	}
}

func TestAShortCommandWithAForbiddenINSIsRecorded(t *testing.T) {
	for _, ins := range []byte{insReset, insSetRetries, insImportKey} {
		c := newSelected(t, Options{})
		_, sw := send(t, c, []byte{0x00, ins, 0x00})
		wantSW(t, "three bytes", sw, swWrongLength)
		_, sw = send(t, c, []byte{0x00, ins})
		wantSW(t, "two bytes", sw, swWrongLength)
		f := c.Forbidden()
		if len(f) != 2 || f[0].INS != ins || !f[0].Malformed || f[0].Why == "" {
			t.Errorf("Forbidden after INS %02X = %+v", ins, f)
		}
	}
	c := newSelected(t, Options{})
	send(t, c, []byte{0x00})            // no INS: nothing to record
	send(t, c, []byte{0x00, insVerify}) // an INS that is permitted
	send(t, c, []byte{insReset})        // the byte is the CLA, not the INS
	if f := c.Forbidden(); len(f) != 0 {
		t.Errorf("Forbidden = %+v", f)
	}
}

func TestAChainOfMoreThan65535BytesIsRefusedAtItsLastPart(t *testing.T) { // assumed: R21
	for _, tc := range []struct {
		first, last int
		want        uint16
	}{
		{65535, 1, swWrongLength},
		{65535, 5, swWrongLength},
		{65000, 600, swWrongLength},
		{65534, 1, swNotFound}, // 65535 in all: SELECT runs, and finds no such AID
		{65535, 0, swNotFound},
		{100, 100, swNotFound},
	} {
		c := New(Options{})
		c.PowerOn()
		_, sw := send(t, c, extended(claChain, insSelect, 4, 0, make([]byte, tc.first), -1))
		wantSW(t, "the first part", sw, swOK)
		var last []byte
		if tc.last == 0 {
			last = []byte{claPlain, insSelect, 4, 0}
		} else {
			last = short(claPlain, insSelect, 4, 0, bytes.Repeat([]byte{1}, tc.last), -1)
			if tc.last > 255 {
				last = extended(claPlain, insSelect, 4, 0, bytes.Repeat([]byte{1}, tc.last), -1)
			}
		}
		_, sw = send(t, c, last)
		wantSW(t, fmt.Sprintf("%d and %d bytes", tc.first, tc.last), sw, tc.want)
		if c.st().chain != nil {
			t.Errorf("a chain is left open after %d and %d bytes", tc.first, tc.last)
		}
	}
}

func TestPrintingACardOrOptionsLeaksNoSecret(t *testing.T) {
	// The values that are searched for in printed text hold a character that no number can hold (a letter outside a to f, and the like), so that
	// no address and no number that fmt prints can be taken for them. The body is 36 bytes, 72 hexadecimal digits: more than any number.
	const pin, puk, body, newPIN = "Qz7Jk9Wm", "Yt4Hp2Nx", "OBJ-qzjxkw-BODY-OBJ-qzjxkw-BODY-QZJX", "Xv3Ln8Ry"
	o := Options{PIN: pin, PUK: puk}
	c := New(o)
	c.PutObject(ObjectSlot9A, []byte(body))
	c.PowerOn()
	selectPIV(t, c)
	send(t, c, changeCmd(refPIN, pin, newPIN))
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%d", "%x", "%X", "%q", "%T", "%p", "%10.3v", "%-20s"} {
		values := map[string]any{"card": c, "options": o, "options pointer": &o, "reference": c.st().pin}
		if verb == "%p" { // fmt does not hand %p to a Formatter: a pointer prints an address, and only a pointer is asked
			values = map[string]any{"card": c, "options pointer": &o}
		}
		for name, v := range values {
			got := fmt.Sprintf(verb, v)
			for _, secret := range []string{pin, puk, body, newPIN, hex.EncodeToString([]byte(body)), strings.ToUpper(hex.EncodeToString([]byte(body))), listed(body), listed(pin)} {
				if strings.Contains(got, secret) {
					t.Errorf("%s of the %s prints %q: %s", verb, name, secret, got)
				}
			}
		}
	}
	// What is printed is what a test may want to see.
	got := fmt.Sprintf("%+v", c)
	for _, want := range []string{"5.8.0", "7654321", "PIN tries 3 of 3", "PUK tries 3 of 3", "1 objects"} {
		if !strings.Contains(got, want) {
			t.Errorf("the card prints %q, which lacks %q", got, want)
		}
	}
	if got := fmt.Sprintf("%v", c.st().pin); !strings.Contains(got, "tries 3 of 3") {
		t.Errorf("a reference prints %q", got)
	}
	if got := fmt.Sprint(Options{}); !strings.Contains(got, "PIN default") {
		t.Errorf("options print %q", got)
	}
}

func TestNewRefusesAPINOrPUKThatEndsInFF(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    Options
	}{
		{"PIN", Options{PIN: "123456\xff"}},
		{"PIN", Options{PIN: "12345\xff"}},
		{"PIN", Options{PIN: "1234\xff\xff"}},
		{"PIN", Options{PIN: "ab\xff\xff\xff\xff"}},
		{"PUK", Options{PUK: "1234567\xff"}},
		{"PUK", Options{PUK: "123456\xff\xff"}},
	} {
		text := panicText(func() { New(tc.o) })
		if !strings.Contains(text, tc.name) || !strings.Contains(text, "FF") {
			t.Errorf("New(%q %q) panicked with %q", tc.o.PIN, tc.o.PUK, text)
		}
	}
	// An FF inside a value is allowed, and the value can be sent.
	for _, pin := range []string{"12\xff456", "\xff12345", "12\xff\xff56", "\xff\xff\xff\xff\xff\x00"} {
		c := newSelected(t, Options{PIN: pin})
		_, sw := send(t, c, short(0, insVerify, 0, refPIN, pad(pin), -1))
		wantSW(t, fmt.Sprintf("VERIFY of %q", pin), sw, swOK)
	}
	c := newSelected(t, Options{PUK: "12\xff456"})
	_, sw := send(t, c, changeCmd(refPUK, "12\xff456", "87654321"))
	wantSW(t, "CHANGE with a PUK that has an FF inside", sw, swOK)
	// A new value that ends in FF is the same value without it: it is never stored with the padding.
	c = newSelected(t, Options{})
	_, sw = send(t, c, changeCmd(refPIN, DefaultPIN, "654321"))
	wantSW(t, "CHANGE", sw, swOK)
	_, sw = send(t, c, verifyCmd("654321"))
	wantSW(t, "VERIFY of the changed PIN", sw, swOK)
}

func TestOnlyTheShortFormOfTheTagListIsServed(t *testing.T) { // assumed: R8
	c := newSelected(t, Options{})
	obj := []byte{0x53, 0x01, 0xAB}
	c.PutObject(ObjectSlot9A, obj)
	for name, list := range map[string][]byte{
		"5C 81 03":      {0x5C, 0x81, 0x03, 0x5F, 0xC1, 0x05},
		"5C 82 00 03":   {0x5C, 0x82, 0x00, 0x03, 0x5F, 0xC1, 0x05},
		"5C 04":         {0x5C, 0x04, 0x5F, 0xC1, 0x05, 0x00},
		"5C 02":         {0x5C, 0x02, 0x5F, 0xC1},
		"5D 03":         {0x5D, 0x03, 0x5F, 0xC1, 0x05},
		"5C 03 and one": {0x5C, 0x03, 0x5F, 0xC1, 0x05, 0x00},
		"short":         {0x5C, 0x03, 0x5F, 0xC1},
	} {
		_, sw := send(t, c, short(0, insGetData, 0x3F, 0xFF, list, 0))
		wantSW(t, name, sw, swWrongData)
	}
	data, sw := send(t, c, short(0, insGetData, 0x3F, 0xFF, []byte{0x5C, 0x03, 0x5F, 0xC1, 0x05}, 0))
	wantSW(t, "5C 03", sw, swOK)
	if !bytes.Equal(data, obj) {
		t.Errorf("data = %x", data)
	}
}

func TestTheDocNamesTheStatusWordOfABlockedReference(t *testing.T) {
	doc, err := os.ReadFile("doc.go")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(strings.Fields(strings.ReplaceAll(string(doc), "//", " ")), " ")
	for _, want := range []string{"6983", "blocked", "R10", "that is refused (a P1 or P2 that is not 00, or data) drops what waits as well",
		`leaves "PIN verified" as it was`, "counting the last part", "never ends in FF", "5C 81 03", "65526", "P1 and P2 are held strictly"} {
		if !strings.Contains(text, want) {
			t.Errorf("doc.go does not say %q", want)
		}
	}
	// 6983 stands in the paragraph about VERIFY and CHANGE REFERENCE DATA, and the code answers it for both.
	c := newSelected(t, Options{})
	for i := 0; i < 3; i++ {
		send(t, c, verifyCmd("000000"))
		send(t, c, changeCmd(refPUK, "00000000", "87654321"))
	}
	_, sw := send(t, c, verifyCmd(DefaultPIN))
	wantSW(t, "VERIFY of a blocked PIN", sw, swBlocked)
	_, sw = send(t, c, changeCmd(refPUK, DefaultPUK, "87654321"))
	wantSW(t, "CHANGE of a blocked PUK", sw, swBlocked)
}

func TestCertificateObjectNeverReturnsACorruptObject(t *testing.T) {
	for _, n := range []int{0, 1, 127, 128, 255, 256, 1000, MaxCertificateDER} {
		der := bytes.Repeat([]byte{0x30}, n)
		obj := CertificateObject(der)
		tag, outer, rest, ok := readTLV(obj)
		if !ok || tag != 0x53 || len(rest) != 0 {
			t.Fatalf("n = %d: the object is not one 53 object", n)
		}
		tag, cert, rest, ok := readTLV(outer)
		if !ok || tag != 0x70 || len(cert) != n || !bytes.Equal(rest, []byte{0x71, 0x01, 0x00, 0xFE, 0x00}) {
			t.Errorf("n = %d: certificate of %d bytes, rest %x", n, len(cert), rest)
		}
	}
	for _, n := range []int{MaxCertificateDER + 1, 65536, 70000, 1 << 20} {
		text := panicText(func() { CertificateObject(make([]byte, n)) })
		if !strings.Contains(text, fmt.Sprint(n)) || !strings.Contains(text, "does not fit") {
			t.Errorf("n = %d: panic text %q", n, text)
		}
	}
	if text := panicText(func() { tlv(0x53, make([]byte, 65536)) }); text == "" {
		t.Error("tlv wrote a length that does not fit")
	}
}

func TestARefusedGetResponseDropsWhatWaits(t *testing.T) { // assumed: R10
	for name, bad := range map[string]struct {
		apdu []byte
		want uint16
	}{
		"P1":   {[]byte{0, insGetResponse, 1, 0, 0}, swWrongP1P2},
		"P2":   {[]byte{0, insGetResponse, 0, 1, 0}, swWrongP1P2},
		"data": {short(0, insGetResponse, 0, 0, []byte{1}, -1), swWrongLength},
	} {
		c := newSelected(t, Options{})
		c.PutObject(ObjectSlot9A, bigObject(600))
		send(t, c, getDataCmd(ObjectSlot9A, 0))
		_, sw := send(t, c, bad.apdu)
		wantSW(t, "the refused GET RESPONSE with a bad "+name, sw, bad.want)
		_, sw = send(t, c, getResponseCmd(0))
		wantSW(t, "the next GET RESPONSE after a bad "+name, sw, swWrongP1P2)
	}
	// The neighbour: a good GET RESPONSE keeps what is left.
	c := newSelected(t, Options{})
	c.PutObject(ObjectSlot9A, bigObject(600))
	send(t, c, getDataCmd(ObjectSlot9A, 0))
	_, sw := send(t, c, getResponseCmd(0))
	wantSW(t, "the first GET RESPONSE", sw, swMore|0x58)
	_, sw = send(t, c, getResponseCmd(0))
	wantSW(t, "the second GET RESPONSE", sw, swOK)
}

func TestASuccessfulChangeOfThePINLeavesVerifiedAsItWas(t *testing.T) { // assumed: R20
	c := newSelected(t, Options{})
	send(t, c, verifyCmd(DefaultPIN))
	_, sw := send(t, c, changeCmd(refPIN, DefaultPIN, "654321"))
	wantSW(t, "CHANGE", sw, swOK)
	_, sw = send(t, c, emptyVerify())
	wantSW(t, "verified before, verified after", sw, swOK)

	c = newSelected(t, Options{})
	_, sw = send(t, c, changeCmd(refPIN, DefaultPIN, "654321"))
	wantSW(t, "CHANGE", sw, swOK)
	_, sw = send(t, c, emptyVerify())
	wantSW(t, "not verified before, not verified after", sw, 0x63C3)
}

// A Card is one func value that returns its state: fmt prints a func as an address and never follows it, at any depth and with any verb.
func TestACardIsOneUnexportedFunc(t *testing.T) {
	typ := reflect.TypeOf(Card{})
	if typ.NumField() != 1 || typ.Field(0).Type.Kind() != reflect.Func || typ.Field(0).IsExported() {
		t.Fatalf("Card has %d fields, the first of kind %s: want one unexported func", typ.NumField(), typ.Field(0).Type.Kind())
	}
}

func TestACardPrintedInAnyFormShowsNoSecret(t *testing.T) {
	// The values that are searched for in printed text hold a character that no number can hold (a letter outside a to f, and the like), so that
	// no address and no number that fmt prints can be taken for them. The body is 36 bytes, 72 hexadecimal digits: more than any number.
	const pin, puk, body, newPIN = "Qz7Jk9Wm", "Yt4Hp2Nx", "OBJ-qzjxkw-BODY-OBJ-qzjxkw-BODY-QZJX", "Xv3Ln8Ry"
	c := New(Options{PIN: pin, PUK: puk})
	c.PutObject(ObjectSlot9A, []byte(body))
	copyOfCard := *c // a second handle to the same card
	type holder struct {
		inner Card
		ptr   *Card
		any   any
	}
	h := holder{inner: copyOfCard, ptr: c, any: copyOfCard}
	w := "%w" // not a constant, so that go vet does not refuse the call
	forms := map[string]string{
		"value %v":              fmt.Sprintf("%v", copyOfCard),
		"value %+v":             fmt.Sprintf("%+v", copyOfCard),
		"value %#v":             fmt.Sprintf("%#v", copyOfCard),
		"value %d":              fmt.Sprintf("%d", copyOfCard),
		"value %x":              fmt.Sprintf("%x", copyOfCard),
		"value %w":              fmt.Sprintf(w, copyOfCard),
		"pointer %w":            fmt.Sprintf(w, c),
		"Errorf %w":             fmt.Errorf(w, c).Error(),
		"Errorf %w of a value":  fmt.Errorf(w, copyOfCard).Error(),
		"holder %v":             fmt.Sprintf("%v", h),
		"holder %+v":            fmt.Sprintf("%+v", h),
		"holder %#v":            fmt.Sprintf("%#v", h),
		"pointer to holder %+v": fmt.Sprintf("%+v", &h),
		"slice of cards":        fmt.Sprintf("%+v", []Card{copyOfCard}),
		"map of cards":          fmt.Sprintf("%+v", map[string]*Card{"a": c}),
	}
	for name, got := range forms {
		for _, secret := range []string{pin, puk, body, newPIN, hex.EncodeToString([]byte(body)), listed(body), listed(pin), listed(puk)} {
			if strings.Contains(got, secret) {
				t.Errorf("%s prints %q: %.150s", name, secret, got)
			}
		}
	}
	// Both handles are the one card.
	c.PowerOn()
	selectPIV(t, c)
	if _, sw := send(t, &copyOfCard, verifyCmd(pin)); sw != swOK {
		t.Errorf("VERIFY through the copy: %04X", sw)
	}
	if _, sw := send(t, c, emptyVerify()); sw != swOK {
		t.Errorf("the other handle does not see the verified PIN: %04X", sw)
	}
	// Options is the caller's own input: its PIN is in the clear in any struct that holds it (an unexported field is out of the reach of Format).
	type env struct{ opts Options }
	if got := fmt.Sprintf("%+v", env{Options{PIN: pin}}); !strings.Contains(got, pin) {
		t.Skip("fmt no longer prints an Options in an unexported field: the doc of Options may say less")
	}
}

// A Card held by value in an unexported field is never asked for its methods, and a verb that is bad for a pointer makes fmt print a value
// again from the top. Nothing in a Card may be a kind that fmt follows: every verb prints at most an address.
func TestACardInAnUnexportedFieldPrintsNoStateWithAnyVerb(t *testing.T) {
	// The values that are searched for in printed text hold a character that no number can hold (a letter outside a to f, and the like), so that
	// no address and no number that fmt prints can be taken for them. The body is 36 bytes, 72 hexadecimal digits: more than any number.
	const pin, puk, body = "Qz7Jk9Wm", "Yt4Hp2Nx", "OBJ-qzjxkw-BODY-OBJ-qzjxkw-BODY-QZJX"
	c := New(Options{PIN: pin, PUK: puk})
	c.PutObject(ObjectSlot9A, []byte(body))
	type env struct{ card Card }
	type envAny struct{ v any }
	type envPtr struct{ card *Card }
	type envDeep struct {
		inner struct{ cards [2]Card }
	}
	var deep envDeep
	deep.inner.cards[1] = *c
	values := map[string]any{
		"a Card":                *c,
		"a struct with a Card":  env{*c},
		"a pointer to it":       &env{*c},
		"a Card in an any":      envAny{*c},
		"a *Card in an any":     envAny{c},
		"a *Card":               envPtr{c},
		"a slice of structs":    []env{{*c}, {*c}},
		"a map of structs":      map[string]env{"a": {*c}},
		"a map with Card keys":  map[string][]envAny{"a": {{*c}}},
		"a Card in an array":    deep,
		"an array of Cards":     [2]Card{*c, *c},
		"a pointer to a *Card":  &envPtr{c},
		"a channel-free struct": struct{ a, b Card }{*c, *c},
	}
	secrets := []string{pin, puk, body, hex.EncodeToString([]byte(body)), strings.ToUpper(hex.EncodeToString([]byte(body))), listed(body), listed(pin), listed(puk), "OBJ-qzjxkw"}
	verbs := "vTtbcdoOqxXUeEfFgGsp"
	for name, v := range values {
		for _, r := range verbs {
			for _, flags := range []string{"", "+", "#", "-", " ", "0", "10", ".3", "+#"} {
				format := "%" + flags + string(r)
				got := fmt.Sprintf(format, v)
				for _, secret := range secrets {
					if strings.Contains(got, secret) {
						t.Fatalf("%s of %s prints %q: %.200s", format, name, secret, got)
					}
				}
			}
		}
		w, bad, letter := verbText("w"), verbText("!"), verbText("z") // not constants, so that vet and staticcheck do not refuse the calls
		for _, got := range []string{fmt.Sprintf(w, v), fmt.Errorf(w, v).Error(), fmt.Sprintf(bad, v), fmt.Sprintf(letter, v)} {
			for _, secret := range secrets {
				if strings.Contains(got, secret) {
					t.Fatalf("a bad verb of %s prints %q: %.200s", name, secret, got)
				}
			}
		}
	}
	// What a test sees of such a card is an address.
	if got := fmt.Sprintf("%s", env{*c}); !strings.Contains(got, "0x") {
		t.Errorf("%%s of a struct with a Card prints %q", got)
	}
	// The zero Card is no card.
	var zero Card
	if got := fmt.Sprintf("%v", zero); !strings.Contains(got, "not made by New") {
		t.Errorf("the zero Card prints %q", got)
	}
	for name, f := range map[string]func(){
		"PowerOn":   func() { zero.PowerOn() },
		"PowerOff":  func() { zero.PowerOff() },
		"Exchange":  func() { zero.Exchange([]byte{0, 0, 0, 0}) },
		"Log":       func() { zero.Log() },
		"Forbidden": func() { zero.Forbidden() },
		"PutObject": func() { zero.PutObject(1, []byte{1}) },
	} {
		if text := panicText(f); !strings.Contains(text, "zero Card is no card") {
			t.Errorf("%s of the zero Card panicked with %q", name, text)
		}
	}
}

// verbText is "%" and a letter, built so that no analyser reads it as a constant format.
func verbText(letter string) string { return strings.Join([]string{"%", letter}, "") }

func TestVerifyWithP1FFResetsTheSecurityStatus(t *testing.T) { // assumed: R8 (the blocked PIN, and the data)
	reset := []byte{0, insVerify, 0xFF, refPIN}
	c := newSelected(t, Options{})
	send(t, c, verifyCmd(DefaultPIN))
	_, sw := send(t, c, emptyVerify())
	wantSW(t, "verified", sw, swOK)
	data, sw := send(t, c, reset)
	wantSW(t, "00 20 FF 80", sw, swOK)
	if len(data) != 0 {
		t.Errorf("data %x", data)
	}
	_, sw = send(t, c, emptyVerify())
	wantSW(t, "after the reset", sw, 0x63C3)
	// No try is spent, and none is given back.
	send(t, c, verifyCmd("000000")) // 2 left
	_, sw = send(t, c, reset)
	wantSW(t, "reset after a wrong PIN", sw, swOK)
	_, sw = send(t, c, emptyVerify())
	wantSW(t, "the counter did not move", sw, 0x63C2)
	// A PIN that was not verified: the same.
	_, sw = send(t, c, reset)
	wantSW(t, "reset of a PIN that is not verified", sw, swOK)
	// A blocked PIN: 9000, and it stays blocked.
	for i := 0; i < 2; i++ {
		send(t, c, verifyCmd("000000"))
	}
	_, sw = send(t, c, reset)
	wantSW(t, "reset of a blocked PIN", sw, swOK)
	_, sw = send(t, c, emptyVerify())
	wantSW(t, "the blocked PIN", sw, 0x63C0)
	_, sw = send(t, c, verifyCmd(DefaultPIN))
	wantSW(t, "the right PIN, blocked", sw, swBlocked)
	// With data it is 6700, and nothing changes.
	c = newSelected(t, Options{})
	send(t, c, verifyCmd(DefaultPIN))
	_, sw = send(t, c, short(0, insVerify, 0xFF, refPIN, pad(DefaultPIN), -1))
	wantSW(t, "00 20 FF 80 with data", sw, swWrongLength)
	_, sw = send(t, c, emptyVerify())
	wantSW(t, "still verified", sw, swOK)
	// The reference is the PIN, and P1 is 00 or FF.
	_, sw = send(t, c, []byte{0, insVerify, 0xFF, refPUK})
	wantSW(t, "P2 81", sw, swRefNotFound)
	for _, p1 := range []byte{0x01, 0x80, 0xFE} {
		_, sw = send(t, c, []byte{0, insVerify, p1, refPIN})
		wantSW(t, fmt.Sprintf("P1 %02X", p1), sw, swWrongP1P2)
	}
	// An extended form, with an Le and no data, is the same command.
	_, sw = send(t, c, extended(0, insVerify, 0xFF, refPIN, nil, 0))
	wantSW(t, "extended", sw, swOK)
	_, sw = send(t, c, emptyVerify())
	wantSW(t, "after the extended reset", sw, 0x63C3)
	// And it is no way round the counter: a reset between wrong values does not give a try back.
	c = newSelected(t, Options{})
	for i := 0; i < 3; i++ {
		send(t, c, verifyCmd("000000"))
		send(t, c, reset)
	}
	_, sw = send(t, c, verifyCmd(DefaultPIN))
	wantSW(t, "after three wrong values and three resets", sw, swBlocked)
}

func TestAChangeBackToTheFactoryValueKeepsTheDefaultFlagAtZero(t *testing.T) { // assumed: R23
	for _, tc := range []struct {
		name         string
		ref          byte
		factory, new string
	}{{"PIN", refPIN, DefaultPIN, "654321"}, {"PUK", refPUK, DefaultPUK, "87654321"}} {
		c := newSelected(t, Options{})
		flag := func() byte {
			data, sw := send(t, c, metadataCmd(tc.ref))
			wantSW(t, tc.name+" metadata", sw, swOK)
			return data[5] // 01 01 FF 05 01 <flag> ...
		}
		if flag() != 1 {
			t.Fatalf("%s: the factory card does not say it is the default", tc.name)
		}
		send(t, c, changeCmd(tc.ref, tc.factory, tc.new))
		if flag() != 0 {
			t.Errorf("%s: the flag is not 0 after a change", tc.name)
		}
		_, sw := send(t, c, changeCmd(tc.ref, tc.new, tc.factory))
		wantSW(t, tc.name+" change back", sw, swOK)
		if flag() != 0 {
			t.Errorf("%s: a change back to the factory value made the flag 1: the card compares the value", tc.name)
		}
		_, sw = send(t, c, changeCmd(tc.ref, tc.factory, tc.factory)) // and to the same value
		wantSW(t, tc.name+" change to the same value", sw, swOK)
		if flag() != 0 {
			t.Errorf("%s: the flag is not 0", tc.name)
		}
	}
}

func TestAnLeShorterThanTheAnswerIsCutWith61xxNeverRight6Cxx(t *testing.T) { // assumed: R24
	c := newSelected(t, Options{})
	c.PutObject(ObjectSlot9A, bigObject(300))
	for _, tc := range []struct {
		name string
		apdu []byte
	}{
		{"object, Le 1", getDataCmd(ObjectSlot9A, 1)},
		{"object, Le 40", getDataCmd(ObjectSlot9A, 40)},
		{"object, Le 255", getDataCmd(ObjectSlot9A, 255)},
		{"object, extended Le 10", extended(0, insGetData, 0x3F, 0xFF, []byte{0x5C, 3, 0x5F, 0xC1, 5}, 10)},
		{"SELECT, Le 4", short(0, insSelect, 4, 0, aidShort, 4)},
		{"metadata, Le 3", short(0, insGetMetadata, 0, refPIN, nil, 3)},
		{"version, Le 1", short(0, insGetVersion, 0, 0, nil, 1)},
	} {
		data, sw := send(t, c, tc.apdu)
		if sw>>8 != 0x61 || len(data) == 0 {
			t.Errorf("%s: %d bytes and SW %04X, want a cut and 61xx", tc.name, len(data), sw)
		}
		if sw>>8 == 0x6C {
			t.Errorf("%s answers 6Cxx", tc.name)
		}
	}
	// What is cut is what GET RESPONSE gives next.
	data, sw := send(t, c, getDataCmd(ObjectSlot9A, 40))
	wantSW(t, "Le 40", sw, swMore) // 260 left: 256 or more is 00
	rest, sw := send(t, c, getResponseCmd(0))
	if len(data) != 40 || len(rest) != 256 || sw != swMore|4 {
		t.Errorf("parts %d and %d, SW %04X", len(data), len(rest), sw)
	}
}

func TestAVerifyBlockShorterThanSixBytesIsAWrongValueThatSpendsATry(t *testing.T) { // assumed: R8
	for name, block := range map[string][]byte{
		"all FF":           bytes.Repeat([]byte{0xFF}, 8),
		"1234 and four FF": pad("1234"),
		"one byte":         pad("1"),
		"five bytes":       pad("12345"),
	} {
		c := newSelected(t, Options{})
		_, sw := send(t, c, short(0, insVerify, 0, refPIN, block, -1))
		wantSW(t, name, sw, 0x63C2)
		_, sw = send(t, c, emptyVerify())
		wantSW(t, name+": the counter", sw, 0x63C2)
	}
	// A block of six bytes that is merely wrong is the same, and the right one is not refused.
	c := newSelected(t, Options{})
	_, sw := send(t, c, verifyCmd("000000"))
	wantSW(t, "six wrong bytes", sw, 0x63C2)
	_, sw = send(t, c, verifyCmd(DefaultPIN))
	wantSW(t, "the right PIN", sw, swOK)
}
