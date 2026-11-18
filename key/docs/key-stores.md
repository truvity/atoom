# Key stores

*Copied from [truvity/sluis](https://github.com/truvity/sluis) `main` at commit `3f0afea8773b3d85205a5fb7d521282bbdcff598`, file `docs/concepts/keys.md` (the sections Key stores and Algorithms). The text stays there as it is (decision records are append-only); sluis links here for the card behaviour. Names such as `key@` are sluis's key service, the first consumer of this driver; the links that point into sluis's own pages are not part of this repository.*

## Key stores

The key store is the choice of key@'s driver, which is private to key@. There are three:

| Mode | Driver | Where it is used |
|---|---|---|
| **AWS KMS** | `aws-kms` | [lambda](/runtimes/#lambda), [kubernetes](/runtimes/#kubernetes) and other AWS installations |
| **YubiKey PIV** | `yubikey-piv` | [boards](/runtimes/#boards) and other installations on single-board computers: one card per board |
| **Software** | `software` | [local](/runtimes/#local), CI, and development |

The local installation uses software keys and needs no hardware. A dedicated development card is opt-in: only key@ reaches it, through the host's smart-card service. The floor is in code: the software driver refuses to start when the environment is `production`, and configuration chooses only among the modes the floor allows, so a development configuration loaded on the wrong board cannot make a software key a production authority key ([ADR 0006](/decisions/0006-keys-and-sealing/#floors-in-code)). On Kubernetes the keys are AWS KMS keys; the workload tokens a pod presents are the cluster's projected service-account tokens, which the cluster signs and sluis neither holds nor signs.

PKCS#11 drivers (YubiHSM 2, Nitrokey HSM 2), more drivers and post-quantum keys come later ([#1029](https://github.com/truvity/sluis/issues/1029)).


## Algorithms

ES384 (ECDSA on P-384) is the default for everything sluis signs, the checkpoints of the issuance log included. The checkpoints of the audit log are the one exception: a P-256 key signing SHA-256, the standard C2SP signature, so that a generic witness verifies them. sluis supports the FAPI 2.0 set, ES256, PS256 and EdDSA, as the algorithms of that profile ([ADR 0038](/decisions/0038-fapi-2-profile/)). It has a short list of named exceptions, each with a key of its own that serves nothing else: cloud federation (ES256 for GCP, RS256 for Azure Entra), a per-audience RS256 key, NATS Ed25519 keys, the C2SP witness keys and the audit checkpoint key ([ADR 0006](/decisions/0006-keys-and-sealing/#named-exceptions)). Everything else stays within the FIPS-approved set, with no FIPS mode or certified build in 2.0 ([ADR 0006](/decisions/0006-keys-and-sealing/#algorithms)). Support depends on the driver: RSA signatures (RS256, and PS256, which is the same card operation and not measured) take about 0.17 seconds on a YubiKey 5 with firmware 5.8 (RS256 measured 2026-11-08), and Ed25519 in PIV needs firmware 5.7 or newer.

In Go, JOSE is `go-jose/v4` behind sluis's own `jose` package; Rust and TypeScript only verify ([Approved libraries](/reference/approved-libraries/#jose)). Tokens are standard JWS and JWT that any language verifies against the published key set.
