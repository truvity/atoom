package sqs_test

// The tests of this file run the driver over an in-process fake of an SQS FIFO queue (internal/sqsfake), through its Connect handler on an
// in-memory transport, as a Go host links it: on every platform, with no container. local_test.go runs the driver through the real AWS SDK
// client against ElasticMQ, and the conformance kit runs against the thin main over ElasticMQ (tools/scripts/conformance-transport.sh).

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/common/call"
	"github.com/truvity/atoom/common/refusal"
	atoomsocket "github.com/truvity/atoom/socket"
	transportv1alpha1 "github.com/truvity/atoom/transport/gen/atoom/transport/v1alpha1"
	"github.com/truvity/atoom/transport/gen/atoom/transport/v1alpha1/transportv1alpha1connect"
	"github.com/truvity/atoom/transport/sqs"
	"github.com/truvity/atoom/transport/sqs/internal/sqsfake"
)

// mount serves the driver in process and returns a client of it.
func mount(t *testing.T, opts sqs.Options) (transportv1alpha1connect.TransportServiceClient, *sqs.Service) {
	t.Helper()
	svc, err := sqs.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	path, h := svc.Handler()
	hc, stop := atoomsocket.InProcess(path, h)
	t.Cleanup(stop)
	return transportv1alpha1connect.NewTransportServiceClient(hc, "http://transport", call.ClientOptions()...), svc
}

