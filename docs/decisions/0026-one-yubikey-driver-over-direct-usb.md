# 0026. One YubiKey driver, over direct USB, with the policy declared by the host

Status: accepted, 2026-11-26 (owner decision on the pull request)

## Context

`key/yubikeypiv` reaches a card through piv-go, cgo, `libpcsclite` and the `pcscd` service. It is the one executable of atoom that is not static and is not cross-compiled ([packages](../packages.md#what-runs-where)). It serves one job: a host that signs unattended. The PIN is given once or never, a touch is never asked, a key is adopted only when its attestation matches the plan, and the driver administers nothing on the card.

A second job has arrived: a host that runs a ceremony with a person present. Its keys need the PIN and a touch for every use. The key is made at the ceremony, under a management key that the host chose. The host is one static executable that links the driver in its own process, also on a machine with no card service installed. The extraction plan left this open as R7 ([archive](../archive/extraction-plan.md#10-risks-and-open-questions)).

Measured by a consumer on real cards, firmware 5.2.6 and 5.8.0:

- A static Go program with cgo off reaches the PIV application over direct USB (the CCID interface) and signs, while `pcscd` runs on the same host for other cards.
- P-384 keys in retired slots with PIN always and touch always sign, and the attestation shows both policies.
- The attestation chain of firmware 5.8 verifies.
- A random AES-192 management key that needs a touch works.
- One key can be deleted and moved (firmware 5.7 and later).
- The status word `6982` cannot tell a wrong management key from a missing touch.

Most of this is written up in [PIN, touch, delete, move and reset](../../key/yubikeypiv/docs/research/yubikey-piv-pin-touch-and-reset-2026-11-13.md). The reading over direct USB is not yet (the last rule). The readings on firmware 5.2.6 are evidence about the transport and the answer codes only, not a promise that such a card can be used.

## Decision

### One driver

- **atoom has one driver for the PIV application of a YubiKey.** It is a new module, pure Go, cgo off, that speaks CCID over direct USB. Its working name is `key/pivusb`. It serves the same key port, `KeyStoreService` and `UnlockService`, and is built beside `key/yubikeypiv` first.
- **`key/yubikeypiv` is deleted when the new driver has passed**, on a real board of a host that signs unattended: the key conformance kit, the PIN release between two cards, and the restart and session-reset cases that `key/yubikeypiv` passes today. Until then both exist, and no new feature goes into `key/yubikeypiv`.
- **The attestation check and the pinned vendor certificates move to a shared library** that both drivers import ([0003](0003-libraries-with-thin-dependencies.md)). They accept exactly what they accept today.

### Policy is the host's

- **A host declares, per purpose, the slot, the algorithm, the PIN policy (`never`, `once`, `always`) and the touch policy (`never`, `always`).** The driver makes a key with exactly the declared policies. It adopts a key only when its attestation shows exactly the declared slot and policies and a firmware at or above the floor, besides the checks it makes today. A mismatch is a refusal, never an adjustment. A host that declares "PIN once, touch never" is held as strictly as `key/yubikeypiv` holds it now by a constant.
- **The purpose `pin-release` keeps its fixed slot and policy** (slot 87, PIN never, as [the key port page](../../key/docs/key-port.md#the-declaration-of-purposes) says); it is not declared with another policy.
- **The firmware floor stays 5.7, for every host.** This driver refuses a card below it, with the administration service on as well.
- **`Ensure` without the ceremony service keeps today's rule** ([0021](0021-drivers-know-no-deployment-shapes.md)): it makes a key only when the host allows factory defaults with its neutral switch.

### The card and its session

- **The transport claims the CCID interface of one card exclusively.** If another process holds it (a PC/SC service, gpg's `scdaemon`, which claims the interface itself when no PC/SC service runs, or another driver), the driver refuses with a reason of its own that says the interface is busy, and does not retry on its own. It is not the existing reason `busy`, which stays the queue's. It does not touch the FIDO and OTP interfaces of the card. The driver releases the interface when it stops.
- **One card for each driver instance, chosen by the card's serial number.** The driver reads the serial from the card and compares it before it sends anything else. A host with two cards runs two instances. A USB port path may narrow the choice; it never replaces the serial check. This driver always requires the serial, also when the host allows factory defaults, which [0021](0021-drivers-know-no-deployment-shapes.md) lets `key/yubikeypiv` leave out.
- **A card that is unplugged or reset is a new session.** The driver drops everything it held, the PIN included, and is locked again. A PIN is never used across a re-enumeration.
- **One exchange with the card at a time, a bounded queue in front of it, and a timeout on every exchange.** A card that stops answering fails the call and does not hold the service.

### PIN and touch

- **The PIN is delivered by `UnlockService`, as today:** tried once, held in locked memory, never written. A host that links the driver in its own process calls the same service in-process ([0009](0009-connectrpc-in-process.md)).
- **For a purpose whose PIN policy is `always`, a delivered PIN serves exactly one operation by default and is wiped after it:** each operation needs its own unlock. This is the default because the policy means that the person is asked each time.
- **The host may say, in the unlock call, that the PIN is held for the session of that card instead,** until the host locks the driver, the driver stops, or the card re-enumerates. The driver then presents it before each operation of that key. That is the host's choice ([0021](0021-drivers-know-no-deployment-shapes.md)), stated per unlock and never a setting of the driver's own. A PIN policy of `once` keeps today's held PIN.
- **A touch is the card's business: the driver cannot see it.** For a purpose whose touch policy is `always` the call waits for the card within its deadline, and a card that gave up answers a refusal with a reason of its own, which is not `busy` either. `Capabilities` says which purposes need a touch, so that the host can ask the person before it calls. The field is additive in `v1alpha1`.

### Card administration

- **Administration is a second service of the driver, the ceremony service** (working name). Its proto lives where `UnlockService`'s lives today: `key/proto/atoom/key/v1alpha1/`, in the key module, package `atoom.key.v1alpha1`. It is not a module of its own.
- **This is an explicit, bounded exception to [0002](0002-ports-and-drivers-over-connectrpc.md)'s refusal of "splitting optional calls into separate services",** with `UnlockService` as the precedent. Two things bound it: only this driver implements the service, and the driver serves it only when the host switches it on. A host that signs unattended never mounts it.
- **The service holds these calls:**
  - describe the card: its serial and firmware; each slot's algorithm, policies and origin; and whether the PIN, the PUK and the management key are still the factory values;
  - set the management key: AES-192, with or without a touch. The driver never assumes Triple-DES, because AES-192 is the factory type from firmware 5.7;
  - change the PIN and the PUK;
  - make the key of a declared purpose, through `Ensure`, with the management key that the host delivers through this service and never from a file of options;
  - delete one key;
  - write and read the certificate object of a slot.
- **When the management key needs a touch, a wrong key and a missing touch both answer `6982`.** The driver reports one reason that says the touch was not given or the management key is wrong, and it never tries the management key again on its own.
- **It never resets an application of the card, never writes the attestation key or its certificate (slot `f9`), and never sets a configuration lock.**

### Platforms and tests

- **Linux on amd64 and arm64.** Access to the USB device without a privileged user is a udev rule. It ships in the `.deb` and `.rpm` of the thin main, because that package runs the driver as a service. That adds one file to the fixed set of [packages](../packages.md#the-packages) and to the release check, and both change with the pull request that builds the package. A host that links the driver in its own process writes its own rule. macOS is not supported for cards by this driver until someone measures whether the system's own smart-card service leaves the interface free.
- **The driver is tested against a card simulated at the level of command and response bytes in CI,** and by hand on a real card in factory state; the existing bench recipe is the model ([bench](../../key/yubikeypiv/docs/bench.md)). What a consumer measured on real cards is written up next to the driver as research pages ([0006](0006-driver-research-lives-here.md)), without identifying any card.

## Why

- **One code base for the card.** Two drivers for one application would drift in their policy checks, their session rules and their firmware handling.
- **Every Go executable is static.** The last cgo exception, with its two native build hosts and its glibc ceiling, goes away when `key/yubikeypiv` is deleted.
- **The host decides policy** ([0021](0021-drivers-know-no-deployment-shapes.md)). The driver holds no PIN or touch constant that belongs to one kind of host.
- **A strict match of attestation and declaration is as strong as a constant.** The card states the policies of each key and the driver refuses any other. Declaring them moves the choice to the host and loses nothing.
- **The session form of an `always` PIN is offered at all** because every typed PIN is a chance to mistype, three wrong entries block the card, and a touch is still asked for each signature.
- **Administration stays away from the host that signs unattended.** The ceremony service is off unless the host switches it on, and a host that never mounts it cannot change a PIN, a PUK or a management key.

## Consequences

- A second driver for the length of the overlap, and the checks of both in one shared library.
- **A CCID and PIV layer of our own** takes the place of piv-go and pcsc-lite. atoom reviews and keeps that code. Firmware differences that the libraries absorbed (answer codes, the metadata call, the key types of each firmware) are ours to read, test and bound by the floor.
- **The interface is used exclusively.** While the driver runs, a host cannot share the PIV application with another tool's card service, gpg's included.
- **No macOS for cards,** as above.
- The pages that name the card driver, PC/SC or the one cgo exception change with the pull requests that build and delete it. The release table, the packages and the dependency check change with them.
- The declaration of purposes gains the two policies, as it has the slot: for the card driver only. Other drivers ignore them.

## Roads not taken

- **Two drivers for good, one for each job.** Two code bases for one application, and the second job would still need a card service on every machine.
- **A second transport inside `key/yubikeypiv`, behind build tags.** The module would keep piv-go, the cgo build and two sets of session rules, for a driver that the second job wants without them.
- **A pure Go client of the `pcscd` service.** It removes cgo and keeps the daemon, which is the dependency the second job cannot have.
- **PKCS#11.** It adds a C library, and the spike found that `ykcs11` does not hold the card for itself and keeps a PIN copy that cannot be wiped ([the spike](../../key/yubikeypiv/docs/research/s4-piv-go-or-pkcs11.md)).
- **The vendor's library, or one in another language, behind a process boundary.** A second runtime and a second executable for each host, with a socket to guard, for what one Go package does in process.
- **The OpenPGP application of the card.** Another driver, later, under the same rules.

## Open points

These stay open, and the pull requests that build the driver settle them.

- The proto of the ceremony service, with the field of the unlock call that says "one operation or the session", and the lock call (a `Lock` on `UnlockService`, in the style of `Status` and `Unlock`).
- The names of the three new refusal reasons: the interface is busy; the card gave up waiting for a touch; the touch was not given or the management key is wrong (`6982`, a third reason of its own).
- How the simulated card is shared with the fake of `key/yubikeypiv` while both exist.
- Whether the cached touch policy is offered, beside `never` and `always`.
- The module's final name.
