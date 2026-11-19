# AWS KMS as a key store: what it gives, what it limits, and the calls that follow (2026-11-19)

*Research for the driver `key/awskms` (step 2b of [docs/plan.md](../../../../docs/plan.md)). Read on 2026-11-19 in the AWS KMS API reference and developer guide pages named below (`Sign`, `DeriveSharedSecret`, `CreateAlias`, "Aliases in AWS KMS", "Request quotas"), and in the page `docs/learn/how-a-kms-key-works.md` of truvity/ritueel `main` (read 2026-11-10 there). No AWS account was used and no call was made to AWS; the driver is tested against a fake (below). Quotas and error behaviour change: check them before relying on them. UNVERIFIED marks what no page I read says.*

## Question

Can the key port ([contract](../../../docs/contract.md)) be served by AWS KMS, which operations of the contract map to which KMS calls, what does KMS limit, and what must the driver do that KMS does not?

## Answer in short

Yes, for four of the six things a key store does: `Ensure`, `Sign`, `PublicKey` and `Ecdh`. KMS cannot attest and has no PIN, so the card's three PIN-release calls and `Attest` are not offered. The mapping is in [layout.md](../layout.md). The choices that needed research follow.

## What KMS gives

- **The three signing shapes of the contract exist as KMS key specs.** `ECC_NIST_P384` with `ECDSA_SHA_384`, `ECC_NIST_P256` with `ECDSA_SHA_256`, `RSA_2048` with `RSASSA_PKCS1_V1_5_SHA_256`. `Sign` takes `MessageType: DIGEST`, which skips the hash, and the length of the digest must match the algorithm. An ECDSA signature is a DER object (ANSI X9.62, RFC 3279 section 2.2.3) and an RSA signature is encoded as RFC 8017 says: exactly what the contract specifies, so the driver passes both through. (`Sign` reference.)
- **Key agreement exists, since 2024.** `DeriveSharedSecret` needs an ECC key with `KeyUsage: KEY_AGREEMENT`, takes the peer's public key as DER SubjectPublicKeyInfo (up to 8192 bytes) and returns the **raw** shared secret, with no key-derivation function; the page recommends NIST SP 800-56C for deriving a key from it. That is the contract's `Ecdh` ("the raw ECDH shared secret"). The peer's key must be on the same curve; the contract gives SEC1 uncompressed P-384, so the driver validates the point (a point off the curve is the caller's `peer_public` refusal, before KMS) and wraps it in SubjectPublicKeyInfo. A `KEY_AGREEMENT` key cannot sign and a `SIGN_VERIFY` key cannot agree: a purpose is one or the other for its whole life. (`DeriveSharedSecret` reference.)
- **Names.** An alias is `alias/` plus letters, digits, `/`, `_` and `-` (256 characters at most in all), unique in an account and Region, may not begin `alias/aws/`, and is an independent resource that anyone who may call `UpdateAlias` can point at another key of the same type and usage. It is accepted as a key identifier in `Sign`, `GetPublicKey`, `DescribeKey` and `DeriveSharedSecret`. An alias is not secret (it shows in CloudTrail). (`CreateAlias` reference; "Aliases in AWS KMS".)
- **The alias is atomic.** `CreateAlias` answers `AlreadyExistsException` for a name that is taken. That makes it the lock for "create if absent" with several callers (below).
- **The key is read-once material.** The public half never changes, so it is cached. (A rotation is a new key: ritueel's page, "It cannot rotate by itself": automatic rotation exists only for symmetric keys.)

## What KMS limits

| Limit | What the driver does |
| --- | --- |
| **No attestation.** `GetPublicKey` returns no certificate and no statement of where the key was born (ritueel's page). | `Capabilities.attests` is false; `Attest` and `VerifyAttestation` answer `unimplemented`; the kit skips the cases that need them. A reader checks the key's description, its policy and the record of its creation instead ([what a store can prove](../../../docs/what-a-store-can-prove.md)). |
| **No PIN, no touch.** `Sign` has no field for either; the only proof of who is calling is the role session. | The PIN release is a card's: `BeginPinRelease`, `SealPin` and `FinishPinRelease` answer `unimplemented`. The purpose `pin-release` is not held. |
| **Quotas (default, per account and Region, adjustable).** Cryptographic operations on ECC keys (`Sign`, `Verify`, `DeriveSharedSecret`): 1,000 a second, shared. On RSA keys (`Sign`, `Verify`, ...): 1,000 a second, shared. `GetPublicKey` and `DescribeKey`: 2,000 a second each. `CreateKey` and `CreateAlias`: 5 a second. `TagResource`: 10. `ScheduleKeyDeletion`: 15. The same page also gives, as an example, a smaller figure for some Regions (300 for ECC and 500 for RSA in Singapore), so the table of one's own Region and Service Quotas decide. Throttling is counted over every principal of the account, services included, and a cross-account request counts against the caller. ("Request quotas".) | Public keys and key identifiers are read once, so a `Sign` costs one KMS request and a restart a few. A throttle is `unavailable` and the SDK's own retry runs first. The numbers are the host's to watch; the driver adds no limiter. |
| **Eventual consistency.** The KMS API follows an eventual consistency model (every page above says so). A key that `CreateKey` returned may be invisible to the next call for a moment. | The calls after a creation (`CreateAlias`, `GetPublicKey`) are retried on `NotFoundException`, five tries in about half a second. The fake can lag a call to test it; how long the real service lags is UNVERIFIED. |
| **Key spec, usage and origin cannot change** after a key is made (ritueel's page). | A key of another spec or usage under a purpose's alias is refused and never replaced; a purpose has one algorithm for life, as the contract says. |
| **A key in a state other than Enabled cannot sign.** Disabled: `DisabledException`. Pending deletion: `KMSInvalidStateException`. ritueel's page: a disabled key still answers `GetPublicKey`, a key pending deletion does not. | `failed_precondition` with the word `store`; the state is in the journal. The driver does not make a second key to replace one it cannot use. |
| **The key policy is the protection, and the driver cannot check it.** Whoever can write the policy, make grants, or move an alias can sign (ritueel's page). | The driver names the key by ARN after the first read; the policy, grants and alias permissions are the deployment's. [layout.md](../layout.md) says what sluis asks of them. |
| **A new key has a new public key.** | A rotation under an alias needs a restart of the driver and, in sluis, a new signed key plan. |

## Decisions

1. **The alias finds the key, tags label it.** The brief for this step said "alias/tag per purpose". An alias is unique and atomic, which a tag is not (a tag cannot be conditioned on a create: two callers could each tag a key and both succeed), so the alias is the name and the lock and the tags (`atoom:purpose`, `atoom:algorithm`, `atoom:managed-by`) say what the key is for. Tags need `kms:TagResource` on creation; a deployment that makes its own keys tags them its own way and the driver reads only the alias.
2. **Resolve once, sign by ARN.** An alias can be moved. A policy that names an alias matches only a request that names the alias too (ritueel's page), so a deployment that wants "only this one key" writes the key's ARN; the driver then must call by ARN. The cost is that a rotation takes a restart, which sluis wants anyway (its key plan names the ARN).
3. **Making keys is opt-in** (`Options.CreateKeys`). Session, the first consumer, may want `Ensure` to make keys; sluis's deployment makes them. Off by default, because a function whose role can call `CreateKey` can also make a key that no policy of the deployment has seen.
4. **No data keys in this driver.** The contract has no call for `GenerateDataKey`; sluis makes data keys in its `key@` actor under a symmetric key. See [layout.md](../layout.md), "What is not here".
5. **The purposes are the closed list of the other drivers, minus `pin-release`.** The purpose table is copied, not shared, because a driver requires no other driver; the cost is that a new purpose is added in each driver and the kit's cases catch a drift only for the reference purposes.
6. **Go** (coordinator's call R2 A). Session is a Go Lambda authorizer with no sidecars, so the driver is a Go library that links into the function; the thin main is for the day a sidecar is wanted.

## Roads not taken

- **A local KMS emulator in a container (for example `local-kms`).** Considered for the test of the driver. A fake that speaks the KMS JSON protocol in the test process, behind the real AWS SDK client, tests the same serialisation with no container and no network, so it also runs on the macOS jobs, where there is no container runtime. I did not check whether any emulator implements `DeriveSharedSecret` (UNVERIFIED); the fake does, with the real curve arithmetic.
- **Tag-only lookup** (`ListResourceTags` over all keys) was rejected: it scans every key in the account, and a tag is not unique.
- **A sweep for orphan keys** (a key made, then the process died before `CreateAlias`). Rejected for now: the orphan has no alias, no one reaches it, and it costs its monthly price. A sweep needs `kms:ListKeys` and `kms:ListResourceTags` and a rule for "old enough"; the tag `atoom:managed-by` makes one possible later.
- **Multi-Region keys.** A replica gives a second Region its own policy and aliases (ritueel's page), and the driver's single alias per Region already works with a replica per Region; replication itself is the deployment's.
- **A Rust twin** (sluis ADR 0012 rule R5 says AWS drivers are Rust). Not built: R2 chose Go for the Lambda shape; add one only if sluis's own Lambda shape needs it.

## What is not measured

Latency of `Sign` and of key creation, and how long the real service takes to show a new key everywhere: no call was made. The first real use (the session consumer, or a smoke run with a test account) should measure them and amend this page.
