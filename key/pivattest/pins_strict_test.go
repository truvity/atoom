package pivattest_test

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/truvity/atoom/key/pivattest"
	"github.com/truvity/atoom/key/pivattest/attesttest"
)

// The pins: what Default trusts, what its construction refuses, and what NewVerifierFromPEM (which has no table) lets through.

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func derOf(t *testing.T, b []byte) [][]byte {
	t.Helper()
	var out [][]byte
	for {
		var blk *pem.Block
		blk, b = pem.Decode(b)
		if blk == nil {
			return out
		}
		out = append(out, blk.Bytes)
	}
}

func pemOf(ders ...[]byte) []byte {
	var out []byte
	for _, d := range ders {
		out = append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: d})...)
	}
	return out
}

func cnOf(t *testing.T, der []byte) string {
	t.Helper()
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c.Subject.CommonName
}

// vendor is the embedded files as the test reads them, with no help of the package.
type vendor struct {
	rootPEM, bundlePEM []byte
	rootDER            []byte
	bundleDER          [][]byte
}

func embedded(t *testing.T) vendor {
	t.Helper()
	r, b := pivattest.EmbeddedFiles()
	v := vendor{rootPEM: r, bundlePEM: b, bundleDER: derOf(t, b)}
	root := derOf(t, r)
	if len(root) != 1 {
		t.Fatalf("the root file holds %d certificates", len(root))
	}
	v.rootDER = root[0]
	return v
}

// without is the bundle with the certificates of the given names left out.
func (v vendor) without(t *testing.T, names ...string) []byte {
	t.Helper()
	var keep [][]byte
	for _, d := range v.bundleDER {
		if !slices.Contains(names, cnOf(t, d)) {
			keep = append(keep, d)
		}
	}
	return pemOf(keep...)
}

var namedErrors = []error{pivattest.ErrParse, pivattest.ErrPinTable, pivattest.ErrRootPin, pivattest.ErrRootCount, pivattest.ErrNoAnchor, pivattest.ErrAdmission}

