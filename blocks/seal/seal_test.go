package seal_test

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/blocks/seal"
	"github.com/truvity/atoom/blocks/seal/conformance"
	sealv1alpha1 "github.com/truvity/atoom/blocks/seal/gen/atoom/seal/v1alpha1"
	"github.com/truvity/atoom/blocks/seal/gen/atoom/seal/v1alpha1/sealv1alpha1connect"
	"github.com/truvity/atoom/common/call"
	"github.com/truvity/atoom/common/refusal"
	"github.com/truvity/atoom/key/gen/atoom/key/v1alpha1/keyv1alpha1connect"
	"github.com/truvity/atoom/key/purpose"
	"github.com/truvity/atoom/socket"
	"github.com/truvity/atoom/store/gen/atoom/store/v1alpha1/storev1alpha1connect"
)

// env is a block over stand-ins of its two ports, every hop a ConnectRPC call on an in-memory transport, as a host links them.
type env struct {
	keys   *fakeKeys
	items  *fakeItems
	block  *seal.Block
	client sealv1alpha1connect.SealServiceClient
}

type setup struct {
	purposes   []seal.Purpose
	rules      seal.Rules
	recipients seal.Recipients
	items      *fakeItems
	ttl        time.Duration
	now        func() time.Time
	observe    func(context.Context, seal.Event)
	limit      uint64
	reserve    uint32
}

func start(t *testing.T, s setup) *env {
	t.Helper()
	if s.purposes == nil {
		s.purposes = conformance.Purposes()
	}
	if s.rules == nil {
		s.rules = conformance.Rules()
	}
	if s.items == nil {
		s.items = newFakeItems()
	}
	if s.limit == 0 {
		s.limit, s.reserve = conformance.SealsPerGeneration, conformance.SealReservation
	}
	keys := newFakeKeys(seal.KeyPurposes("", s.purposes))
	khc, kstop := socket.InProcess(keyv1alpha1connect.NewKeyStoreServiceHandler(keys))
	t.Cleanup(kstop)
	ihc, istop := socket.InProcess(storev1alpha1connect.NewItemServiceHandler(s.items))
	t.Cleanup(istop)
	b, err := seal.New(context.Background(), seal.Options{
		Keys:               keyv1alpha1connect.NewKeyStoreServiceClient(khc, "http://key", call.ClientOptions()...),
		Items:              storev1alpha1connect.NewItemServiceClient(ihc, "http://store", call.ClientOptions()...),
		Purposes:           s.purposes,
		Rules:              s.rules,
		Installation:       "test",
		Recipients:         s.recipients,
		CacheTTL:           s.ttl,
		Now:                s.now,
		Observe:            s.observe,
		SealsPerGeneration: s.limit,
		SealReservation:    s.reserve,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	shc, sstop := socket.InProcess(b.Handler())
	t.Cleanup(sstop)
	return &env{keys: keys, items: s.items, block: b, client: sealv1alpha1connect.NewSealServiceClient(shc, "http://seal", call.ClientOptions()...)}
}

func (e *env) seal(t *testing.T, scope string, pt string) []byte {
	t.Helper()
	res, err := e.client.Seal(context.Background(), connect.NewRequest(&sealv1alpha1.SealRequest{Purpose: conformance.PurposeSeal, Scope: scope, Address: []string{"a"}, Plaintext: []byte(pt)}))
	if err != nil {
		t.Fatal(err)
	}
	return res.Msg.GetEnvelope()
}

func (e *env) open(scope string, envl []byte) (string, error) {
	res, err := e.client.Open(context.Background(), connect.NewRequest(&sealv1alpha1.OpenRequest{Items: []*sealv1alpha1.OpenItem{
		{Purpose: conformance.PurposeSeal, Scope: scope, Address: []string{"a"}, Envelope: envl},
	}}))
	if err != nil {
		return "", err
	}
	return string(res.Msg.GetPlaintexts()[0]), nil
}

func reasonOf(err error) string {
	r, _ := refusal.Reason(err)
	return r
}

// The block passes its own kit over the stand-ins of its ports.
func TestTheBlockPassesItsKit(t *testing.T) {
	e := start(t, setup{})
	var out bytes.Buffer
	if err := conformance.Run(context.Background(), e.client, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	t.Log(out.String())
	// A second run against the same store starts clean: its scopes are its own.
	out.Reset()
	if err := conformance.Run(context.Background(), e.client, &out); err != nil {
		t.Fatalf("second run: %v\n%s", err, out.String())
	}
}

// The kit fails a block that does not keep its word: here one whose seals do not bind the address (a host that strips it), and one
// that serves no purpose of the kit.
func TestTheKitFailsABlockThatBreaksTheContract(t *testing.T) {
	e := start(t, setup{})
	strip := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			switch m := req.Any().(type) {
			case *sealv1alpha1.SealRequest:
				m.Address = []string{"fixed"}
			case *sealv1alpha1.OpenRequest:
				for _, it := range m.GetItems() {
					if len(it.GetAddress()) > 0 && len(it.GetAddress()) <= seal.MaxAddressParts && len(it.GetAddress()[0]) <= 1024 {
						it.Address = []string{"fixed"}
					}
				}
			}
			return next(ctx, req)
		}
	})
	hc, stop := socket.InProcess(e.block.Handler(connect.WithInterceptors(strip)))
	defer stop()
	bad := sealv1alpha1connect.NewSealServiceClient(hc, "http://seal")
	var out bytes.Buffer
	if err := conformance.Run(context.Background(), bad, &out); err == nil || !strings.Contains(out.String(), "FAIL bound_to_its_address") {
		t.Fatalf("a block that does not bind the address passed:\n%s", out.String())
	}
	other := start(t, setup{purposes: []seal.Purpose{{Name: "only", Use: seal.UseSeal, Code: 1}}, rules: seal.AllowAll()})
	out.Reset()
	if err := conformance.RunCases(context.Background(), other.client, conformance.Cases()[:1], &out); err == nil {
		t.Fatalf("a block without the kit's purposes passed:\n%s", out.String())
	}
	if err := conformance.RunCases(context.Background(), other.client, nil, &out); err == nil {
		t.Fatal("a run of no case passed")
	}
}

