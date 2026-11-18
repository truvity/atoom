package index_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	indexv1alpha1 "github.com/truvity/atoom/index/gen/atoom/index/v1alpha1"
	"github.com/truvity/atoom/index/gen/atoom/index/v1alpha1/indexv1alpha1connect"
)

// The procedure names are part of the contract: a driver in another language is dialled by them.
func TestTheProceduresHaveTheNamesOfTheContract(t *testing.T) {
	for got, want := range map[string]string{
		indexv1alpha1connect.IndexServiceGetCursorProcedure:  "/atoom.index.v1alpha1.IndexService/GetCursor",
		indexv1alpha1connect.IndexServiceApplyProcedure:      "/atoom.index.v1alpha1.IndexService/Apply",
		indexv1alpha1connect.IndexReadServiceSearchProcedure: "/atoom.index.v1alpha1.IndexReadService/Search",
	} {
		if got != want {
			t.Errorf("procedure = %q, want %q", got, want)
		}
	}
}

// The field numbers are the ones sluis used, so that the bytes of a message did not change with the move.
func TestACursorKeepsTheFieldNumbersOfSluis(t *testing.T) {
	b, err := proto.Marshal(&indexv1alpha1.Cursor{Size: 3, Root: []byte("r"), State: []byte("s")})
	if err != nil {
		t.Fatal(err)
	}
	// size = 1, root = 2, state = 3.
	want := []byte{0x08, 3, 0x12, 1, 'r', 0x1a, 1, 's'}
	if string(b) != string(want) {
		t.Fatalf("Cursor bytes = %x, want %x", b, want)
	}
}

type write struct {
	indexv1alpha1connect.UnimplementedIndexServiceHandler
}

func (write) GetCursor(_ context.Context, req *connect.Request[indexv1alpha1.GetCursorRequest]) (*connect.Response[indexv1alpha1.GetCursorResponse], error) {
	return connect.NewResponse(&indexv1alpha1.GetCursorResponse{Cursor: &indexv1alpha1.Cursor{Size: int64(len(req.Msg.GetLog().GetLog()))}}), nil
}

type read struct {
	indexv1alpha1connect.UnimplementedIndexReadServiceHandler
}

func (read) Search(_ context.Context, req *connect.Request[indexv1alpha1.SearchRequest]) (*connect.Response[indexv1alpha1.SearchResponse], error) {
	return connect.NewResponse(&indexv1alpha1.SearchResponse{Rows: []*indexv1alpha1.Row{{RecordId: req.Msg.GetQ()}}}), nil
}

// A Go consumer mounts a driver's handlers on an in-memory server and calls them through the clients, which is how a library is linked
// in process (docs/plan.md, section 1). The two sides are two services, so a host can put them on two servers.
func TestTheClientsCallTheHandlers(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(indexv1alpha1connect.NewIndexServiceHandler(write{}))
	mux.Handle(indexv1alpha1connect.NewIndexReadServiceHandler(read{}))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	w := indexv1alpha1connect.NewIndexServiceClient(srv.Client(), srv.URL)
	got, err := w.GetCursor(context.Background(), connect.NewRequest(&indexv1alpha1.GetCursorRequest{Log: &indexv1alpha1.LogRef{Log: "main"}}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg.GetCursor().GetSize() != 4 {
		t.Fatalf("cursor = %v", got.Msg)
	}
	if _, err := w.Apply(context.Background(), connect.NewRequest(&indexv1alpha1.ApplyRequest{})); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("an unserved call: code = %v, want unimplemented (%v)", connect.CodeOf(err), err)
	}
	r := indexv1alpha1connect.NewIndexReadServiceClient(srv.Client(), srv.URL)
	found, err := r.Search(context.Background(), connect.NewRequest(&indexv1alpha1.SearchRequest{Q: "exchange"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Msg.GetRows()) != 1 || found.Msg.GetRows()[0].GetRecordId() != "exchange" {
		t.Fatalf("search = %v", found.Msg)
	}
}
