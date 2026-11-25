// Package sqsfake is a fake of one Amazon SQS FIFO queue for the tests of the driver, in process: it implements the driver's API (the three
// calls it makes) with the rules of a FIFO queue that the driver relies on, so that its logic is tested on every platform, with no container.
// The SDK's serialisation and the real queue's answers are tested against ElasticMQ (tools/scripts/conformance-transport.sh).
//
// The rules it keeps: the order of the sends within a group; no message of a group is handed out while one of it is in flight; a message
// taken is invisible for the visibility timeout and then comes again with a new receipt handle, and only the latest handle deletes it; a
// send whose deduplication id was seen in the last five minutes is accepted and dropped; a receive made again with the same
// ReceiveRequestAttemptId, before its messages were deleted or came visible, returns them again. Each call can be told to fail.
package sqsfake

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/smithy-go"
)

// QueueURL is the URL of the fake's queue (the example account of the AWS documentation).
const QueueURL = "https://sqs.us-east-1.amazonaws.com/111122223333/example.fifo"

type message struct {
	id        string
	group     string
	body      string
	attrs     map[string]types.MessageAttributeValue
	handle    string
	invisible time.Time // in flight until then
	receives  int
}

// Fake is one FIFO queue. It is safe for concurrent use.
type Fake struct {
	mu         sync.Mutex
	now        func() time.Time
	visibility time.Duration
	msgs       []*message
	dedup      map[string]time.Time
	attempts   map[string][]*message
	seq        int
	fail       map[string]error
	calls      map[string]int
	changed    chan struct{}
}

// New returns an empty queue with a visibility timeout of 30 seconds.
func New() *Fake {
	return &Fake{now: time.Now, visibility: 30 * time.Second, dedup: map[string]time.Time{}, attempts: map[string][]*message{},
		fail: map[string]error{}, calls: map[string]int{}, changed: make(chan struct{})}
}

// SetVisibility sets the visibility timeout of the queue.
func (f *Fake) SetVisibility(d time.Duration) { f.mu.Lock(); f.visibility = d; f.mu.Unlock() }

// Fail makes every call of op (SendMessage, ReceiveMessage, DeleteMessageBatch) fail with an AWS error of the code; "" stops it.
func (f *Fake) Fail(op, code string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if code == "" {
		delete(f.fail, op)
		return
	}
	f.fail[op] = &smithy.GenericAPIError{Code: code, Message: "the fake was told to fail " + op}
}

// Calls returns how many times op was called.
func (f *Fake) Calls(op string) int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls[op] }

// Len returns the number of messages in the queue, in flight or not.
func (f *Fake) Len() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.msgs) }

// Receives returns how many times each message in the queue was handed out, in order.
func (f *Fake) Receives() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int, 0, len(f.msgs))
	for _, m := range f.msgs {
		out = append(out, m.receives)
	}
	return out
}

// Put puts a message with the body as it is, as another writer of the queue would.
func (f *Fake) Put(group, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	f.msgs = append(f.msgs, &message{id: "m" + strconv.Itoa(f.seq), group: group, body: body})
	f.wake()
}

// wake tells every waiting receive that something changed; the lock is held.
func (f *Fake) wake() {
	close(f.changed)
	f.changed = make(chan struct{})
}

func (f *Fake) enter(op string) error {
	f.calls[op]++
	return f.fail[op]
}

// SendMessage puts one message at the end of the queue.
func (f *Fake) SendMessage(_ context.Context, in *awssqs.SendMessageInput, _ ...func(*awssqs.Options)) (*awssqs.SendMessageOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("SendMessage"); err != nil {
		return nil, err
	}
	if aws.ToString(in.QueueUrl) != QueueURL || aws.ToString(in.MessageGroupId) == "" || aws.ToString(in.MessageDeduplicationId) == "" {
		return nil, &smithy.GenericAPIError{Code: "InvalidParameterValue", Message: "a FIFO send needs the queue, a group and a deduplication id"}
	}
	d := aws.ToString(in.MessageDeduplicationId)
	if len(d) > 128 {
		return nil, &smithy.GenericAPIError{Code: "InvalidParameterValue", Message: "the deduplication id is over 128 characters"}
	}
	if at, ok := f.dedup[d]; ok && f.now().Sub(at) < 5*time.Minute {
		return &awssqs.SendMessageOutput{MessageId: aws.String("dup")}, nil
	}
	f.dedup[d] = f.now()
	f.seq++
	m := &message{id: "m" + strconv.Itoa(f.seq), group: aws.ToString(in.MessageGroupId), body: aws.ToString(in.MessageBody), attrs: in.MessageAttributes}
	f.msgs = append(f.msgs, m)
	f.wake()
	return &awssqs.SendMessageOutput{MessageId: aws.String(m.id), SequenceNumber: aws.String(strconv.Itoa(f.seq))}, nil
}