// The wraps are items of the store port under the block's prefix, one per recipient, and their value is ciphertext.
func TestTheWrapsAreItemsOfTheStore(t *testing.T) {
	e := start(t, setup{})
	envl := e.seal(t, "acme", "secret value")
	keys := e.items.keysWith("seal/wrap/")
	want := "seal/wrap/acme/" + conformance.PurposeSeal + "/1/" + e.block.Recipient()
	if len(keys) != 1 || keys[0] != want {
		t.Fatalf("the store holds %q, want [%s]", keys, want)
	}
	for _, it := range e.items.items {
		if bytes.Contains(it.GetValue(), []byte("secret value")) {
			t.Fatal("a plaintext is in the store")
		}
	}
	if bytes.Contains(envl, []byte("secret value")) {
		t.Fatal("the envelope holds the plaintext")
	}
	e.seal(t, "", "own")
	if got := e.items.keysWith("seal/wrap/-/"); len(got) != 1 {
		t.Fatalf("the installation's own scope is %q, want one wrap under seal/wrap/-/", got)
	}
}

// Callers that seal in a new scope at once make one key: one maker writes it, and the others open the same.
func TestConcurrentSealsInANewScopeMakeOneKey(t *testing.T) {
	e := start(t, setup{})
	var wg sync.WaitGroup
	envs := make([][]byte, 32)
	for i := range envs {
		wg.Go(func() { envs[i] = e.seal(t, "fresh", "v") })
	}
	wg.Wait()
	if keys := e.items.keysWith("seal/wrap/fresh/"); len(keys) != 1 {
		t.Fatalf("%d wraps, want 1", len(keys))
	}
	for _, envl := range envs {
		if got, err := e.open("fresh", envl); err != nil || got != "v" {
			t.Fatalf("open: %q, %v", got, err)
		}
	}
}

