package pivtest

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestSelectAnsweredAndOtherAIDsNotFound(t *testing.T) {
	wantData := []byte{0x61, 0x11, 0x4F, 0x06, 0x00, 0x00, 0x10, 0x00, 0x01, 0x00, 0x79, 0x07, 0x4F, 0x05, 0xA0, 0x00, 0x00, 0x03, 0x08}
	for _, tc := range []struct {
		name string
		aid  []byte
		want uint16
	}{
		{"five-byte AID", aidShort, swOK},
		{"full AID", aidFull, swOK},
		{"another application", []byte{0xA0, 0x00, 0x00, 0x03, 0x08, 0x00, 0x00, 0x20}, swNotFound},
		{"a prefix of the prefix", aidShort[:4], swNotFound},
		{"OpenPGP", []byte{0xD2, 0x76, 0x00, 0x01, 0x24, 0x01}, swNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := New(Options{})
			c.PowerOn()
			data, sw := send(t, c, short(0, insSelect, 0x04, 0x00, tc.aid, 0))
			wantSW(t, "SELECT", sw, tc.want)
			if tc.want == swOK && !bytes.Equal(data, wantData) {
				t.Errorf("data = %x, want %x", data, wantData)
			}
			if tc.want != swOK && len(data) != 0 {
				t.Errorf("data = %x, want none", data)
			}
		})
	}
	t.Run("the response is a well-formed template", func(t *testing.T) {
		tag, v, rest, ok := readTLV(selectResponse)
		if !ok || tag != 0x61 || len(rest) != 0 || len(v) != 0x11 {
			t.Fatalf("template = %x, tag %x ok %v", selectResponse, tag, ok)
		}
	})
	t.Run("a failed SELECT changes nothing", func(t *testing.T) { // assumed: R2
		c := newSelected(t, Options{})
		send(t, c, verifyCmd(DefaultPIN))
		_, sw := send(t, c, short(0, insSelect, 0x04, 0x00, []byte{1, 2, 3, 4, 5}, 0))
		wantSW(t, "SELECT of another AID", sw, swNotFound)
		_, sw = send(t, c, emptyVerify())
		wantSW(t, "empty VERIFY", sw, swOK)
	})
	t.Run("P1 and P2", func(t *testing.T) { // assumed: R8
		c := New(Options{})
		c.PowerOn()
		_, sw := send(t, c, short(0, insSelect, 0x00, 0x00, aidShort, 0))
		wantSW(t, "P1 00", sw, swWrongP1P2)
		_, sw = send(t, c, short(0, insSelect, 0x04, 0x0C, aidShort, 0))
		wantSW(t, "P2 0C", sw, swWrongP1P2)
	})
}

func TestSelectClearsPINVerified(t *testing.T) { // assumed: R2
	c := newSelected(t, Options{})
	_, sw := send(t, c, verifyCmd(DefaultPIN))
	wantSW(t, "VERIFY", sw, swOK)
	selectPIV(t, c)
	_, sw = send(t, c, emptyVerify())
	wantSW(t, "empty VERIFY after SELECT", sw, 0x63C3)
}

func TestCommandsBeforeSelectAreRefused(t *testing.T) { // assumed: R7
	c := New(Options{})
	c.PowerOn()
	for name, apdu := range map[string][]byte{
		"GET VERSION":  {0, insGetVersion, 0, 0},
		"GET SERIAL":   {0, insGetSerial, 0, 0},
		"VERIFY":       verifyCmd(DefaultPIN),
		"empty VERIFY": emptyVerify(),
		"CHANGE":       changeCmd(refPIN, DefaultPIN, "654321"),
		"GET METADATA": metadataCmd(refPIN),
		"GET DATA":     getDataCmd(ObjectSlot9A, 0),
		"GET RESPONSE": getResponseCmd(0),
		"chained part": short(claChain, insGetData, 0x3F, 0xFF, []byte{0x5C, 0x03}, -1),
		"unknown INS":  {0, 0x99, 0, 0},
	} {
		data, sw := send(t, c, apdu)
		wantSW(t, name, sw, swNotAllowed)
		if len(data) != 0 {
			t.Errorf("%s: data %x", name, data)
		}
	}
	if _, sw := send(t, c, short(0, insSelect, 0x04, 0, aidShort, 0)); sw != swOK {
		t.Fatalf("SELECT after the refusals: %04X", sw)
	}
	if _, sw := send(t, c, []byte{0, insGetVersion, 0, 0}); sw != swOK {
		t.Errorf("GET VERSION after SELECT: %04X", sw)
	}
}

