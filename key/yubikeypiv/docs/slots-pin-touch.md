# Keys, touch, PIN and exclusive card access

What the `key/yubikeypiv` driver does with the keys of a card, with its PIN and touch policy, with the PIN release between cards, and with the card itself. The driver is a library and a thin main ([README](../README.md)). A host, such as sluis, runs it and decides who may call it.

## Keys on the card

- **Every key is born on the card.** The driver adopts a key only after it verifies its attestation, and it refuses an imported key ([card-slot-map.md](card-slot-map.md)). The driver makes keys only when its host allows factory defaults.
- **The card holds asymmetric keys only.** A host that needs data keys makes them in software and wraps them to the key-agreement key of a card (slot `9d`), so each card opens them with its own key.
- **A card has no backup.** A host whose card is lost or stolen makes new keys on a new card and revokes the old ones. The driver has no way to copy a key.
- **Signing speed is set by the card.** A YubiKey 5 Nano signs P-384 in about 147 ms with yubikit (measured 2026-10-31, firmware 5.8.0). Through the driver it takes 161 to 166 ms (the median of 50 signatures on each of three cards, measured 2026-11-04). The driver adds piv-go's command handling, one connect call and a check of each signature with the public key. That is about 6 signatures a second for a card. An RSA-2048 signature takes about 0.17 s ([pcsc-and-cards-on-boards.md](pcsc-and-cards-on-boards.md#the-rsa-key-and-the-queue)).

## Touch and PIN policy

- **Keys are PIN once and touch never,** except the `pin-release` key in slot `87`, whose PIN policy is never. A host signs unattended. The policies are fixed when the key is made and the attestation shows them. The driver makes only PIN never and PIN once, and touch never, and refuses a key with another policy in a slot of the plan (`pin_policy` or `touch_policy`).
- **The firmware floor is 5.7.** The driver refuses a card that runs older firmware, by what the card reports and by what the attestation of each key reports. Firmware 5.7 fixes the EUCLEAK flaw (Yubico advisory YSA-2024-03) and adds Ed25519 in PIV. `FirmwareFloor` is in `plan.go`.

## The locked start and the admin socket

- **The driver starts locked.** It has no PIN until an administrator delivers it. A key that needs the PIN is refused with the reason `locked` until then (`pin-release`, which needs none, signs for the PIN release only).
- **The admin socket takes the PIN.** The `UnlockService` ([unlock.proto](../../proto/atoom/key/v1alpha1/unlock.proto)) has two calls. `Status` says whether the driver holds the PIN, the node, and the attempts the card has left (`-1` when the card does not say). `Unlock` gives the PIN to the card. The socket is a local UNIX socket of mode 0600. The driver reads the peer's uid from the kernel (`SO_PEERCRED`) and checks it against `AdminConfig.Allow` before it reads the message, so the PIN is never read from a caller that is not allowed. The thin main admits one uid, `--admin-uid` (default root). The guard flags of the key socket do not apply to it ([socket guard](../../../docs/socket-guard.md)).
- **The PIN is 6 to 8 octets** (NIST SP 800-73-4, part 2, 3.2.1). It lives in one page of locked memory (`mlock`, `MADV_DONTDUMP`) and is never written to disk, to a log or to a core dump. It never leaves the driver process. A PIN that cannot be put in locked memory is not sent to the card.
- **A wrong PIN is tried once and never retried.** The card blocks after three wrong PINs and then needs the PUK. The driver reads the counter first, with a `VERIFY` that carries no PIN and costs no attempt, and sends no PIN to a card with one attempt left (reason `pin_retries`) or none (`pin_blocked`). A PIN the card refuses is dropped (reason `pin`); an administrator who mistyped it types it again.
- **Locked memory and `SO_PEERCRED` are Linux only.** On another system the driver builds, starts and stays locked.
- **After a full power loss one unlock is needed.** A host with several cards then lets the first unlocked card release its PIN to the others (below).

## The PIN release between cards

