# index/conformance

The conformance kit of the [index port](../docs/index-port.md): the cases every index driver must pass, run through the driver's ConnectRPC endpoints. A driver passes the suite or it is not a driver.

| | |
| --- | --- |
| Library | crate `atoom-index-conformance`: `index_suite(write_client, read_client)` runs the cases and returns their results, `report(&cases, out)` prints them and fails on a failed case and on none run |
| Binary | `index-conformance --socket PATH --read-socket PATH` runs the suite against the driver that serves its write side (`IndexService`) on the UNIX socket PATH and its read side (`IndexReadService`) on the other (the same path twice for a driver that serves both on one), whatever its language; the exit status is 0 only when every case passed |

The suite writes under a project of its own, `conformance-<run>`, and **cannot remove what it wrote**: the contract has no delete. Run it against a scratch index.

```sh
cargo build -p atoom-index-sqlite -p atoom-index-conformance
mkdir -m 0750 /tmp/index-state
target/debug/index-sqlite serve --socket /tmp/index.sock --read-socket /tmp/index-ro.sock --state-dir /tmp/index-state &
target/debug/index-conformance --socket /tmp/index.sock --read-socket /tmp/index-ro.sock
```

`just conformance` (CI runs it on every architecture) builds the kit once and runs it against the thin main of the sqlite driver (`tools/scripts/conformance-index.sh`). The cases and what they fix beyond the contract's comments are in [docs/suite.md](docs/suite.md). The kit has a test of its own that a driver that is wrong in each way the suite looks for fails the case that looks for it. Where the code came from is in [provenance](../../docs/provenance.md#index).

Dependencies: the port ([`atoom-index`](../README.md)), [`atoom-common`](../../common/README.md) for the refusal helper, [`atoom-socket`](../../socket/README.md) for the client, `buffa` for the message fields, and `clap`, `tokio` and `anyhow`.
