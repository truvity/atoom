package sqs_test

// The tests of this file run the driver through the real AWS SDK client against ElasticMQ, a queue server that speaks SQS's protocol, in a
// container (tools/scripts/conformance-transport.sh starts one and sets ATOOM_SQS_ENDPOINT). Without the variable they skip and say so;
// ATOOM_REQUIRE_ELASTICMQ=1 turns the skip into a failure (CI sets it on Linux). A skip is not a pass: the conformance kit of the transport
// port runs against the thin main over the same container, and these tests hold the driver to what only SQS does (receipt handles that a
// new receive replaces, the deduplication of a FIFO queue, a visibility timeout that passes, a restart of the driver).

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/truvity/atoom/transport/sqs"
)

var queueSeq atomic.Int64

// localQueue makes a FIFO queue on ElasticMQ and returns the client and its URL, or skips.
func localQueue(t *testing.T) (*awssqs.Client, string) {
	t.Helper()
	endpoint := os.Getenv("ATOOM_SQS_ENDPOINT")
	if endpoint == "" {
		if os.Getenv("ATOOM_REQUIRE_ELASTICMQ") != "" {
			t.Fatal("ATOOM_SQS_ENDPOINT is not set and the run is required to use ElasticMQ (tools/scripts/conformance-transport.sh)")
		}
		t.Skip("SKIP: ATOOM_SQS_ENDPOINT is not set (ElasticMQ runs in tools/scripts/conformance-transport.sh)")
	}
	client := awssqs.New(awssqs.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(endpoint),
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "local", SecretAccessKey: "local"}, nil
		}),
		RetryMaxAttempts: 1,
	})
	name := fmt.Sprintf("atoom-test-%d-%d.fifo", time.Now().UnixNano(), queueSeq.Add(1))
	out, err := client.CreateQueue(context.Background(), &awssqs.CreateQueueInput{
		QueueName:  aws.String(name),
		Attributes: map[string]string{"FifoQueue": "true", "VisibilityTimeout": "30"},
	})
	if err != nil {
		t.Fatalf("CreateQueue: %v", err)
	}
	url := aws.ToString(out.QueueUrl)
	t.Cleanup(func() {
		_, _ = client.DeleteQueue(context.Background(), &awssqs.DeleteQueueInput{QueueUrl: aws.String(url)})
	})
	return client, url
}

func localOpts(client *awssqs.Client, url string) sqs.Options {
	return sqs.Options{SQS: client, QueueURL: url, Emitter: "test@host", WaitTime: time.Second}
}

func depth(t *testing.T, client *awssqs.Client, url string) string {
	t.Helper()
	out, err := client.GetQueueAttributes(context.Background(), &awssqs.GetQueueAttributesInput{QueueUrl: aws.String(url),
		AttributeNames: []types.QueueAttributeName{"ApproximateNumberOfMessages", "ApproximateNumberOfMessagesNotVisible"}})
	if err != nil {
		t.Fatal(err)
	}
	return out.Attributes["ApproximateNumberOfMessages"] + "/" + out.Attributes["ApproximateNumberOfMessagesNotVisible"]
}

func TestLocalARoundTripKeepsTheOrderAndARetryIsOneMessage(t *testing.T) {
	client, url := localQueue(t)
	c, _ := mount(t, localOpts(client, url))
	for _, u := range []string{"a", "b", "a", "c"} {
		emit(t, c, msg(u))
	}
	k := receive(t, c)
	got := k.take(t, 3)
	if fmt.Sprint(uids(got)) != "[a b c]" {
		t.Fatalf("got %v; want the retry of a dropped by the queue", uids(got))
	}
	for i, d := range got {
		if d.GetEmitter() != "test@host" || string(d.GetMessage().GetPayload()) != "payload-"+d.GetMessage().GetId() {
			t.Fatalf("delivery %d: %v", i, d)
		}
	}
	k.quiet(t, 1500*time.Millisecond)
	ack(t, c, got...)
	if d := depth(t, client, url); d != "0/0" {
		t.Fatalf("the queue holds %s after the ack", d)
	}
}

// What the driver holds comes again on the next stream under the same ids; a visibility timeout that passes while the consumer holds a
// message makes SQS hand it out again, with a new receipt handle: the driver does not deliver it twice, and deletes it by the new handle.
func TestLocalHeldMessagesAndANewReceiptHandle(t *testing.T) {
	client, url := localQueue(t)
	opts := localOpts(client, url)
	opts.VisibilityTimeout = 2 * time.Second
	c, _ := mount(t, opts)
	emit(t, c, msg("a"))
	k := receive(t, c)
	first := k.take(t, 1)
	k.quiet(t, 3*time.Second) // the timeout passes and SQS hands the message out again
	k.stop()
	k = receive(t, c)
	again := k.take(t, 1)
	if again[0].GetDeliveryId() != first[0].GetDeliveryId() {
		t.Fatalf("came again under %d, first under %d", again[0].GetDeliveryId(), first[0].GetDeliveryId())
	}
	ack(t, c, again...)
	k.quiet(t, 2500*time.Millisecond)
	if d := depth(t, client, url); d != "0/0" {
		t.Fatalf("the queue holds %s: the ack did not delete by the latest handle", d)
	}
}

// D50 on SQS: after a restart of the driver, the message it held and did not acknowledge comes again, under a new and greater id, with the
// same uid and bytes: one record for a writer that deduplicates by uid.
func TestLocalARestartRedeliversTheSameMessage(t *testing.T) {
	client, url := localQueue(t)
	opts := localOpts(client, url)
	opts.VisibilityTimeout = time.Second
	c, _ := mount(t, opts)
	emit(t, c, msg("usage-1"))
	k := receive(t, c)
	first := k.take(t, 1)[0]
	k.stop()

	c2, _ := mount(t, opts)
	k2 := receive(t, c2)
	again := k2.take(t, 1)[0]
	if again.GetDeliveryId() <= first.GetDeliveryId() {
		t.Fatalf("the id after the restart %d is not greater than %d", again.GetDeliveryId(), first.GetDeliveryId())
	}
	if again.GetMessage().GetId() != first.GetMessage().GetId() || string(again.GetMessage().GetPayload()) != string(first.GetMessage().GetPayload()) {
		t.Fatalf("the redelivery is another message: %v, first %v", again, first)
	}
	ack(t, c2, again)
	time.Sleep(1500 * time.Millisecond)
	if d := depth(t, client, url); d != "0/0" {
		t.Fatalf("the queue holds %s", d)
	}
}
