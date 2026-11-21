# What a key store can prove

What each kind of key store can show about where its key lives, how a driver declares it, and what no evidence can fix.

Someone tells you a key is "safe on a hardware token", or "in the cloud, locked down". How would you know? A claim is not evidence. This page says what each kind of store can show, so a reader can tell a claim from a proof. The stores are compared in [Key stores](key-stores.md).

## Three kinds of evidence

| Kind | Example | What it shows | What it does not show |
|---|---|---|---|
| **Attested** | A YubiKey PIV applet | A certificate chain, ending at the maker's published root, says: this key was made inside this device, with this firmware. For a PIV key it also gives the slot, the serial of the card, and the PIN and touch policy. | That the maker is honest, or that the computer in front of you is. |
| **Read from the service** | AWS KMS | You ask the service for the key's description and its policy, and read who is allowed to sign. The audit trail records every use. | That the key was made in a hardware module: the service gives no such statement. |
| **None** | A key file, a password manager, the operating system's key store, a token that cannot attest | Nothing. The key could have been made anywhere, and copied anywhere. | Everything. |

## Why a key made elsewhere can be told apart

A key that was *imported* into a token can be copied before it went in. A key *generated* on the token cannot, because it never existed outside. The token signs a statement about the second kind only: an imported key has no attestation, which is how the two are told apart. For that reason a driver that can attest refuses an imported key.

The chain is checked against the maker's root, which the verifier carries, and never against a certificate that the token or the requester hands over by itself: a root inside the evidence is not trusted. If the chain cannot be checked, the check has not passed, and a verifier must say so.

## What a driver declares

A driver says in `Capabilities` whether it attests. The field is `attests` ([the contract](key-port.md)). It is true when the store can say where a key lives. The calls behind it are `Attest` (the attestation of the key of a purpose, as the store makes it) and `VerifyAttestation` (reads another store's attestation against this store's own anchors). A store that declares `attests: false` refuses both: the software driver with the reason `no_attestation` (`failed_precondition`), the AWS KMS driver with `unimplemented`. The conformance kit skips the cases that need attestation for it. The kit never fails a driver for what it did not declare, and never passes it for it either.

The three drivers of atoom:

| Driver | `attests` | Evidence |
|---|---|---|
| `key/yubikeypiv` | true | Attested. It checks the attestation of every key it adopts, at start and when it makes one. It checks that the key was generated on the card and not imported, that the chain reaches Yubico Attestation Root 1 (pinned by its SHA-256), that the firmware is at or above the floor (5.7), that the serial is the card's own, and that the slot, the PIN policy, the touch policy and the algorithm are those of the slot plan. A key that fails any check is refused. |
| `key/awskms` | false | Read from the service, by the driver only in part: it refuses a key whose origin is not AWS KMS or AWS CloudHSM, a key of another spec or usage than the purpose's, and a key that is disabled or pending deletion. It does not read or judge the key policy; that is the deployment's. |
| `key/software` | false | None. |

## What the cloud gives instead

A cloud key service cannot attest where its key was made, but it can tell you, truthfully and every time you ask, who may use the key. A reader who needs more than the driver checks can read three things from the service and compare them with what is expected: the key's description (its type, its Region, that it is enabled), its policy (which role may sign, and with what), and any replica. A change of policy is the thing to watch for.

## What the kind allows a key store to hold

| Kind | May hold |
|---|---|
| Attested | Keys that must be tied to one device and be provable. |
| Read from the service | Keys that need an audit trail and access control. |
| None | Development, tests, CI and a local installation. A key that others rely on needs evidence the store cannot give. |

A new device or service is judged the same way before it joins: what does it prove? A token that cannot attest has no evidence, whatever else it does well. A Nitrokey 3 is an example: its PIV applet answers the attestation command with "function not supported" (firmware 1.9.1, read 2026-11-05; [research](../yubikeypiv/docs/research/nitrokey-3-as-a-piv-card-2026-11-05.md)).

## What evidence cannot fix

- **A lying host.** Evidence says where a key lives, not what you are about to sign. A compromised host can ask a store to sign a digest the caller never meant. The driver checks the digest's length and nothing more; the host that serves the driver decides who may call it and audits what it asks for. A touch for every signature, where the slot demands it, limits what malware can do unattended.
- **A stolen card and its PIN.** The key is where it should be, and in the wrong hands. Revoking the key answers that, and it is the consumer's act, not the driver's.
- **The maker.** You trust the maker's root, as you trust any root. Compare it, pin it, and refresh it by reviewed change. The roots a driver carries are part of what it trusts.

## Try it

On two cards, make a key on one and import a key into a slot of the slot plan on the other with the maker's tool. Ask each for its attestation certificate. The first answers, the second refuses. Then start `key/yubikeypiv` against each and read the refusal.
