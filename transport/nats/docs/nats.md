# The NATS JetStream driver

How the [transport port](../../docs/transport-port.md) maps to NATS JetStream.

## The mapping

| Port | JetStream |
| --- | --- |
| The transport | one stream (the log), which captures the subject the host names, and one durable pull consumer of it (the consumer's), with explicit acknowledgement. With `Create` the driver makes them when they are not there: file storage, a duplicate window of two minutes, the consumer from the first message with an ack wait of 30 seconds (`AckWait`). Without it, they are the deployment's, and the driver needs no right to make them |
| `Emit` | a publish to the subject, with `Nats-Msg-Id` set to the message's id and the header `Atoom-Emitter` set to the emitter; the body is the message in its Protobuf encoding. The answer waits for JetStream's publish acknowledgement, so a stored message is answered, blocking or not |
| The emitter | the host's name for the caller (`Emitter`, or `Identify` from the context of the call), never a field of the message |
| `Receive` | pulls of up to `max_batch` messages (at most 100), each waiting up to `WaitTime` (5 seconds); one stream at a time, a second one is `already_exists` |
| The delivery id | the message's stream sequence |
| `Ack` | an acknowledgement of each message that waits for JetStream's answer (`DoubleAck`); an id the driver does not hold is ignored |
| A message the driver did not write | terminated (`Term`), so that JetStream does not deliver it again, and logged |

## At least once, and one message

JetStream delivers again a message that is not acknowledged within the consumer's ack wait. The driver holds every message it delivered until it is acknowledged:

- a stream that ends leaves what it was given held; the next stream gets it first, under the same ids, with no new pull, and an `Ack` made after the stream ended still acknowledges it;
- a message JetStream delivers again while it is held takes the place of the held one (its acknowledgement is the newer one) and is not delivered twice on a stream;
- after a restart of the driver, JetStream delivers it again once the ack wait has passed, under the same id, since the id is the stream sequence.

A pull is not bound to the stream: what a pull in progress brings when the stream ends is held and goes on the next one. So the driver lets go of a dropped consumer once that pull ends (up to `WaitTime`), and a consumer that opens a new stream at once may be refused with `already_exists` for that while, as the port allows.

A consumer that must count a message once (audit's usage class, for one) keeps one message for each id. JetStream drops a publish made again within the duplicate window, and every delivery of a message carries the same id and the same payload, so such a consumer makes one message of a retry and of a redelivery alike. The kit's case `a_redelivered_or_repeated_message_is_one_message` holds the driver to it.

## Permissions

The user the broker maps the host to needs, on the stream: publish on the subject, and the JetStream API calls `STREAM.INFO` (with `Create`: `STREAM.CREATE`); for a host that consumes, `CONSUMER.INFO` and `CONSUMER.MSG.NEXT` on the consumer and publish on its acknowledgement subjects (`$JS.ACK.<stream>.<consumer>.>`), and with `Create` `CONSUMER.CREATE`. An emitter-only host, such as session, needs the first set alone.

## Not tested here

The tests run against the real server, embedded, on one node. A cluster (replicated streams, a leader that moves), the broker's mapping of an identity to a user, TLS, and limits of the account (the stream's size, the maximum payload) are the deployment's to check.
