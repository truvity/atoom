package yubikeypiv_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/common/refusal"
	"github.com/truvity/atoom/key/conformance"
	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
	"github.com/truvity/atoom/key/gen/atoom/key/v1alpha1/keyv1alpha1connect"
	"github.com/truvity/atoom/key/pinrelease"
	"github.com/truvity/atoom/key/purpose"
	"github.com/truvity/atoom/key/signature"
	"github.com/truvity/atoom/key/yubikeypiv"
	"github.com/truvity/atoom/key/yubikeypiv/internal/card"
	"github.com/truvity/atoom/key/yubikeypiv/internal/card/cardtest"
)

// skipUnlessLinux skips a test that serves the unlock service. The driver can be unlocked only on Linux: it holds the PIN in locked
// memory (mlock, MADV_DONTDUMP) and reads the peer's uid off the admin socket (SO_PEERCRED), and elsewhere it starts and stays locked
// (pinmem_other.go, peercred_other.go). The tests that need no unlock run everywhere.
func skipUnlessLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("SKIP: the driver can be unlocked only on Linux (locked memory, SO_PEERCRED); on " + runtime.GOOS + " it stays locked")
	}
}

// shortDir returns a short directory: a Unix socket path holds about 108 bytes.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "k")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

type running struct {
	key   keyv1alpha1connect.KeyStoreServiceClient
	admin keyv1alpha1connect.UnlockServiceClient
	adm   string
	card  *cardtest.Card
}

// inProcess is the host of a test: it serves the key-store handler on an in-process HTTP server, as a consumer that links the
// driver does, and says where it listens.
type inProcess struct {
	ready chan keyv1alpha1connect.KeyStoreServiceClient
}

func newInProcess() *inProcess {
	return &inProcess{ready: make(chan keyv1alpha1connect.KeyStoreServiceClient, 1)}
}

func (h *inProcess) host() yubikeypiv.Host {
	return yubikeypiv.Host{Serve: func(ctx context.Context, path string, handler http.Handler) error {
		mux := http.NewServeMux()
		mux.Handle(path, handler)
		srv := httptest.NewServer(mux)
		defer srv.Close()
		h.ready <- keyv1alpha1connect.NewKeyStoreServiceClient(srv.Client(), srv.URL)
		<-ctx.Done()
		return nil
	}}
}

// The two values of Options.AllowFactoryDefaults, named for the tests: a card in factory state that the driver may provision, and a
// card that its owners provisioned.
const (
	factoryDefaults = true
	provisioned     = false
)

// startWith serves the driver over a fake card. allowAdmin decides who is an administrator.
func startWith(t *testing.T, c *cardtest.Card, factory bool, allowAdmin func(uint32) bool) running {
	t.Helper()
	return startOpts(t, c, factory, allowAdmin, yubikeypiv.Options{Purposes: refPurposes(), Node: "node-a"})
}

// startOpts is startWith with the options of the PIN release: the node, the key plan check and the state directory.
// The factory switch, the serial and the card are the test's.
func startOpts(t *testing.T, c *cardtest.Card, factory bool, allowAdmin func(uint32) bool, opts yubikeypiv.Options) running {
	t.Helper()
	r, _ := startRun(t, c, factory, allowAdmin, opts, true)
	return r
}

// runResult is how Run ended: finished is closed once err is set.
type runResult struct {
	finished chan struct{}
	err      error
}

// startRun serves the driver and returns how Run ends. strict makes an error of Run a failure of the test at its end;
// a test that expects the driver to exit passes false and reads the result itself.
func startRun(t *testing.T, c *cardtest.Card, factory bool, allowAdmin func(uint32) bool, opts yubikeypiv.Options, strict bool) (running, *runResult) {
	t.Helper()
	skipUnlessLinux(t)
	opts.AllowFactoryDefaults, opts.Serial, opts.Open = factory, c.SerialNo, c.Opener()
	if opts.Boundary == "" {
		opts.Boundary = "test"
	}
	base := shortDir(t)
	r := running{adm: filepath.Join(base, "a.sock"), card: c}
	host := newInProcess()
	admin := yubikeypiv.AdminConfig{Socket: r.adm, Allow: allowAdmin}
	ctx, cancel := context.WithCancel(context.Background())
	res := &runResult{finished: make(chan struct{})}
	go func() {
		res.err = yubikeypiv.Run(ctx, opts, host.host(), admin)
		close(res.finished)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-res.finished:
			if strict && res.err != nil {
				t.Errorf("Run: %v", res.err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Run did not return")
		}
		if !c.Closed() {
			t.Error("the card was not closed")
		}
	})
	select {
	case r.key = <-host.ready:
	case <-res.finished:
		t.Fatalf("Run returned early: %v", res.err)
	case <-time.After(5 * time.Second):
		t.Fatal("the key-store handler was not served")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case <-res.finished:
			t.Fatalf("Run returned early: %v", res.err)
		default:
		}
		if fi, err := os.Lstat(r.adm); err == nil && fi.Mode()&os.ModeSocket != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the admin socket did not appear")
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.admin = keyv1alpha1connect.NewUnlockServiceClient(adminHTTPClient(r.adm), "http://admin")
	return r, res
}

