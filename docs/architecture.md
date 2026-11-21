# Architecture

How the repository is laid out, what each kind of directory is, and the rules that keep the libraries apart. The decisions behind it are the [records](decisions/README.md).

## The four kinds of thing

| Kind | What it is | Records |
| --- | --- | --- |
| **Port** | a Protobuf contract, its generated ConnectRPC stubs (Go, Rust), its documentation and a conformance kit | [0002](decisions/0002-ports-and-drivers-over-connectrpc.md) |
| **Driver** | one implementation of one port: a library plus a thin main that serves it on a socket | [0002](decisions/0002-ports-and-drivers-over-connectrpc.md), [0010](decisions/0010-distribution-per-driver-and-lambda.md) |
| **Block** | a contract built from other ports and blocks, with no backend code, a kit, and rules supplied by its host | [0008](decisions/0008-blocks.md) |
| **Kit** | the tests of a port or a block, run through its ConnectRPC endpoint in any language | [0002](decisions/0002-ports-and-drivers-over-connectrpc.md), [0014](decisions/0014-what-atoom-proves.md) |

```mermaid
flowchart TB
  subgraph consumers["Consumers"]
    C1["sluis, session, audit, ritueel"]
  end
  subgraph atoom["truvity/atoom (one version)"]
    B["Blocks: seal, state, token, export (planned)"]
    P["Ports: contract + stubs + docs + kit"]
    D["Drivers: library + thin main"]
    K["Kits"]
  end
  C1 -- "ConnectRPC: in-process, socket or extension" --> B
  C1 -- "ConnectRPC" --> P
  B -- "calls only ports and blocks" --> P
  D -- implements --> P
  K -- tests --> D
  K -- tests --> B
```

A consumer calls a port or a block through its generated client, always, also in one process ([0009](decisions/0009-connectrpc-in-process.md)). The one exception is Cedar, which is used as a library ([0011](decisions/0011-cedar-as-a-library.md)).

## Repository layout

```text
atoom/
  versions.yaml          the pinned runtimes (record 0015)
  go.work                development only; lists every Go module
  Cargo.toml             one workspace; explicit member list, no globs
  Justfile               the recipes that CI and people run
  common/                Refusal and IndexEntry, the call context (Go)
  socket/                the socket host: dial, serve, the caller guard, the call context (Rust crate and Go module)
  key/                   port: proto, stubs, README, docs/
    conformance/         the key kit (a Go module of its own)
    software/  yubikeypiv/  awskms/     drivers
    slotplan/  pinrelease/              small libraries of the card driver
  store/                 port: items
    conformance/  rqlite/  memory/  dynamodb/
  source/  blob/  transport/  index/  destination/    the other ports, same shape
  blocks/                blocks, one directory each (planned: none is built yet)
  deploy/release/        the Dockerfile templates of the images
  tools/                 release scripts, isolation check, conformance scripts
  docs/                  this page, the call context, the socket guard, packages, release, provenance, plan, decisions, archive
```

- A directory with a `go.mod` is a Go library, one with a `Cargo.toml` a Rust library. Both files may sit side by side in one directory (Go sources at the root, Rust in `src/`), which is how a port holds stubs in two languages.
- Each library has its own `README.md` and a `docs/` folder.
- Cargo members and `go.work` `use` lines are listed explicitly, so that a Go-only directory needs no exclusion.
- The module path is `github.com/truvity/atoom/<dir>`, the crate `atoom-<dir with dashes>`, the proto package `atoom.<port>.v1alpha1`.
- A block lives in `blocks/<name>/`: its contract, stubs, README, `docs/`, kit and implementation. Its page is `docs/<name>-block.md` in that directory. This is planned; the first block is `seal` (step B4 of the [plan](plan.md)).

## Dependency rules

Checked in CI by `just check-isolation` ([0003](decisions/0003-libraries-with-thin-dependencies.md)):

