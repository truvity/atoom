// Package sqs is the Amazon SQS driver of the transport port of atoom, atoom.transport.v1alpha1.TransportService, in Go: the transport
// contract over one SQS FIFO queue. The Go module is github.com/truvity/atoom/transport/sqs. A Go program, a Lambda function for one, links
// it and mounts its handler in its own process, so that a Lambda function emits with no sidecar; the thin main (cmd/sqs) serves both sides on
// a UNIX socket.
//
//   - Emit is one SendMessage to the FIFO queue, in the one message group the host names, so that the queue keeps the order of the emits.
//     The body is the message in its Protobuf encoding, in base64; the emitter, as the host names the caller, is a message attribute. The
//     deduplication id is the SHA-256 of the message's id, so that an emit made again with the same id (a retry) within SQS's five
//     minutes is one message. SQS answers once the message is stored, so a blocking emit and another are answered alike.
//   - Receive long-polls the queue and hands each message on as a delivery. The driver gives each message a delivery id when it first
//     takes it and holds it until it is acknowledged: a message that comes again (a stream that ended, a visibility timeout that passed)
//     comes under the same id, first on the next stream. One stream at a time.
//   - Ack deletes the messages by their latest receipt handles. An id the driver does not hold is ignored.
//
// SQS delivers at least once, so a message may come twice; it is the consumer that makes it one, by the message's id (its
// dedup, docs/sqs.md). A FIFO queue hands out no message of a group while an earlier one of it is in flight, so the driver delivers at most
// one receive (ten messages) ahead of the acknowledgements: a consumer acknowledges what it holds to receive more.
package sqs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"google.golang.org/protobuf/proto"

	"github.com/truvity/atoom/common/refusal"
	transportv1alpha1 "github.com/truvity/atoom/transport/gen/atoom/transport/v1alpha1"
	"github.com/truvity/atoom/transport/gen/atoom/transport/v1alpha1/transportv1alpha1connect"
)

const (
	// EmitterAttribute is the message attribute that carries the emitter.
	EmitterAttribute = "atoom-emitter"
	// EmitterMax is the most bytes of an emitter name, as for the spool.
	EmitterMax = 255
	// MessageMax is the default bound of the encoded message: its base64 fits in 256 KiB, the size every SQS queue takes.
	MessageMax = 192 * 1024
	// MessageMaxLimit is the largest bound a host may set: its base64 and the attribute fit in SQS's 1 MiB.
	MessageMaxLimit = 768 * 1024
	// AckMax is the most ids in one Ack.
	AckMax = 1000
	// BatchMax is the most messages SQS returns to one receive, and so the most deliveries in one batch.
	BatchMax = 10
	// HeldMax is the most messages the driver holds unacknowledged; over it a Receive stops taking more until an Ack.
	HeldMax = 1000
	// DefaultWaitTime is the long poll of a receive: SQS's longest.
	DefaultWaitTime = 20 * time.Second
	// ShortPollPause is the pause after an empty short poll.
	ShortPollPause = 250 * time.Millisecond
	// DefaultMessageGroupID is the message group of every emit unless the host names another.
	DefaultMessageGroupID = "transport"
)

// API is the part of the SQS client the driver calls; *sqs.Client is one.
type API interface {
	SendMessage(ctx context.Context, in *awssqs.SendMessageInput, opts ...func(*awssqs.Options)) (*awssqs.SendMessageOutput, error)
	ReceiveMessage(ctx context.Context, in *awssqs.ReceiveMessageInput, opts ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error)
	DeleteMessageBatch(ctx context.Context, in *awssqs.DeleteMessageBatchInput, opts ...func(*awssqs.Options)) (*awssqs.DeleteMessageBatchOutput, error)
}

// Options are what the driver needs.
type Options struct {
	// SQS is the SQS client. Required.
	SQS API
	// QueueURL is the URL of a FIFO queue (it ends in ".fifo"). Required.
	QueueURL string
	// Emitter is the emitter of every emit when Identify is nil: the host's name for itself. One of the two is required.
	Emitter string
	// Identify names the caller of an emit from the context of the call (a host that tells its callers apart); false refuses the emit
	// with the reason caller. It overrides Emitter.
	Identify func(ctx context.Context) (string, bool)
	// MessageGroupID is the message group of every emit; empty is [DefaultMessageGroupID]. The order is kept within one group.
	MessageGroupID string
	// MessageMax bounds the Protobuf encoding of a message; zero is [MessageMax], and more than [MessageMaxLimit] is refused.
	MessageMax int
	// WaitTime is the long poll of each receive, whole seconds up to 20; zero is [DefaultWaitTime].
	WaitTime time.Duration
	// ShortPoll makes every receive answer at once (no long poll), with a pause of [ShortPollPause] after an empty one. A long poll that a
	// stream's end cuts off may go on in SQS and take messages for nobody: they come to the next receive (the same
	// ReceiveRequestAttemptId), but alone, ahead of later ones that the window then holds back. A short poll leaves nothing behind, at
	// the price of more calls while the queue is empty. WaitTime is then ignored.
	ShortPoll bool
	// VisibilityTimeout is the visibility timeout of the messages a receive takes, whole seconds; zero is the queue's own.
	VisibilityTimeout time.Duration
	// Now is the clock of the first delivery id; nil is time.Now.
	Now func() time.Time
	// Logger receives every line the driver writes. nil means slog.Default(), read at each line; the host decides where the lines go,
	// or that they go nowhere.
	Logger *slog.Logger
}

