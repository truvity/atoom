// Package nats is the NATS JetStream driver of the transport port of atoom, atoom.transport.v1alpha1.TransportService, in Go: the transport
// contract over one JetStream stream and one durable consumer. The Go module is github.com/truvity/atoom/transport/nats. A Go program links
// it and mounts its handler in its own process, so that its audit records reach the writer with no sidecar; the thin main (cmd/nats) serves
// both sides on a UNIX socket.
//
//   - Emit publishes the message, in its Protobuf encoding, to the subject the host names, with the header Nats-Msg-Id set to the message's
//     uid, so that JetStream drops a publish made again with the same uid within the stream's duplicate window (a retry). The emitter, as
//     the host names the caller, is the header Atoom-Emitter. The answer waits for JetStream's acknowledgement, so the message is stored
//     (in the stream's storage) whether the emit is blocking or not.
//   - Receive pulls from the durable consumer the host names (explicit acknowledgement) and hands each message on as a delivery whose id is
//     its stream sequence: the same for a message that comes again, after a restart of the driver too, and increasing with the order of
//     the publishes. The driver holds every message it delivered until it is acknowledged; what it holds comes first on the next stream.
//     One stream at a time.
//   - Ack acknowledges the messages to JetStream and waits for its answer. An id the driver does not hold is ignored.
//
// JetStream delivers at least once: a message that is not acknowledged within the consumer's ack wait is delivered again. It is the
// consumer that makes one record of it, by the message's uid (the writer's dedup, docs/nats.md).
//
// The connection is the host's: it dials NATS with the credentials, the token or the certificate its deployment gives it (the thin main
// takes them as flags) and hands the driver a JetStream context. The driver knows nothing of how they were made.
package nats

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"

	"github.com/truvity/atoom/common/refusal"
	transportv1alpha1 "github.com/truvity/atoom/transport/gen/atoom/transport/v1alpha1"
	"github.com/truvity/atoom/transport/gen/atoom/transport/v1alpha1/transportv1alpha1connect"
)

const (
	// EmitterHeader is the header that carries the emitter.
	EmitterHeader = "Atoom-Emitter"
	// EmitterMax is the most bytes of an emitter name, as for the spool.
	EmitterMax = 255
	// MessageMax is the default bound of the encoded message, below NATS's default payload limit of 1 MB.
	MessageMax = 768 * 1024
	// AckMax is the most ids in one Ack.
	AckMax = 1000
	// BatchMax is the most messages one pull asks for, and so the most deliveries in one batch.
	BatchMax = 100
	// HeldMax is the most messages the driver holds unacknowledged; over it a Receive pulls no more until an Ack.
	HeldMax = 10000
	// DefaultWaitTime is how long one pull waits for a message.
	DefaultWaitTime = 5 * time.Second
	// DefaultAckWait is the ack wait of a consumer the driver makes.
	DefaultAckWait = 30 * time.Second
	// DefaultDuplicates is the duplicate window of a stream the driver makes.
	DefaultDuplicates = 2 * time.Minute
)

// Options are what the driver needs.
type Options struct {
	// JetStream is the JetStream context over the host's connection. Required.
	JetStream jetstream.JetStream
	// Stream is the name of the stream of the log. Required.
	Stream string
	// Subject is the subject an emit publishes to; the stream must capture it. Required.
	Subject string
	// Consumer is the name of the durable consumer that Receive pulls from. Required.
	Consumer string
	// Create makes the stream (file storage, the one subject, a duplicate window of [DefaultDuplicates]) and the durable consumer (explicit
	// acknowledgement, from the first message, an ack wait of AckWait) when they are not there. Off, a missing stream or consumer is a
	// failure of the backend: a deployment that makes them itself gives the driver no right to.
	Create bool
	// AckWait is the ack wait of a consumer the driver makes; zero is [DefaultAckWait].
	AckWait time.Duration
	// Emitter is the emitter of every emit when Identify is nil: the host's name for itself. One of the two is required.
	Emitter string
	// Identify names the caller of an emit from the context of the call; false refuses the emit with the reason caller. It overrides
	// Emitter.
	Identify func(ctx context.Context) (string, bool)
	// MessageMax bounds the Protobuf encoding of a message; zero is [MessageMax].
	MessageMax int
	// WaitTime is how long one pull waits for a message; zero is [DefaultWaitTime].
	WaitTime time.Duration
}

