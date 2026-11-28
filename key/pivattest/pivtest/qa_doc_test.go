package pivtest

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// These tests read doc.go sentence by sentence and hold each statement about a command, a status word, a state change or a limit to the
// code. Where an earlier test already holds a sentence, it is not held again here.

// qaReader is "any type with those three methods": a reader simulator may take a *Card.
type qaReader interface {
	PowerOn() []byte
	PowerOff()
	Exchange(apdu []byte) (resp []byte, wait time.Duration)
}

var (
	_ qaReader = (*Card)(nil)
	_ TestingT = (*testing.T)(nil)
	_ TestingT = (*testing.B)(nil)
	_ TestingT = testing.TB(nil)
	_ TestingT = (*fakeT)(nil)
)

func qaRead(t testing.TB, name string) string {
	t.Helper()
	b, err := os.ReadFile(name) //nolint:gosec // a test reads the files of its own package
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// qaFiles lists the non-test sources and the test files of the package, the files of this series (qa_*) apart.
func qaFiles(t testing.TB) (sources, tests []string) {
	t.Helper()
	all, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range all {
		switch {
		case strings.HasPrefix(f, "qa_"):
		case strings.HasSuffix(f, "_test.go"):
			tests = append(tests, f)
		default:
			sources = append(sources, f)
		}
	}
	return sources, tests
}

// qaLow is the low byte of a 61xx: the bytes left, and 00 for 256 or more.
func qaLow(left int) uint16 {
	if left >= 256 {
		return 0
	}
	return uint16(left & 0xFF)
}

func qaSWOf(t testing.TB, c *Card, apdu []byte) uint16 {
	t.Helper()
	_, sw := send(t, c, apdu)
	return sw
}

// "Every guess is marked in the source with the comment 'assumed: R<n>' at the code and at its test, and listed in the table below."
func TestQaTheTableOfAssumptionsAndTheMarksAgree(t *testing.T) {
	rowRe := regexp.MustCompile(`(?m)^//\tR(\d+)\s`)
	markRe := regexp.MustCompile(`assumed: R(\d+)`)
	rows := map[string]bool{}
	for _, m := range rowRe.FindAllStringSubmatch(qaRead(t, "doc.go"), -1) {
		rows[m[1]] = true
	}
	if len(rows) < 8 {
		t.Fatalf("the table of doc.go has %d rows: %v", len(rows), rows)
	}
	sources, tests := qaFiles(t)
	marks := func(files []string) map[string]bool {
		out := map[string]bool{}
		for _, f := range files {
			if f == "doc.go" {
				continue
			}
			for _, m := range markRe.FindAllStringSubmatch(qaRead(t, f), -1) {
				out[m[1]] = true
			}
		}
		return out
	}
	inSource, inTests := marks(sources), marks(tests)
	for r := range rows {
		if !inSource[r] {
			t.Errorf("R%s is in the table but no source marks it", r)
		}
		if !inTests[r] {
			t.Errorf("R%s is in the table but no test marks it", r)
		}
	}
	for r := range inSource {
		if !rows[r] {
			t.Errorf("the source marks R%s as an assumption and the table does not list it", r)
		}
	}
	for r := range inTests {
		if !rows[r] {
			t.Errorf("a test marks R%s as an assumption and the table does not list it", r)
		}
	}
}

// "No goroutine, no timer, no clock, no I/O" and "it imports the standard library only".
func TestQaTheSourcesImportTheStandardLibraryOnlyAndStartNothing(t *testing.T) {
	sources, _ := qaFiles(t)
	for _, name := range sources {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, name, qaRead(t, name), parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if first, _, _ := strings.Cut(p, "/"); strings.Contains(first, ".") || p == "C" {
				t.Errorf("%s imports %s, which is not in the standard library", name, p)
			}
			if imp.Name != nil && imp.Name.Name == "." {
				t.Errorf("%s has a dot import of %s", name, p)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.GoStmt:
				t.Errorf("%s: a go statement at %s", name, fset.Position(n.Pos()))
			case *ast.SelectStmt, *ast.SendStmt, *ast.ChanType:
				t.Errorf("%s: a channel at %s", name, fset.Position(n.Pos()))
			case *ast.SelectorExpr:
				if id, ok := n.X.(*ast.Ident); ok && id.Name == "time" && n.Sel.Name != "Duration" {
					t.Errorf("%s: time.%s at %s: the card uses time for the type Duration only", name, n.Sel.Name, fset.Position(n.Pos()))
				}
			}
			return true
		})
	}
}

// "The answer to reset: TS 3B, T0 8B (an interface byte TD1 follows, 11 historical bytes), TD1 01 (protocol T=1), the historical bytes
// pivtest-sim, and the check byte", "made up".
func TestQaTheAnswerToResetIsWhatItSays(t *testing.T) {
	c := New(Options{})
	atr := c.PowerOn()
	if len(atr) != 15 || atr[0] != 0x3B || atr[1]>>4 != 8 || int(atr[1]&0x0F) != len(atr)-4 || atr[2] != 0x01 || string(atr[3:14]) != "pivtest-sim" {
		t.Fatalf("answer to reset = %x", atr)
	}
	var x byte
	for _, b := range atr[1:] {
		x ^= b
	}
	if x != 0 {
		t.Errorf("the check byte of %x is wrong", atr)
	}
	if bytes.Contains(bytes.ToLower(atr), []byte("yubi")) {
		t.Errorf("the answer to reset names a vendor: %x", atr)
	}
	atr[0] = 0 // a copy
	if again := c.PowerOn(); again[0] != 0x3B {
		t.Errorf("PowerOn returned its own slice")
	}
}

// "The zero value is a valid card: firmware 5.8.0, the serial DefaultSerial, the factory PIN and PUK with three tries each, extended APDUs
// accepted and the mode Extended." A card made with New is not powered.
func TestQaTheZeroOptionsAreTheCardTheDocDescribes(t *testing.T) {
	c := New(Options{})
	if resp, wait := c.Exchange([]byte{0, insSelect, 4, 0}); resp != nil || wait != 0 {
		t.Errorf("a new card is powered: %x %v", resp, wait)
	}
	c.PowerOff() // a card that is off, off again: no panic
	c.PowerOff()
	c.PowerOn()
	selectPIV(t, c)
	if data, sw := send(t, c, []byte{0, insGetVersion, 0, 0}); sw != swOK || !bytes.Equal(data, []byte{5, 8, 0}) {
		t.Errorf("version %x %04X", data, sw)
	}
	if data, _ := send(t, c, []byte{0, insGetSerial, 0, 0}); !bytes.Equal(data, []byte{0, 0x74, 0xCB, 0xB1}) || DefaultSerial != 7654321 {
		t.Errorf("serial %x", data)
	}
	want := []byte{1, 1, 0xFF, 5, 1, 1, 6, 2, 3, 3}
	for _, ref := range []byte{refPIN, refPUK} {
		if data, sw := send(t, c, metadataCmd(ref)); sw != swOK || !bytes.Equal(data, want) {
			t.Errorf("metadata of %02X = %x %04X", ref, data, sw)
		}
	}
	if DefaultPIN != "123456" || DefaultPUK != "12345678" {
		t.Errorf("the factory values changed: %q %q", DefaultPIN, DefaultPUK)
	}
	c.PutObject(ObjectSlot9A, bigObject(3000))
	id := []byte{0x5C, 3, 0x5F, 0xC1, 5}
	if data, sw := send(t, c, extended(0, insGetData, 0x3F, 0xFF, id, 0)); sw != swOK || len(data) != 3000 {
		t.Errorf("an extended APDU in the default mode: %d bytes, %04X", len(data), sw)
	}
	// A firmware with a zero part is not the default; only the zero version is.
	c = newSelected(t, Options{Firmware: [3]byte{0, 0, 1}})
	if data, _ := send(t, c, []byte{0, insGetVersion, 0, 0}); !bytes.Equal(data, []byte{0, 0, 1}) {
		t.Errorf("version %x", data)
	}
}

