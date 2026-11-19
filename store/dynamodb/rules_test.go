package dynamodb

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1alpha1 "github.com/truvity/atoom/common/gen/atoom/common/v1alpha1"
	"github.com/truvity/atoom/common/refusal"
	storev1alpha1 "github.com/truvity/atoom/store/gen/atoom/store/v1alpha1"
)

func wantRefusal(t *testing.T, err error, code connect.Code, reason, field string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want %s/%s at %q, got success", code, reason, field)
	}
	if got := connect.CodeOf(err); got != code {
		t.Fatalf("want code %s, got %s", code, got)
	}
	if got, _ := refusal.Reason(err); got != reason {
		t.Fatalf("want reason %q, got %q", reason, got)
	}
	if got, _ := refusal.FieldPath(err); got != field {
		t.Fatalf("want field %q, got %q", field, got)
	}
}

func TestKeysArePrintableASCIIWithoutASpace(t *testing.T) {
	for _, ok := range []string{"a", "prod/-/lease/a.b_c-d", strings.Repeat("a", 512)} {
		if err := checkKey([]byte(ok), "key"); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range [][]byte{nil, []byte("a b"), []byte("a\x00b"), []byte("é"), bytes.Repeat([]byte("a"), 513)} {
		wantRefusal(t, checkKey(bad, "key"), connect.CodeInvalidArgument, "address", "key")
	}
}

func TestPageSizes(t *testing.T) {
	for in, want := range map[int32]int{0: defaultPage, 7: 7, 500: 500, 100000: maxPage} {
		if got, err := pageSize(in); err != nil || got != want {
			t.Errorf("pageSize(%d) = %d, %v; want %d", in, got, err, want)
		}
	}
	_, err := pageSize(-1)
	wantRefusal(t, err, connect.CodeInvalidArgument, "batch", "page_size")
}

func TestPageTokenIsAKeyUnderThePrefix(t *testing.T) {
	if err := checkToken(nil, []byte("a/")); err != nil {
		t.Fatal(err)
	}
	if err := checkToken([]byte("a/b"), []byte("a/")); err != nil {
		t.Fatal(err)
	}
	wantRefusal(t, checkToken([]byte("b/x"), []byte("a/")), connect.CodeInvalidArgument, "address", "page_token")
	wantRefusal(t, checkToken([]byte("a /x"), nil), connect.CodeInvalidArgument, "address", "page_token")
}

func put(key string) *storev1alpha1.Put {
	return &storev1alpha1.Put{Key: []byte(key), Value: []byte("v")}
}

func absent(key string) *storev1alpha1.Condition {
	return &storev1alpha1.Condition{Key: []byte(key), Expect: &storev1alpha1.Condition_Absent{Absent: true}}
}

func TestCheckTransact(t *testing.T) {
	good := &storev1alpha1.TransactRequest{
		Conditions: []*storev1alpha1.Condition{absent("a/1"), {Key: []byte("a/2"), Expect: &storev1alpha1.Condition_Version{Version: 3}}},
		Puts:       []*storev1alpha1.Put{put("a/1")},
		Deletes:    []*storev1alpha1.Delete{{Key: []byte("a/3")}},
	}
	exp, err := checkTransact(good)
	if err != nil || len(exp) != 2 || !exp[0].absent || exp[1].version != 3 {
		t.Fatalf("a good request: %v %v", exp, err)
	}
	tooMany := &storev1alpha1.TransactRequest{}
	for i := range maxOps + 1 {
		tooMany.Deletes = append(tooMany.Deletes, &storev1alpha1.Delete{Key: []byte(fmt.Sprintf("k/%04d", i))})
	}
	_, err = checkTransact(tooMany)
	wantRefusal(t, err, connect.CodeInvalidArgument, "batch", "conditions")

	for name, tc := range map[string]struct {
		req    *storev1alpha1.TransactRequest
		code   connect.Code
		reason string
		field  string
	}{
		"a condition that expects nothing": {&storev1alpha1.TransactRequest{Conditions: []*storev1alpha1.Condition{{Key: []byte("a")}}}, connect.CodeInvalidArgument, "batch", "conditions[0].expect"},
		"absent false":                     {&storev1alpha1.TransactRequest{Conditions: []*storev1alpha1.Condition{{Key: []byte("a"), Expect: &storev1alpha1.Condition_Absent{}}}}, connect.CodeInvalidArgument, "batch", "conditions[0].expect"},
		"a bad condition key":              {&storev1alpha1.TransactRequest{Conditions: []*storev1alpha1.Condition{absent("a b")}}, connect.CodeInvalidArgument, "address", "conditions[0].key"},
		"a key written twice":              {&storev1alpha1.TransactRequest{Puts: []*storev1alpha1.Put{put("a"), put("a")}}, connect.CodeInvalidArgument, "batch", "puts[1].key"},
		"a key put and deleted":            {&storev1alpha1.TransactRequest{Puts: []*storev1alpha1.Put{put("a")}, Deletes: []*storev1alpha1.Delete{{Key: []byte("a")}}}, connect.CodeInvalidArgument, "batch", "deletes[0].key"},
		"an index with no name":            {&storev1alpha1.TransactRequest{Puts: []*storev1alpha1.Put{{Key: []byte("a"), Indexes: []*commonv1alpha1.IndexEntry{{Value: []byte("x")}}}}}, connect.CodeInvalidArgument, "index", "puts[0].indexes[0].name"},
		"an index name with a NUL":         {&storev1alpha1.TransactRequest{Puts: []*storev1alpha1.Put{{Key: []byte("a"), Indexes: []*commonv1alpha1.IndexEntry{{Name: "a\x00b"}}}}}, connect.CodeInvalidArgument, "index", "puts[0].indexes[0].name"},
		"an index value too long":          {&storev1alpha1.TransactRequest{Puts: []*storev1alpha1.Put{{Key: []byte("a"), Indexes: []*commonv1alpha1.IndexEntry{{Name: "n", Value: make([]byte, maxIndexValue+1)}}}}}, connect.CodeInvalidArgument, "index", "puts[0].indexes[0].value"},
		"a value too long":                 {&storev1alpha1.TransactRequest{Puts: []*storev1alpha1.Put{{Key: []byte("a"), Value: make([]byte, maxValue+1)}}}, connect.CodeInvalidArgument, "batch", "puts[0].value"},
		"a put that asks for a log row":    {&storev1alpha1.TransactRequest{Puts: []*storev1alpha1.Put{{Key: []byte("a"), Log: true}}}, connect.CodeUnimplemented, "log", "puts[0].log"},
		"a delete that asks for a log row": {&storev1alpha1.TransactRequest{Deletes: []*storev1alpha1.Delete{{Key: []byte("a"), Log: true}}}, connect.CodeUnimplemented, "log", "deletes[0].log"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := checkTransact(tc.req)
			wantRefusal(t, err, tc.code, tc.reason, tc.field)
		})
	}
}