// adminHTTPClient dials the admin socket over h2c, as the card driver's own main and sluis's `node unlock` do.
func adminHTTPClient(socket string) *http.Client {
	var protocols http.Protocols
	protocols.SetUnencryptedHTTP2(true)
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		Protocols: &protocols,
	}}
}

func self() func(uint32) bool {
	me := uint32(os.Getuid()) //nolint:gosec // a uid is non-negative
	return func(uid uint32) bool { return uid == me }
}

func start(t *testing.T, factory bool) running {
	t.Helper()
	return startWith(t, cardtest.New(), factory, self())
}

func (r running) unlock(t *testing.T, pin string) {
	t.Helper()
	if _, err := r.admin.Unlock(context.Background(), connect.NewRequest(&keyv1alpha1.UnlockRequest{Pin: []byte(pin)})); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
}

func wantRefused(t *testing.T, err error, code connect.Code, reason string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want %s/%s, got success", code, reason)
	}
	if connect.CodeOf(err) != code {
		t.Fatalf("code = %s, want %s (%v)", connect.CodeOf(err), code, err)
	}
	if got, _ := refusal.Reason(err); got != reason {
		t.Fatalf("reason = %q, want %q", got, reason)
	}
}

func ensure(t *testing.T, c keyv1alpha1connect.KeyStoreServiceClient, purpose, alg string) *keyv1alpha1.EnsureResponse {
	t.Helper()
	res, err := c.Ensure(context.Background(), connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: purpose, Algorithm: alg}))
	if err != nil {
		t.Fatalf("Ensure(%s, %s): %v", purpose, alg, err)
	}
	return res.Msg
}

func sign(c keyv1alpha1connect.KeyStoreServiceClient, purpose, alg string, digest []byte) (*keyv1alpha1.SignResponse, error) {
	res, err := c.Sign(context.Background(), connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: purpose, Digest: digest, Algorithm: alg}))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

func digest384() []byte { d := sha512.Sum384([]byte("x")); return d[:] }

