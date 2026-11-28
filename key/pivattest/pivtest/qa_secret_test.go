package pivtest

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// The log and the tripwire are what a failing test prints. A PIN, a PUK or the body of an object that reaches either of them reaches the
// log of a build. These tests seed values made of unusual bytes, run every command that carries them in every form, and search all that the
// card gives out, except the answer to GET DATA itself, for the values in every spelling that a formatter or a person might make.

var (
	qaPIN        = []byte("\x91\x82\x73\x64\x55\x46")
	qaPUK        = []byte("\xA1\xB2\xC3\xD4\xE5\xF6\x17\x28")
	qaNewPIN     = []byte("\x66\x77\x88\x99\xAA\xBB\x2C")
	qaNewPUK     = []byte("\x31\x42\x53\x64\x75\x86\x97\x08")
	qaWrong      = []byte("\x13\x24\x35\x46\x57\x68\x79")
	qaSecretBody = append([]byte{0x53, 0x1E, 0xC0, 0xFF, 0xEE, 0xC0, 0xDE, 0xFA, 0xCE}, []byte("QAOBJMARKER-0123456789")...)
)

// qaNeedles lists the spellings of a secret: the bytes, the hex in four layouts, the decimal list that %v prints for a byte slice, the Go
// quotations, base64, and every run of four bytes of it in raw and in hex.
func qaNeedles(b []byte) []string {
	var hexs, hex0x, dec []string
	for _, x := range b {
		hexs = append(hexs, fmt.Sprintf("%02x", x))
		hex0x = append(hex0x, fmt.Sprintf("0x%02x", x))
		dec = append(dec, strconv.Itoa(int(x)))
	}
	lower := hex.EncodeToString(b)
	out := []string{
		string(b), lower, strings.ToUpper(lower),
		strings.Join(hexs, " "), strings.ToUpper(strings.Join(hexs, " ")),
		strings.Join(hex0x, ", "), strings.ToUpper(strings.Join(hex0x, ", ")),
		strings.Join(hex0x, ","), strings.Join(hexs, ":"),
		"[" + strings.Join(dec, " ") + "]", strings.Join(dec, " "), strings.Join(dec, ", "), strings.Join(dec, ","),
		strconv.Quote(string(b)), strconv.QuoteToASCII(string(b)), fmt.Sprintf("%x", string(b)), fmt.Sprintf("% x", string(b)), fmt.Sprintf("%X", string(b)),
		fmt.Sprintf("%#v", b), fmt.Sprintf("%v", b), fmt.Sprintf("%+v", b), fmt.Sprintf("%d", b),
		base64.StdEncoding.EncodeToString(b), base64.RawStdEncoding.EncodeToString(b), base64.URLEncoding.EncodeToString(b),
	}
	for i := 0; i+4 <= len(b); i++ {
		out = append(out, string(b[i:i+4]), hex.EncodeToString(b[i:i+4]), strings.ToUpper(hex.EncodeToString(b[i:i+4])))
	}
	return out
}

func qaSecrets() map[string][]byte {
	return map[string][]byte{"PIN": qaPIN, "PUK": qaPUK, "new PIN": qaNewPIN, "new PUK": qaNewPUK, "wrong value": qaWrong, "object body": qaSecretBody}
}

// qaLeaks returns what of the secrets a text holds. The ones that are in the text on purpose are named in allow.
func qaLeaks(text string, allow ...string) []string {
	var out []string
outer:
	for name, secret := range qaSecrets() {
		for _, a := range allow {
			if a == name {
				continue outer
			}
		}
		for _, n := range qaNeedles(secret) {
			if len(n) >= 4 && strings.Contains(text, n) {
				out = append(out, fmt.Sprintf("%s as %q", name, n))
				break
			}
		}
	}
	return out
}