func TestVersionAndSerialBytes(t *testing.T) {
	c := newSelected(t, Options{})
	data, sw := send(t, c, []byte{0, insGetVersion, 0, 0})
	wantSW(t, "GET VERSION", sw, swOK)
	if !bytes.Equal(data, []byte{5, 8, 0}) {
		t.Errorf("version = %x, want 050800", data)
	}
	data, sw = send(t, c, []byte{0, insGetSerial, 0, 0})
	wantSW(t, "GET SERIAL", sw, swOK)
	if !bytes.Equal(data, []byte{0x00, 0x74, 0xCB, 0xB1}) { // 7654321
		t.Errorf("serial = %x, want 0074cbb1 (%d)", data, DefaultSerial)
	}
	c = newSelected(t, Options{Firmware: [3]byte{5, 7, 2}, Serial: 0x01020304})
	data, _ = send(t, c, []byte{0, insGetVersion, 0, 0})
	if !bytes.Equal(data, []byte{5, 7, 2}) {
		t.Errorf("version = %x, want 050702", data)
	}
	data, _ = send(t, c, []byte{0, insGetSerial, 0, 0})
	if !bytes.Equal(data, []byte{1, 2, 3, 4}) {
		t.Errorf("serial = %x, want 01020304", data)
	}
	t.Run("with Le", func(t *testing.T) {
		data, sw := send(t, c, short(0, insGetVersion, 0, 0, nil, 0))
		wantSW(t, "GET VERSION", sw, swOK)
		if len(data) != 3 {
			t.Errorf("data = %x", data)
		}
	})
}

func TestEmptyVerifyReadsTheCounterAndSpendsNoTry(t *testing.T) {
	c := newSelected(t, Options{})
	for i := 0; i < 5; i++ {
		data, sw := send(t, c, emptyVerify())
		wantSW(t, "empty VERIFY", sw, 0x63C3)
		if len(data) != 0 {
			t.Errorf("data = %x", data)
		}
	}
	if c.st().pin.left != 3 {
		t.Errorf("tries left = %d, want 3", c.st().pin.left)
	}
	_, sw := send(t, c, short(0, insVerify, 0, refPIN, nil, 0)) // the same with an Le
	wantSW(t, "empty VERIFY with Le", sw, 0x63C3)
	_, sw = send(t, c, extended(0, insVerify, 0, refPIN, nil, 0))
	wantSW(t, "empty VERIFY, extended", sw, 0x63C3)
}

func TestWrongPINCountsDownAs63Cx(t *testing.T) {
	c := newSelected(t, Options{})
	for _, want := range []uint16{0x63C2, 0x63C1, 0x63C0} {
		_, sw := send(t, c, verifyCmd("000000"))
		wantSW(t, "wrong PIN", sw, want)
	}
	if c.st().verified {
		t.Error("verified after a wrong PIN")
	}
	t.Run("a PIN of another length", func(t *testing.T) {
		c := newSelected(t, Options{})
		_, sw := send(t, c, verifyCmd("1234567")) // 7 bytes: padded, but not the PIN
		wantSW(t, "7-byte PIN", sw, 0x63C2)
	})
	t.Run("a wrong PIN leaves the PIN not verified", func(t *testing.T) { // assumed: R20
		c := newSelected(t, Options{})
		send(t, c, verifyCmd(DefaultPIN))
		send(t, c, verifyCmd("000000"))
		_, sw := send(t, c, emptyVerify())
		wantSW(t, "empty VERIFY", sw, 0x63C2)
	})
	t.Run("a larger maximum", func(t *testing.T) {
		c := newSelected(t, Options{MaxPINTries: 10})
		_, sw := send(t, c, verifyCmd("000000"))
		wantSW(t, "wrong PIN", sw, 0x63C9)
	})
}

func TestBlockedPINAnswers6983EvenForTheRightPIN(t *testing.T) {
	c := newSelected(t, Options{})
	for i := 0; i < 3; i++ {
		send(t, c, verifyCmd("000000"))
	}
	_, sw := send(t, c, verifyCmd(DefaultPIN))
	wantSW(t, "right PIN", sw, swBlocked)
	_, sw = send(t, c, verifyCmd("000000"))
	wantSW(t, "wrong PIN", sw, swBlocked)
	_, sw = send(t, c, emptyVerify())
	wantSW(t, "empty VERIFY reads the counter", sw, 0x63C0)
	_, sw = send(t, c, changeCmd(refPIN, DefaultPIN, "654321"))
	wantSW(t, "CHANGE of a blocked PIN", sw, swBlocked)
	if c.st().pin.left != 0 {
		t.Errorf("tries left = %d", c.st().pin.left)
	}
}

func TestRightPINRestoresTheCounter(t *testing.T) {
	c := newSelected(t, Options{})
	send(t, c, verifyCmd("000000"))
	send(t, c, verifyCmd("000000"))
	_, sw := send(t, c, emptyVerify())
	wantSW(t, "after two wrong", sw, 0x63C1)
	_, sw = send(t, c, verifyCmd(DefaultPIN))
	wantSW(t, "right PIN", sw, swOK)
	if c.st().pin.left != c.st().pin.max {
		t.Errorf("tries left = %d, want %d", c.st().pin.left, c.st().pin.max)
	}
}

func TestEmptyVerifyAfterVerifyAnswers9000(t *testing.T) {
	c := newSelected(t, Options{})
	_, sw := send(t, c, verifyCmd(DefaultPIN))
	wantSW(t, "VERIFY", sw, swOK)
	for i := 0; i < 2; i++ {
		_, sw = send(t, c, emptyVerify())
		wantSW(t, "empty VERIFY", sw, swOK)
	}
}

