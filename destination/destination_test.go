package destination_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	destinationv1alpha1 "github.com/truvity/atoom/destination/gen/atoom/destination/v1alpha1"
	"github.com/truvity/atoom/destination/gen/atoom/destination/v1alpha1/destinationv1alpha1connect"
)

// The procedure names are part of the contract: a driver in another language is dialled by them.
func TestTheProceduresHaveTheNamesOfTheContract(t *testing.T) {
	for got, want := range map[string]string{
		destinationv1alpha1connect.DestinationServicePutProcedure:      "/atoom.destination.v1alpha1.DestinationService/Put",
		destinationv1alpha1connect.DestinationServiceDeleteProcedure:   "/atoom.destination.v1alpha1.DestinationService/Delete",
		destinationv1alpha1connect.DestinationServiceListProcedure:     "/atoom.destination.v1alpha1.DestinationService/List",
		destinationv1alpha1connect.DestinationServicePurgeProcedure:    "/atoom.destination.v1alpha1.DestinationService/Purge",
		destinationv1alpha1connect.DestinationServiceReadBackProcedure: "/atoom.destination.v1alpha1.DestinationService/ReadBack",
	} {
		if got != want {
			t.Errorf("procedure = %q, want %q", got, want)
		}
	}
}

// The field numbers are the ones sluis used, so that the bytes of a message did not change with the move.
func TestAPutRequestKeepsTheFieldNumbersOfSluis(t *testing.T) {
	b, err := proto.Marshal(&destinationv1alpha1.PutRequest{Address: "a", ContentType: "c", CacheControl: "k", Body: []byte("b")})
	if err != nil {
		t.Fatal(err)
	}
	// address = 1, content_type = 2, cache_control = 3, body = 4.
	want := []byte{0x0a, 1, 'a', 0x12, 1, 'c', 0x1a, 1, 'k', 0x22, 1, 'b'}
	if string(b) != string(want) {
		t.Fatalf("PutRequest bytes = %x, want %x", b, want)
	}
	if got := destinationv1alpha1.PurgeResult_PURGE_RESULT_PURGED; got != 2 {
		t.Fatalf("PURGE_RESULT_PURGED = %d, want 2", got)
	}
}

type fake struct {
	destinationv1alpha1connect.UnimplementedDestinationServiceHandler
}

func (fake) Put(_ context.Context, req *connect.Request[destinationv1alpha1.PutRequest]) (*connect.Response[destinationv1alpha1.PutResponse], error) {
	return connect.NewResponse(&destinationv1alpha1.PutResponse{Version: "1", Sha256: req.Msg.GetAddress()}), nil
}

// A Go consumer mounts a driver's handler on an in-memory server and calls it through the client, which is how a library is linked in
// process (docs/plan.md, section 1).
func TestTheClientCallsTheHandler(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(destinationv1alpha1connect.NewDestinationServiceHandler(fake{}))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := destinationv1alpha1connect.NewDestinationServiceClient(srv.Client(), srv.URL)
	res, err := c.Put(context.Background(), connect.NewRequest(&destinationv1alpha1.PutRequest{Address: "jwks.json"}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Msg.GetVersion() != "1" || res.Msg.GetSha256() != "jwks.json" {
		t.Fatalf("put = %v", res.Msg)
	}
	if _, err := c.ReadBack(context.Background(), connect.NewRequest(&destinationv1alpha1.ReadBackRequest{Address: "x"})); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("an unserved call: code = %v, want unimplemented (%v)", connect.CodeOf(err), err)
	}
}