// "MaxPINTries byte // 1 to 15", and the counter in 63Cx has four bits.
func TestQaEveryMaximumFromOneToFifteenIsHeld(t *testing.T) {
	for m := 1; m <= 15; m++ {
		c := newSelected(t, Options{MaxPINTries: byte(m), MaxPUKTries: byte(16 - m)})
		if data, _ := send(t, c, metadataCmd(refPIN)); data[8] != byte(m) || data[9] != byte(m) {
			t.Errorf("PIN metadata %x for a maximum of %d", data, m)
		}
		if data, _ := send(t, c, metadataCmd(refPUK)); data[8] != byte(16-m) || data[9] != byte(16-m) {
			t.Errorf("PUK metadata %x for a maximum of %d", data, 16-m)
		}
		wantSW(t, fmt.Sprintf("a wrong PIN, maximum %d", m), qaSWOf(t, c, verifyCmd("000000")), swTriesLeft|qaLow(m-1))
		wantSW(t, fmt.Sprintf("a wrong PUK, maximum %d", 16-m), qaSWOf(t, c, changeCmd(refPUK, "00000000", "87654321")), swTriesLeft|qaLow(15-m))
		for i := 0; i < m-1; i++ {
			qaSWOf(t, c, verifyCmd("000000"))
		}
		wantSW(t, "the PIN is blocked after exactly its maximum", qaSWOf(t, c, verifyCmd(DefaultPIN)), swBlocked)
	}
}

// "A value given is not the default", "empty: DefaultPIN, and the card says it is the default".
func TestQaAGivenValueIsNotTheDefaultEvenWhenItEqualsTheFactoryValue(t *testing.T) {
	c := newSelected(t, Options{PIN: DefaultPIN, PUK: DefaultPUK})
	for _, ref := range []byte{refPIN, refPUK} {
		if data, _ := send(t, c, metadataCmd(ref)); data[5] != 0 {
			t.Errorf("reference %02X given as the factory value says it is the default: %x", ref, data)
		}
	}
	c = newSelected(t, Options{PIN: "654321"})
	if data, _ := send(t, c, metadataCmd(refPUK)); data[5] != 1 {
		t.Errorf("a PUK that was not given says it is not the default: %x", data)
	}
	if data, _ := send(t, c, metadataCmd(refPIN)); data[5] != 0 {
		t.Errorf("a PIN that was given says it is the default: %x", data)
	}
	wantSW(t, "the factory PIN on a card with another PIN", qaSWOf(t, c, verifyCmd(DefaultPIN)), 0x63C2)
}

// "A PIN or a PUK is 6 to 8 bytes and travels padded with FF to 8."
func TestQaAPINAndAPUKOfSixToEightBytesTravelPaddedWithFF(t *testing.T) {
	for n := 6; n <= 8; n++ {
		val := strings.Repeat("7", n)
		puk := strings.Repeat("9", n)
		c := newSelected(t, Options{PIN: val, PUK: puk})
		if n < 8 {
			zero := append([]byte(val), make([]byte, 8-n)...) // padded with 00: another value
			wantSW(t, "padded with 00", qaSWOf(t, c, short(0, insVerify, 0, refPIN, zero, -1)), 0x63C2)
		}
		wantSW(t, fmt.Sprintf("a PIN of %d bytes", n), qaSWOf(t, c, verifyCmd(val)), swOK)
		wantSW(t, fmt.Sprintf("a PUK of %d bytes", n), qaSWOf(t, c, changeCmd(refPUK, puk, "87654321")), swOK)
	}
	t.Run("an FF inside the value", func(t *testing.T) {
		c := newSelected(t, Options{PIN: "ab\xffcde"})
		wantSW(t, "VERIFY", qaSWOf(t, c, verifyCmd("ab\xffcde")), swOK)
	})
	t.Run("a new value of 6 to 8 bytes", func(t *testing.T) {
		for n := 6; n <= 8; n++ {
			next := strings.Repeat("5", n)
			c := newSelected(t, Options{})
			wantSW(t, "CHANGE PIN", qaSWOf(t, c, changeCmd(refPIN, DefaultPIN, next)), swOK)
			wantSW(t, "VERIFY the new PIN", qaSWOf(t, c, verifyCmd(next)), swOK)
			wantSW(t, "CHANGE PUK", qaSWOf(t, c, changeCmd(refPUK, DefaultPUK, next)), swOK)
			wantSW(t, "CHANGE PUK again", qaSWOf(t, c, changeCmd(refPUK, next, DefaultPUK)), swOK)
		}
	})
	t.Run("a new value that is all padding", func(t *testing.T) {
		c := newSelected(t, Options{})
		wantSW(t, "CHANGE", qaSWOf(t, c, short(0, insChange, 0, refPIN, join(pad(DefaultPIN), bytes.Repeat([]byte{0xFF}, 8)), -1)), swWrongData)
	})
	t.Run("a block of 15 or 17 bytes is not a CHANGE", func(t *testing.T) {
		c := newSelected(t, Options{})
		block := join(pad(DefaultPIN), pad("654321"))
		wantSW(t, "15 bytes", qaSWOf(t, c, short(0, insChange, 0, refPIN, block[:15], -1)), swWrongLength)
		wantSW(t, "17 bytes", qaSWOf(t, c, short(0, insChange, 0, refPIN, append(block, 0xFF), -1)), swWrongLength)
		if c.st().pin.left != 3 {
			t.Error("a refused CHANGE spent a try")
		}
	})
}

// "The card state survives PowerOff: the firmware version, the serial number, the PIN and the PUK, their counters, their maxima and whether
// each is still the factory value, and the data objects. The session state does not: the selected application, the verified PIN, the
// buffer of a chained command and the rest of a response that waits for GET RESPONSE. Every session starts at SELECT."
func TestQaTheCardStateSurvivesAPowerCycleAndTheSessionStateDoesNot(t *testing.T) {
	o := Options{Firmware: [3]byte{5, 7, 4}, Serial: 0xCAFEBABE, PIN: "424242", PUK: "13579246", MaxPINTries: 7, MaxPUKTries: 9, ResponseMode: Short61xx}
	c := newSelected(t, o)
	obj := bigObject(600)
	c.PutObject(ObjectSlot9A, obj)
	qaSWOf(t, c, verifyCmd("000000"))
	qaSWOf(t, c, verifyCmd("000000"))
	wantSW(t, "CHANGE PUK", qaSWOf(t, c, changeCmd(refPUK, "13579246", "24681357")), swOK)
	wantSW(t, "a wrong PUK", qaSWOf(t, c, changeCmd(refPUK, "13579246", "24681357")), 0x63C8)
	send(t, c, short(claChain, insGetData, 0x3F, 0xFF, []byte{0x5C, 0x03}, -1)) // a chain waits
	for cycle := 0; cycle < 2; cycle++ {
		c.PowerOff()
		c.PowerOff()
		c.PowerOn()
		wantSW(t, "before SELECT", qaSWOf(t, c, emptyVerify()), swNotAllowed)
		selectPIV(t, c)
		wantSW(t, "the counter", qaSWOf(t, c, emptyVerify()), 0x63C5)
		if data, _ := send(t, c, metadataCmd(refPIN)); !bytes.Equal(data, []byte{1, 1, 0xFF, 5, 1, 0, 6, 2, 7, 5}) {
			t.Errorf("PIN metadata %x", data)
		}
		if data, _ := send(t, c, metadataCmd(refPUK)); !bytes.Equal(data, []byte{1, 1, 0xFF, 5, 1, 0, 6, 2, 9, 8}) {
			t.Errorf("PUK metadata %x", data)
		}
		if data, _ := send(t, c, []byte{0, insGetVersion, 0, 0}); !bytes.Equal(data, []byte{5, 7, 4}) {
			t.Errorf("version %x", data)
		}
		if data, _ := send(t, c, []byte{0, insGetSerial, 0, 0}); !bytes.Equal(data, []byte{0xCA, 0xFE, 0xBA, 0xBE}) {
			t.Errorf("serial %x", data)
		}
		// The chain is gone: the last part alone is a malformed tag list.
		wantSW(t, "the last part alone", qaSWOf(t, c, short(0, insGetData, 0x3F, 0xFF, []byte{0x5F, 0xC1, 0x05}, 0)), swWrongData)
		first, sw := send(t, c, getDataCmd(ObjectSlot9A, 0))
		wantSW(t, "GET DATA", sw, swMore)
		if !bytes.Equal(first, obj[:256]) {
			t.Error("the object changed")
		}
		// The rest waits; a power cycle takes it.
		if cycle == 0 {
			continue
		}
		c.PowerOff()
		c.PowerOn()
		selectPIV(t, c)
		wantSW(t, "the rest of the response", qaSWOf(t, c, getResponseCmd(0)), swWrongP1P2)
	}
	wantSW(t, "the old PUK", qaSWOf(t, c, changeCmd(refPUK, "13579246", "24681357")), 0x63C7)
	wantSW(t, "the new PUK", qaSWOf(t, c, changeCmd(refPUK, "24681357", "13579246")), swOK)
	wantSW(t, "the PIN, back at its maximum", qaSWOf(t, c, verifyCmd("424242")), swOK)
	if data, _ := send(t, c, metadataCmd(refPIN)); data[9] != 7 {
		t.Errorf("PIN metadata %x", data)
	}
}

