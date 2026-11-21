# Cards

What the `key/yubikeypiv` driver needs from a card, what a card in use looks like, and what the driver never does to it.

## What the driver needs

- **A YubiKey 5 with the PIV application.** The driver talks PIV over PC/SC, through piv-go and pcsc-lite ([README](../README.md)). Other devices are not served; see [which-keys-to-buy.md](which-keys-to-buy.md) for what was tried.
- **Firmware 5.7.0 or newer.** The floor fixes the EUCLEAK flaw in ECDSA signing (Yubico advisory YSA-2024-03) and adds Ed25519 in PIV. The driver refuses a card below it, both by what the card reports and by what the attestation of each key reports (`FirmwareFloor` in `plan.go`).
- **One card for each host.** A host runs one driver and the driver holds one card, exclusively. It opens the one reader it is told to open (`--reader`), and outside development it checks the card's serial against the one it was given (`--serial`). Which reader it opens is in [the README](../README.md#which-card-it-opens).
- **PIV slots from the slot plan.** Each purpose has a slot, an algorithm, and a policy for PIN and touch. Keys are PIN once and touch never, except the `pin-release` key in slot `87`, which is PIN never. The rows the driver serves are in `plan.go`; the whole map and the reasons are in [card-slot-map.md](card-slot-map.md) and [slots-pin-touch.md](slots-pin-touch.md). Slot `9e` is not used, because it signs without a PIN by default, and slot `f9` (the Yubico attestation key) is never touched.
- **Keys born on the card.** The driver adopts a key only after it checks the key's attestation against Yubico's roots. It refuses an imported key and a key whose policy is not the plan's.
- **A PC/SC stack that it alone uses.** `pcscd`, a CCID driver and `libpcsclite` ([runtime-dependencies.md](runtime-dependencies.md), [pcsc-and-cards-on-boards.md](pcsc-and-cards-on-boards.md)). Any other program that holds the card makes the driver fail closed.

## A card in use

A card that a host uses holds its keys in the slots of the plan and nothing else the driver cares about. It starts locked: the driver has no PIN until an administrator delivers it on the admin socket, and a key that needs the PIN is refused with the reason `locked` until then. The card keeps its PIN, PUK and management key as they were set when it was provisioned. A person who looks at it with a PIV tool sees keys in the slots of the plan that its host has enabled (`9a`, `9d` and `82` to `88`, and `9c` when the host turns the certificate on), each with an attestation certificate, and an untouched `f9`.

## What the driver never does

- **It holds no root.** A root key is not a key of this driver. A root that certifies the keys of a card lives outside the driver and outside the card the driver holds.
- **It never resets the card.** It never calls piv-go's `Reset`, which blocks the PIN and the PUK and wipes every key. What it does at start and exit is a reset of the card's *session* (a warm reset at the PC/SC layer): the card forgets a PIN verification, and no key, PIN, PUK, management key or counter changes ([restart-and-session-reset.md](restart-and-session-reset.md)).
- **It never changes a PIN, a PUK or a management key**, never uses the PUK, never deletes or moves a key, and never retries a wrong PIN. It sends a PIN once, and not at all to a card with one attempt left.
- **It makes a key only in development**, with the factory management key. A staging or production card is provisioned by its owners, and the driver only adopts and verifies what it finds.

## A card for tests and a card for production

The difference that matters to the driver is the environment it is started in.

- **A test card** is in factory state (PIN `123456`, PUK `12345678`, the default management key) and can be thrown away. In `development` the driver makes the keys of the plan on it. `just card-bench` runs the driver and the key conformance kit against such a card. Its rules, and what it writes to the card, are in [bench.md](bench.md). The keys stay on the card, and the next run adopts them.
- **A production card** (and a staging card) is provisioned before the driver sees it, with its own PIN, PUK and management key. The driver is started with the card's serial, makes no key, and refuses a card whose keys do not verify. Never run the bench on such a card.

A card goes from one use to the other only by being reset and provisioned again with a tool outside the driver. What a reset does is in [how-a-yubikey-holds-keys.md](how-a-yubikey-holds-keys.md#what-a-reset-does).