func TestChangePINClearsTheDefaultFlagAndRestoresTheCounter(t *testing.T) {
	c := newSelected(t, Options{})
	data, _ := send(t, c, metadataCmd(refPIN))
	if !bytes.Contains(data, []byte{0x05, 0x01, 0x01}) {
		t.Fatalf("metadata before = %x, want the default flag 1", data)
	}
	send(t, c, verifyCmd("000000"))
	_, sw := send(t, c, changeCmd(refPIN, DefaultPIN, "6543210"))
	wantSW(t, "CHANGE", sw, swOK)
	data, _ = send(t, c, metadataCmd(refPIN))
	if want := []byte{0x01, 0x01, 0xFF, 0x05, 0x01, 0x00, 0x06, 0x02, 0x03, 0x03}; !bytes.Equal(data, want) {
		t.Errorf("metadata after = %x, want %x", data, want)
	}
	_, sw = send(t, c, verifyCmd(DefaultPIN))
	wantSW(t, "old PIN", sw, 0x63C2)
	_, sw = send(t, c, verifyCmd("6543210"))
	wantSW(t, "new PIN", sw, swOK)
	t.Run("a wrong old value spends a try", func(t *testing.T) {
		c := newSelected(t, Options{})
		_, sw := send(t, c, changeCmd(refPIN, "000000", "654321"))
		wantSW(t, "CHANGE", sw, 0x63C2)
		_, sw = send(t, c, verifyCmd(DefaultPIN))
		wantSW(t, "the PIN is unchanged", sw, swOK)
	})
	t.Run("a new value outside 6 to 8 bytes", func(t *testing.T) { // assumed: R8
		for _, next := range []string{"12345", "", "1"} {
			c := newSelected(t, Options{})
			_, sw := send(t, c, changeCmd(refPIN, DefaultPIN, next))
			wantSW(t, "CHANGE to "+next, sw, swWrongData)
			if c.st().pin.left != 3 || !c.st().pin.isDefault {
				t.Errorf("a refused change spent a try or cleared the flag")
			}
		}
	})
}

func TestPUKCounterIsIndependent(t *testing.T) {
	c := newSelected(t, Options{})
	_, sw := send(t, c, changeCmd(refPUK, "00000000", "87654321"))
	wantSW(t, "wrong PUK", sw, 0x63C2)
	_, sw = send(t, c, changeCmd(refPUK, "00000000", "87654321"))
	wantSW(t, "wrong PUK again", sw, 0x63C1)
	if c.st().pin.left != 3 {
		t.Errorf("a wrong PUK spent a PIN try: %d left", c.st().pin.left)
	}
	send(t, c, verifyCmd("000000"))
	if c.st().puk.left != 1 {
		t.Errorf("a wrong PIN spent a PUK try: %d left", c.st().puk.left)
	}
	_, sw = send(t, c, changeCmd(refPUK, DefaultPUK, "87654321"))
	wantSW(t, "right PUK", sw, swOK)
	if c.st().puk.left != 3 || c.st().puk.isDefault || c.st().pin.left != 2 || !c.st().pin.isDefault {
		t.Errorf("after the PUK change: puk %+v pin %+v", c.st().puk, c.st().pin)
	}
	for i := 0; i < 3; i++ {
		send(t, c, changeCmd(refPUK, "00000000", "87654321"))
	}
	_, sw = send(t, c, changeCmd(refPUK, "87654321", "11111111"))
	wantSW(t, "blocked PUK", sw, swBlocked)
	_, sw = send(t, c, verifyCmd(DefaultPIN))
	wantSW(t, "the PIN still works", sw, swOK)
}

func TestMetadataOfPINAndPUK(t *testing.T) {
	c := newSelected(t, Options{})
	for _, tc := range []struct {
		name string
		ref  byte
		want []byte
	}{
		{"PIN", refPIN, []byte{0x01, 0x01, 0xFF, 0x05, 0x01, 0x01, 0x06, 0x02, 0x03, 0x03}},
		{"PUK", refPUK, []byte{0x01, 0x01, 0xFF, 0x05, 0x01, 0x01, 0x06, 0x02, 0x03, 0x03}}, // assumed: R14 (the tag 01)
	} {
		data, sw := send(t, c, metadataCmd(tc.ref))
		wantSW(t, tc.name, sw, swOK)
		if !bytes.Equal(data, tc.want) {
			t.Errorf("%s metadata = %x, want %x", tc.name, data, tc.want)
		}
	}
	send(t, c, verifyCmd("000000"))
	data, _ := send(t, c, metadataCmd(refPIN))
	if want := []byte{0x01, 0x01, 0xFF, 0x05, 0x01, 0x01, 0x06, 0x02, 0x03, 0x02}; !bytes.Equal(data, want) { // assumed: R14
		t.Errorf("after a wrong PIN = %x, want %x", data, want)
	}
	for _, ref := range []byte{0x00, 0x9A, 0x9B, 0x9C, 0x82, 0xF9} {
		_, sw := send(t, c, metadataCmd(ref))
		wantSW(t, "reference", sw, swRefNotFound)
	}
	t.Run("options", func(t *testing.T) {
		c := newSelected(t, Options{PIN: "7654321", MaxPINTries: 5, MaxPUKTries: 4})
		data, _ := send(t, c, metadataCmd(refPIN))
		if want := []byte{0x01, 0x01, 0xFF, 0x05, 0x01, 0x00, 0x06, 0x02, 0x05, 0x05}; !bytes.Equal(data, want) { // assumed: R14
			t.Errorf("PIN = %x, want %x", data, want)
		}
		data, _ = send(t, c, metadataCmd(refPUK))
		if want := []byte{0x01, 0x01, 0xFF, 0x05, 0x01, 0x01, 0x06, 0x02, 0x04, 0x04}; !bytes.Equal(data, want) { // assumed: R14
			t.Errorf("PUK = %x, want %x", data, want)
		}
	})
}