// Host is what a program that runs the driver provides.
type Host struct {
	// Serve serves h at path on the host's server until ctx ends. For a socket it is a closure around socket.Serve.
	Serve func(ctx context.Context, path string, h http.Handler) error
	// HandlerOptions are the Connect options of the handler: the read limit, the interceptors of the host.
	HandlerOptions []connect.HandlerOption
}

// ErrOptions is an [Options] that cannot make a driver.
var ErrOptions = errors.New("sqs: the options are incomplete or out of range")

// held is a message the driver took and holds until it is acknowledged.
type held struct {
	id        uint64
	messageID string
	handle    string // the latest receipt handle
	delivery  *transportv1alpha1.Delivery
	sent      uint64 // the stream it was last sent on
}

// Service is the transport over one SQS FIFO queue: the TransportService.
type Service struct {
	transportv1alpha1connect.UnimplementedTransportServiceHandler
	api        API
	queue      string
	identify   func(ctx context.Context) (string, bool)
	group      string
	max        int
	wait       int32
	short      bool
	visibility int32
	logger     *slog.Logger

	consuming atomic.Bool
	// attempt is the ReceiveRequestAttemptId of the next receive. It is kept until a receive returns, so that a receive cut off by the end
	// of a stream is made again with the same id, and SQS hands the next one the messages it had taken for it (FIFO queues).
	attempt string

	mu      sync.Mutex
	next    uint64           // the next delivery id
	stream  uint64           // the number of the open stream (or the last)
	byID    map[uint64]*held // delivery id -> held
	byMsgID map[string]*held // SQS message id -> held
}

var _ transportv1alpha1connect.TransportServiceHandler = (*Service)(nil)

// New makes the service of the queue in opts. It makes no call: the queue is first reached by an emit or a receive.
func New(opts Options) (*Service, error) {
	switch {
	case opts.SQS == nil, opts.QueueURL == "":
		return nil, errors.Join(ErrOptions, errors.New("sqs: Options.SQS and Options.QueueURL are required"))
	case !strings.HasSuffix(opts.QueueURL, ".fifo"):
		return nil, errors.Join(ErrOptions, errors.New("sqs: the queue is not a FIFO queue (its URL does not end in .fifo): a standard queue does not keep the order"))
	case opts.Identify == nil && (opts.Emitter == "" || len(opts.Emitter) > EmitterMax):
		return nil, errors.Join(ErrOptions, errors.New("sqs: Options.Emitter (1 to 255 bytes) or Options.Identify is required"))
	case opts.MessageMax < 0 || opts.MessageMax > MessageMaxLimit:
		return nil, errors.Join(ErrOptions, errors.New("sqs: Options.MessageMax is over the limit"))
	case opts.WaitTime < 0 || opts.WaitTime > DefaultWaitTime || opts.WaitTime%time.Second != 0:
		return nil, errors.Join(ErrOptions, errors.New("sqs: Options.WaitTime is whole seconds up to 20"))
	case opts.VisibilityTimeout < 0 || opts.VisibilityTimeout > 12*time.Hour || opts.VisibilityTimeout%time.Second != 0:
		return nil, errors.Join(ErrOptions, errors.New("sqs: Options.VisibilityTimeout is whole seconds up to 12 hours"))
	}
	identify := opts.Identify
	if identify == nil {
		name := opts.Emitter
		identify = func(context.Context) (string, bool) { return name, true }
	}
	group := opts.MessageGroupID
	if group == "" {
		group = DefaultMessageGroupID
	}
	maxMsg := opts.MessageMax
	if maxMsg == 0 {
		maxMsg = MessageMax
	}
	wait := opts.WaitTime
	if wait == 0 {
		wait = DefaultWaitTime
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	// The ids start at the time of the start, in nanoseconds: they increase across restarts of the driver as the clock does, and within one
	// run they increase by one.
	first := uint64(max(now().UnixNano(), 1))
	return &Service{
		api: opts.SQS, queue: opts.QueueURL, identify: identify, group: group, max: maxMsg,
		short: opts.ShortPoll, wait: int32(wait / time.Second), visibility: int32(opts.VisibilityTimeout / time.Second),
		logger: opts.Logger, next: first, byID: map[uint64]*held{}, byMsgID: map[string]*held{},
	}, nil
}

// Handler returns the Connect handler of the transport service, to mount on a server or an in-memory transport.
func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	path, h := transportv1alpha1connect.NewTransportServiceHandler(s, opts...)
	// Receive sends the headers of its response before it has a delivery, so that the call of a consumer returns and the stream is open
	// while the queue is empty. Connect sends the headers with the first message, so the handler is given the response writer to flush with.
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
		return errors.New("sqs: the host has no Serve")
	}
	svc, err := New(opts)
	if err != nil {
		return err
	}
	path, h := svc.Handler(host.HandlerOptions...)
	return host.Serve(ctx, path, h)
}

