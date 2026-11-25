# The SQS driver

How the [transport port](../../docs/transport-port.md) maps to an Amazon SQS FIFO queue.

## The mapping

| Port | SQS |
| --- | --- |
| The transport | one FIFO queue (its URL ends in `.fifo`), made by the deployment with its visibility timeout, its redrive policy and its encryption; the driver makes nothing |
| `Emit` | `SendMessage` in the one message group of the host (`MessageGroupID`), with the deduplication id SHA-256(id) in hex; the body is the message in its Protobuf encoding, in base64 (a body is text); the emitter is the message attribute `atoom-emitter`. SQS answers once the message is stored, so a blocking emit and another are answered alike |
| The bounds | the encoded message at most 192 KiB by default (`MessageMax`, so that its base64 fits a queue's 256 KiB), up to 768 KiB for a queue that takes 1 MiB; the emitter 255 bytes |
| `Receive` | `ReceiveMessage` of up to ten messages, with a long poll of up to 20 s (`WaitTime`) or short polls (`ShortPoll`), and the same `ReceiveRequestAttemptId` again after a receive that did not return |
| The delivery id | a number the driver gives a message when it first takes it: from the time of its start in nanoseconds, one more for each message, so the ids increase across restarts as the clock does |
| `Ack` | `DeleteMessageBatch` by the latest receipt handle of each message; a handle SQS no longer takes (`ReceiptHandleIsInvalid`) is let go: the message comes again under a new id |
| A message the driver did not write | left in the queue and logged; it comes again until the queue's redrive policy moves it to the dead-letter queue, and until then it holds back the rest of its group |

## The window of a FIFO queue

A FIFO queue hands out no message of a group while one of that group is in flight. With one group, the consumer has at most one receive (ten messages) unacknowledged: the next comes once it acknowledges. A consumer acknowledges what it is done with as it goes, on a timer, not when a batch is full. The transport port allows this window, and the kit acknowledges as it takes where it emits on an open stream.

A long poll that the end of a stream cuts off may go on in SQS and take a message for nobody. The next receive uses the same attempt id and gets that message, but alone: messages emitted after it wait behind it in the window until it is acknowledged. Nothing is lost; the order holds. `ShortPoll` (the thin main's `--wait-time 0s`) leaves no poll behind, at the price of a call every quarter of a second while the queue is empty; the conformance run uses it, since the kit emits right after it drops a stream.

## At least once, and one message

- A stream that ends leaves what it was given held; the next stream gets it first, under the same ids, and an `Ack` after the stream ended still deletes it.
- A message whose visibility timeout passes while it is held comes again from SQS: the driver keeps its newer receipt handle and does not deliver it twice on a stream. Set the visibility timeout above the time a consumer takes to be done with a batch, and the redrive policy's `maxReceiveCount` well above one, since each such return counts as a receive.
- After a restart of the driver, a message it held comes again once its visibility timeout has passed, under a new and greater id.

A consumer that must count a message once (audit's usage class, for one) keeps one message for each id. SQS drops a retry within five minutes by its deduplication id; a redelivery, and a retry after that, carry the same id and the same payload, so such a consumer makes one message of each. The kit's case `a_redelivered_or_repeated_message_is_one_message` holds the driver to it against ElasticMQ, and the tests of the module to a restart.

## Not tested here

ElasticMQ and the fake are not SQS: the throughput of one message group (300 sends a second, more with high throughput mode), the exact lifetime of a receipt handle and of an attempt id, encryption and the redrive policy are the deployment's to check in an account.