// "Another INS answers 6D00 and another CLA 6E00", "Before SELECT, every instruction other than SELECT answers 6985", and the part of the
// list that says what is "not part of this card yet".
func TestQaEveryInstructionAndEveryClassAnswersAsTheDocSays(t *testing.T) {
	known := map[byte]bool{insSelect: true, insGetVersion: true, insGetSerial: true, insVerify: true, insChange: true, insGetMetadata: true,
		insGetData: true, insGetResponse: true}
	for ins := 0; ins < 256; ins++ {
		c := New(Options{})
		c.PowerOn()
		sw := qaSWOf(t, c, []byte{0, byte(ins), 0, 0})
		if ins == insSelect {
			if sw == swNotAllowed {
				t.Errorf("SELECT before SELECT answered 6985")
			}
		} else if sw != swNotAllowed {
			t.Errorf("INS %02X before SELECT: SW = %04X, want 6985", ins, sw)
		}
		selectPIV(t, c)
		sw = qaSWOf(t, c, []byte{0, byte(ins), 0, 0})
		if known[byte(ins)] == (sw == swInsNotAllowed) {
			t.Errorf("INS %02X after SELECT: SW = %04X, known: %v", ins, sw, known[byte(ins)])
		}
		wantForbidden := 0
		if qaIsTripwire(byte(ins)) {
			wantForbidden = 2
		}
		if n := len(c.Forbidden()); n != wantForbidden {
			t.Errorf("INS %02X: %d forbidden entries, want %d", ins, n, wantForbidden)
		}
	}
	for _, tc := range []struct {
		name string
		ins  byte
	}{{"PUT DATA", 0xDB}, {"GENERAL AUTHENTICATE", 0x87}, {"GENERATE ASYMMETRIC KEY PAIR", 0x47}, {"ATTEST", 0xF9}, {"SET MANAGEMENT KEY", 0xFF},
		{"MOVE KEY", 0xF6}, {"DELETE KEY", 0xF2}, {"IMPORT ASYMMETRIC KEY", 0xFE}} {
		c := newSelected(t, Options{})
		wantSW(t, tc.name, qaSWOf(t, c, short(0, tc.ins, 0, 0x9A, []byte{1, 2, 3}, -1)), swInsNotAllowed)
	}
	for cla := 0; cla < 256; cla++ {
		c := newSelected(t, Options{})
		data, sw := send(t, c, []byte{byte(cla), insGetVersion, 0, 0})
		switch cla {
		case 0x00:
			if sw != swOK || len(data) != 3 {
				t.Errorf("CLA 00: %x %04X", data, sw)
			}
		case 0x10:
			if sw != swOK || len(data) != 0 {
				t.Errorf("CLA 10 is a part of a chain, answered 9000 with no data: %x %04X", data, sw)
			}
		default:
			if sw != swClaNotAllowed || len(data) != 0 {
				t.Errorf("CLA %02X: %x %04X, want 6E00", cla, data, sw)
			}
		}
	}
}

// R8: "P1 and P2 are held strictly here", and 6700, 6A86 and 6A88 as the doc gives them, and a refused command spends nothing.
func TestQaTheRefusalsOfR8SpendNothingAndChangeNothing(t *testing.T) { // assumed: R8
	c := newSelected(t, Options{})
	wantSW(t, "VERIFY", qaSWOf(t, c, verifyCmd(DefaultPIN)), swOK)
	good8, good16 := pad(DefaultPIN), join(pad(DefaultPIN), pad("654321"))
	refuse := func(name string, apdu []byte, want uint16) {
		t.Helper()
		if sw := qaSWOf(t, c, apdu); sw != want {
			t.Errorf("%s: SW = %04X, want %04X", name, sw, want)
		}
	}
	for p := 0; p < 256; p++ {
		b := byte(p)
		if p != 0 {
			if p == 0xFF { // VERIFY with P1 FF resets the security status (SP 800-73-4), and takes no data: with the PIN it is 6700 and changes nothing
				refuse("VERIFY P1 FF with data", short(0, insVerify, b, refPIN, good8, -1), swWrongLength)
				refuse("VERIFY P1 FF with data, extended", extended(0, insVerify, b, refPIN, good8, -1), swWrongLength)
				refuse("VERIFY P1 FF, P2 of the PUK", []byte{0, insVerify, b, refPUK}, swRefNotFound)
			} else {
				refuse("VERIFY P1", short(0, insVerify, b, refPIN, good8, -1), swWrongP1P2)
				refuse("VERIFY P1, no data", []byte{0, insVerify, b, refPIN}, swWrongP1P2)
			}
			refuse("CHANGE P1", short(0, insChange, b, refPIN, good16, -1), swWrongP1P2)
			refuse("GET METADATA P1", short(0, insGetMetadata, b, refPIN, nil, 0), swWrongP1P2)
			refuse("GET VERSION P1", []byte{0, insGetVersion, b, 0}, swWrongP1P2)
			refuse("GET VERSION P2", []byte{0, insGetVersion, 0, b}, swWrongP1P2)
			refuse("GET SERIAL P1", []byte{0, insGetSerial, b, 0}, swWrongP1P2)
			refuse("GET SERIAL P2", []byte{0, insGetSerial, 0, b}, swWrongP1P2)
		}
		if p != refPIN {
			refuse("VERIFY P2", short(0, insVerify, 0, b, good8, -1), swRefNotFound)
		}
		if p != refPIN && p != refPUK {
			refuse("CHANGE P2", short(0, insChange, 0, b, good16, -1), swRefNotFound)
			refuse("GET METADATA P2", short(0, insGetMetadata, 0, b, nil, 0), swRefNotFound)
		}
		list := []byte{0x5C, 3, 0x5F, 0xC1, 5}
		if p != 0x3F {
			refuse("GET DATA P1", short(0, insGetData, b, 0xFF, list, 0), swWrongP1P2)
		}
		if p != 0xFF {
			refuse("GET DATA P2", short(0, insGetData, 0x3F, b, list, 0), swWrongP1P2)
		}
		for q := 0; q < 256; q++ {
			if p == 4 && q == 0 {
				continue
			}
			refuse("SELECT P1 P2", short(0, insSelect, b, byte(q), aidShort, 0), swWrongP1P2)
		}
	}
	for k := 1; k < 60; k++ {
		data := bytes.Repeat([]byte{0x31}, k)
		if k != 8 {
			refuse("VERIFY data length", short(0, insVerify, 0, refPIN, data, -1), swWrongLength)
		}
		if k != 16 {
			refuse("CHANGE PIN data length", short(0, insChange, 0, refPIN, data, -1), swWrongLength)
			refuse("CHANGE PUK data length", short(0, insChange, 0, refPUK, data, -1), swWrongLength)
		}
		refuse("GET METADATA data", short(0, insGetMetadata, 0, refPIN, data, -1), swWrongLength)
		refuse("GET VERSION data", short(0, insGetVersion, 0, 0, data, -1), swWrongLength)
		refuse("GET SERIAL data", short(0, insGetSerial, 0, 0, data, -1), swWrongLength)
	}
	// GET RESPONSE with data, with something waiting, is 6700.
	c.PutObject(ObjectSlot9A, bigObject(600))
	wantSW(t, "GET DATA", qaSWOf(t, c, getDataCmd(ObjectSlot9A, 0)), swMore)
	refuse("GET RESPONSE with data", short(0, insGetResponse, 0, 0, []byte{1, 2, 3}, -1), swWrongLength)
	// Nothing was spent, nothing was changed, and no failed SELECT or malformed command took the verified PIN away.
	if c.st().pin.left != 3 || c.st().puk.left != 3 || !c.st().pin.isDefault || !c.st().puk.isDefault || string(c.st().pin.value) != DefaultPIN || string(c.st().puk.value) != DefaultPUK {
		t.Errorf("a refusal changed the card: PIN %+v PUK %+v", c.st().pin, c.st().puk)
	}
	wantSW(t, "the PIN is still verified", qaSWOf(t, c, emptyVerify()), swOK)
}

