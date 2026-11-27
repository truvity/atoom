package pivattest_test

import (
	"crypto/x509"
	"errors"
	"testing"

	"github.com/truvity/atoom/key/pivattest"
	"github.com/truvity/atoom/key/pivattest/attesttest"
)

// classOf names the class of an error of the library in the words of the corpus.
func classOf(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, pivattest.ErrChain):
		return attesttest.ClassChain
	case errors.Is(err, pivattest.ErrFirmwareExt):
		return attesttest.ClassFirmware
	case errors.Is(err, pivattest.ErrSerialExt):
		return attesttest.ClassSerial
	case errors.Is(err, pivattest.ErrPolicyExt):
		return attesttest.ClassPolicy
	case errors.Is(err, pivattest.ErrFormFactorExt):
		return attesttest.ClassFormFactor
	case errors.Is(err, pivattest.ErrRootCount):
		return attesttest.ClassRootCount
	case errors.Is(err, pivattest.ErrRootPin):
		return attesttest.ClassRootPin
	case errors.Is(err, pivattest.ErrParse):
		return attesttest.ClassParse
	case errors.Is(err, pivattest.ErrNoAnchor):
		return attesttest.ClassNoAnchor
	}
	return "other: " + err.Error()
}

// resultOf is the library's answer in the form of a golden result.
func resultOf(ev pivattest.Evidence, err error) attesttest.Result {
	if err != nil {
		return attesttest.Result{Class: classOf(err)}
	}
	return attesttest.Result{
		OK: true, Slot: ev.Slot, Firmware: [3]int{ev.Firmware.Major, ev.Firmware.Minor, ev.Firmware.Patch}, Serial: ev.Serial,
		FormFactor: ev.FormFactor, PIN: int(ev.PIN), Touch: int(ev.Touch),
	}
}

// verifiers builds the verifier of each PKI of a corpus.
func verifiers(t *testing.T, c *attesttest.Corpus) map[string]*pivattest.Verifier {
	t.Helper()
	out := map[string]*pivattest.Verifier{}
	for name, p := range c.PKIs {
		v, err := pivattest.NewVerifierFromPEM([]byte(p.RootPEM), []byte(p.BundlePEM), p.RootSHA256)
		if err != nil {
			t.Fatalf("the PKI %q: %v", name, err)
		}
		out[name] = v
	}
	return out
}

func parse(t *testing.T, der []byte) *x509.Certificate {
	t.Helper()
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
