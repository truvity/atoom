# The destination port

`atoom.destination.v1alpha1.DestinationService`: a **destination** is where a published document goes: a directory that a forwarder serves, later a bucket behind an edge. The port is written for a consumer that publishes public documents (key sets, certificates, configuration); a driver per type of destination serves it. The contract is [destination.proto](../proto/atoom/destination/v1alpha1/destination.proto); this page is its rules in prose, and the conformance kit ([README](../conformance/README.md), [the suite](../conformance/docs/suite.md)) holds every driver to them.

The port has five calls:

| Call | What it does |
| --- | --- |
| `Put` | a document, whole and atomically, with its content type and `Cache-Control`; a document already at the address is replaced |
| `Delete` | removes a document the consumer wrote; `removed: false` when there was none |
| `List` | the documents the consumer wrote under a prefix, in address order, with the digest of what the destination holds now |
| `Purge` | evicts the edge copy of public addresses |
| `ReadBack` | fetches a public address the way a verifier does: no credential and no cache-buster |

## The contract

- **An address** is one to eight segments joined by `/`; a segment is one to 128 characters of lower-case letters, digits, `-`, `_` and `.`, never `.` or `..`; an address is at most 512 bytes. The first segment is not `.atoom` (a name a driver may keep for itself) and no segment starts with `.tmp-` (the name of a driver's temporary file). A leading dot is allowed otherwise, for `.well-known`. The reserved name is `.atoom`. It was `.sluis` in the code this port came from and was renamed before the first release, with no compatibility shim: `.sluis/...` is an ordinary first segment now. A consumer that wrote documents under `.sluis` migrates its own data when it takes this port.
- **A write is whole.** A reader sees the old document or the new one, never half of it, and a crash leaves one of them.
- **A document is the driver's** (listed and removable) only while the driver wrote it: a file that someone else put at the destination is never listed and never removed.
- **The digest in `List` is of what the destination holds now**, not of what was written, so an edit made at the destination shows.
- **A document is at most 2 MiB**; the content type and the cache control are printable ASCII of at most 256 characters, because they become header values.
- **A page** of a listing is at most `page_size` documents (0 is 200; no page is larger than 1000), and the token of the next page is empty on the last page. A prefix is empty or an address prefix that may end inside a segment.
- **A destination with no public address** (a store of record, a directory with none declared) answers `unimplemented` with the reason `unsupported` to `ReadBack`, and `Purge` with `PURGE_RESULT_NONE`. With an edge, `Purge` answers `PURGE_RESULT_PURGED` and the next `ReadBack` fetches the document again. A `Purge` or a `ReadBack` of an address that is not the destination's is `invalid_argument`.

## Refusals

Every refusal is a Connect error with the `Refusal` detail of [common](../../common/docs/refusal.md): a reason from a closed list and the path of the field at fault, never a value.

| Code | Reason | When |
| --- | --- | --- |
| `invalid_argument` | `invalid` | an address, a prefix, a page token, a body or a header value that is not acceptable; the field path says which |
| `unimplemented` | `unsupported` | `ReadBack` on a destination with no public address |
| `unavailable` | `engine` | the destination could not be reached or failed; the message is the reason only |

## Drivers

| Driver | Backend | Where it fits |
| --- | --- | --- |
| [destination/dir](../dir/README.md) | a local directory | a host whose public documents are static files served by a forwarder or a file server |
| `destination-memory` (in the [kit](../conformance/README.md)) | memory | the reference driver; plays a public address and an edge, to try the kit against every branch |

## Capabilities

The port has no `Capabilities` call. A destination says what it is by what it answers: one with no public address answers `unimplemented` / `unsupported` to `ReadBack` and `PURGE_RESULT_NONE` to `Purge` (see the contract). Open: whether a `Capabilities` call replaces those answers is decided with the first destination that has an edge.

## The call context

Every call carries the deadline and the correlation id of the request it is part of. The rules are on [the call context](../../docs/call-context.md). The Rust client of the port is `atoom_destination::client::Client`, the `Destination` that a consumer calls a driver's socket with (`Client::new(dial.transport(), dial.config())` over `atoom_socket::dial`; the kit re-exports it as `rpc::Client`). A Go client: `destinationv1alpha1connect.NewDestinationServiceClient(hc, url, call.ClientOptions()...)`.

## A driver of your own

Rust: implement the `Destination` trait of the crate `atoom-destination` (the port crate, `std` only; `atoom-destination-conformance` re-exports it) and serve it with `rpc::Service(Arc::new(driver)).router()` (of the kit); any language: serve `DestinationService` (Go: implement `destinationv1alpha1connect.DestinationServiceHandler`). Serve it on a socket or mount it in a process, and run the kit against it: `destination-conformance --socket PATH` (with `--public-base URL` when it has a public address and `--edge` when a cache sits in front of it). A driver passes the suite or it is not a driver. The kit speaks the contract only, so the driver may be written in any language.