// R8: "6A80 for a new PIN or PUK of fewer than 6 or more than 8 bytes (checked before a try is spent)".
func TestQaANewValueIsCheckedBeforeATryIsSpent(t *testing.T) { // assumed: R8
	for _, ref := range []byte{refPIN, refPUK} {
		old := DefaultPIN
		if ref == refPUK {
			old = DefaultPUK
		}
		for _, next := range []string{"", "1", "12345"} {
			c := newSelected(t, Options{})
			wantSW(t, "a wrong old value and a short new one", qaSWOf(t, c, changeCmd(ref, "000000", next)), swWrongData)
			wantSW(t, "the right old value and a short new one", qaSWOf(t, c, changeCmd(ref, old, next)), swWrongData)
			if c.st().pin.left != 3 || c.st().puk.left != 3 {
				t.Errorf("a refused new value spent a try: PIN %d PUK %d", c.st().pin.left, c.st().puk.left)
			}
			// even for a blocked reference the new value is looked at first
			for i := 0; i < 3; i++ {
				qaSWOf(t, c, changeCmd(ref, "000000", "654321"))
			}
			wantSW(t, "a blocked reference and a short new value", qaSWOf(t, c, changeCmd(ref, old, next)), swWrongData)
			wantSW(t, "a blocked reference and a good new value", qaSWOf(t, c, changeCmd(ref, old, "654321")), swBlocked)
		}
	}
}

// R20: "A wrong value in VERIFY, or a wrong old PIN in CHANGE REFERENCE DATA, leaves the PIN not verified." Nothing else takes the verified
// PIN away but SELECT and a power cycle; a successful change does not give it.
func TestQaWhatLeavesThePINVerifiedAndWhatDoesNot(t *testing.T) { // assumed: R20
	verified := func(c *Card) bool {
		t.Helper()
		switch sw := qaSWOf(t, c, emptyVerify()); {
		case sw == swOK:
			return true
		case sw>>4 == 0x63C:
			return false
		default:
			t.Fatalf("empty VERIFY answered %04X", sw)
			return false
		}
	}
	fresh := func() *Card {
		c := newSelected(t, Options{})
		wantSW(t, "VERIFY", qaSWOf(t, c, verifyCmd(DefaultPIN)), swOK)
		return c
	}
	c := fresh()
	qaSWOf(t, c, changeCmd(refPIN, "000000", "654321"))
	if verified(c) {
		t.Error("a wrong old PIN in CHANGE left the PIN verified")
	}
	c = fresh()
	qaSWOf(t, c, changeCmd(refPUK, "00000000", "87654321"))
	if !verified(c) {
		t.Error("a wrong old PUK took the verified PIN away")
	}
	c = fresh()
	wantSW(t, "CHANGE PIN", qaSWOf(t, c, changeCmd(refPIN, DefaultPIN, "654321")), swOK)
	if !verified(c) {
		t.Error("a successful change of the PIN took the verified PIN away")
	}
	wantSW(t, "CHANGE PUK", qaSWOf(t, c, changeCmd(refPUK, DefaultPUK, "87654321")), swOK)
	if !verified(c) {
		t.Error("a successful change of the PUK took the verified PIN away")
	}
	c = newSelected(t, Options{})
	wantSW(t, "CHANGE PIN", qaSWOf(t, c, changeCmd(refPIN, DefaultPIN, "654321")), swOK)
	if verified(c) {
		t.Error("a successful change of the PIN verified the PIN: the doc does not say so")
	}
	c = fresh()
	for _, apdu := range [][]byte{{0, insGetVersion, 0, 0}, metadataCmd(refPIN), getDataCmd(ObjectSlot9A, 0), {0, 0x99, 0, 0}, {0, 0xFB, 0, 0},
		short(claChain, insGetVersion, 0, 0, nil, -1), {0, insVerify, 1, 2}, {0x80, insVerify, 0, 0x80}, {0, 1}} {
		send(t, c, apdu)
		if !verified(c) {
			t.Errorf("%x took the verified PIN away", apdu)
		}
	}
	c = fresh()
	wantSW(t, "a right VERIFY again", qaSWOf(t, c, verifyCmd(DefaultPIN)), swOK)
	wantSW(t, "a wrong VERIFY", qaSWOf(t, c, verifyCmd("000000")), 0x63C2)
	if verified(c) {
		t.Error("a wrong VERIFY left the PIN verified")
	}
	// a blocked PIN is not verified, and the right value does not unblock it
	c = newSelected(t, Options{MaxPINTries: 1})
	qaSWOf(t, c, verifyCmd("000000"))
	wantSW(t, "the right value, blocked", qaSWOf(t, c, verifyCmd(DefaultPIN)), swBlocked)
	if verified(c) {
		t.Error("a blocked PIN is verified")
	}
}

// R10: "GET RESPONSE with nothing waiting answers 6A86, and any command other than GET RESPONSE drops what waits."
func TestQaEveryCommandButGetResponseDropsWhatWaits(t *testing.T) { // assumed: R10
	for name, apdu := range map[string][]byte{
		"GET VERSION":            {0, insGetVersion, 0, 0},
		"GET SERIAL":             {0, insGetSerial, 0, 0},
		"VERIFY":                 verifyCmd("000000"),
		"empty VERIFY":           emptyVerify(),
		"CHANGE, refused":        changeCmd(refPIN, DefaultPIN, "123"),
		"CHANGE PUK":             changeCmd(refPUK, "00000000", "87654321"),
		"GET METADATA":           metadataCmd(refPUK),
		"GET DATA":               getDataCmd(ObjectSlot9C, 0),
		"SELECT":                 short(0, insSelect, 4, 0, aidShort, 0),
		"SELECT of another AID":  short(0, insSelect, 4, 0, []byte{1, 2, 3, 4, 5}, 0),
		"INS FB":                 {0, insReset, 0, 0},
		"unknown INS":            {0, 0x99, 0, 0},
		"chained part":           short(claChain, insVerify, 0, refPIN, []byte{1}, -1),
		"chained GET RESPONSE":   short(claChain, insGetResponse, 0, 0, nil, 0),
		"wrong CLA":              {0x80, insGetVersion, 0, 0},
		"extended":               extended(0, insGetVersion, 0, 0, nil, 0),
		"a length that is wrong": {0, insVerify, 0, 0x80, 8, 1},
		"too short":              {0, insGetResponse, 0},
	} {
		c := newSelected(t, Options{})
		c.PutObject(ObjectSlot9A, bigObject(600))
		wantSW(t, name+": GET DATA", qaSWOf(t, c, getDataCmd(ObjectSlot9A, 0)), swMore)
		send(t, c, apdu)
		wantSW(t, name+": GET RESPONSE", qaSWOf(t, c, getResponseCmd(0)), swWrongP1P2)
	}
	t.Run("GET RESPONSE keeps the rest, in the short form and in the extended one", func(t *testing.T) {
		for _, apdu := range [][]byte{getResponseCmd(10), {0, insGetResponse, 0, 0}, extended(0, insGetResponse, 0, 0, nil, 10)} {
			c := newSelected(t, Options{})
			c.PutObject(ObjectSlot9A, bigObject(600))
			send(t, c, getDataCmd(ObjectSlot9A, 0))
			data, sw := send(t, c, apdu)
			if sw>>8 != 0x61 || len(data) == 0 || len(data) > 256 {
				t.Errorf("GET RESPONSE %x: %d bytes, %04X", apdu, len(data), sw)
			}
			if sw2 := qaSWOf(t, c, getResponseCmd(1)); sw2>>8 != 0x61 && sw2 != swOK {
				t.Errorf("a second GET RESPONSE: %04X", sw2)
			}
		}
	})
	t.Run("GET RESPONSE with nothing waiting, in every form", func(t *testing.T) {
		c := newSelected(t, Options{})
		for _, apdu := range [][]byte{getResponseCmd(0), {0, insGetResponse, 0, 0}, extended(0, insGetResponse, 0, 0, nil, 0), getResponseCmd(7)} {
			wantSW(t, fmt.Sprintf("%x", apdu), qaSWOf(t, c, apdu), swWrongP1P2)
		}
	})
}