func TestGetDataOfAnEmptyObjectIs6A82(t *testing.T) {
	c := newSelected(t, Options{})
	for _, id := range []uint32{ObjectSlot9A, ObjectSlot9C, ObjectSlot9D, ObjectSlot9E, ObjectRetired1, ObjectRetired1 + 19, ObjectAttestation} {
		data, sw := send(t, c, getDataCmd(id, 0))
		wantSW(t, "unseeded object", sw, swNotFound)
		if len(data) != 0 {
			t.Errorf("data = %x", data)
		}
	}
	der := []byte{0x30, 0x03, 0x02, 0x01, 0x07}
	obj := CertificateObject(der)
	if want := []byte{0x53, 0x0C, 0x70, 0x05, 0x30, 0x03, 0x02, 0x01, 0x07, 0x71, 0x01, 0x00, 0xFE, 0x00}; !bytes.Equal(obj, want) {
		t.Fatalf("CertificateObject = %x, want %x", obj, want)
	}
	c.PutObject(ObjectSlot9A, obj)
	data, sw := send(t, c, getDataCmd(ObjectSlot9A, 0))
	wantSW(t, "seeded object", sw, swOK)
	if !bytes.Equal(data, obj) {
		t.Errorf("object = %x, want %x", data, obj)
	}
	_, sw = send(t, c, getDataCmd(ObjectSlot9C, 0))
	wantSW(t, "the neighbour", sw, swNotFound)
	c.PutObject(ObjectSlot9A, nil)
	_, sw = send(t, c, getDataCmd(ObjectSlot9A, 0))
	wantSW(t, "removed object", sw, swNotFound)
	t.Run("a tag list that is not one id", func(t *testing.T) { // assumed: R8
		for name, data := range map[string][]byte{
			"wrong tag": {0x5D, 0x03, 0x5F, 0xC1, 0x05},
			"two bytes": {0x5C, 0x02, 0x5F, 0xC1},
			"trailing":  {0x5C, 0x03, 0x5F, 0xC1, 0x05, 0x00},
			"empty":     nil,
		} {
			_, sw := send(t, c, short(0, insGetData, 0x3F, 0xFF, data, 0))
			wantSW(t, name, sw, swWrongData)
		}
		_, sw := send(t, c, short(0, insGetData, 0x3F, 0x00, []byte{0x5C, 0x03, 0x5F, 0xC1, 0x05}, 0))
		wantSW(t, "P2", sw, swWrongP1P2)
	})
}

func TestAResponseLongerThanLeChainsWith61xxAndGetResponse(t *testing.T) {
	obj := bigObject(600)
	for _, mode := range []ResponseMode{Extended, Short61xx} {
		c := newSelected(t, Options{ResponseMode: mode})
		c.PutObject(ObjectSlot9A, obj)
		first, sw := send(t, c, getDataCmd(ObjectSlot9A, 0))
		wantSW(t, "first", sw, swMore) // 344 left: 256 or more is 00
		second, sw := send(t, c, getResponseCmd(0))
		wantSW(t, "second", sw, swMore|0x58) // 88 left
		third, sw := send(t, c, getResponseCmd(0x58))
		wantSW(t, "third", sw, swOK)
		if len(first) != 256 || len(second) != 256 || len(third) != 88 {
			t.Fatalf("part sizes %d %d %d, want 256 256 88", len(first), len(second), len(third))
		}
		if got := join(first, second, third); !bytes.Equal(got, obj) {
			t.Errorf("the parts do not join to the object")
		}
		_, sw = send(t, c, getResponseCmd(0))
		wantSW(t, "after the last part", sw, swWrongP1P2) // assumed: R10
	}
	t.Run("an Le that cuts a short answer", func(t *testing.T) {
		c := newSelected(t, Options{})
		c.PutObject(ObjectSlot9A, obj[:100])
		first, sw := send(t, c, getDataCmd(ObjectSlot9A, 40))
		wantSW(t, "Le 40", sw, swMore|60)
		rest, sw := send(t, c, getResponseCmd(60))
		wantSW(t, "rest", sw, swOK)
		if len(first) != 40 || len(rest) != 60 {
			t.Errorf("sizes %d %d", len(first), len(rest))
		}
	})
	t.Run("an answer that fits", func(t *testing.T) {
		c := newSelected(t, Options{})
		c.PutObject(ObjectSlot9A, obj[:256])
		data, sw := send(t, c, getDataCmd(ObjectSlot9A, 0))
		wantSW(t, "256 bytes at Le 00", sw, swOK)
		if len(data) != 256 {
			t.Errorf("len = %d", len(data))
		}
	})
	t.Run("a command with no Le is read as the largest Le", func(t *testing.T) { // assumed: R22
		c := newSelected(t, Options{})
		c.PutObject(ObjectSlot9A, obj[:300])
		data, sw := send(t, c, getDataCmd(ObjectSlot9A, -1))
		wantSW(t, "no Le", sw, swMore|44)
		if len(data) != 256 {
			t.Errorf("len = %d", len(data))
		}
	})
}

