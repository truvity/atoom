package store_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	commonv1alpha1 "github.com/truvity/atoom/common/gen/atoom/common/v1alpha1"
	storev1alpha1 "github.com/truvity/atoom/store/gen/atoom/store/v1alpha1"
	"github.com/truvity/atoom/store/gen/atoom/store/v1alpha1/storev1alpha1connect"
)

// The procedure names are part of the contract: a driver in another language is dialled by them.
func TestTheProceduresHaveTheNamesOfTheContract(t *testing.T) {
	for got, want := range map[string]string{
		storev1alpha1connect.ItemServiceGetProcedure:      "/atoom.store.v1alpha1.ItemService/Get",
		storev1alpha1connect.ItemServiceQueryProcedure:    "/atoom.store.v1alpha1.ItemService/Query",
		storev1alpha1connect.ItemServiceTransactProcedure: "/atoom.store.v1alpha1.ItemService/Transact",
		storev1alpha1connect.ItemServiceTailLogProcedure:  "/atoom.store.v1alpha1.ItemService/TailLog",
	} {
		if got != want {
			t.Errorf("procedure = %q, want %q", got, want)
		}
	}
}

// The field numbers are the ones sluis used, so that the bytes of a message did not change with the move.
func TestAnItemKeepsTheFieldNumbersOfSluis(t *testing.T) {
	in := &storev1alpha1.DriverItem{
		Key:     []byte("k"),
		Version: 3,
		Value:   []byte("v"),
		Indexes: []*commonv1alpha1.IndexEntry{{Name: "i", Value: []byte("x")}},
	}
	b, err := proto.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	// key = 1, version = 2, value = 3, indexes = 5 (expires_at = 4 is absent).
	want := []byte{0x0a, 1, 'k', 0x10, 3, 0x1a, 1, 'v', 0x2a, 6, 0x0a, 1, 'i', 0x12, 1, 'x'}
	if string(b) != string(want) {
		t.Fatalf("DriverItem bytes = %x, want %x", b, want)
	}
}

type fake struct {
	storev1alpha1connect.UnimplementedItemServiceHandler
}

func (fake) Get(_ context.Context, req *connect.Request[storev1alpha1.ItemGetRequest]) (*connect.Response[storev1alpha1.ItemGetResponse], error) {
	return connect.NewResponse(&storev1alpha1.ItemGetResponse{
		Item: &storev1alpha1.DriverItem{Key: req.Msg.GetKey(), Version: 1},
	}), nil
}

// A Go consumer mounts a driver's handler on an in-memory server and calls it through the client, which is how a library is linked in
// process (docs/plan.md, section 1).
func TestTheClientCallsTheHandler(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(storev1alpha1connect.NewItemServiceHandler(fake{}))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := storev1alpha1connect.NewItemServiceClient(srv.Client(), srv.URL)
	res, err := c.Get(context.Background(), connect.NewRequest(&storev1alpha1.ItemGetRequest{Key: []byte("a/b")}))
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Msg.GetItem(); string(got.GetKey()) != "a/b" || got.GetVersion() != 1 {
		t.Fatalf("item = %v", got)
	}
	if _, err := c.Query(context.Background(), connect.NewRequest(&storev1alpha1.QueryRequest{})); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("an unserved call: code = %v, want unimplemented (%v)", connect.CodeOf(err), err)
	}
}
