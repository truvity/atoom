# Algorithms and what each is for

*Copied from [truvity/sluis](https://github.com/truvity/sluis) `main` at commit `3f0afea8773b3d85205a5fb7d521282bbdcff598`, file `docs/reference/keys.md` (the sections Algorithms and Purposes per algorithm). The text stays there as it is (decision records are append-only); sluis links here for the card behaviour. Names such as `key@` are sluis's key service, the first consumer of this driver; the links that point into sluis's own pages are not part of this repository.*

## Algorithms

The FIPS-approved set is the default; an exception says why ([algorithms](/decisions/0006-keys-and-sealing/#algorithms), [named exceptions](/decisions/0006-keys-and-sealing/#named-exceptions)).

| Algorithm | Family | Curve | Status | Purposes | Note |
|---|---|---|---|---|---|
| [`ES384`](#es384) | signature | P-384 | default | [`token-signing`](#token-signing), [`x509-intermediate`](#x509-intermediate), [`ssh-user`](#ssh-user), [`ssh-host`](#ssh-host), [`issuance-log`](#issuance-log), [`release-signing`](#release-signing), [`workload-issuer`](#workload-issuer), [`pin-release`](#pin-release) | ECDSA over P-384 with SHA-384, the default of everything sluis signs for itself, the checkpoints of the issuance log included. The audit checkpoints are the one exception (ES256). |
| [`ECDH-P-384`](#ecdh-p-384) | key-agreement | P-384 | default | [`enrolment`](#enrolment) | ECDH over P-384 with HKDF-SHA384 and AES key wrap; the wrap of a data key on a board, the wrap of a backup archive key for each board and for the backup recovery key, and the HPKE enrolment key. |
| [`AES-256-GCM`](#aes-256-gcm) | aead | — | default | [`wrapping`](#wrapping), [`backup-archive`](#backup-archive) | The nonce comes from the cryptographic library. On AWS the symmetric key of the cloud key service; in software the data keys and the encryption of a backup archive. |
| `HMAC-SHA384` | mac | — | default | — | The keyed hash behind key's Mac: pseudonyms, link ids and the hashed index entries of sealed kinds. |
| [`PS256`](#ps256) | signature | — | exception | [`token-signing-ps256`](#token-signing-ps256) | FAPI 2.0. A dedicated key; PS256 on a card was not measured; it is the same RSA operation as RS256, which takes about 0.17 s on a YubiKey 5 with firmware 5.8 (measured 2026-11-08), so expect about that. |
| [`ES256`](#es256) | signature | P-256 | exception | [`audit-log`](#audit-log), [`audit-log-second`](#audit-log-second), [`token-signing-es256`](#token-signing-es256) | FAPI 2.0, cloud federation for GCP and the checkpoints of the audit logs (ECDSA over P-256 with SHA-256, the C2SP signature type 0x02 that generic witnesses verify). A dedicated key. |
| [`EdDSA`](#eddsa) | signature | Ed25519 | exception | [`witness`](#witness), [`token-signing-eddsa`](#token-signing-eddsa), [`nats`](#nats) | FAPI 2.0, the NATS keys (nkeys fix Ed25519) and the C2SP witness keys. A dedicated key per purpose; Ed25519 in PIV needs firmware 5.7. |
| [`RS256`](#rs256) | signature | — | exception | [`token-signing-rs256`](#token-signing-rs256) | RSA-2048: an audience configured to need RS256, and cloud federation for Azure Entra. A dedicated key; published beside the EC keys. |
| `ML-DSA-44` | signature | — | exception | — | A witness format that the C2SP tlog-cosignature specification allows; a witness key authorises nothing. |


## Purposes per algorithm

One key per purpose and algorithm, never shared across purposes ([key purposes](/decisions/0006-keys-and-sealing/#key-purposes)).

### ES384

ECDSA over P-384 with SHA-384, the default of everything sluis signs for itself, the checkpoints of the issuance log included. The audit checkpoints are the one exception (ES256).

| Purpose | Kind | Ops | AWS key | Board | Used by | Notes |
|---|---|---|---|---|---|---|
| <a id="token-signing"></a>`token-signing` | signing | `sign`, `public-key` | `ECC_NIST_P384`, one per boundary | slot `9a` | [`signer`](/reference/actors/#signer) | Signs tokens. Its certificate is a non-CA certificate that allows digital signatures only, issued by the node's intermediate when the installation turns the token certificate on (off in the core, decision X1 A); the published key set then carries the x5c chain. Never the intermediate itself. |
| <a id="x509-intermediate"></a>`x509-intermediate` | signing | `sign`, `public-key`, `certify` | `ECC_NIST_P384`, one per trust domain | slot `9c` | [`ca`](/reference/actors/#ca) | The X.509 intermediate of a trust domain, certified by a root that root custody holds; sluis holds intermediates only. The core needs none: the key is generated only when the PKI add-on turns the token certificate on (decision X1 A of 2026-11-18). |
| <a id="ssh-user"></a>`ssh-user` | signing | `sign`, `public-key` | `ECC_NIST_P384`, one per boundary | slot `82` | [`ssh-ca`](/reference/actors/#ssh-ca) | The SSH user certificate authority. |
| <a id="ssh-host"></a>`ssh-host` | signing | `sign`, `public-key` | `ECC_NIST_P384`, one per boundary | slot `83` | [`ssh-ca`](/reference/actors/#ssh-ca) | The SSH host certificate authority. |
| <a id="issuance-log"></a>`issuance-log` | signing | `sign`, `public-key` | `ECC_NIST_P384`, one per boundary | slot `89` | [`writer`](/reference/actors/#writer) | Signs the checkpoints of the issuance log, a third log kind of the audit writer; ca emits the entries. |
| <a id="release-signing"></a>`release-signing` | signing | `sign`, `public-key` | `ECC_NIST_P384`, one per boundary | slot `85` | [`signer`](/reference/actors/#signer) | The OpenPGP release subkey, reached through key; in the release boundary only. The primary's certification of the subkey is the kind OpenPgpBinding. |
| <a id="workload-issuer"></a>`workload-issuer` | signing | `sign`, `public-key` | `ECC_NIST_P384`, one per boundary | slot `86`; on bare-metal | [`sluis-node`](/reference/actors/#sluis-node) | Boards only. Signs the workload tokens of the node's actors, through key; never shared with token-signing, because its callers and its blast radius differ. |
| <a id="pin-release"></a>`pin-release` | signing | `sign`, `public-key` | `ECC_NIST_P384`, one per boundary | slot `87`; on bare-metal | [`key`](/reference/actors/#key) | Boards only. The locked key signs its PinReleaseRequest with it, and the unlocked peer's key signs its PinRelease answer. The slot is generated with PIN policy never and touch policy never, so it signs while the board is locked, and the KeyPlan's slot map records its public key and attestation. Each side verifies the other's signature with the key of the slot map, after its own card driver verified the slot's attestation, before the PIN is sealed or opened. Used for nothing else. |

### ECDH-P-384

ECDH over P-384 with HKDF-SHA384 and AES key wrap; the wrap of a data key on a board, the wrap of a backup archive key for each board and for the backup recovery key, and the HPKE enrolment key.

| Purpose | Kind | Ops | AWS key | Board | Used by | Notes |
|---|---|---|---|---|---|---|
| <a id="enrolment"></a>`enrolment` | key-agreement | `open`, `public-key` | `ECC_NIST_P384`, one per boundary | slot `8a` | [`exchange`](/reference/actors/#exchange) | The HPKE key that a host's enrolment request is sealed to. |

### AES-256-GCM

The nonce comes from the cryptographic library. On AWS the symmetric key of the cloud key service; in software the data keys and the encryption of a backup archive.

| Purpose | Kind | Ops | AWS key | Board | Used by | Notes |
|---|---|---|---|---|---|---|
| <a id="wrapping"></a>`wrapping` | wrapping | `wrap`, `unwrap`, `public-key` | `SYMMETRIC_DEFAULT`, one per boundary | slot `9d`; algorithm `ECDH-P-384` | [`key`](/reference/actors/#key) | Protects the data keys. On AWS the boundary's one symmetric key, with the encryption context {boundary, tenant, purpose}. On a board the key-agreement key in slot 9d; a data key is wrapped for it and for the backup recovery key. Grants use the boundary data key `grant`, not this key. |
| <a id="backup-archive"></a>`backup-archive` | wrapping | `wrap`, `unwrap` | `SYMMETRIC_DEFAULT`, one per boundary | no slot; algorithm `ECDH-P-384` | [`exporter`](/reference/actors/#exporter), [`store`](/reference/actors/#store) | The key of a backup archive: the tar is encrypted as a whole with AES-256-GCM under a key made for that archive, enabled on use. On AWS key calls GenerateDataKey for exporter under a dedicated KMS key that may live in another account and whose policy lets only the restore role decrypt; exporter encrypts the tar and writes the wrapped copy in the archive header, and the restore of store opens it. On a board exporter wraps the archive key by ECDH P-384 with public keys only, for the key-agreement keys named in the plan and for the backup recovery key; the restore asks key for the card's ECDH. No card slot of its own. |

### PS256

FAPI 2.0. A dedicated key; PS256 on a card was not measured; it is the same RSA operation as RS256, which takes about 0.17 s on a YubiKey 5 with firmware 5.8 (measured 2026-11-08), so expect about that.

| Purpose | Kind | Ops | AWS key | Board | Used by | Notes |
|---|---|---|---|---|---|---|
| <a id="token-signing-ps256"></a>`token-signing-ps256` | signing | `sign`, `public-key` | `RSA_2048`, one per boundary | slot `8b` | [`signer`](/reference/actors/#signer) | Tokens for a client that declares the FAPI 2.0 profile with PS256. |

### ES256

FAPI 2.0, cloud federation for GCP and the checkpoints of the audit logs (ECDSA over P-256 with SHA-256, the C2SP signature type 0x02 that generic witnesses verify). A dedicated key.

| Purpose | Kind | Ops | AWS key | Board | Used by | Notes |
|---|---|---|---|---|---|---|
| <a id="audit-log"></a>`audit-log` | signing | `sign`, `public-key` | `ECC_NIST_P256`, one per writer instance | slot `84` | [`writer`](/reference/actors/#writer) | Signs the checkpoints of a project's audit log and usage log with ECDSA over P-256 and SHA-256 (KMS ECDSA_SHA_256), the standard C2SP signed note, so that a generic witness verifies it. One key per writer instance (one per project, and per node on boards); only that writer may use it. |
| <a id="audit-log-second"></a>`audit-log-second` | signing | `sign`, `public-key` | `ECC_NIST_P256`, one per writer instance | slot `8f`; on bare-metal | [`writer`](/reference/actors/#writer) | Boards only. The log key of a second audit writer instance on a node's card, which holds the first one's key in slot 84 as `audit-log`: P-256, signing the checkpoints of that instance's audit log and usage log like `audit-log`, and used only by that instance. On AWS a second writer instance has a key of its own under `audit-log` and needs no purpose of this name. A third instance has no pinned slot: it is named in the KeyPlan with a slot of the rotation spares. |
| <a id="token-signing-es256"></a>`token-signing-es256` | signing | `sign`, `public-key` | `ECC_NIST_P256`, one per boundary | slot `8c` | [`signer`](/reference/actors/#signer) | Tokens for FAPI 2.0 with ES256, and workload tokens for a GCP federation. On a board the key of slot 8c is the FAPI key; the key of a GCP federation is a key of the key service and never a card key. |

### EdDSA

FAPI 2.0, the NATS keys (nkeys fix Ed25519) and the C2SP witness keys. A dedicated key per purpose; Ed25519 in PIV needs firmware 5.7.

| Purpose | Kind | Ops | AWS key | Board | Used by | Notes |
|---|---|---|---|---|---|---|
| <a id="witness"></a>`witness` | signing | `sign`, `public-key` | `ECC_NIST_EDWARDS25519`, one per boundary | slot `8e` | [`witness`](/reference/actors/#witness) | The key of the witness, in the format the cosignature specification requires (Ed25519, or ML-DSA-44 where the witness uses that). The witness signs its cosignatures through key, like every other signer; on a board the Ed25519 key sits in slot 8e. |
| <a id="token-signing-eddsa"></a>`token-signing-eddsa` | signing | `sign`, `public-key` | `ECC_NIST_EDWARDS25519`, one per boundary | slot `8d` | [`signer`](/reference/actors/#signer) | Tokens for FAPI 2.0 with EdDSA. On a board an Ed25519 key in slot 8d, which needs firmware 5.7. |
| <a id="nats"></a>`nats` | signing | `sign`, `public-key` | `ECC_NIST_EDWARDS25519`, one per boundary | no slot | `adapter-<vendor>` | The NATS operator, signing and account keys: one Ed25519 key per purpose, used only by the adapter that mints NATS credentials. Never a card key: the keys of NATS live in the key service, outside the board installation. |

### RS256

RSA-2048: an audience configured to need RS256, and cloud federation for Azure Entra. A dedicated key; published beside the EC keys.

| Purpose | Kind | Ops | AWS key | Board | Used by | Notes |
|---|---|---|---|---|---|---|
| <a id="token-signing-rs256"></a>`token-signing-rs256` | signing | `sign`, `public-key` | `RSA_2048`, one per boundary | slot `88` | [`signer`](/reference/actors/#signer) | The ID tokens of an audience that declares `signing_alg: RS256` (its access tokens stay ES384), and workload tokens for an Azure Entra federation. The audience, not the caller, decides. The key set carries no `x5c` for it. On a board the key is an RSA-2048 key in slot 88 (a fixed row of the default map; it serves the audiences, and the key of an Azure federation is a key of the key service, never a card key), PIN once and touch never, born on the card and attested like the others. One private-key operation of the card takes about 0.17 s and holds the card meanwhile (measured 2026-11-08 on a YubiKey 5 with firmware 5.8: median 173 ms, an ES384 signature 148 ms), so the card driver admits four at once (the burst), signs them one at a time and refuses the rest as busy (a call of another key waits for about one to two RSA signatures, up to 0.47 s in one sample; the bound is the four admitted, about 0.7 s), and key@ limits the purpose more tightly on a card than on a software store: one bucket for the purpose, a burst of 4 and 2 signatures a second (a third of the card), shared by every RS256 audience of the board, so a flood of sign-ins to one RS256 audience also refuses the ID tokens of the others (ES384 audiences are not affected). Both refusals reach signer as a failed call, and signer answers its caller with a refusal that it audits as `signer.refused`; key@ audits its own limit refusal (reason `rate`), and a `busy` of the driver is a counted refusal of key@ with the reason `busy` (and a line of the driver's journal once a minute with a count). A key in slot 88 that is not the plan's leaves the purpose absent and does not stop the driver. The software driver holds the key in a file and ignores the slot. Discovery and the key set are built from the ring, and signer reads the KeyPlan: for up to the cache of the key set (5 s), and for the hour of grace after a KeyPlan stops listing the purpose, discovery may list RS256 while signer already refuses it. That is the safe direction (a relying party is told of a key that is there and gets a refusal, never a token of another algorithm). The first RS256 key of an installation that has served for a while, and the first of each further board, is published for the lead time before it signs, like any new key. signer keeps the time a KeyPlan has omitted the purpose in memory: a restart of signer begins its hour of grace again, and the key stays in the ring and the key set meanwhile, with nothing signing with it. |