// Host is what a program that runs the driver provides.
type Host struct {
	// Serve serves h at path on the host's server until ctx ends. For a socket it is a closure around socket.Serve.
	Serve func(ctx context.Context, path string, h http.Handler) error
	// HandlerOptions are the Connect options of the handler: the read limit, the interceptors of the host.
	HandlerOptions []connect.HandlerOption
}

// ErrOptions is an [Options] that cannot make a driver.
var ErrOptions = errors.New("nats: the options are incomplete or out of range")

// held is a message the driver delivered and holds until it is acknowledged.
type held struct {
	msg      jetstream.Msg // the latest delivery of it, whose reply subject acknowledges it
	delivery *transportv1alpha1.Delivery
	sent     uint64 // the stream it was last sent on
}

// Service is the transport over one JetStream stream and durable consumer: the TransportService.
type Service struct {
	transportv1alpha1connect.UnimplementedTransportServiceHandler
	js       jetstream.JetStream
	stream   string
	subject  string
	consumer string
	create   bool
	ackWait  time.Duration
	identify func(ctx context.Context) (string, bool)
	max      int
	wait     time.Duration

	consuming atomic.Bool
	ready     atomic.Bool // the stream was made, or found

	mu      sync.Mutex
	streams uint64           // the number of the open stream (or the last)
	byID    map[uint64]*held // stream sequence -> held
}

var _ transportv1alpha1connect.TransportServiceHandler = (*Service)(nil)

// New makes the service of the stream in opts. It makes no call: the stream is first reached by an emit or a receive.
func New(opts Options) (*Service, error) {
	switch {
	case opts.JetStream == nil, opts.Stream == "", opts.Subject == "", opts.Consumer == "":
		return nil, errors.Join(ErrOptions, errors.New("nats: Options.JetStream, Stream, Subject and Consumer are required"))
	case opts.Identify == nil && (opts.Emitter == "" || len(opts.Emitter) > EmitterMax):
		return nil, errors.Join(ErrOptions, errors.New("nats: Options.Emitter (1 to 255 bytes) or Options.Identify is required"))
	case opts.MessageMax < 0, opts.WaitTime < 0, opts.AckWait < 0:
		return nil, errors.Join(ErrOptions, errors.New("nats: a bound or a time is negative"))
	}
	identify := opts.Identify
	if identify == nil {
		name := opts.Emitter
		identify = func(context.Context) (string, bool) { return name, true }
	}
	maxMsg := opts.MessageMax
	if maxMsg == 0 {
		maxMsg = MessageMax
	}
	wait := opts.WaitTime
	if wait == 0 {
		wait = DefaultWaitTime
	}
	ackWait := opts.AckWait
	if ackWait == 0 {
		ackWait = DefaultAckWait
	}
	return &Service{js: opts.JetStream, stream: opts.Stream, subject: opts.Subject, consumer: opts.Consumer, create: opts.Create,
		ackWait: ackWait, identify: identify, max: maxMsg, wait: wait, byID: map[uint64]*held{}}, nil
}

// Handler returns the Connect handler of the transport service, to mount on a server or an in-memory transport.
func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	path, h := transportv1alpha1connect.NewTransportServiceHandler(s, opts...)
	// Receive sends the headers of its response before it has a delivery, so that the call of a consumer returns and the stream is open
	// while the stream is empty. Connect sends the headers with the first message, so the handler is given the response writer to flush with.
	return path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), writerKey{}, w)))
	})
}

type writerKey struct{}

func openStream(ctx context.Context) {
	if w, ok := ctx.Value(writerKey{}).(http.ResponseWriter); ok {
		_ = http.NewResponseController(w).Flush()
	}
}

// Run serves the transport port on the host's server until ctx ends.
func Run(ctx context.Context, opts Options, host Host) error {
	if host.Serve == nil {
		return errors.New("nats: the host has no Serve")
	}
	svc, err := New(opts)
	if err != nil {
		return err
	}
	path, h := svc.Handler(host.HandlerOptions...)
	return host.Serve(ctx, path, h)
}

// engine is the refusal of a failure of NATS: the reason only, and NATS's text in the operator's log.
func engine(what string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	slog.Error("NATS failed", "call", what, "err", err)
	return refusal.New(connect.CodeUnavailable, "engine", "")
}

