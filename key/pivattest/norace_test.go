//go:build !race

package pivattest_test

import (
	"bytes"
	"crypto/ed25519"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/truvity/atoom/key/pivattest/attesttest"
)

// A caller that writes its bytes while VerifyDER runs commits a data race of its own, which the race detector would report; this test is for
// the build without it. The call copies both inputs first, so what it reads is one set of bytes: an answer is either the answer of the
// certificate as it was (the same evidence, the key the same) or an error, and never an evidence made of two states.
func TestVerifyDERWhileTheCallerWritesTheBytes(t *testing.T) {
	p := fixedPKI(t, attesttest.PKIOptions{})
	v := verifierOf(t, p)
	pub := slotKeys(t)["ed25519"]
	slot, err := p.SlotDER(attesttest.SlotOptions{PublicKey: pub, NotBefore: t2010, NotAfter: t2099})
	if err != nil {
		t.Fatal(err)
	}
	signer := bytes.Clone(p.F9.Raw)
	want, err := v.VerifyDER(slot, signer)
	if err != nil {
		t.Fatal(err)
	}
	// the writer flips a byte of the Ed25519 key inside the slot certificate and a byte of the signature of the signer, back and forth
	at := bytes.Index(slot, []byte(pub.(ed25519.PublicKey)))
	if at < 0 {
		t.Fatal("the key is not in the certificate")
	}
	var stop atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			slot[at+3] ^= 0xff
			signer[len(signer)-5] ^= 0xff
		}
	}()
	for i := 0; i < 2000; i++ {
		ev, err := v.VerifyDER(slot, signer)
		if err != nil {
			if !namedVerifyError(err) {
				t.Errorf("an error that is not named: %v", err)
			}
			continue
		}
		if d := evidenceDiff(ev, want); d != "" {
			t.Errorf("an evidence made of another state of the bytes: %s", d)
			break
		}
	}
	stop.Store(true)
	wg.Wait()
}