- a **port** module requires only the ConnectRPC and Protobuf runtimes and what their generated code needs, besides the `common` types;
- a **driver** may require the `common` types, its own port, the kit of its own port (for tests), a shared library and its own backend SDK. It may not require another driver or the code of another port;
- **no driver requires another driver.** A small library that several drivers of one port need is a shared library, listed in `tools/isolation/shared.txt` (`socket`, `key/slotplan`, `key/pinrelease`, `blob/fsdir`);
- a **block** requires only ports and blocks, with no cycle, and never a backend ([0008](decisions/0008-blocks.md)). The check is added with the first block;
- a consumer that takes `key` and `key/software` has no AWS SDK, PC/SC, DynamoDB or PostgreSQL code in its module graph.

The cgo and PC/SC dependency of `key/yubikeypiv` is behind the build tag `atoom_card`. Without the tag the driver builds with no C code and refuses to open a card.

## Ports

Every port has one page, `<port>/docs/<port>-port.md`, in the same shape: the calls, the contract, **Capabilities**, the refusals, the drivers (built and planned), the call context (a link), and how to write a driver. The call context is stated once, in the [call context](call-context.md), and every port page links to it.

| Port | Page | What it abstracts | Drivers built |
| --- | --- | --- | --- |
| `key` | [key port](../key/docs/key-port.md) | private keys: sign, agree, attest | `software`, `yubikeypiv`, `awskms` |
| `store` | [store port](../store/docs/store-port.md) | items with versions, expiry, indexes and a change log | `rqlite`, `memory`, `dynamodb` |
| `source` | [source port](../source/docs/source-port.md) | named values and their bytes, watched or not | `file`, `credential` |
| `blob` | [blob port](../blob/docs/blob-port.md) | write-once objects and a few conditional pointers | `fs`, `fsreadonly` |
| `transport` | [transport port](../transport/docs/transport-port.md) | a durable ordered queue to one consumer | `spool` |
| `index` | [index port](../index/docs/index-port.md) | a search index of the records of an append-only log | `sqlite` |
| `destination` | [destination port](../destination/docs/destination-port.md) | where a published document goes | `dir` |

`common` is not a port: it holds the types every port speaks ([refusal](../common/docs/refusal.md)).

- **Capabilities.** A port with optional calls or properties has a `Capabilities` call: `key` (the store, its algorithms, attestation), `store` (the engine, the change log; durability is planned) and `source` (how a newer version is reported). A driver answers `unimplemented` for a call it does not offer, and the kit skips the cases that need it. The other ports have none today, and their pages say so.
- **Generic ports.** The blob, transport and index contracts were written for an audit log and still carry audit's vocabulary in a few fields. Their pages describe them generically, and the rename is planned (step B12 of the [plan](plan.md)). The audit model lives in [truvity/audit](https://github.com/truvity/audit).
- **Refusals.** A refusal is a Connect error with the `Refusal` detail of `common`: a reason from a closed list and the path of the field at fault, never a value.

## Drivers

Each driver has a README and a page in its `docs/` folder. [Packages](packages.md) lists every driver with the forms it is released in, Lambda included. Every thin main guards its socket the same way ([socket guard](socket-guard.md)). The research behind a backend is next to the driver ([0006](decisions/0006-driver-research-lives-here.md)).

## Where each kind of fact lives

| Fact | Home |
| --- | --- |
| a decision, and why | [decision records](decisions/README.md) |
| the rules of a contract | the port's page |
| how a driver behaves | the driver's README and `docs/` |
| what a backend can do, measured | the driver's `docs/research/` |
| the deadline and the correlation id | [call context](call-context.md) |
| the socket guard | [socket guard](socket-guard.md) |
| names, forms, Lambda, images | [packages](packages.md) |
| how a release is made | [releasing](release.md) |
| where code was copied from | [provenance](provenance.md) |
| what is built and what comes next | [plan](plan.md) |
| history of the extraction | [archive](archive/README.md) |
