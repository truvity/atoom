package pivattest_test

import (
	"os"
	"testing"

	"github.com/truvity/atoom/key/pivattest"
	"github.com/truvity/atoom/key/pivattest/attesttest"
)

// The corpus is committed data. This test makes a new one, only when ATOOM_PIVATTEST_WRITE_CORPUS names the file to write (the committed
// one is attesttest/corpus/corpus.json). It fills the results from THIS library and says so in the header, so the corpus is "not yet
// confirmed by the old check" until the differential test of the card driver (key/yubikeypiv/internal/card, build tag atoom_card) is run
// with ATOOM_PIVATTEST_RECORD and writes the results of the old check over them. Keys are generated, so a new corpus has new certificates:
// replace the results every time.
func TestWriteCorpus(t *testing.T) {
	path := os.Getenv("ATOOM_PIVATTEST_WRITE_CORPUS")
	if path == "" {
		t.Skip("set ATOOM_PIVATTEST_WRITE_CORPUS to the file to write")
	}
	c, err := attesttest.Build()
	if err != nil {
		t.Fatal(err)
	}
	vs := verifiers(t, c)
	for i, cs := range c.Cases {
		ev, err := vs[cs.PKI].VerifyDER(cs.Slot, cs.Signer)
		c.Cases[i].Want = resultOf(ev, err)
	}
	for i, k := range c.Constructions {
		_, err := pivattest.NewVerifierFromPEM([]byte(k.RootPEM), []byte(k.BundlePEM), k.WantRootSHA256)
		c.Constructions[i].WantOK, c.Constructions[i].WantClass = err == nil, classOf(err)
	}
	c.GoldenSource = "not yet confirmed by the old library: these results were given by key/pivattest itself; the differential test of key/yubikeypiv/internal/card (tag atoom_card, ATOOM_PIVATTEST_RECORD) replaces them with the old library's"
	if err := c.Write(path); err != nil {
		t.Fatal(err)
	}
}