func TestExtendedAPDUGivesTheWholeResponseInExtendedMode(t *testing.T) {
	obj := bigObject(3000)
	c := newSelected(t, Options{})
	c.PutObject(ObjectSlot9A, obj)
	id := []byte{0x5C, 0x03, 0x5F, 0xC1, 0x05}
	data, sw := send(t, c, extended(0, insGetData, 0x3F, 0xFF, id, 0))
	wantSW(t, "extended, Le 0000", sw, swOK)
	if !bytes.Equal(data, obj) {
		t.Errorf("got %d bytes, want %d", len(data), len(obj))
	}
	data, sw = send(t, c, extended(0, insGetData, 0x3F, 0xFF, id, 1000))
	wantSW(t, "extended, Le 1000", sw, swMore)
	if len(data) != 1000 {
		t.Errorf("len = %d, want 1000", len(data))
	}
	data, sw = send(t, c, extended(0, insGetData, 0x3F, 0xFF, id, -1)) // assumed: R22
	wantSW(t, "extended, no Le", sw, swOK)
	if len(data) != len(obj) {
		t.Errorf("len = %d", len(data))
	}
	// A short command to the same card still chains.
	data, sw = send(t, c, getDataCmd(ObjectSlot9A, 0))
	wantSW(t, "short", sw, swMore)
	if len(data) != 256 {
		t.Errorf("len = %d", len(data))
	}
	t.Run("Short61xx cuts an extended command at 256", func(t *testing.T) {
		c := newSelected(t, Options{ResponseMode: Short61xx})
		c.PutObject(ObjectSlot9A, obj)
		data, sw := send(t, c, extended(0, insGetData, 0x3F, 0xFF, id, 0))
		wantSW(t, "extended in Short61xx", sw, swMore)
		if len(data) != 256 {
			t.Errorf("len = %d", len(data))
		}
	})
	t.Run("an extended APDU with no answer", func(t *testing.T) {
		data, sw := send(t, c, extended(0, insGetVersion, 0, 0, nil, 0))
		wantSW(t, "GET VERSION, extended", sw, swOK)
		if !bytes.Equal(data, []byte{5, 8, 0}) {
			t.Errorf("data = %x", data)
		}
	})
}

func TestExtendedAPDURefusedWhenTheOptionIsOff(t *testing.T) {
	c := newSelected(t, Options{NoExtendedAPDU: true})
	c.PutObject(ObjectSlot9A, bigObject(300))
	id := []byte{0x5C, 0x03, 0x5F, 0xC1, 0x05}
	for name, apdu := range map[string][]byte{
		"with data": extended(0, insGetData, 0x3F, 0xFF, id, 0),
		"Le only":   extended(0, insGetVersion, 0, 0, nil, 0),
	} {
		data, sw := send(t, c, apdu)
		wantSW(t, name, sw, swWrongLength)
		if len(data) != 0 {
			t.Errorf("%s: data %x", name, data)
		}
	}
	_, sw := send(t, c, getDataCmd(ObjectSlot9A, 0))
	wantSW(t, "a short command still works", sw, swMore|44)
}

func TestCommandChainingWithCLA10AssemblesTheData(t *testing.T) {
	c := newSelected(t, Options{})
	c.PutObject(ObjectSlot9A, []byte{0x53, 0x01, 0xAB})
	first := short(claChain, insGetData, 0x3F, 0xFF, []byte{0x5C, 0x03}, -1)
	second := short(claChain, insGetData, 0x3F, 0xFF, []byte{0x5F}, -1)
	last := short(claPlain, insGetData, 0x3F, 0xFF, []byte{0xC1, 0x05}, 0)
	for i, part := range [][]byte{first, second} {
		data, sw := send(t, c, part)
		wantSW(t, "chained part", sw, swOK)
		if len(data) != 0 {
			t.Errorf("part %d answered data %x", i, data)
		}
	}
	data, sw := send(t, c, last)
	wantSW(t, "last part", sw, swOK)
	if !bytes.Equal(data, []byte{0x53, 0x01, 0xAB}) {
		t.Errorf("data = %x", data)
	}
	// The chain is used up: the same last part alone is a malformed tag list.
	_, sw = send(t, c, last)
	wantSW(t, "last part alone", sw, swWrongData)
	log := c.Log()
	if len(log) != 5 || log[1].CLA != claChain || log[1].DataLen != 2 || log[3].DataLen != 2 {
		t.Errorf("log = %+v", log)
	}
	t.Run("a part with another header drops the chain", func(t *testing.T) { // assumed: R21
		c := newSelected(t, Options{})
		c.PutObject(ObjectSlot9A, []byte{0x53, 0x01, 0xAB})
		send(t, c, first)
		_, sw := send(t, c, short(claChain, insGetData, 0x3F, 0x00, []byte{0x5F}, -1))
		wantSW(t, "other P2", sw, swWrongLength)
		_, sw = send(t, c, last)
		wantSW(t, "last part after the drop", sw, swWrongData)
	})
	t.Run("a last part with another header runs alone", func(t *testing.T) { // assumed: R21
		c := newSelected(t, Options{})
		send(t, c, first)
		data, sw := send(t, c, []byte{0, insGetVersion, 0, 0})
		wantSW(t, "GET VERSION", sw, swOK)
		if !bytes.Equal(data, []byte{5, 8, 0}) {
			t.Errorf("data = %x", data)
		}
		_, sw = send(t, c, last)
		wantSW(t, "last", sw, swWrongData)
	})
	t.Run("too much data", func(t *testing.T) { // assumed: R21
		c := newSelected(t, Options{})
		part := extended(claChain, insGetData, 0x3F, 0xFF, make([]byte, 40000), -1)
		_, sw := send(t, c, part)
		wantSW(t, "first 40000", sw, swOK)
		_, sw = send(t, c, part)
		wantSW(t, "second 40000", sw, swWrongLength)
	})
	t.Run("a PIN in two parts", func(t *testing.T) {
		c := newSelected(t, Options{})
		pin := pad(DefaultPIN)
		send(t, c, short(claChain, insVerify, 0, refPIN, pin[:3], -1))
		_, sw := send(t, c, short(claPlain, insVerify, 0, refPIN, pin[3:], -1))
		wantSW(t, "VERIFY", sw, swOK)
	})
}