func TestQaTheSearchFindsASecretInEverySpellingItLooksFor(t *testing.T) {
	for name, secret := range qaSecrets() {
		for i, n := range qaNeedles(secret) {
			if got := qaLeaks("log: " + n + " end"); len(got) == 0 {
				t.Errorf("%s: spelling %d (%q) is not found", name, i, n)
			}
		}
		for _, f := range []string{"%v", "%+v", "%#v", "%s", "%d", "%x", "% x", "%q"} {
			if got := qaLeaks(fmt.Sprintf(f, secret)); len(got) == 0 {
				t.Errorf("%s: the formatter %s of the bytes is not found", name, f)
			}
		}
		if got := qaLeaks(fmt.Sprintf("%v %+v", struct{ B []byte }{secret}, &struct{ B []byte }{secret})); len(got) == 0 {
			t.Errorf("%s: a secret in a struct is not found", name)
		}
	}
	if got := qaLeaks("an ordinary log: CLA 00 INS 20 P1 00 P2 80 SW 63C2 DataLen 8"); len(got) != 0 {
		t.Errorf("a clean text has a leak: %v", got)
	}
}

// qaPrint is everything that the card prints about the commands it has seen.
func qaPrint(c *Card) string {
	var sb strings.Builder
	for _, verb := range qaVerbs() { // the card itself, with every verb, as a pointer and as a value
		fmt.Fprintf(&sb, verb+"\n", c)
		fmt.Fprintf(&sb, verb+"\n", *c)
	}
	for _, list := range [][]Entry{c.Log(), c.Forbidden()} {
		for _, f := range []string{"%v", "%+v", "%#v", "%s", "%d", "%x", "%q"} {
			fmt.Fprintf(&sb, f+"\n", list)
			for _, e := range list {
				fmt.Fprintf(&sb, f+"\n", e)
				fmt.Fprintf(&sb, f+"\n", &e)
			}
		}
		for _, e := range list {
			sb.WriteString(e.Why)
			v := reflect.ValueOf(e)
			for i := 0; i < v.NumField(); i++ {
				fmt.Fprintf(&sb, " %s=%v", v.Type().Field(i).Name, v.Field(i).Interface())
			}
		}
	}
	return sb.String()
}

func qaPanicText(f func()) (text string) {
	defer func() {
		if r := recover(); r != nil {
			text = fmt.Sprint(r)
		}
	}()
	f()
	return ""
}

