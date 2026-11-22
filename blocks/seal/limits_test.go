package seal_test

import (
	"context"
	"crypto/ecdh"
	"encoding/binary"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/truvity/atoom/blocks/seal"
	"github.com/truvity/atoom/blocks/seal/conformance"
	sealv1alpha1 "github.com/truvity/atoom/blocks/seal/gen/atoom/seal/v1alpha1"
	storev1alpha1 "github.com/truvity/atoom/store/gen/atoom/store/v1alpha1"
)

func generationOf(env []byte) uint32 { return binary.BigEndian.Uint32(env[2:6]) }

func (f *fakeItems) counter(t *testing.T, key string) uint64 {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.items[key]
	if !ok {
		return 0
	}
	c := &sealv1alpha1.SealCounter{}
	if err := proto.Unmarshal(it.GetValue(), c); err != nil {
		t.Fatal(err)
	}
	return c.GetReserved()
}

// The counter of a generation is reserved N seals at a time, one store write per N seals, and it is an upper bound of the seals.
func TestTheCounterIsReservedNSealsAtATime(t *testing.T) {
	e := start(t, setup{limit: 100, reserve: 10})
	for range 25 {
		e.seal(t, "c", "v")
	}
	if got := e.items.counter(t, "seal/count/c/"+conformance.PurposeSeal+"/1"); got != 30 {
		t.Fatalf("the counter is %d after 25 seals with reservations of 10, want 30", got)
	}
	caps, err := e.client.Capabilities(context.Background(), connect.NewRequest(&sealv1alpha1.CapabilitiesRequest{}))
	if err != nil || caps.Msg.GetSealsPerGeneration() != 100 || caps.Msg.GetSealReservation() != 10 {
		t.Fatalf("capabilities %v, %v", caps, err)
	}
}

// Two instances share the limit through the counter: no generation makes more seals than the limit, counted over both, and the two
// move to the same next generation rather than one each.
func TestTwoInstancesShareTheLimit(t *testing.T) {
	items := newFakeItems()
	var list []*ecdh.PublicKey
	recipients := func(context.Context) ([]*ecdh.PublicKey, error) { return list, nil }
	a := start(t, setup{items: items, recipients: recipients, limit: 20, reserve: 5})
	b := start(t, setup{items: items, recipients: recipients, limit: 20, reserve: 5})
	list = []*ecdh.PublicKey{a.block.WrappingKey(), b.block.WrappingKey()}
	per := map[uint32]int{}
	for i := range 90 {
		e := a
		if i%2 == 1 {
			e = b
		}
		per[generationOf(e.seal(t, "shared", "v"))]++
	}
	for g, n := range per {
		if n > 20 {
			t.Fatalf("generation %d made %d seals, the limit is 20", g, n)
		}
	}
	if len(per) < 5 {
		t.Fatalf("90 seals used %d generations, want at least 5", len(per))
	}
	for g := range len(per) {
		if per[uint32(g+1)] == 0 {
			t.Fatalf("generation %d was skipped: %v", g+1, per)
		}
	}
}

// The default limit is half of the AES-GCM limit, and a limit above the AES-GCM limit is refused.
func TestTheDefaultLimitAndItsBound(t *testing.T) {
	if seal.DefaultSealsPerGeneration != 1<<31 || seal.MaxSealsPerGeneration != 1<<32 || seal.DefaultSealReservation != 1024 {
		t.Fatal("the documented defaults changed")
	}
	e := start(t, setup{limit: seal.MaxSealsPerGeneration, reserve: seal.DefaultSealReservation})
	e.seal(t, "d", "v")
}

