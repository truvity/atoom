package source_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	sourcev1alpha1 "github.com/truvity/atoom/source/gen/atoom/source/v1alpha1"
	"github.com/truvity/atoom/source/gen/atoom/source/v1alpha1/sourcev1alpha1connect"
)

// The procedure names are part of the contract: a driver in another language is dialled by them.
func TestTheProceduresHaveTheNamesOfTheContract(t *testing.T) {
	for got, want := range map[string]string{
		sourcev1alpha1connect.SourceServiceCapabilitiesProcedure: "/atoom.source.v1alpha1.SourceService/Capabilities",
		sourcev1alpha1connect.SourceServiceReadProcedure:         "/atoom.source.v1alpha1.SourceService/Read",
		sourcev1alpha1connect.SourceServiceWatchProcedure:        "/atoom.source.v1alpha1.SourceService/Watch",
	} {
		if got != want {
			t.Errorf("procedure = %q, want %q", got, want)
		}
	}
}

// The field numbers are the ones sluis used, so that the bytes of a message did not change with the move.
func TestAReadResponseKeepsTheFieldNumbersOfSluis(t *testing.T) {
	in := &sourcev1alpha1.ReadResponse{
		Unchanged: true,
		Data:      []byte("d"),
		Version:   "v",
		PollAfter: durationpb.New(2 * time.Second),
	}
	b, err := proto.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	// unchanged = 1, data = 2, version = 3, poll_after = 4 (seconds = 1 inside).
	want := []byte{0x08, 1, 0x12, 1, 'd', 0x1a, 1, 'v', 0x22, 2, 0x08, 2}
	if string(b) != string(want) {
		t.Fatalf("ReadResponse bytes = %x, want %x", b, want)
	}
	if got := sourcev1alpha1.Watch_NOTIFY; got != 2 {
		t.Fatalf("Watch_NOTIFY = %d, want 2", got)
	}
}

type fake struct {
	sourcev1alpha1connect.UnimplementedSourceServiceHandler
}

func (fake) Read(_ context.Context, req *connect.Request[sourcev1alpha1.ReadRequest]) (*connect.Response[sourcev1alpha1.ReadResponse], error) {
	return connect.NewResponse(&sourcev1alpha1.ReadResponse{Data: []byte(req.Msg.GetName()), Version: "1"}), nil
}

// A Go consumer mounts a driver's handler on an in-memory server and calls it through the client, which is how a library is linked in
// process (docs/plan.md, section 1).
func TestTheClientCallsTheHandler(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(sourcev1alpha1connect.NewSourceServiceHandler(fake{}))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := sourcev1alpha1connect.NewSourceServiceClient(srv.Client(), srv.URL)
	res, err := c.Read(context.Background(), connect.NewRequest(&sourcev1alpha1.ReadRequest{Name: "boundary"}))
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Msg.GetData()) != "boundary" || res.Msg.GetVersion() != "1" {
		t.Fatalf("read = %v", res.Msg)
	}
	if _, err := c.Capabilities(context.Background(), connect.NewRequest(&sourcev1alpha1.CapabilitiesRequest{})); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("an unserved call: code = %v, want unimplemented (%v)", connect.CodeOf(err), err)
	}
}
