# key

The key port of atoom: a service that holds private keys and signs, agrees and attests for a caller, and never lets a key leave. The contract is the proto package `atoom.key.v1alpha1` (`KeyStoreService`, `UnlockService`), with its Go and ConnectRPC stubs.

| | |
| --- | --- |
| Go module | `github.com/truvity/atoom/key` (the contract, its stubs and five small packages: `purpose`, the host's declaration of purposes, `signature`, the DER to raw conversion of a JWS, `keyid`, the `key_id` of a key, `route`, a client that sends each call to the driver of its purpose, and `signer`, a `crypto.Signer` over a client of the port; it requires the ConnectRPC and Protobuf runtimes and nothing else) |
| Contract | [proto/atoom/key/v1alpha1/keystore.proto](proto/atoom/key/v1alpha1/keystore.proto), [unlock.proto](proto/atoom/key/v1alpha1/unlock.proto) |
| Purposes | any purpose a host declares: a name, an algorithm (`ES256`, `ES384`, `RS256`, `ECDH-P384`) and the operations allowed, as `purpose.Declaration` or the JSON file of a thin main's `--purposes`; the contract holds no list ([the declaration](docs/key-port.md#the-declaration-of-purposes)) |
| Signatures | `Sign` takes a digest only (a wrong length is refused) and returns the plain signature, ASN.1 DER for ECDSA, with the `key_id` of the key that signed ([record 0016](../docs/decisions/0016-the-key-port-signs-digests.md)); `signature.DERToRaw` makes the raw `r||s` of a JWS ([DER and raw](docs/algorithms.md#der-and-raw-signatures)); `signer.New` makes a `crypto.Signer` of one purpose ([a crypto.Signer](docs/key-port.md#a-cryptosigner)) |
| Stubs | `github.com/truvity/atoom/key/gen/atoom/key/v1alpha1` and `.../keyv1alpha1connect` (committed; `just gen` makes them) |
| Documentation | [docs/key-port.md](docs/key-port.md) (the key port: the rules of the contract), [docs/key-stores.md](docs/key-stores.md), [docs/algorithms.md](docs/algorithms.md), [docs/research](docs/research) |
| Where it came from | [provenance](../docs/provenance.md#key) |

## The libraries of the port

Each is its own Go module with its own README and documentation; a consumer that takes `key` and `key/software` has no PC/SC code in its module graph.

| Directory | What it is | Needs |
| --- | --- | --- |
| [conformance](conformance/README.md) | the conformance kit: the cases every key driver must pass, through its ConnectRPC endpoint; the command `key-conformance --socket` | the port |
| [software](software/README.md) | driver: keys held in files (P-256, P-384, RSA-2048); library and a thin main | the port, the kit (tests only) |
| [yubikeypiv](yubikeypiv/README.md) | driver: the keys of a YubiKey 5 over PIV; library and a thin main; the C code is behind the build tag `atoom_card` | the port, the kit (tests only), `slotplan`, `pinrelease` |
| [awskms](awskms/README.md) | driver: asymmetric keys in AWS KMS, found by alias; library (links into a Lambda function) and a thin main; no attestation | the port, the kit (tests only), the AWS SDK |
| [slotplan](slotplan/README.md) | the check of a card slot map: which slot each purpose lives in, which the card driver uses to place the purposes of its host; its `Reference` is a reference map that holds the purposes of sluis, which no driver takes a slot from | nothing |
| [pinrelease](pinrelease/README.md) | the wire format of the PIN release between boards: signed texts and a sealed PIN | nothing |
| [usbccid](usbccid/README.md) | the USB layer of a card that speaks CCID over direct USB ([record 0026](../docs/decisions/0026-one-yubikey-driver-over-direct-usb.md)): discovery of the devices with a CCID interface from sysfs, one usbfs node, one claimed interface, bulk transfers with a timeout; Linux only, pure Go. Its package `usbccid/ccid` is the CCID reader on a `Port`: one command APDU in, one response APDU out, with a budget for each exchange and a lost-session rule; it builds on every system | `golang.org/x/sys`, no atoom module |

## A driver of your own

Serve `keyv1alpha1connect.KeyStoreServiceHandler` (Go) or the generated service trait (Rust), take your purposes from your host, say what you offer in `Capabilities` (the purposes included), start yourself with the declaration of the kit (`key-conformance --print-declaration`) and run `key-conformance --socket PATH` against your endpoint. You need nothing from this repository but the contract and the kit. See [docs/key-port.md](docs/key-port.md).

Status: v1alpha1. A field may change until the contract becomes v1beta1; a breaking change raises the minor version of the repository.
