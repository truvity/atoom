# 0020. The token block signs through seal and is stateless; the rotation ring is a step of its own

Status: accepted, 2026-11-25 (owner decision on the pull request of step B8a; the owner's answers Q1 B, Q2 C, Q3 A and Q4 A, and D32 B on the release names). Partly supersedes [0008](0008-blocks.md): the row of `token` in its table of the first blocks, "JWT signing under a signing policy, JWKS, rotation", now reads "JWS signing by purpose, the key set, verification", and rotation is step B8b.

## Context

Record 0008 named the `token` block: from sluis's `signer`, depending on `seal`, with JWT signing, JWKS and rotation. Its two first consumers share less than that row says.

- **sluis's `signer`** is a Rust actor:
  - It issues ID tokens (`typ` `JWT`) and access tokens (`at+jwt`), ES384, with RS256 on a purpose of its own for an audience that needs it.
  - Its kid is the RFC 7638 thumbprint, which its standards page claims. Its key set carries an `x5c` chain when its CA certifies the key.
  - It signs through key@, which checks what it signs.
  - Its claims rules are about 900 lines: a closed set, issuers, audiences, lifetimes.
  - Its rotation is a ring of about 2,800 lines in its store: keys pre-published, active, retiring and dropped; one key per node; revocations; activation held until exporter@ has read the key back from every public destination.
- **session** is a Go service:
  - It signs the `private_key_jwt` client assertion (`JWT`) and the identity header (`session-identity+jwt`), ES256, directly on the key port.
  - Its kid is the key port's `key_id`, by its record 0019, which accepts the key ARN in the public key set.
  - Each purpose may be on its own key driver (a file, AWS KMS).
  - It reuses an identity header for 30 seconds, after its session read.
  - It has decided to publish the previous key of a purpose for a while after a rotation, before its first site that rotates.

What both need is the same envelope and the same key set: a compact JWS over a purpose's key, a JWK Set with a kid per key, and a check of a token against it. The claims, the reuse and the lifecycle of keys differ.

## Decision

- **The block signs through the `seal` block** (Q1 B, as 0008 says). `Sign` hands seal the JWS signing input; seal's host sees it with its rules, seal hashes it, and the key port signs the digest. The key stays behind the one process that holds the key driver, which for sluis is key@.
  - **`key/route`** is a client of the key port that routes each call by its purpose to a client of its own. It lets a host keep each purpose on a driver of its own and still give seal one `Keys` client; session needs this. Its `Capabilities` answers each client's purposes of those routed to it. The calls that name no purpose (attestation checks, the PIN release) are not routed.
- **The block is stateless** (Q2 C). Its key set holds:
  - the current key of each purpose it is asked for, read from seal;
  - the public keys the host hands in, each with a kid and an `x5c` chain or none (a previous key, a next key), after them.

  `Verify` checks a token against the same keys. The block keeps a purpose's public key between calls only as a cache. When the `key_id` of a signature is not the kept key's, it reads the key again and signs once more, so a token never carries a kid that is not its key's.
- **The rotation ring is step B8b.** It is designed against sluis's `SigningRing` and built before session's first site that rotates a signing key. It covers which keys a set holds besides the current ones, when each signs, how long each stays, revocations and keys per node. Until then each host keeps its own lifecycle and hands the keys in.
- **The kid rule is a host option for each purpose** (Q3 A):
  - `key_id` (the default): the key port's `key_id` as it is, as record 0016 says a consumer does;
  - `thumbprint`: the RFC 7638 thumbprint, base64url without padding.
- **The token is a compact JWS of the block's form:**
  - the header is exactly `alg`, `kid` and `typ`;
  - the payload is the host's claims as their own bytes, after a check that they are one strict JSON object (UTF-8, no lone surrogate escape, no member twice, nothing after, at most 64 KiB);
  - the signature is raw `r||s` for ECDSA and the PKCS#1 v1.5 octets for RS256.

  The block verifies every signature before it answers.
- **A purpose declares the types it signs** (`typ`). The algorithm comes from seal.
- **The claims rules stay with each host.** The block never reads a claim. Its rules hook sees the operation, the purpose, the type, the claims and the authorization, for a host that decides there.
- **One implementation, in Go, with Rust stubs and a thin main** (Q4 A):
  - The crate `atoom-blocks-token` carries the generated client and service.
  - The thin main `cmd/token` serves the block on a UNIX socket, over a seal block on another socket or over one it hosts on two drivers. Its access rule is the socket guard.
  - sluis's `signer` checks its claims in Rust before it calls.
  - The main is released in the forms of a driver under names of its own, `atoom-block-token` (row `block-token` of the release table): executable, `.deb` and `.rpm` package, tarball and image (D32 B). Blocks are a kind of release row beside drivers, and the release checks know both.
- **What stays with each host:** the HTTP endpoint of the key set and its cache, the reuse of signed tokens, the claims rules, and, until B8b, the keys of a rotation.

## Consequences

- session can drop its own JWS code and sign through seal: one seal block with its signing purposes beside its seal purposes, and `key/route` in front of its drivers. Its kids do not change.
- sluis's `signer` can drop its key set and signature code and call the thin main. It keeps its ring and hands the ring's published keys in until B8b. Its kids do not change (`thumbprint`), and its `x5c` chains pass through.
- A key that changes behind the block, a restart-only rotation, costs one extra signature at the next `Sign`. The key set shows the new key at its next call.
- Each consumer adopts the block in a pull request of its own, after the release that carries it.
- The thin main is a released executable that is not a driver: the release table, its checks, the packages, the smoke and the candidate's evidence know a second kind, the block, named `atoom-block-<name>`.

## Roads not taken

| Road | Why not |
| --- | --- |
| Sign directly on the key port | sluis's signer would reach the card driver and lose key@'s check of what it signs; and it is not what 0008 says (Q1 A) |
| The rotation ring now, over the store port | About 2,800 lines of sluis's ring are coupled to its nodes, revocations and exporter reports, and session's need is smaller and later; one design against both, after the stateless block, is cheaper (Q2 B) |
| One kid rule for everyone | The thumbprint would change session's kids and hide the ARN that its record 0019 accepted; the `key_id` would break sluis's RFC 7638 claim (Q3 B, Q3 C) |
| The block re-encodes the claims | Two encoders would make two byte strings of one set of claims; signing the bytes the host checked keeps one |
| The general and flattened JWS serialisations | Neither consumer issues them as tokens; sluis's signed config is read by `policy` |
| A key set cache with a time to live in the block | A stale key set after a restart-only rotation; the key is read again for every `KeySet`, and the host's HTTP cache is the cache of the published document |
| The thin main as a driver package, `atoom-driver-blocks-token` | It is not a driver; a name of its own tells a consumer what it installs |
| sluis's `signer` rewritten in Go | sluis's choice (its record 0012 leaves it open); the thin main serves either way (Q4 B) |
