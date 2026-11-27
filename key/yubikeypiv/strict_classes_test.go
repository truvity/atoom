package yubikeypiv_test

import (
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/truvity/atoom/key/pivattest"
	"github.com/truvity/atoom/key/yubikeypiv/internal/card"
)

// The refusal of an adoption whose attestation check failed is worded by where the error comes from: "clock" and "pins" for an error of building
// the verifier over the pinned certificates (the card marks it, card.BuildError), "certificate" for a certificate that is missing, too large, does not
// parse or has a malformed vendor extension, "chain" for the rest. Each class is reached here with the real library: real certificates through
// the fake card and a test PKI, and, for the errors of the build, the errors that the library itself gives (the clock from Default under the fake
// clock of testing/synctest, the files from the constructor), marked as the card marks them. The words hold no key, serial or certificate content.

const (
	qpSayChain = "chain: the attestation does not chain to the Yubico root"
	qpSayCert  = "certificate: the attestation certificate is missing, too large, does not parse or has a malformed vendor extension"
	qpSayPins  = "pins: the pinned Yubico certificates of this program are not as its table says"
	qpSayClock = "clock: the clock of this machine is outside the validity of the pinned certificates"
)

// qpRealBuildErrors are errors of building a verifier, as the library gives them for the faults of the files and of the clock.
func qpRealBuildErrors(t *testing.T, w *qpWorld) map[string]error {
	t.Helper()
	root, bundle := qpPEM(w.root), qpPEM(w.piv)
	out := map[string]error{}
	var err error
	_, err = pivattest.NewVerifierFromPEM(root, bundle, strings.Repeat("0", 64))
	out["the root is not the pinned one"] = err
	_, err = pivattest.NewVerifierFromPEM([]byte("not a certificate"), bundle, qpSum(w.root))
	out["the root file holds no certificate"] = err
	_, err = pivattest.NewVerifierFromPEM(root, nil, qpSum(w.root))
	out["the bundle holds no PIV intermediate"] = err
	bad := append(slices.Clone(bundle), []byte("-----BEGIN CERTIFICATE-----\nAQID\n-----END CERTIFICATE-----\n")...)
	_, err = pivattest.NewVerifierFromPEM(root, bad, qpSum(w.root))
	out["a certificate of the program's own bundle does not parse"] = err
	for name, e := range out {
		if e == nil {
			t.Fatalf("%s: the library built a verifier", name)
		}
	}
	// the clock: the error that Default gives at a clock before the pinned certificates (the fake clock of the bubble starts in 2000)
	synctest.Test(t, func(t *testing.T) {
		_, e := pivattest.Default()
		if !errors.Is(e, pivattest.ErrClock) {
			t.Fatalf("Default in the year 2000: %v", e)
		}
		out["the clock of the machine"] = e
	})
	return out
}

func qpSum(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	return hex.EncodeToString(sum[:])
}

func TestQPTheRefusalOfAFailedCheckIsWordedByWhereTheErrorComes(t *testing.T) {
	const serial = 12345678
	w := newQPWorld(t, serial, false, false)
	other := newQPWorld(t, serial, false, false)
	good, _ := w.slot(t, qpSlot{})
	cert := func(ext ...pkix.Extension) *x509.Certificate { c, _ := w.slot(t, qpSlot{ext: ext}); return c }
	rogue, _ := other.slot(t, qpSlot{})

	type tc struct {
		name   string
		slot   *x509.Certificate
		verify func(att, slot *x509.Certificate) (card.Attestation, error)
		want   string
	}
	var cases []tc
	for name, e := range qpRealBuildErrors(t, w) {
		e := e
		word := qpSayPins
		if name == "the clock of the machine" {
			word = qpSayClock
		}
		cases = append(cases, tc{"a build error: " + name, good, func(_, _ *x509.Certificate) (card.Attestation, error) { return card.Attestation{}, card.BuildError(e) }, word})
	}
	for _, c := range []struct {
		name string
		slot *x509.Certificate
	}{
		{"a certificate over the limit", w.sizedSlot(t, pivattest.MaxCertificateSize+1, qpSlot{})},
		{"a certificate that is missing", nil},
		{"a certificate that does not parse", &x509.Certificate{Raw: []byte{1, 2, 3}}},
		{"a certificate with no raw bytes", &x509.Certificate{}},
		{"a malformed firmware extension", cert(pkix.Extension{Id: oidFW, Value: []byte{5, 8}})},
		{"a malformed serial extension", cert(pkix.Extension{Id: oidSer, Value: []byte{0x02, 0x01, 0xff}})},
		{"a malformed policy extension", cert(pkix.Extension{Id: oidPol, Value: []byte{9, 9}})},
		{"a malformed form factor extension", cert(pkix.Extension{Id: oidForm, Value: []byte{1, 2}})},
	} {
		cases = append(cases, tc{c.name, c.slot, w.verify, qpSayCert})
	}
	zero := &pivattest.Verifier{}
	cases = append(cases,
		tc{"a slot certificate of another PKI", rogue, w.verify, qpSayChain},
		tc{"an error of the check that is of no class", good, func(_, _ *x509.Certificate) (card.Attestation, error) {
			return card.Attestation{}, errors.New("something else")
		}, qpSayChain},
		// an error that the library gives for a verifier with no anchors, from a check and not from a build: the card did not mark it
		tc{"a verifier with no anchor, from the check itself", good, func(att, slot *x509.Certificate) (card.Attestation, error) {
			_, err := zero.Verify(slot, att)
			return card.Attestation{}, err
		}, qpSayChain},
	)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wc := *w
			wc.verify = c.verify
			slot := c.slot
			o := qpAdopt(t, &wc, w.f9, slot)
			if o.accepted() || o.word != c.want {
				t.Fatalf("the word is %q (%v), want %q", o.word, o.err, c.want)
			}
			if l := qpLeaks(o.err.Error(), good, serial, true); len(l) > 0 {
				t.Errorf("the words hold %v: %q", l, o.err)
			}
		})
	}
	// and the neighbour: the good one is adopted
	if o := qpAdopt(t, w, w.f9, good); !o.accepted() {
		t.Fatalf("the good certificate: %v", o.err)
	}
}