A locked driver can get its PIN from a driver of another card that is already unlocked, with no person. The PIN never leaves a driver in clear. The wire format of the two signed texts and of the sealing is in [key/pinrelease](../../pinrelease/docs/README.md). The calls are `BeginPinRelease`, `SealPin` and `FinishPinRelease` of the [key port](../../docs/key-port.md).

**A card-only feature.** The software driver has no PIN and attests nothing, so it refuses these calls (and `Attest` and `VerifyAttestation`) with a reason, and takes no part in a release. A host that does not give the driver a `PeerKeys` releases no PIN to a peer.

**One PIN for a group of cards.** The driver of the unlocked card seals the PIN that its own card took, and the locked card gives that PIN to its own card. A release works only between cards that have the same PIN, which a host arranges when it provisions them.

**How it goes.**

1. The locked driver calls `BeginPinRelease` with a request id (a UUIDv7). The driver makes an ephemeral P-384 key for this request, keeps the private half, and writes the text of its own request: its boundary, its node, the id and the public key. It signs the text with the `pin-release` key (slot `87`). A driver that holds the PIN refuses (`unlocked`).
2. The host carries the signed request to the driver of an unlocked peer, with the host's signed key plan, and calls `SealPin`. The driver verifies the request, seals its PIN to the ephemeral key (ECDH P-384, HKDF-SHA384, AES-256-GCM), writes the answer text itself and signs it with its own `pin-release` key.
3. The host carries the answer back and calls `FinishPinRelease`. The driver verifies the answer, opens it with the one-time key and gives the PIN to its card once, by the same path as an unlock.

**The `pin-release` key signs while the card is locked.** It sits in slot `87`, with PIN never and touch never. Because it needs no PIN, the driver signs with it only texts that the driver writes itself, from its own configuration: the request in `BeginPinRelease` and the answer in `SealPin`. `Sign` refuses the purpose, so a caller cannot have a digest of its choosing signed.

**What the driver checks itself,** with its own crypto, so that the word of its caller alone releases nothing:

- **The request** (the releasing side, `SealPin`): it is of this driver's boundary, from a node other than its own, and its ES384 signature verifies with the `pin-release` key that the host's `PeerKeys` returns for that node. The driver seals to the exact text it verified, to the ephemeral key inside it.
- **The answer** (the requesting side, `FinishPinRelease`): it is for the request this driver wrote (its id and the SHA-384 of its text), from another node of its boundary, and its signature verifies with that node's `pin-release` key. The driver verifies before it opens, so an answer that fails is no PIN attempt. The one-time key is used once.
- **The attestation of a peer's slot** (`VerifyAttestation`): the driver reads an attestation of another card against the same Yubico anchors as its own and the firmware floor, and returns the key, the slot, the two policies and the serial. What the slot must be (`87`, PIN never, touch never, the key the host lists) is the host's check on that answer, because the pinned Yubico anchors and piv-go are in the driver only. `Attest` returns the attestation of the key of a purpose, for the host's key plan.

**`PeerKeys` is the host's part.** It is an interface, `PeerKey(ctx, keyPlan, node) (*ecdsa.PublicKey, error)`. The driver gives it the key plan document that came with the call and the node that signed the text, and it returns the `pin-release` key the document lists for that node. An error that wraps `ErrUnlisted` is the reason `peer`; any other error is `key_plan`. The driver also refuses its own node, an empty node and a key that is not P-384, whatever the host says. It is called once for each release call and by several calls at once.

**What the driver does not check:** that the document is the newest one, that a request id is fresh or used before, and revocation of the peer. It cannot see the host's store. Those checks are the host's, made in `PeerKeys` and before the calls.

**What it guarantees, and what it does not.**

- The PIN never leaves the driver in clear.
- A caller that is compromised cannot obtain the PIN or spend the card's PIN tries without a request or an answer that a listed peer's driver wrote and signed. The driver refuses its own keys, its own card's signatures and a node that the host does not list, and its card signs no text of the caller's choosing.
- A caller that is compromised can still use a peer key that the host's `PeerKeys` still lists after it should have been revoked. The remedy is the host's: list the peer no longer. A replayed request gets a PIN sealed to a one-time key that the requesting driver has already dropped.
- An answer carries the PIN that the releasing card took. A card with another PIN refuses it once, and the driver then stops, as below, and never sends a PIN below the floor of one attempt left.