func TestAnItemRoundTripsThroughItsAttributes(t *testing.T) {
	in := &stored{
		key: []byte("a/b"), version: 7, value: []byte{0, 1, 2, 255}, hasExp: true, expMs: 1_700_000_123_456,
		indexes: sortEntries([]entry{{"owner", []byte("bob")}, {"kind", []byte{0, 255}}, {"owner", []byte("alice")}, {"owner", []byte("bob")}}),
	}
	if len(in.indexes) != 3 || in.indexes[0].name != "kind" || string(in.indexes[1].value) != "alice" {
		t.Fatalf("entries sorted by name, then value, without repeats: %v", in.indexes)
	}
	out, err := decode(in.attributes())
	if err != nil {
		t.Fatal(err)
	}
	if string(out.key) != "a/b" || out.version != 7 || !bytes.Equal(out.value, in.value) || !out.hasExp || out.expMs != in.expMs || len(out.indexes) != 3 {
		t.Fatalf("round trip: %+v", out)
	}
	di := out.driverItem()
	if di.GetExpiresAt().AsTime().UnixMilli() != in.expMs || di.GetIndexes()[0].GetName() != "kind" {
		t.Fatalf("driver item: %v", di)
	}
	// An empty value and no expiry stay as they were.
	bare, err := decode((&stored{key: []byte("k"), version: 1}).attributes())
	if err != nil || bare.hasExp || len(bare.value) != 0 || bare.driverItem().GetExpiresAt() != nil {
		t.Fatalf("bare item: %+v %v", bare, err)
	}
}

func TestTheTimeToLiveNeverComesBeforeTheExpiry(t *testing.T) {
	for ms, want := range map[int64]int64{1000: 1, 1001: 2, 1999: 2, 2000: 2, 0: 0, -5: 0} {
		if got := ttlSeconds(ms); got != want {
			t.Errorf("ttlSeconds(%d) = %d, want %d", ms, got, want)
		}
	}
	s := &stored{hasExp: true, expMs: 1500}
	if !s.live(1499) || s.live(1500) || s.live(2000) {
		t.Error("an item is live strictly before its expiry")
	}
	var none *stored
	if none.live(0) {
		t.Error("no item is not live")
	}
}

func TestTimestampsKeepAMillisecond(t *testing.T) {
	for _, ms := range []int64{0, 1, 999, 1000, 1_700_000_123_456} {
		if got := tsToMs(timestamppb.New(msToTime(ms))); got != ms {
			t.Errorf("%d came back as %d", ms, got)
		}
	}
}

func TestIndexEntriesOfAnItemAndOfAValueAreInDifferentPartitions(t *testing.T) {
	a := entryPKBytes(entry{"n", []byte("v")})
	b := entryPKBytes(entry{"n", []byte("w")})
	c := entryPKBytes(entry{"nv", nil})
	if bytes.Equal(a, b) || bytes.Equal(a, c) || a[0] != partEntry {
		t.Fatal("two entries share a partition")
	}
}
