package dynamodb_test

// The tests of this file run against DynamoDB Local, a container that AWS publishes (tools/scripts/conformance-store.sh starts one and
// sets ATOOM_DYNAMODB_ENDPOINT). Without the variable they skip and say so; ATOOM_REQUIRE_DYNAMODB_LOCAL=1 turns the skip into a failure
// (CI sets it on Linux). A skip is not a pass: the conformance kit of the item port is what holds the driver to the contract, and these
// tests hold it to what only this driver does (the table, the time to live, the index entries, the retry of a lost race).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1alpha1 "github.com/truvity/atoom/common/gen/atoom/common/v1alpha1"
	"github.com/truvity/atoom/common/refusal"
	"github.com/truvity/atoom/store/dynamodb"
	storev1alpha1 "github.com/truvity/atoom/store/gen/atoom/store/v1alpha1"
	"github.com/truvity/atoom/store/gen/atoom/store/v1alpha1/storev1alpha1connect"
)

var tableSeq atomic.Int64

// localClient is a client of DynamoDB Local, or a skip.
func localClient(t *testing.T) *awsdynamodb.Client {
	t.Helper()
	endpoint := os.Getenv("ATOOM_DYNAMODB_ENDPOINT")
	if endpoint == "" {
		if os.Getenv("ATOOM_REQUIRE_DYNAMODB_LOCAL") != "" {
			t.Fatal("ATOOM_DYNAMODB_ENDPOINT is not set and the run is required to use DynamoDB Local (tools/scripts/conformance-store.sh)")
		}
		t.Skip("SKIP: ATOOM_DYNAMODB_ENDPOINT is not set (DynamoDB Local runs in tools/scripts/conformance-store.sh)")
	}
	return awsdynamodb.New(awsdynamodb.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(endpoint),
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "local", SecretAccessKey: "local"}, nil
		}),
		RetryMaxAttempts: 1,
	})
}

// hooked wraps the client so that a test can act between the read and the write of a Transact.
type hooked struct {
	*awsdynamodb.Client
	mu          sync.Mutex
	beforeWrite func() // called once, before the next write (PutItem, DeleteItem or TransactWriteItems), then cleared
	puts        int
	deletes     int
	transacts   int
}

func (h *hooked) fire() {
	h.mu.Lock()
	f := h.beforeWrite
	h.beforeWrite = nil
	h.mu.Unlock()
	if f != nil {
		f()
	}
}

func (h *hooked) PutItem(ctx context.Context, in *awsdynamodb.PutItemInput, o ...func(*awsdynamodb.Options)) (*awsdynamodb.PutItemOutput, error) {
	h.fire()
	h.mu.Lock()
	h.puts++
	h.mu.Unlock()
	return h.Client.PutItem(ctx, in, o...)
}

func (h *hooked) DeleteItem(ctx context.Context, in *awsdynamodb.DeleteItemInput, o ...func(*awsdynamodb.Options)) (*awsdynamodb.DeleteItemOutput, error) {
	h.fire()
	h.mu.Lock()
	h.deletes++
	h.mu.Unlock()
	return h.Client.DeleteItem(ctx, in, o...)
}

func (h *hooked) TransactWriteItems(ctx context.Context, in *awsdynamodb.TransactWriteItemsInput, o ...func(*awsdynamodb.Options)) (*awsdynamodb.TransactWriteItemsOutput, error) {
	h.fire()
	h.mu.Lock()
	h.transacts++
	h.mu.Unlock()
	return h.Client.TransactWriteItems(ctx, in, o...)
}

type env struct {
	raw    *awsdynamodb.Client
	api    *hooked
	table  string
	client storev1alpha1connect.ItemServiceClient
}

