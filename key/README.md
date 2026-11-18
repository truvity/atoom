# key

The key port of atoom: a service that holds private keys and signs, agrees and attests for a caller, and never lets a key leave. The contract is the proto package `atoom.key.v1alpha1` (`KeyStoreService`, `UnlockService`), with its Go and ConnectRPC stubs.

| | |
| --- | --- |
| Go module | `github.com/truvity/atoom/key` (the contract and its stubs; it requires the ConnectRPC and Protobuf runtimes and nothing else) |
| Contract | [proto/atoom/key/v1alpha1/keystore.proto](proto/atoom/key/v1alpha1/keystore.proto), [unlock.proto](proto/atoom/key/v1alpha1/unlock.proto) |
| Stubs | `github.com/truvity/atoom/key/gen/atoom/key/v1alpha1` and `.../keyv1alpha1connect` (committed; `just gen` makes them) |
| Documentation | [docs/contract.md](docs/contract.md) (the rules of the contract), [docs/key-stores.md](docs/key-stores.md), [docs/algorithms.md](docs/algorithms.md), [docs/research](docs/research) |
| Where it came from | [PROVENANCE.md](PROVENANCE.md) |

## The libraries of the port

Each is its own Go module with its own README and documentation; a consumer that takes `key` and `key/software` has no PC/SC code in its module graph.

| Directory | What it is | Needs |
| --- | --- | --- |
| [conformance](conformance/README.md) | the conformance kit: the cases every key driver must pass, through its ConnectRPC endpoint; the command `key-conformance --socket` | the port |
| [software](software/README.md) | driver: P-384 keys held in files; library and a thin main | the port, the kit (tests only) |
| [yubikeypiv](yubikeypiv/README.md) | driver: the keys of a YubiKey 5 over PIV; library and a thin main; the C code is behind the build tag `atoom_card` | the port, the kit (tests only), `slotplan`, `pinrelease` |
| [slotplan](slotplan/README.md) | the check of a card slot map: which slot each purpose lives in | nothing |
| [pinrelease](pinrelease/README.md) | the wire format of the PIN release between boards: signed texts and a sealed PIN | nothing |

`key/awskms`, the driver for AWS KMS, is a later step of the plan ([docs/plan.md](../docs/plan.md), step 2b).

## A driver of your own

Serve `keyv1alpha1connect.KeyStoreServiceHandler` (Go) or the generated service trait (Rust), say what you offer in `Capabilities`, and run `key-conformance --socket PATH` against your endpoint. You need nothing from this repository but the contract and the kit. See [docs/contract.md](docs/contract.md).

Status: v1alpha1. A field may change until the contract becomes v1beta1; a breaking change raises the minor version of the repository.
