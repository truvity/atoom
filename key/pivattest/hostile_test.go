package pivattest_test

import (
	"bytes"
	"crypto/x509"
	"errors"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"

	"github.com/truvity/atoom/key/pivattest"
	"github.com/truvity/atoom/key/pivattest/attesttest"
)

// Hostile input: bytes that are not what the call asks for. Every call returns (no panic, no hang), an error comes with a zero evidence and
// is one of the named errors, and a verifier is built only when the files are what they should be.

var verifyErrors = []error{pivattest.ErrParse, pivattest.ErrChain, pivattest.ErrFirmwareExt, pivattest.ErrSerialExt, pivattest.ErrPolicyExt, pivattest.ErrFormFactorExt, pivattest.ErrNoAnchor, pivattest.ErrNil}

func namedVerifyError(err error) bool {
	for _, e := range verifyErrors {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// safeVerifyDER calls VerifyDER and reports a panic, an error that is not named, and an error that comes with evidence.
func safeVerifyDER(t testing.TB, v *pivattest.Verifier, a, b []byte) (ev pivattest.Evidence, err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("VerifyDER panicked on %d and %d bytes: %v", len(a), len(b), r)
		}
	}()
	ev, err = v.VerifyDER(a, b)
	if err != nil && (!namedVerifyError(err) || ev != (pivattest.Evidence{})) {
		t.Fatalf("VerifyDER gave %v with evidence %+v", err, ev)
	}
	return ev, err
}

func safeBuild(t testing.TB, root, bundle []byte, sum string) (v *pivattest.Verifier, err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NewVerifierFromPEM panicked on %d and %d bytes: %v", len(root), len(bundle), r)
		}
	}()
	v, err = pivattest.NewVerifierFromPEM(root, bundle, sum)
	if (err == nil) != (v != nil) {
		t.Fatalf("a verifier and an error: %v %v", v != nil, err)
	}
	if err != nil && !isNamed(err) {
		t.Fatalf("an error that is no named error: %v", err)
	}
	return v, err
}

func TestEveryTruncationAndExtensionOfADERCertificateIsAParseError(t *testing.T) {
	p := fixedPKI(t, attesttest.PKIOptions{})
	v := verifierOf(t, p)
	slot := fixedSlot(t, p, attesttest.SlotOptions{})
	if _, err := safeVerifyDER(t, v, slot.Raw, p.F9.Raw); err != nil && !errors.Is(err, pivattest.ErrChain) {
		t.Fatal(err) // the current time is inside the window (2010 to 2099) of the PKI
	}
	for i := 0; i < len(slot.Raw); i++ {
		if _, err := safeVerifyDER(t, v, slot.Raw[:i], p.F9.Raw); !errors.Is(err, pivattest.ErrParse) {
			t.Fatalf("a slot certificate cut to %d of %d bytes: %v", i, len(slot.Raw), err)
		}
	}
	for i := 0; i < len(p.F9.Raw); i++ {
		if _, err := safeVerifyDER(t, v, slot.Raw, p.F9.Raw[:i]); !errors.Is(err, pivattest.ErrParse) {
			t.Fatalf("a signer cut to %d of %d bytes: %v", i, len(p.F9.Raw), err)
		}
	}
	for _, extra := range [][]byte{{0}, {0x30, 0x00}, bytes.Repeat([]byte{0xff}, 1000)} {
		if _, err := safeVerifyDER(t, v, append(bytes.Clone(slot.Raw), extra...), p.F9.Raw); !errors.Is(err, pivattest.ErrParse) {
			t.Errorf("a slot certificate with %d bytes after it: %v", len(extra), err)
		}
		if _, err := safeVerifyDER(t, v, slot.Raw, append(bytes.Clone(p.F9.Raw), extra...)); !errors.Is(err, pivattest.ErrParse) {
			t.Errorf("a signer with %d bytes after it: %v", len(extra), err)
		}
	}
}

