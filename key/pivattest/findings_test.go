package pivattest_test

import (
	"errors"
	"os"
	"testing"

	"github.com/truvity/atoom/key/pivattest"
	"github.com/truvity/atoom/key/pivattest/attesttest"
)

// Findings: behaviour that the library has today (and the check it replaces has, as the standard library's chain building gives it) and that
// a caller could be hurt by. Each test below states the behaviour a careful reader would want and FAILS TODAY; it is skipped unless
// ATOOM_PIVATTEST_QA_FINDINGS is set, so the suite stays green. The behaviour that is there today is pinned by the tests of misuse_test.go
// (TestTheSameCertificateTwiceAndAnAnchorAsTheSlotCertificate), which pass.
const findingsSwitch = "ATOOM_PIVATTEST_QA_FINDINGS"

func skipUnlessFindings(t *testing.T) {
	t.Helper()
	if os.Getenv(findingsSwitch) == "" {
		t.Skipf("a finding that fails today: set %s to run it", findingsSwitch)
	}
}

// FINDING 1. A certificate that is itself a trust anchor (the root, or a PIV attestation intermediate: both are public files) is accepted as
// the "slot certificate" with ANY signer, because the standard library takes a certificate that is in the roots as a chain of one: the
// signer is not part of the chain. The evidence then says Generated true (for a PIV intermediate), an Anchor, and the vendor's public key,
// and says nothing that tells it from the attestation of a key a card made. The same holds, with the root as the anchor, for the two
// intermediates the root signed directly. A caller that does not compare Evidence.PublicKey with the key it asked about (and the package
// leaves that to it) takes the vendor's public certificate for a card's attestation; no secret is needed to make the call.
//
// What a careful reader wants: the signer is part of the chain that was verified, so a certificate that is an anchor, or that the anchor
// signed without the signer, is refused (ErrChain). The old check accepts the same; making this change is a change of "exactly what the old
// check accepts" and so a decision of the record.
func TestFindingAnAnchorIsNotAnAttestationOfAKey(t *testing.T) {
	skipUnlessFindings(t)
	p := fixedPKI(t, attesttest.PKIOptions{})
	v := verifierOf(t, p)
	unrelated := fixedSlot(t, p, attesttest.SlotOptions{})
	for _, c := range []struct {
		name string
		cert func() (ev pivattest.Evidence, err error)
	}{
		{"the PIV intermediate as the slot certificate", func() (pivattest.Evidence, error) { return v.VerifyAt(t2030, p.PIV, unrelated) }},
		{"the root as the slot certificate", func() (pivattest.Evidence, error) { return v.VerifyAt(t2030, p.Root, unrelated) }},
	} {
		if ev, err := c.cert(); !errors.Is(err, pivattest.ErrChain) {
			t.Errorf("%s: accepted with the evidence %+v (error %v)", c.name, ev, err)
		}
	}
}
