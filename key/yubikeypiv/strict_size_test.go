package yubikeypiv_test

import (
	"crypto/x509"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/common/refusal"
	"github.com/truvity/atoom/key/pivattest"
)

// E. The size check of the peer path. A certificate of a card is about 1 KiB; the bytes that a peer sends are refused over the library's limit
// before they are parsed, from the field that holds them, and a certificate of exactly the limit goes on to the check.

func qpWantSizeRefusal(t *testing.T, name string, o qpOut, field string) {
	t.Helper()
	if o.err == nil {
		t.Fatalf("%s: accepted", name)
	}
	if connect.CodeOf(o.err) != connect.CodeInvalidArgument {
		t.Fatalf("%s: code %s (%v)", name, connect.CodeOf(o.err), o.err)
	}
	if r, _ := refusal.Reason(o.err); r != "attestation" {
		t.Fatalf("%s: reason %q", name, r)
	}
	if f, _ := refusal.FieldPath(o.err); f != field {
		t.Fatalf("%s: field %q, want %q", name, f, field)
	}
	if strings.Contains(o.err.Error(), "16385") || strings.Contains(o.err.Error(), "longer") {
		t.Errorf("%s: the words name the size: %v", name, o.err)
	}
	var refused int
	for _, l := range o.rec.got() {
		switch l.msg {
		case "a peer's attestation refused":
			t.Errorf("%s: the check of the library was reached", name)
		case "refused":
			refused++
		}
	}
	if refused != 1 {
		t.Errorf("%s: %d refusal lines, want one", name, refused)
	}
}

func TestQPThePeersCertificateOfExactlyTheLimitIsCheckedAndOneByteMoreIsRefusedBeforeParsing(t *testing.T) {
	const limit = pivattest.MaxCertificateSize
	const serial = 12345678
	w := newQPWorld(t, serial, false, false)
	slotAt := w.sizedSlot(t, limit, qpSlot{})
	slotOver := w.sizedSlot(t, limit+1, qpSlot{})
	wBigF9 := w.sizedF9(t, limit)
	slotUnderBig, _ := wBigF9.slot(t, qpSlot{})
	wOverF9 := w.sizedF9(t, limit+1)
	slotUnderOver, _ := wOverF9.slot(t, qpSlot{})
	small, _ := w.slot(t, qpSlot{})
	if len(slotAt.Raw) != limit || len(slotOver.Raw) != limit+1 || len(wBigF9.f9.Raw) != limit || len(wOverF9.f9.Raw) != limit+1 {
		t.Fatalf("the sizes are %d %d %d %d", len(slotAt.Raw), len(slotOver.Raw), len(wBigF9.f9.Raw), len(wOverF9.f9.Raw))
	}

	// at the limit: not refused for its size, and the whole check is made: the answer is the key
	for name, in := range map[string][2]*x509.Certificate{"a slot certificate of the limit": {w.f9, slotAt}, "an attestation certificate of the limit": {wBigF9.f9, slotUnderBig}} {
		if o := qpPeer(t, w, in[0], in[1]); !o.accepted() || len(o.spki) == 0 {
			t.Errorf("%s: %v", name, o.err)
		}
	}
	// one byte over, for each certificate: the refusal of the field that holds it
	qpWantSizeRefusal(t, "a slot certificate one byte over", qpPeer(t, w, w.f9, slotOver), "attestation")
	qpWantSizeRefusal(t, "an attestation certificate one byte over", qpPeer(t, w, wOverF9.f9, slotUnderOver), "attestation_signer")
	qpWantSizeRefusal(t, "both one byte over", qpPeer(t, w, wOverF9.f9, slotOver), "attestation")

	// the check comes before any parsing: bytes over the limit that are no certificate are refused for their size, and the field says which
	// of the two it was, even when the other one is the first to fail a parse
	junkOver, junkAt, junkSmall := make([]byte, limit+1), make([]byte, limit), []byte{0x30, 0x03, 1, 2, 3}
	qpWantSizeRefusal(t, "junk over the limit as the attestation", qpPeerRaw(t, w, junkOver, w.f9.Raw), "attestation")
	qpWantSizeRefusal(t, "junk over the limit as the signer, with a valid attestation", qpPeerRaw(t, w, small.Raw, junkOver), "attestation_signer")
	qpWantSizeRefusal(t, "junk over the limit as the signer, with an attestation that does not parse", qpPeerRaw(t, w, junkSmall, junkOver), "attestation_signer")
	qpWantSizeRefusal(t, "junk over the limit in both", qpPeerRaw(t, w, junkOver, junkOver), "attestation")
	// junk that is not over the limit is the refusal of a parse, from the field that fails first: the attestation
	for name, in := range map[string][2][]byte{
		"junk of the limit as the attestation":                 {junkAt, w.f9.Raw},
		"junk of the limit as the signer, a valid attestation": {small.Raw, junkAt},
		"junk of the limit in both":                            {junkAt, junkAt},
		"nothing at all":                                       {nil, nil},
		"junk that is small in both":                           {junkSmall, junkSmall},
	} {
		o := qpPeerRaw(t, w, in[0], in[1])
		want := "attestation"
		if name == "junk of the limit as the signer, a valid attestation" {
			want = "attestation_signer"
		}
		if connect.CodeOf(o.err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, o.err)
			continue
		}
		if f, _ := refusal.FieldPath(o.err); f != want {
			t.Errorf("%s: field %q, want %q", name, f, want)
		}
	}
}

// On adoption the certificate comes from the card, not from a peer: the driver does not size it, the library does. A certificate of the limit is
// adopted and one byte more is refused as a certificate that cannot be read (not as a chain), with the journal saying why.
func TestQPAnAdoptedCertificateOfTheLimitIsTakenAndOneByteMoreIsRefused(t *testing.T) {
	const limit = pivattest.MaxCertificateSize
	w := newQPWorld(t, 12345678, false, false)
	if o := qpAdopt(t, w, w.f9, w.sizedSlot(t, limit, qpSlot{})); !o.accepted() {
		t.Fatalf("a slot certificate of the limit: %v", o.err)
	}
	o := qpAdopt(t, w, w.f9, w.sizedSlot(t, limit+1, qpSlot{}))
	if o.accepted() || !strings.HasPrefix(o.word, "certificate: ") {
		t.Fatalf("one byte over: %v", o.err)
	}
	line := o.rec.line(t, "attestation refused")
	if got := line.attrs[1].Value.String(); !strings.Contains(got, "longer than") {
		t.Errorf("the journal says %q", got)
	}
}
