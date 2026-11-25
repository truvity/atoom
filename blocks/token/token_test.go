package token_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/blocks/seal"
	"github.com/truvity/atoom/blocks/token"
	"github.com/truvity/atoom/blocks/token/conformance"
	tokenv1alpha1 "github.com/truvity/atoom/blocks/token/gen/atoom/token/v1alpha1"
	"github.com/truvity/atoom/blocks/token/gen/atoom/token/v1alpha1/tokenv1alpha1connect"
	"github.com/truvity/atoom/blocks/token/internal/tokentest"
	"github.com/truvity/atoom/common/call"
	"github.com/truvity/atoom/common/refusal"
	"github.com/truvity/atoom/key/gen/atoom/key/v1alpha1/keyv1alpha1connect"
	"github.com/truvity/atoom/key/purpose"
	"github.com/truvity/atoom/key/route"
	"github.com/truvity/atoom/socket"
)

func kitStack(t *testing.T) *stack {
	return newStack(t, stackOptions{purposes: conformance.Purposes(), rules: conformance.Rules()})
}

func TestTheKitPassesOverASealBlock(t *testing.T) {
	s := kitStack(t)
	var out bytes.Buffer
	if err := conformance.Run(context.Background(), s.client, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
}

func TestTheKitWithoutItsRulesSkipsOneCase(t *testing.T) {
	s := newStack(t, stackOptions{purposes: conformance.Purposes()})
	var out bytes.Buffer
	if err := conformance.RunCases(context.Background(), s.client, conformance.CasesWithoutRules(), &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), conformance.RulesCase) || len(conformance.CasesWithoutRules()) != len(conformance.Cases())-1 {
		t.Fatalf("the case of the rules ran, or another one was left out:\n%s", out.String())
	}
}

func wantRefusal(t *testing.T, err error, code connect.Code, reason string) {
	t.Helper()
	r, _ := refusal.Reason(err)
	if err == nil || connect.CodeOf(err) != code || r != reason {
		t.Fatalf("got %v (%s/%s), want %s/%s", err, connect.CodeOf(err), r, code, reason)
	}
}

