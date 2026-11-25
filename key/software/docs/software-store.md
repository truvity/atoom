# The software store

Reference of the `key/software` driver: what it keeps, where, and what it refuses.

The driver serves the [key port](../../docs/key-port.md) over private keys held in files. A software key has no evidence of where it lives and can be copied ([what a store can prove](../../docs/what-a-store-can-prove.md)). The volume is the boundary: whoever can read the state directory has the keys. That is the accepted risk of a local installation, a CI run and development, so the driver adds no passphrase, no import, no export and no attestation. [A key in a file](a-key-in-a-file.md) explains what that means; the README of the [driver](../README.md) has the library and the command line.

## What it keeps

One key for each purpose that the host declared, in its own file in the state directory. The algorithm of the declaration decides the key:

| Algorithm | Key |
|---|---|
| `ES384` | ECDSA on P-384 |
| `ES256` | ECDSA on P-256 |
| `RS256` | RSA-2048, public exponent 65537 |
| `ECDH-P384` | ECDH on P-384 |

The driver holds no list of purposes: the host declares them (`Options.Purposes`, or `--purposes FILE` on the thin main; [the declaration](../../docs/key-port.md#the-declaration-of-purposes)), and each purpose has one algorithm for its life. A call that names a purpose the host did not declare is refused with the reason `purpose`, and so is `Sign` of a purpose declared without `sign` and `Ecdh` of one declared without `agree`. `Capabilities` lists the declared purposes.

ECDSA signatures are made with RFC 6979 deterministic nonces (`crypto/ecdsa` with a nil reader), so that no signature depends on a random source; the signature is returned as made, with no low-S normalisation. The nonce is this driver's business and not the port's (KMS and the card use their own), so a test verifies a signature and never compares its bytes. `Sign` and `PublicKey` return the `key_id`, the hex SHA-256 of the public key.

A key is generated in memory with the system's random source and written straight into its file. It never goes to a plain file on the way, and it is never replaced: `Ensure` for a purpose that has a key returns the public key and `created` false.

## The state directory

| Item | Rule |
|---|---|
| Directory | Mode 0700, owned by the user of the driver's process. The driver creates it when it is absent (its parent must exist). |
| Key file | `<name of the purpose>.p8` (a declared name is 1 to 64 characters of `a-z`, `0-9` and `-`, so it is always a safe file name): one PKCS#8 DER private key (RFC 5208), mode 0600, owned by the same user, a regular file (a symbolic link is refused), at most 4096 bytes. A P-384 key is about 185 bytes and an RSA-2048 key about 1220. |
| Writing | A temporary file `.tmp-<random>` made with `O_EXCL`, synced, then linked to its name. The link fails when the name is taken, so a key is never replaced. The directory is synced after. |
| Unfinished write | A leftover `.tmp-` file is removed when the driver opens the directory. |
| Time | `created_at` of `Ensure` is the modification time of the key file, which is written once. |

## What it refuses

The driver refuses to start on a state directory or a file that does not follow the rules above. Each problem is a path and a word, never a byte of a key. The refusals are:

- a directory or file with another mode or another owner;
- a file that is not a regular file, is not PKCS#8, or is not named after a purpose that the host declared (a host that stops declaring a purpose must move its key file away on purpose, so that no key is lost unseen);
- a key of another kind than its purpose needs (an `audit-log` key made as P-384 by an older version, or a purpose whose declared algorithm changed, for example). The key is never re-made in place, because a new key changes the verifier key of whatever it signs. A person removes the file.

`software check --state-dir DIR --purposes FILE` runs the same checks, reads and writes nothing, and lists the problems.

A call is refused with a reason from the port's list: `purpose`, `algorithm`, `digest` (wrong length: 48 octets for `ES384`, 32 for `ES256` and `RS256`), `peer_public` (not a point on P-384), `absent` (the key is not made) and `store` (an I/O error; the journal has the cause, the answer does not). `Attest` and `VerifyAttestation` are refused with `no_attestation`, and the three calls of the PIN release with `unlocked` or `no_pin`: a software store has no PIN and is never locked.

## Options

| Option | Meaning |
|---|---|
| `StateDir` | The directory of the key files. |
| `Purposes` | The host's declaration: name, algorithm and operations of each purpose. Required; none, or one that is not a declaration, is refused before anything is opened. |
| `Caller` | Names the caller in the journal line of a refusal, when the host knows it. |

The thin main takes the same as `--state-dir` and `--purposes`, and the flags of the [socket guard](../../../docs/socket-guard.md).

## Where it may run

The driver has no option that names an installation ([record 0021](../../../docs/decisions/0021-drivers-know-no-deployment-shapes.md)). Anyone who can read the volume has the keys, wherever it runs. A host that must not hold an authority key in a file in some installation does not mount this driver there, and a configuration of that host then cannot make a software key such a key by mistake.

## What it does not do

- **No passphrase.** A key file is a plain PKCS#8 file. Protection is the file mode and the volume.
- **No import and no export.** Nothing in the port takes a key out or puts one in, and the driver has no other way in.
- **No attestation.** `Capabilities` says `attests: false`.
- **No backup.** A copy of the directory is a copy of the keys, and that is the danger as much as the remedy.

## Not built

These are ways to hold a software key that the driver could grow. None is built; each is a candidate.

| Candidate | What it would add | Where the facts are |
|---|---|---|
| An age file with a passphrase | A key file that a passphrase opens, so a copy of the volume is not a copy of the key. | [Age or JWE for the key file](research/age-or-jwe-for-the-key-file-2026-11-13.md) |
| A key file unlocked by a hardware-bound secret (a FIDO2 `hmac-secret`) | The key file opens only with a token present. | [A key in a file](a-key-in-a-file.md), the same research |
| A password manager or the operating system's key store as the home of the key | A key that survives a lost disk (a manager) or needs no file (a key store). Both are copyable by the user's own programs. | [Software key backends](research/software-key-backends-2026-11-01.md) |

Open: whether `key/software` should protect its key files at all, or leave that to the volume. The research says what each way costs; it does not choose.