// prepareStream makes the stream when the host asked for it and it is not there, once.
func (s *Service) prepareStream(ctx context.Context) error {
	if !s.create || s.ready.Load() {
		return nil
	}
	_, err := s.js.Stream(ctx, s.stream)
	if errors.Is(err, jetstream.ErrStreamNotFound) {
		_, err = s.js.CreateStream(ctx, jetstream.StreamConfig{Name: s.stream, Subjects: []string{s.subject}, Storage: jetstream.FileStorage,
			Duplicates: DefaultDuplicates})
		if errors.Is(err, jetstream.ErrStreamNameAlreadyInUse) {
			err = nil
		}
	}
	if err == nil {
		s.ready.Store(true)
	}
	return err
}

// pullConsumer finds the durable consumer, making it (and the stream) when the host asked for it.
func (s *Service) pullConsumer(ctx context.Context) (jetstream.Consumer, error) {
	c, err := s.js.Consumer(ctx, s.stream, s.consumer)
	if err == nil || !s.create || (!errors.Is(err, jetstream.ErrConsumerNotFound) && !errors.Is(err, jetstream.ErrStreamNotFound)) {
		return c, err
	}
	if err := s.prepareStream(ctx); err != nil {
		return nil, err
	}
	c, err = s.js.CreateOrUpdateConsumer(ctx, s.stream, jetstream.ConsumerConfig{Durable: s.consumer, AckPolicy: jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverAllPolicy, AckWait: s.ackWait})
	return c, err
}

// Emit publishes one message and waits for JetStream to store it.
func (s *Service) Emit(ctx context.Context, req *connect.Request[transportv1alpha1.EmitRequest]) (*connect.Response[transportv1alpha1.EmitResponse], error) {
	m := req.Msg.GetMessage()
	switch {
	case m == nil:
		return nil, refusal.New(connect.CodeInvalidArgument, "schema", "message")
	case m.GetUid() == "":
		return nil, refusal.New(connect.CodeInvalidArgument, "schema", "message.uid")
	case len(m.GetRecord()) == 0:
		return nil, refusal.New(connect.CodeInvalidArgument, "schema", "message.record")
	}
	emitter, ok := s.identify(ctx)
	if !ok || emitter == "" {
		return nil, refusal.New(connect.CodePermissionDenied, "caller", "")
	}
	if len(emitter) > EmitterMax {
		return nil, refusal.New(connect.CodeInvalidArgument, "bound", "emitter")
	}
	raw, err := proto.Marshal(m)
	if err != nil {
		return nil, refusal.New(connect.CodeInvalidArgument, "schema", "message")
	}
	if len(raw) > s.max {
		return nil, refusal.New(connect.CodeInvalidArgument, "bound", "message")
	}
	if err := s.prepareStream(ctx); err != nil {
		return nil, engine("CreateStream", err)
	}
	msg := natsgo.NewMsg(s.subject)
	msg.Data = raw
	msg.Header.Set(EmitterHeader, emitter)
	if _, err := s.js.PublishMsg(ctx, msg, jetstream.WithMsgID(m.GetUid()), jetstream.WithExpectStream(s.stream)); err != nil {
		return nil, engine("Publish", err)
	}
	return connect.NewResponse(&transportv1alpha1.EmitResponse{}), nil
}

// decode turns a message of the stream into a delivery, or false for one the driver did not write.
func decode(m jetstream.Msg) (*transportv1alpha1.Delivery, uint64, bool) {
	meta, err := m.Metadata()
	if err != nil {
		return nil, 0, false
	}
	msg := &transportv1alpha1.AuditMessage{}
	if err := proto.Unmarshal(m.Data(), msg); err != nil || msg.GetUid() == "" || len(msg.GetRecord()) == 0 {
		return nil, meta.Sequence.Stream, false
	}
	emitter := m.Headers().Get(EmitterHeader)
	if emitter == "" {
		return nil, meta.Sequence.Stream, false
	}
	return &transportv1alpha1.Delivery{DeliveryId: meta.Sequence.Stream, Emitter: emitter, Message: msg}, meta.Sequence.Stream, true
}