func sign(ctx context.Context, s *stack, p, typ, claims string) (*tokenv1alpha1.SignResponse, error) {
	res, err := s.client.Sign(ctx, connect.NewRequest(&tokenv1alpha1.SignRequest{Purpose: p, Typ: typ, ClaimsJson: []byte(claims)}))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

func TestThePublicKeyIsReadOnceAndAgainWhenTheKeyChanges(t *testing.T) {
	ctx := context.Background()
	s := kitStack(t)
	before := s.keys.PublicKeys.Load()
	signsBefore := s.keys.Signs.Load()
	first, err := sign(ctx, s, conformance.PurposeES384, conformance.TypeJWT, `{"n":0}`)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		res, err := sign(ctx, s, conformance.PurposeES384, conformance.TypeJWT, `{"n":1}`)
		if err != nil {
			t.Fatal(i, err)
		}
		if res.GetKid() != first.GetKid() {
			t.Fatal("the kid changed with no new key")
		}
	}
	if n := s.keys.PublicKeys.Load() - before; n != 0 {
		t.Fatalf("%d PublicKey calls for 21 signatures; the key read at the start is kept", n)
	}
	if n := s.keys.Signs.Load() - signsBefore; n != 21 {
		t.Fatalf("%d signatures at the key port, want 21", n)
	}

	s.keys.Replace(conformance.PurposeES384)
	signsBefore = s.keys.Signs.Load()
	res, err := sign(ctx, s, conformance.PurposeES384, conformance.TypeJWT, `{"n":2}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.GetKid() == first.GetKid() {
		t.Fatal("a token of the new key carries the old kid")
	}
	if n := s.keys.Signs.Load() - signsBefore; n != 2 {
		t.Fatalf("%d signatures for a sign across a new key, want 2 (one by the new key under the old kid, then one under its own)", n)
	}
	// The new token verifies; the old one only when the host hands the old key in.
	if _, err := s.client.Verify(ctx, connect.NewRequest(&tokenv1alpha1.VerifyRequest{Token: res.GetToken()})); err != nil {
		t.Fatal(err)
	}
	_, err = s.client.Verify(ctx, connect.NewRequest(&tokenv1alpha1.VerifyRequest{Token: first.GetToken()}))
	wantRefusal(t, err, connect.CodePermissionDenied, token.ReasonToken)
}

// second is another token block over the stack's seal block, as another instance of the host would be.
func (s *stack) second(t *testing.T) tokenv1alpha1connect.TokenServiceClient {
	t.Helper()
	b, err := token.New(context.Background(), token.Options{Seal: s.seal, Purposes: conformance.Purposes(), Rules: token.AllowAll()})
	if err != nil {
		t.Fatal(err)
	}
	hc, stop := socket.InProcess(b.Handler())
	t.Cleanup(stop)
	return tokenv1alpha1connect.NewTokenServiceClient(hc, "http://token", call.ClientOptions()...)
}

func TestVerifyReadsTheKeyAgainForAnUnknownKid(t *testing.T) {
	ctx := context.Background()
	s := kitStack(t)
	s.keys.Replace(conformance.PurposeES256) // behind the first block, which keeps the old key
	other := s.second(t)
	res, err := other.Sign(ctx, connect.NewRequest(&tokenv1alpha1.SignRequest{Purpose: conformance.PurposeES256, Typ: conformance.TypeKit, ClaimsJson: []byte(`{}`)}))
	if err != nil {
		t.Fatal(err)
	}
	before := s.keys.PublicKeys.Load()
	got, err := s.client.Verify(ctx, connect.NewRequest(&tokenv1alpha1.VerifyRequest{Token: res.Msg.GetToken()}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg.GetPurpose() != conformance.PurposeES256 {
		t.Fatalf("verified as %q", got.Msg.GetPurpose())
	}
	if n := s.keys.PublicKeys.Load() - before; n != int64(len(conformance.Purposes())) {
		t.Fatalf("%d PublicKey calls, want one read again of each purpose", n)
	}
	// A kid that no key has costs one read again of each purpose too, and is refused.
	// {"alg":"ES256","kid":"none","typ":"kit+jwt"}, {} and a signature of three octets.
	unknown := "eyJhbGciOiJFUzI1NiIsImtpZCI6Im5vbmUiLCJ0eXAiOiJraXQrand0In0.e30.AAAA"
	_, err = s.client.Verify(ctx, connect.NewRequest(&tokenv1alpha1.VerifyRequest{Token: unknown}))
	wantRefusal(t, err, connect.CodePermissionDenied, token.ReasonToken)
}

func TestASealBlockThatDoesNotAnswer(t *testing.T) {
	ctx := context.Background()
	s := kitStack(t)
	s.keys.Fail.Store(true)
	_, err := sign(ctx, s, conformance.PurposeES384, conformance.TypeJWT, `{}`)
	wantRefusal(t, err, connect.CodeUnavailable, token.ReasonSeal)
	_, err = s.client.KeySet(ctx, connect.NewRequest(&tokenv1alpha1.KeySetRequest{}))
	wantRefusal(t, err, connect.CodeUnavailable, token.ReasonSeal)
}

func TestTheSealBlocksOwnRefusalGoesToTheCaller(t *testing.T) {
	ctx := context.Background()
	// sluis key@, the host of seal, checks what it signs; its refusal reaches the caller of the token block as it is.
	sealRules := seal.RulesFunc(func(_ context.Context, r seal.Request) (seal.Decision, error) {
		if r.Op == seal.OpSign && bytes.Contains(r.Content, []byte(".")) && strings.Contains(string(r.Content), "eyJub3QiOnRydWV9") {
			return seal.Decision{}, refusal.New(connect.CodePermissionDenied, "content", "content")
		}
		return seal.Decision{}, nil
	})
	s := newStack(t, stackOptions{purposes: conformance.Purposes(), sealRules: sealRules})
	_, err := sign(ctx, s, conformance.PurposeES384, conformance.TypeJWT, `{"not":true}`)
	wantRefusal(t, err, connect.CodePermissionDenied, "content")
	if _, err := sign(ctx, s, conformance.PurposeES384, conformance.TypeJWT, `{"not":false}`); err != nil {
		t.Fatal(err)
	}
}

func TestASignatureThatDoesNotVerifyIsNotATokenOfTheBlock(t *testing.T) {
	ctx := context.Background()
	for _, p := range conformance.Purposes() {
		s := kitStack(t)
		s.keys.Lie(p.Name)
		_, err := sign(ctx, s, p.Name, p.Types[0], `{}`)
		wantRefusal(t, err, connect.CodeInternal, token.ReasonKey)
	}
}

func TestTheRulesSeeTheCallAndTheirConnectErrorGoesAsItIs(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	var seen []token.Request
	rules := token.RulesFunc(func(_ context.Context, r token.Request) error {
		mu.Lock()
		seen = append(seen, r)
		mu.Unlock()
		if r.Typ == conformance.TypeAccess {
			return connect.NewError(connect.CodeUnauthenticated, errors.New("the host's own"))
		}
		return nil
	})
	s := newStack(t, stackOptions{purposes: conformance.Purposes(), rules: rules})
	res, err := s.client.Sign(ctx, connect.NewRequest(&tokenv1alpha1.SignRequest{Purpose: conformance.PurposeES384, Typ: conformance.TypeJWT, ClaimsJson: []byte(`{"a":1}`), Authorization: []byte("grant")}))
	if err != nil || res == nil {
		t.Fatal(err)
	}
	_, err = sign(ctx, s, conformance.PurposeES384, conformance.TypeAccess, `{}`)
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("got %v, want the rules' own error", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || seen[0].Op != token.OpSign || seen[0].Purpose != conformance.PurposeES384 || seen[0].Typ != conformance.TypeJWT ||
		string(seen[0].Claims) != `{"a":1}` || string(seen[0].Authorization) != "grant" {
		t.Fatalf("the rules saw %+v", seen)
	}
}

func TestTheFacts(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	var facts []token.Event
	s := newStack(t, stackOptions{purposes: conformance.Purposes(), observe: func(_ context.Context, e token.Event) {
		mu.Lock()
		facts = append(facts, e)
		mu.Unlock()
	}})
	res, err := sign(ctx, s, conformance.PurposeES256, conformance.TypeKit, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = sign(ctx, s, conformance.PurposeES256, conformance.TypeJWT, `{}`)
	wantRefusal(t, err, connect.CodeInvalidArgument, token.ReasonTyp)
	mu.Lock()
	defer mu.Unlock()
	want := []token.Event{
		{Action: token.EventSign, Op: token.OpSign, Purpose: conformance.PurposeES256, Kid: res.GetKid()},
		{Action: token.EventRefuse, Op: token.OpSign, Purpose: conformance.PurposeES256, Reason: token.ReasonTyp},
	}
	if len(facts) != len(want) || facts[0] != want[0] || facts[1] != want[1] {
		t.Fatalf("facts %+v, want %+v", facts, want)
	}
}

func TestTheOptions(t *testing.T) {
	ctx := context.Background()
	s := kitStack(t)
	ok := conformance.Purposes()
	for name, o := range map[string]token.Options{
		"no seal":              {Purposes: ok, Rules: token.AllowAll()},
		"no rules":             {Seal: s.seal, Purposes: ok},
		"no purpose":           {Seal: s.seal, Rules: token.AllowAll()},
		"no type":              {Seal: s.seal, Rules: token.AllowAll(), Purposes: []token.Purpose{{Name: conformance.PurposeES384}}},
		"a bad type":           {Seal: s.seal, Rules: token.AllowAll(), Purposes: []token.Purpose{{Name: conformance.PurposeES384, Types: []string{"+jwt"}}}},
		"a type twice":         {Seal: s.seal, Rules: token.AllowAll(), Purposes: []token.Purpose{{Name: conformance.PurposeES384, Types: []string{"JWT", "JWT"}}}},
		"a purpose twice":      {Seal: s.seal, Rules: token.AllowAll(), Purposes: []token.Purpose{ok[0], ok[0]}},
		"a bad kid rule":       {Seal: s.seal, Rules: token.AllowAll(), Purposes: []token.Purpose{{Name: conformance.PurposeES384, Types: []string{"JWT"}, Kid: 7}}},
		"a bad algorithm":      {Seal: s.seal, Rules: token.AllowAll(), Purposes: []token.Purpose{{Name: conformance.PurposeES384, Types: []string{"JWT"}, Algorithm: "HS256"}}},
		"another algorithm":    {Seal: s.seal, Rules: token.AllowAll(), Purposes: []token.Purpose{{Name: conformance.PurposeES384, Types: []string{"JWT"}, Algorithm: purpose.ES256}}},
		"not a seal purpose":   {Seal: s.seal, Rules: token.AllowAll(), Purposes: []token.Purpose{{Name: "no-such", Types: []string{"JWT"}}}},
		"seal's wrapping only": {Seal: s.seal, Rules: token.AllowAll(), Purposes: []token.Purpose{{Name: seal.DefaultWrappingPurpose, Types: []string{"JWT"}}}},
	} {
		if _, err := token.New(ctx, o); err == nil {
			t.Errorf("%s: New took it", name)
		}
	}
	// The algorithm comes from the seal block when the host names none, and the caller's slice is left as it was.
	ps := []token.Purpose{{Name: conformance.PurposeRS256, Types: []string{"JWT"}}}
	if _, err := token.New(ctx, token.Options{Seal: s.seal, Rules: token.AllowAll(), Purposes: ps}); err != nil {
		t.Fatal(err)
	}
	if ps[0].Algorithm != "" {
		t.Fatal("New changed the caller's declaration")
	}
	if _, err := token.SealPurposes(ps); err == nil {
		t.Fatal("SealPurposes took a purpose with no algorithm")
	}
}

func TestTheDeclarationFile(t *testing.T) {
	raw := token.MarshalDeclaration(conformance.Purposes())
	got, err := token.ParseDeclaration(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	want := conformance.Purposes()
	if len(got) != len(want) {
		t.Fatalf("%d purposes back, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Name != want[i].Name || got[i].Algorithm != want[i].Algorithm || got[i].Kid != want[i].Kid || strings.Join(got[i].Types, ",") != strings.Join(want[i].Types, ",") {
			t.Fatalf("purpose %d came back as %+v", i, got[i])
		}
	}
	if p, err := token.ParseDeclaration(strings.NewReader(`{"purposes":[{"name":"a","types":["JWT"]}]}`)); err != nil || p[0].Kid != token.KidKeyID {
		t.Fatalf("the default kid rule: %v %+v", err, p)
	}
	for name, doc := range map[string]string{
		"an unknown member":         `{"purposes":[{"name":"a","types":["JWT"],"slot":"9c"}]}`,
		"an unknown kid rule":       `{"purposes":[{"name":"a","types":["JWT"],"kid":"x5t"}]}`,
		"a member twice":            `{"purposes":[{"name":"a","name":"b","types":["JWT"]}]}`,
		"no purpose":                `{"purposes":[]}`,
		"no type":                   `{"purposes":[{"name":"a"}]}`,
		"not an object":             `[]`,
		"text after the object":     `{"purposes":[{"name":"a","types":["JWT"]}]} x`,
		"an algorithm of no JWS":    `{"purposes":[{"name":"a","types":["JWT"],"algorithm":"ECDH-P384"}]}`,
		"a declaration of too much": `{"purposes":[{"name":"` + strings.Repeat("a", token.MaxDeclaration) + `"}]}`,
	} {
		if _, err := token.ParseDeclaration(strings.NewReader(doc)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

// TestSessionsShape is session's use: each purpose on a driver of its own (decision 0019 of session), behind one routing client that the
// seal block takes, and the two kid rules.
func TestSessionsShape(t *testing.T) {
	ctx := context.Background()
	ps := []token.Purpose{
		{Name: "client-assertion", Algorithm: purpose.ES256, Types: []string{"JWT"}},
		{Name: "identity-header", Algorithm: purpose.ES256, Types: []string{"session-identity+jwt"}},
	}
	sps, _ := token.SealPurposes(ps)
	decl := seal.KeyPurposes("", sps)
	file, kms := tokentest.NewKeys(nil), tokentest.NewKeys(nil)
	file.Store, kms.Store = "software", "aws-kms"
	for _, d := range decl {
		if d.Name == "client-assertion" {
			file.Decl = append(file.Decl, d)
		} else {
			kms.Decl = append(kms.Decl, d)
		}
	}
	client := func(f *tokentest.Keys) keyv1alpha1connect.KeyStoreServiceClient {
		hc, stop := socket.InProcess(keyv1alpha1connect.NewKeyStoreServiceHandler(f))
		t.Cleanup(stop)
		return keyv1alpha1connect.NewKeyStoreServiceClient(hc, "http://key", call.ClientOptions()...)
	}
	fc, kc := client(file), client(kms)
	keys, err := route.New(map[string]keyv1alpha1connect.KeyStoreServiceClient{"wrapping": kc, "identity-header": kc, "client-assertion": fc})
	if err != nil {
		t.Fatal(err)
	}
	s := newStack(t, stackOptions{purposes: ps, keys: keys})
	a, err := sign(ctx, s, "client-assertion", "JWT", `{"iss":"c","sub":"c"}`)
	if err != nil {
		t.Fatal(err)
	}
	h, err := sign(ctx, s, "identity-header", "session-identity+jwt", `{"sub":"p"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a.GetKid(), "software#") || !strings.HasPrefix(h.GetKid(), "aws-kms#") {
		t.Fatalf("the kids %q and %q are not the key_ids of their drivers", a.GetKid(), h.GetKid())
	}
	if file.Signs.Load() != 1 || kms.Signs.Load() != 1 {
		t.Fatalf("signatures: %d in the file driver, %d in KMS; want one each", file.Signs.Load(), kms.Signs.Load())
	}
	set, err := s.client.KeySet(ctx, connect.NewRequest(&tokenv1alpha1.KeySetRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Msg.GetKeys()) != 2 || set.Msg.GetKeys()[0].GetKid() != a.GetKid() || set.Msg.GetKeys()[1].GetKid() != h.GetKid() {
		t.Fatal("the key set is not the two purposes' keys")
	}
}
