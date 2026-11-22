package wrap_test

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"testing"

	"github.com/truvity/atoom/blocks/seal/internal/wrap"
)

func ctx() wrap.Context {
	return wrap.Context{Domain: "atoom-wrap-v1", Installation: "dev", Scope: "acme", Purpose: "data", Generation: 1, Recipient: "node-1-abcdefghijklmnop"}
}

func TestWrapRoundTrip(t *testing.T) {
	recipient, _ := ecdh.P384().GenerateKey(rand.Reader)
	dk := bytes.Repeat([]byte{7}, 32)
	eph, wrapped, err := wrap.Wrap(ctx(), recipient.PublicKey(), dk)
	if err != nil {
		t.Fatal(err)
	}
	if len(eph) != 97 || eph[0] != 4 || len(wrapped) != 40 {
		t.Fatalf("ephemeral %d bytes, wrapped %d bytes", len(eph), len(wrapped))
	}
	peer, err := wrap.ParsePublic(eph)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := recipient.ECDH(peer) // the key store does this step
	if err != nil {
		t.Fatal(err)
	}
	got, err := wrap.Unwrap(ctx(), secret, wrapped)
	if err != nil || !bytes.Equal(got, dk) {
		t.Fatalf("Unwrap = %x, %v", got, err)
	}
	// The wrap is bound to its context: each field changes the key-encryption key.
	for name, c := range map[string]wrap.Context{
		"installation": {Domain: "atoom-wrap-v1", Installation: "prod", Scope: "acme", Purpose: "data", Generation: 1, Recipient: ctx().Recipient},
		"scope":        {Domain: "atoom-wrap-v1", Installation: "dev", Scope: "globex", Purpose: "data", Generation: 1, Recipient: ctx().Recipient},
		"purpose":      {Domain: "atoom-wrap-v1", Installation: "dev", Scope: "acme", Purpose: "evidence", Generation: 1, Recipient: ctx().Recipient},
		"generation":   {Domain: "atoom-wrap-v1", Installation: "dev", Scope: "acme", Purpose: "data", Generation: 2, Recipient: ctx().Recipient},
		"domain":       {Domain: "sluis-wrap-v1", Installation: "dev", Scope: "acme", Purpose: "data", Generation: 1, Recipient: ctx().Recipient},
		"recipient":    {Domain: "atoom-wrap-v1", Installation: "dev", Scope: "acme", Purpose: "data", Generation: 1, Recipient: "node-2-abcdefghijklmnop"},
	} {
		if _, err := wrap.Unwrap(c, secret, wrapped); err == nil {
			t.Errorf("unwrapped under another %s", name)
		}
	}
	other, _ := ecdh.P384().GenerateKey(rand.Reader)
	wrong, _ := other.ECDH(peer)
	if _, err := wrap.Unwrap(ctx(), wrong, wrapped); err == nil {
		t.Error("unwrapped with another recipient's secret")
	}
}

func TestWrapRefusesAShortKey(t *testing.T) {
	recipient, _ := ecdh.P384().GenerateKey(rand.Reader)
	if _, _, err := wrap.Wrap(ctx(), recipient.PublicKey(), make([]byte, 16)); err == nil {
		t.Fatal("a 16-byte data key was wrapped")
	}
	if _, err := wrap.ParsePublic([]byte{4, 1, 2}); err == nil {
		t.Fatal("a bad point was accepted")
	}
}