// The kit of the port passes against the driver over the fake card once it is unlocked: every case runs, the one that needs a store
// that attests included.
func TestTheKitPassesOnTheFakeCardOnceUnlocked(t *testing.T) {
	c := cardtest.New()
	r := startOpts(t, c, factoryDefaults, self(), yubikeypiv.Options{Purposes: conformance.Declaration(), Node: "node-a"})
	r.unlock(t, "123456")
	var out bytes.Buffer
	if err := conformance.Run(context.Background(), r.key, &out); err != nil {
		t.Fatalf("the kit failed: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "15 cases, 15 ok, 0 failed, 0 skipped") {
		t.Fatalf("report:\n%s", out.String())
	}
}

// verifies: REQ-KEYS-026
func TestEveryPurposeIsMadeInItsSlotWithItsPolicies(t *testing.T) {
	r := start(t, factoryDefaults)
	want := []struct {
		purpose, alg string
		slot         card.Slot
		bits         int
		pin          card.PINPolicy
	}{
		{"x509-intermediate", "ES384", 0x9c, 384, card.PINOnce},
		{"token-signing", "ES384", 0x9a, 384, card.PINOnce},
		{"wrapping", "ECDH-P384", 0x9d, 384, card.PINOnce},
		{"audit-log", "ES256", 0x84, 256, card.PINOnce},
		{"pin-release", "ES384", 0x87, 384, card.PINNever},
	}
	for _, w := range want {
		res := ensure(t, r.key, w.purpose, w.alg)
		if !res.GetCreated() {
			t.Errorf("%s: not created on a factory card", w.purpose)
		}
		att, err := r.card.Attest(w.slot)
		if err != nil {
			t.Fatalf("%s: slot %s holds no key: %v", w.purpose, w.slot, err)
		}
		root, _ := r.card.AttestationCertificate()
		a, err := r.card.VerifyAttestation(root, att)
		if err != nil {
			t.Fatalf("%s: attestation: %v", w.purpose, err)
		}
		if a.Slot != w.slot || a.PIN != w.pin || a.Touch != card.TouchNever {
			t.Errorf("%s: attested %+v, want slot %s, PIN %d, touch never", w.purpose, a, w.slot, w.pin)
		}
		if pub := att.PublicKey.(*ecdsa.PublicKey); pub.Params().BitSize != w.bits {
			t.Errorf("%s: a P-%d key, want P-%d", w.purpose, pub.Params().BitSize, w.bits)
		}
		if again := ensure(t, r.key, w.purpose, w.alg); again.GetCreated() || !bytes.Equal(again.GetSpkiDer(), res.GetSpkiDer()) {
			t.Errorf("%s: a second Ensure made a key or returned another one", w.purpose)
		}
	}
}

func TestItStartsLocked(t *testing.T) {
	r := start(t, factoryDefaults)
	ensure(t, r.key, "token-signing", "ES384")
	ensure(t, r.key, "pin-release", "ES384")
	ensure(t, r.key, "wrapping", "ECDH-P384")

	st, err := r.admin.Status(context.Background(), connect.NewRequest(&keyv1alpha1.StatusRequest{}))
	if err != nil || !st.Msg.GetLocked() || st.Msg.GetNode() != "node-a" || st.Msg.GetPinRetries() != 3 {
		t.Fatalf("Status = %v, %v; want locked, node-a, 3 retries", st, err)
	}
	// A key that needs the PIN is refused, and the card is not asked.
	_, err = sign(r.key, "token-signing", "ES384", digest384())
	wantRefused(t, err, connect.CodeFailedPrecondition, "locked")
	_, err = r.key.Ecdh(context.Background(), connect.NewRequest(&keyv1alpha1.EcdhRequest{Purpose: "wrapping", PeerPublic: p384Point(t)}))
	wantRefused(t, err, connect.CodeFailedPrecondition, "locked")
	if r.card.LoginAttempts != 0 || r.card.Signs != 0 {
		t.Fatalf("the locked driver talked to the card: %d logins, %d signatures", r.card.LoginAttempts, r.card.Signs)
	}
	// The public keys are public. The PIN release key signs while the board is locked (0006 "Touch and PIN"), but only
	// the request the driver writes itself.
	if _, err := r.key.PublicKey(context.Background(), connect.NewRequest(&keyv1alpha1.PublicKeyRequest{Purpose: "token-signing"})); err != nil {
		t.Fatalf("PublicKey while locked: %v", err)
	}
	if _, err := r.key.BeginPinRelease(context.Background(), connect.NewRequest(&keyv1alpha1.BeginPinReleaseRequest{RequestId: "01a1a66e-2a00-7abc-8def-0123456789ab"})); err != nil {
		t.Fatalf("a signed request while locked: %v", err)
	}
	// Unlocked, the same call works.
	r.unlock(t, "123456")
	if _, err := sign(r.key, "token-signing", "ES384", digest384()); err != nil {
		t.Fatalf("Sign after unlock: %v", err)
	}
	st, _ = r.admin.Status(context.Background(), connect.NewRequest(&keyv1alpha1.StatusRequest{}))
	if st.Msg.GetLocked() {
		t.Fatal("Status says locked after the unlock")
	}
}

// verifies: REQ-KEYS-058
func TestAWrongPINIsTriedOnceAndNeverRetried(t *testing.T) {
	r := start(t, factoryDefaults)
	ensure(t, r.key, "token-signing", "ES384")
	_, err := r.admin.Unlock(context.Background(), connect.NewRequest(&keyv1alpha1.UnlockRequest{Pin: []byte("654321")}))
	wantRefused(t, err, connect.CodePermissionDenied, "pin")
	if r.card.LoginAttempts != 1 || r.card.Retries != 2 {
		t.Fatalf("%d attempts, %d retries left; want 1 and 2", r.card.LoginAttempts, r.card.Retries)
	}
	// Nothing retries on its own: a call that needs the PIN is refused as locked and sends nothing.
	for range 5 {
		_, err = sign(r.key, "token-signing", "ES384", digest384())
		wantRefused(t, err, connect.CodeFailedPrecondition, "locked")
	}
	if r.card.LoginAttempts != 1 {
		t.Fatalf("%d attempts after the refusals, want still 1", r.card.LoginAttempts)
	}
	// The administrator types it again.
	r.unlock(t, "123456")
	if _, err := sign(r.key, "token-signing", "ES384", digest384()); err != nil {
		t.Fatalf("Sign: %v", err)
	}
}

func TestABlockedCardIsSaidSo(t *testing.T) {
	c := cardtest.New()
	c.Retries = 0
	r := startWith(t, c, factoryDefaults, self())
	_, err := r.admin.Unlock(context.Background(), connect.NewRequest(&keyv1alpha1.UnlockRequest{Pin: []byte("123456")}))
	wantRefused(t, err, connect.CodeFailedPrecondition, "pin_blocked")
}

func TestAPINOfTheWrongLengthNeverReachesTheCard(t *testing.T) {
	r := start(t, factoryDefaults)
	for _, pin := range []string{"", "12345", "123456789"} {
		_, err := r.admin.Unlock(context.Background(), connect.NewRequest(&keyv1alpha1.UnlockRequest{Pin: []byte(pin)}))
		wantRefused(t, err, connect.CodeInvalidArgument, "pin")
	}
	if r.card.LoginAttempts != 0 {
		t.Fatalf("%d PINs reached the card", r.card.LoginAttempts)
	}
}

func TestUnlockingTwiceAsksTheCardOnce(t *testing.T) {
	r := start(t, factoryDefaults)
	r.unlock(t, "123456")
	r.unlock(t, "000000") // a second PIN, even a wrong one, is not sent while the driver is unlocked
	if r.card.LoginAttempts != 1 {
		t.Fatalf("%d attempts, want 1", r.card.LoginAttempts)
	}
}

func TestACardThatLosesItsLoginIsLoggedInAgainWithTheHeldPIN(t *testing.T) {
	r := start(t, factoryDefaults)
	ensure(t, r.key, "token-signing", "ES384")
	r.unlock(t, "123456")
	r.card.Reset() // a power loss: the card forgets the login
	if _, err := sign(r.key, "token-signing", "ES384", digest384()); err != nil {
		t.Fatalf("Sign after the card lost its login: %v", err)
	}
	if r.card.LoginAttempts != 2 {
		t.Fatalf("%d attempts, want 2 (the unlock and one login)", r.card.LoginAttempts)
	}
}

func TestAHeldPINThatTheCardRefusesLocksTheDriverAgainAndIsNotRetried(t *testing.T) {
	r := start(t, factoryDefaults)
	ensure(t, r.key, "token-signing", "ES384")
	r.unlock(t, "123456")
	r.card.PIN = "654321" // someone changed the PIN
	r.card.Reset()
	_, err := sign(r.key, "token-signing", "ES384", digest384())
	wantRefused(t, err, connect.CodeFailedPrecondition, "locked")
	for range 3 {
		_, err = sign(r.key, "token-signing", "ES384", digest384())
		wantRefused(t, err, connect.CodeFailedPrecondition, "locked")
	}
	if r.card.LoginAttempts != 2 {
		t.Fatalf("%d attempts, want 2 (the unlock and the one refused)", r.card.LoginAttempts)
	}
	st, _ := r.admin.Status(context.Background(), connect.NewRequest(&keyv1alpha1.StatusRequest{}))
	if !st.Msg.GetLocked() {
		t.Fatal("the driver is not locked again")
	}
}

// Without factory defaults (the default of a host) the driver never makes a key: a card that has none answers absent.
func TestWithoutFactoryDefaultsNoKeyIsMade(t *testing.T) {
	r := startWith(t, cardtest.New(), provisioned, self())
	_, err := r.key.Ensure(context.Background(), connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: "token-signing", Algorithm: "ES384"}))
	wantRefused(t, err, connect.CodeNotFound, "absent")
	if _, aerr := r.card.Attest(0x9a); !errors.Is(aerr, card.ErrNoKey) {
		t.Fatalf("a key was made: %v", aerr)
	}
}

