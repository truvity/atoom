# transport/conformance

The conformance kit of the [transport port](../docs/transport-port.md): the cases every transport driver must pass, run through the driver's ConnectRPC endpoints. A driver passes the suite or it is not a driver.

| | |
| --- | --- |
| Library | crate `atoom-transport-conformance`: `transport_suite(emit_client, receive_client)` runs the cases and returns their results, `report(&cases, out)` prints them and fails on a failed case and on none run |
| Binary | `transport-conformance --emit-socket PATH --receive-socket PATH` runs the suite against the driver that serves on those UNIX sockets (the same path twice for a driver that serves both sides on one), whatever its language; the exit status is 0 only when every case passed |

The suite emits messages of its own (their uid starts with `conformance-<run>-`), takes them from the driver and acknowledges them, and looks only at those: a message that another emitter left in the queue is not judged and not acknowledged. It is one consumer's suite, so run it against a driver that nobody else consumes from.

```sh
cargo build -p atoom-transport-spool -p atoom-transport-conformance
mkdir -m 0700 /tmp/spool /tmp/socks
target/debug/transport-spool serve --socket-dir /tmp/socks --root /tmp/spool &
target/debug/transport-conformance --emit-socket /tmp/socks/emit.sock --receive-socket /tmp/socks/receive.sock
```

`just conformance` (CI runs it on every architecture) builds the kit once and runs it against the thin main of the spool driver (`tools/scripts/conformance-transport.sh`). The cases and what they fix beyond the contract's comments are in [docs/suite.md](docs/suite.md). The kit has a test of its own that a driver that is wrong in each way the suite looks for fails the case that looks for it. Where the code came from is in [provenance](../../docs/provenance.md#transport).

Dependencies: the port ([`atoom-transport`](../README.md)), [`atoom-common`](../../common/README.md) for the refusal helper, [`atoom-socket`](../../socket/README.md) for the client, and `clap`, `tokio` and `anyhow`.