// Every byte of the slot certificate changed, one bit at a time: an answer that is not an error is the answer of the certificate as it was.
func TestEveryChangedByteOfASlotCertificateIsRefusedOrIsTheSameAnswer(t *testing.T) {
	p := fixedPKI(t, attesttest.PKIOptions{})
	v := verifierOf(t, p)
	slot := fixedSlot(t, p, attesttest.SlotOptions{})
	want, err := v.VerifyAt(t2030, slot, p.F9)
	if err != nil {
		t.Fatal(err)
	}
	for i := range slot.Raw {
		for _, bit := range []byte{0x01, 0x80} {
			der := bytes.Clone(slot.Raw)
			der[i] ^= bit
			c, err := x509.ParseCertificate(der)
			if err != nil {
				continue // refused at the parse, by the standard library: VerifyDER's case, above
			}
			ev, err := v.VerifyAt(t2030, c, p.F9)
			if err == nil && evidenceDiff(ev, want) != "" {
				t.Fatalf("byte %d bit %#x: accepted with other evidence: %s", i, bit, evidenceDiff(ev, want))
			}
			if err != nil && !namedVerifyError(err) {
				t.Fatalf("byte %d: %v", i, err)
			}
		}
	}
}

func TestRandomBytesAreAnErrorForBothEntries(t *testing.T) {
	p := fixedPKI(t, attesttest.PKIOptions{})
	v := verifierOf(t, p)
	rng := rand.NewChaCha8([32]byte{1, 2}) // a fixed seed: the same bytes on every run
	slot := fixedSlot(t, p, attesttest.SlotOptions{})
	length := func() int { return int(rng.Uint64() % 700) }
	noise := func(n int) []byte {
		b := make([]byte, n)
		_, _ = rng.Read(b)
		return b
	}
	for i := 0; i < 3000; i++ {
		a, b := noise(length()), noise(length())
		if i%3 == 0 && len(a) > 4 {
			a[0], a[1] = 0x30, 0x82 // the start of a certificate: the parser goes further than the first byte
		}
		if i%5 == 0 && len(b) > 4 {
			b[0], b[1] = 0x30, 0x82
		}
		if _, err := safeVerifyDER(t, v, a, b); err == nil {
			t.Fatalf("random bytes were accepted: %x %x", a, b)
		}
		// a good certificate with noise in the other place
		if _, err := safeVerifyDER(t, v, slot.Raw, a); err == nil {
			t.Fatalf("random bytes were accepted as the signer: %x", a)
		}
		if _, err := safeBuild(t, a, b, "00"); err == nil {
			t.Fatalf("random files built a verifier: %x %x", a, b)
		}
		if _, err := safeBuild(t, p.RootPEM(), b, p.RootSHA256()); err == nil {
			t.Fatalf("a random bundle built a verifier: %x", b)
		}
	}
}

// A PEM file cut at every length: the root file holds its one certificate when the block is whole, and none before; the bundle holds a
// PIV intermediate (here the last block) when that block is whole. Nothing between panics or builds a verifier from a part of a block.
func TestEveryTruncationOfAPEMFile(t *testing.T) {
	p := fixedPKI(t, attesttest.PKIOptions{})
	root, bundle, sum := p.RootPEM(), p.BundlePEM(), p.RootSHA256()
	endOf := func(b []byte, n int) int { // the offset after the end line of the n-th block (1-based)
		off := 0
		for i := 0; i < n; i++ {
			j := bytes.Index(b[off:], []byte("-----END CERTIFICATE-----"))
			off += j + len("-----END CERTIFICATE-----")
		}
		return off
	}
	whole := endOf(root, 1)
	for i := 0; i <= len(root); i++ {
		_, err := safeBuild(t, root[:i], bundle, sum)
		if (err == nil) != (i >= whole) || (err != nil && !errors.Is(err, pivattest.ErrRootCount)) {
			t.Fatalf("the root file cut to %d of %d bytes (whole from %d): %v", i, len(root), whole, err)
		}
	}
	pivWhole := endOf(bundle, 3)
	for i := 0; i <= len(bundle); i++ {
		_, err := safeBuild(t, root, bundle[:i], sum)
		if (err == nil) != (i >= pivWhole) || (err != nil && !errors.Is(err, pivattest.ErrNoAnchor)) {
			t.Fatalf("the bundle cut to %d of %d bytes (the PIV block whole from %d): %v", i, len(bundle), pivWhole, err)
		}
	}
	// the vendor's files: every length of the root file, and a stride of the bundle (the construction is slower)
	f := embedded(t)
	for i := 0; i < len(f.rootPEM)-1; i++ { // the last byte is the newline after the end line
		if v, err := pivattest.BuildFrom(f.rootPEM[:i], f.bundlePEM, pivattest.Pins()); v != nil || !errors.Is(err, pivattest.ErrRootCount) {
			t.Fatalf("the vendor's root file cut to %d bytes: %v", i, err)
		}
	}
	lastPinned := 0
	for _, pin := range pivattest.Pins() {
		for k, d := range f.bundleDER {
			if sha(d) == pin.SHA256 && k+1 > lastPinned {
				lastPinned = k + 1
			}
		}
	}
	needed := endOf(f.bundlePEM, lastPinned)
	for i := 0; i <= len(f.bundlePEM); i += 29 {
		v, err := pivattest.BuildFrom(f.rootPEM, f.bundlePEM[:i], pivattest.Pins())
		if (err == nil) != (i >= needed) || (err != nil && !isNamed(err)) || (err != nil && v != nil) {
			t.Fatalf("the vendor's bundle cut to %d of %d bytes (every pinned block whole from %d): %v", i, len(f.bundlePEM), needed, err)
		}
	}
}

