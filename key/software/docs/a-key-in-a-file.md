# A key in a file

What a key in a file is, what it is good for, and how it could be protected.

This page assumes you know what a key pair is: a private key that signs or agrees on a secret, and a public key that others use to check the result.

## What a key file is

A key file is a file that holds a private key. The `key/software` driver keeps one PKCS#8 file for each purpose in a state directory that only the driver's user can reach ([the software store](software-store.md) has the files and the rules). The key is generated in memory and written straight into the file.

A key file is a copy by nature. It can be copied and read without a trace. It gives no evidence of where it lives, and a backup of it is another copy, which is exactly the danger. A hardware token or a cloud key service can say where a key was made; a file cannot ([what a store can prove](../../docs/what-a-store-can-prove.md)).

## What it is for

A key file is the home of development, tests, CI and a local installation. The volume is the boundary, and the people and programs that can read it are the ones trusted with the key.

It is not for a key that others rely on and that must be tied to one place. For that use a store that cannot be copied: [a YubiKey or a cloud key](../../docs/key-stores.md). Where the driver may run at all is a choice of the host, which mounts it or does not ([where it may run](software-store.md#where-it-may-run)).

## How a key file could be protected

The driver today does not protect the file beyond its mode and the volume. These are the ways that are known. They are candidates, not built, and the facts come from the research pages.

- **A passphrase, through age.** age is a small file-encryption format. A file can be locked to several recipients, each of which can open it alone, and a passphrase is one kind of recipient (scrypt). The age specification says that an scrypt stanza, if present, must be the only stanza in the header, so a passphrase file has only that recipient (age v1, read 2026-11-11; [research](research/age-or-jwe-for-the-key-file-2026-11-13.md)).
- **A secret held by a hardware token.** A FIDO2 token with the `hmac-secret` extension returns a secret that only that token can compute. Other programs use it to unlock a file or a volume: `systemd-cryptenroll` accepts FIDO2 tokens "that implement the 'hmac-secret' extension" to unlock a disk volume, and an age plug-in reuses a passkey on a hardware token outside the browser (age project note, published 2025-07-14). The secret does not open the file directly. It can unwrap a second key, so that locking a new file needs no token. Nothing was measured on a card for this page.
- **Another store as the home of the key.** A password manager or the operating system's key store can hold the bytes between runs. What each protects against is in [software key backends](research/software-key-backends-2026-11-01.md).

Each way trades a risk for another. A passphrase can be guessed, and a copy of the file is still a copy that an attacker can try against. A token protects against a copied file and fails when the token is lost.

## What a reset or a loss means

- **A reset of a token ends its secret.** A FIDO reset removes every passkey on the card, and a token that cannot rebuild a secret (without its key handle, for a passkey of the kind that stores nothing on the card) cannot open the file again. A key file that a token opens needs another way in, such as a passphrase file kept apart.
- **A lost way in is a lost file.** If every way to open a protected key file is lost, the key is lost.
- **A lost state directory is a new key.** The driver makes a key when `Ensure` finds none, so a driver started on an empty directory makes a new key with a new public key. Whoever relied on the old public key has to learn the new one.

## Sources

Read 2026-11-11.

- The age project, [Encrypting Files with Passkeys and age](https://words.filippo.io/passkey-encryption/) (published 2025-07-14): a token that returns the credential's state in the credential id, and an age plug-in that reuses a passkey on a hardware token outside the browser.
- systemd, [systemd-cryptenroll](https://www.freedesktop.org/software/systemd/man/latest/systemd-cryptenroll.html): FIDO2 tokens with the hmac-secret extension.
- age, [age v1](https://age-encryption.org/v1): the scrypt stanza.