// start makes a table of its own on DynamoDB Local and serves the driver on an in-process HTTP server, as a consumer that links it does.
func start(t *testing.T) *env {
	t.Helper()
	raw := localClient(t)
	table := fmt.Sprintf("atoom-test-%d-%d", os.Getpid(), tableSeq.Add(1))
	ctx := context.Background()
	if err := dynamodb.EnsureTable(ctx, raw, table); err != nil {
		t.Fatalf("EnsureTable: %v", err)
	}
	t.Cleanup(func() { _, _ = raw.DeleteTable(ctx, &awsdynamodb.DeleteTableInput{TableName: aws.String(table)}) })
	h := &hooked{Client: raw}
	svc, err := dynamodb.New(dynamodb.Options{DynamoDB: h, Table: table})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(svc.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &env{raw: raw, api: h, table: table, client: storev1alpha1connect.NewItemServiceClient(srv.Client(), srv.URL)}
}

func (e *env) transact(t *testing.T, req *storev1alpha1.TransactRequest) (*storev1alpha1.TransactResponse, error) {
	t.Helper()
	res, err := e.client.Transact(context.Background(), connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

func (e *env) mustTransact(t *testing.T, req *storev1alpha1.TransactRequest) *storev1alpha1.TransactResponse {
	t.Helper()
	res, err := e.transact(t, req)
	if err != nil {
		t.Fatalf("Transact: %v", err)
	}
	return res
}

func (e *env) get(t *testing.T, key string) (*storev1alpha1.DriverItem, error) {
	t.Helper()
	res, err := e.client.Get(context.Background(), connect.NewRequest(&storev1alpha1.ItemGetRequest{Key: []byte(key), Linearizable: true}))
	if err != nil {
		return nil, err
	}
	return res.Msg.GetItem(), nil
}

func (e *env) query(t *testing.T, prefix, index, value string) []string {
	t.Helper()
	var keys []string
	var token []byte
	for range 100 {
		res, err := e.client.Query(context.Background(), connect.NewRequest(&storev1alpha1.QueryRequest{
			Prefix: []byte(prefix), Index: index, IndexValue: []byte(value), PageSize: 3, PageToken: token, Linearizable: true,
		}))
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		for _, it := range res.Msg.GetItems() {
			keys = append(keys, string(it.GetKey()))
		}
		if len(res.Msg.GetNextPageToken()) == 0 {
			return keys
		}
		token = res.Msg.GetNextPageToken()
	}
	t.Fatal("a listing that never ends")
	return nil
}

// rawCount is how many items of the table, entries included, DynamoDB holds.
func (e *env) rawCount(t *testing.T) int {
	t.Helper()
	n := 0
	var start map[string]types.AttributeValue
	for {
		out, err := e.raw.Scan(context.Background(), &awsdynamodb.ScanInput{TableName: aws.String(e.table), ExclusiveStartKey: start, ConsistentRead: aws.Bool(true)})
		if err != nil {
			t.Fatal(err)
		}
		n += len(out.Items)
		if len(out.LastEvaluatedKey) == 0 {
			return n
		}
		start = out.LastEvaluatedKey
	}
}

func put(key, value string, entries ...[2]string) *storev1alpha1.Put {
	p := &storev1alpha1.Put{Key: []byte(key), Value: []byte(value)}
	for _, e := range entries {
		p.Indexes = append(p.Indexes, &commonv1alpha1.IndexEntry{Name: e[0], Value: []byte(e[1])})
	}
	return p
}

func absent(key string) *storev1alpha1.Condition {
	return &storev1alpha1.Condition{Key: []byte(key), Expect: &storev1alpha1.Condition_Absent{Absent: true}}
}

func atVersion(key string, v uint64) *storev1alpha1.Condition {
	return &storev1alpha1.Condition{Key: []byte(key), Expect: &storev1alpha1.Condition_Version{Version: v}}
}

func wantRefusal(t *testing.T, err error, code connect.Code, reason string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want %s/%s, got success", code, reason)
	}
	if got := connect.CodeOf(err); got != code {
		t.Fatalf("want code %s, got %s: %v", code, got, err)
	}
	if got, _ := refusal.Reason(err); got != reason {
		t.Fatalf("want reason %q, got %q", reason, got)
	}
}

func TestCapabilitiesDeclareNoChangeLog(t *testing.T) {
	e := start(t)
	res, err := e.client.Capabilities(context.Background(), connect.NewRequest(&storev1alpha1.CapabilitiesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Msg.GetStore() != "dynamodb" || res.Msg.GetTailLog() {
		t.Fatalf("capabilities %v: want store dynamodb and tail_log false", res.Msg)
	}
	stream, err := e.client.TailLog(context.Background(), connect.NewRequest(&storev1alpha1.TailLogRequest{}))
	if err == nil {
		for stream.Receive() {
		}
		err = stream.Err()
		_ = stream.Close()
	}
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("TailLog: %v, want unimplemented", err)
	}
	// A write that asks for a log row is refused, and nothing is written.
	_, err = e.transact(t, &storev1alpha1.TransactRequest{Puts: []*storev1alpha1.Put{{Key: []byte("a/1"), Value: []byte("v"), Log: true}}})
	wantRefusal(t, err, connect.CodeUnimplemented, "log")
	if _, err := e.get(t, "a/1"); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("the refused put left an item: %v", err)
	}
}

func TestTheTableAndItsTimeToLive(t *testing.T) {
	e := start(t)
	ctx := context.Background()
	if err := dynamodb.EnsureTable(ctx, e.raw, e.table); err != nil {
		t.Fatalf("EnsureTable twice: %v", err)
	}
	if err := dynamodb.CheckTable(ctx, e.raw, e.table); err != nil {
		t.Fatal(err)
	}
	ttl, err := e.raw.DescribeTimeToLive(ctx, &awsdynamodb.DescribeTimeToLiveInput{TableName: aws.String(e.table)})
	if err != nil {
		t.Fatal(err)
	}
	st := ttl.TimeToLiveDescription
	if st == nil || aws.ToString(st.AttributeName) != dynamodb.TTLAttribute || (st.TimeToLiveStatus != types.TimeToLiveStatusEnabled && st.TimeToLiveStatus != types.TimeToLiveStatusEnabling) {
		t.Fatalf("time to live: %+v", st)
	}
	// A table with other keys is refused.
	other := e.table + "-other"
	_, err = e.raw.CreateTable(ctx, &awsdynamodb.CreateTableInput{
		TableName:            aws.String(other),
		AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("id"), AttributeType: types.ScalarAttributeTypeS}},
		KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String("id"), KeyType: types.KeyTypeHash}},
		BillingMode:          types.BillingModePayPerRequest,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.raw.DeleteTable(ctx, &awsdynamodb.DeleteTableInput{TableName: aws.String(other)}) })
	if err := dynamodb.CheckTable(ctx, e.raw, other); !errors.Is(err, dynamodb.ErrTableMismatch) {
		t.Fatalf("CheckTable of a table with other keys: %v", err)
	}
}

