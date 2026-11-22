# How the key port maps to AWS KMS

The driver ([README](../README.md)) is a thin mapping of the [key contract](../../docs/key-port.md) onto the KMS API. This page says the mapping, the layout of keys in an AWS account that a host such as sluis asks of it, and what the driver does not do. The research behind the choices is in [research/aws-kms-as-a-key-store-2026-11-19.md](research/aws-kms-as-a-key-store-2026-11-19.md).

## The mapping

| Contract | KMS | Notes |
| --- | --- | --- |
| a purpose | one KMS key, found by `alias/<prefix>/<purpose>` or by the identifier in `Options.KeyIDs` | the purposes are the host's: `Options.Purposes` (or `--purposes FILE`) declares each name with its algorithm and operations, and no other name is served. A KMS key has no PIN, so a purpose named `pin-release` is only a name here and the PIN release calls answer `unimplemented` |
| `Ensure` | `DescribeKey` on the alias; when absent and `Options.CreateKeys` is on: `CreateKey` (with tags), `CreateAlias`; then `GetPublicKey` | `created` is true only for the call that made the key; `created_at` is the key's `CreationDate` |
| `Sign` | `Sign` with `MessageType: DIGEST` | the digest length is checked first (48 octets for `ES384`, 32 for `ES256` and `RS256`) |
| `PublicKey` | `GetPublicKey` | SubjectPublicKeyInfo in DER, as the contract says; kept after the first read |
| `Ecdh` | `DeriveSharedSecret` with `ECDH` on a `KEY_AGREEMENT` key | the raw secret; the peer's SEC1 point is checked here and sent as SubjectPublicKeyInfo |
| `Capabilities` | none | `store: aws-kms`, the four algorithms, the declared purposes, `attests: false` |
| `Attest`, `VerifyAttestation`, `BeginPinRelease`, `SealPin`, `FinishPinRelease` | none | `unimplemented`; not declared in `Capabilities` |

| Algorithm | KMS key spec | Key usage | Signing algorithm | Purposes |
| --- | --- | --- | --- | --- |
| `ES384` | `ECC_NIST_P384` | `SIGN_VERIFY` | `ECDSA_SHA_384` | any purpose declared `ES384` |
| `ES256` | `ECC_NIST_P256` | `SIGN_VERIFY` | `ECDSA_SHA_256` | any purpose declared `ES256` |
| `RS256` | `RSA_2048` | `SIGN_VERIFY` | `RSASSA_PKCS1_V1_5_SHA_256` | any purpose declared `RS256` |
| `ECDH-P384` | `ECC_NIST_P384` | `KEY_AGREEMENT` | (none) | any purpose declared `ECDH-P384` |

