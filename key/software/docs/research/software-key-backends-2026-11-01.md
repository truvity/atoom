# Where a software key could live besides a file: 1Password and the OS key store (2026-11-01)

Research for the `key/software` driver: how a password manager (1Password) and the operating system's key store hold a secret, what each protects against, and what that means if the driver ever keeps a key somewhere other than a file.

## Question

Where may a software key live between runs, if not in a file on one disk: in 1Password, or in the operating system's key store? What does each protect against, what does each cost, and what would a driver have to do about it? The driver today keeps PKCS#8 files ([the software store](../software-store.md)).

## When

Read on 2026-11-01. The rows marked *rechecked* were read again on 2026-11-02, against the document or the source and not a summary.

## Finding

**1Password through its `op` command line can take a key without the key touching a disk or a command line; the OS key store is the weakest of the three homes (file, 1Password, key store).** `op` takes a JSON item template on standard input, and it needs no CGO and no pre-1.0 library. The Go SDK's desktop sign-in needs CGO on Linux and macOS, and its service accounts cannot reach personal vaults. Neither store is a place for a key that others rely on: whoever unlocks the vault, or any program of the user once the key store is unlocked, can read the key.

## Method

Read the vendor documents and the library source named below. Nothing was written to a vault or a key store. The facts about what a program of the same user can do are the vendors' own statements, not tests.

## What the setup over- and under-counts

- No vault or key store was exercised, so size limits are the documented ones, not measured.
- macOS and Windows key store behaviour was read from source only.

## Numbers

### 1Password

| Fact | Source | Label |
|---|---|---|
| The Go SDK is `github.com/1password/onepassword-sdk-go`, latest release v0.4.1 on 2026-07-30, a version 0. | GitHub releases (*rechecked*). | READ |
| `WithDesktopAppIntegration` returns an error when the program is built without CGO on Linux and macOS ("requires CGO to cross-compile"). | `client_builder_no_cgo.go` (*rechecked*). | READ |
| Service accounts cannot reach Personal, Private or Employee vaults; they have hourly and daily rate limits. | 1Password service account documents, 2026-11-01. | READ |
| `op item create` can read an item JSON template from standard input: `op item template get Login \| op item create --vault personal -`, and not together with `--template`. The documents say command arguments "can be visible to other processes on your machine" and "If you're assigning sensitive values, use a JSON template instead." | `op item` reference (*rechecked*). | READ |
| An SSH Key item accepts Ed25519 and RSA (2048, 3072, 4096) keys; "other key types, such as DSA or ECDSA keys, aren't supported". A P-384 key does not go in one. | The 1Password SSH key documents (*rechecked*). | READ |
| An authorization of the command line expires "after 10 minutes of inactivity" with "a hard limit of 12 hours". | The CLI app-integration security page (*rechecked*). | READ |
| On Linux the command line connects to a Unix socket of the app that is "owned by the current user/group, allowing any process started by this user to connect to it". | The same page (*rechecked*). | READ |
| Removing a member of a shared vault does not change its keys; recovery needs the account password and the Secret Key, which 1Password cannot recover. | The 1Password security white paper, dated 2024-12-10, sections 1 and 10. | READ |
| Per-item and per-field size limits. | No official figure found. | UNVERIFIED |

### The operating system key store (`github.com/zalando/go-keyring`)

| Fact | Source | Label |
|---|---|---|
| The latest tag is v0.2.8 (2026-03-23). | GitHub, 2026-11-01. | READ |
| On Linux it uses the Secret Service over the D-Bus session bus, and opens the session with `"plain"`, so the secret crosses the bus unencrypted. | `secret_service/secret_service.go`, master (*rechecked* 2026-11-02). | READ |
| Size: on macOS the combination of service, user and secret "should not exceed ~3000 bytes"; on Windows the password is limited to 2560 bytes; on Linux there is no limit but "performance suffers with big values (>100KiB)". | The `ErrSetDataTooBig` comment in `keyring.go` and `keyring_windows.go` (*rechecked*). | READ |
| Without a D-Bus session bus there is no key store on Linux: the library fails to find one. | `godbus` source, 2026-11-01. | READ |
| Whether another program of the same user can read an entry without a prompt. | Linux: any program on the session bus can use the Secret Service API of an unlocked collection (secondary sources). macOS and Windows: not confirmed from an authoritative source. | READ for Linux; UNVERIFIED for macOS and Windows |

A P-384 PKCS#8 key is under 200 bytes and an RSA-2048 PKCS#8 key about 1220 bytes, the two kinds the driver keeps, so all three limits are above them.

## Other routes read

- **The 1Password Go SDK.** In-process, but a version 0 with breaking changes allowed between minors, and a sign-in that needs CGO or a service account that cannot reach personal vaults.
- **An SSH Key item for the key.** The native type, but it refuses P-384.
- **A Document item with the key as a file.** Fits any key, but `op` attaches a file by path, so the key would be written to disk first.
- **The Connect server.** Self-hosted, but it cannot create the item types needed and its file endpoints only read.

## What it means for key/software

Both are candidates, not built. A driver that kept its key in 1Password would give a lost disk no loss of the key, and would add nothing against compromise: the vault syncs between devices, so the sync is redundancy against loss, not against theft. A driver that kept its key in the OS key store would add no account and no file, and would be readable by any program of the same user once the store is unlocked, with no copy: a dead disk is a dead key. Neither is a place for a key that others rely on. The password-manager route would use `op` and never the SDK, for the reasons in the table above. The way to protect the file itself is in [Age or JWE for the key file](age-or-jwe-for-the-key-file-2026-11-13.md) and [A key in a file](../a-key-in-a-file.md).

Open: whether either home is wanted, and the size limits on each platform, which would be measured before any use.

## Raw logs

Not kept; the sources are public and named in the table.