// The expiry is a number of milliseconds for the reader and a whole second, rounded up, for DynamoDB's time to live; the index entries of
// an item are freed with it.
func TestExpiryIsWrittenAsTimeToLive(t *testing.T) {
	e := start(t)
	exp := time.Now().Add(time.Hour).Truncate(time.Millisecond).Add(250 * time.Millisecond)
	p := put("a/1", "v", [2]string{"owner", "bob"})
	p.ExpiresAt = timestamppb.New(exp)
	e.mustTransact(t, &storev1alpha1.TransactRequest{Puts: []*storev1alpha1.Put{p}})
	out, err := e.raw.Scan(context.Background(), &awsdynamodb.ScanInput{TableName: aws.String(e.table), ConsistentRead: aws.Bool(true)})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 2 {
		t.Fatalf("%d items in the table, want the item and one entry", len(out.Items))
	}
	want := strconv.FormatInt((exp.UnixMilli()+999)/1000, 10)
	for _, it := range out.Items {
		n, ok := it["ttl"].(*types.AttributeValueMemberN)
		if !ok || n.Value != want {
			t.Fatalf("an item of the table has ttl %v, want %s", it["ttl"], want)
		}
	}
	got, err := e.get(t, "a/1")
	if err != nil || got.GetExpiresAt().AsTime().UnixMilli() != exp.UnixMilli() {
		t.Fatalf("Get: %v %v", got, err)
	}
}

// An index entry is written and removed with its item: a rewrite that drops an entry, and a delete, leave none behind.
func TestIndexEntriesFollowTheirItem(t *testing.T) {
	e := start(t)
	e.mustTransact(t, &storev1alpha1.TransactRequest{Puts: []*storev1alpha1.Put{
		put("s/1", "v", [2]string{"user", "alice"}, [2]string{"user", "alice"}, [2]string{"kind", "web"}),
		put("s/2", "v", [2]string{"user", "alice"}),
	}})
	if n := e.rawCount(t); n != 5 {
		t.Fatalf("%d items in the table, want 2 items and 3 entries (a repeated entry is one)", n)
	}
	e.mustTransact(t, &storev1alpha1.TransactRequest{Conditions: []*storev1alpha1.Condition{atVersion("s/1", 1)}, Puts: []*storev1alpha1.Put{put("s/1", "v2", [2]string{"user", "bob"})}})
	if n := e.rawCount(t); n != 4 {
		t.Fatalf("%d items after a rewrite, want 2 items and 2 entries", n)
	}
	if got := e.query(t, "s/", "user", "alice"); len(got) != 1 || got[0] != "s/2" {
		t.Fatalf("alice finds %v, want s/2", got)
	}
	if got := e.query(t, "s/", "user", "bob"); len(got) != 1 || got[0] != "s/1" {
		t.Fatalf("bob finds %v, want s/1", got)
	}
	e.mustTransact(t, &storev1alpha1.TransactRequest{Deletes: []*storev1alpha1.Delete{{Key: []byte("s/1")}, {Key: []byte("s/2")}, {Key: []byte("s/absent")}}})
	if n := e.rawCount(t); n != 0 {
		t.Fatalf("%d items after the deletes, want none", n)
	}
}