func TestAProvisionedCardIsAdopted(t *testing.T) {
	c := cardtest.New()
	if _, err := c.PutKey(0x9a, card.KeySpec{Algorithm: card.EC384, PIN: card.PINOnce, Touch: card.TouchNever}); err != nil {
		t.Fatal(err)
	}
	r := startWith(t, c, provisioned, self())
	res := ensure(t, r.key, "token-signing", "ES384")
	if res.GetCreated() || len(res.GetSpkiDer()) == 0 {
		t.Fatalf("Ensure = %+v, want the existing key", res)
	}
	r.unlock(t, "123456")
	if _, err := sign(r.key, "token-signing", "ES384", digest384()); err != nil {
		t.Fatalf("Sign: %v", err)
	}
}

func TestAFactoryDefaultsCardWithAnotherManagementKeyIsNotTouched(t *testing.T) {
	c := cardtest.New()
	c.ManagementKey = bytes.Repeat([]byte{0x42}, 24) // not the factory key
	r := startWith(t, c, factoryDefaults, self())
	_, err := r.key.Ensure(context.Background(), connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: "token-signing", Algorithm: "ES384"}))
	wantRefused(t, err, connect.CodeFailedPrecondition, "card")
	if _, aerr := c.Attest(0x9a); !errors.Is(aerr, card.ErrNoKey) {
		t.Fatalf("a key was made: %v", aerr)
	}
}

func TestTheAuditLogKeyIsP256AndSignsSHA256(t *testing.T) {
	r := start(t, factoryDefaults)
	r.unlock(t, "123456")
	// ES384 is refused for it, and the key is P-256.
	_, err := r.key.Ensure(context.Background(), connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: "audit-log", Algorithm: "ES384"}))
	wantRefused(t, err, connect.CodeFailedPrecondition, "algorithm")
	res := ensure(t, r.key, "audit-log", "ES256")
	pub, err := parseECDSA(res.GetSpkiDer())
	if err != nil || pub.Curve != elliptic.P256() {
		t.Fatalf("the audit-log key: %v", err)
	}
	d := sha256.Sum256([]byte("checkpoint"))
	sig, err := sign(r.key, "audit-log", "ES256", d[:])
	if err != nil || !ecdsa.VerifyASN1(pub, d[:], sig.GetSignatureDer()) {
		t.Fatalf("Sign: %v", err)
	}
	_, err = sign(r.key, "audit-log", "ES384", digest384())
	wantRefused(t, err, connect.CodeInvalidArgument, "algorithm")
}