func isNamed(err error) bool {
	for _, e := range namedErrors {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

func subjects(v *pivattest.Verifier) []string {
	var out []string
	for _, s := range pivattest.AnchorSubjects(v) {
		out = append(out, string(s))
	}
	slices.Sort(out)
	return out
}

func TestDefaultSucceedsAndNamesExactlyTheSixCertificates(t *testing.T) {
	v, err := pivattest.Default()
	if err != nil || v == nil {
		t.Fatalf("Default: %v", err)
	}
	if v2, err := pivattest.Default(); v2 != v || err != nil {
		t.Fatalf("a second call: %v %v", v2 == v, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if v3, err := pivattest.Default(); v3 != v || err != nil {
				t.Error("Default is not one verifier for every goroutine")
			}
		}()
	}
	wg.Wait()

	pins := pivattest.Pins()
	wantNames := []string{"Yubico Attestation Root 1", "Yubico Attestation Intermediate A 1", "Yubico Attestation Intermediate B 1", "Yubico PIV Attestation A 1",
		"Yubico PIV Attestation B 1", "Yubico PIV Attestation B2 1"}
	var names []string
	roles := map[pivattest.Role]int{}
	for _, p := range pins {
		names = append(names, p.Name)
		roles[p.Role]++
	}
	if !slices.Equal(names, wantNames) {
		t.Fatalf("the pins name %q", names)
	}
	if roles[pivattest.RoleRoot] != 1 || roles[pivattest.RoleIntermediate] != 2 || roles[pivattest.RolePIV] != 3 {
		t.Errorf("the roles of the pins: %v", roles)
	}
	f := embedded(t)
	all := append([][]byte{f.rootDER}, f.bundleDER...)
	if len(all) != 16 {
		t.Fatalf("the two files hold %d certificates, the page says 16", len(all))
	}
	byName := map[string][]string{}
	for _, d := range all {
		byName[cnOf(t, d)] = append(byName[cnOf(t, d)], sha(d))
	}
	for _, p := range pins {
		got := byName[p.Name]
		if len(got) != 1 || got[0] != p.SHA256 {
			t.Errorf("%s: the files hold %v, the pin is %s", p.Name, got, p.SHA256)
		}
		if len(p.SHA256) != 64 || strings.ToLower(p.SHA256) != p.SHA256 {
			t.Errorf("%s: the digest %q is not 64 characters of lower-case hexadecimal", p.Name, p.SHA256)
		}
	}
	// the anchors of Default are the root and the three PIV intermediates: nothing else of the bundle
	var want []string
	for _, d := range all {
		cn := cnOf(t, d)
		if cn == "Yubico Attestation Root 1" || strings.HasPrefix(cn, "Yubico PIV Attestation ") {
			c, _ := x509.ParseCertificate(d)
			want = append(want, string(c.RawSubject))
		}
	}
	slices.Sort(want)
	if len(want) != 4 || !slices.Equal(subjects(v), want) {
		t.Errorf("the anchors of Default are %d subjects, want the root and the three PIV intermediates (%d)", len(subjects(v)), len(want))
	}
}

// The files and the page agree: the sentences of docs/pins.md that a machine can check.
func TestThePinsPageHoldsTheDigestsAndTheDatesOfTheFiles(t *testing.T) {
	page, err := os.ReadFile("docs/pins.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(page)
	f := embedded(t)
	for name, b := range map[string][]byte{"the root file": f.rootPEM, "the bundle": f.bundlePEM} {
		if !strings.Contains(text, sha(b)) {
			t.Errorf("docs/pins.md has no SHA-256 for %s (%s)", name, sha(b))
		}
	}
	all := append([][]byte{f.rootDER}, f.bundleDER...)
	rows := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		if cells := strings.Split(line, "|"); strings.HasPrefix(line, "| Yubico ") && len(cells) >= 6 {
			rows[strings.TrimSpace(cells[1])] = line
		}
	}
	role := map[pivattest.Role]string{pivattest.RoleRoot: "root", pivattest.RoleIntermediate: "intermediate", pivattest.RolePIV: "piv-attestation"}
	for _, p := range pivattest.Pins() {
		line, ok := rows[p.Name]
		if !ok {
			t.Errorf("no row for %s", p.Name)
			continue
		}
		var cert *x509.Certificate
		for _, d := range all {
			if sha(d) == p.SHA256 {
				cert, _ = x509.ParseCertificate(d)
			}
		}
		cells := strings.Split(line, "|")
		if !strings.Contains(cells[2], role[p.Role]) {
			t.Errorf("%s: the page says the role is %q, the table says %q", p.Name, cells[2], role[p.Role])
		}
		if !strings.Contains(cells[3], p.SHA256) {
			t.Errorf("%s: the digest of the page is %q", p.Name, cells[3])
		}
		if from := cert.NotBefore.UTC().Format("2006-01-02 15:04:05"); strings.TrimSpace(cells[4]) != from {
			t.Errorf("%s: valid from %q on the page, %q in the certificate", p.Name, strings.TrimSpace(cells[4]), from)
		}
		if to := cert.NotAfter.UTC().Format("2006-01-02 15:04:05"); strings.TrimSpace(cells[5]) != to {
			t.Errorf("%s: valid to %q on the page, %q in the certificate", p.Name, strings.TrimSpace(cells[5]), to)
		}
	}
}

func TestAChangedByteOfAPinnedCertificateIsANamedError(t *testing.T) {
	f := embedded(t)
	table := pivattest.Pins()
	pinned := map[string]bool{}
	for _, p := range table {
		pinned[p.SHA256] = true
	}
	flipAt := func(der []byte, at int) []byte {
		out := slices.Clone(der)
		out[at] ^= 0x01
		return out
	}
	// the root: every byte
	for i := range f.rootDER {
		v, err := pivattest.BuildFrom(pemOf(flipAt(f.rootDER, i)), f.bundlePEM, table)
		if v != nil || !isNamed(err) {
			t.Fatalf("root, byte %d: %v (a verifier: %v)", i, err, v != nil)
		}
	}
	// the certificates of the bundle: a stride of the bytes of each pinned one (the others: a coarser one, and the table must hold)
	base, err := pivattest.Default()
	if err != nil {
		t.Fatal(err)
	}
	baseSubjects := subjects(base)
	for k, der := range f.bundleDER {
		stride := 3
		if !pinned[sha(der)] {
			stride = 41
		}
		for i := 0; i < len(der); i += stride {
			bundle := slices.Clone(f.bundleDER)
			bundle[k] = flipAt(der, i)
			v, err := pivattest.BuildFrom(f.rootPEM, pemOf(bundle...), table)
			if pinned[sha(der)] {
				if v != nil || !isNamed(err) {
					t.Fatalf("pinned certificate %s, byte %d: %v (a verifier: %v)", cnOf(t, der), i, err, v != nil)
				}
				continue
			}
			// a certificate the table does not name is not trusted by the package: a changed byte in one leaves the anchors as they were,
			// unless it no longer parses
			switch {
			case err == nil && !slices.Equal(subjects(v), baseSubjects):
				t.Fatalf("unpinned certificate %d, byte %d: the anchors changed", k, i)
			case err != nil && !errors.Is(err, pivattest.ErrParse) && !errors.Is(err, pivattest.ErrPinTable):
				t.Fatalf("unpinned certificate %d, byte %d: %v", k, i, err)
			}
		}
	}
}

// A block of the bundle whose text is damaged (a character that is not base64) is skipped by the PEM reader, as any text between blocks
// is. Default fails closed on it; NewVerifierFromPEM, which has no table to say what is missing, builds a verifier with one anchor less.
func TestADamagedPinnedBlockIsDroppedByTheReaderAndRefusedByTheTable(t *testing.T) {
	f := embedded(t)
	// damage the block of "Yubico PIV Attestation B2 1": find it
	var target int
	for k, d := range f.bundleDER {
		if cnOf(t, d) == "Yubico PIV Attestation B2 1" {
			target = k
		}
	}
	blocks := strings.SplitAfter(string(f.bundlePEM), "-----END CERTIFICATE-----\n")
	// a "!" at the start of the base64 body: not base64, so the block is not a block
	blocks[target] = strings.Replace(blocks[target], "-----BEGIN CERTIFICATE-----\n", "-----BEGIN CERTIFICATE-----\n!", 1)
	bundle := []byte(strings.Join(blocks, ""))
	if got := len(derOf(t, bundle)); got != len(f.bundleDER)-1 {
		t.Fatalf("the reader kept %d blocks of %d: the damage did not remove one", got, len(f.bundleDER))
	}
	if v, err := pivattest.BuildFrom(f.rootPEM, bundle, pivattest.Pins()); !errors.Is(err, pivattest.ErrPinTable) || v != nil {
		t.Errorf("BuildFrom: %v", err)
	}
	v, err := pivattest.NewVerifierFromPEM(f.rootPEM, bundle, sha(f.rootDER))
	if err != nil {
		t.Fatalf("NewVerifierFromPEM: %v", err)
	}
	if n := len(subjects(v)); n != 3 {
		t.Errorf("%d anchors, want the root and two PIV intermediates", n)
	}
}

func TestTheRootFileOfDefaultHoldsExactlyOneCertificate(t *testing.T) {
	f := embedded(t)
	other, err := attesttest.NewPKI(attesttest.PKIOptions{})
	if err != nil {
		t.Fatal(err)
	}
	junk := pem.EncodeToMemory(&pem.Block{Type: "EXAMPLE DATA", Bytes: []byte{1}})
	tests := []struct {
		name string
		root []byte
		want error
	}{
		{"no certificate", nil, pivattest.ErrRootCount},
		{"text only", []byte("not a pem file\n"), pivattest.ErrRootCount},
		{"a block that is not a certificate", junk, pivattest.ErrRootCount},
		{"two certificates, the root twice", append(slices.Clone(f.rootPEM), f.rootPEM...), pivattest.ErrRootCount},
		{"two certificates, the root and an intermediate", pemOf(f.rootDER, f.bundleDER[0]), pivattest.ErrRootCount},
		{"two certificates, an intermediate first", pemOf(f.bundleDER[0], f.rootDER), pivattest.ErrRootCount},
		{"a certificate that does not parse", pemOf([]byte{1, 2, 3}), pivattest.ErrRootCount},
		{"the root and one that does not parse", append(slices.Clone(f.rootPEM), pemOf([]byte{1, 2, 3})...), pivattest.ErrRootCount},
		{"another certificate", attesttest.PEM(other.Root), pivattest.ErrRootPin},
		{"an intermediate of the vendor", pemOf(f.bundleDER[0]), pivattest.ErrRootPin},
		{"the root with text before and after, and a key block", append(append([]byte("text\n"), junk...), append(slices.Clone(f.rootPEM), []byte("\ntrailing text\n")...)...), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := pivattest.BuildFrom(tt.root, f.bundlePEM, pivattest.Pins())
			v2, err2 := pivattest.NewVerifierFromPEM(tt.root, f.bundlePEM, sha(f.rootDER))
			if tt.want == nil {
				if err != nil || v == nil || err2 != nil || v2 == nil {
					t.Fatalf("refused: %v %v", err, err2)
				}
				return
			}
			if !errors.Is(err, tt.want) || v != nil || !errors.Is(err2, tt.want) || v2 != nil {
				t.Fatalf("got %v and %v, want %v", err, err2, tt.want)
			}
		})
	}
}

func TestTheBundleOfDefaultNeedsItsPIVIntermediates(t *testing.T) {
	f := embedded(t)
	tests := []struct {
		name   string
		bundle []byte
		want   error // from BuildFrom
		fromV  error // from NewVerifierFromPEM over the vendor's root
	}{
		{"no PIV intermediate", f.without(t, "Yubico PIV Attestation A 1", "Yubico PIV Attestation B 1", "Yubico PIV Attestation B2 1"), pivattest.ErrNoAnchor, pivattest.ErrNoAnchor},
		{"an empty bundle", nil, pivattest.ErrNoAnchor, pivattest.ErrNoAnchor},
		{"a bundle of text", []byte("none"), pivattest.ErrNoAnchor, pivattest.ErrNoAnchor},
		{"the PIV intermediates without the intermediates that link them", f.without(t, "Yubico Attestation Intermediate A 1", "Yubico Attestation Intermediate B 1"), pivattest.ErrNoAnchor, pivattest.ErrNoAnchor},
		// one of three missing: the table refuses, a verifier with no table does not know
		{"the newest PIV intermediate is missing", f.without(t, "Yubico PIV Attestation B2 1"), pivattest.ErrPinTable, nil},
		{"the intermediate B is missing (B and B2 cannot be linked)", f.without(t, "Yubico Attestation Intermediate B 1"), pivattest.ErrPinTable, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if v, err := pivattest.BuildFrom(f.rootPEM, tt.bundle, pivattest.Pins()); !errors.Is(err, tt.want) || v != nil {
				t.Errorf("BuildFrom: %v", err)
			}
			v, err := pivattest.NewVerifierFromPEM(f.rootPEM, tt.bundle, sha(f.rootDER))
			if tt.fromV == nil {
				if err != nil || v == nil {
					t.Errorf("NewVerifierFromPEM: %v", err)
				}
			} else if !errors.Is(err, tt.fromV) || v != nil {
				t.Errorf("NewVerifierFromPEM: %v", err)
			}
		})
	}
}