// Receive streams the deliveries: first the messages the driver holds from an earlier stream, in the order of their ids, then what each
// pull brings. A second stream while one is open is refused with already_exists.
func (s *Service) Receive(ctx context.Context, req *connect.Request[transportv1alpha1.ReceiveRequest], stream *connect.ServerStream[transportv1alpha1.ReceiveResponse]) error {
	if !s.consuming.CompareAndSwap(false, true) {
		return refusal.New(connect.CodeAlreadyExists, "taken", "")
	}
	defer s.consuming.Store(false)
	batch := int(req.Msg.GetMaxBatch())
	if batch <= 0 || batch > BatchMax {
		batch = BatchMax
	}
	cons, err := s.pullConsumer(ctx)
	if err != nil {
		return engine("Consumer", err)
	}
	s.mu.Lock()
	s.streams++
	me := s.streams
	s.mu.Unlock()
	openStream(ctx)

	if err := s.send(stream, s.pending(me), batch); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.mu.Lock()
		full := len(s.byID) >= HeldMax
		s.mu.Unlock()
		if full {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}
		// The pull is not bound to the stream: what it brings is held even when the stream ends meanwhile, and goes on the next one.
		res, err := cons.Fetch(batch, jetstream.FetchMaxWait(s.wait))
		if err != nil {
			return engine("Fetch", err)
		}
		var msgs []jetstream.Msg
		for m := range res.Messages() {
			msgs = append(msgs, m)
		}
		ds := s.take(me, msgs)
		if err := res.Error(); err != nil && !errors.Is(err, natsgo.ErrTimeout) {
			_ = s.send(stream, ds, batch)
			return engine("Fetch", err)
		}
		if err := s.send(stream, ds, batch); err != nil {
			return err
		}
	}
}

// pending marks every message held as sent on the stream me and returns their deliveries, in the order of their ids.
func (s *Service) pending(me uint64) []*transportv1alpha1.Delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]uint64, 0, len(s.byID))
	for id := range s.byID {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out := make([]*transportv1alpha1.Delivery, 0, len(ids))
	for _, id := range ids {
		h := s.byID[id]
		h.sent = me
		out = append(out, h.delivery)
	}
	return out
}

// take holds the messages of one pull and returns the deliveries to send on the stream me: a message the driver holds already (JetStream
// delivered it again after the ack wait) takes the place of the held one and is sent only if this stream has not had it. A message the
// driver did not write is terminated, so that JetStream does not deliver it again, and logged.
func (s *Service) take(me uint64, msgs []jetstream.Msg) []*transportv1alpha1.Delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*transportv1alpha1.Delivery
	for _, m := range msgs {
		d, seq, ok := decode(m)
		if h, there := s.byID[seq]; there && seq != 0 {
			h.msg = m
			if h.sent != me {
				h.sent = me
				out = append(out, h.delivery)
			}
			continue
		}
		if !ok {
			slog.Error("NATS: a message the driver did not write is terminated", "stream_sequence", seq)
			_ = m.Term()
			continue
		}
		s.byID[seq] = &held{msg: m, delivery: d, sent: me}
		out = append(out, d)
	}
	return out
}

func (s *Service) send(stream *connect.ServerStream[transportv1alpha1.ReceiveResponse], ds []*transportv1alpha1.Delivery, batch int) error {
	for len(ds) > 0 {
		n := min(batch, len(ds))
		if err := stream.Send(&transportv1alpha1.ReceiveResponse{Deliveries: ds[:n]}); err != nil {
			return err
		}
		ds = ds[n:]
	}
	return nil
}

// Ack acknowledges the messages to JetStream and waits for its answer. An id the driver does not hold is ignored.
func (s *Service) Ack(ctx context.Context, req *connect.Request[transportv1alpha1.AckRequest]) (*connect.Response[transportv1alpha1.AckResponse], error) {
	ids := req.Msg.GetDeliveryIds()
	if len(ids) > AckMax {
		return nil, refusal.New(connect.CodeInvalidArgument, "bound", "delivery_ids")
	}
	for _, id := range ids {
		s.mu.Lock()
		h, ok := s.byID[id]
		var m jetstream.Msg
		if ok {
			m = h.msg
		}
		s.mu.Unlock()
		if !ok {
			continue
		}
		if err := m.DoubleAck(ctx); err != nil && !errors.Is(err, jetstream.ErrMsgAlreadyAckd) {
			return nil, engine("Ack", err)
		}
		s.mu.Lock()
		delete(s.byID, id)
		s.mu.Unlock()
	}
	return connect.NewResponse(&transportv1alpha1.AckResponse{}), nil
}

// Held returns the delivery ids the driver holds unacknowledged, in order: for the host's metrics and for tests.
func (s *Service) Held() []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]uint64, 0, len(s.byID))
	for id := range s.byID {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}
