# destination/conformance

The conformance kit of the [destination port](../docs/destination-port.md): the suite every destination driver must pass, run through the driver's ConnectRPC endpoint, and the Rust helpers for writing a driver of the port. A driver passes the suite or it is not a driver.

| | |
| --- | --- |
| Library | crate `atoom-destination-conformance`: `suite::run(&driver, &caps)` runs the suite in process against any `Destination` (the Rust trait of the five calls), or through a socket with `rpc::Client`; `rpc::Service(Arc::new(driver)).router()` serves any `Destination` as the ConnectRPC `DestinationService`; `memory::Memory` is the in-memory reference driver; `sha256_hex` is the digest; the trait, `Document`, `Error`, `check_address`, `check_prefix`, `check_document` and `page_size` are defined in the port crate `atoom-destination` and re-exported here, so a driver or a consumer may take them from either |
| Binary `destination-conformance` | `destination-conformance --socket PATH [--public-base URL] [--edge]` runs the suite against the driver that serves on the UNIX socket PATH, whatever its language; `--public-base` says the destination has public addresses `<URL>/<address>`, and `--edge` that a cache sits in front of them; the exit status is 0 only when the suite passed |
| Binary `destination-memory` | `destination-memory --socket PATH [--public-base URL] [--edge]` serves the in-memory driver on a socket (mode 0600), so that the kit can be tried against every shape of destination |

```sh
cargo build -p atoom-destination-dir -p atoom-destination-conformance
mkdir -m 0755 /tmp/dest-root; mkdir -m 0700 /tmp/dest-state
target/debug/destination-dir serve --socket /tmp/dest.sock --root /tmp/dest-root --state-dir /tmp/dest-state &
target/debug/destination-conformance --socket /tmp/dest.sock
```

The suite writes under the address prefix `conformance/` and removes what it wrote, so it can run against a destination that serves real documents elsewhere; it needs one that holds nothing under that prefix. `just conformance` (CI runs it on every architecture) builds the kit once and runs it against the thin main of the dir driver and against `destination-memory` in its three shapes (`tools/scripts/conformance-destination.sh`). The cases and what they fix beyond the contract's comments are in [docs/suite.md](docs/suite.md). The kit has a test of its own: the in-memory driver passes through a socket in each shape, and a driver that is wrong in a way the suite looks for fails. Where the code came from is in [../PROVENANCE.md](../PROVENANCE.md).

Dependencies: the port ([`atoom-destination`](../README.md): the trait, the documents and the rules), [`atoom-common`](../../common/README.md) for the refusal helpers, [`atoom-socket`](../../socket/README.md) for the client, `aws-lc-rs` (SHA-256), `connectrpc`, `clap`, `tokio` and `anyhow`. A driver in Rust requires this crate for the trait and the service; the suite is its test.
