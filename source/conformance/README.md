# source/conformance

The conformance kit of the [source port](../docs/source-port.md): the cases every source driver must pass, run through the driver's ConnectRPC endpoint. A driver passes the suite or it is not a driver.

| | |
| --- | --- |
| Library | crate `atoom-source-conformance`: `source_suite(client, &fixture)` runs the cases and returns their results, `report(&cases, out)` prints them and fails on a failed case and on none run |
| Binary | `source-conformance --socket PATH --present NAME --bytes FILE` runs the suite against the driver that serves on the UNIX socket PATH, whatever its language; the exit status is 0 only when every case passed |

The suite only reads, and the contract has no call that writes, so the caller puts one value in the driver's backend before the run and names it: `--present NAME` is a name the driver serves, and `--bytes FILE` holds the exact bytes the driver must return for it. For the file driver that is a document `NAME.jws` in its directory; for the credential driver a credential `NAME` that only its owner can read.

```sh
mkdir docs && printf '{"payload":"x"}\n' > docs/boundary.jws
cargo build -p atoom-source-file -p atoom-source-conformance
target/debug/source-file serve --socket /tmp/source.sock --dir docs &
printf '{"payload":"x"}\n' > /tmp/boundary.bytes
target/debug/source-conformance --socket /tmp/source.sock --present boundary --bytes /tmp/boundary.bytes
```

`just conformance` (CI runs it on Linux, on every architecture) builds the kit once and runs it against the thin main of both drivers in this repository (`tools/scripts/conformance-source.sh`). The cases and what they fix beyond the contract's comments are in [docs/suite.md](docs/suite.md). The kit has a test of its own that a driver that is wrong in each way the suite looks for fails the case that looks for it. Where the code came from is in [../PROVENANCE.md](../PROVENANCE.md).

Dependencies: the port ([`atoom-source`](../README.md)), [`atoom-common`](../../common/README.md) for the refusal helper, [`atoom-socket`](../../socket/README.md) for the client, and `clap`, `tokio` and `anyhow`.
