package transport_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	transportv1alpha1 "github.com/truvity/atoom/transport/gen/atoom/transport/v1alpha1"
	"github.com/truvity/atoom/transport/gen/atoom/transport/v1alpha1/transportv1alpha1connect"
)

// The procedure names are part of the contract: a driver in another language is dialled by them.
func TestTheProceduresHaveTheNamesOfTheContract(t *testing.T) {
	for got, want := range map[string]string{
		transportv1alpha1connect.TransportServiceEmitProcedure:    "/atoom.transport.v1alpha1.TransportService/Emit",
		transportv1alpha1connect.TransportServiceReceiveProcedure: "/atoom.transport.v1alpha1.TransportService/Receive",
		transportv1alpha1connect.TransportServiceAckProcedure:     "/atoom.transport.v1alpha1.TransportService/Ack",
	} {
		if got != want {
			t.Errorf("procedure = %q, want %q", got, want)
		}
	}
}

// The field numbers are the ones sluis used, so that the bytes of a message did not change with the move.
func TestAnEmitRequestKeepsTheFieldNumbersOfSluis(t *testing.T) {
	b, err := proto.Marshal(&transportv1alpha1.EmitRequest{
		Message:  &transportv1alpha1.AuditMessage{Uid: "u", Record: []byte("r"), Usage: [][]byte{[]byte("a")}},
		Blocking: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// message = 1 (uid = 1, record = 2, usage = 3), blocking = 2.
	want := []byte{0x0a, 9, 0x0a, 1, 'u', 0x12, 1, 'r', 0x1a, 1, 'a', 0x10, 1}
	if string(b) != string(want) {
		t.Fatalf("EmitRequest bytes = %x, want %x", b, want)
	}
}

type fake struct {
	transportv1alpha1connect.UnimplementedTransportServiceHandler
}

func (fake) Emit(_ context.Context, req *connect.Request[transportv1alpha1.EmitRequest]) (*connect.Response[transportv1alpha1.EmitResponse], error) {
	if req.Msg.GetMessage().GetUid() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}
	return connect.NewResponse(&transportv1alpha1.EmitResponse{}), nil
}

// A Go consumer mounts a driver's handler on an in-memory server and calls it through the client, which is how a library is linked in
// process (docs/plan.md, section 1).
func TestTheClientCallsTheHandler(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(transportv1alpha1connect.NewTransportServiceHandler(fake{}))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := transportv1alpha1connect.NewTransportServiceClient(srv.Client(), srv.URL)
	if _, err := c.Emit(context.Background(), connect.NewRequest(&transportv1alpha1.EmitRequest{
		Message: &transportv1alpha1.AuditMessage{Uid: "u", Record: []byte("{}")},
	})); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Ack(context.Background(), connect.NewRequest(&transportv1alpha1.AckRequest{DeliveryIds: []uint64{1}})); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("an unserved call: code = %v, want unimplemented (%v)", connect.CodeOf(err), err)
	}
}