// A purpose with no slot, named or default, is refused when the driver starts, before it opens a card; issuance-log, which the card driver
// had no row for, now takes the slot of the reference map (89) like any purpose a host declares.
func TestAPurposeWithNoSlotIsRefusedAtTheStart(t *testing.T) {
	c := cardtest.New()
	opened := 0
	open := func(sel card.Selector) (card.Card, error) { opened++; return c.Opener()(sel) }
	_, err := yubikeypiv.New(yubikeypiv.Options{AllowFactoryDefaults: true, Serial: c.SerialNo, Open: open, Purposes: []purpose.Declaration{
		{Name: "second-intermediate", Algorithm: purpose.ES384, Operations: []purpose.Operation{purpose.Sign}},
	}})
	var p *yubikeypiv.Problems
	if !errors.As(err, &p) || !strings.Contains(err.Error(), "no slot") {
		t.Fatalf("err = %v", err)
	}
	if opened != 0 {
		t.Fatalf("the card was opened %d times before the declaration was refused", opened)
	}
}

func TestIssuanceLogIsServedInItsSlot(t *testing.T) {
	r := start(t, factoryDefaults)
	r.unlock(t, "123456")
	if res := ensure(t, r.key, "issuance-log", "ES384"); len(res.GetSpkiDer()) == 0 {
		t.Fatal("no public key")
	}
}

func TestOnlyAnAdministratorReachesTheAdminSocket(t *testing.T) {
	r := startWith(t, cardtest.New(), factoryDefaults, func(uint32) bool { return false })
	_, err := r.admin.Unlock(context.Background(), connect.NewRequest(&keyv1alpha1.UnlockRequest{Pin: []byte("123456")}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("Unlock by a non-administrator: %v, want permission_denied", err)
	}
	_, err = r.admin.Status(context.Background(), connect.NewRequest(&keyv1alpha1.StatusRequest{}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("Status by a non-administrator: %v, want permission_denied", err)
	}
	if r.card.LoginAttempts != 0 {
		t.Fatal("the PIN of a non-administrator reached the card")
	}
	fi, err := os.Lstat(r.adm)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the admin socket: %v, %v; want mode 0600", fi, err)
	}
}

func TestTheAdminSocketOfARunningDriverIsNotTaken(t *testing.T) {
	r := start(t, factoryDefaults)
	err := yubikeypiv.ServeAdmin(context.Background(), yubikeypiv.AdminConfig{Socket: r.adm, Allow: self()}, nil)
	if err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("a second server on a live socket: %v", err)
	}
}

// verifies: REQ-KEYS-004
func TestCapabilitiesSayCardAndAttestation(t *testing.T) {
	r := start(t, factoryDefaults)
	res, err := r.key.Capabilities(context.Background(), connect.NewRequest(&keyv1alpha1.CapabilitiesRequest{}))
	if err != nil || res.Msg.GetStore() != "yubikey-piv" || !res.Msg.GetAttests() {
		t.Fatalf("Capabilities = %v, %v", res, err)
	}
}

// verifies: REQ-KEYS-022
func TestDriverRefusesToStart(t *testing.T) {
	spec := card.KeySpec{Algorithm: card.EC384, PIN: card.PINOnce, Touch: card.TouchNever}
	tests := []struct {
		name  string
		setup func(c *cardtest.Card)
		want  string
	}{
		{"firmware below the floor", func(c *cardtest.Card) { c.Firmware = card.Version{Major: 5, Minor: 4, Patch: 3} }, "card.firmware"},
		{"the floor itself is accepted", func(c *cardtest.Card) { c.Firmware = card.Version{Major: 5, Minor: 7, Patch: 0} }, ""},
		{"an attestation that chains to another root", func(c *cardtest.Card) {
			c.RogueRoot = true
			_, _ = c.PutKey(0x9a, spec)
		}, "slots.9a.attestation: chain"},
		{"an attestation of older firmware", func(c *cardtest.Card) {
			c.AttestedFirmware = card.Version{Major: 5, Minor: 4, Patch: 3}
			_, _ = c.PutKey(0x9a, spec)
		}, "slots.9a.attestation: firmware"},
		{"a key whose attestation states other policies", func(c *cardtest.Card) {
			_, _ = c.PutKey(0x9a, spec)
			c.Lie(0x9a, card.KeySpec{Algorithm: card.EC384, PIN: card.PINNever, Touch: card.TouchNever})
		}, "slots.9a.attestation: pin_policy"},
		{"a key made with the wrong PIN policy for its slot", func(c *cardtest.Card) {
			_, _ = c.PutKey(0x87, spec) // 87 must be PIN never
		}, "slots.87.attestation: pin_policy"},
		{"a P-384 key in the audit-log slot", func(c *cardtest.Card) {
			_, _ = c.PutKey(0x84, spec)
		}, "slots.84.attestation: algorithm"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := cardtest.New()
			tt.setup(c)
			_, err := yubikeypiv.New(yubikeypiv.Options{Purposes: refPurposes(), Serial: c.SerialNo, Open: c.Opener()})
			if tt.want == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			var p *yubikeypiv.Problems
			if !errors.As(err, &p) || !strings.Contains(strings.Join(p.List, ";"), tt.want) {
				t.Fatalf("err = %v, want a problem with %q", err, tt.want)
			}
			if !c.Closed() {
				t.Fatal("the refused card was left open")
			}
		})
	}
}