**A refused released PIN stops the release.** The driver keeps the stop in its state directory (`pin-release.refused`, `Options.StateDir`) and makes no request and tries no released PIN while it is there, after a restart too. A successful unlock on the admin socket removes it. A stop that cannot be saved ends the driver, and the driver does not start on a state directory it cannot write. With no state directory (the `check` command) the stop is kept in memory only.

**The reasons of the PIN release calls.**

| Reason | Meaning |
| --- | --- |
| `unlocked` | the driver holds a PIN already: nothing to release to it (a debug line in the journal, not a warning) |
| `one_time_key` | no one-time key, or the answer is not for it |
| `sealed` | the sealed PIN does not open |
| `request_text`, `answer_text` | the text is not of the format, not this node's, or not for the request in flight |
| `attestation` | an attestation does not verify |
| `key_plan` | `PeerKeys` refused the document |
| `boundary` | the text is of another boundary |
| `peer` | the other node is this one, or the host lists no `pin-release` key for it |
| `signature` | the signature does not verify with the peer's `pin-release` key |
| `pin_refused` | the card refused a released PIN before: a person unlocks the card |

## Exclusive card access

The driver has the card to itself, from start to stop. Measured 2026-10-31 on a YubiKey 5 Nano (firmware 5.8.0): the PIN state belongs to the powered card and not to a connection. While the card is unlocked, any local process that reaches it can sign. Any client's default disconnect resets the card. Wrong PINs from any local user block it. So exclusive access is a requirement of the host, not a preference:

- the driver runs as a user of its own, the card service (`pcscd`) accepts only that user, and no other software that talks to cards runs on the host ([pcsc-and-cards-on-boards.md](pcsc-and-cards-on-boards.md));
- the driver listens on UNIX sockets and checks the peer uid of every connection ([socket guard](../../../docs/socket-guard.md)), so that only the callers a host names reach the keys;
- everything that signs on the host signs through the driver.

## Factory defaults are the host's switch

The driver knows nothing of the installation it runs in ([record 0021](../../../docs/decisions/0021-drivers-know-no-deployment-shapes.md)). One switch of the host, `Options.AllowFactoryDefaults` (`--allow-factory-defaults`, off by default), says the card is in factory state on a bench or a developer's machine: only then does the driver make a key, with the management key (the factory default unless the host gives one), and only then may the serial be left out, to take the only card. Without it the serial of the card is required (`ErrSerial`): without the serial any genuine YubiKey with matching attestations would be adopted.

## What the host provides

- **The key socket:** a listener and a check of who may call (`Host.Serve`). atoom's thin main gives a UNIX socket with the [socket guard](../../../docs/socket-guard.md).
- **The admin socket's rule:** who may deliver the PIN (`AdminConfig.Allow`), and the way the PIN reaches it. A host should read the PIN from standard input or a prompt and never put it in an argument, an environment variable or a line of output.
- **`PeerKeys`,** when it wants PIN release: the check of the signed key plan, of the sequence of its documents and of revocation, and the carrying of the texts between cards. sluis's decisions are [0006](https://github.com/truvity/sluis/blob/main/docs/decisions/0006-keys-and-sealing.md), [0008](https://github.com/truvity/sluis/blob/main/docs/decisions/0008-boundaries-and-tenants.md) and [0041](https://github.com/truvity/sluis/blob/main/docs/decisions/0041-a-compromised-key-is-revoked-then-replaced.md).
- **The set-up of the card service and of the user** the driver runs as ([examples/](examples/README.md)).
- **The purposes,** and what each key is used for: the host declares them to the driver (`Options.Purposes`, or `--purposes FILE`), with a slot for each but `pin-release` ([card-slot-map.md](card-slot-map.md)); `purpose.Reference()` names the slots of the reference map.
