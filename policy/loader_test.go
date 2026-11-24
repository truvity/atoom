package policy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/cedar-policy/cedar-go/types"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/truvity/atoom/socket"
	sourcev1alpha1 "github.com/truvity/atoom/source/gen/atoom/source/v1alpha1"
	"github.com/truvity/atoom/source/gen/atoom/source/v1alpha1/sourcev1alpha1connect"
)

// fakeSource is a source with one name, served in-process: a document that the test replaces, a version label per document, and a
// watch that says when it changed.
type fakeSource struct {
	sourcev1alpha1connect.UnimplementedSourceServiceHandler
	watch     sourcev1alpha1.Watch
	pollAfter time.Duration

	mu      sync.Mutex
	doc     []byte
	version int
	down    bool
	reads   int
	changed chan struct{}
}

func newFakeSource(watch sourcev1alpha1.Watch) *fakeSource {
	return &fakeSource{watch: watch, changed: make(chan struct{}, 16)}
}

func (f *fakeSource) set(doc []byte) {
	f.mu.Lock()
	f.doc, f.version = doc, f.version+1
	f.mu.Unlock()
	select {
	case f.changed <- struct{}{}:
	default:
	}
}

func (f *fakeSource) setDown(down bool) {
	f.mu.Lock()
	f.down = down
	f.mu.Unlock()
}

func (f *fakeSource) Capabilities(context.Context, *connect.Request[sourcev1alpha1.CapabilitiesRequest]) (*connect.Response[sourcev1alpha1.CapabilitiesResponse], error) {
	return connect.NewResponse(&sourcev1alpha1.CapabilitiesResponse{Watch: f.watch}), nil
}

func (f *fakeSource) Read(_ context.Context, req *connect.Request[sourcev1alpha1.ReadRequest]) (*connect.Response[sourcev1alpha1.ReadResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.down {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("down"))
	}
	if req.Msg.GetName() != DefaultName || f.doc == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such name"))
	}
	label := fmt.Sprintf("v%d", f.version)
	resp := &sourcev1alpha1.ReadResponse{Version: label}
	if f.pollAfter > 0 {
		resp.PollAfter = durationpb.New(f.pollAfter)
	}
	if req.Msg.GetIfNoneMatch() == label {
		resp.Unchanged = true
	} else {
		resp.Data = f.doc
	}
	return connect.NewResponse(resp), nil
}

func (f *fakeSource) Watch(ctx context.Context, req *connect.Request[sourcev1alpha1.SourceWatchRequest], s *connect.ServerStream[sourcev1alpha1.SourceWatchResponse]) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-f.changed:
			if err := s.Send(&sourcev1alpha1.SourceWatchResponse{Name: req.Msg.GetName()}); err != nil {
				return err
			}
		}
	}
}

func (f *fakeSource) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

func (f *fakeSource) client(t *testing.T) sourcev1alpha1connect.SourceServiceClient {
	t.Helper()
	hc, stop := socket.InProcess(sourcev1alpha1connect.NewSourceServiceHandler(f))
	t.Cleanup(stop)
	return sourcev1alpha1connect.NewSourceServiceClient(hc, "http://source")
}

// signer signs documents with a key made for the test and thrown away after it.
type signer struct {
	kid string
	key *ecdsa.PrivateKey
}

func newSigner(t *testing.T, kid string) signer {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return signer{kid: kid, key: k}
}

func (s signer) publisher(coSigns ...string) Publisher {
	return Publisher{KID: s.kid, Key: &s.key.PublicKey, CoSigns: coSigns}
}