// log is the logger of the driver: the host's Options.Logger, or slog.Default() read now when the host gave none.
func (s *Service) log() *slog.Logger {
	if s.logger != nil {
		return s.logger
	}
	return slog.Default()
}

// engine is the refusal of a failure of SQS: the reason only, and SQS's text in the operator's log.
func (s *Service) engine(what string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	s.log().Error("SQS failed", "call", what, "err", err)
	return refusal.New(connect.CodeUnavailable, "engine", "")
}

// DeduplicationID is the deduplication id of a message: the SHA-256 of its id, in hex.
func DeduplicationID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

// Emit sends one message to the queue.
func (s *Service) Emit(ctx context.Context, req *connect.Request[transportv1alpha1.EmitRequest]) (*connect.Response[transportv1alpha1.EmitResponse], error) {
	m := req.Msg.GetMessage()
	switch {
	case m == nil:
		return nil, refusal.New(connect.CodeInvalidArgument, "schema", "message")
	case m.GetId() == "":
		return nil, refusal.New(connect.CodeInvalidArgument, "schema", "message.id")
	case len(m.GetPayload()) == 0:
		return nil, refusal.New(connect.CodeInvalidArgument, "schema", "message.payload")
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
	_, err = s.api.SendMessage(ctx, &awssqs.SendMessageInput{
		QueueUrl:               aws.String(s.queue),
		MessageBody:            aws.String(base64.StdEncoding.EncodeToString(raw)),
		MessageGroupId:         aws.String(s.group),
		MessageDeduplicationId: aws.String(DeduplicationID(m.GetId())),
		MessageAttributes: map[string]types.MessageAttributeValue{
			EmitterAttribute: {DataType: aws.String("String"), StringValue: aws.String(emitter)},
		},
	})
	if err != nil {
		return nil, s.engine("SendMessage", err)
	}
	return connect.NewResponse(&transportv1alpha1.EmitResponse{}), nil
}

// decode turns a message of the queue into a delivery, or false for one the driver did not write.
func decode(m types.Message) (*transportv1alpha1.Delivery, bool) {
	raw, err := base64.StdEncoding.DecodeString(aws.ToString(m.Body))
	if err != nil {
		return nil, false
	}
	msg := &transportv1alpha1.Message{}
	if err := proto.Unmarshal(raw, msg); err != nil || msg.GetId() == "" || len(msg.GetPayload()) == 0 {
		return nil, false
	}
	emitter := aws.ToString(m.MessageAttributes[EmitterAttribute].StringValue)
	if emitter == "" {
		return nil, false
	}
	return &transportv1alpha1.Delivery{Emitter: emitter, Message: msg}, true
}

// Receive streams the deliveries: first the messages the driver holds from an earlier stream, in the order of their ids, then what each
// long poll of the queue brings. A second stream while one is open is refused with already_exists.
func (s *Service) Receive(ctx context.Context, req *connect.Request[transportv1alpha1.ReceiveRequest], stream *connect.ServerStream[transportv1alpha1.ReceiveResponse]) error {
	if !s.consuming.CompareAndSwap(false, true) {
		return refusal.New(connect.CodeAlreadyExists, "taken", "")
	}
	defer s.consuming.Store(false)
	batch := int(req.Msg.GetMaxBatch())
	if batch <= 0 || batch > BatchMax {
		batch = BatchMax
	}
	s.mu.Lock()
	s.stream++
	me := s.stream
	s.mu.Unlock()
	openStream(ctx)

	// What the driver holds from before: sent again first, under the same ids.
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
			// The consumer holds as many as the driver keeps: it acknowledges before more is taken.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}
		in := &awssqs.ReceiveMessageInput{
			QueueUrl:              aws.String(s.queue),
			MaxNumberOfMessages:   int32(batch),
			WaitTimeSeconds:       s.waitSeconds(),
			MessageAttributeNames: []string{EmitterAttribute},
		}
		if s.visibility > 0 {
			in.VisibilityTimeout = s.visibility
		}
		if s.attempt == "" {
			s.attempt = newAttempt()
		}
		in.ReceiveRequestAttemptId = aws.String(s.attempt)
		out, err := s.api.ReceiveMessage(ctx, in)
		if err != nil {
			return s.engine("ReceiveMessage", err)
		}
		s.attempt = ""
		if err := s.send(stream, s.take(me, out.Messages), batch); err != nil {
			return err
		}
		if s.short && len(out.Messages) == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(ShortPollPause):
			}
		}
	}
}

