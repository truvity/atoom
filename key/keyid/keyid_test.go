package keyid

import (
	"strings"
	"testing"
)

func TestEveryIdEndsWithTheFingerprintOfTheKey(t *testing.T) {
	spki := []byte("a subject public key info")
	f := Fingerprint(spki)
	if len(f) != 64 || f != strings.ToLower(f) {
		t.Fatalf("fingerprint %q", f)
	}
	// SHA-256 of the bytes, computed independently: echo -n 'a subject public key info' | sha256sum.
	if f != "852297c6afd2347d03c6985cd6c550efbad1c4a218bd020389f07e74088204c6" {
		t.Fatalf("fingerprint %s is not the SHA-256 of the bytes", f)
	}
	for _, id := range []string{Software(spki), Card("9a", spki), KMS("arn:aws:kms:eu-west-1:111122223333:key/abc", spki)} {
		if !strings.HasSuffix(id, f) {
			t.Errorf("%q does not end with the fingerprint", id)
		}
	}
	if Software(spki) != f || Card("9a", spki) != "9a:"+f || KMS("arn", spki) != "arn#"+f {
		t.Error("the forms are not the documented ones")
	}
	if Fingerprint([]byte("another")) == f {
		t.Error("two keys share a fingerprint")
	}
}