func (s signer) signature(t *testing.T, seq uint64, kind, payload64 string) map[string]string {
	t.Helper()
	h, err := json.Marshal(map[string]any{"alg": "ES384", "kid": s.kid, "seq": seq, "kind": kind})
	if err != nil {
		t.Fatal(err)
	}
	protected := b64.EncodeToString(h)
	d := sha512.Sum384([]byte(protected + "." + payload64))
	r, sv, err := ecdsa.Sign(rand.Reader, s.key, d[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 96)
	r.FillBytes(sig[:48])
	sv.FillBytes(sig[48:])
	return map[string]string{"protected": protected, "signature": b64.EncodeToString(sig)}
}

// sign makes a document of the default kind, signed by every signer.
func sign(t *testing.T, seq uint64, payload []byte, by ...signer) []byte {
	t.Helper()
	p64 := b64.EncodeToString(payload)
	sigs := make([]map[string]string, 0, len(by))
	for _, s := range by {
		sigs = append(sigs, s.signature(t, seq, DefaultKind, p64))
	}
	doc, err := json.Marshal(map[string]any{"payload": p64, "signatures": sigs})
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

const testSchema = `entity User; entity Route; action "GET" appliesTo { principal: [User], resource: [Route] };`

func bundleOf(t *testing.T, policies map[string]string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"schema": testSchema, "policies": policies})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var (
	permitAll  = map[string]string{"all": `permit (principal, action, resource);`}
	permitNone = map[string]string{"none": `forbid (principal, action, resource);`}
)

func sha384hex(b []byte) string {
	d := sha512.Sum384(b)
	return hex.EncodeToString(d[:])
}

func allowed(t *testing.T, l *Loader) bool {
	t.Helper()
	d, err := l.Decide(Request{
		Principal: types.NewEntityUID("User", "alice"),
		Action:    types.NewEntityUID("Action", "GET"),
		Resource:  types.NewEntityUID("Route", "home"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Digest != l.Current().Digest() {
		t.Fatal("the decision does not carry the digest of the set in force")
	}
	return d.Allow
}

type outcomes struct {
	mu  sync.Mutex
	all []Outcome
}

func (o *outcomes) add(x Outcome) {
	o.mu.Lock()
	o.all = append(o.all, x)
	o.mu.Unlock()
}

func (o *outcomes) list() []Outcome {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]Outcome(nil), o.all...)
}

func TestColdStartRefusesWithoutAValidSet(t *testing.T) {
	pub := newSigner(t, "publisher-1")
	other := newSigner(t, "publisher-1")
	cases := []struct {
		name string
		doc  []byte
	}{
		{"no document", nil},
		{"a signature by a key that is not pinned", sign(t, 1, bundleOf(t, permitAll), other)},
		{"a bundle that does not compile", sign(t, 1, bundleOf(t, map[string]string{"bad": "permit ("}), pub)},
		{"not a signed document", bundleOf(t, permitAll)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := newFakeSource(sourcev1alpha1.Watch_NONE)
			if c.doc != nil {
				src.set(c.doc)
			}
			l, err := New(t.Context(), Options{Source: src.client(t), Publishers: []Publisher{pub.publisher()}})
			if err == nil || l != nil {
				t.Fatalf("started with %v", l.Current())
			}
		})
	}
}

func TestReloadRules(t *testing.T) {
	pub := newSigner(t, "publisher-1")
	src := newFakeSource(sourcev1alpha1.Watch_NONE)
	first := bundleOf(t, permitAll)
	src.set(sign(t, 1, first, pub))
	var seen outcomes
	l, err := New(t.Context(), Options{Source: src.client(t), Publishers: []Publisher{pub.publisher()}, OnReload: seen.add})
	if err != nil {
		t.Fatal(err)
	}
	if l.Current().Digest() != sha384hex(first) || l.Current().Seq() != 1 || !allowed(t, l) {
		t.Fatalf("the start: %v", l.Current())
	}
	ctx := t.Context()

	if out := l.Reload(ctx); out.Result != Unchanged {
		t.Fatalf("the same version: %v", out.Result)
	}

	second := bundleOf(t, permitNone)
	src.set(sign(t, 2, second, pub))
	if out := l.Reload(ctx); out.Result != Applied || out.Seq != 2 || out.Digest != sha384hex(second) || allowed(t, l) {
		t.Fatalf("a newer document: %+v", out)
	}

	// A refused document leaves the set in force, whatever the reason.
	refused := []struct {
		name string
		doc  []byte
		want func(error) bool
	}{
		{"an older seq", sign(t, 1, first, pub), func(err error) bool { return errors.Is(err, ErrOlder) }},
		{"the same seq with another payload", sign(t, 2, first, pub), func(err error) bool { return errors.Is(err, ErrConflict) }},
		{"a signature by another key", sign(t, 3, first, newSigner(t, "publisher-1")), func(err error) bool {
			var r *Refused
			return errors.As(err, &r) && r.Step == 4
		}},
		{"a bundle that fails validation", sign(t, 3, bundleOf(t, map[string]string{"x": `permit (principal, action, resource) when { principal.nope };`}), pub), func(err error) bool {
			var f *Fault
			return errors.As(err, &f) && f.Class == ClassValidation && f.ID == "x"
		}},
	}
	for _, r := range refused {
		src.set(r.doc)
		out := l.Reload(ctx)
		if out.Result != Rejected || !r.want(out.Err) {
			t.Fatalf("%s: %+v", r.name, out)
		}
		if out.Seq != 2 || out.Digest != sha384hex(second) || allowed(t, l) {
			t.Fatalf("%s: the set in force changed: %+v", r.name, out)
		}
	}

	// The same seq and payload under a new version label is the set in force.
	src.set(sign(t, 2, second, pub))
	if out := l.Reload(ctx); out.Result != Unchanged {
		t.Fatalf("the same document again: %+v", out)
	}

	src.setDown(true)
	if out := l.Reload(ctx); out.Result != Unavailable || out.Seq != 2 {
		t.Fatalf("a source that is down: %+v", out)
	}
	src.setDown(false)

	got := seen.list()
	want := []Result{Applied, Applied, Rejected, Rejected, Rejected, Rejected, Unavailable}
	if len(got) != len(want) {
		t.Fatalf("OnReload saw %d outcomes, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Result != want[i] {
			t.Fatalf("OnReload %d: %v, want %v", i, got[i].Result, want[i])
		}
	}
}

func TestCoSigner(t *testing.T) {
	pub := newSigner(t, "publisher-1")
	party := newSigner(t, "party-1")
	pins := []Publisher{pub.publisher(), party.publisher(DefaultKind)}
	src := newFakeSource(sourcev1alpha1.Watch_NONE)
	src.set(sign(t, 1, bundleOf(t, permitAll), pub))
	if _, err := New(t.Context(), Options{Source: src.client(t), Publishers: pins}); err == nil {
		t.Fatal("started without the co-signer's signature")
	}
	src.set(sign(t, 1, bundleOf(t, permitAll), pub, party))
	if _, err := New(t.Context(), Options{Source: src.client(t), Publishers: pins}); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRunFollowsTheSource(t *testing.T) {
	for _, w := range []sourcev1alpha1.Watch{sourcev1alpha1.Watch_NOTIFY, sourcev1alpha1.Watch_POLL} {
		t.Run(w.String(), func(t *testing.T) {
			pub := newSigner(t, "publisher-1")
			src := newFakeSource(w)
			src.pollAfter = 10 * time.Millisecond
			src.set(sign(t, 1, bundleOf(t, permitAll), pub))
			l, err := New(t.Context(), Options{Source: src.client(t), Publishers: []Publisher{pub.publisher()}, MinInterval: 10 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- l.Run(ctx) }()

			// Decisions go on while the set is swapped under them.
			stop := make(chan struct{})
			var wg sync.WaitGroup
			wg.Go(func() {
				for {
					select {
					case <-stop:
						return
					default:
						if _, err := l.Decide(Request{
							Principal: types.NewEntityUID("User", "alice"),
							Action:    types.NewEntityUID("Action", "GET"),
							Resource:  types.NewEntityUID("Route", "home"),
						}); err != nil {
							t.Error(err)
							return
						}
					}
				}
			})
			src.set(sign(t, 2, bundleOf(t, permitNone), pub))
			waitFor(t, "the newer set", func() bool { return l.Current().Seq() == 2 })
			src.set(sign(t, 3, bundleOf(t, map[string]string{"bad": "permit ("}), pub))
			src.set(sign(t, 4, bundleOf(t, permitAll), pub))
			waitFor(t, "the set after a refused one", func() bool { return l.Current().Seq() == 4 })
			close(stop)
			wg.Wait()
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRunRespectsTheMinimumInterval(t *testing.T) {
	pub := newSigner(t, "publisher-1")
	src := newFakeSource(sourcev1alpha1.Watch_NOTIFY)
	src.set(sign(t, 1, bundleOf(t, permitAll), pub))
	l, err := New(t.Context(), Options{Source: src.client(t), Publishers: []Publisher{pub.publisher()}, MinInterval: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = l.Run(ctx) }()
	before := src.readCount()
	for i := range 10 {
		src.set(sign(t, uint64(2+i), bundleOf(t, permitAll), pub))
	}
	time.Sleep(500 * time.Millisecond)
	// Eleven changes in half a second at a minimum interval of 200 ms: a few reads, not one for each change.
	if n := src.readCount() - before; n > 5 {
		t.Fatalf("%d reads in 500 ms at a minimum interval of 200 ms", n)
	}
}

func TestRunReturnsForASourceThatNeverChanges(t *testing.T) {
	pub := newSigner(t, "publisher-1")
	src := newFakeSource(sourcev1alpha1.Watch_NONE)
	src.set(sign(t, 1, bundleOf(t, permitAll), pub))
	l, err := New(t.Context(), Options{Source: src.client(t), Publishers: []Publisher{pub.publisher()}})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestOptionsAreChecked(t *testing.T) {
	src := newFakeSource(sourcev1alpha1.Watch_NONE)
	if _, err := New(t.Context(), Options{Publishers: []Publisher{newSigner(t, "p").publisher()}}); err == nil {
		t.Fatal("no source")
	}
	if _, err := New(t.Context(), Options{Source: src.client(t)}); err == nil {
		t.Fatal("no publishers")
	}
}