// Two instances share a store and list each other as recipients: a key one makes is wrapped for both, and the other opens what the
// first sealed. A recipient added later gets its wrap from a Rewrap of an instance that opens the key, and not before.
func TestRecipientsAndRewrap(t *testing.T) {
	items := newFakeItems()
	var mu sync.Mutex
	var list []*ecdh.PublicKey
	recipients := func(context.Context) ([]*ecdh.PublicKey, error) {
		mu.Lock()
		defer mu.Unlock()
		return append([]*ecdh.PublicKey(nil), list...), nil
	}
	a := start(t, setup{items: items, recipients: recipients})
	b := start(t, setup{items: items, recipients: recipients})
	// A is its only recipient at first: a key made now is wrapped for A alone.
	mu.Lock()
	list = []*ecdh.PublicKey{a.block.WrappingKey()}
	mu.Unlock()
	early := a.seal(t, "t1", "early")
	if _, err := b.open("t1", early); connect.CodeOf(err) != connect.CodeUnavailable || reasonOf(err) != seal.ReasonWrap {
		t.Fatalf("B opened a key not wrapped for it, or refused otherwise: %v", err)
	}
	// B cannot make a key while it is not a recipient.
	if _, err := b.client.Seal(context.Background(), connect.NewRequest(&sealv1alpha1.SealRequest{Purpose: conformance.PurposeSeal, Scope: "t9", Address: []string{"a"}})); reasonOf(err) != seal.ReasonWrap {
		t.Fatalf("B made a key it would not be able to open again: %v", err)
	}
	mu.Lock()
	list = append(list, b.block.WrappingKey())
	mu.Unlock()
	// A key made now is wrapped for both.
	both := a.seal(t, "t2", "both")
	if got, err := b.open("t2", both); err != nil || got != "both" {
		t.Fatalf("B: %q, %v", got, err)
	}
	// B rewraps nothing it cannot open; A wraps t1 for B.
	res, err := b.client.Rewrap(context.Background(), connect.NewRequest(&sealv1alpha1.RewrapRequest{}))
	if err != nil || res.Msg.GetWritten() != 0 {
		t.Fatalf("B's pass: %v, %v", res, err)
	}
	res, err = a.client.Rewrap(context.Background(), connect.NewRequest(&sealv1alpha1.RewrapRequest{}))
	if err != nil || res.Msg.GetWritten() != 1 {
		t.Fatalf("A's pass: %v, %v", res, err)
	}
	if got, err := b.open("t1", early); err != nil || got != "early" {
		t.Fatalf("B after the rewrap: %q, %v", got, err)
	}
	// A list without this instance makes no key.
	mu.Lock()
	list = []*ecdh.PublicKey{b.block.WrappingKey()}
	mu.Unlock()
	if _, err := a.client.Seal(context.Background(), connect.NewRequest(&sealv1alpha1.SealRequest{Purpose: conformance.PurposeSeal, Scope: "t3", Address: []string{"a"}})); reasonOf(err) != seal.ReasonWrap {
		t.Fatalf("a key was made for a list without its maker: %v", err)
	}
}

// An Erase on one instance stops the other from opening at its next Rewrap, before its cache entry would end.
func TestAnEraseElsewhereIsSeenAtTheNextRewrap(t *testing.T) {
	items := newFakeItems()
	var list []*ecdh.PublicKey
	recipients := func(context.Context) ([]*ecdh.PublicKey, error) { return list, nil }
	a := start(t, setup{items: items, recipients: recipients})
	b := start(t, setup{items: items, recipients: recipients})
	list = []*ecdh.PublicKey{a.block.WrappingKey(), b.block.WrappingKey()}
	envl := a.seal(t, "gone", "v")
	if _, err := b.open("gone", envl); err != nil {
		t.Fatal(err)
	}
	if _, err := a.client.Erase(context.Background(), connect.NewRequest(&sealv1alpha1.EraseRequest{Scope: "gone"})); err != nil {
		t.Fatal(err)
	}
	if _, err := b.open("gone", envl); err != nil {
		t.Fatalf("B's cache still holds the key until its pass, as documented: %v", err)
	}
	if _, err := b.client.Rewrap(context.Background(), connect.NewRequest(&sealv1alpha1.RewrapRequest{})); err != nil {
		t.Fatal(err)
	}
	if _, err := b.open("gone", envl); reasonOf(err) != seal.ReasonOpen {
		t.Fatalf("B opened an erased key after its pass: %v", err)
	}
}

