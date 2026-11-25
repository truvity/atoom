package index_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	indexv1alpha2 "github.com/truvity/atoom/index/gen/atoom/index/v1alpha2"
	"github.com/truvity/atoom/index/gen/atoom/index/v1alpha2/indexv1alpha2connect"
)

// The procedure names are part of the contract: a driver in another language is dialled by them.
func TestTheProceduresHaveTheNamesOfTheContract(t *testing.T) {
	for got, want := range map[string]string{
		indexv1alpha2connect.IndexServiceGetCursorProcedure:  "/atoom.index.v1alpha2.IndexService/GetCursor",
		indexv1alpha2connect.IndexServiceApplyProcedure:      "/atoom.index.v1alpha2.IndexService/Apply",
		indexv1alpha2connect.IndexReadServiceSearchProcedure: "/atoom.index.v1alpha2.IndexReadService/Search",
	} {
		if got != want {
			t.Errorf("procedure = %q, want %q", got, want)
		}
	}
}

// The field numbers of the cursor are the ones sluis used: a verified position in a log did not change with record 0025.
func TestACursorKeepsTheFieldNumbersOfSluis(t *testing.T) {
	b, err := proto.Marshal(&indexv1alpha2.Cursor{Size: 3, Root: []byte("r"), State: []byte("s")})
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
	indexv1alpha2connect.UnimplementedIndexServiceHandler
}

func (write) GetCursor(_ context.Context, req *connect.Request[indexv1alpha2.GetCursorRequest]) (*connect.Response[indexv1alpha2.GetCursorResponse], error) {
	return connect.NewResponse(&indexv1alpha2.GetCursorResponse{Cursor: &indexv1alpha2.Cursor{Size: int64(len(req.Msg.GetLog().GetLog()))}}), nil
}

type read struct {
	indexv1alpha2connect.UnimplementedIndexReadServiceHandler
}

func (read) Search(_ context.Context, req *connect.Request[indexv1alpha2.SearchRequest]) (*connect.Response[indexv1alpha2.SearchResponse], error) {
	return connect.NewResponse(&indexv1alpha2.SearchResponse{Rows: []*indexv1alpha2.Row{{EntryId: req.Msg.GetQ()}}}), nil
}

// A Go consumer mounts a driver's handlers on an in-memory server and calls them through the clients, which is how a library is linked
// in process (docs/archive/extraction-plan.md, section 1). The two sides are two services, so a host can put them on two servers.
func TestTheClientsCallTheHandlers(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(indexv1alpha2connect.NewIndexServiceHandler(write{}))
	mux.Handle(indexv1alpha2connect.NewIndexReadServiceHandler(read{}))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	w := indexv1alpha2connect.NewIndexServiceClient(srv.Client(), srv.URL)
	got, err := w.GetCursor(context.Background(), connect.NewRequest(&indexv1alpha2.GetCursorRequest{Log: &indexv1alpha2.LogRef{Log: "main"}}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg.GetCursor().GetSize() != 4 {
		t.Fatalf("cursor = %v", got.Msg)
	}
	if _, err := w.Apply(context.Background(), connect.NewRequest(&indexv1alpha2.ApplyRequest{})); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("an unserved call: code = %v, want unimplemented (%v)", connect.CodeOf(err), err)
	}
	r := indexv1alpha2connect.NewIndexReadServiceClient(srv.Client(), srv.URL)
	found, err := r.Search(context.Background(), connect.NewRequest(&indexv1alpha2.SearchRequest{Q: "exchange"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Msg.GetRows()) != 1 || found.Msg.GetRows()[0].GetEntryId() != "exchange" {
		t.Fatalf("search = %v", found.Msg)
	}
}