func TestHugeInputsReturn(t *testing.T) {
	p := fixedPKI(t, attesttest.PKIOptions{})
	v := verifierOf(t, p)
	slot := fixedSlot(t, p, attesttest.SlotOptions{})
	const mib = 1 << 20
	huge := make([]byte, 64*mib)
	t.Run("a DER string of 64 MiB of zeros", func(t *testing.T) {
		if _, err := safeVerifyDER(t, v, huge, slot.Raw); !errors.Is(err, pivattest.ErrParse) {
			t.Fatal(err)
		}
		if _, err := safeVerifyDER(t, v, slot.Raw, huge); !errors.Is(err, pivattest.ErrParse) {
			t.Fatal(err)
		}
	})
	t.Run("a certificate followed by 32 MiB", func(t *testing.T) {
		if _, err := safeVerifyDER(t, v, append(bytes.Clone(slot.Raw), huge[:32*mib]...), p.F9.Raw); !errors.Is(err, pivattest.ErrParse) {
			t.Fatal(err)
		}
	})
	t.Run("a long header of a DER certificate", func(t *testing.T) {
		b := append([]byte{0x30, 0x84, 0x7f, 0xff, 0xff, 0xff}, huge[:16*mib]...)
		if _, err := safeVerifyDER(t, v, b, p.F9.Raw); !errors.Is(err, pivattest.ErrParse) {
			t.Fatal(err)
		}
	})
	t.Run("a PEM file of 64 MiB of text", func(t *testing.T) {
		text := bytes.Repeat([]byte("A"), 64*mib)
		if _, err := safeBuild(t, text, p.BundlePEM(), p.RootSHA256()); !errors.Is(err, pivattest.ErrRootCount) {
			t.Fatal(err)
		}
		if _, err := safeBuild(t, p.RootPEM(), text, p.RootSHA256()); !errors.Is(err, pivattest.ErrNoAnchor) {
			t.Fatal(err)
		}
	})
	t.Run("a block of 16 MiB of base64", func(t *testing.T) {
		b := []byte("-----BEGIN CERTIFICATE-----\n" + strings.Repeat("QUJD", 4*mib) + "\n-----END CERTIFICATE-----\n")
		if _, err := safeBuild(t, p.RootPEM(), b, p.RootSHA256()); !errors.Is(err, pivattest.ErrParse) {
			t.Fatal(err)
		}
		if _, err := safeBuild(t, b, p.BundlePEM(), p.RootSHA256()); !errors.Is(err, pivattest.ErrRootCount) {
			t.Fatal(err)
		}
	})
	t.Run("a bundle of 600 certificates, the PIV intermediate 300 times", func(t *testing.T) {
		b := bytes.Repeat(attesttest.PEM(p.Inter, p.PIV), 300)
		nv, err := safeBuild(t, p.RootPEM(), b, p.RootSHA256())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := nv.VerifyAt(t2030, slot, p.F9); err != nil {
			t.Fatal(err)
		}
	})
}