// An opened key stays in memory for the time to live and is opened again from its wrap after it: one ECDH per key and time to live, not
// per call.
func TestTheCacheOpensAKeyOncePerTimeToLive(t *testing.T) {
	var mu sync.Mutex
	now := time.Unix(1_800_000_000, 0)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	e := start(t, setup{ttl: time.Minute, now: clock})
	envl := e.seal(t, "c", "v")
	e.block.Forget() // empty the cache: the next open is from the wrap
	before := e.keys.ecdhN.Load()
	for range 10 {
		if _, err := e.open("c", envl); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.keys.ecdhN.Load() - before; n != 1 {
		t.Fatalf("%d ECDH calls for ten opens, want 1", n)
	}
	mu.Lock()
	now = now.Add(2 * time.Minute)
	mu.Unlock()
	if _, err := e.open("c", envl); err != nil {
		t.Fatal(err)
	}
	if n := e.keys.ecdhN.Load() - before; n != 2 {
		t.Fatalf("%d ECDH calls after the time to live, want 2", n)
	}
}

// A port that does not answer is unavailable with its reason, and never another answer.
func TestAPortThatDoesNotAnswerIsUnavailable(t *testing.T) {
	e := start(t, setup{})
	envl := e.seal(t, "p", "v")
	e.block.Forget()
	e.keys.fail.Store(true)
	if _, err := e.open("p", envl); connect.CodeOf(err) != connect.CodeUnavailable || reasonOf(err) != seal.ReasonKey {
		t.Fatalf("key port down: %v", err)
	}
	e.keys.fail.Store(false)
	e.block.Forget()
	e.items.fail.Store(true)
	if _, err := e.open("p", envl); connect.CodeOf(err) != connect.CodeUnavailable || reasonOf(err) != seal.ReasonStore {
		t.Fatalf("store port down: %v", err)
	}
}

// The block reports its facts to the host, with no value in them.
func TestTheHostObservesTheFacts(t *testing.T) {
	var mu sync.Mutex
	var events []seal.Event
	e := start(t, setup{observe: func(_ context.Context, ev seal.Event) { mu.Lock(); events = append(events, ev); mu.Unlock() }})
	e.seal(t, "o", "v")
	_, _ = e.open("o", []byte("not an envelope"))
	mu.Lock()
	defer mu.Unlock()
	var actions []string
	for _, ev := range events {
		actions = append(actions, ev.Action)
	}
	want := []string{seal.EventMake, seal.EventWrap, seal.EventRefuse}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Fatalf("events %v, want %v", actions, want)
	}
	if events[0].Count != 1 || events[0].Scope != "o" || events[2].Reason != seal.ReasonOpen {
		t.Fatalf("events %+v", events)
	}
}

