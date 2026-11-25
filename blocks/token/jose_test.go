package token

import (
	"crypto/rsa"
	"crypto/x509"
	"math/big"
	"testing"
)

// The example of RFC 7638, section 3.1: the thumbprint of its RSA key.
func TestTheThumbprintIsTheRFCs(t *testing.T) {
	n, err := b64.DecodeString("0vx7agoebGcQSuuPiLJXZptN9nndrQmbXEps2aiAFbWhM78LhWx4cbbfAAtVT86zwu1RK7aPFFxuhDR1L6tSoc_BJECPebWKRXjBZCiFV4n3oknjhMstn64tZ_2W-5JsGY4Hc5n9yBXArwl93lqt7_RN5w6Cf0h4QyQ5v-65YGjQR0_FDW2QvzqY368QQMicAtaSqzs8KJZgnYb9c7d0zgdAZHzu6qMQvRL5hajrn1n91CbOpbISD08qNLyrdkt-bFTWhAI4vMQFh6WeZu0fM4lFd2NcRwr3XPksINHaQ-G_xBniIqbw0Ls1jF44-csFCur-kEgU8awapJzKnqDKgw")
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(&rsa.PublicKey{N: new(big.Int).SetBytes(n), E: 65537})
	if err != nil {
		t.Fatal(err)
	}
	k, err := parsePublic("RS256", spki)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := k.thumbprint(), "NzbLsXh8uDCcd-6MNwXF4W_7noWXFZAfHkxZsRGC9Xs"; got != want {
		t.Fatalf("thumbprint %s, want %s", got, want)
	}
}

func TestStrictObject(t *testing.T) {
	for raw, ok := range map[string]bool{
		`{}`:                            true,
		" \t{}\n":                       true,
		`{"a":"\\ud800"}`:               true, // an escaped backslash, then text
		`{"a":"\ud83d\ude00"}`:          true,
		`{"a":"\uD83D\uDE00"}`:          true,
		`{"a":"\ud83dx"}`:               false,
		`{"a":"\ud83d\u0041"}`:          false,
		`{"a":"\ud83d"}`:                false,
		`{"a":"\ude00"}`:                false,
		`{"a":"\ud83d\\ude00"}`:         false,
		`{"a":[1,{"b":2}],"c":{"a":1}}`: true,
		`{"a":[1,{"b":2,"b":3}]}`:       false,
		`{"a":1}{}`:                     false,
		`null`:                          false,
		`{"a":1,}`:                      false,
	} {
		if got := strictObject([]byte(raw)) == nil; got != ok {
			t.Errorf("%s: %v, want %v", raw, got, ok)
		}
	}
}

func TestParseHeader(t *testing.T) {
	for raw, ok := range map[string]bool{
		`{"alg":"ES256","kid":"k","typ":"JWT"}`:              true,
		`{"typ":"JWT","alg":"ES256","kid":"k"}`:              true,
		`{"alg":"ES256","kid":"k"}`:                          false,
		`{"alg":"ES256","kid":"k","typ":"JWT","crit":["x"]}`: false,
		`{"alg":"ES256","kid":"","typ":"JWT"}`:               false,
		`{"alg":"ES256","kid":1,"typ":"JWT"}`:                false,
		`{"alg":"ES256","kid":"k","kid":"k","typ":"JWT"}`:    false,
	} {
		if _, got := parseHeader([]byte(raw)); got != ok {
			t.Errorf("%s: %v, want %v", raw, got, ok)
		}
	}
}