// R21: "a part with an INS, P1 or P2 other than the first drops the chain and answers 6700, so does a chain of more than 65535 bytes, and a
// last part with another header than the chain runs alone."
func TestQaChainingAsTheTableOfAssumptionsSaysIt(t *testing.T) { // assumed: R21
	seeded := func() *Card {
		c := newSelected(t, Options{})
		c.PutObject(ObjectSlot9A, []byte{0x53, 0x01, 0xAB})
		return c
	}
	first := short(claChain, insGetData, 0x3F, 0xFF, []byte{0x5C, 0x03}, -1)
	last := short(claPlain, insGetData, 0x3F, 0xFF, []byte{0x5F, 0xC1, 0x05}, 0)
	t.Run("the whole chain runs", func(t *testing.T) {
		c := seeded()
		wantSW(t, "first", qaSWOf(t, c, first), swOK)
		data, sw := send(t, c, last)
		wantSW(t, "last", sw, swOK)
		if !bytes.Equal(data, []byte{0x53, 0x01, 0xAB}) {
			t.Errorf("data = %x", data)
		}
	})
	for name, part := range map[string][]byte{
		"INS": short(claChain, insGetSerial, 0x3F, 0xFF, []byte{0x5F}, -1),
		"P1":  short(claChain, insGetData, 0x3E, 0xFF, []byte{0x5F}, -1),
		"P2":  short(claChain, insGetData, 0x3F, 0xFE, []byte{0x5F}, -1),
	} {
		c := seeded()
		send(t, c, first)
		wantSW(t, "a part with another "+name, qaSWOf(t, c, part), swWrongLength)
		wantSW(t, "the last part after the drop, with another "+name, qaSWOf(t, c, last), swWrongData)
	}
	for name, lastPart := range map[string][]byte{
		"INS": {0, insGetVersion, 0, 0},
		"P1":  short(claPlain, insGetData, 0x3E, 0xFF, []byte{0x5F, 0xC1, 0x05}, 0),
		"P2":  short(claPlain, insGetData, 0x3F, 0xFE, []byte{0x5F, 0xC1, 0x05}, 0),
	} {
		c := seeded()
		send(t, c, first)
		sw := qaSWOf(t, c, lastPart)
		if name == "INS" {
			wantSW(t, "a last part with another "+name+" runs alone", sw, swOK)
		} else {
			wantSW(t, "a last part with another "+name+" runs alone", sw, swWrongP1P2)
		}
		wantSW(t, "and the chain is gone", qaSWOf(t, c, last), swWrongData)
	}
	t.Run("65535 bytes are the most", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			parts []int
			want  []uint16
		}{
			{"one part of 65535", []int{65535}, []uint16{swOK}},
			{"two parts that make 65535", []int{40000, 25535}, []uint16{swOK, swOK}},
			{"two parts that make 65536", []int{40000, 25536}, []uint16{swOK, swWrongLength}},
			{"a part of 65535 and a part of one", []int{65535, 1}, []uint16{swOK, swWrongLength}},
			{"three parts", []int{30000, 30000, 5535}, []uint16{swOK, swOK, swOK}},
		} {
			c := seeded()
			for i, n := range tc.parts {
				wantSW(t, fmt.Sprintf("%s: part %d", tc.name, i), qaSWOf(t, c, extended(claChain, insGetData, 0x3F, 0xFF, make([]byte, n), -1)), tc.want[i])
			}
			// after a refused part the chain is gone; after an accepted one, the last part joins it
			if tc.want[len(tc.want)-1] == swWrongLength {
				wantSW(t, tc.name+": the last part alone", qaSWOf(t, c, last), swWrongData)
			}
		}
	})
	t.Run("a chain of SELECT", func(t *testing.T) {
		c := New(Options{})
		c.PowerOn()
		wantSW(t, "part", qaSWOf(t, c, short(claChain, insSelect, 4, 0, aidFull[:4], -1)), swOK)
		wantSW(t, "part", qaSWOf(t, c, short(claChain, insSelect, 4, 0, aidFull[4:7], -1)), swOK)
		data, sw := send(t, c, short(claPlain, insSelect, 4, 0, aidFull[7:], 0))
		wantSW(t, "last", sw, swOK)
		if !bytes.Equal(data, selectResponse) {
			t.Errorf("data = %x", data)
		}
		wantSW(t, "selected", qaSWOf(t, c, []byte{0, insGetVersion, 0, 0}), swOK)
	})
	t.Run("a part answers 9000 and no data, whatever its Le", func(t *testing.T) {
		for _, apdu := range [][]byte{
			short(claChain, insGetVersion, 0, 0, nil, 0), short(claChain, insGetVersion, 0, 0, nil, 3), extended(claChain, insGetVersion, 0, 0, nil, 0),
			short(claChain, insGetData, 0x3F, 0xFF, []byte{0x5C, 3}, 0), extended(claChain, insGetData, 0x3F, 0xFF, []byte{0x5C, 3}, 0),
		} {
			c := seeded()
			data, sw := send(t, c, apdu)
			if sw != swOK || len(data) != 0 {
				t.Errorf("%x: %x %04X", apdu, data, sw)
			}
		}
	})
	t.Run("before SELECT a part is refused and opens no chain", func(t *testing.T) {
		c := New(Options{})
		c.PowerOn()
		c.PutObject(ObjectSlot9A, []byte{0x53, 0x01, 0xAB})
		wantSW(t, "part", qaSWOf(t, c, first), swNotAllowed)
		selectPIV(t, c)
		wantSW(t, "the last part alone", qaSWOf(t, c, last), swWrongData)
	})
	t.Run("another CLA, a malformed part and a refused extended part drop the chain", func(t *testing.T) {
		for name, apdu := range map[string][]byte{
			"CLA":       {0x80, insGetData, 0x3F, 0xFF},
			"malformed": {claChain, insGetData, 0x3F, 0xFF, 9, 1},
			"short":     {claChain, insGetData},
		} {
			c := seeded()
			send(t, c, first)
			send(t, c, apdu)
			wantSW(t, name+": the last part alone", qaSWOf(t, c, last), swWrongData)
		}
		c := newSelected(t, Options{NoExtendedAPDU: true})
		c.PutObject(ObjectSlot9A, []byte{0x53, 0x01, 0xAB})
		send(t, c, first)
		wantSW(t, "an extended part", qaSWOf(t, c, extended(claChain, insGetData, 0x3F, 0xFF, []byte{0x5F}, -1)), swWrongLength)
		wantSW(t, "the last part alone", qaSWOf(t, c, last), swWrongData)
	})
}

// R22: "A command with no Le is read as a command with the largest Le: 256 bytes, or 65536 when it came extended in the mode Extended."
func TestQaACommandWithNoLeIsReadAsTheLargestLe(t *testing.T) { // assumed: R22
	id := []byte{0x5C, 0x03, 0x5F, 0xC1, 0x05}
	obj := make([]byte, 70000)
	for i := range obj {
		obj[i] = byte(i)
	}
	for _, tc := range []struct {
		name     string
		mode     ResponseMode
		apdu     []byte
		wantLen  int
		wantSW   uint16
		extended bool
	}{
		{"short, no Le, Extended", Extended, short(0, insGetData, 0x3F, 0xFF, id, -1), 256, swMore | qaLow(len(obj)-256), false},
		{"short, no Le, Short61xx", Short61xx, short(0, insGetData, 0x3F, 0xFF, id, -1), 256, swMore | qaLow(len(obj)-256), false},
		{"extended, no Le, Extended", Extended, extended(0, insGetData, 0x3F, 0xFF, id, -1), 65536, swMore | qaLow(len(obj)-65536), true},
		{"extended, no Le, Short61xx", Short61xx, extended(0, insGetData, 0x3F, 0xFF, id, -1), 256, swMore | qaLow(len(obj)-256), true},
	} {
		c := newSelected(t, Options{ResponseMode: tc.mode})
		c.PutObject(ObjectSlot9A, obj)
		data, sw := send(t, c, tc.apdu)
		if len(data) != tc.wantLen {
			t.Errorf("%s: %d bytes, want %d", tc.name, len(data), tc.wantLen)
		}
		if sw != tc.wantSW {
			t.Errorf("%s: SW %04X, want %04X", tc.name, sw, tc.wantSW)
		}
		if !bytes.Equal(data, obj[:len(data)]) {
			t.Errorf("%s: wrong bytes", tc.name)
		}
	}
	t.Run("GET RESPONSE with no Le", func(t *testing.T) {
		c := newSelected(t, Options{})
		c.PutObject(ObjectSlot9A, obj[:1000])
		send(t, c, getDataCmd(ObjectSlot9A, 0))
		data, sw := send(t, c, []byte{0, insGetResponse, 0, 0})
		if len(data) != 256 || sw != swMore {
			t.Errorf("%d bytes %04X, want 256 and 6100", len(data), sw)
		}
	})
}