// A mark reaches another instance at its next Rewrap: before it, a cached key of the marked generation seals on (within the cache's
// time to live); after it, the instance seals under the new generation.
func TestAMarkReachesAnotherInstanceAtItsRewrap(t *testing.T) {
	items := newFakeItems()
	var list []*ecdh.PublicKey
	recipients := func(context.Context) ([]*ecdh.PublicKey, error) { return list, nil }
	a := start(t, setup{items: items, recipients: recipients})
	b := start(t, setup{items: items, recipients: recipients})
	list = []*ecdh.PublicKey{a.block.WrappingKey(), b.block.WrappingKey()}
	if g := generationOf(b.seal(t, "m", "v")); g != 1 {
		t.Fatalf("generation %d", g)
	}
	res, err := a.client.MarkCompromised(context.Background(), connect.NewRequest(&sealv1alpha1.MarkCompromisedRequest{Purpose: conformance.PurposeSeal, Scope: "m", Generation: 1}))
	if err != nil || res.Msg.GetGeneration() != 2 {
		t.Fatalf("mark: %v, %v", res, err)
	}
	if g := generationOf(a.seal(t, "m", "v")); g != 2 {
		t.Fatalf("the marking instance seals under generation %d", g)
	}
	if g := generationOf(b.seal(t, "m", "v")); g != 1 {
		t.Fatalf("the other instance, with the key in its cache, sealed under generation %d before its pass", g)
	}
	if _, err := b.client.Rewrap(context.Background(), connect.NewRequest(&sealv1alpha1.RewrapRequest{})); err != nil {
		t.Fatal(err)
	}
	if g := generationOf(b.seal(t, "m", "v")); g != 2 {
		t.Fatalf("the other instance seals under generation %d after its pass", g)
	}
}

// A generation marked compromised with no newer one (a mark written by another host of the store) refuses every seal, once the cache
// refills; what it sealed still opens.
func TestAMarkWithNoNewerGenerationRefusesTheSeal(t *testing.T) {
	e := start(t, setup{})
	before := e.seal(t, "x", "v")
	v, _ := proto.Marshal(&sealv1alpha1.GenerationMark{Compromised: true})
	if _, err := e.items.Transact(context.Background(), connect.NewRequest(&storev1alpha1.TransactRequest{
		Puts: []*storev1alpha1.Put{{Key: []byte("seal/mark/x/" + conformance.PurposeSeal + "/1"), Value: v}},
	})); err != nil {
		t.Fatal(err)
	}
	e.block.Forget()
	_, err := e.client.Seal(context.Background(), connect.NewRequest(&sealv1alpha1.SealRequest{Purpose: conformance.PurposeSeal, Scope: "x", Address: []string{"a"}}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || reasonOf(err) != seal.ReasonCompromised {
		t.Fatalf("a seal under a compromised generation: %v", err)
	}
	if got, err := e.open("x", before); err != nil || got != "v" {
		t.Fatalf("an envelope of the compromised generation: %q, %v", got, err)
	}
}

// Erase deletes the counters and the marks of the scope with its wraps.
func TestEraseDeletesTheCountersAndMarks(t *testing.T) {
	e := start(t, setup{})
	envl := e.seal(t, "z", "v")
	if _, err := e.client.MarkCompromised(context.Background(), connect.NewRequest(&sealv1alpha1.MarkCompromisedRequest{Purpose: conformance.PurposeSeal, Scope: "z", Generation: generationOf(envl)})); err != nil {
		t.Fatal(err)
	}
	e.seal(t, "z", "v")
	if len(e.items.keysWith("seal/count/z/")) == 0 || len(e.items.keysWith("seal/mark/z/")) == 0 {
		t.Fatal("no counter or mark to erase")
	}
	if _, err := e.client.Erase(context.Background(), connect.NewRequest(&sealv1alpha1.EraseRequest{Scope: "z"})); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"seal/wrap/z/", "seal/count/z/", "seal/mark/z/"} {
		if left := e.items.keysWith(p); len(left) != 0 {
			t.Fatalf("left after the erase: %v", left)
		}
	}
	if g := generationOf(e.seal(t, "z", "new")); g != 1 {
		t.Fatalf("a new key after the erase is at generation %d", g)
	}
}