// qaCarrying runs every command that carries a secret, in the short form, the extended form, with an Le, chained, malformed, refused and
// forbidden, and returns the answers to all of them but GET DATA and GET RESPONSE.
func qaCarrying(t *testing.T, c *Card) (answers [][]byte) {
	t.Helper()
	send1 := func(apdu []byte) {
		t.Helper()
		resp, _ := c.Exchange(apdu)
		if resp == nil {
			t.Fatalf("no answer to %x", apdu)
		}
		answers = append(answers, resp)
	}
	padded := func(b []byte) []byte { return append(append([]byte(nil), b...), bytes0xFF(8-len(b))...) }
	oldPIN, oldPUK := padded(qaPIN), padded(qaPUK)
	newPIN, newPUK, wrong := padded(qaNewPIN), padded(qaNewPUK), padded(qaWrong)
	for _, ins := range []byte{insReset, insSetRetries, insImportKey} { // a driver that sends a secret in a forbidden command
		for _, secret := range [][]byte{qaPIN, qaPUK, qaSecretBody} {
			send1(short(0, ins, 0, 0, secret, -1))
			send1(extended(0, ins, 3, 3, secret, 0))
			send1(short(claChain, ins, 0, 0, secret[:3], -1))
			send1(short(claPlain, ins, 0, 0, secret[3:], 0))
			send1(join([]byte{0, ins, 0, 0, qaByte(len(secret) + 5)}, secret)) // a length that lies
			send1(join([]byte{0x80, ins, 0, 0, qaByte(len(secret))}, secret))  // another CLA
		}
	}
	for _, p := range [][]byte{wrong, oldPIN, wrong} {
		send1(short(0, insVerify, 0, refPIN, p, -1))
		send1(short(0, insVerify, 0, refPIN, p, 0))
		send1(extended(0, insVerify, 0, refPIN, p, 0))
		send1(short(claChain, insVerify, 0, refPIN, p[:3], -1))
		send1(short(claPlain, insVerify, 0, refPIN, p[3:], -1))
		send1(short(0, insVerify, 0, refPIN, p[:7], -1))                    // 7 bytes
		send1(short(0, insVerify, 1, refPIN, p, -1))                        // P1
		send1(short(0, insVerify, 0, refPUK, p, -1))                        // P2
		send1(join([]byte{0, insVerify, 0, refPIN, 9}, p))                  // Lc of 9 for 8 bytes
		send1(join([]byte{0, insVerify, 0, refPIN, 8}, p, []byte{0, 0}))    // two bytes after the Le
		send1(append([]byte{0x80, insVerify, 0, refPIN, 8}, p...))          // another CLA
		send1(extended(0, insVerify, 0, refPIN, p[:7], -1))                 // extended, 7 bytes
		send1(join([]byte{0, insVerify, 0, refPIN, 0, 0, 8}, p[:5]))        // extended, too short
		send1(join([]byte{0, insVerify, 0, refPIN, 0, 0, 0, 8}, p))         // an extended Lc of zero, then data
		send1(join([]byte{0, insVerify, 0, refPIN, 0, 0, 8}, p, p))         // extended, too long
		send1(join([]byte{0, insVerify, 0, refPIN, 0, 0, 8}, p, []byte{0})) // extended, one byte too many
	}
	both := func(ref byte, old, next []byte) {
		block := join(old, next)
		send1(short(0, insChange, 0, ref, block, -1))
		send1(short(0, insChange, 0, ref, block, 0))
		send1(extended(0, insChange, 0, ref, block, 0))
		send1(short(claChain, insChange, 0, ref, block[:5], -1))
		send1(extended(claChain, insChange, 0, ref, block[5:12], -1))
		send1(short(claPlain, insChange, 0, ref, block[12:], 0))
		send1(short(0, insChange, 0, ref, block[:15], -1))
		send1(short(0, insChange, 1, ref, block, -1))
		send1(short(0, insChange, 0, 0x9B, block, -1))
		send1(join([]byte{0, insChange, 0, ref, 17}, block))
		send1(join([]byte{0x80, insChange, 0, ref, 16}, block))
		send1(short(0, insChange, 0, ref, join(old, bytes0xFF(8)), -1)) // a new value of nothing
		send1(short(0, insChange, 0, ref, join(old, next[:3], bytes0xFF(5)), -1))
	}
	both(refPIN, wrong, newPIN)
	both(refPUK, wrong, newPUK)
	both(refPIN, oldPIN, newPIN) // the PIN is now the new one
	send1(short(0, insVerify, 0, refPIN, oldPIN, -1))
	send1(short(0, insVerify, 0, refPIN, newPIN, -1))
	both(refPUK, oldPUK, newPUK)
	both(refPUK, newPUK, oldPUK)
	for _, ref := range []byte{refPIN, refPUK, 0x9B} {
		send1(metadataCmd(ref))
		send1(short(0, insGetMetadata, 0, ref, qaPIN, -1))
	}
	for i := 0; i < 4; i++ { // block the PIN, then more of everything
		send1(short(0, insVerify, 0, refPIN, wrong, -1))
	}
	send1(short(0, insVerify, 0, refPIN, newPIN, -1))
	send1(changeCmd(refPIN, string(qaNewPIN), string(qaPIN)))
	send1(short(0, insSelect, 4, 0, qaPIN, 0))
	send1(short(0, insSelect, 4, 0, append(append([]byte(nil), aidShort...), qaPUK...), 0))
	send1(short(0, insGetVersion, 0, 0, qaPUK, -1))
	send1(short(0, insGetSerial, 0, 0, qaPIN, -1))
	// the object: asked for in many forms, and the id list of other bytes
	for _, apdu := range [][]byte{
		short(0, insGetData, 0x3F, 0xFF, qaPIN, 0), short(0, insGetData, 0x3F, 0xFF, qaSecretBody, 0), extended(0, insGetData, 0x3F, 0xFF, qaSecretBody, 0),
		short(0, insGetData, 0x3F, 0xFF, append([]byte{0x5C, 3}, qaPIN[:3]...), 0), short(0, insGetData, 0x3F, 0xFF, append([]byte{0x5C, 3}, qaPIN[:2]...), 0),
		short(0, insGetData, 1, 2, qaSecretBody, 0), short(0, insGetResponse, 0, 0, qaSecretBody, -1),
	} {
		send1(apdu)
	}
	return answers
}

func bytes0xFF(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = 0xFF
	}
	return b
}