// "The tripwire is a list of rules in tripwire.go: a later rule is one more entry", and "a rule that reads the data may match only the whole
// of a chained command".
func TestQaALaterRuleIsOneMoreEntryAndSeesTheJoinedChain(t *testing.T) {
	saved := tripwire
	t.Cleanup(func() { tripwire = saved })
	tripwire = append(append([]rule(nil), saved...), rule{"INS 77 with six bytes of data", func(c *command) bool { return c.ins == 0x77 && len(c.data) == 6 }})
	c := newSelected(t, Options{})
	send(t, c, short(0, 0x77, 0, 0, []byte{1, 2, 3, 4, 5}, -1)) // five bytes: no
	if n := len(c.Forbidden()); n != 0 {
		t.Fatalf("a rule that reads the data matched five bytes: %+v", c.Forbidden())
	}
	send(t, c, short(0, 0x77, 0, 0, []byte{1, 2, 3, 4, 5, 6}, -1)) // six bytes: yes
	send(t, c, short(claChain, 0x77, 0, 0, []byte{1, 2, 3}, -1))   // three, then two: no
	send(t, c, short(claChain, 0x77, 0, 0, []byte{4, 5}, -1))
	if n := len(c.Forbidden()); n != 1 {
		t.Fatalf("%d entries after the single command and two parts: %+v", n, c.Forbidden())
	}
	send(t, c, short(claPlain, 0x77, 0, 0, []byte{6}, -1)) // joined: six
	f := c.Forbidden()
	if len(f) != 2 || f[1].DataLen != 1 || f[1].CLA != claPlain || !strings.Contains(f[1].Why, "six bytes") {
		t.Errorf("Forbidden = %+v", f)
	}
	// The three rules of the package are still there.
	send(t, c, []byte{0, insReset, 0, 0})
	if n := len(c.Forbidden()); n != 3 {
		t.Errorf("INS FB is no longer recorded: %d entries", n)
	}
}

// "RequireNoForbidden fails a test that sent one", and what it says.
func TestQaRequireNoForbiddenSaysWhichCommandAndHowMany(t *testing.T) {
	c := newSelected(t, Options{})
	send(t, c, short(0x80, insImportKey, 0x07, 0x9A, []byte{1, 2, 3}, -1))
	send(t, c, []byte{0, insSetRetries, 3, 3})
	var ft fakeT
	RequireNoForbidden(&ft, c)
	for _, want := range []string{"2 forbidden", "CLA 80 INS FE P1 07 P2 9A", "imports a key"} {
		if !ft.failed || !strings.Contains(ft.msg, want) {
			t.Errorf("message %q does not hold %q", ft.msg, want)
		}
	}
	for _, secret := range []string{"010203", "01 02 03"} {
		if strings.Contains(ft.msg, secret) {
			t.Errorf("message holds the data of the command: %q", ft.msg)
		}
	}
}

// "Forbidden ... are recorded whatever the card answers", and a card that is off records nothing.
func TestQaAForbiddenInstructionIsRecordedWhateverTheAnswerIs(t *testing.T) {
	type tc struct {
		name string
		o    Options
		pre  func(*Card)
		apdu []byte
		want uint16
	}
	for _, x := range []tc{
		{"6D00", Options{}, nil, []byte{0, insReset, 0, 0}, swInsNotAllowed},
		{"6985, before SELECT", Options{}, func(c *Card) { c.PowerOff(); c.PowerOn() }, []byte{0, insSetRetries, 0, 0}, swNotAllowed},
		{"6E00, wrong CLA", Options{}, nil, []byte{0x80, insImportKey, 0, 0}, swClaNotAllowed},
		{"6700, extended refused", Options{NoExtendedAPDU: true}, nil, extended(0, insReset, 0, 0, nil, 0), swWrongLength},
		{"6700, malformed", Options{}, nil, []byte{0, insReset, 0, 0, 5, 1}, swWrongLength},
		{"9000, a part of a chain", Options{}, nil, short(claChain, insImportKey, 0, 0, []byte{1}, -1), swOK},
		{"6700, a part that does not fit the chain", Options{}, func(c *Card) { send(t, c, short(claChain, insGetVersion, 0, 0, nil, -1)) }, short(claChain, insSetRetries, 0, 0, nil, -1), swWrongLength},
	} {
		c := newSelected(t, x.o)
		if x.pre != nil {
			x.pre(c)
		}
		before := len(c.Forbidden())
		wantSW(t, x.name, qaSWOf(t, c, x.apdu), x.want)
		f := c.Forbidden()
		if len(f) != before+1 || f[before].SW != x.want || f[before].Why == "" || f[before].INS != x.apdu[1] {
			t.Errorf("%s: Forbidden = %+v", x.name, f)
		}
	}
	c := newSelected(t, Options{})
	c.PowerOff()
	if resp, _ := c.Exchange([]byte{0, insReset, 0, 0}); resp != nil || len(c.Forbidden()) != 0 || len(c.Log()) != 1 {
		t.Errorf("a card that is off recorded a command: %d forbidden, %d log", len(c.Forbidden()), len(c.Log()))
	}
}

// The ids of the data objects, as the doc lists them, and "GET DATA returns the bytes that PutObject seeded, as they were seeded".
func TestQaTheObjectIdsAndTheBytesOfAnObject(t *testing.T) {
	for _, id := range []struct {
		got, want uint32
	}{{ObjectSlot9A, 0x5FC105}, {ObjectSlot9C, 0x5FC10A}, {ObjectSlot9D, 0x5FC10B}, {ObjectSlot9E, 0x5FC101}, {ObjectRetired1, 0x5FC10D}, {ObjectAttestation, 0x5FFF01}} {
		if id.got != id.want {
			t.Errorf("id %06X, the doc says %06X", id.got, id.want)
		}
	}
	c := newSelected(t, Options{})
	ids := []uint32{ObjectSlot9A, ObjectSlot9C, ObjectSlot9D, ObjectSlot9E, ObjectAttestation}
	for i := uint32(0); i < 20; i++ {
		ids = append(ids, ObjectRetired1+i)
	}
	body := func(id uint32) []byte {
		return append(append([]byte{0x53, 0x04}, binary.BigEndian.AppendUint32(nil, id<<8)[:3]...), 0xEE)
	}
	for _, id := range ids {
		c.PutObject(id, body(id))
	}
	for _, id := range ids {
		data, sw := send(t, c, getDataCmd(id, 0))
		if sw != swOK || !bytes.Equal(data, body(id)) {
			t.Errorf("object %06X: %x %04X", id, data, sw)
		}
	}
	for _, id := range []uint32{0x5FC100, 0x5FC102, 0x5FC104, 0x5FC106, 0x5FC109, 0x5FC10C, ObjectRetired1 + 20, 0x5FC122, 0x5FFF00, 0x5FFF02, 0x000000, 0xFFFFFF, 0x7E0000} {
		if _, sw := send(t, c, getDataCmd(id, 0)); sw != swNotFound {
			t.Errorf("object %06X was not seeded: SW %04X", id, sw)
		}
	}
	t.Run("bytes that are not a data object come back as they are", func(t *testing.T) {
		for _, raw := range [][]byte{{0x00}, {0xFF, 0xFF}, {0x53}, {0x53, 0xFF, 0x01}, {0x70, 0x00, 0x71}, bytes.Repeat([]byte{0x00}, 100)} {
			c.PutObject(ObjectSlot9D, raw)
			if data, sw := send(t, c, getDataCmd(ObjectSlot9D, 0)); sw != swOK || !bytes.Equal(data, raw) {
				t.Errorf("%x came back as %x %04X", raw, data, sw)
			}
		}
	})
	t.Run("PutObject replaces, and an empty body removes", func(t *testing.T) {
		c.PutObject(ObjectSlot9D, []byte{1})
		c.PutObject(ObjectSlot9D, []byte{2, 3})
		if data, _ := send(t, c, getDataCmd(ObjectSlot9D, 0)); !bytes.Equal(data, []byte{2, 3}) {
			t.Errorf("data = %x", data)
		}
		c.PutObject(ObjectSlot9D, []byte{})
		if _, sw := send(t, c, getDataCmd(ObjectSlot9D, 0)); sw != swNotFound {
			t.Errorf("removed object: SW %04X", sw)
		}
	})
	t.Run("an object seeded on a card that is off", func(t *testing.T) {
		c := New(Options{})
		c.PutObject(ObjectSlot9C, []byte{0x53, 0x00})
		c.PowerOn()
		selectPIV(t, c)
		if data, sw := send(t, c, getDataCmd(ObjectSlot9C, 0)); sw != swOK || !bytes.Equal(data, []byte{0x53, 0x00}) {
			t.Errorf("%x %04X", data, sw)
		}
	})
}

// "a certificate object is 53 L { 70 L <DER>, 71 01 00, FE 00 }", for every length form.
func TestQaACertificateObjectHasTheShapeTheDocGives(t *testing.T) {
	for _, n := range []int{0, 1, 127, 128, 255, 256, 1500, 4464, 65000} {
		der := make([]byte, n)
		for i := range der {
			der[i] = byte(i + 1)
		}
		obj := CertificateObject(der)
		tag, outer, rest, ok := readTLV(obj)
		if !ok || tag != 0x53 || len(rest) != 0 {
			t.Fatalf("n = %d: the object is not one 53: %x...", n, obj[:min(len(obj), 8)])
		}
		tag, cert, rest, ok := readTLV(outer)
		if !ok || tag != 0x70 || !bytes.Equal(cert, der) {
			t.Fatalf("n = %d: no 70 with the DER", n)
		}
		if !bytes.Equal(rest, []byte{0x71, 0x01, 0x00, 0xFE, 0x00}) {
			t.Fatalf("n = %d: the tail is %x, want 71 01 00 FE 00", n, rest)
		}
	}
}