// A refusal of the rules that is a Connect error reaches the caller as it is.
func TestTheRulesMayAnswerWithTheirOwnRefusal(t *testing.T) {
	own := refusal.New(connect.CodeFailedPrecondition, "grant", "authorization")
	e := start(t, setup{rules: seal.RulesFunc(func(_ context.Context, r seal.Request) (seal.Decision, error) {
		if string(r.Authorization) != "ok" {
			return seal.Decision{}, own
		}
		return seal.Decision{MayCreate: true}, nil
	})})
	_, err := e.client.Seal(context.Background(), connect.NewRequest(&sealv1alpha1.SealRequest{Purpose: conformance.PurposeSeal, Address: []string{"a"}}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || reasonOf(err) != "grant" {
		t.Fatalf("%v", err)
	}
	if _, err := e.client.Seal(context.Background(), connect.NewRequest(&sealv1alpha1.SealRequest{Purpose: conformance.PurposeSeal, Address: []string{"a"}, Authorization: []byte("ok")})); err != nil {
		t.Fatal(err)
	}
}

// The block's declaration to its key driver is the wrapping purpose and the signing purposes, which the key port's checks accept.
func TestKeyPurposes(t *testing.T) {
	decl := seal.KeyPurposes("", conformance.Purposes())
	if _, err := purpose.New(decl); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range decl {
		names = append(names, d.Name+":"+d.Algorithm)
	}
	want := "wrapping:ECDH-P384,kit-es384:ES384,kit-es256:ES256,kit-rs256:RS256"
	if strings.Join(names, ",") != want {
		t.Fatalf("%v, want %s", names, want)
	}
}

// New refuses options that are not right, and a key store that does not serve the block's declaration.
func TestNewRefusesWhatIsNotRight(t *testing.T) {
	keys := newFakeKeys([]purpose.Declaration{{Name: "other", Algorithm: purpose.ES384, Operations: []purpose.Operation{purpose.Sign}}})
	khc, stop := socket.InProcess(keyv1alpha1connect.NewKeyStoreServiceHandler(keys))
	defer stop()
	kc := keyv1alpha1connect.NewKeyStoreServiceClient(khc, "http://key")
	ihc, stop2 := socket.InProcess(storev1alpha1connect.NewItemServiceHandler(newFakeItems()))
	defer stop2()
	ic := storev1alpha1connect.NewItemServiceClient(ihc, "http://store")
	good := seal.Options{Keys: kc, Items: ic, Purposes: conformance.Purposes(), Rules: seal.AllowAll(), Installation: "i"}
	if _, err := seal.New(context.Background(), good); err == nil || !strings.Contains(err.Error(), "KeyPurposes") {
		t.Fatalf("a key store without the declaration: %v", err)
	}
	for name, change := range map[string]func(*seal.Options){
		"no rules":        func(o *seal.Options) { o.Rules = nil },
		"no installation": func(o *seal.Options) { o.Installation = "" },
		"no purposes":     func(o *seal.Options) { o.Purposes = nil },
		"a code twice": func(o *seal.Options) {
			o.Purposes = []seal.Purpose{{Name: "a", Use: seal.UseSeal, Code: 1}, {Name: "b", Use: seal.UseSeal, Code: 1}}
		},
		"no code":    func(o *seal.Options) { o.Purposes = []seal.Purpose{{Name: "a", Use: seal.UseSeal}} },
		"a bad name": func(o *seal.Options) { o.Purposes = []seal.Purpose{{Name: "A", Use: seal.UseMac}} },
		"a name twice": func(o *seal.Options) {
			o.Purposes = []seal.Purpose{{Name: "a", Use: seal.UseMac}, {Name: "a", Use: seal.UseMac}}
		},
		"the wrapping name": func(o *seal.Options) { o.Purposes = []seal.Purpose{{Name: "wrapping", Use: seal.UseMac}} },
		"no algorithm":      func(o *seal.Options) { o.Purposes = []seal.Purpose{{Name: "s", Use: seal.UseSign}} },
		"no use":            func(o *seal.Options) { o.Purposes = []seal.Purpose{{Name: "s"}} },
		"a long cache":      func(o *seal.Options) { o.CacheTTL = 2 * time.Hour },
		"a bad prefix":      func(o *seal.Options) { o.KeyPrefix = "no slash" },
	} {
		o := good
		change(&o)
		if _, err := seal.New(context.Background(), o); err == nil || !strings.Contains(err.Error(), "the options") {
			t.Errorf("%s: %v", name, err)
		}
	}
	o := good
	o.Keys = nil
	if _, err := seal.New(context.Background(), o); err == nil {
		t.Error("no key port")
	}
}

// A block keeps working after Close, opening its key from its wrap at each call and keeping none.
func TestAfterCloseNoKeyIsKept(t *testing.T) {
	e := start(t, setup{})
	envl := e.seal(t, "w", "v")
	e.block.Close()
	before := e.keys.ecdhN.Load()
	for range 3 {
		if got, err := e.open("w", envl); err != nil || got != "v" {
			t.Fatalf("%q, %v", got, err)
		}
	}
	if n := e.keys.ecdhN.Load() - before; n != 3 {
		t.Fatalf("%d ECDH calls for three opens after Close, want 3", n)
	}
}

// A caller that goes away does not fail the open that others wait for.
func TestACallerThatLeavesDoesNotFailTheOthers(t *testing.T) {
	e := start(t, setup{})
	envl := e.seal(t, "l", "v")
	e.block.Forget()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := e.client.Open(ctx, connect.NewRequest(&sealv1alpha1.OpenRequest{Items: []*sealv1alpha1.OpenItem{{Purpose: conformance.PurposeSeal, Scope: "l", Address: []string{"a"}, Envelope: envl}}}))
	if !errors.Is(err, context.Canceled) && connect.CodeOf(err) != connect.CodeCanceled {
		t.Fatalf("a cancelled caller: %v", err)
	}
	if got, err := e.open("l", envl); err != nil || got != "v" {
		t.Fatalf("%q, %v", got, err)
	}
}
