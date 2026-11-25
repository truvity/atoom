# The source suite

The cases of `source_suite`, in the order they run. Each one speaks the contract and nothing else, over a client of the kit, and writes nothing.

| Case | What it holds a driver to |
| --- | --- |
| `capabilities` | `Capabilities` answers `NONE`, `NOTIFY` or `POLL`, never the unset value |
| `read_gives_the_bytes_and_a_label` | a read of the fixture's name returns exactly the fixture's bytes, is not `unchanged`, and carries a non-empty label that is the same on a second read |
| `read_if_none_match` | a read at the label held is `unchanged` with no bytes and the same label; a read at any other label gets the bytes |
| `absent_name` | a name no value has is `not_found`, reason `absent`, field path `name` |
| `malformed_names` | the empty name, `../x`, `a/b` and a name with a NUL are each `invalid_argument`, reason `schema`, field path `name` |
| `watch_follows_the_capability` | by the capability the driver declared: `NONE`, `Watch` is `unimplemented`; `NOTIFY`, a watch of a name that does not change stays open and says nothing, and a watch of a name the source cannot hold is `invalid_argument`/`schema`; `POLL`, a read says in `poll_after` when to ask again |

## What the suite fixes beyond the proto comments

These are the choices the contract left open. Every driver makes them, and a consumer may rely on them. They are in the crate documentation of `atoom_source_conformance` and, as prose, in the [source port](../../docs/source-port.md).

- A refusal always names the request's `name` as the field at fault.
- A source answers a name it cannot hold with `schema` and a name it can hold and does not have with `absent`, so a caller tells a mistake from an absence.
- `Watch` follows the capability: a source that cannot be watched says so with `unimplemented`, and does not open a stream that never speaks.

## What the suite cannot test

It cannot change the backend, so that a `NOTIFY` driver says a newer version exists when the content changes, and says the empty label when a name goes away, is the test of each driver (the file driver has one that replaces and removes a document under a watch). It cannot make the backend unavailable, so the `unavailable/engine` refusal is a driver test too, and so are the rules of the backend itself (the file driver's links and size limit, the credential driver's owner and mode rules): each driver has a backend suite for them (`suite::run_temp`).

It cannot test a `POLL` driver beyond the `poll_after` of a read: that a poll brings a new deployment is the test of each driver (the AppConfig driver has one over its fake).

## Running it

Against a socket: `source-conformance --socket PATH --present NAME --bytes FILE`. From Rust: `atoom_source_conformance::source_suite(atoom_socket::dial(path), &Fixture { name, data }).await`, then `report`. A driver in Go can run the binary as a test step; the contract is the same in every language.
