# key/yubikeypiv

The YubiKey PIV key driver of atoom: the [key port](../README.md) over the keys of a YubiKey 5, through piv-go and pcsc-lite. The card holds the keys and they never leave it.

| | |
| --- | --- |
| Go module | `github.com/truvity/atoom/key/yubikeypiv` (requires the port, the common types, `key/slotplan`, `key/pinrelease`, piv-go, and the kit for its tests; no other driver) |
| Library | package `yubikeypiv`: `New(Options)` opens the card and returns a `Service` (`Handler()`, `Close()`); `Run(ctx, opts, host, admin)` serves the key socket through the host and the unlock service on the admin socket; `Check(opts)` |
| Thin main | `cmd/yubikeypiv`: `yubikeypiv serve`, `yubikeypiv check`, `yubikeypiv version` |
| C code | behind the build tag **`atoom_card`** (cgo, libpcsclite). Without the tag the driver builds everywhere with no C code, and refuses to open a card |
| Documentation | [docs/](docs/) (below) |
| Where it came from | [PROVENANCE.md](PROVENANCE.md) |

## What it does

- **Starts locked.** The driver holds the card exclusively and has no PIN until an administrator delivers it on the local admin socket (`UnlockService`); a key that needs the PIN is refused with the reason `locked` until then. A wrong PIN is tried once and never retried, and the driver does not send a PIN to a card with one attempt left.
- **Keeps one slot for each key.** The slot plan gives each purpose a PIV slot, a policy for PIN and touch, and an algorithm. A key is made on the card in development with the factory management key and in no other environment (a staging or production card is provisioned by its owners' ceremony). It verifies the attestation of every key it adopts against Yubico's roots, refuses a firmware below 5.7 (the EUCLEAK fix) and a card whose serial is not the one it was told, and refuses an imported key.
- **Resets the card's session** at start and exit, so that a card left PIN-verified by an earlier process is never trusted.
- **Takes part in the PIN release** between boards (`BeginPinRelease`, `SealPin`, `FinishPinRelease`) when its host gives it a way to check the other node's key (below).

## The host seam

The driver runs unchanged inside a larger system, which keeps what is its own and plugs in at three places:

| Place | What the host provides | atoom's thin main |
| --- | --- | --- |
| `Host.Serve` (and `Host.HandlerOptions`) | the server of the key socket: its listener, its peer-credential check, its interceptors | a UNIX socket, mode 0600 by default |
| `AdminConfig.Allow` | which peer uid may use the admin socket | one uid (`--admin-uid`, default root); the socket is mode 0600 as well |
| `Options.PeerKeys` | the check of the other side of a PIN release: it verifies the signed key plan that came with the call and returns the pin-release key it lists for the node | none: a main without it releases no PIN to a peer |

`PeerKeys` is an interface (`PeerKey(ctx, keyPlan, node) (*ecdsa.PublicKey, error)`): an error that wraps `ErrUnlisted` is the reason `peer`, any other is `key_plan`; the driver also refuses its own node, an empty one and a key that is not P-384 whatever the host says. What sluis puts behind it (the publisher's signature, the kind, the sequence of documents, the durable mark of the highest one) stays in sluis. The driver keeps what is the card's: the checks of the boundary and of the other side's signature, and the stop that a refused released PIN leaves in its state directory.

## Build tag and platforms

| Build | Without the tag | With `-tags atoom_card` |
| --- | --- | --- |
| Needs | nothing | `CGO_ENABLED=1`; Linux: `pkg-config` and `libpcsclite-dev`; macOS: the PC/SC framework (UNVERIFIED on the macOS runner until the CI job has run) |
| linux/amd64, linux/arm64, darwin/arm64 | builds and tests with the fake card | native build on each (nothing is cross-compiled) |
| Opens a card | no: `card.ErrNoCardSupport` | yes |

**The driver can be unlocked only on Linux.** It holds the PIN in locked memory (`mlock`, `MADV_DONTDUMP`) and reads the peer's uid off the admin socket (`SO_PEERCRED`); on another system it builds, starts and stays locked, and the tests that serve the unlock service skip there with that reason.

The tests use the fake card of `internal/card/cardtest` (a test PKI of its own, factory state, PIN `123456`). The tests that need a real card run by hand on a bench; no real card or PIN is used in CI. `just go-card` builds, vets, lints and tests with the tag.

## Documentation

- [slots-pin-touch.md](docs/slots-pin-touch.md), [card-slot-map.md](docs/card-slot-map.md): the keys per board, the slot plan, touch and PIN, exclusive access, the floors in code (from sluis)
- [pcsc-and-cards-on-boards.md](docs/pcsc-and-cards-on-boards.md), [restart-and-session-reset.md](docs/restart-and-session-reset.md), [runtime-dependencies.md](docs/runtime-dependencies.md), [examples/](docs/examples): how a host runs the driver, what a restarted driver does to the card, what it needs at run time
- [how-a-yubikey-holds-keys.md](docs/how-a-yubikey-holds-keys.md), [cards.md](docs/cards.md), [which-keys-to-buy.md](docs/which-keys-to-buy.md): background (from ritueel)
- [research/](docs/research): the spike on piv-go or PKCS#11 (S4), the card libraries and tooling, PIN, touch and reset measured on cards, speed and sharing, the Nitrokey 3 as a PIV card
