package signer_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"sync"
	"testing"

	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
	"github.com/truvity/atoom/key/purpose"
	"github.com/truvity/atoom/key/signature"
	"github.com/truvity/atoom/key/signer"
)

// ecTarget is a Signer whose store answers, to every Sign, with the bytes that the current iteration of the fuzz target sets.
type ecTarget struct {
	iteration sync.Mutex // one iteration at a time: it sets the answer, signs and checks
	mu        sync.Mutex // guards answer, which the handler of the fake reads
	answer    []byte
	s         *signer.Signer
	key       *testKey
	digest    []byte
}

func newECTarget(f *testing.F, alg string) *ecTarget {
	fk, k := newFake(f, alg)
	tg := &ecTarget{key: k, digest: digestOf(alg, "fuzz")}
	fk.setSign(func(context.Context, *keyv1alpha1.SignRequest) (*keyv1alpha1.SignResponse, error) {
		tg.mu.Lock()
		defer tg.mu.Unlock()
		return &keyv1alpha1.SignResponse{SignatureDer: tg.answer, KeyId: k.id}, nil
	})
	tg.s = newSigner(f, fk)
	return tg
}

// FuzzSignNeverReturnsAnECDSASignatureThatDoesNotVerify feeds arbitrary bytes to the Signer as the answer of the port for an ES256 and
// for an ES384 purpose. The rule: Sign returns a signature if and only if crypto/ecdsa verifies it, and then it returns those bytes; it
// returns nil and ErrVerify in every other case; it never panics. The seeds below run with plain go test.
func FuzzSignNeverReturnsAnECDSASignatureThatDoesNotVerify(f *testing.F) {
	targets := [2]*ecTarget{newECTarget(f, purpose.ES256), newECTarget(f, purpose.ES384)}
	algs := [2]string{purpose.ES256, purpose.ES384}

	for i, tg := range targets {
		good := tg.key.sign(f, tg.digest)
		raw, err := signature.DERToRaw(algs[i], good)
		if err != nil {
			f.Fatal(err)
		}
		other := newKey(f, algs[i], 1).sign(f, tg.digest)
		flipped := append([]byte(nil), good...)
		flipped[len(flipped)-1] ^= 1
		sel := byte(i)
		f.Add(sel, good)
		f.Add(sel, other)
		f.Add(sel, raw)
		f.Add(sel, flipped)
		f.Add(sel, good[:len(good)-1])
		f.Add(sel, append(append([]byte(nil), good...), 0))
		f.Add(sel, []byte{})
		f.Add(sel, []byte{0x30})
		f.Add(sel, []byte{0x30, 0x00})
		f.Add(sel, []byte{0x30, 0x06, 0x02, 0x01, 0x00, 0x02, 0x01, 0x00}) // r = s = 0
		f.Add(sel, []byte{0x30, 0x06, 0x02, 0x01, 0x01, 0x02, 0x01, 0x01}) // r = s = 1
		f.Add(sel, bytes.Repeat([]byte{0xff}, 72))
		f.Add(sel, make([]byte, 96))
	}
	// A signature of the other curve's size, and of a random string of the size of the right one.
	random := make([]byte, 70)
	if _, err := rand.Read(random); err != nil {
		f.Fatal(err)
	}
	f.Add(byte(0), random)
	f.Add(byte(1), random)

	f.Fuzz(func(t *testing.T, which byte, data []byte) {
		tg := targets[which%2]
		alg := algs[which%2]
		pub := tg.s.Public().(*ecdsa.PublicKey)

		tg.iteration.Lock()
		defer tg.iteration.Unlock()
		tg.mu.Lock()
		tg.answer = data
		tg.mu.Unlock()

		sig, err := tg.s.Sign(nil, tg.digest, hashOf(alg))
		if ecdsa.VerifyASN1(pub, tg.digest, data) {
			if err != nil || !bytes.Equal(sig, data) {
				t.Fatalf("%s: a signature that verifies was refused or changed: %v", alg, err)
			}
			return
		}
		if sig != nil {
			t.Fatalf("%s: Sign returned %x, which crypto/ecdsa does not verify", alg, sig)
		}
		refused(t, sig, err, signer.ErrVerify)
	})
}
