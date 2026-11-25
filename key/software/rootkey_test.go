package software_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"connectrpc.com/connect"

	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
)

// sluis holds intermediates and never a root (REQ-PKI-027): the software key driver makes no key for a root purpose, whatever
// the purpose is called and whatever algorithm is asked for, and it signs or answers nothing for one. The request for a root is refused as
// a purpose that the store does not hold, and the state directory holds no key file afterwards. The scan half: the port of the key
// store has no operation or field that could carry a private key out. (That no purpose of sluis's registry is a root is sluis's check.)
//
// verifies: REQ-PKI-027
func TestTheSoftwareDriverMakesNoRootKeyAndThePortHasNoWayOut(t *testing.T) {
	r := start(t)
	ctx := context.Background()
	for _, purpose := range []string{"root", "root-ca", "ca-root", "x509-root", "root-key", "trust-anchor", "Root", "x509-root-ca"} {
		for _, alg := range []string{"ES384", "ES256", "ECDH-P384"} {
			_, err := r.client.Ensure(ctx, connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: purpose, Algorithm: alg}))
			wantRefused(t, err, connect.CodeInvalidArgument, "purpose")
		}
		_, err := r.client.Sign(ctx, connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: purpose, Digest: make([]byte, 48), Algorithm: "ES384"}))
		if err == nil {
			t.Errorf("Sign under %q succeeded", purpose)
		}
		_, err = r.client.PublicKey(ctx, connect.NewRequest(&keyv1alpha1.PublicKeyRequest{Purpose: purpose}))
		if err == nil {
			t.Errorf("PublicKey of %q answered", purpose)
		}
	}
	if entries, err := os.ReadDir(r.state); err == nil && len(entries) != 0 {
		t.Errorf("the state directory holds %d files after the requests for a root", len(entries))
	}
	// A real purpose still gets its key: the refusals above are about the root, not about the driver.
	if !ensure(t, r.client, "x509-intermediate", "ES384").GetCreated() {
		t.Error("the intermediate's key was not made")
	}

	// Nothing in the port can take a private key out: no operation of that kind, no field that is one.
	svc := keyv1alpha1.File_atoom_key_v1alpha1_keystore_proto.Services().ByName("KeyStoreService")
	if svc == nil || svc.Methods().Len() < 8 {
		t.Fatal("the KeyStoreService was not found or has no methods")
	}
	// The answer of Ecdh holds a shared secret, which is the point of key agreement and not a private key.
	has := func(name string, bad ...string) string {
		for _, b := range bad {
			if strings.Contains(strings.ToLower(name), b) {
				return b
			}
		}
		return ""
	}
	for i := 0; i < svc.Methods().Len(); i++ {
		m := svc.Methods().Get(i)
		if b := has(string(m.Name()), "export", "import", "private", "backup", "dump", "extract"); b != "" {
			t.Errorf("operation %s mentions %q", m.Name(), b)
		}
		for j := 0; j < m.Output().Fields().Len(); j++ {
			if b := has(string(m.Output().Fields().Get(j).Name()), "private", "export", "pkcs8", "seed"); b != "" {
				t.Errorf("%s answers with a field that mentions %q: %s", m.Name(), b, m.Output().Fields().Get(j).Name())
			}
		}
	}
}