func fakeOpts(f *sqsfake.Fake) sqs.Options {
	return sqs.Options{SQS: f, QueueURL: sqsfake.QueueURL, Emitter: "test@host", WaitTime: time.Second}
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

func TestMessagesComeInOrderAsEmittedWithTheHostsEmitter(t *testing.T) {
	f := sqsfake.New()
	c, svc := mount(t, fakeOpts(f))
	for _, u := range []string{"a", "b", "c"} {
		emit(t, c, msg(u))
	}
	k := receive(t, c)
	got := k.take(t, 3)
	if !slices.Equal(uids(got), []string{"a", "b", "c"}) {
		t.Fatalf("order %v", uids(got))
	}
	for i, d := range got {
		if d.GetEmitter() != "test@host" || d.GetDeliveryId() == 0 || string(d.GetMessage().GetPayload()) != "payload-"+d.GetMessage().GetId() {
			t.Fatalf("delivery %d: %v", i, d)
		}
		if i > 0 && d.GetDeliveryId() <= got[i-1].GetDeliveryId() {
			t.Fatalf("the ids do not increase: %d then %d", got[i-1].GetDeliveryId(), d.GetDeliveryId())
		}
	}
	if !slices.Equal(svc.Held(), []uint64{got[0].GetDeliveryId(), got[1].GetDeliveryId(), got[2].GetDeliveryId()}) {
		t.Fatalf("held %v", svc.Held())
	}
	ack(t, c, got...)
	if f.Len() != 0 || len(svc.Held()) != 0 {
		t.Fatalf("after the ack: %d in the queue, %v held", f.Len(), svc.Held())
	}
}

// A FIFO queue hands out nothing more of a group while part of it is in flight: the driver delivers one receive ahead of the
// acknowledgements, and the next message comes once the consumer acknowledges.
func TestTheConsumerAcknowledgesToReceiveMore(t *testing.T) {
	f := sqsfake.New()
	c, _ := mount(t, fakeOpts(f))
	k := receive(t, c)
	emit(t, c, msg("one"))
	first := k.take(t, 1)
	emit(t, c, msg("two"))
	k.quiet(t, 300*time.Millisecond)
	ack(t, c, first...)
	if got := k.take(t, 1); got[0].GetMessage().GetId() != "two" {
		t.Fatalf("after the ack: %v", uids(got))
	}
}

// What the driver holds when a stream ends comes first on the next one, under the same ids, without a new receive of SQS; an Ack made
// after the stream ended still deletes.
func TestHeldMessagesComeAgainUnderTheSameIDsAndAnAckAfterTheStreamStillDeletes(t *testing.T) {
	f := sqsfake.New()
	c, _ := mount(t, fakeOpts(f))
	emit(t, c, msg("a"))
	emit(t, c, msg("b"))
	k := receive(t, c)
	first := k.take(t, 2)
	k.stop()
	k = receive(t, c)
	again := k.take(t, 2)
	for i := range first {
		if first[i].GetDeliveryId() != again[i].GetDeliveryId() || first[i].GetMessage().GetId() != again[i].GetMessage().GetId() {
			t.Fatalf("came again as %v, first as %v", again, first)
		}
	}
	if r := f.Receives(); !slices.Equal(r, []int{1, 1}) {
		t.Fatalf("SQS handed the messages out %v times; the driver should send what it holds", r)
	}
	k.stop()
	ack(t, c, again...)
	if f.Len() != 0 {
		t.Fatalf("%d messages left after an ack made after the stream ended", f.Len())
	}
}

// A message whose visibility timeout passes while the consumer holds it comes again from SQS: the driver takes its new receipt handle and
// does not deliver it twice on the stream, and the ack deletes it by the new handle.
func TestAMessageThatComesAgainWhileHeldIsNotDeliveredTwice(t *testing.T) {
	f := sqsfake.New()
	f.SetVisibility(200 * time.Millisecond)
	c, _ := mount(t, fakeOpts(f))
	emit(t, c, msg("a"))
	k := receive(t, c)
	got := k.take(t, 1)
	k.quiet(t, 700*time.Millisecond)
	if r := f.Receives(); len(r) != 1 || r[0] < 2 {
		t.Fatalf("the message was handed out %v times; want it to come again from SQS", r)
	}
	f.SetVisibility(30 * time.Second)
	// Let the receive in progress take the message again with the long timeout, so that the handle held is the latest.
	time.Sleep(400 * time.Millisecond)
	ack(t, c, got...)
	if f.Len() != 0 {
		t.Fatalf("%d messages left: the ack did not use the latest handle", f.Len())
	}
}

// D50: an emit made again under the same uid (a producer's retry) and a redelivery end as one record for a consumer that keeps one record
// for each uid. The queue drops the retry within its five minutes; a restart of the driver delivers the message again under a new, greater
// id, and the uid is the same.
func TestARetryAndARedeliveryAreOneRecordByUID(t *testing.T) {
	f := sqsfake.New()
	f.SetVisibility(500 * time.Millisecond)
	c, _ := mount(t, fakeOpts(f))
	m := msg("usage-1")
	emit(t, c, m)
	emit(t, c, m)
	if f.Len() != 1 {
		t.Fatalf("%d messages for one uid emitted twice; want the queue to drop the retry", f.Len())
	}
	k := receive(t, c)
	first := k.take(t, 1)
	k.stop()

	// A new driver on the same queue, as after a restart: the message comes again once its visibility timeout has passed.
	c2, _ := mount(t, fakeOpts(f))
	k2 := receive(t, c2)
	records := map[string]*transportv1alpha1.Message{first[0].GetMessage().GetId(): first[0].GetMessage()}
	var again []*transportv1alpha1.Delivery
	deadline := time.Now().Add(10 * time.Second)
	for len(again) == 0 && time.Now().Before(deadline) {
		select {
		case d := <-k2.got:
			again = append(again, d)
		case <-time.After(100 * time.Millisecond):
		}
	}
	if len(again) != 1 {
		t.Fatalf("the message did not come again after the restart")
	}
	d := again[0]
	if d.GetDeliveryId() <= first[0].GetDeliveryId() {
		t.Fatalf("the id after the restart %d is not greater than %d", d.GetDeliveryId(), first[0].GetDeliveryId())
	}
	if old, ok := records[d.GetMessage().GetId()]; !ok || string(old.GetPayload()) != string(d.GetMessage().GetPayload()) {
		t.Fatalf("the redelivery is not the same message: %v", d)
	}
	ack(t, c2, d)
	if len(records) != 1 || f.Len() != 0 {
		t.Fatalf("%d records, %d messages left", len(records), f.Len())
	}
}

func TestASecondConsumerIsRefused(t *testing.T) {
	f := sqsfake.New()
	c, _ := mount(t, fakeOpts(f))
	emit(t, c, msg("a"))
	k := receive(t, c)
	k.take(t, 1)
	stream, err := c.Receive(context.Background(), connect.NewRequest(&transportv1alpha1.ReceiveRequest{}))
	if err == nil {
		for stream.Receive() {
			t.Fatal("a second consumer was served")
		}
		err = stream.Err()
	}
	wantRefusal(t, err, connect.CodeAlreadyExists, "taken", "")
}

func TestEveryRefusalNamesAReason(t *testing.T) {
	f := sqsfake.New()
	c, _ := mount(t, fakeOpts(f))
	for _, tc := range []struct {
		m     *transportv1alpha1.Message
		field string
	}{
		{nil, "message"},
		{&transportv1alpha1.Message{Payload: []byte("r")}, "message.id"},
		{&transportv1alpha1.Message{Id: "u"}, "message.payload"},
	} {
		_, err := c.Emit(context.Background(), connect.NewRequest(&transportv1alpha1.EmitRequest{Message: tc.m}))
		wantRefusal(t, err, connect.CodeInvalidArgument, "schema", tc.field)
	}
	big := &transportv1alpha1.Message{Id: "u", Payload: make([]byte, sqs.MessageMax)}
	_, err := c.Emit(context.Background(), connect.NewRequest(&transportv1alpha1.EmitRequest{Message: big}))
	wantRefusal(t, err, connect.CodeInvalidArgument, "bound", "message")
	_, err = c.Ack(context.Background(), connect.NewRequest(&transportv1alpha1.AckRequest{DeliveryIds: make([]uint64, sqs.AckMax+1)}))
	wantRefusal(t, err, connect.CodeInvalidArgument, "bound", "delivery_ids")
	if n := f.Calls("SendMessage"); n != 0 {
		t.Fatalf("a refused emit reached SQS (%d calls)", n)
	}
	// An unknown id is ignored.
	if _, err := c.Ack(context.Background(), connect.NewRequest(&transportv1alpha1.AckRequest{DeliveryIds: []uint64{7}})); err != nil {
		t.Fatalf("an unknown id: %v", err)
	}
}

// A host that tells its callers apart names the emitter; one it cannot name is refused with the reason caller.
func TestTheHostNamesTheEmitter(t *testing.T) {
	f := sqsfake.New()
	opts := fakeOpts(f)
	opts.Emitter = ""
	opts.Identify = func(ctx context.Context) (string, bool) {
		id := call.CorrelationID(ctx)
		return "actor@" + id, strings.HasPrefix(id, "corr-")
	}
	c, _ := mount(t, opts)
	ctx := call.WithCorrelationID(context.Background(), "corr-0001")
	if _, err := c.Emit(ctx, connect.NewRequest(&transportv1alpha1.EmitRequest{Message: msg("a")})); err != nil {
		t.Fatal(err)
	}
	_, err := c.Emit(call.WithCorrelationID(context.Background(), "other-0001"), connect.NewRequest(&transportv1alpha1.EmitRequest{Message: msg("b")}))
	wantRefusal(t, err, connect.CodePermissionDenied, "caller", "")
	k := receive(t, c)
	if d := k.take(t, 1)[0]; d.GetEmitter() != "actor@corr-0001" {
		t.Fatalf("emitter %q", d.GetEmitter())
	}
}

// A failure of SQS is unavailable/engine, with SQS's text kept out of the answer; nothing is dropped.
func TestAFailureOfSQSIsEngine(t *testing.T) {
	f := sqsfake.New()
	c, svc := mount(t, fakeOpts(f))
	f.Fail("SendMessage", "ServiceUnavailable")
	_, err := c.Emit(context.Background(), connect.NewRequest(&transportv1alpha1.EmitRequest{Message: msg("a")}))
	wantRefusal(t, err, connect.CodeUnavailable, "engine", "")
	if strings.Contains(err.Error(), "fake") {
		t.Fatalf("SQS's text reached the caller: %v", err)
	}
	f.Fail("SendMessage", "")
	emit(t, c, msg("a"))

	f.Fail("ReceiveMessage", "ServiceUnavailable")
	stream, err := c.Receive(context.Background(), connect.NewRequest(&transportv1alpha1.ReceiveRequest{}))
	if err == nil {
		for stream.Receive() {
			t.Fatal("a delivery came while SQS fails")
		}
		err = stream.Err()
	}
	wantRefusal(t, err, connect.CodeUnavailable, "engine", "")
	f.Fail("ReceiveMessage", "")

	k := receive(t, c)
	got := k.take(t, 1)
	f.Fail("DeleteMessageBatch", "ServiceUnavailable")
	_, err = c.Ack(context.Background(), connect.NewRequest(&transportv1alpha1.AckRequest{DeliveryIds: []uint64{got[0].GetDeliveryId()}}))
	wantRefusal(t, err, connect.CodeUnavailable, "engine", "")
	if len(svc.Held()) != 1 {
		t.Fatalf("a failed ack let go of the message: %v", svc.Held())
	}
	f.Fail("DeleteMessageBatch", "")
	ack(t, c, got...)
	if f.Len() != 0 {
		t.Fatalf("%d left", f.Len())
	}
}

// A message that another writer put in the queue, which the driver cannot read, is left to SQS and its redrive policy, and not delivered.
func TestAMessageTheDriverDidNotWriteIsNotDelivered(t *testing.T) {
	f := sqsfake.New()
	c, _ := mount(t, fakeOpts(f))
	f.Put("other", "not base64 !")
	emit(t, c, msg("a"))
	k := receive(t, c)
	if got := k.take(t, 1); got[0].GetMessage().GetId() != "a" {
		t.Fatalf("%v", uids(got))
	}
}

func TestTheOptionsAreChecked(t *testing.T) {
	f := sqsfake.New()
	good := fakeOpts(f)
	for name, mut := range map[string]func(o *sqs.Options){
		"no client":                func(o *sqs.Options) { o.SQS = nil },
		"no queue":                 func(o *sqs.Options) { o.QueueURL = "" },
		"a standard queue":         func(o *sqs.Options) { o.QueueURL = strings.TrimSuffix(sqsfake.QueueURL, ".fifo") },
		"no emitter":               func(o *sqs.Options) { o.Emitter = "" },
		"an emitter too long":      func(o *sqs.Options) { o.Emitter = strings.Repeat("e", sqs.EmitterMax+1) },
		"a message bound too high": func(o *sqs.Options) { o.MessageMax = sqs.MessageMaxLimit + 1 },
		"a wait too long":          func(o *sqs.Options) { o.WaitTime = 21 * time.Second },
		"a part of a second":       func(o *sqs.Options) { o.WaitTime = 1500 * time.Millisecond },
		"a visibility too long":    func(o *sqs.Options) { o.VisibilityTimeout = 13 * time.Hour },
	} {
		o := good
		mut(&o)
		if _, err := sqs.New(o); !errors.Is(err, sqs.ErrOptions) {
			t.Errorf("%s: %v, want ErrOptions", name, err)
		}
	}
	if _, err := sqs.New(good); err != nil {
		t.Fatal(err)
	}
}

func TestTheDeduplicationIDIsTheSHA256OfTheUID(t *testing.T) {
	id := sqs.DeduplicationID("0190a8b4-7e5f-7c3a-9b1e-2f3d4c5b6a79")
	if len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" || id == sqs.DeduplicationID("other") {
		t.Fatalf("%q", id)
	}
}