func TestADifferentSerialIsRefused(t *testing.T) {
	c := cardtest.New()
	if _, err := yubikeypiv.New(yubikeypiv.Options{Purposes: refPurposes(), AllowFactoryDefaults: true, Serial: c.SerialNo + 1, Open: c.Opener()}); err == nil {
		t.Fatal("a card with another serial was opened")
	}
}

func TestCheckVerifiesAndWritesNothing(t *testing.T) {
	c := cardtest.New()
	if err := yubikeypiv.Check(yubikeypiv.Options{Purposes: refPurposes(), Serial: c.SerialNo, Open: c.Opener()}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !c.Closed() {
		t.Fatal("Check left the card open")
	}
	if _, err := c.Attest(0x9a); !errors.Is(err, card.ErrNoKey) {
		t.Fatalf("Check made a key: %v", err)
	}
	c = cardtest.New()
	c.Firmware = card.Version{Major: 5, Minor: 1}
	if err := yubikeypiv.Check(yubikeypiv.Options{Purposes: refPurposes(), Serial: c.SerialNo, Open: c.Opener()}); !errors.As(err, new(*yubikeypiv.Problems)) {
		t.Fatalf("Check on old firmware: %v, want a refusal (Problems)", err)
	}
}

func p384Point(t *testing.T) []byte {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	e, err := k.PublicKey.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	return e.Bytes()
}

func parseECDSA(spki []byte) (*ecdsa.PublicKey, error) {
	k, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		return nil, err
	}
	pub, ok := k.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("not an EC key")
	}
	return pub, nil
}

// Without factory defaults the driver is bound to one card: without a serial it does not start, so a genuine card whose
// attestations match the slot map is not adopted for being there.
func TestWithoutFactoryDefaultsTheDriverIsBoundToACard(t *testing.T) {
	c := cardtest.New()
	_, err := yubikeypiv.New(yubikeypiv.Options{Purposes: refPurposes(), Open: c.Opener()})
	if !errors.Is(err, yubikeypiv.ErrSerial) {
		t.Errorf("without a serial: %v, want ErrSerial", err)
	}
	// The serial names the card: another card is not opened, and with factory defaults the driver still takes the only one.
	c = cardtest.New()
	if _, err := yubikeypiv.New(yubikeypiv.Options{Purposes: refPurposes(), Serial: c.SerialNo + 7, Open: c.Opener()}); err == nil {
		t.Error("a card with another serial was adopted")
	}
	if s, err := yubikeypiv.New(yubikeypiv.Options{Purposes: refPurposes(), AllowFactoryDefaults: true, Open: c.Opener()}); err != nil {
		t.Errorf("factory defaults without a serial: %v", err)
	} else {
		_ = s.Close()
	}
}

// verifies: REQ-KEYS-026
func TestAnImportedKeyIsRefusedAndNeverReplaced(t *testing.T) {
	spec := card.KeySpec{Algorithm: card.EC384, PIN: card.PINOnce, Touch: card.TouchNever}
	c := cardtest.New()
	if err := c.PutImportedKey(0x9a, spec); err != nil {
		t.Fatal(err)
	}
	// REQ-KEYS-026: the start refuses it, with factory defaults or without.
	for _, factory := range []bool{factoryDefaults, provisioned} {
		_, err := yubikeypiv.New(yubikeypiv.Options{Purposes: refPurposes(), AllowFactoryDefaults: factory, Serial: c.SerialNo, Open: c.Opener()})
		var p *yubikeypiv.Problems
		if !errors.As(err, &p) || !strings.Contains(strings.Join(p.List, ";"), "slots.9a.attestation: origin") {
			t.Errorf("factory defaults %v: err = %v, want the imported key refused", factory, err)
		}
	}
	if _, err := c.Generate(card.DefaultManagementKey, 0x9a, spec); err == nil {
		t.Error("the fake allowed a replacement: the test would prove nothing")
	}
}