// The corpus as the seed of a fuzz target: both entries on every certificate of every case, in every pairing of the seeds.
var (
	fuzzCorpusOnce sync.Once
	fuzzVerifiers  []*pivattest.Verifier
)

func fuzzVerifierSet(f *testing.F) []*pivattest.Verifier {
	fuzzCorpusOnce.Do(func() {
		c, err := attesttest.Load()
		if err != nil {
			f.Fatal(err)
		}
		for _, name := range []string{"good", "rsa"} {
			p := c.PKIs[name]
			v, err := pivattest.NewVerifierFromPEM([]byte(p.RootPEM), []byte(p.BundlePEM), p.RootSHA256)
			if err != nil {
				f.Fatal(err)
			}
			fuzzVerifiers = append(fuzzVerifiers, v)
		}
	})
	return fuzzVerifiers
}

func FuzzVerifyDERCorpus(f *testing.F) {
	vs := fuzzVerifierSet(f)
	c, err := attesttest.Load()
	if err != nil {
		f.Fatal(err)
	}
	for _, cs := range c.Cases {
		f.Add(cs.Slot, cs.Signer, cs.PKI == "rsa")
	}
	f.Fuzz(func(t *testing.T, a, b []byte, second bool) {
		v := vs[0]
		if second {
			v = vs[1]
		}
		ev, err := safeVerifyDER(t, v, a, b)
		if err != nil {
			return
		}
		slot, perr := x509.ParseCertificate(a)
		if perr != nil {
			t.Fatalf("accepted a slot certificate that does not parse: %v", perr)
		}
		if _, perr := x509.ParseCertificate(b); perr != nil {
			t.Fatalf("accepted a signer that does not parse: %v", perr)
		}
		has := map[string]bool{}
		for _, e := range slot.Extensions {
			has[e.Id.String()] = true
		}
		if ev.HasFirmware != has[attesttest.OIDFirmware.String()] || ev.HasSerial != has[attesttest.OIDSerial.String()] ||
			ev.HasPolicy != has[attesttest.OIDPolicy.String()] || ev.HasFormFactor != has[attesttest.OIDFormFactor.String()] {
			t.Fatalf("the flags of %+v do not say which extensions the certificate holds", ev)
		}
		if ev.Anchor == "" || ev.Generated != strings.HasPrefix(ev.Anchor, "Yubico PIV Attestation ") || ev.HasSlot != (ev.Slot != 0) {
			t.Fatalf("an inconsistent evidence: %+v", ev)
		}
		if ev.HasPolicy && (ev.PIN < pivattest.PINNever || ev.PIN > pivattest.PINMatchAlways || ev.Touch < pivattest.TouchNever || ev.Touch > pivattest.TouchCached) {
			t.Fatalf("a policy out of range: %+v", ev)
		}
		if ev.Algorithm != independentAlgorithm(ev.PublicKey) {
			t.Fatalf("algorithm %v for a key of %T", ev.Algorithm, ev.PublicKey)
		}
	})
}

func FuzzNewVerifierFromPEMCorpus(f *testing.F) {
	c, err := attesttest.Load()
	if err != nil {
		f.Fatal(err)
	}
	for _, k := range c.Constructions {
		f.Add([]byte(k.RootPEM), []byte(k.BundlePEM), k.WantRootSHA256)
	}
	slot, signer := c.Cases[0].Slot, c.Cases[0].Signer
	f.Fuzz(func(t *testing.T, root, bundle []byte, sum string) {
		v, err := safeBuild(t, root, bundle, sum)
		if err != nil {
			return
		}
		// a verifier that was built is a verifier that can be used: it answers, whatever it answers
		if _, err := safeVerifyDER(t, v, slot, signer); err == nil {
			// accepted by a verifier built from the fuzzer's files: the files then hold the PKI of the case, and the root is the pinned one
			if !strings.Contains(string(root), "BEGIN CERTIFICATE") {
				t.Fatal("accepted with no root")
			}
		}
	})
}