// An entry that names nothing (its item is gone, or declares other entries now) is not a result.
func TestStaleEntriesAreNotResults(t *testing.T) {
	e := start(t)
	ctx := context.Background()
	e.mustTransact(t, &storev1alpha1.TransactRequest{Puts: []*storev1alpha1.Put{put("s/1", "v", [2]string{"user", "alice"}), put("s/2", "v", [2]string{"user", "alice"})}})
	// The item s/1 is removed behind the driver's back (what the time to live does, entry and item at different moments).
	if _, err := e.raw.DeleteItem(ctx, &awsdynamodb.DeleteItemInput{TableName: aws.String(e.table), Key: map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberB{Value: []byte{0x01}}, "sk": &types.AttributeValueMemberB{Value: []byte("s/1")},
	}}); err != nil {
		t.Fatal(err)
	}
	if got := e.query(t, "", "user", "alice"); len(got) != 1 || got[0] != "s/2" {
		t.Fatalf("alice finds %v, want only s/2", got)
	}
}

// A Transact that needs more writes than one DynamoDB transaction holds is refused, never split, and writes nothing.
func TestMoreWritesThanATransactionHoldsIsRefused(t *testing.T) {
	e := start(t)
	req := &storev1alpha1.TransactRequest{}
	for i := range 51 {
		req.Puts = append(req.Puts, put(fmt.Sprintf("big/%03d", i), "v", [2]string{"a", "1"})) // a put and an entry: 102 writes
	}
	_, err := e.transact(t, req)
	wantRefusal(t, err, connect.CodeInvalidArgument, "batch")
	if n := e.rawCount(t); n != 0 {
		t.Fatalf("%d items written by a refused Transact", n)
	}
	req.Puts = req.Puts[:50]
	e.mustTransact(t, req)
	if n := e.rawCount(t); n != 100 {
		t.Fatalf("%d items, want 50 items and 50 entries", n)
	}
}

// One put with one condition is one conditional PutItem, and a transaction only when more than one item changes.
func TestOneWriteIsNotATransaction(t *testing.T) {
	e := start(t)
	e.mustTransact(t, &storev1alpha1.TransactRequest{Conditions: []*storev1alpha1.Condition{absent("c/1")}, Puts: []*storev1alpha1.Put{put("c/1", "1")}})
	e.mustTransact(t, &storev1alpha1.TransactRequest{Conditions: []*storev1alpha1.Condition{atVersion("c/1", 1)}, Puts: []*storev1alpha1.Put{put("c/1", "2")}})
	e.mustTransact(t, &storev1alpha1.TransactRequest{Conditions: []*storev1alpha1.Condition{atVersion("c/1", 2)}, Deletes: []*storev1alpha1.Delete{{Key: []byte("c/1")}}})
	if e.api.puts != 2 || e.api.deletes != 1 || e.api.transacts != 0 {
		t.Fatalf("PutItem %d, DeleteItem %d, TransactWriteItems %d: want 2, 1, 0", e.api.puts, e.api.deletes, e.api.transacts)
	}
	e.mustTransact(t, &storev1alpha1.TransactRequest{Puts: []*storev1alpha1.Put{put("c/2", "1"), put("c/3", "1")}})
	// A condition on a key that is not written is held by a check, so that it is true at the write too: that is a transaction.
	e.mustTransact(t, &storev1alpha1.TransactRequest{Conditions: []*storev1alpha1.Condition{atVersion("c/2", 1)}, Puts: []*storev1alpha1.Put{put("c/3", "2")}})
	if e.api.transacts != 2 {
		t.Fatalf("TransactWriteItems %d, want 2", e.api.transacts)
	}
}

