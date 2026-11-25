package token_test

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	sealv1alpha1 "github.com/truvity/atoom/blocks/seal/gen/atoom/seal/v1alpha1"
	"github.com/truvity/atoom/blocks/token"
	tokenv1alpha1 "github.com/truvity/atoom/blocks/token/gen/atoom/token/v1alpha1"
	"github.com/truvity/atoom/blocks/token/gen/atoom/token/v1alpha1/tokenv1alpha1connect"
	"github.com/truvity/atoom/blocks/token/internal/tokentest"
	"github.com/truvity/atoom/common/call"
	"github.com/truvity/atoom/key/purpose"
	"github.com/truvity/atoom/socket"
)

// clock is a time a test moves.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

const ringName = "ring"

// ringPurpose is a ring purpose of the tests: ES256, kid the key_id, the slots given (none: its own name).
func ringPurpose(current string, slots ...string) token.Purpose {
	return token.Purpose{Name: ringName, Algorithm: purpose.ES256, Types: []string{"JWT"}, Ring: &token.Ring{
		Slots: slots, Current: current,
		Lead: 10 * time.Minute, Retain: 20 * time.Minute, CacheMaxAge: 5 * time.Minute, TokenLifetime: 15 * time.Minute,
	}}
}

// instance is another token block over a stack's seal block and store: another instance of the host, with its own declaration.
func (s *stack) instance(t *testing.T, p token.Purpose, node string, c *clock) tokenv1alpha1connect.TokenServiceClient {
	t.Helper()
	b, err := token.New(context.Background(), token.Options{Seal: s.seal, Purposes: []token.Purpose{p}, Rules: token.AllowAll(), Items: s.store, Node: node, Clock: c.now})
	if err != nil {
		t.Fatal(err)
	}
	hc, stop := socket.InProcess(b.Handler())
	t.Cleanup(stop)
	return tokenv1alpha1connect.NewTokenServiceClient(hc, "http://token", call.ClientOptions()...)
}

