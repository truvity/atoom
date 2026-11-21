# For a key file age fits better than JWE (read, 2026-11-13)

Research for the `key/software` driver: if the driver ever protects its key files, should the container be age or JWE (the JOSE encryption format)? Nothing here is built.

## Question

`key/software` keeps its keys as plain PKCS#8 files ([the software store](../software-store.md)). How could it protect those files? Two ways are known ([A key in a file](../a-key-in-a-file.md)): a passphrase, and a secret held by a hardware token (a FIDO2 `hmac-secret`) that unwraps a key. Could the container be an age file, or could the JOSE family (JWE for encryption, JWK for keys) be the container instead, for the key file, for the wrapped key that a token opens, or for both?

Terms used below. A **recipient stanza** is one block of the age header that wraps the file key for one recipient. A **work factor** is the cost setting of a password hash such as scrypt. A **KDF** (key derivation function) turns one secret into a key. An **AEAD** is a cipher that also checks that nothing was changed. **hmac-secret** is a card feature that gives a secret that only that card can compute. A **PIV slot** is a numbered place for a key on a smartcard. The **serialisation** of a JWE is its written form: compact (one text line) or JSON. The **protected header** of a JWE is the part of its header that the AEAD authenticates. **HPKE** (hybrid public key encryption) is a public-key encryption scheme.

## When

Read on 2026-11-13. Nothing was run: this page compares documents and source code.

## Finding

**For a key file age is the better container, and JWE is not needed. For the wrapped key that a token opens, age and a strict JWE profile are similar in security, and age is the smaller choice.** What decides it is not a missing algorithm: JOSE has registered key forms that take a 256-bit secret, and age has no native stanza for `hmac-secret` either. Either way the derivation and the names would be a design of the project. What separates the options is that age keeps one format with a header that is fully authenticated, and keeps a passphrase file readable with the plain `age` command. This is a conclusion of the author, not a decision.

## What was read

- The specifications: age (C2SP `age.md` and `age-plugin.md`, last changed in commit `fc7f312aff` of 2026-07-29; the stable address `age-encryption.org/v1` was compared on 2026-11-13 for the sentences used here, and they agree), RFC 7516, 7517, 7518, 7520, 7914, 8037, 8725, and the IETF draft on HPKE in JWE.
- Code: `filippo.io/age` v1.3.2 (2026-08-29), `github.com/go-jose/go-jose/v4` v4.1.9 (2026-11-05) and `github.com/go-piv/piv-go/v2` v2.6.0, as module source (code, read, not run).
- The security advisories on the repository pages of age and go-jose, and one password guidance page (OWASP).
- The addresses and read dates are in the table at the end. Text in the cells of the tables comes from the cited document or source file, with its section. A sentence that starts "Conclusion", the finding and the recommendation are the author's. Library behaviour was read in source code at the stated version and not run.

## 1. What must be stored

A protected key file has these parts. Each row says who opens it.