// Nothing the card hands out, and nothing it was handed, shares memory with the caller.
func TestQaNoSliceIsSharedWithTheCaller(t *testing.T) {
	c := newSelected(t, Options{})
	in := []byte{0x53, 0x01, 0xAA}
	c.PutObject(ObjectSlot9A, in)
	in[2] = 0
	data, _ := send(t, c, getDataCmd(ObjectSlot9A, 0))
	if !bytes.Equal(data, []byte{0x53, 0x01, 0xAA}) {
		t.Errorf("PutObject kept the caller's slice: %x", data)
	}
	data[2] = 0
	if again, _ := send(t, c, getDataCmd(ObjectSlot9A, 0)); !bytes.Equal(again, []byte{0x53, 0x01, 0xAA}) {
		t.Errorf("an answer was the card's own slice: %x", again)
	}
	for _, apdu := range [][]byte{{0, insGetVersion, 0, 0}, {0, insGetSerial, 0, 0}, metadataCmd(refPIN)} {
		d, _ := send(t, c, apdu)
		for i := range d {
			d[i] = 0xEE
		}
		again, _ := send(t, c, apdu)
		if bytes.Contains(again, []byte{0xEE}) {
			t.Errorf("the answer to %x is the card's own slice", apdu)
		}
	}
	// the rest of a response is a copy
	obj := bigObject(600)
	c.PutObject(ObjectSlot9C, obj)
	send(t, c, getDataCmd(ObjectSlot9C, 0))
	c.PutObject(ObjectSlot9C, bytes.Repeat([]byte{0xDD}, 600))
	rest, sw := send(t, c, getResponseCmd(0))
	if !bytes.Equal(rest, obj[256:512]) || sw != swMore|0x58 {
		t.Errorf("the rest of the response changed with the object: %04X", sw)
	}
	// the buffer of an APDU is the caller's after Exchange returns, also a part of a chain
	part := short(claChain, insGetData, 0x3F, 0xFF, []byte{0x5C, 0x03}, -1)
	send(t, c, part)
	part[5], part[6] = 0xEE, 0xEE
	data, sw = send(t, c, short(claPlain, insGetData, 0x3F, 0xFF, []byte{0x5F, 0xC1, 0x05}, 0))
	if sw != swOK || !bytes.Equal(data, []byte{0x53, 0x01, 0xAA}) {
		t.Errorf("a chain kept the caller's buffer: %x %04X", data, sw)
	}
	// a new PIN is copied from the buffer of the command
	cmd := changeCmd(refPIN, DefaultPIN, "654321")
	wantSW(t, "CHANGE", qaSWOf(t, c, cmd), swOK)
	for i := range cmd {
		cmd[i] = 0
	}
	wantSW(t, "VERIFY the new PIN", qaSWOf(t, c, verifyCmd("654321")), swOK)
	// the log and the list of the forbidden are copies
	send(t, c, []byte{0, insReset, 0, 0})
	l, f := c.Log(), c.Forbidden()
	l[0].INS, f[0].INS = 0x99, 0x99
	if c.Log()[0].INS == 0x99 || c.Forbidden()[0].INS == 0x99 {
		t.Error("Log or Forbidden returned the card's own slice")
	}
}

// "GET SERIAL: four bytes, big-endian" and "GET VERSION: three bytes", for the edges of the values.
func TestQaSerialAndVersionBytes(t *testing.T) {
	for _, tc := range []struct {
		serial uint32
		want   []byte
	}{{1, []byte{0, 0, 0, 1}}, {0xFF, []byte{0, 0, 0, 0xFF}}, {0x100, []byte{0, 0, 1, 0}}, {0xFFFFFFFF, []byte{0xFF, 0xFF, 0xFF, 0xFF}}, {0x80000000, []byte{0x80, 0, 0, 0}}} {
		c := newSelected(t, Options{Serial: tc.serial})
		if data, _ := send(t, c, []byte{0, insGetSerial, 0, 0}); !bytes.Equal(data, tc.want) {
			t.Errorf("serial %d: %x, want %x", tc.serial, data, tc.want)
		}
	}
	c := newSelected(t, Options{Firmware: [3]byte{255, 255, 255}})
	if data, _ := send(t, c, []byte{0, insGetVersion, 0, 0}); !bytes.Equal(data, []byte{255, 255, 255}) {
		t.Errorf("version %x", data)
	}
}

// "SELECT ... the PIV application, by its 5-byte or its 11-byte AID": and by no other length of it.
func TestQaSelectTakesTheAIDOfFiveBytesOrOfElevenAndNoPrefixBetween(t *testing.T) {
	for n := 0; n <= len(aidFull)+3; n++ {
		data := append(append([]byte(nil), aidFull...), 0, 0, 0)[:n]
		c := New(Options{})
		c.PowerOn()
		sw := qaSWOf(t, c, short(0, insSelect, 4, 0, data, 0))
		want := uint16(swNotFound)
		if n == 5 || n == 11 {
			want = swOK
		}
		if sw != want {
			t.Errorf("SELECT of %d bytes of the AID: %04X, want %04X", n, sw, want)
		}
	}
}

// R8: "6A80 ... for a tag list of GET DATA that is not 5C 03 <id>": a list of any other length, with or without the bytes to match.
func TestQaATagListThatIsNotExactlyFiveCZeroThreeAndAnIdIsRefused(t *testing.T) { // assumed: R8
	c := newSelected(t, Options{})
	c.PutObject(ObjectSlot9A, []byte{0x53, 0x01, 0xAB})
	for name, list := range map[string][]byte{
		"length 0":          {0x5C, 0x00},
		"length 1":          {0x5C, 0x01, 0x5F},
		"length 2":          {0x5C, 0x02, 0x5F, 0xC1},
		"length 4":          {0x5C, 0x04, 0x5F, 0xC1, 0x05, 0x00},
		"length 5":          {0x5C, 0x05, 0x5F, 0xC1, 0x05, 0x00, 0x00},
		"length 3, 2 bytes": {0x5C, 0x03, 0x5F, 0xC1},
		"a tag of 53":       {0x53, 0x03, 0x5F, 0xC1, 0x05},
		"no tag":            {0x03, 0x5F, 0xC1, 0x05},
		"two lists":         {0x5C, 0x03, 0x5F, 0xC1, 0x05, 0x5C, 0x03, 0x5F, 0xC1, 0x05},
		"the id in two":     {0x5C, 0x01, 0x5F, 0x5C, 0x02, 0xC1, 0x05},
	} {
		wantSW(t, name, qaSWOf(t, c, short(0, insGetData, 0x3F, 0xFF, list, 0)), swWrongData)
	}
	wantSW(t, "the right list", qaSWOf(t, c, short(0, insGetData, 0x3F, 0xFF, []byte{0x5C, 0x03, 0x5F, 0xC1, 0x05}, 0)), swOK)
}

// R8, its last sentence: "A VERIFY block whose value, with the padding off, is shorter than 6 bytes (all FF, or "1234" and four FF) is a wrong
// value and spends a try, where a card might refuse it with 6A80 and spend none." In every form of the command, for every length, for a
// verified PIN and a blocked one.
func TestQaAVerifyBlockOfFewerThanSixBytesIsAWrongValueInEveryForm(t *testing.T) { // assumed: R8
	var blocks [][]byte
	for k := 0; k <= 5; k++ {
		blocks = append(blocks, pad(strings.Repeat("1", k)), pad(DefaultPIN[:k])) // "11" and four FF; the start of the PIN itself
	}
	blocks = append(blocks, pad("12\xff\xff"[:2]), []byte("\xff\xff\xff\xff\xff\xff\xff\xff"), []byte("1\xff\xff\xff\xff\xff\xff\xff"))
	for _, block := range blocks {
		for form, apdu := range map[string][]byte{
			"short":    short(0, insVerify, 0, refPIN, block, -1),
			"4S":       short(0, insVerify, 0, refPIN, block, 0),
			"extended": extended(0, insVerify, 0, refPIN, block, -1),
			"4E":       extended(0, insVerify, 0, refPIN, block, 0),
		} {
			c := newSelected(t, Options{})
			wantSW(t, "the right PIN first", qaSWOf(t, c, verifyCmd(DefaultPIN)), swOK)
			wantSW(t, fmt.Sprintf("%s, block %x", form, block), qaSWOf(t, c, apdu), 0x63C2)
			wantSW(t, "the PIN is not verified after it", qaSWOf(t, c, emptyVerify()), 0x63C2)
			wantSW(t, "a second one", qaSWOf(t, c, apdu), 0x63C1)
			wantSW(t, "a third one", qaSWOf(t, c, apdu), 0x63C0)
			wantSW(t, "a fourth: blocked", qaSWOf(t, c, apdu), swBlocked)
			wantSW(t, "the right PIN, blocked", qaSWOf(t, c, verifyCmd(DefaultPIN)), swBlocked)
		}
		c := newSelected(t, Options{})
		wantSW(t, "in two parts", qaSWOf(t, c, short(claChain, insVerify, 0, refPIN, block[:3], -1)), swOK)
		wantSW(t, fmt.Sprintf("in two parts, block %x", block), qaSWOf(t, c, short(claPlain, insVerify, 0, refPIN, block[3:], -1)), 0x63C2)
	}
	// the neighbour: six bytes, with and without the padding, is a value like any other
	c := newSelected(t, Options{})
	wantSW(t, "six wrong bytes", qaSWOf(t, c, verifyCmd("000000")), 0x63C2)
	wantSW(t, "the right PIN of six bytes, padded", qaSWOf(t, c, verifyCmd(DefaultPIN)), swOK)
}

