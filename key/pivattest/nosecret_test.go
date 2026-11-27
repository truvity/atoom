package pivattest_test

import (
	"crypto/x509"
	"encoding/pem"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A secret scanner flags the armor of a private key of any form ("PRIVATE KEY", "RSA PRIVATE KEY", "ENCRYPTED PRIVATE KEY", "PGP PRIVATE KEY BLOCK"),
// even one that holds three bytes of nothing. This repository is public: no file of this module may hold such an armor line, except a source file
// that makes keys at test time (it may spell the name of a block it writes). The pattern is built from two parts so that this file does not match
// itself.
var qpPrivateArmor = regexp.MustCompile(`BEGIN[ A-Z0-9_-]{0,100}` + "PRIVATE" + ` KEY`)

// qpScanModule lists the files under dir (the module of the test) that hold the armor of a private key and are not a source file that makes keys.
func qpScanModule(t *testing.T, dir string) (scanned int, flagged []string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(path) //nolint:gosec // a test reads the files of its own module
		if err != nil {
			return err
		}
		scanned++
		makesKeys := strings.HasSuffix(path, ".go") && strings.Contains(string(b), "GenerateKey")
		if qpPrivateArmor.Match(b) && !makesKeys {
			flagged = append(flagged, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return scanned, flagged
}

func TestQPNoFileOfTheModuleHoldsTheArmorOfAPrivateKey(t *testing.T) {
	scanned, flagged := qpScanModule(t, ".")
	if scanned < 30 {
		t.Fatalf("only %d files scanned", scanned)
	}
	for _, f := range flagged {
		t.Errorf("%s holds the armor of a private key, which a secret scanner flags", f)
	}
	// the scan is held to a file that does hold one, and to a source file that makes keys
	dir := t.TempDir()
	bad := "-----BEGIN " + "PRIVATE" + " KEY-----\nAQID\n-----END " + "PRIVATE" + " KEY-----\n"
	for name, body := range map[string]string{"data.json": bad, "page.md": "x\n-----BEGIN RSA " + "PRIVATE" + " KEY-----\n", "maker.go": "package p\n// GenerateKey\nvar x = \"" + bad + "\"\n", "plain.go": "package p\nvar x = \"" + bad + "\"\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, got := qpScanModule(t, dir)
	if len(got) != 3 {
		t.Errorf("the scan flagged %v, want the three files that are not a source file that makes keys", got)
	}
}

// The PEM blocks of the corpus are certificates, or a block of a kind that is no key: no block holds anything that parses as a private key.
func TestQPTheBlocksOfTheCorpusAreNoKeys(t *testing.T) {
	c := loadCorpus(t)
	kinds := map[string]int{}
	check := func(what, text string) {
		rest := []byte(text)
		for {
			var blk *pem.Block
			blk, rest = pem.Decode(rest)
			if blk == nil {
				return
			}
			kinds[blk.Type]++
			if blk.Type != "CERTIFICATE" && blk.Type != "EXAMPLE DATA" {
				t.Errorf("%s: a PEM block of the type %q", what, blk.Type)
			}
			if _, err := x509.ParsePKCS8PrivateKey(blk.Bytes); err == nil {
				t.Errorf("%s: a block that parses as a PKCS#8 private key", what)
			}
			if _, err := x509.ParseECPrivateKey(blk.Bytes); err == nil {
				t.Errorf("%s: a block that parses as an EC private key", what)
			}
			if _, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
				t.Errorf("%s: a block that parses as an RSA private key", what)
			}
		}
	}
	for name, p := range c.PKIs {
		check("the PKI "+name, p.RootPEM+p.BundlePEM)
	}
	for _, k := range c.Constructions {
		check("the construction "+k.Name, k.RootPEM+k.BundlePEM)
	}
	if kinds["CERTIFICATE"] == 0 || kinds["EXAMPLE DATA"] == 0 {
		t.Errorf("the blocks of the corpus: %v", kinds)
	}
}
