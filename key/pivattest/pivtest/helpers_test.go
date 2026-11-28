package pivtest

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// short builds a short command APDU. le < 0 leaves Le out; 256 is coded 00.
func short(cla, ins, p1, p2 byte, data []byte, le int) []byte {
	b := []byte{cla, ins, p1, p2}
	if len(data) > 0 {
		b = append(b, byte(len(data)&0xFF))
		b = append(b, data...)
	}
	if le >= 0 {
		b = append(b, byte(le&0xFF))
	}
	return b
}

// extended builds an extended command APDU. le < 0 leaves Le out; 65536 is coded 00 00.
func extended(cla, ins, p1, p2 byte, data []byte, le int) []byte {
	b := []byte{cla, ins, p1, p2}
	if len(data) > 0 {
		b = append(b, 0)
		b = binary.BigEndian.AppendUint16(b, uint16(len(data)&0xFFFF))
		b = append(b, data...)
	} else if le >= 0 {
		b = append(b, 0)
	}
	if le >= 0 {
		b = binary.BigEndian.AppendUint16(b, uint16(le&0xFFFF))
	}
	return b
}

// pad pads a PIN or a PUK with FF to 8 bytes.
func pad(s string) []byte {
	b := []byte(s)
	for len(b) < 8 {
		b = append(b, 0xFF)
	}
	return b
}

func join(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

// send sends one APDU and splits the answer into data and status word.
func send(t testing.TB, c *Card, apdu []byte) ([]byte, uint16) {
	t.Helper()
	resp, wait := c.Exchange(apdu)
	if wait != 0 {
		t.Fatalf("wait = %v, want 0 in this part", wait)
	}
	if len(resp) < 2 {
		t.Fatalf("response %x is shorter than a status word", resp)
	}
	n := len(resp) - 2
	return resp[:n], uint16(resp[n])<<8 | uint16(resp[n+1])
}

func wantSW(t testing.TB, what string, got, want uint16) {
	t.Helper()
	if got != want {
		t.Errorf("%s: SW = %04X, want %04X", what, got, want)
	}
}

func selectPIV(t testing.TB, c *Card) {
	t.Helper()
	if _, sw := send(t, c, short(0, insSelect, 0x04, 0x00, aidShort, 0)); sw != swOK {
		t.Fatalf("SELECT: SW = %04X", sw)
	}
}

// newSelected is a card that is powered on and has the PIV application selected.
func newSelected(t testing.TB, o Options) *Card {
	t.Helper()
	c := New(o)
	c.PowerOn()
	selectPIV(t, c)
	return c
}

func verifyCmd(pin string) []byte { return short(0, insVerify, 0, refPIN, pad(pin), -1) }
func emptyVerify() []byte         { return []byte{0, insVerify, 0, refPIN} }

func changeCmd(ref byte, old, next string) []byte {
	return short(0, insChange, 0, ref, join(pad(old), pad(next)), -1)
}

func metadataCmd(ref byte) []byte { return short(0, insGetMetadata, 0, ref, nil, 0) }

func getDataCmd(id uint32, le int) []byte {
	return short(0, insGetData, 0x3F, 0xFF, append([]byte{0x5C, 0x03}, binary.BigEndian.AppendUint32(nil, id)[1:]...), le)
}

func getResponseCmd(le int) []byte { return []byte{0, insGetResponse, 0, 0, byte(le & 0xFF)} }

// bigObject is an object of n bytes: 53 followed by a length and n-4 bytes counting upward.
func bigObject(n int) []byte {
	body := make([]byte, n-4)
	for i := range body {
		body[i] = byte(i)
	}
	return tlv(tagObject, body)
}
