package nats_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/truvity/atoom/common/call"
	transportv1alpha1 "github.com/truvity/atoom/transport/gen/atoom/transport/v1alpha1"
	"github.com/truvity/atoom/transport/nats"
	"github.com/truvity/atoom/transport/nats/internal/natstest"
)

// server starts an embedded NATS server with JetStream and returns a JetStream context over a connection to it, and the server's URL.
func server(t *testing.T, token string) (jetstream.JetStream, string, func()) {
	t.Helper()
	url, stop, err := natstest.Start(natstest.Options{Dir: t.TempDir(), Token: token})
	if err != nil {
		t.Fatal(err)
	}
	var stopped bool
	halt := func() {
		if !stopped {
			stopped = true
			stop()
		}
	}
	t.Cleanup(halt)
	var opts []natsgo.Option
	if token != "" {
		opts = append(opts, natsgo.Token(token))
	}
	nc, err := natsgo.Connect(url, append(opts, natsgo.MaxReconnects(0))...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	return js, url, halt
}

func natsOpts(js jetstream.JetStream) nats.Options {
	return nats.Options{JetStream: js, Stream: "LOG", Subject: "log.records", Consumer: "writer", Create: true, Emitter: "test@host",
		WaitTime: 200 * time.Millisecond}
}

func streamMsgs(t *testing.T, js jetstream.JetStream) uint64 {
	t.Helper()
	s, err := js.Stream(context.Background(), "LOG")
	if err != nil {
		t.Fatal(err)
	}
	info, err := s.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return info.State.Msgs
}

func pendingAcks(t *testing.T, js jetstream.JetStream) (pending int, waiting uint64) {
	t.Helper()
	c, err := js.Consumer(context.Background(), "LOG", "writer")
	if err != nil {
		t.Fatal(err)
	}
	info, err := c.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return info.NumAckPending, info.NumPending
}

func TestMessagesComeInOrderWithTheirStreamSequenceAsTheID(t *testing.T) {
	js, _, _ := server(t, "")
	c, svc := mount(t, natsOpts(js))
	for _, u := range []string{"a", "b", "c"} {
		emit(t, c, msg(u))
	}
	k := receive(t, c)
	got := k.take(t, 3)
	if !slices.Equal(uids(got), []string{"a", "b", "c"}) {
		t.Fatalf("order %v", uids(got))
	}
	for i, d := range got {
		if d.GetDeliveryId() != uint64(i+1) || d.GetEmitter() != "test@host" || string(d.GetMessage().GetPayload()) != "payload-"+d.GetMessage().GetId() {
			t.Fatalf("delivery %d: %v", i, d)
		}
	}
	ack(t, c, got...)
	if p, _ := pendingAcks(t, js); p != 0 || len(svc.Held()) != 0 {
		t.Fatalf("after the ack: %d pending, %v held", p, svc.Held())
	}
	k.quiet(t, 500*time.Millisecond)
}

// D50: a publish made again with the same uid (a producer's retry) is dropped by JetStream through Nats-Msg-Id; a restart of the driver
// delivers what was not acknowledged again, under the same id (its stream sequence) and with the same bytes: one record by uid.
func TestARetryAndARedeliveryAreOneRecordByUID(t *testing.T) {
	js, _, _ := server(t, "")
	opts := natsOpts(js)
	opts.AckWait = time.Second
	c, _ := mount(t, opts)
	m := msg("usage-1")
	emit(t, c, m)
	emit(t, c, m)
	if n := streamMsgs(t, js); n != 1 {
		t.Fatalf("%d messages for one uid published twice; want JetStream to drop the retry", n)
	}
	k := receive(t, c)
	first := k.take(t, 1)[0]
	k.stop()

	c2, _ := mount(t, opts) // a new driver on the same stream and consumer, as after a restart
	k2 := receive(t, c2)
	again := k2.take(t, 1)[0]
	if again.GetDeliveryId() != first.GetDeliveryId() || again.GetMessage().GetId() != first.GetMessage().GetId() ||
		string(again.GetMessage().GetPayload()) != string(first.GetMessage().GetPayload()) {
		t.Fatalf("after the restart: %v, first %v", again, first)
	}
	records := map[string]bool{first.GetMessage().GetId(): true, again.GetMessage().GetId(): true}
	if len(records) != 1 {
		t.Fatalf("%d records", len(records))
	}
	ack(t, c2, again)
	k2.quiet(t, 1500*time.Millisecond)
}

// What the driver holds when a stream ends comes first on the next one, under the same ids; an Ack made after the stream ended still
// acknowledges.
func TestHeldMessagesComeAgainAndAnAckAfterTheStreamStillAcknowledges(t *testing.T) {
	js, _, _ := server(t, "")
	c, _ := mount(t, natsOpts(js))
	emit(t, c, msg("a"))
	emit(t, c, msg("b"))
	k := receive(t, c)
	first := k.take(t, 2)
	k.stop()
	k = receive(t, c)
	again := k.take(t, 2)
	for i := range first {
		if first[i].GetDeliveryId() != again[i].GetDeliveryId() {
			t.Fatalf("came again as %v, first as %v", again, first)
		}
	}
	k.stop()
	ack(t, c, again...)
	if p, w := pendingAcks(t, js); p != 0 || w != 0 {
		t.Fatalf("%d pending, %d waiting after an ack made after the stream ended", p, w)
	}
}

// A message whose ack wait passes while the consumer holds it is delivered again by JetStream: the driver keeps the new delivery, does not
// deliver it twice on the stream, and the ack acknowledges it.
func TestAMessageRedeliveredWhileHeldIsNotDeliveredTwice(t *testing.T) {
	js, _, _ := server(t, "")
	opts := natsOpts(js)
	opts.AckWait = 500 * time.Millisecond
	c, _ := mount(t, opts)
	emit(t, c, msg("a"))
	k := receive(t, c)
	got := k.take(t, 1)
	k.quiet(t, 1500*time.Millisecond)
	cons, err := js.Consumer(context.Background(), "LOG", "writer")
	if err != nil {
		t.Fatal(err)
	}
	info, err := cons.Info(context.Background())
	if err != nil || info.NumRedelivered == 0 && info.Delivered.Consumer < 2 {
		t.Fatalf("JetStream did not deliver the message again: %+v %v", info, err)
	}
	ack(t, c, got...)
	k.quiet(t, 1500*time.Millisecond)
	if p, _ := pendingAcks(t, js); p != 0 {
		t.Fatalf("%d pending after the ack", p)
	}
}

func TestASecondConsumerIsRefused(t *testing.T) {
	js, _, _ := server(t, "")
	c, _ := mount(t, natsOpts(js))
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
	js, _, _ := server(t, "")
	c, _ := mount(t, natsOpts(js))
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
	big := &transportv1alpha1.Message{Id: "u", Payload: make([]byte, nats.MessageMax)}
	_, err := c.Emit(context.Background(), connect.NewRequest(&transportv1alpha1.EmitRequest{Message: big}))
	wantRefusal(t, err, connect.CodeInvalidArgument, "bound", "message")
	_, err = c.Ack(context.Background(), connect.NewRequest(&transportv1alpha1.AckRequest{DeliveryIds: make([]uint64, nats.AckMax+1)}))
	wantRefusal(t, err, connect.CodeInvalidArgument, "bound", "delivery_ids")
	if _, err := c.Ack(context.Background(), connect.NewRequest(&transportv1alpha1.AckRequest{DeliveryIds: []uint64{7}})); err != nil {
		t.Fatalf("an unknown id: %v", err)
	}
}

func TestTheHostNamesTheEmitter(t *testing.T) {
	js, _, _ := server(t, "")
	opts := natsOpts(js)
	opts.Emitter = ""
	opts.Identify = func(ctx context.Context) (string, bool) {
		id := call.CorrelationID(ctx)
		return "actor@" + id, strings.HasPrefix(id, "corr-")
	}
	c, _ := mount(t, opts)
	if _, err := c.Emit(call.WithCorrelationID(context.Background(), "corr-0001"), connect.NewRequest(&transportv1alpha1.EmitRequest{Message: msg("a")})); err != nil {
		t.Fatal(err)
	}
	_, err := c.Emit(call.WithCorrelationID(context.Background(), "other-0001"), connect.NewRequest(&transportv1alpha1.EmitRequest{Message: msg("b")}))
	wantRefusal(t, err, connect.CodePermissionDenied, "caller", "")
	k := receive(t, c)
	if d := k.take(t, 1)[0]; d.GetEmitter() != "actor@corr-0001" {
		t.Fatalf("emitter %q", d.GetEmitter())
	}
}

// A message on the subject that the driver did not write is terminated and not delivered; the next message comes.
func TestAMessageTheDriverDidNotWriteIsNotDelivered(t *testing.T) {
	js, _, _ := server(t, "")
	c, _ := mount(t, natsOpts(js))
	emit(t, c, msg("first")) // makes the stream
	k := receive(t, c)
	ack(t, c, k.take(t, 1)...)
	if _, err := js.Publish(context.Background(), "log.records", []byte("not a message")); err != nil {
		t.Fatal(err)
	}
	emit(t, c, msg("a"))
	if got := k.take(t, 1); got[0].GetMessage().GetId() != "a" {
		t.Fatalf("%v", uids(got))
	}
}

// Without Create, a stream that is not there is a failure of the backend, and the emit is refused with engine; so is a server that is gone.
func TestAFailureOfNATSIsEngine(t *testing.T) {
	js, _, halt := server(t, "")
	opts := natsOpts(js)
	opts.Create = false
	c, _ := mount(t, opts)
	_, err := c.Emit(context.Background(), connect.NewRequest(&transportv1alpha1.EmitRequest{Message: msg("a")}))
	wantRefusal(t, err, connect.CodeUnavailable, "engine", "")
	stream, err := c.Receive(context.Background(), connect.NewRequest(&transportv1alpha1.ReceiveRequest{}))
	if err == nil {
		for stream.Receive() {
			t.Fatal("a delivery came with no consumer")
		}
		err = stream.Err()
	}
	wantRefusal(t, err, connect.CodeUnavailable, "engine", "")

	c2, _ := mount(t, natsOpts(js))
	emit(t, c2, msg("b"))
	halt()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = c2.Emit(ctx, connect.NewRequest(&transportv1alpha1.EmitRequest{Message: msg("c")}))
	wantRefusal(t, err, connect.CodeUnavailable, "engine", "")
}

// The connection is the host's: a server that asks for a token takes a host that presents it, and the driver needs nothing else.
func TestTheHostsTokenIsAllTheDriverNeeds(t *testing.T) {
	js, url, _ := server(t, "a-token-the-broker-maps")
	c, _ := mount(t, natsOpts(js))
	emit(t, c, msg("a"))
	if _, err := natsgo.Connect(url, natsgo.MaxReconnects(0)); err == nil {
		t.Fatal("a connection without the token was taken")
	}
}

func TestTheOptionsAreChecked(t *testing.T) {
	js, _, _ := server(t, "")
	good := natsOpts(js)
	for name, mut := range map[string]func(o *nats.Options){
		"no JetStream":        func(o *nats.Options) { o.JetStream = nil },
		"no stream":           func(o *nats.Options) { o.Stream = "" },
		"no subject":          func(o *nats.Options) { o.Subject = "" },
		"no consumer":         func(o *nats.Options) { o.Consumer = "" },
		"no emitter":          func(o *nats.Options) { o.Emitter = "" },
		"an emitter too long": func(o *nats.Options) { o.Emitter = strings.Repeat("e", nats.EmitterMax+1) },
		"a negative bound":    func(o *nats.Options) { o.MessageMax = -1 },
	} {
		o := good
		mut(&o)
		if _, err := nats.New(o); !errors.Is(err, nats.ErrOptions) {
			t.Errorf("%s: %v, want ErrOptions", name, err)
		}
	}
}
