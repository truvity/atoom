# The transport suite

The cases of `transport_suite`, in the order they run. Each one speaks the contract and nothing else, over a client of the kit for the emit side and one for the consume side. Each opens the stream it needs and drops it before the next case; a driver that has not yet let go of a dropped consumer (a refusal with `already_exists` as the answer to `Receive` or as the first message of the stream, before any delivery) is asked again for ten seconds.

| Case | What it holds a driver to |
| --- | --- |
| `emit_then_receive_in_order` | three messages emitted (one not blocking, two blocking) come on an open stream in the order emitted, each exactly as emitted (uid, record, both usage records), each with a non-empty emitter and a delivery id other than 0, and the ids increase; the three are acknowledged |
| `a_second_consumer_is_refused` | while a `Receive` stream is open and served (a message came on it), a second is `already_exists` (at the call or at the first message); the message is acknowledged |
| `unacknowledged_messages_come_again` | two messages are delivered and not acknowledged; the stream is dropped; the next `Receive` delivers the same two under the same ids |
| `acknowledged_messages_do_not_come_again` | a message that is acknowledged (with an id the driver does not know beside it, and then again) is not delivered to the next stream in 1.5 seconds; an unknown id and a repeated acknowledgement are not errors |
| `malformed_emits` | an emit with no message, with an empty uid and with an empty record is `invalid_argument`, reason `schema`, field `message`, `message.uid` and `message.record` |

## What the suite fixes beyond the proto comments

These are the choices the contract left open. Every driver makes them, and a consumer may rely on them. They are in the crate documentation of `atoom_transport_conformance` and, as prose, in the [transport port](../../docs/transport-port.md).

- A delivery has an emitter name that is not empty and a delivery id that is not 0.
- The ids of one stream increase with the order of the emits.
- A second consumer is refused with `already_exists`, not served and not queued behind the first.
- An unknown id in an `Ack` is ignored.

## What the suite cannot test

It cannot restart the driver, so that a message survives a restart of the driver (not only of the consumer) is the spool's own test. It cannot fill the driver, so the refusal of a full queue (`unavailable`/`engine`, nothing dropped) is a driver test too, and so are the rules of the backend (the sum of a frame, the torn tail, the bound). It cannot tell who the emitter should be: the name is the host's, and the suite only asks that there is one.