func TestANewCommandDropsAPendingResponse(t *testing.T) { // assumed: R10
	c := newSelected(t, Options{})
	c.PutObject(ObjectSlot9A, bigObject(600))
	_, sw := send(t, c, getResponseCmd(0))
	wantSW(t, "GET RESPONSE with nothing waiting", sw, swWrongP1P2)
	_, sw = send(t, c, getDataCmd(ObjectSlot9A, 0))
	wantSW(t, "GET DATA", sw, swMore)
	_, sw = send(t, c, []byte{0, insGetVersion, 0, 0})
	wantSW(t, "GET VERSION", sw, swOK)
	_, sw = send(t, c, getResponseCmd(0))
	wantSW(t, "GET RESPONSE after another command", sw, swWrongP1P2)
	// The same for a command that is refused, and for a malformed one.
	for name, apdu := range map[string][]byte{"refused": {0, 0x99, 0, 0}, "malformed": {0, 1}} {
		send(t, c, getDataCmd(ObjectSlot9A, 0))
		send(t, c, apdu)
		_, sw = send(t, c, getResponseCmd(0))
		wantSW(t, "GET RESPONSE after a "+name+" command", sw, swWrongP1P2)
	}
	// Two GET RESPONSEs in a row keep the rest.
	send(t, c, getDataCmd(ObjectSlot9A, 0))
	_, sw = send(t, c, getResponseCmd(100))
	wantSW(t, "GET RESPONSE Le 100", sw, swMore|0xF4)
}

func TestPowerCycleClearsTheSessionAndKeepsTheCardState(t *testing.T) {
	c := newSelected(t, Options{ResponseMode: Short61xx})
	obj := bigObject(600)
	c.PutObject(ObjectSlot9A, obj)
	send(t, c, changeCmd(refPIN, DefaultPIN, "654321"))
	send(t, c, verifyCmd("654321"))
	send(t, c, verifyCmd("000000")) // 2 left, and not verified
	send(t, c, getDataCmd(ObjectSlot9A, 0))
	before := c.Log()
	c.PowerOff()
	if resp, _ := c.Exchange([]byte{0, insGetVersion, 0, 0}); resp != nil {
		t.Errorf("a card that is off answered %x", resp)
	}
	atr := c.PowerOn()
	if len(atr) < 4 || atr[0] != 0x3B {
		t.Errorf("answer to reset = %x", atr)
	}
	var x byte
	for _, b := range atr[1:] {
		x ^= b
	}
	if x != 0 {
		t.Errorf("the check byte of %x is wrong", atr)
	}
	_, sw := send(t, c, emptyVerify())
	wantSW(t, "before SELECT", sw, swNotAllowed)
	selectPIV(t, c)
	_, sw = send(t, c, getResponseCmd(0))
	wantSW(t, "the pending response is gone", sw, swWrongP1P2)
	_, sw = send(t, c, emptyVerify())
	wantSW(t, "the counter is kept", sw, 0x63C2)
	data, sw := send(t, c, getDataCmd(ObjectSlot9A, 0))
	wantSW(t, "the object is kept", sw, swMore)
	if !bytes.Equal(data, obj[:256]) {
		t.Errorf("object differs")
	}
	_, sw = send(t, c, verifyCmd("654321"))
	wantSW(t, "the changed PIN is kept", sw, swOK)
	if after := c.Log(); len(after) <= len(before) || !reflect.DeepEqual(after[:len(before)], before) {
		t.Errorf("a power cycle did not keep the log: %d entries before, %d after", len(before), len(after))
	}
	data, _ = send(t, c, []byte{0, insGetVersion, 0, 0})
	if !bytes.Equal(data, []byte{5, 8, 0}) {
		t.Errorf("version = %x", data)
	}

	t.Run("the verified PIN", func(t *testing.T) {
		c := newSelected(t, Options{})
		send(t, c, verifyCmd(DefaultPIN))
		c.PowerOff()
		c.PowerOn()
		selectPIV(t, c)
		_, sw := send(t, c, emptyVerify())
		wantSW(t, "after the power cycle", sw, 0x63C3)
	})
	t.Run("the chain", func(t *testing.T) {
		c := newSelected(t, Options{})
		c.PutObject(ObjectSlot9A, []byte{0x53, 0x01, 0xAB})
		send(t, c, short(claChain, insGetData, 0x3F, 0xFF, []byte{0x5C, 0x03}, -1))
		c.PowerOff()
		c.PowerOn()
		selectPIV(t, c)
		_, sw := send(t, c, short(claPlain, insGetData, 0x3F, 0xFF, []byte{0x5F, 0xC1, 0x05}, 0))
		wantSW(t, "the last part alone", sw, swWrongData)
	})
	t.Run("PowerOn twice is a new session", func(t *testing.T) {
		c := newSelected(t, Options{})
		c.PowerOn()
		_, sw := send(t, c, emptyVerify())
		wantSW(t, "after PowerOn", sw, swNotAllowed)
	})
}

