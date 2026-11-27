package pivattest_test

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/truvity/atoom/key/pivattest"
	"github.com/truvity/atoom/key/pivattest/attesttest"
)

// The six cases of the corpus where a trust anchor is given as the slot certificate (the PIV attestation intermediate and the root, each with
// three signers). The standard library reads an anchor as a chain of one certificate, so the check accepts it whoever the signer is. What the
// caller gets for it is held here fact by fact, apart from the strict replay: nothing is stated (every fact zero, every flag false), Generated is
// false, and the key of the anchor itself is not attested, by any of the three entries.
func TestQPAnAnchorGivenAsTheSlotCertificateIsAcceptedAndStatesNothing(t *testing.T) {
	c := loadCorpus(t)
	vs := verifiers(t, c)
	byName := map[string]attesttest.Case{}
	for _, cs := range c.Cases {
		byName[cs.Name] = cs
	}
	anchors := map[string]struct{ cn string }{"piv-intermediate": {"Yubico PIV Attestation TEST ONLY 1"}, "root": {"TEST ONLY Yubico Attestation Root 1"}}
	n := 0
	for a, info := range anchors {
		for _, signer := range []string{"unrelated", "f9", "itself"} {
			name := "slot-is-the-" + a + "-signer-" + signer
			cs, ok := byName[name]
			if !ok {
				t.Errorf("the corpus has no case %q", name)
				continue
			}
			n++
			t.Run(name, func(t *testing.T) {
				// the case holds what its name says: the slot certificate is that anchor of the PKI of the case
				pk := c.PKIs[cs.PKI]
				if !qpPEMHolds(pk, cs.Slot, info.cn) {
					t.Fatalf("the slot certificate is not the certificate %q of the PKI %q", info.cn, cs.PKI)
				}
				// and the signer is what its name says: another PKI's attestation certificate chains to nothing of this PKI, the card's own does
				pool := x509.NewCertPool()
				pool.AppendCertsFromPEM([]byte(pk.RootPEM))
				pool.AppendCertsFromPEM([]byte(pk.BundlePEM))
				_, chainErr := parse(t, cs.Signer).Verify(x509.VerifyOptions{Roots: pool, CurrentTime: c.At, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
				switch signer {
				case "itself":
					if !bytes.Equal(cs.Slot, cs.Signer) {
						t.Error("the signer is not the slot certificate")
					}
				case "f9":
					if bytes.Equal(cs.Slot, cs.Signer) || !strings.Contains(parse(t, cs.Signer).Subject.CommonName, "F9") || chainErr != nil {
						t.Errorf("the signer is not the card's own f9 certificate: %q, %v", parse(t, cs.Signer).Subject.CommonName, chainErr)
					}
				case "unrelated":
					if bytes.Equal(cs.Slot, cs.Signer) || !strings.Contains(parse(t, cs.Signer).Subject.CommonName, "F9") || chainErr == nil {
						t.Errorf("the signer is not the f9 certificate of another PKI: %q, %v", parse(t, cs.Signer).Subject.CommonName, chainErr)
					}
				}
				if !cs.Want.OK || cs.Want.Class != "" || cs.Want.Slot != 0 || cs.Want.Firmware != [3]int{} || cs.Want.Serial != 0 || cs.Want.FormFactor != 0 || cs.Want.PIN != 0 || cs.Want.Touch != 0 {
					t.Fatalf("the recorded answer is not 'accepted with every fact zero': %+v", cs.Want)
				}
				slot, signerCert := parse(t, cs.Slot), parse(t, cs.Signer)
				v := vs[cs.PKI]
				at, err := v.VerifyAt(c.At, slot, signerCert)
				if err != nil {
					t.Fatalf("VerifyAt: %v", err)
				}
				der, err := v.VerifyDER(cs.Slot, cs.Signer)
				if err != nil {
					t.Fatalf("VerifyDER: %v", err)
				}
				now, err := v.Verify(slot, signerCert)
				if err != nil {
					t.Fatalf("Verify: %v", err)
				}
				for entry, ev := range map[string]pivattest.Evidence{"VerifyAt": at, "VerifyDER": der, "Verify": now} {
					ownKey := parse(t, cs.Slot).PublicKey
					gotKey := ev.PublicKey
					ev.PublicKey = nil
					if want := (pivattest.Evidence{Algorithm: pivattest.AlgEC384, Anchor: info.cn}); ev != want || !sameKey(gotKey, ownKey) {
						t.Errorf("%s: evidence %+v, want nothing stated, the anchor's own key and the anchor %q", entry, ev, info.cn)
					}
					ev.PublicKey = gotKey
					if ev.Generated {
						t.Errorf("%s: Generated is true for an anchor given as the slot certificate", entry)
					}
					if ev.AttestsKey(ownKey) {
						t.Errorf("%s: the anchor's own key is attested", entry)
					}
					if ev.AttestsKey(parse(t, cs.Signer).PublicKey) {
						t.Errorf("%s: the signer's key is attested", entry)
					}
				}
			})
		}
	}
	if n != 6 {
		t.Errorf("%d of the six cases found", n)
	}
}

// qpPEMHolds says that der is the certificate of the common name cn that the PKI's root file or bundle holds.
func qpPEMHolds(p attesttest.PKIData, der []byte, cn string) bool {
	for _, f := range []string{p.RootPEM, p.BundlePEM} {
		rest := []byte(f)
		for {
			var blk *pem.Block
			blk, rest = pem.Decode(rest)
			if blk == nil {
				break
			}
			if cert, err := x509.ParseCertificate(blk.Bytes); err == nil && cert.Subject.CommonName == cn && bytes.Equal(cert.Raw, der) {
				return true
			}
		}
	}
	return false
}