func TestAnUnpluggedCardWipesThePINAndEndsTheDriver(t *testing.T) {
	skipUnlessLinux(t)
	c := cardtest.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base := shortDir(t)
	host := newInProcess()
	admin := yubikeypiv.AdminConfig{Socket: filepath.Join(base, "a.sock"), Allow: self()}
	done := make(chan error, 1)
	go func() {
		done <- yubikeypiv.Run(ctx, yubikeypiv.Options{Purposes: refPurposes(), AllowFactoryDefaults: true, Boundary: "test", Node: "n", Serial: c.SerialNo, Open: c.Opener()}, host.host(), admin)
	}()
	var key keyv1alpha1connect.KeyStoreServiceClient
	select {
	case key = <-host.ready:
	case err := <-done:
		t.Fatalf("Run returned early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("the key-store handler was not served")
	}
	for i := 0; ; i++ {
		if fi, err := os.Lstat(admin.Socket); err == nil && fi.Mode()&os.ModeSocket != 0 {
			break
		}
		if i > 500 {
			t.Fatal("the admin socket did not appear")
		}
		time.Sleep(10 * time.Millisecond)
	}
	adm := keyv1alpha1connect.NewUnlockServiceClient(adminHTTPClient(admin.Socket), "http://admin")
	ensure(t, key, "token-signing", "ES384")
	if _, err := adm.Unlock(ctx, connect.NewRequest(&keyv1alpha1.UnlockRequest{Pin: []byte("123456")})); err != nil {
		t.Fatal(err)
	}
	if _, err := sign(key, "token-signing", "ES384", digest384()); err != nil {
		t.Fatal(err)
	}

	c.Unplug() // not tested on a card: the fake fails the way piv-go is expected to once the transaction is dead
	_, err := sign(key, "token-signing", "ES384", digest384())
	wantRefused(t, err, connect.CodeUnavailable, "card")
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "the card is gone") {
			t.Fatalf("Run returned %v, want an error that says the card is gone", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the driver is still up with a dead card")
	}
	if !c.Closed() {
		t.Error("the card was not closed")
	}
}

func TestACardWithOneAttemptLeftIsNotTried(t *testing.T) {
	c := cardtest.New()
	c.Retries = 1
	r := startWith(t, c, factoryDefaults, self())
	_, err := r.admin.Unlock(context.Background(), connect.NewRequest(&keyv1alpha1.UnlockRequest{Pin: []byte("123456")}))
	wantRefused(t, err, connect.CodeFailedPrecondition, "pin_retries")
	if c.LoginAttempts != 0 || c.Retries != 1 {
		t.Fatalf("%d attempts, %d retries: the card was tried", c.LoginAttempts, c.Retries)
	}
	st, _ := r.admin.Status(context.Background(), connect.NewRequest(&keyv1alpha1.StatusRequest{}))
	if !st.Msg.GetLocked() || st.Msg.GetPinRetries() != 1 {
		t.Fatalf("Status = %v", st)
	}
}

// Two wrong PINs leave one attempt: the third unlock is refused before the card sees the PIN, so the card is never
// blocked by the driver.
func TestABadUnlockCannotBlockTheCard(t *testing.T) {
	c := cardtest.New()
	r := startWith(t, c, factoryDefaults, self())
	for range 2 {
		_, err := r.admin.Unlock(context.Background(), connect.NewRequest(&keyv1alpha1.UnlockRequest{Pin: []byte("654321")}))
		wantRefused(t, err, connect.CodePermissionDenied, "pin")
	}
	_, err := r.admin.Unlock(context.Background(), connect.NewRequest(&keyv1alpha1.UnlockRequest{Pin: []byte("654321")}))
	wantRefused(t, err, connect.CodeFailedPrecondition, "pin_retries")
	if c.Retries != 1 || c.LoginAttempts != 2 {
		t.Fatalf("%d retries left after %d attempts, want 1 and 2", c.Retries, c.LoginAttempts)
	}
}

// The purposes of a host that no driver ever knew (the target of step B13), with ES256 and ES384, in the spare slots that the host names:
// each key is born on the card in its slot with the policies of the plan (PIN once, touch never), attested, signs for the caller once the
// card is unlocked, and the signature converts to the raw r||s of a JWS. A key that the host declared without an operation is made and read
// and never used.
//
// verifies: REQ-KEYS-026
func TestAHostDeclaredPurposeIsServedByTheCardWithES256AndES384(t *testing.T) {
	signs := []purpose.Operation{purpose.Sign}
	r := startOpts(t, cardtest.New(), factoryDefaults, self(), yubikeypiv.Options{Node: "node-a", Purposes: []purpose.Declaration{
		{Name: "client-assertion", Algorithm: purpose.ES256, Operations: signs, Slot: "90"},
		{Name: "identity-header", Algorithm: purpose.ES384, Operations: signs, Slot: "91"},
		{Name: "made-only", Algorithm: purpose.ES256, Slot: "92"},
	}})
	r.unlock(t, "123456")
	for _, w := range []struct {
		purpose, alg string
		slot         card.Slot
		bits         int
	}{{"client-assertion", "ES256", 0x90, 256}, {"identity-header", "ES384", 0x91, 384}, {"made-only", "ES256", 0x92, 256}} {
		res := ensure(t, r.key, w.purpose, w.alg)
		att, err := r.card.Attest(w.slot)
		if err != nil {
			t.Fatalf("%s: slot %s holds no key: %v", w.purpose, w.slot, err)
		}
		root, _ := r.card.AttestationCertificate()
		a, err := r.card.VerifyAttestation(root, att)
		if err != nil || a.Slot != w.slot || a.PIN != card.PINOnce || a.Touch != card.TouchNever {
			t.Fatalf("%s: attested %+v, %v", w.purpose, a, err)
		}
		pub, err := parseECDSA(res.GetSpkiDer())
		if err != nil || pub.Params().BitSize != w.bits {
			t.Fatalf("%s: %v", w.purpose, err)
		}
	}
	d256, d384 := sha256.Sum256([]byte("jws signing input")), sha512.Sum384([]byte("jws signing input"))
	for _, w := range []struct {
		purpose, alg string
		digest       []byte
	}{{"client-assertion", "ES256", d256[:]}, {"identity-header", "ES384", d384[:]}} {
		pubRes, err := r.key.PublicKey(context.Background(), connect.NewRequest(&keyv1alpha1.PublicKeyRequest{Purpose: w.purpose}))
		if err != nil {
			t.Fatal(err)
		}
		pub, _ := parseECDSA(pubRes.Msg.GetSpkiDer())
		sig, err := sign(r.key, w.purpose, w.alg, w.digest)
		if err != nil || !ecdsa.VerifyASN1(pub, w.digest, sig.GetSignatureDer()) {
			t.Fatalf("%s: Sign: %v", w.purpose, err)
		}
		if raw, err := signature.DERToRaw(w.alg, sig.GetSignatureDer()); err != nil || len(raw) != 2*signature.Size(w.alg) {
			t.Fatalf("%s: raw form: %v", w.purpose, err)
		}
	}
	_, err := sign(r.key, "made-only", "ES256", d256[:])
	wantRefused(t, err, connect.CodeInvalidArgument, "purpose")
	// The names of the old closed list that the host did not declare are not known.
	_, err = r.key.Ensure(context.Background(), connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: "token-signing", Algorithm: "ES384"}))
	wantRefused(t, err, connect.CodeInvalidArgument, "purpose")
	// Capabilities says the purposes and the slots stay the host's business.
	caps, err := r.key.Capabilities(context.Background(), connect.NewRequest(&keyv1alpha1.CapabilitiesRequest{}))
	if err != nil || len(caps.Msg.GetPurposes()) != 3 || caps.Msg.GetPurposes()[1].GetName() != "identity-header" || caps.Msg.GetPurposes()[2].GetOperations() != nil {
		t.Fatalf("Capabilities: %v %v", caps, err)
	}
}

