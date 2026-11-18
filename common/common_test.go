package common_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	commonv1alpha1 "github.com/truvity/atoom/common/gen/atoom/common/v1alpha1"
)

// The wire names are part of the contract: a Connect error detail carries the full name of its message.
func TestTheMessagesHaveTheNamesOfTheContract(t *testing.T) {
	for name, m := range map[string]proto.Message{
		"atoom.common.v1alpha1.Refusal":    &commonv1alpha1.Refusal{},
		"atoom.common.v1alpha1.IndexEntry": &commonv1alpha1.IndexEntry{},
	} {
		if got := string(m.ProtoReflect().Descriptor().FullName()); got != name {
			t.Errorf("full name = %q, want %q", got, name)
		}
	}
}

// The field numbers are the ones sluis used, so that the bytes of a message did not change with the move.
func TestTheMessagesRoundTripWithTheFieldNumbersOfSluis(t *testing.T) {
	in := &commonv1alpha1.IndexEntry{Name: "by-owner", Value: []byte{1, 2, 3}}
	b, err := proto.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x0a, 8, 'b', 'y', '-', 'o', 'w', 'n', 'e', 'r', 0x12, 3, 1, 2, 3}
	if string(b) != string(want) {
		t.Fatalf("IndexEntry bytes = %x, want %x", b, want)
	}
	var out commonv1alpha1.IndexEntry
	if err := proto.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(in, &out) {
		t.Fatalf("round trip: %v != %v", &out, in)
	}

	r := &commonv1alpha1.Refusal{Reason: "bound", FieldPath: "a.b"}
	b, err = proto.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	want = []byte{0x0a, 5, 'b', 'o', 'u', 'n', 'd', 0x12, 3, 'a', '.', 'b'}
	if string(b) != string(want) {
		t.Fatalf("Refusal bytes = %x, want %x", b, want)
	}
}