// The log line of an attestation that is not of a key a card generated carries two fields more than it did, on both paths: whether the name gave
// a slot (a bool) and the common name of the anchor of the chain (nothing but a common name of a certificate of the PKI). Nothing else of
// the journal is new: the same lines, with the same keys, in the same order, and no key, serial or certificate byte in any value.
func TestQPTheOriginLogLineGainsTwoFieldsAndNothingElseIsNew(t *testing.T) {
	const serial = 12345678
	rooted := newQPWorld(t, serial, true, false)
	pivd := newQPWorld(t, serial, false, true)
	plain := newQPWorld(t, serial, false, false)
	const rootCN, pivCN = "TEST ONLY Yubico Attestation Root 1", "Yubico PIV Attestation TEST ONLY 1"
	byPIV, _ := plain.slot(t, qpSlot{by: "piv"})
	byRoot, _ := plain.slot(t, qpSlot{by: "root"})
	noSlot, _ := plain.slot(t, qpSlot{cn: "an attestation"})
	for _, c := range []struct {
		name      string
		w         *qpWorld
		slot      *x509.Certificate
		slotNamed bool
		anchor    string
	}{
		{"the root as the slot certificate, named 9a", rooted, rooted.root, true, "YubiKey PIV Attestation 9a"},
		{"the PIV intermediate as the slot certificate", pivd, pivd.piv, false, pivCN},
		{"signed directly by the PIV intermediate", plain, byPIV, true, pivCN},
		{"signed directly by the root", plain, byRoot, true, rootCN},
		{"a name that gives no slot, under a chain of three", plain, noSlot, false, pivCN},
	} {
		t.Run(c.name, func(t *testing.T) {
			adopt := qpAdopt(t, c.w, c.w.f9, c.slot)
			peer := qpPeer(t, c.w, c.w.f9, c.slot)
			type line struct {
				msg  string
				keys string
			}
			lines := func(o qpOut) (got []line) {
				for _, l := range o.rec.got() {
					got = append(got, line{l.msg, strings.Join(l.keys(), ",")})
				}
				return got
			}
			if want := []line{{"card session reset", "where,card,driver_holds_pin"}, {"attestation refused", "slot,error,slot_named,anchor"}}; !slices.Equal(lines(adopt), want) {
				t.Errorf("adoption: the lines are %v, want %v", lines(adopt), want)
			}
			if want := []line{{"card session reset", "where,card,driver_holds_pin"}, {"a peer's attestation refused", "error,slot_named,anchor"}, {"refused", "method,code,reason,field,caller"}}; !slices.Equal(lines(peer), want) {
				t.Errorf("peer: the lines are %v, want %v", lines(peer), want)
			}
			for who, o := range map[string]qpOut{"adoption": adopt, "peer": peer} {
				var l logLine
				for _, x := range o.rec.got() {
					if x.msg == "attestation refused" || x.msg == "a peer's attestation refused" {
						l = x
					}
				}
				at := map[string]slogValue{}
				for _, a := range l.attrs {
					at[a.Key] = slogValue{kind: a.Value.Kind().String(), str: a.Value.String()}
				}
				if at["slot_named"].kind != "Bool" || (at["slot_named"].str == "true") != c.slotNamed {
					t.Errorf("%s: slot_named is %v, want %v", who, at["slot_named"], c.slotNamed)
				}
				if at["anchor"].kind != "String" || at["anchor"].str != c.anchor {
					t.Errorf("%s: anchor is %v, want exactly the common name %q", who, at["anchor"], c.anchor)
				}
				var all string
				for _, x := range o.rec.got() {
					for _, a := range x.attrs {
						all += " " + a.Value.String()
					}
				}
				if leaks := qpLeaks(all, c.slot, serial, false); len(leaks) > 0 {
					t.Errorf("%s: the journal holds %v", who, leaks)
				}
			}
		})
	}
}

type slogValue struct{ kind, str string }