// visible takes up to n messages, the lock held: in order, skipping every group with a message in flight.
func (f *Fake) visible(n int, vis time.Duration) []*message {
	now := f.now()
	busy := map[string]bool{}
	for _, m := range f.msgs {
		if m.invisible.After(now) {
			busy[m.group] = true
		}
	}
	var out []*message
	for _, m := range f.msgs {
		if len(out) == n {
			break
		}
		if busy[m.group] {
			continue
		}
		m.invisible = now.Add(vis)
		m.receives++
		f.seq++
		m.handle = fmt.Sprintf("%s#%d", m.id, f.seq)
		out = append(out, m)
	}
	return out
}

// ReceiveMessage hands out up to MaxNumberOfMessages, waiting up to WaitTimeSeconds for one.
func (f *Fake) ReceiveMessage(ctx context.Context, in *awssqs.ReceiveMessageInput, _ ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error) {
	deadline := time.Now().Add(time.Duration(in.WaitTimeSeconds) * time.Second)
	for {
		f.mu.Lock()
		if err := f.enter("ReceiveMessage"); err != nil {
			f.mu.Unlock()
			return nil, err
		}
		vis := f.visibility
		if in.VisibilityTimeout > 0 {
			vis = time.Duration(in.VisibilityTimeout) * time.Second
		}
		attempt := aws.ToString(in.ReceiveRequestAttemptId)
		var got []*message
		if prev, ok := f.attempts[attempt]; ok && attempt != "" {
			got = prev
		} else {
			got = f.visible(int(max(in.MaxNumberOfMessages, 1)), vis)
		}
		if len(got) > 0 {
			if attempt != "" {
				f.attempts[attempt] = got
			}
			out := &awssqs.ReceiveMessageOutput{}
			for _, m := range got {
				out.Messages = append(out.Messages, types.Message{MessageId: aws.String(m.id), ReceiptHandle: aws.String(m.handle),
					Body: aws.String(m.body), MessageAttributes: m.attrs})
			}
			f.mu.Unlock()
			// The answer is lost when the caller has gone: the messages stay in flight, as on the real queue.
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return out, nil
		}
		ch := f.changed
		f.mu.Unlock()
		left := time.Until(deadline)
		if left <= 0 {
			return &awssqs.ReceiveMessageOutput{}, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ch:
		case <-time.After(min(left, 50*time.Millisecond)):
		}
		f.mu.Lock()
		f.calls["ReceiveMessage"]-- // one call, however long it waits
		f.mu.Unlock()
	}
}

// DeleteMessageBatch deletes each message by its latest receipt handle; an older handle fails as ReceiptHandleIsInvalid.
func (f *Fake) DeleteMessageBatch(_ context.Context, in *awssqs.DeleteMessageBatchInput, _ ...func(*awssqs.Options)) (*awssqs.DeleteMessageBatchOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("DeleteMessageBatch"); err != nil {
		return nil, err
	}
	if len(in.Entries) == 0 || len(in.Entries) > 10 {
		return nil, errors.New("sqsfake: a batch holds 1 to 10 entries")
	}
	out := &awssqs.DeleteMessageBatchOutput{}
	for _, e := range in.Entries {
		found := -1
		for i, m := range f.msgs {
			if m.handle == aws.ToString(e.ReceiptHandle) {
				found = i
				break
			}
		}
		if found < 0 {
			out.Failed = append(out.Failed, types.BatchResultErrorEntry{Id: e.Id, Code: aws.String("ReceiptHandleIsInvalid"), SenderFault: true})
			continue
		}
		f.msgs = append(f.msgs[:found], f.msgs[found+1:]...)
		out.Successful = append(out.Successful, types.DeleteMessageBatchResultEntry{Id: e.Id})
	}
	// An attempt id answers again only while its messages are all still there.
	for a, ms := range f.attempts {
		for _, m := range ms {
			gone := true
			for _, x := range f.msgs {
				if x == m {
					gone = false
				}
			}
			if gone {
				delete(f.attempts, a)
				break
			}
		}
	}
	f.wake()
	return out, nil
}
