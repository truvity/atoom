# The transport port

`atoom.transport.v1alpha1.TransportService`: a **transport** carries audit messages from the emitters (the parts of a system that record events) to one consumer (the writer that seals them into a log). It is a queue that holds a message until the consumer says it has sealed it, so that no fact is lost between the moment an emitter hands it over and the moment the log has it. The contract is [transport.proto](../proto/atoom/transport/v1alpha1/transport.proto); this page is its rules in prose, and the conformance kit ([README](../conformance/README.md), [the suite](../conformance/docs/suite.md)) holds every driver to them.

The port has three calls:

| Call | Who | What it does |
| --- | --- | --- |
| `Emit` | the emitters | hands one message to the transport; the answer says the transport has it |
| `Receive` | the consumer | a stream of batches of deliveries, from the oldest message not yet acknowledged |
| `Ack` | the consumer | says that deliveries are sealed, so that the transport may let them go |

A message is an `AuditMessage`: a `uid` (the record's own unique id), the `record` (opaque bytes) and the `usage` records of the same event (opaque bytes). The transport does not look inside.

## The contract

- **An emit that was answered is kept.** `blocking` asks for the answer only once the message is durable; a driver for which every accepted message is durable (the spool) answers the same either way.
- **The emitter is named by the driver, never by the message.** A delivery carries `emitter`: who the driver took the caller to be (sluis: `<actor>@<boundary>`, from the peer credentials of the socket). A driver does not take the name from a field the caller sends.
- **Ordered.** A stream delivers the messages in the order they were emitted, and the `delivery_id`s of the deliveries increase with that order. An id is what the consumer acknowledges.
- **At least once.** A message that was delivered and not acknowledged comes again, under the same id, to the next `Receive` (after the consumer restarted, or the driver did). A message that was acknowledged does not come again. An id the driver does not know is ignored, so a repeated acknowledgement is harmless.
- **One consumer.** While a `Receive` stream is open, another `Receive` is refused with `already_exists`: two consumers would split the order between them.
- **Never drops.** A driver that is full refuses the emit with `unavailable`; it does not drop an earlier message and does not drop the new one silently.
- **A stream** that ends on an error tells the consumer to open another; a fresh stream starts from the oldest message not yet acknowledged.

## Refusals

Every refusal is a Connect error with the `Refusal` detail of [common](../../common/docs/refusal.md): a reason from a closed list and the path of the field at fault, never a value.

| Code | Reason | When |
| --- | --- | --- |
| `invalid_argument` | `schema` | an emit with no message, an empty uid or an empty record; the field is `message`, `message.uid` or `message.record` |
| `invalid_argument` | `bound` | a message or an emitter name over the driver's limit; more ids in one `Ack` than the driver takes |
| `permission_denied` | `caller` | the host could not tell who the caller is |
| `already_exists` | `taken` | `Receive` while another stream is open |
| `unavailable` | `engine` | the bound is full, the disk failed, or an earlier failure left the files uncertain; the message is the reason only |
| `unimplemented` | `unsupported` | a call that this server does not serve (a spool serves `Emit` and `Receive` and `Ack` on separate servers) |

## Drivers

| Driver | Backend | Where it fits |
| --- | --- | --- |
| [transport/spool](../spool/README.md) | a local directory of fsync'd segment files, bounded | a host with a disk, where a message must survive the writer being down |

## Every call carries the deadline and the correlation id

Every call of the port carries what is left of the caller's deadline (`Connect-Timeout-Ms`) and the correlation id of the request (`Sluis-Correlation-Id`, 1 to 64 characters of letters, digits, `-` and `_`), and the thin main of every driver reads both and hands them to its handler, which hands them down to the calls it makes. A callee never extends its caller's deadline, and an id of another shape is dropped. A client of the transport port: `TransportServiceClient`, Rust over `atoom_socket::dial`, Go `transportv1alpha1connect.NewTransportServiceClient(hc, url, call.ClientOptions()...)`. `Receive` carries them too. The rules and how a caller outside a handler states them are on [the call context](../../docs/call-context.md).

## A driver of your own

Serve `TransportService` (Go: implement `transportv1alpha1connect.TransportServiceHandler`; Rust: implement the `TransportService` trait of the crate `atoom-transport`), serve it on a socket or mount it in a process, and run the kit against it: `transport-conformance --emit-socket PATH --receive-socket PATH` (the same path twice for a driver that serves both sides on one socket). A driver passes the suite or it is not a driver. The kit speaks the contract only, so the driver may be written in any language.
