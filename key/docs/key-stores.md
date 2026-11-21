# Key stores

Where a key can live, which places the key drivers of atoom serve, and which places could be served later.

A key has to live somewhere. The places differ in one question above all: **can the key be copied?** A key that cannot be copied can only be lost. A key that can be copied can also be stolen without anyone noticing.

One rule holds for every driver of the [key port](key-port.md): a key is made in its store and never leaves it. Nothing in the contract takes a private key out or puts one in. A driver that exports a key is not a driver of this port.

## The key stores that have a driver

The choice of store is the choice of driver. A host runs one driver for a key, and the caller sees only the port. `Capabilities` names the store (`software`, `yubikey-piv`, `aws-kms`).

| Store | Driver | Where it fits |
|---|---|---|
| Software | [`key/software`](../software/README.md) | A local installation, CI and development |
| YubiKey PIV | [`key/yubikeypiv`](../yubikeypiv/README.md) | Boards and other installations on single-board computers: one card for each board |
| AWS KMS | [`key/awskms`](../awskms/README.md) | AWS installations, including a Lambda function that links the library in |

The three drivers serve the same four algorithms (`ES384`, `ES256`, `RS256`, `ECDH-P384`; see [algorithms](algorithms.md)). They differ in everything below.

| | Software | YubiKey PIV | AWS KMS |
|---|---|---|---|
| **Kind** | Software | Hardware | Cloud service |
| **Where the key is** | A PKCS#8 file for each purpose, in a state directory of the driver's user. | A PIV slot of a YubiKey 5. One slot for each purpose. | The service. The driver asks it to sign. |
| **Can it be copied?** | Yes. Anyone who can read the volume has the key, and a backup of it is a copy. | No. The key is made on the card, and an imported key is refused. | No, but the service can keep a replica (multi-Region keys). The driver neither makes nor manages replicas. |
| **Protects against** | A caller that cannot read the state directory. | Theft of the computer, and a copy of the key by malware. | Theft of a computer, loss of a card. Gives an audit trail of every use. |
| **Does not protect against** | Anyone or any program that can read the volume. A copy anywhere. | Loss, fire, a thief who has the PIN and the card. A caller the host admits can use the card while it is unlocked. | A compromised or locked account. A key deleted after the waiting period. |
| **Redundancy** | More copies, which raises the chance of theft. | None on one card. A second card holds a second, independent key. | A replica in another Region, made by the deployment. |
| **What "present" means** | The state directory is readable by the driver's user. | Possession of the card, in the reader, and the PIN. The driver starts locked and receives the PIN from an administrator or an unlocked peer. | Cloud permissions: the role the driver runs as, and the key policy. |
| **What it can prove** ([page](what-a-store-can-prove.md)) | Nothing. | An attestation chain from the card to Yubico's root. | Nothing from the driver. The key's description and policy can be read from the service. |
| **Cost** | Free. | A card for each board. | An account to secure; a price for each key and for each use. |
| **Good for** | Development, tests, CI, a local installation. | Keys that must be tied to one device and be provable. | Keys that need an audit trail and cloud access control. |

### Software

The driver keeps one P-384 key for each purpose (P-256 for `audit-log`, RSA-2048 for `token-signing-rs256`) in a state directory of mode 0700. It adds no passphrase, no import and no export, because the volume is the boundary. It does not attest. It is the store of a local installation, a CI run and a smoke installation without cards. Whether it may run in production is an option of the driver, not a rule of the port; see [its README](../software/README.md). [The software store](../software/docs/software-store.md) is its reference and [A key in a file](../software/docs/a-key-in-a-file.md) explains what a key file is and is not.

### YubiKey PIV

A PIV card makes the key physical. The key is generated on the card, so it never exists in the memory of the computer, and a thief needs the card, its PIN and, where the slot demands it, a touch. The card can *attest* the key: it hands out a certificate, signed under the maker's published root, saying that a key in a given slot was made inside this card, with this firmware and this PIN and touch policy. A key that was imported cannot be attested, and the driver refuses it. The driver also refuses firmware below 5.7.

A card has no backup, so a board whose card is lost is replaced with a new card and new keys. RSA signatures take about 0.17 seconds on a YubiKey 5 with firmware 5.8 (RS256, measured 2026-11-08); PS256 is the same card operation and was not measured. The behaviour of the card is in [How a YubiKey holds keys](../yubikeypiv/docs/how-a-yubikey-holds-keys.md), [Slots, PIN and touch](../yubikeypiv/docs/slots-pin-touch.md) and [Which keys to buy](../yubikeypiv/docs/which-keys-to-buy.md).

### AWS KMS

AWS KMS makes the key a permission. The key is as safe as the account's access control, and every use is in the audit trail. There is no card to lose, but an account to lose, and a deletion that can be undone only during a waiting period. A key is "present" because a role can use it, which is weaker than a touch unless the role is hard to assume; the key policy and the role belong to the deployment, not to the driver. KMS cannot say where a key was born, so the driver does not attest. How the contract maps to KMS is in [the layout](../awskms/docs/layout.md), and the research behind it in [AWS KMS as a key store](../awskms/docs/research/aws-kms-as-a-key-store-2026-11-19.md).

## Stores without a driver

These are candidates. None is built, and none is scheduled. Each should arrive with its first real installation, a real device or a real account, because a store that is only imagined cannot be tested.

| Store | Why it could be a driver | What has to be measured first |
|---|---|---|
| PKCS#11 HSMs (YubiHSM 2, Nitrokey HSM 2) | Hardware that makes a key and signs with it, behind one standard interface. YubiHSM 2 attests keys made on the device ([research](research/private-root-alternatives-2026-11-02.md)). The Nitrokey HSM 2 has no PIV applet and is reached through PKCS#11 ([Which keys to buy](../yubikeypiv/docs/which-keys-to-buy.md)). | Which algorithms and digests the device signs, whether it does ECDH on P-384, what it can attest, and how the interface is held by one process (see [piv-go or PKCS#11](../yubikeypiv/docs/research/s4-piv-go-or-pkcs11.md) for the card case). |
| Google Cloud KMS | The same model as AWS KMS in another provider. It attests keys made in Cloud HSM (read 2026-11-01). | Signing with P-384, what the attestation and the policy can prove, and how a key in another project is read. |
| Azure Key Vault | The same, for a company on Azure. No attestation was found (read 2026-11-01). | The same questions, and the difference between the vault and a managed HSM. |
| Nitrokey 3 as a PIV card | Hardware with open firmware. | It has no PIV attestation and no per-key PIN and touch policy (firmware 1.9.1, 2026-11-05; [research](../yubikeypiv/docs/research/nitrokey-3-as-a-piv-card-2026-11-05.md)), so it would be a store of the kind that proves nothing. |

Other ways to hold a software key (an age file, a passphrase, a password manager, the operating system's key store) are not stores of the port. [The research on software key backends](../software/docs/research/software-key-backends-2026-11-01.md) says what is known about them, and [The software store](../software/docs/software-store.md) lists them under "Not built".

## Choosing

| If | Then |
|---|---|
| You are developing or testing | The software driver. |
| You run a local installation or CI | The software driver, in a state directory only the driver's user reaches. |
| The installation is a board with a card and must be provable | The YubiKey PIV driver, one card for each board. |
| The installation runs on AWS and wants audited, policy-gated keys | The AWS KMS driver. |
| You want a key that provably exists in one place | A store that attests ([what a store can prove](what-a-store-can-prove.md)). |
