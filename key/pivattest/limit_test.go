package pivattest_test

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"testing"

	"github.com/truvity/atoom/key/pivattest"
	"github.com/truvity/atoom/key/pivattest/attesttest"
)

// Difference 6: a certificate longer than MaxCertificateSize is refused before it is parsed, on the one path every entry shares. The tests make
// valid certificates of exactly the limit and of one byte more, so that a refusal is the length and nothing else.

var oidFiller = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1}

// sized makes a certificate of exactly n bytes: it adds an extension of filler bytes and makes the certificate again until its length is n (the
// length of an ECDSA signature changes by a byte or two from one signature to the next).
func sized(t *testing.T, n int, build func(extra pkix.Extension) ([]byte, error)) []byte {
	t.Helper()
	fill := n - 1000
	for try := 0; try < 400; try++ {
		der, err := build(attesttest.Ext(oidFiller, false, make([]byte, fill)))
		if err != nil {
			t.Fatal(err)
		}
		if len(der) == n {
			return der
		}
		fill += n - len(der)
	}
	t.Fatalf("no certificate of %d bytes in 400 tries", n)
	return nil
}

func slotOf(t *testing.T, p *attesttest.PKI, n int) []byte {
	t.Helper()
	return sized(t, n, func(extra pkix.Extension) ([]byte, error) {
		return p.SlotDER(attesttest.SlotOptions{
			Extensions: append(attesttest.DefaultExtensions(), extra), NotBefore: t2010, NotAfter: t2099,
		})
	})
}

func f9Of(t *testing.T, p *attesttest.PKI, n int) []byte {
	t.Helper()
	return sized(t, n, func(extra pkix.Extension) ([]byte, error) {
		q, err := p.WithF9(attesttest.F9Options{Extensions: []pkix.Extension{extra}, NotBefore: t2010, NotAfter: t2099})
		if err != nil {
			return nil, err
		}
		return q.F9.Raw, nil
	})
}

func TestTheLimitIsSixteenKiB(t *testing.T) {
	if pivattest.MaxCertificateSize != 16*1024 {
		t.Fatalf("the limit is %d", pivattest.MaxCertificateSize)
	}
}

func TestACertificateOfTheLimitIsReadAndOneByteMoreIsRefused(t *testing.T) {
	const limit = pivattest.MaxCertificateSize
	p := fixedPKI(t, attesttest.PKIOptions{})
	v := verifierOf(t, p)
	good := slotOf(t, p, limit)

	// the signer of the limit: the same card key under another certificate of its own, so that the slot certificate chains through it
	bigF9 := f9Of(t, p, limit)
	q := *p
	q.F9 = parse(t, bigF9)
	slotUnderBig, err := q.SlotDER(attesttest.SlotOptions{NotBefore: t2010, NotAfter: t2099})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("the slot certificate of exactly the limit is accepted by every entry", func(t *testing.T) {
		if len(good) != limit {
			t.Fatalf("the certificate has %d bytes", len(good))
		}
		if ev, err := v.VerifyAt(t2030, parse(t, good), p.F9); err != nil || !ev.Generated {
			t.Errorf("VerifyAt: %+v, %v", ev, err)
		}
		if ev, err := v.VerifyDER(good, p.F9.Raw); err != nil || !ev.Generated {
			t.Errorf("VerifyDER: %+v, %v", ev, err)
		}
		if ev, err := v.Verify(parse(t, good), p.F9); err != nil || !ev.Generated {
			t.Errorf("Verify: %+v, %v", ev, err)
		}
	})
	t.Run("the signer of exactly the limit is accepted by every entry", func(t *testing.T) {
		if len(bigF9) != limit {
			t.Fatalf("the signer has %d bytes", len(bigF9))
		}
		if ev, err := v.VerifyAt(t2030, parse(t, slotUnderBig), q.F9); err != nil || !ev.Generated {
			t.Errorf("VerifyAt: %+v, %v", ev, err)
		}
		if ev, err := v.VerifyDER(slotUnderBig, bigF9); err != nil || !ev.Generated {
			t.Errorf("VerifyDER: %+v, %v", ev, err)
		}
	})
	t.Run("the slot certificate one byte over is refused by every entry, and is not parsed", func(t *testing.T) {
		over := slotOf(t, p, limit+1)
		if len(over) != limit+1 {
			t.Fatalf("the certificate has %d bytes", len(over))
		}
		roots, inter := x509.NewCertPool(), x509.NewCertPool()
		roots.AddCert(p.PIV)
		inter.AddCert(p.F9)
		if _, err := parse(t, over).Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, CurrentTime: t2030, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
			t.Fatalf("the certificate is not valid in itself, so the test proves nothing: %v", err)
		}
		for name, do := range map[string]func() (pivattest.Evidence, error){
			"Verify":    func() (pivattest.Evidence, error) { return v.Verify(parse(t, over), p.F9) },
			"VerifyAt":  func() (pivattest.Evidence, error) { return v.VerifyAt(t2030, parse(t, over), p.F9) },
			"VerifyDER": func() (pivattest.Evidence, error) { return v.VerifyDER(over, p.F9.Raw) },
		} {
			ev, err := do()
			if !errors.Is(err, pivattest.ErrTooLarge) || ev != (pivattest.Evidence{}) {
				t.Errorf("%s: %+v, %v", name, ev, err)
			}
			if errors.Is(err, pivattest.ErrParse) || errors.Is(err, pivattest.ErrChain) {
				t.Errorf("%s: the error is also another class: %v", name, err)
			}
		}
	})
	t.Run("the signer one byte over is refused by every entry", func(t *testing.T) {
		over := f9Of(t, p, limit+1)
		if len(over) != limit+1 {
			t.Fatalf("the certificate has %d bytes", len(over))
		}
		for name, do := range map[string]func() (pivattest.Evidence, error){
			"Verify":    func() (pivattest.Evidence, error) { return v.Verify(parse(t, good), parse(t, over)) },
			"VerifyAt":  func() (pivattest.Evidence, error) { return v.VerifyAt(t2030, parse(t, good), parse(t, over)) },
			"VerifyDER": func() (pivattest.Evidence, error) { return v.VerifyDER(good, over) },
		} {
			if ev, err := do(); !errors.Is(err, pivattest.ErrTooLarge) || ev != (pivattest.Evidence{}) {
				t.Errorf("%s: %+v, %v", name, ev, err)
			}
		}
	})
	t.Run("a certificate that does not parse and is over the limit is a size error, and under it a parse error", func(t *testing.T) {
		if _, err := v.VerifyDER(make([]byte, limit+1), p.F9.Raw); !errors.Is(err, pivattest.ErrTooLarge) {
			t.Errorf("over: %v", err)
		}
		if _, err := v.VerifyDER(make([]byte, limit), p.F9.Raw); !errors.Is(err, pivattest.ErrParse) {
			t.Errorf("at: %v", err)
		}
	})
}
