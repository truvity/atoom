package blob_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	blobv1alpha1 "github.com/truvity/atoom/blob/gen/atoom/blob/v1alpha1"
	"github.com/truvity/atoom/blob/gen/atoom/blob/v1alpha1/blobv1alpha1connect"
)

// The procedure names are part of the contract: a driver in another language is dialled by them.
func TestTheProceduresHaveTheNamesOfTheContract(t *testing.T) {
	for got, want := range map[string]string{
		blobv1alpha1connect.BlobServiceCreateProcedure: "/atoom.blob.v1alpha1.BlobService/Create",
		blobv1alpha1connect.BlobServicePutProcedure:    "/atoom.blob.v1alpha1.BlobService/Put",
		blobv1alpha1connect.BlobServiceGetProcedure:    "/atoom.blob.v1alpha1.BlobService/Get",
		blobv1alpha1connect.BlobServiceListProcedure:   "/atoom.blob.v1alpha1.BlobService/List",
	} {
		if got != want {
			t.Errorf("procedure = %q, want %q", got, want)
		}
	}
}

// The field numbers are the ones sluis used, so that the bytes of a message did not change with the move.
func TestAPutRequestKeepsTheFieldNumbersOfSluis(t *testing.T) {
	b, err := proto.Marshal(&blobv1alpha1.PutRequest{Path: "p", Data: []byte("d"), IfMatch: "t"})
	if err != nil {
		t.Fatal(err)
	}
	// path = 1, data = 2, if_match = 3.
	want := []byte{0x0a, 1, 'p', 0x12, 1, 'd', 0x1a, 1, 't'}
	if string(b) != string(want) {
		t.Fatalf("PutRequest bytes = %x, want %x", b, want)
	}
}

type fake struct {
	blobv1alpha1connect.UnimplementedBlobServiceHandler
}

func (fake) Get(_ context.Context, req *connect.Request[blobv1alpha1.BlobGetRequest]) (*connect.Response[blobv1alpha1.BlobGetResponse], error) {
	return connect.NewResponse(&blobv1alpha1.BlobGetResponse{Data: []byte(req.Msg.GetPath()), Etag: "1"}), nil
}

// A Go consumer mounts a driver's handler on an in-memory server and calls it through the client, which is how a library is linked in
// process (docs/archive/extraction-plan.md, section 1).
func TestTheClientCallsTheHandler(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(blobv1alpha1connect.NewBlobServiceHandler(fake{}))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := blobv1alpha1connect.NewBlobServiceClient(srv.Client(), srv.URL)
	res, err := c.Get(context.Background(), connect.NewRequest(&blobv1alpha1.BlobGetRequest{Path: "log/checkpoint"}))
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Msg.GetData()) != "log/checkpoint" || res.Msg.GetEtag() != "1" {
		t.Fatalf("get = %v", res.Msg)
	}
	_, err = c.Create(context.Background(), connect.NewRequest(&blobv1alpha1.CreateRequest{Path: "a", Data: []byte("x")}))
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("an unserved call: code = %v, want unimplemented (%v)", connect.CodeOf(err), err)
	}
}
