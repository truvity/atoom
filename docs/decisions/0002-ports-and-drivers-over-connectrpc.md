# 0002. Ports and drivers, extensible, over ConnectRPC

Status: accepted, 2026-11-18

## Context

The pieces shared by the three projects are written in two languages (Go and Rust) and run in different places: as a process behind a socket (sluis), or inside the consumer (session, ritueel). One abstraction has to serve all of them, and anyone has to be able to add an implementation, inside or outside this repository.

## Decision

The repository is, by design, **abstract interfaces (ports) with implementations (drivers)**, and it is extensible.

- **A port** is its Protobuf contract, the generated ConnectRPC stubs for each language that needs them (Go, Rust), its documentation, and a **conformance kit** that tests any driver through the driver's ConnectRPC endpoint, whatever the driver's language. A port has no dependency beyond the ConnectRPC and Protobuf runtimes.
- **A driver** is a ConnectRPC server of one port, in any language, with its own thin dependencies and its own documentation. It is a **library** (the ConnectRPC handler and its logic) plus a **thin main** that serves the library on a socket, as its own process, as sluis runs drivers today. Both are in atoom. A consumer in the driver's language may instead embed the library in-process (in Go, by mounting the Connect handler on an in-memory transport). The contract is the same either way. This matters on the Lambda shape, where there are no sidecars: a driver that a Lambda function needs must link into the function.
- **Anyone can write a driver**, in this repository or outside it. It plugs in by serving the port's contract and passing the port's conformance kit. A driver outside atoom needs nothing from atoom except the port's contract and kit.
- **The language is a choice per driver, not per repository.** A Go consumer can use a Rust driver as a sidecar over ConnectRPC, and the other way round.
- **The ConnectRPC and Protobuf versions are pinned and move together with sluis** (see the plan, "Pinned runtimes").
- Directory and module names: a port is a directory named for what it abstracts (`key/`, `store/`); each of its drivers is a subdirectory named for the backend, lower case without separators (`key/software`, `key/yubikeypiv`, `key/awskms`).

## Consequences

- The Go and Rust question is answered per driver. The repository does not unify languages.
- A conformance kit is a program that speaks the port's contract, so one kit covers drivers in every language.