An ECDSA signature from KMS is ASN.1 DER and an RSA signature is the octet string of RFC 8017, which is what the contract says; the driver passes them through (a JWS wants the raw `r||s` of an ECDSA signature: [DER and raw](../../docs/algorithms.md#der-and-raw-signatures)).

## Finding and making a key

- **The alias is the name, the tags are the label.** A key that the driver makes carries `atoom:purpose`, `atoom:algorithm` and `atoom:managed-by`. The alias finds it; a person reading the console, or a policy that conditions on a tag, can tell what it is for.
- **The key is read once and then named by its ARN.** An alias can be moved to another key by anyone who may call `UpdateAlias`, and a policy that names an alias matches only a request that names it too. The driver therefore resolves the alias when it first needs the key and signs by ARN afterwards. A rotation under an alias takes a restart (a new KMS key under the same alias is a new public key, and the caller must learn it; a host such as sluis does it through a new signed key plan).
- **Making a key is off by default.** `Options.CreateKeys` lets `Ensure` call `CreateKey`. Where the deployment makes the keys (Terraform, CloudFormation) and holds the key policy, leave it off: `Ensure` then finds the key, returns its public key, and answers `not_found` (`absent`) for a key that is not there. A key that `Options.KeyIDs` names is always the deployment's: the driver never makes one for it.
- **The alias is the lock.** `CreateAlias` refuses a name that is taken (`AlreadyExistsException`). Two drivers that make the key of a purpose at once each make a KMS key; one names it, the other finds that its alias is taken, schedules its own key for deletion (7 days, the shortest KMS allows) and answers the winner's key. A process that dies between `CreateKey` and `CreateAlias` leaves a key with no alias; nothing reaches it, and it costs its monthly price until someone deletes it. The driver makes no sweep for these.
- **KMS is eventually consistent.** The calls that follow `CreateKey` (`CreateAlias`, `GetPublicKey`) answer `NotFoundException` for a moment on a key that exists; the driver waits and asks again, up to five times in under a second.

## Refusals

| Reason | Code | When |
| --- | --- | --- |
| `purpose`, `algorithm`, `digest`, `peer_public` | as the contract | checked before KMS is called, so a bad request costs no KMS request |
| `absent` | `not_found` | the key of the purpose is not there, and the driver may not make it |
| `algorithm` | `failed_precondition` | a key under the alias has another spec or usage, or was not born in KMS (`Origin` is external): never replaced |
| `store` | `failed_precondition` | the key is disabled or pending deletion, or KMS says `DisabledException`, `KMSInvalidStateException`, `KeyUnavailableException`, `InvalidKeyUsageException` |
| `store` | `unavailable` | KMS throttles or times out (`ThrottlingException`, `LimitExceededException`, `DependencyTimeoutException`, `KMSInternalException`), or the network fails |
| `store` | `internal` | anything else, an access denial included; the journal has KMS's code and text, the caller has the word |

## The layout a host such as sluis uses

The driver does not fix how many keys an account holds. A host decides that. The driver holds one key for each purpose that its host declares. sluis (an identity product, the first consumer; [its decision on keys](https://github.com/truvity/sluis/blob/main/docs/decisions/0006-keys-and-sealing.md)) asks for **one symmetric key and five asymmetric keys** in a boundary: four P-384 keys (token signing, the X.509 intermediate, the SSH user CA, the SSH host CA) and the audit log's P-256 key. Each further trust domain adds a P-384 key and each further audit writer a P-256 key. This driver serves the **five asymmetric keys** as the purposes `token-signing`, `x509-intermediate`, `ssh-user`, `ssh-host` and `audit-log`. The backup recovery key (a P-384 key-agreement key in an anchor account) is the purpose `wrapping`.

A host that holds keys this way wants these properties of them. The driver leaves each to the deployment, because they are policy and not mapping:

- **The key policy names the role of the process that runs the driver alone** (on Lambda, the role of the one function that may reach KMS). `kms:Sign` takes no encryption context, so an asymmetric key is protected by its policy, and every other part of the host reaches a key through the driver.
- **The control-plane actions on the alias** (`kms:CreateAlias`, `kms:UpdateAlias`, `kms:DeleteAlias`) are held to named key administrators, with no delegation to IAM through the account root, because whoever can retarget an alias sends new writes to a key of their choice. A host can name the ARN of each key (`Options.KeyIDs`) and refuse to start on another, which has the same effect.
- **A tenant's keys in a customer account** are used cross-account through the key policy or a grant that covers exactly `Sign` and `GetPublicKey` (and `DeriveSharedSecret`), by key ARN: name each such key in `Options.KeyIDs`. Revocation by the customer makes the calls answer `unavailable` or `failed_precondition` for that key and no other.
- **KMS automatic rotation** applies to the symmetric key, which this driver does not touch. An asymmetric key rotates by a new key.

## What is not here

- **Data keys.** A host may make a data key with `GenerateDataKey` under one symmetric key, with an encryption context. The key port has no call for it (a store "never lets a key leave", and a data key is a key that does), so `GenerateDataKey` is not used and a data key is made and wrapped by the host (in atoom, the `seal` block), not by a driver. If a consumer needs it in this driver, it is a new call of the port (an additive change, declared in `Capabilities`), decided with its consumer.
- **Attestation.** KMS gives no statement of where a key was born. What a reader can check instead is the key's description, its policy and the record of its creation ([what a store can prove](../../docs/what-a-store-can-prove.md)).
- **Import, export, multi-Region keys, custom key stores.** A key is born in KMS and stays there; a key whose origin is not KMS is refused.