func TestQaTheLogTheTripwireAndTheAnswersNeverHoldAPINAPUKOrABody(t *testing.T) {
	for name, o := range map[string]Options{
		"default modes":      {PIN: string(qaPIN), PUK: string(qaPUK)},
		"short responses":    {PIN: string(qaPIN), PUK: string(qaPUK), ResponseMode: Short61xx, MaxPINTries: 15},
		"no extended APDUs":  {PIN: string(qaPIN), PUK: string(qaPUK), NoExtendedAPDU: true},
		"one try each":       {PIN: string(qaPIN), PUK: string(qaPUK), MaxPINTries: 1, MaxPUKTries: 1},
		"before SELECT only": {PIN: string(qaPIN), PUK: string(qaPUK)},
	} {
		t.Run(name, func(t *testing.T) {
			c := New(o)
			c.PutObject(ObjectSlot9A, qaSecretBody)
			c.PutObject(ObjectSlot9C, qaSecretBody)
			c.PowerOn()
			if name != "before SELECT only" {
				selectPIV(t, c)
			}
			answers := qaCarrying(t, c)
			// a power cycle, and the same again with the session of a fresh SELECT
			c.PowerOff()
			c.PowerOn()
			selectPIV(t, c)
			answers = append(answers, qaCarrying(t, c)...)
			if got := qaLeaks(qaPrint(c)); len(got) != 0 {
				t.Errorf("the card, the log or the list of the forbidden prints: %v", got)
			}
			for _, verb := range qaVerbs() {
				if !qaOptionsBadVerb(verb) {
					if got := qaLeaks(fmt.Sprintf(verb, o)); len(got) != 0 {
						t.Errorf("%s of the options prints %v", verb, got)
					}
				}
			}
			for _, a := range answers {
				if got := qaLeaks(string(a)); len(got) != 0 {
					t.Errorf("an answer holds %v: %x", got, a)
				}
			}
			if len(c.Log()) < 300 || len(c.Forbidden()) < 20 {
				t.Errorf("the run was too small to mean anything: %d entries, %d forbidden", len(c.Log()), len(c.Forbidden()))
			}
			// the failure message of RequireNoForbidden, which is what a failing test prints
			var ft fakeT
			RequireNoForbidden(&ft, c)
			if !ft.failed {
				t.Fatal("RequireNoForbidden did not fail")
			}
			if got := qaLeaks(ft.msg); len(got) != 0 {
				t.Errorf("the failure message holds %v: %q", got, ft.msg)
			}
		})
	}
}

func TestQaTheErrorTextsOfNewNeverHoldTheValue(t *testing.T) {
	for _, o := range []Options{
		{PIN: "\x91\x82\x73\x64\x55"}, {PIN: "\x91\x82\x73\x64\x55\x46\x37\x28\x19"}, {PUK: "\xA1\xB2\xC3\xD4\xE5"}, {PUK: "\xA1\xB2\xC3\xD4\xE5\xF6\x17\x28\x39"},
		{MaxPINTries: 16, PIN: string(qaPIN)}, {ResponseMode: 9, PUK: string(qaPUK)},
	} {
		text := qaPanicText(func() { New(o) })
		if text == "" {
			t.Errorf("%+v: no panic", o)
		}
		if got := qaLeaks(text); len(got) != 0 {
			t.Errorf("the panic text holds %v: %q", got, text)
		}
		if got := qaLeaks(strings.ToValidUTF8(text, "")); len(got) != 0 {
			t.Errorf("the panic text holds %v: %q", got, text)
		}
	}
}

// What a driver's test may print about a failed run is the entries of the log: no field of an entry can hold a payload, whatever a later
// change adds.
func TestQaAnEntryHoldsNumbersAndAReasonOnly(t *testing.T) {
	typ := reflect.TypeOf(Entry{})
	want := map[string]reflect.Kind{"CLA": reflect.Uint8, "INS": reflect.Uint8, "P1": reflect.Uint8, "P2": reflect.Uint8, "DataLen": reflect.Int,
		"SW": reflect.Uint16, "Malformed": reflect.Bool, "Why": reflect.String}
	if typ.NumField() != len(want) {
		t.Errorf("Entry has %d fields, the audit knows %d", typ.NumField(), len(want))
	}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if want[f.Name] != f.Type.Kind() {
			t.Errorf("Entry.%s is %s, the audit expects %s", f.Name, f.Type.Kind(), want[f.Name])
		}
	}
}