| File | Opened by | Format | Not decided |
|---|---|---|---|
| Key file: a private key (PKCS#8 today, plain) | A passphrase, or a key that a token unwraps | age, with the recipients below; encrypting needs no token | Whether the driver protects its files at all |
| Passphrase key file | A passphrase alone | age, one scrypt stanza alone | Nothing |
| Unlock file: the private half of a key that wraps the key file, one way in for each token named | A secret of any one named token (CTAP2 `hmac-secret`) | None | The format; where the key handle is kept, for a passkey of the kind that stores nothing on the card |
| Recovery file: an age identity | A passphrase | An age identity in a passphrase-protected file | Whether this is an age file with one scrypt stanza |

What must be true after a cut during a write: a reader must refuse a damaged key file and never open it. Both formats do this, because both end in an authenticated cipher. The driver writes a key file to a temporary file, syncs it, links it to its name and syncs the directory, so a cut leaves the old state or a leftover temporary file that the next start removes. On a real USB stick, in a trial of another project (measured 2026-11-13), one 512-byte sector held the content of the write before it.

## 2. age against JWE, row by row

"JWE" below means the JOSE encryption format with the algorithms of RFC 7518. A sentence that starts "Conclusion" is the author's, not the source's.

| Question | age (v1) | JWE |
|---|---|---|
| Several recipients in one file | Yes. "Each recipient stanza wraps the same _file key_ independently." (age.md, Recipient stanza) | Yes, only in the JSON serialisation: a `recipients` array with one `encrypted_key` each (RFC 7516 §3.2, §7.2.1). The compact form has one recipient. |
| A passphrase beside other recipients | Forbidden: "An scrypt stanza, if present, MUST be the only stanza in the header." The reason given is "an expectation of authentication that is implicit in password-based encryption". The Go library refuses it too. | No rule found in RFC 7516 or RFC 7518. go-jose documents the cost: the PBES2 algorithms "have to be run for each recipient". Conclusion: a file opened by a card or a passphrase is possible, but the guessing target is then the key file itself, which is copied to backups. |
| Password algorithm, its cost, who sets it | scrypt (RFC 7914, memory-hard), `r = 8`, `p = 1`; the header carries log2 of the work factor. The Go default is 18. OWASP: scrypt at least 2^17, `r = 8`, `p = 1`. | PBES2: PBKDF2 with HMAC-SHA-2 and AES key wrap; the header carries `p2c`, the count (RFC 7518 §4.8). RFC: "A minimum iteration count of 1000 is RECOMMENDED." OWASP: PBKDF2-HMAC-SHA256 at least 600,000. No memory-hard algorithm is registered in JWA, and no JOSE draft for one was found (IETF datatracker, searched 2026-11-13). OWASP lists Argon2id first, scrypt second, and PBKDF2 for the case that FIPS-140 compliance is required. |
| A hostile cost number in a file | "The identity implementation SHOULD apply an upper limit to the work factor." The Go library caps at 22 by default; `SetMaxWorkFactor` sets it between 1 and 30. | RFC 7518 and RFC 8725 give no upper limit for `p2c` (both were searched). go-jose v4.1.9 refuses `p2c` above 1,000,000 and offers no setting (code, v4.1.9, read, not run). In the general JSON form `p2c` may sit in an unprotected header. |
| A recipient on a PIV card (the driver's keys are P-384) | Native: X25519, and "tagged" P-256 for hardware ("use P-256 ECDH"). `age-plugin-yubikey`: "any PIV token with an ECDSA P-256 key". No P-384 recipient. | ECDH-ES on P-256, P-384, P-521 (RFC 7518 §6.2.1.1) and X25519 (RFC 8037 §3.2). The HPKE draft adds `HPKE-1-KE` on P-384. piv-go v2.6.0 does ECDH on a PIV slot for P-256 and P-384, and for an X25519 key (`X25519PrivateKey.ECDH`; whether a slot holds one depends on the card's firmware) (code, read, not run). |
| A recipient in a cloud key store | None in the spec or in the plugin list that was read. | RSA-OAEP-256 and ECDH-ES are registered. AWS KMS `DeriveSharedSecret` needs a NIST ECC key with `KeyUsage` `KEY_AGREEMENT`. No use of a key file needs it. |
| `hmac-secret` output as the unlock secret | No stanza in age.md. A `fido2prf` stanza is defined in a 2025-07-14 post and has a plugin. The list of plugins names `age-plugin-fido2-hmac` (v0.5.0), "Plugin for wrapping software native identities with FIDO2"; its README says to "consider this plugin to be experimental until the version v1.0.0 is published". | `dir` (the key is the content key) or `A256KW` / `A256GCMKW` with a 256-bit key (RFC 7518 §4.4, §4.5, §4.7). No algorithm derives a key from a pseudo-random output. Conclusion: the profile must name the derivation. |
| What is authenticated | The whole header: an HMAC over every line, including each stanza and its numbers; then each 64 KiB chunk of the payload, with a final-chunk flag. | The protected header is the additional authenticated data. The shared `unprotected` header and the per-recipient `header` "are not integrity protected" (RFC 7516 §7.2.1). The compact form has only the protected header. |
| Choice of algorithm | None. The stanza type names how the file key is wrapped; the payload cipher is fixed. | `alg` and `enc` are in the file. RFC 8725 lists the mistakes: use only the algorithms the caller allows (§3.1), avoid RSA1_5 (§3.2), validate curve points (§3.4), avoid `zip` (§3.6), type the object (§3.11). A draft to deprecate `none` and `RSA1_5` is in the RFC Editor queue. |
| A second tool opens it | `age` and `rage`; a passphrase file opens with the `age` command line alone. | `step crypto jwe decrypt` (v0.31.0) and `jose` (C). For a card secret, the key must be derived by hand. Not run. |

## 3. The libraries (code, read, not run)

| | `filippo.io/age` v1.3.2 | `go-jose/v4` v4.1.9 |
|---|---|---|
| Several recipients | `age.Encrypt(dst, recipients...)`; labels keep scrypt alone | `NewMultiEncrypter`; `dir` and `ECDH-ES` are "Not supported in multi-recipient mode" |
| Password | scrypt, cap settable | PBES2 supported, `p2c` default 600,000 on writing, fixed cap on reading |
| Card or cloud key does the unwrap | `age.Identity` is an open interface (`Unwrap(stanzas)`); a custom identity may call a card. Plugins run as separate programs. | `OpaqueKeyDecrypter.DecryptKey(encryptedKey, header)`; ECDH-ES with a software key needs `*ecdsa.PrivateKey`. A card needs the caller's own Concat KDF. |
| Curves | X25519, P-256 (tagged), ML-KEM hybrids | ECDSA keys on P-256, P-384, P-521. The `OKP` branch of the JWK code reads `Ed25519` only, so X25519 ECDH-ES is not there. HPKE: not in the README. |
| Advisories on record | 1: GHSA-32gq-x56h-299c (2024-12-18, medium), in the `plugin` package | 12 on record, 10 published in 2026 (three of them describe the same problem). Those that touch JWE decryption: GHSA-78h2-9frx-2jm8 (2026-03-31, high, panic on an empty `encrypted_key`, fixed in 4.1.4), GHSA-6mp9-8xrw-74r4 and GHSA-4w3g-jfj2-cw2q (2026-09-04 and 2026-10-19, the same panic in the CBC-HMAC modes, fixed in 4.1.5), GHSA-p9j4-2j95-j4rv (2026-10-30, medium, unprotected headers override protected values, fixed in 4.1.7, no fix for v3), GHSA-c67m-wr4h-2ccg (2026-10-30, medium, quadratic time on many recipients, fixed in 4.1.7) |
| Maintenance | Release 2026-08-29 | Release 2026-11-05; v3 gets only critical fixes |

## 4. JWK as the form of the key inside the file

The driver keeps a PKCS#8 key (RFC 5958). A JWK holds the same EC private key in the member `d` (RFC 7518 §6.2.2.1) and may carry a `kid`. An encrypted JWK is a JWE with `cty` `jwk+json` (RFC 7517 §7). Conclusion: a small gain in naming, a loss in direct reading by OpenSSL and the Go PKCS#8 parser. Keep PKCS#8.

## 5. Three options

**(a) age for every key file; design the wrapped key inside age.** The unlock file is an age file whose content is the wrapping identity. It has one stanza for each named token, of a type with a full name that the project owns, which age.md recommends for non-native types. The body wraps the file key under a key derived from the token secret with a stated KDF. All stanzas are of one non-scrypt kind, so any number of tokens fit. The scrypt rule forces two things: no passphrase stanza in the unlock file or in the key file, and the passphrase kept in a separate recovery file. A custom Go `age.Identity` opens it; the stock `age` command needs a plugin program.
Pro: one format; no field that picks an algorithm; the recovery file opens with plain `age`. Contra: the project invents a stanza type and a KDF that no other age tool knows; the stock tool cannot open the token path; `fido2prf` and `age-plugin-fido2-hmac` exist, and whether either could be used instead of a new stanza was not evaluated; the scrypt rule forces a second file for the passphrase; the hardware fallback of age is P-256 only, although piv-go v2.6.0 also has ECDH for an X25519 key in a slot, which age takes natively (code, read, not run).

**(b) age for the key file; a strict JWE profile for the unlock file only.** What a profile would have to fix, from the standards:
- Serialisation: compact (it has one recipient, RFC 7516 §3.2). No JSON serialisation.
- `alg`: `dir`. `enc`: `A256GCM`. Nothing else.
- Key: derived from the token secret by a KDF that the profile names, with a salt and a context string. (The choice of KDF is left to the design; HKDF with SHA-256 is one.)
- Protected header: only the members the profile lists, all bound by the AEAD.
- Refused: every member and every algorithm the profile does not list, and any content of an unexpected length.
Pro: a registered container with an RFC example for `dir` (RFC 7520 §5.6); any JOSE library parses it; the AEAD binds every protected header member by construction; a JWE can be read by a person and by tools (`step crypto jwe decrypt`, `jose`) apart from the key derivation. Contra: a second format and, if a library is used, a second dependency with advisories; the derivation and the member names are still the project's own; the token path still needs a FIDO tool and a hand-made key for any outside tool.

**(c) JWE for everything.** The key file is a general JSON JWE with `ECDH-ES+A256KW` recipients on P-384 for the wrapping key and the recovery key; the passphrase file is `PBES2-HS512+A256KW`. Pro: one family; P-384 everywhere; the HPKE draft adds a P-384 key encapsulation. Contra: the cost number and per-recipient headers sit outside the authentication; PBKDF2 is not memory-hard (OWASP lists it for the case that FIPS-140 compliance is required); the library published ten advisories in 2026, about five of them on JWE decryption; the HPKE text is still a draft; the stock `age` recovery is lost. The road "one age file with a passphrase stanza and token recipients" would become possible, and this page advises against it.

**Recommendation: (a), as a judgement about the number of formats and dependencies, not about security.** (a) keeps one format with a fixed header authentication. (c) has the most contras. (b) is the closest rival and is a fair choice if the unlock file should be readable by a JOSE tool. Where this does not follow from the table: on security alone the table does not separate (a) from (b). Both authenticate the header (age by a MAC over it, a strict JWE profile by the protected header). The recommendation rests on the number of formats and dependencies, which is a judgement.

## What a test would have to show

1. The whole path, token secret to wrapping key to key file, works in the build the driver ships.
2. A cut during the write of each file kind leaves a file that the reader refuses.
3. A passphrase file with log2 work factor 30 stops at the reader's cap, in a measured time and memory.
4. An older copy of a file is refused or is flagged.
5. The passphrase file opens with the stock `age` command on a clean machine, from the file and the passphrase only.
6. Two tokens each open the unlock file alone. What removing one token does to copies made earlier is stated.
7. For (b), each refused header member is refused by the reader, and RFC 7520 §5.6 reproduces with the code.
8. ECDH on a PIV slot agrees with software ECDH on P-256 and P-384 for many random points.
9. The scrypt cost chosen runs in the memory of the smallest machine the driver runs on, with the time measured there.

## What this does not show

- Nothing was run: no file was made, no token used, no tool called. A library may behave in a way its source does not show.
- The `fido2prf` stanza and the `age-plugin-fido2-hmac` format were read in one blog post and one README; the plugin's own specification was not read.
- The advisory lists are what each repository page showed on 2026-11-13. A list says nothing about bugs not yet found; age has the shorter list. Conclusion: it also has the smaller surface (no header with algorithm names).
- No cost number is claimed for scrypt or PBKDF2 on any machine.
- The HPKE text is draft -22 (6 July 2026). On 2026-11-13 the IETF datatracker shows it in the RFC Editor queue with no RFC number. Only editorial change is expected, but this note did not check that.

## What it means for key/software

Candidate, not built. If the driver protects its key files, the finding points to age: a passphrase file with one scrypt stanza opens with the plain `age` command, and the library `filippo.io/age` has the shorter advisory list. A token-held secret would need a stanza and a derivation that the project designs, whichever container it picks.

Open: whether `key/software` protects its files at all; the choice of (a), (b) or (c) if it does; the type and the KDF of a token path; and what a cut during the write of a protected file must leave. The nine tests above come before any of it.

## Sources read on 2026-11-13

| What | Address |
|---|---|
| age specification, plugin specification (last changed in commit `fc7f312aff`, 2026-07-29) | <https://github.com/C2SP/C2SP/blob/main/age.md>, <https://github.com/C2SP/C2SP/blob/main/age-plugin.md> |
| age Go library v1.3.2, module source and advisories | <https://proxy.golang.org/filippo.io/age/@v/v1.3.2.zip>, <https://github.com/FiloSottile/age/security/advisories> |
| `age-plugin-yubikey` README on `main` (latest release v0.5.1, 2026-04-08); awesome-age list; `age-plugin-fido2-hmac` README on `main` (latest release v0.5.0, 2026-05-11); all read 2026-11-13 | <https://github.com/str4d/age-plugin-yubikey>, <https://github.com/FiloSottile/awesome-age>, <https://github.com/olastor/age-plugin-fido2-hmac> |
| Encrypting Files with Passkeys and age (2025-07-14) | <https://words.filippo.io/passkey-encryption/> |
| RFC 7516, 7517, 7518, 7520, 8037, 8725 | <https://www.rfc-editor.org/rfc/rfc7516>, and the same path for the other numbers |
| RFC 7914 (scrypt) | <https://www.rfc-editor.org/rfc/rfc7914> |
| OWASP Password Storage Cheat Sheet (last change 2026-10-30) | <https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html> |
| HPKE in JWE, draft -22; deprecation of `none` and `RSA1_5`, draft -08 | <https://datatracker.ietf.org/doc/draft-ietf-jose-hpke-encrypt/>, <https://datatracker.ietf.org/doc/draft-ietf-jose-deprecate-none-rsa15/> |
| go-jose v4.1.9 module source and advisories | <https://proxy.golang.org/github.com/go-jose/go-jose/v4/@v/v4.1.9.zip>, <https://github.com/go-jose/go-jose/security/advisories> |
| piv-go v2.6.0 module source | <https://proxy.golang.org/github.com/go-piv/piv-go/v2/@v/v2.6.0.zip> |
| AWS KMS `DeriveSharedSecret` | <https://docs.aws.amazon.com/kms/latest/APIReference/API_DeriveSharedSecret.html> |
| `step crypto jwe`; `jose` | <https://smallstep.com/docs/step-cli/reference/crypto/jwe/encrypt/>, <https://github.com/latchset/jose> |
