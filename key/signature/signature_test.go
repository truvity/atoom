package signature

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"math/big"
	"testing"
)

// RFC 7515, appendix A.3: the ES256 example. The signature of the JWS is the raw form, and it verifies with the public key of the
// appendix, so the conversion of a DER signature of any other key is held to the same reading of r||s.
func TestTheRawFormIsTheOneOfRFC7515AppendixA3(t *testing.T) {
	b64 := func(s string) []byte {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	point := append([]byte{0x04}, append(b64("f83OJ3D2xF1Bg8vub9tLe1gHMzV76e8Tus9uPHvRVEU"), b64("x_FEzRu9m36HLN_tue659LNpXW6pCyStikYjKIWI5a0")...)...)
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), point)
	if err != nil {
		t.Fatal(err)
	}
	input := "eyJhbGciOiJFUzI1NiJ9.eyJpc3MiOiJqb2UiLA0KICJleHAiOjEzMDA4MTkzODAsDQogImh0dHA6Ly9leGFtcGxlLmNvbS9pc19yb290Ijp0cnVlfQ"
	raw := b64("DtEhU3ljbEg8L38VWAfUAqOyKAM6-Xx-F4GawxaepmXFCgfTjDxw5djxLa8ISlSApmWQxfKTUJqPP3-Kg6NU1Q")
	digest := sha256.Sum256([]byte(input))
	r, s := new(big.Int).SetBytes(raw[:32]), new(big.Int).SetBytes(raw[32:])
	if !ecdsa.Verify(pub, digest[:], r, s) {
		t.Fatal("the appendix signature does not verify: the reading of r||s is wrong")
	}
	der, err := RawToDER(ES256, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !ecdsa.VerifyASN1(pub, digest[:], der) {
		t.Fatal("the DER form of the appendix signature does not verify")
	}
	back, err := DERToRaw(ES256, der)
	if err != nil || !bytes.Equal(back, raw) {
		t.Fatalf("the round trip: %v", err)
	}
}

// Both curves, many signatures: each is 2 x size long, verifies as r||s, and comes back as the same DER. Short r or s (leading zero
// octets, which DER drops) and the high bit (which DER prefixes with 00) both happen among them.
func TestRoundTripOfManySignatures(t *testing.T) {
	for _, tc := range []struct {
		alg   string
		curve elliptic.Curve
		sum   func([]byte) []byte
	}{
		{ES256, elliptic.P256(), func(b []byte) []byte { d := sha256.Sum256(b); return d[:] }},
		{ES384, elliptic.P384(), func(b []byte) []byte { d := sha512.Sum384(b); return d[:] }},
	} {
		key, err := ecdsa.GenerateKey(tc.curve, rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		short := 0
		for i := range 600 {
			digest := tc.sum([]byte{byte(i), byte(i >> 8)})
			der, err := ecdsa.SignASN1(rand.Reader, key, digest)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := DERToRaw(tc.alg, der)
			if err != nil || len(raw) != 2*Size(tc.alg) {
				t.Fatalf("%s: raw %d octets, %v", tc.alg, len(raw), err)
			}
			n := Size(tc.alg)
			if !ecdsa.Verify(&key.PublicKey, digest, new(big.Int).SetBytes(raw[:n]), new(big.Int).SetBytes(raw[n:])) {
				t.Fatalf("%s: the raw form does not verify", tc.alg)
			}
			if raw[0] == 0 || raw[n] == 0 {
				short++
			}
			again, err := RawToDER(tc.alg, raw)
			if err != nil || !bytes.Equal(again, der) {
				t.Fatalf("%s: the round trip differs (%v)", tc.alg, err)
			}
		}
		t.Logf("%s: %d of 600 signatures had a leading zero octet", tc.alg, short)
	}
}

func TestRefusals(t *testing.T) {
	good := bytes.Repeat([]byte{0x01}, 64)
	der, err := RawToDER(ES256, good)
	if err != nil {
		t.Fatal(err)
	}
	for name, f := range map[string]func() error{
		"unknown algorithm (DER)": func() error { _, err := DERToRaw("RS256", der); return err },
		"unknown algorithm (raw)": func() error { _, err := RawToDER("RS256", good); return err },
		"empty":                   func() error { _, err := DERToRaw(ES256, nil); return err },
		"trailing bytes":          func() error { _, err := DERToRaw(ES256, append(bytes.Clone(der), 0)); return err },
		"cut":                     func() error { _, err := DERToRaw(ES256, der[:len(der)-1]); return err },
		"not a sequence":          func() error { _, err := DERToRaw(ES256, []byte{0x04, 0x01, 0x01}); return err },
		"ES384 form read as ES256": func() error {
			d384, _ := RawToDER(ES384, bytes.Repeat([]byte{0x7f}, 96))
			_, err := DERToRaw(ES256, d384)
			return err
		},
		"zero r": func() error {
			_, err := DERToRaw(ES256, []byte{0x30, 0x06, 0x02, 0x01, 0x00, 0x02, 0x01, 0x01})
			return err
		},
		"negative r": func() error {
			_, err := DERToRaw(ES256, []byte{0x30, 0x06, 0x02, 0x01, 0xff, 0x02, 0x01, 0x01})
			return err
		},
		"raw too short": func() error { _, err := RawToDER(ES256, good[:63]); return err },
		"raw too long":  func() error { _, err := RawToDER(ES256, append(bytes.Clone(good), 1)); return err },
		"raw zero s": func() error {
			_, err := RawToDER(ES256, append(bytes.Clone(good[:32]), make([]byte, 32)...))
			return err
		},
		"raw 384 as 256": func() error { _, err := RawToDER(ES256, bytes.Repeat([]byte{1}, 96)); return err },
		"non-minimal DER r": func() error {
			_, err := DERToRaw(ES256, []byte{0x30, 0x07, 0x02, 0x02, 0x00, 0x01, 0x02, 0x01, 0x01})
			return err
		},
	} {
		if err := f(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if Size("ES512") != 0 {
		t.Error("Size of an unknown algorithm is not 0")
	}
}