func ringSign(t *testing.T, c tokenv1alpha1connect.TokenServiceClient) (*tokenv1alpha1.SignResponse, error) {
	t.Helper()
	res, err := c.Sign(context.Background(), connect.NewRequest(&tokenv1alpha1.SignRequest{Purpose: ringName, Typ: "JWT", ClaimsJson: []byte(`{}`)}))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

func mustSign(t *testing.T, c tokenv1alpha1connect.TokenServiceClient) *tokenv1alpha1.SignResponse {
	t.Helper()
	res, err := ringSign(t, c)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// states is the ring as "kid-suffix:state" in order, through Reconcile.
func ringStates(t *testing.T, c tokenv1alpha1connect.TokenServiceClient, names map[string]string) string {
	t.Helper()
	res, err := c.Reconcile(context.Background(), connect.NewRequest(&tokenv1alpha1.ReconcileRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, k := range res.Msg.GetRings()[0].GetKeys() {
		n := names[k.GetKid()]
		if n == "" {
			n = "?"
		}
		out = append(out, n+":"+strings.TrimPrefix(strings.ToLower(k.GetState().String()), "key_state_"))
	}
	return strings.Join(out, " ")
}

func published(t *testing.T, c tokenv1alpha1connect.TokenServiceClient, names map[string]string) string {
	t.Helper()
	res, err := c.KeySet(context.Background(), connect.NewRequest(&tokenv1alpha1.KeySetRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, k := range res.Msg.GetKeys() {
		out = append(out, names[k.GetKid()])
	}
	return strings.Join(out, " ")
}

func kidOf(t *testing.T, s *stack, slot string) string {
	t.Helper()
	res, err := s.seal.PublicKey(context.Background(), connect.NewRequest(&sealv1alpha1.PublicKeyRequest{Purpose: slot}))
	if err != nil {
		t.Fatal(err)
	}
	return res.Msg.GetKeyId()
}

func verifyRing(c tokenv1alpha1connect.TokenServiceClient, tok string) error {
	_, err := c.Verify(context.Background(), connect.NewRequest(&tokenv1alpha1.VerifyRequest{Token: tok}))
	return err
}

func TestTheRingRotatesThroughASpareSlot(t *testing.T) {
	c := newClock()
	s := newStack(t, stackOptions{purposes: []token.Purpose{ringPurpose("ring-a", "ring-a", "ring-b")}, clock: c.now})
	names := map[string]string{kidOf(t, s, "ring-a"): "A", kidOf(t, s, "ring-b"): "B"}
	old := s.client // an instance of the configuration before the rotation: slot a current
	if got := ringStates(t, old, names); got != "A:active" {
		t.Fatalf("the ring is %q, want only the current slot's key, active", got)
	}
	tokA := mustSign(t, old)
	if names[tokA.GetKid()] != "A" {
		t.Fatal("not signed by slot a's key")
	}

	// The host makes slot b current and restarts one instance: b's key is published before it signs, and a signs meanwhile.
	c.add(time.Minute)
	next := s.instance(t, ringPurpose("ring-b", "ring-a", "ring-b"), "", c)
	if got := ringStates(t, next, names); got != "A:active B:pre_published" {
		t.Fatalf("after the switch the ring is %q", got)
	}
	if got := published(t, next, names); got != "A B" {
		t.Fatalf("the key set is %q, want both", got)
	}
	if names[mustSign(t, next).GetKid()] != "A" || names[mustSign(t, old).GetKid()] != "A" {
		t.Fatal("a key that is still published signed")
	}
	c.add(9 * time.Minute)
	if got := ringStates(t, old, names); got != "A:active B:pre_published" {
		t.Fatalf("before the lead the ring is %q; the old instance must not retire the new key", got)
	}

	// After the lead, the current slot's key signs and the earlier one retires; the old instance signs with the new key too.
	c.add(time.Minute)
	if got := ringStates(t, next, names); got != "A:retiring B:active" {
		t.Fatalf("after the lead the ring is %q", got)
	}
	if names[mustSign(t, next).GetKid()] != "B" || names[mustSign(t, old).GetKid()] != "B" {
		t.Fatal("not signed by the new key")
	}
	if err := verifyRing(next, tokA.GetToken()); err != nil {
		t.Fatalf("a token of the retiring key: %v", err)
	}
	if got := published(t, old, names); got != "A B" {
		t.Fatalf("the key set is %q, want the retiring key kept", got)
	}

	// After the retention the retiring key leaves the key set, and its tokens no longer verify.
	c.add(20 * time.Minute)
	if got := ringStates(t, old, names); got != "A:dropped B:active" {
		t.Fatalf("after the retention the ring is %q", got)
	}
	if got := published(t, next, names); got != "B" {
		t.Fatalf("the key set is %q", got)
	}
	wantRefusal(t, verifyRing(next, tokA.GetToken()), connect.CodePermissionDenied, token.ReasonToken)

	// Slot a still holds the dropped key: making it current again does not bring it back, and b keeps signing.
	again := s.instance(t, ringPurpose("ring-a", "ring-a", "ring-b"), "", c)
	if got := ringStates(t, again, names); got != "A:dropped B:active" || names[mustSign(t, again).GetKid()] != "B" {
		t.Fatalf("a dropped key presented again: the ring is %q", got)
	}
}

// replica is a stack of its own keys over a shared store: another replica of a host whose driver holds one key at a time.
func replica(t *testing.T, items *tokentest.Items, p token.Purpose, node string, c *clock) *stack {
	t.Helper()
	return newStack(t, stackOptions{purposes: []token.Purpose{p}, items: items, node: node, clock: c.now})
}

func TestARestartRotationSignsAtOnceAndKeepsThePreviousKey(t *testing.T) {
	c := newClock()
	items := tokentest.NewItems()
	p := ringPurpose("")
	p.Ring.AtOnce = true
	p.Ring.Overlap = 2 * time.Minute
	r1 := replica(t, items, p, "", c)
	names := map[string]string{kidOf(t, r1, ringName): "K1"}
	tok1 := mustSign(t, r1.client)

	// A restart onto a new key: the new key signs at once, the previous one stays published and signs on the replicas not yet restarted
	// for the overlap.
	c.add(time.Hour)
	var facts []token.Event
	r2 := newStack(t, stackOptions{purposes: []token.Purpose{p}, items: items, clock: c.now, observe: func(_ context.Context, e token.Event) {
		if e.Action == token.EventRing {
			facts = append(facts, e)
		}
	}})
	names[kidOf(t, r2, ringName)] = "K2"
	if len(facts) != 1 || facts[0].Change != token.ChangeJoined || facts[0].State != "active" || facts[0].Reason != token.ReasonShortcut {
		t.Fatalf("the facts of the join: %+v", facts)
	}
	if got := ringStates(t, r2.client, names); got != "K1:active K2:active" {
		t.Fatalf("after the restart the ring is %q", got)
	}
	if names[mustSign(t, r2.client).GetKid()] != "K2" || names[mustSign(t, r1.client).GetKid()] != "K1" {
		t.Fatal("each replica signs with its own key during the overlap")
	}
	c.add(2 * time.Minute)
	if got := ringStates(t, r2.client, names); got != "K1:retiring K2:active" {
		t.Fatalf("after the overlap the ring is %q", got)
	}
	_, err := ringSign(t, r1.client)
	wantRefusal(t, err, connect.CodeUnavailable, token.ReasonNotActive)
	if got := published(t, r2.client, names); got != "K1 K2" {
		t.Fatalf("the key set is %q, want the previous key kept", got)
	}
	if err := verifyRing(r2.client, tok1.GetToken()); err != nil {
		t.Fatalf("a token of the previous key: %v", err)
	}
	c.add(20 * time.Minute)
	if got := published(t, r2.client, names); got != "K2" {
		t.Fatalf("after the retention the key set is %q", got)
	}
}

func TestARestartWithoutAtOnceWaitsForTheLead(t *testing.T) {
	c := newClock()
	items := tokentest.NewItems()
	r1 := replica(t, items, ringPurpose(""), "", c)
	names := map[string]string{kidOf(t, r1, ringName): "K1"}
	c.add(time.Hour)
	r2 := replica(t, items, ringPurpose(""), "", c)
	names[kidOf(t, r2, ringName)] = "K2"
	if got := ringStates(t, r2.client, names); got != "K1:active K2:pre_published" {
		t.Fatalf("the ring is %q", got)
	}
	_, err := ringSign(t, r2.client)
	wantRefusal(t, err, connect.CodeUnavailable, token.ReasonNotActive)
	c.add(10 * time.Minute)
	if names[mustSign(t, r2.client).GetKid()] != "K2" {
		t.Fatal("not signed by the new key after the lead")
	}
	if got := ringStates(t, r1.client, names); got != "K1:retiring K2:active" {
		t.Fatalf("the ring is %q", got)
	}
}

func TestAReplacementSignsAtOnceWhenNoKeyIsActive(t *testing.T) {
	c := newClock()
	s := newStack(t, stackOptions{purposes: []token.Purpose{ringPurpose("ring-a", "ring-a", "ring-b")}, clock: c.now})
	names := map[string]string{kidOf(t, s, "ring-a"): "A", kidOf(t, s, "ring-b"): "B"}
	tokA := mustSign(t, s.client)
	c.add(time.Minute)
	next := s.instance(t, ringPurpose("ring-b", "ring-a", "ring-b"), "", c)
	if got := ringStates(t, next, names); got != "A:active B:pre_published" {
		t.Fatalf("the ring is %q", got)
	}
	ctx := context.Background()
	res, err := next.Revoke(ctx, connect.NewRequest(&tokenv1alpha1.RevokeRequest{Purpose: ringName, Kid: tokA.GetKid()}))
	if err != nil || !res.Msg.GetChanged() {
		t.Fatalf("revoke: %v", err)
	}
	rec, err := next.Reconcile(ctx, connect.NewRequest(&tokenv1alpha1.ReconcileRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if ch := rec.Msg.GetChanges(); len(ch) != 1 || ch[0].GetChange() != token.ChangeActivated || ch[0].GetReason() != token.ReasonShortcut || names[ch[0].GetKid()] != "B" {
		t.Fatalf("the changes %v, want B activated by the shortcut", ch)
	}
	if names[mustSign(t, s.client).GetKid()] != "B" || published(t, s.client, names) != "B" {
		t.Fatal("the replacement does not sign, or the revoked key is published")
	}
	wantRefusal(t, verifyRing(s.client, tokA.GetToken()), connect.CodePermissionDenied, token.ReasonToken)
}

func TestAKidRevokedBeforeItIsSeenNeverJoins(t *testing.T) {
	c := newClock()
	s := newStack(t, stackOptions{purposes: []token.Purpose{ringPurpose("ring-a", "ring-a", "ring-b")}, clock: c.now})
	names := map[string]string{kidOf(t, s, "ring-a"): "A", kidOf(t, s, "ring-b"): "B"}
	if _, err := s.client.Revoke(context.Background(), connect.NewRequest(&tokenv1alpha1.RevokeRequest{Purpose: ringName, Kid: kidOf(t, s, "ring-b")})); err != nil {
		t.Fatal(err)
	}
	next := s.instance(t, ringPurpose("ring-b", "ring-a", "ring-b"), "", c)
	if got := ringStates(t, next, names); got != "A:active B:revoked" {
		t.Fatalf("the ring is %q", got)
	}
	if names[mustSign(t, next).GetKid()] != "A" {
		t.Fatal("the instance does not sign with the other slot's active key")
	}
}

func TestEachNodeMovesItsOwnKeys(t *testing.T) {
	c := newClock()
	items := tokentest.NewItems()
	p := ringPurpose("")
	p.Ring.Bootstrap = 15 * time.Minute
	n1 := replica(t, items, p, "n1", c)
	c.add(time.Minute)
	n2 := replica(t, items, p, "n2", c) // a first deploy: the ring is young
	names := map[string]string{kidOf(t, n1, ringName): "N1", kidOf(t, n2, ringName): "N2"}
	if got := ringStates(t, n2.client, names); got != "N1:active N2:active" {
		t.Fatalf("a first deploy of two nodes: the ring is %q", got)
	}
	if names[mustSign(t, n1.client).GetKid()] != "N1" || names[mustSign(t, n2.client).GetKid()] != "N2" {
		t.Fatal("each node signs with its own key")
	}
	// A node that joins after the window is published for the lead, and the others are not touched.
	c.add(20 * time.Minute)
	n3 := replica(t, items, p, "n3", c)
	names[kidOf(t, n3, ringName)] = "N3"
	if got := ringStates(t, n1.client, names); got != "N1:active N2:active N3:pre_published" {
		t.Fatalf("a late node: the ring is %q", got)
	}
	// n2 restarts onto a new key: it waits for its lead, n1 keeps signing, and only n2's earlier key retires.
	n2b := replica(t, items, p, "n2", c)
	names[kidOf(t, n2b, ringName)] = "N2b"
	c.add(10 * time.Minute)
	if got := ringStates(t, n1.client, names); got != "N1:active N2:active N3:pre_published N2b:pre_published" {
		t.Fatalf("n1 moved another node's keys: the ring is %q", got)
	}
	if got := ringStates(t, n2b.client, names); got != "N1:active N2:retiring N3:pre_published N2b:active" {
		t.Fatalf("after n2's lead the ring is %q", got)
	}
	if names[mustSign(t, n1.client).GetKid()] != "N1" || names[mustSign(t, n3.client).GetKid()] != "N3" {
		t.Fatal("the other nodes sign with their own keys")
	}
	res, err := n1.client.Capabilities(context.Background(), connect.NewRequest(&tokenv1alpha1.CapabilitiesRequest{}))
	if err != nil || res.Msg.GetPurposes()[0].GetRing().GetNode() != "n1" || res.Msg.GetPurposes()[0].GetRing().GetBootstrapSeconds() != 900 {
		t.Fatalf("the capabilities name another node or bootstrap: %v", err)
	}
}

func TestConfirmHoldsActivation(t *testing.T) {
	c := newClock()
	p := func(current string) token.Purpose {
		r := ringPurpose(current, "ring-a", "ring-b")
		r.Ring.Confirm = true
		return r
	}
	s := newStack(t, stackOptions{purposes: []token.Purpose{p("ring-a")}, clock: c.now})
	names := map[string]string{kidOf(t, s, "ring-a"): "A", kidOf(t, s, "ring-b"): "B"}
	// The first key waits for its confirmation too, but only for it.
	if got := ringStates(t, s.client, names); got != "A:pre_published" {
		t.Fatalf("the ring is %q", got)
	}
	ctx := context.Background()
	conf, err := s.client.ConfirmPublished(ctx, connect.NewRequest(&tokenv1alpha1.ConfirmPublishedRequest{Purpose: ringName, Kids: []string{kidOf(t, s, "ring-a")}}))
	if err != nil || len(conf.Msg.GetConfirmed()) != 1 {
		t.Fatalf("confirm: %v", err)
	}
	if got := ringStates(t, s.client, names); got != "A:active" {
		t.Fatalf("a confirmed first key: the ring is %q", got)
	}
	next := s.instance(t, p("ring-b"), "", c)
	c.add(time.Hour)
	if got := ringStates(t, next, names); got != "A:active B:pre_published" {
		t.Fatalf("an unconfirmed key became active: the ring is %q", got)
	}
	at := c.now().Add(-time.Minute)
	if _, err := next.ConfirmPublished(ctx, connect.NewRequest(&tokenv1alpha1.ConfirmPublishedRequest{Purpose: ringName, Kids: []string{kidOf(t, s, "ring-b")}, AtUnixMs: at.UnixMilli()})); err != nil {
		t.Fatal(err)
	}
	// The lead counts from the confirmation; a second confirmation does not move it.
	c.add(8 * time.Minute)
	if res, err := next.ConfirmPublished(ctx, connect.NewRequest(&tokenv1alpha1.ConfirmPublishedRequest{Purpose: ringName, Kids: []string{kidOf(t, s, "ring-b")}})); err != nil || len(res.Msg.GetConfirmed()) != 0 {
		t.Fatalf("a second confirmation counted: %v", err)
	}
	if got := ringStates(t, next, names); got != "A:active B:pre_published" {
		t.Fatalf("before the lead from the confirmation: %q", got)
	}
	c.add(time.Minute)
	if got := ringStates(t, next, names); got != "A:retiring B:active" {
		t.Fatalf("after the lead from the confirmation: %q", got)
	}
}

func TestTheRingFailsClosed(t *testing.T) {
	ctx := context.Background()
	s := newStack(t, stackOptions{purposes: []token.Purpose{ringPurpose("ring-a", "ring-a", "ring-b")}})
	s.items.Fail.Store(true)
	_, err := ringSign(t, s.client)
	wantRefusal(t, err, connect.CodeUnavailable, token.ReasonRing)
	_, err = s.client.KeySet(ctx, connect.NewRequest(&tokenv1alpha1.KeySetRequest{}))
	wantRefusal(t, err, connect.CodeUnavailable, token.ReasonRing)
	_, err = s.client.Ring(ctx, connect.NewRequest(&tokenv1alpha1.RingRequest{}))
	wantRefusal(t, err, connect.CodeUnavailable, token.ReasonRing)
	_, err = s.client.Revoke(ctx, connect.NewRequest(&tokenv1alpha1.RevokeRequest{Purpose: ringName, Kid: "k"}))
	wantRefusal(t, err, connect.CodeUnavailable, token.ReasonRing)
	// A ring item that does not decode is no ring either.
	s.items.Fail.Store(false)
	s.items.Put(token.DefaultRingPrefix+ringName, []byte("not a ring"))
	_, err = ringSign(t, s.client)
	wantRefusal(t, err, connect.CodeUnavailable, token.ReasonRing)
}

func TestALostRaceReadsAgain(t *testing.T) {
	c := newClock()
	s := newStack(t, stackOptions{purposes: []token.Purpose{ringPurpose("ring-a", "ring-a", "ring-b")}, clock: c.now})
	key := token.DefaultRingPrefix + ringName
	once := sync.Once{}
	s.items.BeforeWrite = func() { once.Do(func() { s.items.Put(key, s.items.Value(key)) }) } // another writer, the same ring
	next := s.instance(t, ringPurpose("ring-b", "ring-a", "ring-b"), "", c)
	names := map[string]string{kidOf(t, s, "ring-a"): "A", kidOf(t, s, "ring-b"): "B"}
	if got := ringStates(t, next, names); got != "A:active B:pre_published" {
		t.Fatalf("after a lost race the ring is %q", got)
	}
	// A store that loses every race is a refusal, not a loop.
	s.items.BeforeWrite = func() { s.items.Put(key, s.items.Value(key)) }
	_, err := next.Revoke(context.Background(), connect.NewRequest(&tokenv1alpha1.RevokeRequest{Purpose: ringName, Kid: "kid-x"}))
	wantRefusal(t, err, connect.CodeUnavailable, token.ReasonRing)
}

func TestKeyChainsByKid(t *testing.T) {
	c := newClock()
	s := newStack(t, stackOptions{purposes: []token.Purpose{ringPurpose("ring-a", "ring-a", "ring-b")}, clock: c.now})
	next := s.instance(t, ringPurpose("ring-b", "ring-a", "ring-b"), "", c)
	a, b := kidOf(t, s, "ring-a"), kidOf(t, s, "ring-b")
	res, err := next.KeySet(context.Background(), connect.NewRequest(&tokenv1alpha1.KeySetRequest{
		Chains:    map[string]*tokenv1alpha1.CertificateChain{ringName: {X5C: [][]byte{[]byte("signing")}}},
		KeyChains: map[string]*tokenv1alpha1.CertificateChain{b: {X5C: [][]byte{[]byte("next")}}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(res.Msg.GetJwksJson(), []byte(`"kid":"`+a+`","alg":"ES256","use":"sig","x5c":["c2lnbmluZw=="]`)) ||
		!bytes.Contains(res.Msg.GetJwksJson(), []byte(`"kid":"`+b+`","alg":"ES256","use":"sig","x5c":["bmV4dA=="]`)) {
		t.Fatalf("the chains are not on their keys: %s", res.Msg.GetJwksJson())
	}
	keys := res.Msg.GetKeys()
	if keys[0].GetState() != tokenv1alpha1.KeyState_KEY_STATE_ACTIVE || keys[1].GetState() != tokenv1alpha1.KeyState_KEY_STATE_PRE_PUBLISHED {
		t.Fatalf("the states of the published keys: %v", keys)
	}
}

func TestTheRingOptions(t *testing.T) {
	ctx := context.Background()
	s := newStack(t, stackOptions{purposes: []token.Purpose{ringPurpose("ring-a", "ring-a", "ring-b")}})
	with := func(f func(*token.Ring)) []token.Purpose {
		p := ringPurpose("ring-a", "ring-a", "ring-b")
		f(p.Ring)
		return []token.Purpose{p}
	}
	for what, ps := range map[string][]token.Purpose{
		"a lead below the cache":         with(func(r *token.Ring) { r.Lead = r.CacheMaxAge - time.Second }),
		"a retention below the lifetime": with(func(r *token.Ring) { r.Retain = r.TokenLifetime - time.Second }),
		"no cache figure":                with(func(r *token.Ring) { r.CacheMaxAge, r.Lead = 0, 0 }),
		"no lifetime":                    with(func(r *token.Ring) { r.TokenLifetime = 0 }),
		"a current slot that is none":    with(func(r *token.Ring) { r.Current = "ring-c" }),
		"a slot twice":                   with(func(r *token.Ring) { r.Slots = []string{"ring-a", "ring-a"} }),
		"at once and confirm":            with(func(r *token.Ring) { r.AtOnce, r.Confirm = true, true }),
		"a negative overlap":             with(func(r *token.Ring) { r.Overlap = -time.Second }),
		"a slot the seal does not serve": with(func(r *token.Ring) { r.Slots = []string{"ring-a", "ring-c"} }),
		"a slot of two purposes": append(with(func(*token.Ring) {}),
			token.Purpose{Name: "ring-b", Algorithm: purpose.ES256, Types: []string{"JWT"}}),
	} {
		if _, err := token.New(ctx, token.Options{Seal: s.seal, Purposes: ps, Rules: token.AllowAll(), Items: s.store}); err == nil {
			t.Errorf("%s: accepted", what)
		}
	}
	if _, err := token.New(ctx, token.Options{Seal: s.seal, Purposes: with(func(*token.Ring) {}), Rules: token.AllowAll()}); err == nil {
		t.Error("a ring without a store: accepted")
	}
	if _, err := token.New(ctx, token.Options{Seal: s.seal, Purposes: with(func(*token.Ring) {}), Rules: token.AllowAll(), Items: s.store, RingPrefix: "a b"}); err == nil {
		t.Error("a ring prefix with a space: accepted")
	}
}

func TestTheDeclarationCarriesTheRing(t *testing.T) {
	p := ringPurpose("ring-b", "ring-a", "ring-b")
	p.Ring.Bootstrap, p.Ring.Overlap, p.Ring.AtOnce = 15*time.Minute, 90*time.Second, true
	plain := token.Purpose{Name: "plain", Algorithm: purpose.ES384, Types: []string{"JWT"}}
	raw := token.MarshalDeclaration([]token.Purpose{p, plain})
	got, err := token.ParseDeclaration(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	r := got[0].Ring
	if r == nil || got[1].Ring != nil || strings.Join(r.Slots, ",") != "ring-a,ring-b" || r.Current != "ring-b" || r.Lead != 10*time.Minute ||
		r.Retain != 20*time.Minute || r.CacheMaxAge != 5*time.Minute || r.TokenLifetime != 15*time.Minute || r.Bootstrap != 15*time.Minute ||
		r.Overlap != 90*time.Second || !r.AtOnce || r.Confirm {
		t.Fatalf("the ring did not come back: %+v\n%s", r, raw)
	}
	for _, bad := range []string{
		`{"purposes":[{"name":"r","types":["JWT"],"ring":{"lead":"ten","retain":"1h","cache_max_age":"5m","token_lifetime":"1h"}}]}`,
		`{"purposes":[{"name":"r","types":["JWT"],"ring":{"retain":"1h","cache_max_age":"5m","token_lifetime":"1h"}}]}`,
		`{"purposes":[{"name":"r","types":["JWT"],"ring":{"lead":"1m","retain":"1h","cache_max_age":"5m","token_lifetime":"1h"}}]}`,
		`{"purposes":[{"name":"r","types":["JWT"],"ring":{"lead":"10m","retain":"1h","cache_max_age":"5m","token_lifetime":"1h","x":1}}]}`,
	} {
		if _, err := token.ParseDeclaration(strings.NewReader(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}
