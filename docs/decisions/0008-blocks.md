# 0008. Blocks: contracts built from other ports

Status: accepted, 2026-11-20

## Context

A port abstracts one kind of backend, and its drivers vary by backend. sluis, session and ritueel also need parts built from several ports. key@ is one: it seals with data keys that the key port wraps and the store keeps. The policy loader is another, and so is the audit pipeline. Without a defined layer, each consumer would combine the ports in its own way.

## Decision

- **A block** is a contract (proto, generated code for each language that needs it, and documentation) with a **kit** that tests it. Its implementation calls **only other ports and blocks**, never a backend.
- **A block declares its dependencies**, and its host passes them in as clients. A block never looks one up. There are no cycles.
- **The host supplies the rules.** A block takes a rules hook for who may do what. sluis plugs in its registry rules and sealed grants; session and ritueel plug in their own. Tenants, boundaries and grants stay out of atoom.
- **One implementation per block, in the language of its main host.** A consumer in another language calls it as a process over ConnectRPC. Two implementations exist only where a kit or a shared test set keeps them in agreement (the Cedar loaders of [0011](0011-cedar-as-a-library.md)). (The audit emitter, which every process needs in Go and in Rust, is part of `truvity/audit`, not atoom.)
- **A block moves into atoom at its second consumer.** Until then it lives in the repository of its first consumer. session's core (session records, the sealed cookie, expiry, logout pointers, the exchange cache) starts in `truvity/session`.
- A block runs in-process by default, or as its own process, like a driver ([0009](0009-connectrpc-in-process.md)).

## The first blocks

| Block | From | Depends on | Language |
| --- | --- | --- | --- |
| `keyring` | sluis key@: data keys per purpose, wraps, `Seal`, `Open`, `Mac`, signing by purpose | `key` and `store` ports | Go |
| `state` | sluis store@: addresses, kinds, signed-config checks, layered reads, watch, the bindings per storage class | `store` and `source` ports | Rust |
| `token` | sluis signer: JWT signing under a signing policy, JWKS, rotation | `keyring` | Go |
| `export` | sluis exporter@ | `destination` port, `state` | Rust |

Audit is not a block of atoom. It is the product of `truvity/audit`, built on atoom's ports ([0013](0013-what-moves-from-sluis.md)).

## Consequences

- A block is tested by its kit over the memory drivers of its dependencies.
- A Go consumer of a Rust block pays a socket hop. On Lambda that means an extension.
