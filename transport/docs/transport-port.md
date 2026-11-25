# The transport port

`atoom.transport.v1alpha1.TransportService`: a **transport** carries messages from many emitters to one consumer. It is a queue that holds a message until the consumer says it has dealt with it, so that nothing is lost between the moment an emitter hands a message over and the moment the consumer has it. The transport does not look inside a message. What the messages are is the consumer's: the audit messages that first used this port, and the writer that seals them into a log, belong to [truvity/audit](https://github.com/truvity/audit).

The contract is [transport.proto](../proto/atoom/transport/v1alpha1/transport.proto); this page is its rules in prose, and the conformance kit ([README](../conformance/README.md), [the suite](../conformance/docs/suite.md)) holds every driver to them.

The port has three calls:

| Call | Who | What it does |
| --- | --- | --- |
| `Emit` | the emitters | hands one message to the transport; the answer says the transport has it |
| `Receive` | the consumer | a stream of batches of deliveries, from the oldest message not yet acknowledged |
| `Ack` | the consumer | says that deliveries are done, so that the transport may let them go |

A message today is an `AuditMessage`: a `uid` (its own unique id), a `record` (opaque bytes) and `usage` (opaque bytes of a second record of the same event). Planned (step B12 of the [plan](../../docs/plan.md)): the message is generic, with an id and opaque payload bytes, and the type loses its audit name and the `usage` field; the consumer packs what it needs into the payload.

## The contract

- **An emit that was answered is kept.** `blocking` asks for the answer only once the message is durable; a driver for which every accepted message is durable (the spool) answers the same either way.
- **The emitter is named by the driver, never by the message.** A delivery carries `emitter`: who the driver took the caller to be, from the peer credentials of the socket or from the host's own naming. A driver does not take the name from a field the caller sends.
- **Ordered.** A stream delivers the messages in the order they were emitted, and the `delivery_id`s of the deliveries increase with that order. An id is what the consumer acknowledges.
- **At least once.** A message that was delivered and not acknowledged comes again, under the same id, to the next `Receive` (after the consumer restarted, or the driver did). A message that was acknowledged does not come again. An id the driver does not know is ignored, so a repeated acknowledgement is harmless.
- **One consumer.** While a `Receive` stream is open, another `Receive` is refused with `already_exists`: two consumers would split the order between them. A driver learns that a consumer went away when it sees the stream end, which is some time after the consumer dropped it (the cancel travels the connection, and the driver's side of the stream ends on its own task). A consumer that opens a new stream right after dropping one may therefore be refused with `already_exists` for that while; it opens another (as after any stream that ends on an error). The refusal comes either as the answer to the call or as the first message of the stream: a Connect server stream is answered before its handler has run, so a driver's refusal is the stream's end-of-stream error, and a consumer looks at both.
- **One record, by the uid.** A message may come more than once: again after its consumer went away, and twice when an emitter emitted it twice under one uid (a retry after an answer it did not get). Every delivery of a uid carries the same record and usage bytes, so a consumer that keeps one record for each uid (the audit writer's dedup) counts a usage-class message once. A driver accepts an emit of a uid it has seen (it may keep both or drop the second: JetStream and SQS FIFO drop it within their windows). The kit's case `a_redelivered_or_repeated_message_is_one_record` holds every driver to it (the owner's D50 A, [record 0022](../../docs/decisions/0022-the-blocks-order-after-0-7-0.md)).
- **A window.** A driver may hold back later messages until the consumer acknowledges earlier ones (an SQS FIFO queue hands out nothing of a group while part of it is in flight). A consumer acknowledges what it has sealed as it goes, and seals on a timer rather than waiting for a full batch; the kit acknowledges as it takes where it emits on an open stream.
- **Never drops.** A driver that is full refuses the emit with `unavailable`; it does not drop an earlier message and does not drop the new one silently. Whether a producer may then drop, count or block is the producer's rule, not the transport's.
- **A stream** that ends on an error tells the consumer to open another; a fresh stream starts from the oldest message not yet acknowledged.

## Capabilities

The port has no `Capabilities` call. Every driver offers `Emit`, `Receive` and `Ack`; a driver may serve them on separate servers, and answers `unimplemented` / `unsupported` to a call that its server does not serve. Open: a driver that cannot keep the order (a queue with several partitions) or the single consumer would need to declare it. Whether that becomes a `Capabilities` call is decided with the first such driver.

## Refusals

Every refusal is a Connect error with the `Refusal` detail of [common](../../common/docs/refusal.md): a reason from a closed list and the path of the field at fault, never a value.

| Code | Reason | When |
| --- | --- | --- |
| `invalid_argument` | `schema` | an emit with no message, an empty uid or an empty record; the field is `message`, `message.uid` or `message.record` |
| `invalid_argument` | `bound` | a message or an emitter name over the driver's limit; more ids in one `Ack` than the driver takes |
| `permission_denied` | `caller` | the host could not tell who the caller is |
| `already_exists` | `taken` | `Receive` while another stream is open |
| `unavailable` | `engine` | the bound is full, the disk failed, or an earlier failure left the files uncertain; the message is the reason only |
| `unimplemented` | `unsupported` | a call that this server does not serve (a spool serves `Emit`, `Receive` and `Ack` on separate servers) |

## Drivers

| Driver | Backend | Where it fits |
| --- | --- | --- |
| [transport/spool](../spool/README.md) | a local directory of fsync'd segment files, bounded | a host with a disk, where a message must survive the consumer being down |
| `transport/sqs` | an Amazon SQS queue | Lambda (planned, step B9): a Go driver that the function mounts in process, so audit on Lambda needs no spool sidecar as an extension ([record 0022](../../docs/decisions/0022-the-blocks-order-after-0-7-0.md)). SQS delivers at least once: the driver and the kit prove that a usage-class message survives a redelivery as exactly one record, through the writer's dedup by message id (a redelivery and duplicate case in the kit) |
| [transport/nats](../nats/README.md) (Go) | a NATS JetStream stream and a durable consumer | a cluster that runs NATS: a Go driver a host mounts in process, so that its audit records need no sidecar (step B9, [record 0023](../../docs/decisions/0023-session-to-done-first.md)); the delivery id is the stream sequence, a retry is dropped by `Nats-Msg-Id`, the connection and its credentials are the host's |

## The call context

Every call carries the deadline and the correlation id of the request it is part of, `Receive` included. The rules are on [the call context](../../docs/call-context.md). A Rust client: `TransportServiceClient` over `atoom_socket::dial`; a Go client: `transportv1alpha1connect.NewTransportServiceClient(hc, url, call.ClientOptions()...)`.

## A driver of your own

Serve `TransportService` (Go: implement `transportv1alpha1connect.TransportServiceHandler`; Rust: implement the `TransportService` trait of the crate `atoom-transport`), serve it on a socket or mount it in a process, and run the kit against it: `transport-conformance --emit-socket PATH --receive-socket PATH` (the same path twice for a driver that serves both sides on one socket). A driver passes the suite or it is not a driver. The kit speaks the contract only, so the driver may be written in any language.
