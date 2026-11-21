# 0014. What atoom proves, and what each consumer proves

Status: accepted, 2026-11-19

## Context

atoom's components are low level. A consumer builds a product on them: a token with claims, an OpenID flow, a session cookie, an audit log. If atoom tried to test those products it would depend on them, and if a consumer tested nothing of its own it would depend on atoom's tests for things atoom cannot see.

## Decision

- **atoom proves the contract of each driver.** The test of a driver here is its port's conformance kit, run through the driver's socket, and the driver's own integration tests against its real backend (or a faithful fake where there is none). The guard of the socket has its own tests ([socket guard](../socket-guard.md)). A block is proved by its kit over the memory drivers of its dependencies ([0008](0008-blocks.md)).
- **atoom proves nothing about a product built on top.** A token's claims, an OpenID flow, sluis's call graph, a session's logout, an audit log's verification: each is the conformance of the product that builds it, and it runs in that product's CI.
- **Each time a consumer takes a new atoom** (a new pin, then a new release), it runs its own conformance again. The order of a bump stays atoom first, then the consumers ([0015](0015-versioning-and-consumption.md)).
- **What a kit does not cover is said.** A kit skips a case for a capability the driver does not declare, and says so in its output; a run that only skips fails. A driver's documentation says where its behaviour is stricter than the contract.

## Consequences

- A green atoom CI means: every driver follows its contract and every block follows its own. It does not mean that a product built on them works.
- A consumer's conformance is the trigger to run again when it moves to a new atoom. Nothing in atoom runs it for the consumer.
- A bug found by a product's conformance in a driver becomes a case in the port's kit, so that every driver is held to it.

## Roads not taken

- **Product conformance in atoom.** atoom would depend on every product, and a product's tests would live far from its code.
- **No integration tests, kit only.** A kit speaks the contract; it cannot find what a backend does that the contract does not say. The integration tests do.