func (s *Service) waitSeconds() int32 {
	if s.short {
		return 0
	}
	return s.wait
}

// newAttempt is a ReceiveRequestAttemptId: 32 hex characters.
func newAttempt() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
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

// take holds the messages of one receive and returns the deliveries to send on the stream me: a message the driver holds already gets its
// new receipt handle and is sent only if this stream has not had it; a new one gets the next id. A message the driver did not write is left
// to SQS (it comes again until the queue's redrive policy moves it) and is logged.
func (s *Service) take(me uint64, msgs []types.Message) []*transportv1alpha1.Delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*transportv1alpha1.Delivery
	for _, m := range msgs {
		msgID := aws.ToString(m.MessageId)
		if h, ok := s.byMsgID[msgID]; ok {
			h.handle = aws.ToString(m.ReceiptHandle)
			if h.sent != me {
				h.sent = me
				out = append(out, h.delivery)
			}
			continue
		}
		d, ok := decode(m)
		if !ok {
			s.log().Error("SQS: a message the driver did not write is left in the queue", "message_id", msgID)
			continue
		}
		d.DeliveryId = s.next
		s.next++
		h := &held{id: d.GetDeliveryId(), messageID: msgID, handle: aws.ToString(m.ReceiptHandle), delivery: d, sent: me}
		s.byID[h.id] = h
		s.byMsgID[msgID] = h
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

// Ack deletes the acknowledged messages from the queue. An id the driver does not hold is ignored; a receipt handle that SQS no longer takes
// is ignored too (the message comes again, under a new id, and the consumer's dedup by id makes it one).
func (s *Service) Ack(ctx context.Context, req *connect.Request[transportv1alpha1.AckRequest]) (*connect.Response[transportv1alpha1.AckResponse], error) {
	ids := req.Msg.GetDeliveryIds()
	if len(ids) > AckMax {
		return nil, refusal.New(connect.CodeInvalidArgument, "bound", "delivery_ids")
	}
	s.mu.Lock()
	var todo []*held
	seen := map[uint64]bool{}
	for _, id := range ids {
		if h, ok := s.byID[id]; ok && !seen[id] {
			seen[id] = true
			todo = append(todo, h)
		}
	}
	s.mu.Unlock()
	for len(todo) > 0 {
		n := min(BatchMax, len(todo))
		part := todo[:n]
		todo = todo[n:]
		entries := make([]types.DeleteMessageBatchRequestEntry, 0, n)
		s.mu.Lock()
		for i, h := range part {
			entries = append(entries, types.DeleteMessageBatchRequestEntry{Id: aws.String(strconv.Itoa(i)), ReceiptHandle: aws.String(h.handle)})
		}
		s.mu.Unlock()
		out, err := s.api.DeleteMessageBatch(ctx, &awssqs.DeleteMessageBatchInput{QueueUrl: aws.String(s.queue), Entries: entries})
		if err != nil {
			return nil, s.engine("DeleteMessageBatch", err)
		}
		failed := map[string]string{}
		for _, f := range out.Failed {
			failed[aws.ToString(f.Id)] = aws.ToString(f.Code)
		}
		s.mu.Lock()
		var bad error
		for i, h := range part {
			code, fail := failed[strconv.Itoa(i)]
			if fail && code != "ReceiptHandleIsInvalid" {
				bad = errors.New(code)
				continue
			}
			delete(s.byID, h.id)
			delete(s.byMsgID, h.messageID)
		}
		s.mu.Unlock()
		if bad != nil {
			return nil, s.engine("DeleteMessageBatch", bad)
		}
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