func TestTheDigestOfTheRootIsComparedExactly(t *testing.T) {
	f := embedded(t)
	good := sha(f.rootDER)
	for name, want := range map[string]string{
		"the right one in capitals": strings.ToUpper(good), "empty": "", "one character short": good[:63], "with a space": good + " ", "a digest of the bundle": sha(f.bundlePEM),
		"the digest of an intermediate": sha(f.bundleDER[0]), "a prefix": good[:16], "with the prefix sha256:": "sha256:" + good,
	} {
		v, err := pivattest.NewVerifierFromPEM(f.rootPEM, f.bundlePEM, want)
		if !errors.Is(err, pivattest.ErrRootPin) || v != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if v, err := pivattest.NewVerifierFromPEM(f.rootPEM, f.bundlePEM, good); err != nil || v == nil {
		t.Fatalf("the right digest: %v", err)
	}
	// the table: a root row that is not the root, no root row, no rows
	table := pivattest.Pins()
	wrong := slices.Clone(table)
	wrong[0].SHA256 = table[1].SHA256
	for name, tb := range map[string][]pivattest.Pin{"a root row of another certificate": wrong, "no root row": table[1:], "no rows": nil} {
		if v, err := pivattest.BuildFrom(f.rootPEM, f.bundlePEM, tb); !errors.Is(err, pivattest.ErrRootPin) || v != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Trailing garbage, garbage before and between the blocks, and an unfinished block at the end are skipped by the PEM reader, in both files
// of Default and in the bundle of NewVerifierFromPEM. A complete table-and-files construction still holds.
func TestGarbageAroundThePEMIsIgnoredAndTheTableStillHolds(t *testing.T) {
	f := embedded(t)
	half := len(f.bundleDER) / 2
	between := append(append(pemOf(f.bundleDER[:half]...), []byte("\n# a comment between two blocks\n")...), pemOf(f.bundleDER[half:]...)...)
	for name, tt := range map[string]struct{ root, bundle []byte }{
		"trailing text":                  {append(slices.Clone(f.rootPEM), "trailing text\n"...), append(slices.Clone(f.bundlePEM), "trailing text\n"...)},
		"trailing zero bytes":            {append(slices.Clone(f.rootPEM), make([]byte, 64)...), append(slices.Clone(f.bundlePEM), make([]byte, 64)...)},
		"an unfinished block at the end": {append(slices.Clone(f.rootPEM), "-----BEGIN CERTIFICATE-----\nMIIB"...), append(slices.Clone(f.bundlePEM), "-----BEGIN CERTIFICATE-----\nMIIB"...)},
		"text before":                    {append([]byte("a note\n"), f.rootPEM...), append([]byte("a note\n"), f.bundlePEM...)},
		"text between the blocks":        {f.rootPEM, between},
		"carriage returns":               {[]byte(strings.ReplaceAll(string(f.rootPEM), "\n", "\r\n")), []byte(strings.ReplaceAll(string(f.bundlePEM), "\n", "\r\n"))},
	} {
		v, err := pivattest.BuildFrom(tt.root, tt.bundle, pivattest.Pins())
		if err != nil || v == nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// ---- an extra PIV-named certificate ----

// A PIV-named certificate that chains to the root and that the table does not name is trusted by NewVerifierFromPEM (it has no table: the
// admission rule is "named like a PIV intermediate and chained to the root"), and refused by the construction that has a table.
func TestAnExtraPIVNamedCertificateIsRefusedByTheTableAndAdmittedByAVerifierWithNone(t *testing.T) {
	p := newOwnPKI(t)
	rootSHA := attesttest.SHA256(p.root)
	table := []pivattest.Pin{
		{Name: p.root.Subject.CommonName, Role: pivattest.RoleRoot, SHA256: rootSHA},
		{Name: p.piv.Subject.CommonName, Role: pivattest.RolePIV, SHA256: attesttest.SHA256(p.piv)},
	}
	rootPEM := attesttest.PEM(p.root)
	if v, err := pivattest.BuildFrom(rootPEM, attesttest.PEM(p.piv), table); err != nil || v == nil {
		t.Fatalf("the baseline: %v", err)
	}
	extra, extraKey := p.extraPIV(t, "Yubico PIV Attestation TEST ONLY Extra")
	bundle := attesttest.PEM(p.piv, extra)

	v, err := pivattest.BuildFrom(rootPEM, bundle, table)
	if !errors.Is(err, pivattest.ErrAdmission) || v != nil {
		t.Errorf("an extra PIV-named certificate that chains to the root: %v", err)
	}
	// with the same name as a pinned one: a name twice in the files
	twin, _ := p.extraPIV(t, p.piv.Subject.CommonName)
	if v, err := pivattest.BuildFrom(rootPEM, attesttest.PEM(p.piv, twin), table); !errors.Is(err, pivattest.ErrPinTable) || v != nil {
		t.Errorf("an extra certificate with the name of a pinned one: %v", err)
	}
	// the verifier that has no table admits it, silently: that is what "the admission rule" says, and a caller of NewVerifierFromPEM is the
	// one who chooses the bundle
	nv, err := pivattest.NewVerifierFromPEM(rootPEM, bundle, rootSHA)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(pivattest.AnchorSubjects(nv)); n != 3 {
		t.Fatalf("%d anchors, want the root and both PIV certificates", n)
	}
	// and a chain under the extra certificate is accepted by it, with the extra certificate as the anchor
	k, err := newKey()
	if err != nil {
		t.Fatal(err)
	}
	f9 := ownCert(t, ownCA("TEST ONLY YubiKey PIV Attestation F9"), extra, &k.PublicKey, extraKey)
	c := &ownCard{f9: f9, key: k}
	slot := c.slot(t, "YubiKey PIV Attestation 9a")
	ev, err := nv.VerifyAt(t2030, slot, f9)
	if err != nil || ev.Anchor != "Yubico PIV Attestation TEST ONLY Extra" || !ev.Generated {
		t.Fatalf("a chain under the extra certificate: %+v, %v", ev, err)
	}
	// the same chain is not accepted by a verifier that was built without the extra certificate
	ov := p.verifier(t)
	if _, err := ov.VerifyAt(t2030, slot, f9); !errors.Is(err, pivattest.ErrChain) {
		t.Errorf("the verifier without the extra certificate: %v", err)
	}
	// a PIV-named certificate that does NOT chain to the root is not admitted, and is no error: the table holds, nothing is added
	stranger := newOwnPKI(t)
	strangerPIV, _ := stranger.extraPIV(t, "Yubico PIV Attestation TEST ONLY Stranger")
	sv, err := pivattest.BuildFrom(rootPEM, attesttest.PEM(p.piv, strangerPIV), table)
	if err != nil || sv == nil {
		t.Fatalf("a PIV-named certificate of another root: %v", err)
	}
	if n := len(pivattest.AnchorSubjects(sv)); n != 2 {
		t.Errorf("%d anchors, want the root and the one PIV certificate", n)
	}
	// a table that names the extra certificate as an intermediate (not an anchor) while the files admit it
	table2 := append(slices.Clone(table), pivattest.Pin{Name: extra.Subject.CommonName, Role: pivattest.RoleIntermediate, SHA256: attesttest.SHA256(extra)})
	if v, err := pivattest.BuildFrom(rootPEM, bundle, table2); !errors.Is(err, pivattest.ErrAdmission) || v != nil {
		t.Errorf("a table that names an admitted certificate as no anchor: %v", err)
	}
}

// ---- a chain of the test-support package is refused by Default ----

func TestNoChainOfTheTestPackageIsAcceptedByDefault(t *testing.T) {
	def, err := pivattest.Default()
	if err != nil {
		t.Fatal(err)
	}
	// every case of the corpus, whatever its golden answer: Default refuses it, as a chain error
	c := loadCorpus(t)
	for _, cs := range c.Cases {
		ev, err := def.VerifyAt(c.At, parse(t, cs.Slot), parse(t, cs.Signer))
		if err == nil {
			t.Errorf("Default accepted the case %q: %+v", cs.Name, ev)
		} else if !errors.Is(err, pivattest.ErrChain) {
			t.Errorf("%s: %v", cs.Name, err)
		}
	}
	// a PKI made now, for the time of the vendor's certificates, with the names of the vendor's PIV intermediate
	for _, o := range []attesttest.PKIOptions{
		{}, {RSA: true}, {PIVName: "Yubico PIV Attestation B2 1"}, {PIVName: "Yubico PIV Attestation A 1"}, {F9: attesttest.F9Options{SignedBy: "root"}},
		{PIVSignedByRoot: true, PIVName: "Yubico PIV Attestation B 1"},
	} {
		p := fixedPKI(t, o)
		slot := fixedSlot(t, p, attesttest.SlotOptions{})
		if ev, err := def.VerifyAt(t2030, slot, p.F9); !errors.Is(err, pivattest.ErrChain) {
			t.Errorf("%+v: Default gave %+v, %v", o, ev, err)
		}
	}
}

// The admission rule is the exact prefix "Yubico PIV Attestation " (with its trailing space, in this case): a name that is near it makes no
// anchor.
func TestOnlyTheExactPrefixAdmitsAnAnchor(t *testing.T) {
	for name, ok := range map[string]bool{
		"Yubico PIV Attestation X": true, "Yubico PIV Attestation  X": true, "Yubico PIV Attestation ": true,
		"Yubico PIV Attestation": false, "Yubico PIV AttestationX": false, "yubico piv attestation x": false, " Yubico PIV Attestation X": false,
		"Yubico  PIV Attestation X": false, "Yubico FIDO Attestation X": false, "X Yubico PIV Attestation ": false, "YubiKey PIV Attestation 9a": false,
	} {
		p := fixedPKI(t, attesttest.PKIOptions{PIVName: name})
		v, err := pivattest.NewVerifierFromPEM(p.RootPEM(), p.BundlePEM(), p.RootSHA256())
		switch {
		case ok && err != nil:
			t.Errorf("%q is not admitted: %v", name, err)
		case !ok && !errors.Is(err, pivattest.ErrNoAnchor):
			t.Errorf("%q is admitted (%v)", name, err)
		case ok:
			ev, err := v.VerifyAt(t2030, fixedSlot(t, p, attesttest.SlotOptions{}), p.F9)
			if err != nil || !ev.Generated || ev.Anchor != name {
				t.Errorf("%q: %+v, %v", name, ev, err)
			}
		}
	}
}