// R23: "A change of the PIN or the PUK to the factory value leaves the default flag at 0: the card keeps a flag, and does not compare the value."
// For a card with a given value, for every form of the command, and a change that fails or is refused leaves the flag as it was.
func TestQaTheDefaultFlagIsKeptNotComputedAndOnlyASuccessfulChangeClearsIt(t *testing.T) { // assumed: R23
	flag := func(c *Card, ref byte) byte {
		t.Helper()
		data, sw := send(t, c, metadataCmd(ref))
		wantSW(t, "metadata", sw, swOK)
		return data[5]
	}
	for _, ref := range []byte{refPIN, refPUK} {
		factory, other := DefaultPIN, "654321"
		if ref == refPUK {
			factory, other = DefaultPUK, "87654321"
		}
		for form, mk := range map[string]func(old, next string) [][]byte{
			"short":    func(o, n string) [][]byte { return [][]byte{changeCmd(ref, o, n)} },
			"extended": func(o, n string) [][]byte { return [][]byte{extended(0, insChange, 0, ref, join(pad(o), pad(n)), -1)} },
			"4S":       func(o, n string) [][]byte { return [][]byte{short(0, insChange, 0, ref, join(pad(o), pad(n)), 0)} },
			"chained": func(o, n string) [][]byte {
				b := join(pad(o), pad(n))
				return [][]byte{short(claChain, insChange, 0, ref, b[:9], -1), short(claPlain, insChange, 0, ref, b[9:], -1)}
			},
		} {
			c := newSelected(t, Options{})
			if flag(c, ref) != 1 {
				t.Fatalf("ref %02X: a factory card does not say it is the default", ref)
			}
			// a change that fails, or is refused, leaves the flag at 1
			for _, apdu := range mk("000000", other) { // a wrong old value
				send(t, c, apdu)
			}
			for _, apdu := range mk(factory, "123") { // a new value that is too short
				send(t, c, apdu)
			}
			if got := flag(c, ref); got != 1 {
				t.Errorf("ref %02X, %s: a change that failed cleared the flag", ref, form)
			}
			// a change to the factory value itself, the first one
			var sw uint16
			for _, apdu := range mk(factory, factory) {
				_, sw = send(t, c, apdu)
			}
			wantSW(t, fmt.Sprintf("ref %02X, %s: a change to the same value", ref, form), sw, swOK)
			if got := flag(c, ref); got != 0 {
				t.Errorf("ref %02X, %s: a change to the factory value left the flag at %d", ref, form, got)
			}
			// and a change away and back, and a wrong value afterwards, leave it at 0
			for _, step := range [][2]string{{factory, other}, {other, factory}} {
				for _, apdu := range mk(step[0], step[1]) {
					_, sw = send(t, c, apdu)
				}
				wantSW(t, fmt.Sprintf("ref %02X, %s: %s to %s", ref, form, step[0], step[1]), sw, swOK)
				if got := flag(c, ref); got != 0 {
					t.Errorf("ref %02X, %s: the flag is %d after a change to %s", ref, form, got, step[1])
				}
			}
		}
	}
	// A card made with the factory value as a given value says it is not the default, and a change keeps it so.
	c := newSelected(t, Options{PIN: DefaultPIN, PUK: DefaultPUK})
	if flag(c, refPIN) != 0 || flag(c, refPUK) != 0 {
		t.Error("a given value, even the factory one, says it is the default")
	}
	// The other reference is not touched by a change.
	c = newSelected(t, Options{})
	send(t, c, changeCmd(refPIN, DefaultPIN, "654321"))
	if flag(c, refPIN) != 0 || flag(c, refPUK) != 1 {
		t.Errorf("a change of the PIN: the flags are %d and %d", flag(c, refPIN), flag(c, refPUK))
	}
}

// R24: "An Le shorter than the answer is cut and followed by 61xx, never 6Cxx." For every command that answers with data, every Le from 1 to
// one less than the answer, in the short and the extended form, in both modes, and for GET RESPONSE; the parts read back make the answer.
func TestQaAnLeShorterThanTheAnswerIsCutWith61xxEverywhereAndNeverWith6Cxx(t *testing.T) { // assumed: R24
	obj := qaPattern(70)
	id := []byte{0x5C, 3, 0x5F, 0xC1, 5}
	for _, mode := range []ResponseMode{Extended, Short61xx} {
		for name, tc := range map[string]struct {
			apdu func(le int, ext bool) []byte
			want []byte
		}{
			"GET VERSION":      {func(le int, ext bool) []byte { return qaLeForm(insGetVersion, 0, 0, nil, le, ext) }, []byte{5, 8, 0}},
			"GET SERIAL":       {func(le int, ext bool) []byte { return qaLeForm(insGetSerial, 0, 0, nil, le, ext) }, []byte{0, 0x74, 0xCB, 0xB1}},
			"SELECT":           {func(le int, ext bool) []byte { return qaLeForm(insSelect, 4, 0, aidShort, le, ext) }, selectResponse},
			"GET METADATA PIN": {func(le int, ext bool) []byte { return qaLeForm(insGetMetadata, 0, refPIN, nil, le, ext) }, []byte{1, 1, 0xFF, 5, 1, 1, 6, 2, 3, 3}},
			"GET METADATA PUK": {func(le int, ext bool) []byte { return qaLeForm(insGetMetadata, 0, refPUK, nil, le, ext) }, []byte{1, 1, 0xFF, 5, 1, 1, 6, 2, 3, 3}},
			"GET DATA":         {func(le int, ext bool) []byte { return qaLeForm(insGetData, 0x3F, 0xFF, id, le, ext) }, obj},
		} {
			for _, ext := range []bool{false, true} {
				for le := 1; le < len(tc.want); le++ {
					c := newSelected(t, Options{ResponseMode: mode})
					c.PutObject(ObjectSlot9A, obj)
					data, sw := send(t, c, tc.apdu(le, ext))
					left := len(tc.want) - le
					if sw>>8 == 0x6C || sw != swMore|qaLow(left) || !bytes.Equal(data, tc.want[:le]) {
						t.Fatalf("%s, Le %d, extended %v, mode %d: %x and %04X, want %d bytes and 61%02X", name, le, ext, mode, data, sw, le, left)
					}
					rest, sw := send(t, c, getResponseCmd(0))
					if sw != swOK || !bytes.Equal(rest, tc.want[le:]) {
						t.Fatalf("%s, Le %d: the rest is %x %04X", name, le, rest, sw)
					}
				}
			}
		}
	}
	// GET RESPONSE with a short Le: cut again, with 61xx, never 6Cxx
	c := newSelected(t, Options{})
	c.PutObject(ObjectSlot9A, obj)
	send(t, c, qaLeForm(insGetData, 0x3F, 0xFF, id, 10, false))
	for le, left := 5, 60; left > 0; left -= le {
		data, sw := send(t, c, getResponseCmd(le))
		want := swMore | qaLow(left-le)
		if left <= le {
			want, le = swOK, left
		}
		if sw != want || len(data) != min(le, left) {
			t.Fatalf("GET RESPONSE Le %d with %d left: %d bytes and %04X, want %04X", le, left, len(data), sw, want)
		}
	}
}

// qaLeForm is a command with an Le, in the short or the extended form.
func qaLeForm(ins, p1, p2 byte, data []byte, le int, ext bool) []byte {
	if ext {
		return extended(0, ins, p1, p2, data, le)
	}
	return short(0, ins, p1, p2, data, le)
}
