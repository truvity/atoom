# 0002. Ports and drivers, extensible, over ConnectRPC

Status: accepted, 2026-11-18

## Context

The shared parts are written in two languages (Go and Rust) and run in different places: as a process behind a socket, inside the consumer's process, or in a Lambda function. One abstraction has to serve all of them, and anyone has to be able to add an implementation, inside or outside this repository.

## Decision

The repository is, by design, **abstract interfaces (ports) with implementations (drivers)**, and it is extensible.

- **A port** is its Protobuf contract, the generated ConnectRPC stubs for each language that needs them (Go, Rust), its documentation, and a **conformance kit** that tests any driver through the driver's ConnectRPC endpoint, whatever the driver's language. A port has no dependency beyond the ConnectRPC and Protobuf runtimes.
- **A driver** is one implementation of one port, with its own thin dependencies and its own documentation. It is a **library** (the ConnectRPC handler and its logic) plus a **thin main** that serves the library on a socket, as a process of its own. Both are in atoom. A consumer in the driver's language may embed the library in-process; [0009](0009-connectrpc-in-process.md) says how. This matters on Lambda, where there are no sidecars: a driver that a function needs links into the function.
- **Anyone can write a driver**, in this repository or outside it. It plugs in by serving the port's contract and passing the port's conformance kit. A driver outside atoom needs nothing from atoom but the contract and the kit. A consumer may offer config-driven selection of a driver by name; atoom ships no registry for it.
- **The language is a choice per driver, not per repository.** A Go consumer can use a Rust driver as a sidecar over ConnectRPC, and the other way round. atoom does not unify languages.
- **Naming.** A port is a directory named for what it abstracts (`key/`, `store/`). Each of its drivers is a subdirectory named for the backend, in lower case without separators (`key/software`, `key/yubikeypiv`, `key/awskms`). The Go module path is `github.com/truvity/atoom/<dir>`, the Rust crate `atoom-<dir with dashes>`, the proto package `atoom.<port>.v1alpha1`. The proto packages were renamed from sluis's `sluis.port.*.v2alpha1` when the ports moved; the package name is part of the RPC path, so the rename changed the wire, which was allowed while sluis was alpha.
- **An optional call is declared.** A driver that does not offer a call answers `unimplemented` and says so in a `Capabilities` call; the kit skips the cases that need it ([architecture](../architecture.md#ports)).
- **The ConnectRPC and Protobuf versions are pinned** in one file, and move together with the consumers ([0015](0015-versioning-and-consumption.md)).
- **One socket guard.** Every thin main guards its socket the same way: the allowed uids, the group and the mode ([socket guard](../socket-guard.md)).

## Consequences

- The Go and Rust question is answered per driver.
- A conformance kit is a program that speaks the port's contract, so one kit covers drivers in every language.
- A new port or a new driver is a new directory with a README and a `docs/` folder.

## Roads not taken

- **Splitting optional calls into separate services.** More contracts to keep, for no gain over a `Capabilities` call.
- **A registry helper that every consumer must use.** A consumer that wants one writes it; nothing depends on it.
- **One language for the repository.** It would force a Go consumer to run a sidecar for a Go driver, or a Rust consumer to link Go.
