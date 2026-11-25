package nats_test

// The tests run the driver over a real NATS server with JetStream, embedded in the test process (internal/natstest), through its Connect
// handler on an in-memory transport, as a Go host links it: on every platform, with no container. The conformance kit runs against the thin
// main over the same server, run as a process (tools/scripts/conformance-transport.sh).

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/common/call"
	"github.com/truvity/atoom/common/refusal"
	atoomsocket "github.com/truvity/atoom/socket"
	transportv1alpha1 "github.com/truvity/atoom/transport/gen/atoom/transport/v1alpha1"
	"github.com/truvity/atoom/transport/gen/atoom/transport/v1alpha1/transportv1alpha1connect"
	"github.com/truvity/atoom/transport/nats"
)

// mount serves the driver in process and returns a client of it.
func mount(t *testing.T, opts nats.Options) (transportv1alpha1connect.TransportServiceClient, *nats.Service) {
	t.Helper()
	svc, err := nats.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	path, h := svc.Handler()
	hc, stop := atoomsocket.InProcess(path, h)
	t.Cleanup(stop)
	return transportv1alpha1connect.NewTransportServiceClient(hc, "http://transport", call.ClientOptions()...), svc
}

func msg(uid string) *transportv1alpha1.Message {
	return &transportv1alpha1.Message{Id: uid, Payload: []byte("payload-" + uid)}
}

func emit(t *testing.T, c transportv1alpha1connect.TransportServiceClient, m *transportv1alpha1.Message) {
	t.Helper()
	if _, err := c.Emit(context.Background(), connect.NewRequest(&transportv1alpha1.EmitRequest{Message: m, Blocking: true})); err != nil {
		t.Fatalf("Emit %s: %v", m.GetId(), err)
	}
}

// consumer is an open Receive stream whose deliveries arrive on a channel.
type consumer struct {
	cancel context.CancelFunc
	got    chan *transportv1alpha1.Delivery
	done   chan error
}

func receive(t *testing.T, c transportv1alpha1connect.TransportServiceClient) *consumer {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	k := &consumer{cancel: cancel, got: make(chan *transportv1alpha1.Delivery, 100), done: make(chan error, 1)}
	go func() {
		// A driver lets go of a consumer that went away once its pull in progress ends: a refusal with already_exists before any delivery
		// is that, and the stream is opened again, for ten seconds (as the kit does).
		until := time.Now().Add(10 * time.Second)
		for {
			stream, err := c.Receive(ctx, connect.NewRequest(&transportv1alpha1.ReceiveRequest{}))
			if err != nil {
				k.done <- err
				return
			}
			n := 0
			for stream.Receive() {
				for _, d := range stream.Msg().GetDeliveries() {
					n++
					k.got <- d
				}
			}
			err = stream.Err()
			if n == 0 && connect.CodeOf(err) == connect.CodeAlreadyExists && time.Now().Before(until) {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			k.done <- err
			return
		}
	}()
	t.Cleanup(k.stop)
	return k
}

// stop drops the stream and waits until it ended.
func (k *consumer) stop() {
	k.cancel()
	err := <-k.done
	k.done <- err
}

func (k *consumer) take(t *testing.T, n int) []*transportv1alpha1.Delivery {
	t.Helper()
	var out []*transportv1alpha1.Delivery
	for len(out) < n {
		select {
		case d := <-k.got:
			out = append(out, d)
		case err := <-k.done:
			k.done <- err
			t.Fatalf("the stream ended after %d of %d: %v", len(out), n, err)
		case <-time.After(10 * time.Second):
			t.Fatalf("only %d of %d deliveries came", len(out), n)
		}
	}
	return out
}

func (k *consumer) quiet(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case got := <-k.got:
		t.Fatalf("a delivery came: %s (%d)", got.GetMessage().GetId(), got.GetDeliveryId())
	case <-time.After(d):
	}
}

func ack(t *testing.T, c transportv1alpha1connect.TransportServiceClient, ds ...*transportv1alpha1.Delivery) {
	t.Helper()
	ids := make([]uint64, 0, len(ds))
	for _, d := range ds {
		ids = append(ids, d.GetDeliveryId())
	}
	if _, err := c.Ack(context.Background(), connect.NewRequest(&transportv1alpha1.AckRequest{DeliveryIds: ids})); err != nil {
		t.Fatalf("Ack: %v", err)
	}
}

func uids(ds []*transportv1alpha1.Delivery) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.GetMessage().GetId())
	}
	return out
}

func wantRefusal(t *testing.T, err error, code connect.Code, reason, field string) {
	t.Helper()
	if connect.CodeOf(err) != code {
		t.Fatalf("code %v (%v), want %v", connect.CodeOf(err), err, code)
	}
	if got, _ := refusal.Reason(err); got != reason {
		t.Fatalf("reason %q, want %q", got, reason)
	}
	if got, _ := refusal.FieldPath(err); got != field {
		t.Fatalf("field %q, want %q", got, field)
	}
}