func TestLogNeverHoldsAPINOrAnObjectBody(t *testing.T) {
	c := newSelected(t, Options{})
	body := []byte{0x53, 0x08, 0xDE, 0xAD, 0xBE, 0xEF, 0xCA, 0xFE, 0xF0, 0x0D}
	c.PutObject(ObjectSlot9A, body)
	send(t, c, verifyCmd("424242"))
	send(t, c, changeCmd(refPIN, DefaultPIN, "135790"))
	send(t, c, getDataCmd(ObjectSlot9A, 0))
	log := c.Log()
	if len(log) != 4 {
		t.Fatalf("log has %d entries, want 4", len(log))
	}
	want := []Entry{
		{CLA: 0, INS: insSelect, P1: 4, P2: 0, DataLen: 5, SW: swOK},
		{CLA: 0, INS: insVerify, P1: 0, P2: refPIN, DataLen: 8, SW: 0x63C2},
		{CLA: 0, INS: insChange, P1: 0, P2: refPIN, DataLen: 16, SW: swOK},
		{CLA: 0, INS: insGetData, P1: 0x3F, P2: 0xFF, DataLen: 5, SW: swOK},
	}
	for i := range want {
		if log[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, log[i], want[i])
		}
	}
	typ := reflect.TypeOf(Entry{})
	for i := 0; i < typ.NumField(); i++ {
		if k := typ.Field(i).Type.Kind(); k == reflect.Slice || k == reflect.Array || k == reflect.Map || k == reflect.Pointer {
			t.Errorf("Entry.%s is a %s: an entry must hold no payload", typ.Field(i).Name, k)
		}
	}
	text := fmt.Sprintf("%+v %v %#v", log, log, log)
	for _, secret := range []string{"424242", "135790", "123456", "deadbeef", "DEADBEEF", "222, 173"} {
		if strings.Contains(text, secret) {
			t.Errorf("the log mentions %q: %s", secret, text)
		}
	}
	// The log is a copy.
	log[0].INS = 0x99
	if c.Log()[0].INS != insSelect {
		t.Error("Log returned the card's own slice")
	}
}

func TestTripwireRecordsTheForbiddenInstructions(t *testing.T) {
	for _, ins := range []byte{insReset, insSetRetries, insImportKey} {
		c := newSelected(t, Options{})
		data, sw := send(t, c, short(0, ins, 0, 0, nil, -1))
		wantSW(t, "forbidden INS", sw, swInsNotAllowed)
		if len(data) != 0 {
			t.Errorf("data %x", data)
		}
		f := c.Forbidden()
		if len(f) != 1 || f[0].INS != ins || f[0].SW != swInsNotAllowed || f[0].Why == "" {
			t.Errorf("Forbidden after INS %02X = %+v", ins, f)
		}
	}
	t.Run("recorded before SELECT too", func(t *testing.T) {
		c := New(Options{})
		c.PowerOn()
		_, sw := send(t, c, []byte{0, insReset, 0, 0})
		wantSW(t, "INS FB before SELECT", sw, swNotAllowed) // assumed: R7
		if f := c.Forbidden(); len(f) != 1 || f[0].INS != insReset {
			t.Errorf("Forbidden = %+v", f)
		}
	})
	t.Run("with any CLA, chained, and extended", func(t *testing.T) {
		c := newSelected(t, Options{})
		send(t, c, []byte{0x80, insReset, 0, 0})
		send(t, c, short(claChain, insImportKey, 0x07, 0x9A, []byte{1, 2, 3}, -1))
		send(t, c, extended(0, insSetRetries, 3, 3, nil, 0))
		if n := len(c.Forbidden()); n != 3 {
			t.Errorf("Forbidden has %d entries, want 3: %+v", n, c.Forbidden())
		}
	})
	t.Run("the permitted commands are not recorded", func(t *testing.T) {
		c := newSelected(t, Options{})
		send(t, c, []byte{0, insGetVersion, 0, 0})
		send(t, c, verifyCmd(DefaultPIN))
		send(t, c, []byte{0, 0x99, 0, 0})
		if f := c.Forbidden(); len(f) != 0 {
			t.Errorf("Forbidden = %+v", f)
		}
	})
	t.Run("the log of the card is not card state", func(t *testing.T) {
		c := newSelected(t, Options{})
		send(t, c, []byte{0, insReset, 0, 0})
		c.PowerOff()
		c.PowerOn()
		if len(c.Forbidden()) != 1 {
			t.Error("a power cycle cleared Forbidden")
		}
	})
	t.Run("RequireNoForbidden", func(t *testing.T) {
		c := newSelected(t, Options{})
		var ft fakeT
		RequireNoForbidden(&ft, c)
		if ft.failed {
			t.Fatalf("failed on a clean card: %s", ft.msg)
		}
		send(t, c, []byte{0, insReset, 0, 0})
		RequireNoForbidden(&ft, c)
		if !ft.failed || !strings.Contains(ft.msg, "INS FB") {
			t.Errorf("did not fail, or did not say what: %q", ft.msg)
		}
	})
}