// A write that loses a race reads again and decides again: an unconditional put goes on after the winner's version, and one that expected
// a version is refused, on the state it is written on.
func TestALostRaceReadsAgain(t *testing.T) {
	e := start(t)
	e.mustTransact(t, &storev1alpha1.TransactRequest{Puts: []*storev1alpha1.Put{put("r/1", "one")}})
	rival := func() {
		if _, err := e.raw.PutItem(context.Background(), &awsdynamodb.PutItemInput{TableName: aws.String(e.table), Item: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberB{Value: []byte{0x01}}, "sk": &types.AttributeValueMemberB{Value: []byte("r/1")},
			"v": &types.AttributeValueMemberN{Value: "2"}, "val": &types.AttributeValueMemberB{Value: []byte("rival")},
		}}); err != nil {
			t.Error(err)
		}
	}
	// No condition: the put is the next version of whatever is there when it commits.
	e.api.beforeWrite = rival
	res := e.mustTransact(t, &storev1alpha1.TransactRequest{Puts: []*storev1alpha1.Put{put("r/1", "mine")}})
	if res.GetItems()[0].GetVersion() != 3 {
		t.Fatalf("an unconditional put after a lost race is version %d, want 3", res.GetItems()[0].GetVersion())
	}
	// A condition on the version that was read: judged again on the new state, and refused.
	e.api.beforeWrite = func() {
		if _, err := e.raw.PutItem(context.Background(), &awsdynamodb.PutItemInput{TableName: aws.String(e.table), Item: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberB{Value: []byte{0x01}}, "sk": &types.AttributeValueMemberB{Value: []byte("r/1")},
			"v": &types.AttributeValueMemberN{Value: "4"}, "val": &types.AttributeValueMemberB{Value: []byte("rival again")},
		}}); err != nil {
			t.Error(err)
		}
	}
	_, err := e.transact(t, &storev1alpha1.TransactRequest{Conditions: []*storev1alpha1.Condition{atVersion("r/1", 3)}, Puts: []*storev1alpha1.Put{put("r/1", "late")}})
	wantRefusal(t, err, connect.CodeAborted, "version")
	if got, _ := e.get(t, "r/1"); string(got.GetValue()) != "rival again" || got.GetVersion() != 4 {
		t.Fatalf("the item after the refused update: %v", got)
	}
}

// A read at the level that was asked for: Get and Query pass ConsistentRead when linearizable is set, and do not when it is not.
func TestConsistentReadFollowsTheFlag(t *testing.T) {
	raw := localClient(t)
	var seen []bool
	var mu sync.Mutex
	rec := &recorder{Client: raw, onRead: func(b *bool) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, aws.ToBool(b))
	}}
	table := fmt.Sprintf("atoom-test-%d-%d", os.Getpid(), tableSeq.Add(1))
	if err := dynamodb.EnsureTable(context.Background(), raw, table); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = raw.DeleteTable(context.Background(), &awsdynamodb.DeleteTableInput{TableName: aws.String(table)})
	})
	svc, _ := dynamodb.New(dynamodb.Options{DynamoDB: rec, Table: table})
	mux := http.NewServeMux()
	mux.Handle(svc.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := storev1alpha1connect.NewItemServiceClient(srv.Client(), srv.URL)
	ctx := context.Background()
	for _, lin := range []bool{true, false} {
		_, _ = c.Get(ctx, connect.NewRequest(&storev1alpha1.ItemGetRequest{Key: []byte("x/1"), Linearizable: lin}))
		_, _ = c.Query(ctx, connect.NewRequest(&storev1alpha1.QueryRequest{Prefix: []byte("x/"), Linearizable: lin}))
		_, _ = c.Query(ctx, connect.NewRequest(&storev1alpha1.QueryRequest{Index: "i", IndexValue: []byte("v"), Linearizable: lin}))
	}
	want := []bool{true, true, true, false, false, false}
	if fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Fatalf("ConsistentRead of the reads: %v, want %v", seen, want)
	}
}

type recorder struct {
	*awsdynamodb.Client
	onRead func(*bool)
}

func (r *recorder) GetItem(ctx context.Context, in *awsdynamodb.GetItemInput, o ...func(*awsdynamodb.Options)) (*awsdynamodb.GetItemOutput, error) {
	r.onRead(in.ConsistentRead)
	return r.Client.GetItem(ctx, in, o...)
}

func (r *recorder) Query(ctx context.Context, in *awsdynamodb.QueryInput, o ...func(*awsdynamodb.Options)) (*awsdynamodb.QueryOutput, error) {
	r.onRead(in.ConsistentRead)
	return r.Client.Query(ctx, in, o...)
}