// A card that already holds a key in the slot of a purpose adopts it only when it is the declared algorithm: a host that declares ES384
// where the card holds P-256 is refused at the start, and a host that declares the same algorithm finds its key again.
func TestACardThatHoldsAnotherAlgorithmThanTheDeclaredOneRefusesTheStart(t *testing.T) {
	c := cardtest.New()
	r := startOpts(t, c, factoryDefaults, self(), yubikeypiv.Options{Node: "node-a", Purposes: []purpose.Declaration{
		{Name: "client-assertion", Algorithm: purpose.ES256, Operations: []purpose.Operation{purpose.Sign}, Slot: "90"},
	}})
	ensure(t, r.key, "client-assertion", "ES256")
	open := func(alg string) error {
		_, err := yubikeypiv.New(yubikeypiv.Options{AllowFactoryDefaults: true, Serial: c.SerialNo, Open: c.Opener(), Purposes: []purpose.Declaration{
			{Name: "client-assertion", Algorithm: alg, Operations: []purpose.Operation{purpose.Sign}, Slot: "90"},
		}})
		return err
	}
	var p *yubikeypiv.Problems
	if err := open(purpose.ES384); !errors.As(err, &p) || !strings.Contains(err.Error(), "algorithm") {
		t.Fatalf("a card that holds P-256 where P-384 is declared: %v", err)
	}
	if err := open(purpose.ES256); err != nil {
		t.Fatalf("the same declaration finds its key again: %v", err)
	}
}

// The key_id of a card key is its slot and the fingerprint of its public key, in Sign and in PublicKey.
func TestTheKeyIDIsTheSlotAndTheFingerprint(t *testing.T) {
	r := start(t, factoryDefaults)
	r.unlock(t, "123456")
	e := ensure(t, r.key, "token-signing", "ES384")
	want := fmt.Sprintf("9a:%x", sha256.Sum256(e.GetSpkiDer()))
	pub, err := r.key.PublicKey(context.Background(), connect.NewRequest(&keyv1alpha1.PublicKeyRequest{Purpose: "token-signing"}))
	if err != nil || pub.Msg.GetKeyId() != want {
		t.Fatalf("PublicKey key_id %q, want %q (%v)", pub.Msg.GetKeyId(), want, err)
	}
	res, err := sign(r.key, "token-signing", "ES384", digest384())
	if err != nil || res.GetKeyId() != want {
		t.Fatalf("Sign key_id %q, want %q (%v)", res.GetKeyId(), want, err)
	}
}

// The PIN release domains are the host's: a pair that cannot keep the request and the answer apart refuses the start, before the
// card is opened.
func TestPinReleaseDomainsThatCannotKeepTheTextsApartRefuseTheStart(t *testing.T) {
	c := cardtest.New()
	_, err := yubikeypiv.New(yubikeypiv.Options{Purposes: refPurposes(), Serial: c.SerialNo, Open: c.Opener(),
		PinReleaseDomains: pinrelease.Domains{Request: "example-pin-release", Answer: "example-pin-release"}})
	if !errors.Is(err, pinrelease.ErrDomains) {
		t.Fatalf("err = %v, want pinrelease.ErrDomains", err)
	}
}