type fakeT struct {
	failed bool
	msg    string
}

func (f *fakeT) Helper() {}
func (f *fakeT) Fatalf(format string, args ...any) {
	f.failed, f.msg = true, fmt.Sprintf(format, args...)
}

func TestWrongCLAAndINS(t *testing.T) {
	c := newSelected(t, Options{})
	for _, cla := range []byte{0x01, 0x04, 0x0C, 0x80, 0x84, 0xFF} {
		_, sw := send(t, c, []byte{cla, insGetVersion, 0, 0})
		wantSW(t, "CLA", sw, swClaNotAllowed)
	}
	for _, ins := range []byte{0x00, 0x01, 0x2C, 0xDB, 0xF9, 0xFC, 0xFF} {
		_, sw := send(t, c, []byte{0, ins, 0, 0})
		wantSW(t, "INS", sw, swInsNotAllowed)
	}
}

func TestCommandsRefuseWhatTheyDoNotTake(t *testing.T) { // assumed: R8
	c := newSelected(t, Options{})
	for _, tc := range []struct {
		name  string
		apdu  []byte
		want  uint16
		spent bool
	}{
		{"VERIFY P2 81", short(0, insVerify, 0, 0x81, pad(DefaultPUK), -1), swRefNotFound, false},
		{"VERIFY P2 9B", short(0, insVerify, 0, 0x9B, pad(DefaultPIN), -1), swRefNotFound, false},
		{"VERIFY P1 01", short(0, insVerify, 1, refPIN, pad(DefaultPIN), -1), swWrongP1P2, false},
		{"VERIFY 7 bytes", short(0, insVerify, 0, refPIN, pad(DefaultPIN)[:7], -1), swWrongLength, false},
		{"VERIFY 9 bytes", short(0, insVerify, 0, refPIN, append(pad(DefaultPIN), 0), -1), swWrongLength, false},
		{"CHANGE 8 bytes", short(0, insChange, 0, refPIN, pad(DefaultPIN), -1), swWrongLength, false},
		{"CHANGE P2 9B", short(0, insChange, 0, 0x9B, join(pad(DefaultPIN), pad("654321")), -1), swRefNotFound, false},
		{"CHANGE P1 01", short(0, insChange, 1, refPIN, join(pad(DefaultPIN), pad("654321")), -1), swWrongP1P2, false},
		{"GET METADATA with data", short(0, insGetMetadata, 0, refPIN, []byte{1}, -1), swWrongLength, false},
		{"GET METADATA P1", short(0, insGetMetadata, 1, refPIN, nil, 0), swWrongP1P2, false},
		{"GET VERSION P1", []byte{0, insGetVersion, 1, 0}, swWrongP1P2, false},
		{"GET SERIAL with data", short(0, insGetSerial, 0, 0, []byte{1}, -1), swWrongLength, false},
		{"GET RESPONSE with data", short(0, insGetResponse, 0, 0, []byte{1}, -1), swWrongP1P2, false}, // nothing waits: R10 first
	} {
		_, sw := send(t, c, tc.apdu)
		wantSW(t, tc.name, sw, tc.want)
	}
	if c.st().pin.left != 3 || c.st().puk.left != 3 {
		t.Errorf("a refused command spent a try: PIN %d PUK %d", c.st().pin.left, c.st().puk.left)
	}
}

func TestOptions(t *testing.T) {
	for name, o := range map[string]Options{
		"a short PIN":       {PIN: "12345"},
		"a long PUK":        {PUK: "123456789"},
		"too many tries":    {MaxPINTries: 16},
		"too many PUK ones": {MaxPUKTries: 200},
		"a mode":            {ResponseMode: 7},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: New did not panic", name)
				}
			}()
			New(o)
		}()
	}
	c := newSelected(t, Options{PIN: "999999", PUK: "88888888"})
	_, sw := send(t, c, verifyCmd("999999"))
	wantSW(t, "PIN of the option", sw, swOK)
	_, sw = send(t, c, changeCmd(refPUK, "88888888", "77777777"))
	wantSW(t, "PUK of the option", sw, swOK)
}

func TestTLV(t *testing.T) {
	for _, wantTag := range []uint32{0x53, 0x5F01, 0x5FC105} {
		for _, n := range []int{0, 1, 127, 128, 255, 256, 4464} {
			v := make([]byte, n)
			b := tlv(wantTag, v)
			tag, got, rest, ok := readTLV(append(b, 0xAA))
			if !ok || tag != wantTag || len(got) != n || !bytes.Equal(rest, []byte{0xAA}) {
				t.Errorf("tag %x n = %d: tag %x ok %v len %d rest %x", wantTag, n, tag, ok, len(got), rest)
			}
		}
	}
	for _, bad := range [][]byte{nil, {0x5C}, {0x5C, 0x03, 1}, {0x5C, 0x81}, {0x5C, 0x83, 1, 2, 3}, {0x5F}, {0x5F, 0x80}, {0x5F, 0x80, 0x80, 0x80, 0x01}} {
		if _, _, _, ok := readTLV(bad); ok {
			t.Errorf("readTLV(%x) = ok", bad)
		}
	}
}
