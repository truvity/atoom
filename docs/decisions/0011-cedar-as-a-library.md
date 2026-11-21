# 0011. Cedar is used as a library; atoom shares how policies load and the tests

Status: accepted, 2026-11-20

## Context

sluis, session and ritueel decide with Cedar. Cedar is a library by design, with no loader and no network API:
- `cedar-policy` (Rust) is the reference;
- `cedar-go` is a separate native implementation, which lags in places (entity tags);
- Java and WebAssembly bindings wrap the Rust engine.

A ConnectRPC port would have restated Cedar's model in proto: 10 value kinds and about 8 messages. Every feature Cedar added later would then wait for the proto.

## Decision

- **Consumers use the Cedar library of their language directly:** `cedar-policy` in Rust, `cedar-go` in Go. This is the named exception to [0009](0009-connectrpc-in-process.md). There is no policy engine port.
- **atoom shares four things around the engine:**
  1. **The policy bundle:** schema, policies, templates and links in **one signed document**, read through the `source` port.
  2. **The loading rules,** written once:
     - verify the signature before parsing;
     - validate against the schema in strict mode;
     - compile once and swap atomically;
     - keep the last good set when a reload fails;
     - refuse a cold start with no valid set;
     - refresh on watch or by ETag, with a minimum interval;
     - no I/O during a decision;
     - audit each decision with the digest of the set it used.
  3. **A loader library in Go and in Rust** that follows those rules over `source` and the local engine.
  4. **A decision test set:** policies, entities, requests and expected decisions, run in CI against both engines. A difference fails the build, or is listed as a known gap of one engine.
- **The caller gives the entities.** The engine fetches nothing. A consumer gathers the entities one request touches (the principal, the resource, their ancestors), as Cedar and AWS Verified Permissions expect.
- A different engine (OPA, Casbin) is a separate design, not a second implementation of this one.

## Consequences

- sluis keeps its layers, grants and the policy actor. session decides in-process on every request, with no hop.
- Verified Permissions can come later as a mode of the loader, if a consumer needs it.
