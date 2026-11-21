# YubiKey PIV: PIN, touch, delete, move and reset, measured on two cards

What a YubiKey 5 does with a key that needs the PIN and a touch for every signature, what a program sees when it refuses, what can be deleted or moved without a reset, and what a reset puts back. Measured on 2026-11-11 and 2026-11-12 on two test cards. This page is research; the last section says what it means for `key/yubikeypiv`.

## Question

A PIV key can be made so that every signature needs the PIN and a touch ("PIN always, touch always"). On a YubiKey 5:

1. What does the card refuse, and with which answer?
2. What does a program see, and what comes from the card and what from the tool or library?
3. What can be deleted and moved without a reset?
4. What does a reset put back, and what can "the card is clean" prove afterwards?

PIV is the smart-card part of the YubiKey. The vendor documents the slots, the policies and the resets ([PIV PIN and touch policies](https://docs.yubico.com/yesdk/users-manual/application-piv/pin-touch-policies.html), [PIV attestation](https://developers.yubico.com/PIV/Introduction/PIV_attestation.html)), and [how a YubiKey holds keys](../how-a-yubikey-holds-keys.md) retells it. This page gives only what was measured.

## Cards and dates

| Name here | Model | Firmware | Dates |
|---|---|---|---|
| Test card A | YubiKey 5 NFC | 5.2.6 | 2026-11-11 and 2026-11-12 |
| Test card B | YubiKey 5C NFC | 5.8.0 | 2026-11-12, two sittings |

Card A is below the driver's firmware floor of 5.7 ([slots-pin-touch.md](../slots-pin-touch.md)); the driver would refuse it. It is here for what it shows about older firmware.

## Result

**On both cards, a key with PIN always and touch always is refused with one answer code, `6982`, whether the PIN or the touch is missing.** An *answer code* (status word, SW) is the two-byte status the card returns; `9000` is success and `6982` is "security status not satisfied". With no touch given, the card refuses after about 14.3 seconds (read as a wait for a touch; the touch itself was not recorded). A missed touch cost no PIN try.

After a PIV, an OpenPGP and a passkey reset, card B read the same as at its first look in every field the test read. Card A read as a factory card in the fields read. That is less than "clean": the test could not read the key behind the factory attestation certificate, the passkey signature counters and some OpenPGP data.

On card B (5.8.0) one key was deleted and one key was moved without a reset, and the state came back to the state before. The card refused a move onto an occupied slot (`6A80`). Card A was not asked.

## Method

Each test card was used by one person at a Linux host. Each test ran in a container with no network and a private smart-card service (pcsc-lite 2.4.1). The tools were `ykman` 5.8.0, python-yubikit 5.8.0, Python 3.14.4 and OpenSSL 3.5.5. The later runs printed these versions; the first signing run on card A did not, and used the same container image. Where a Go program is named below, it talks to the card over USB directly, not through the smart-card service. The host kernel and distribution were not recorded.

Terms. *PIN policy always*: the PIN must be sent right before every use of the key. *Touch policy always*: a touch is needed for every use. A *retired slot* is a PIV key slot from `82` to `95`. *Attestation* is a certificate the card signs to say how a key was made. The *management key* is the secret that allows changes to the PIV application, such as making a key. Slot `f9` holds the card's own attestation certificate. VERIFY is the PIV command that sends the PIN. GET METADATA is a Yubico command (firmware 5.3 and later, by the python-yubikit source) that reports, for each slot, its key and policies, and the PIN counter. OpenPGP, OATH and FIDO2 (the passkey side) are other applications of the card.

1. **Make keys.** P-384 keys in slots `83` and `87`, with PIN always and touch always. Card A: `ykman piv keys generate -a ECCP384 --pin-policy ALWAYS --touch-policy ALWAYS`, with the factory management key. Card B: python-yubikit, with the same policies (read from the log of the run).
2. **Sign and refuse.** For each slot, in this order: a sign request with no PIN; the PIN and a sign request with a touch; a second sign request with no PIN; the PIN and a sign request with no touch. The sign request is the PIV command GENERAL AUTHENTICATE with an 18-byte message and SHA-384. Each signature was checked with `openssl dgst -sha384 -verify`. Card A: python-yubikit, one run. Card B: python-yubikit, one run, and a second program (Go, direct USB) with eight rounds on slot `83`: five touched, one missed with the PIN sent, one with no PIN, one touched at the end.
3. **Touch wait without a PIN, repeated on card A.** Two sign requests to slot `83` with no PIN. In the first the person kept the hand off the card. In the second the person was asked to touch at once.
4. **Attestation.** python-yubikit `attest_key` (the PIV command with instruction `F9`) for each slot. The test program decoded the extensions. The chain was checked on the host with `openssl verify`, with the vendor's root file (and, for firmware 5.7.4 and later, the intermediate file) as given on the vendor's pages.
5. **Delete and move (card B).** python-yubikit `delete_key` and `move_key` on slots `94` and `95`, before and after a read of every slot (GET METADATA), compared with `diff`. The test program refused a move onto an occupied slot before it sent anything; a second try with an override sent it to the card.
6. **PIN counter (card B).** The counter was read three times without a PIN, one wrong PIN was sent on purpose, the right PIN was sent, the PIN was changed and changed back.
7. **Management key with touch.** python-yubikit `set_management_key(..., require_touch=True)`. Card A: a random 3DES key. Card B: a random AES-192 key. Then three requests: the key read back from its file, the factory key, and the new key with no touch.
8. **Resets.** One program sent the PIV, OpenPGP and passkey resets, after the person typed a confirmation. The test read the device's list of applications whose reset is blocked several times; it was empty each time (5 reads on card B). After the resets the card was read again and compared with the first look: a state file for PIV (PIN, PUK and management key states, every slot, every certificate, slot `f9`), a report for the passkey side, and a report for OpenPGP, OATH and the one-time-code application.

## What the setup over- and under-counts

- **A person was at the card, and the hand is not recorded.** The times of requests that needed a touch include the person's reaction. The program printed "touch now" and "do not touch" lines; what the finger did is not recorded. Every reading that depends on a touch is an inference.
- **One sample per case.** Most rows below are single runs. The counts are in the tables.
- **Card A was not at factory state at its first look.** It had one OpenPGP reset before the measurement of the OpenPGP attestation slot. Its PIV and passkey state before the resets was not read.
- **Compares read only what the tools print.** "Equal" holds only for those fields. Two reports that could not read a field look equal too (see "What a clean card can and cannot prove").
- **The chain checks used vendor files downloaded once** (list at the end). Their signature files were not verified. "Chains to the vendor root" means "chains to the file downloaded on 2026-11-12".
- **The cards were not tested alike.** Card B had more steps (delete, move, metadata). "Differences between the two cards" lists what was not tried on which card.

## Numbers

### Sign requests on a key with PIN always and touch always

Card A is python-yubikit. Card B is python-yubikit (8 requests) and the Go program (8 rounds); both gave the same answers. Times of signatures include the person's reaction and are not the speed of the card (about 150 ms; see [card libraries](./card-libraries-and-tooling-2026-11-01.md) and [speed and sharing](./yubikey-piv-speed-and-sharing-2026-11-13.md)).

| Request | Card A (5.2.6) | Card B (5.8.0) |
|---|---|---|
| PIN sent just before, touch given | 2 of 2 signed and verified (3.42 s, 2.27 s) | 8 of 8 signed and verified (1.51 s, 6.71 s with python-yubikit; 0.87 to 4.14 s in the Go program) |
| PIN sent, no touch | `6982` after 14.32 s and 14.32 s (2 of 2) | `6982` after 14.32 s, 14.32 s and 14.33 s (3 of 3) |
| No PIN, directly after a good signature | `6982` after 1.93 s and 6.96 s (2 of 2) | `6982` after 5.01 s and 4.46 s (2 of 2) |
| No PIN, other cases | `6982` after 5.16 s, 14.33 s, 14.30 s (hand off) and 1.33 s (touched at once) | `6982` after 10.88 s, 14.33 s and 14.33 s |
| PIN tries after a missed touch | 3 (read once, indirectly: the program sent the next PIN only after it saw 3 of 3) | 3 of 3 (read once, after the missed touch) |

The same answer code `6982` came back for all 8 refused sign requests on card A and all 8 on card B. No request without the PIN produced a signature.

Reading (inference). A request with no PIN waits for a touch, up to about 14.3 s, and then gives `6982`. Four requests with no PIN took 14.30 to 14.33 s (2 on each card); the shorter ones (1.33 to 10.88 s, 7 requests) fit a touch that ended the wait. The repeat on card A is one pair of requests: 14.30 s hands off, 1.33 s touched at once. The 10.88 s on card B is not explained. A request with the PIN and no touch waits 14.32 to 14.33 s on both cards (5 of 5). The [card libraries note](./card-libraries-and-tooling-2026-11-01.md) measured 14.6 s for one missed touch; the 14.3 s here is the figure of these 5 requests.

### What a program sees

- **One code for two causes.** `6982` does not say whether the PIN or the touch was missing. The tools add to this: the Go program printed "as expected" for any refusal answer, and the first management-key test accepted any exception from python-yubikit as a refusal and did not log the status word.
- **A call can block for 14 seconds.** A sign request with no PIN does not fail at once; the call returns after the wait.
- **The wait comes from the card (inference).** python-yubikit and the Go program (direct USB, a different path) saw the same 14.3 s and the same `6982` on card B.
- **PIN always means a PIN before every request.** After a signature that had the PIN, the next request with no PIN was refused (2 of 2 on each card). The Go program sent the PIN before each of its 7 rounds that had one.
- **A wrong PIN is a different answer.** On card B, one wrong PIN gave `63C2` ("wrong PIN, 2 tries left"), and one right PIN brought the counter back to 3 of 3. This was seen once. A wrong PIN on card A was not tried.
- **Reading the PIN counter.** On card B, GET METADATA gave "3 of 3", the default flag and, after one wrong PIN, "2 of 3" (3 reads before the wrong PIN cost no try). On card A, `ykman piv info` printed "PIN tries remaining: 3" with no total, and a program read the counter by sending a PIN-less VERIFY (3 of 3 in 3 runs, no try spent; the raw answer code was not logged).
- **Keys without a certificate.** On card A, a key in slot `83` with no certificate was shown by neither `ykman piv info` nor `pkcs11-tool --list-objects` (OpenSC module). A key with a certificate in slot `87` was shown by `ykman` only (1 look). Card A has no GET METADATA; the test found keys by asking each slot for an attestation.

### Delete and move without a reset (card B, firmware 5.8.0)

| Step | Result | Times |
|---|---|---|
| Make keys in `94` and `95`, delete `95`, then `94` | The look after the first delete showed only slot `95` changed; after the second the state file equalled the state before the keys were made | 1 sequence |
| Make a key in `94` (PIN always, touch always), move it to `95` | `moved 94 -> 95`; the source slot was empty, the destination held a key | 1 |
| Attestation of `94` before and of `95` after | The public key in the two certificates is the same; the policies read "pin always touch always"; the certificate names the slot it was asked for (`94`, then `95`) | 1 pair |
| Sign with the moved key | The signature verified against the public key made in `94` (1 touch) | 1 |
| Move onto an occupied slot, no override | The test program refused before it sent anything | 1 |
| The same, with the override | The card answered `6A80` (python-yubikit: `ApduError`) | 1 |
| Delete the keys of the move test | The state file equalled the state before the keys (a second sequence, 2 more deletes; 1 more delete of a proof key in the management-key test) | 1 |

On the wire, MOVE KEY and DELETE KEY are one Yubico instruction (`F6`): MOVE KEY has the target slot in the first parameter and DELETE KEY has `FF` there. These values come from reading the library source; they were not captured on the wire. The python-yubikit 5.8.0 source gives firmware 5.7.0 as the floor of both. Yubico's [firmware 5.7 page](https://docs.yubico.com/hardware/yubikey/yk-tech-manual/yk5-firmware-5.7.html) says "5.7.x and later" for moving and deleting keys in its summary, and "As of YubiKey Firmware 5.7.4" in its command tables. Not tried on card A.

### Management key with touch

| Request | Card A (5.2.6, 3DES key) | Card B (5.8.0, AES-192 key) |
|---|---|---|
| Set a random key with `require_touch=True`, using the factory key | accepted (1) | accepted (1) |
| Use the new key, read from its file in a new process, with a touch prompt | accepted (1) | accepted (1) |
| Offer the factory key | no touch: refused after a run time of about 16 s, answer code not logged (1); with a touch prompt: `6982` after 2.65 s (the touch is not recorded) (1) | no touch: `6982`, run time 15 s (1) |
| Offer the new key, no touch | refused after a run time of about 15 s, answer code not logged (1) | refused: `6982`, run time 14 s (1) |

The touch flag of the management key could not be read back on card A (no GET METADATA). On card B no look was made after it was set. On both cards only the refusal of the key with no touch shows the flag.

### The attestation chain

| | Card A (5.2.6) | Card B (5.8.0) |
|---|---|---|
| Firmware and policies in the certificate | firmware bytes `05 02 06`; policies bytes `03 02`, read by the test program as "PIN always, touch always" (2 of 2 slots) | firmware bytes `05 08 00`; the same policy bytes (2 of 2 slots, and slots `94`, `95`) |
| Chain | statement, slot `f9` certificate, one root: 3 certificates | statement, slot `f9` certificate, two intermediates, root: 5 certificates |
| `openssl verify`, default checks | OK for 2 of 2 | OK for 2 of 2 |
| `openssl verify`, strict checks | failed (slot `83` only), six reasons: missing Authority Key Identifier in the statement; in the `f9` certificate a path length without `keyCertSign`, no key usage, no Authority or Subject Key Identifier; in the root, Basic Constraints not marked critical | not tried |

The test program decoded the policy bytes with its own table, which the measurement did not check against the vendor's page. The `openssl` strict option was not logged; the six reasons are OpenSSL's error texts for that run. piv-go v2.6.0's built-in roots do not verify a firmware 5.8 certificate: see [card libraries](./card-libraries-and-tooling-2026-11-01.md).

## What a reset puts back

One test program sent the resets. For PIV it used python-yubikit. By reading its source (not captured), the library sends wrong PINs until the PIN and the PUK are blocked, then sends the PIV reset (instruction `FB`). So a reset by that library first uses up the PIN and the PUK.

| Reset | Card A (5.2.6) | Card B (5.8.0) |
|---|---|---|
| PIV | 5 resets. After 3 looks: PIN tries 3, management key 3DES, no certificate. After the first, 2 new keys were made with the factory management key, though a random key with touch had been set before | 1 reset. The state file was byte for byte the state of the first look, with PIN, PUK and management key at their defaults, and no key or certificate |
| Slot `f9` certificate | same hash before and after (1 reset) | same hash (1 reset) |
| Key behind `f9` | the 2 new statements verified against the same `f9` certificate (1 reset; an inference that the key still signs) | not read |
| OpenPGP | 3 resets, 2 of them read before and after. The factory Admin PIN appears to have been accepted in the 2 attestation steps that followed a reset (inferred from two steps that ended without error; the answer code was not logged; the PIN had been changed to a random value before). The reset after the second attestation step put back the key slot that the step had changed (seen with a look between, 1 of 2 cycles) | 1 reset, after an attestation step that had made a key. The OpenPGP report was byte for byte the first look |
| OpenPGP attestation slot | same key state and same certificate hash before and after, 2 of 2 resets read | same six entries before and after, 1 of 1 |
| Key behind the OpenPGP attestation slot | a statement made after a reset verified under the slot's certificate (1) | not tested after the reset |
| Passkey | 4 resets, all ended "done". The one look after a reset showed no PIN set | 3 resets, all ended "done". The look equalled the first look: no PIN, 100 credential places left |

Passkey reset times (seconds from the first sight of the re-plugged card to the sending of the reset, and to its return, which includes the touch):

| | Sent | Returned |
|---|---|---|
| Card A | 0.554, 0.492, 0.501, 0.575 | 4.695, 3.451, 3.089, 3.180 |
| Card B | 1.086, 0.842, 0.731 | 5.648, 4.783, 3.772 |

The card accepts a passkey reset only soon after it is plugged in. The test used a 5-second window; [how a YubiKey holds keys](../how-a-yubikey-holds-keys.md) quotes about ten seconds from Yubico. All 7 resets were sent inside the 5 seconds. In one run the reset returned after 5.648 s, later than the window, and still ended "done". This is one sample. The polling of the card started up to about 1 s after power-up, so the sent times are lower bounds. The test does not decide between 5 and 10 seconds.

## What a clean card can and cannot prove

"Clean" here means: the compares of the first look and the end look were equal. They read:

- PIV: PIN, PUK and management key states; every slot's key (by metadata on card B) and certificate; the hash of the slot `f9` certificate.
- Passkey side: whether a PIN is set; how many credential places are left; the options the tool prints.
- OpenPGP: PIN counters, PIN policy, key states, whether fingerprints are set, the signature counter, the attestation slot (key state, certificate hash, touch), and the three key slots.
- OATH, the interfaces and the one-time-code application (card B only).

They did not read:

- the key behind the `f9` certificate, and the private OpenPGP attestation key. Only the certificates were compared. A key that signs a new statement is evidence for one reset on card A and nothing for card B;
- passkey signature counters, and the fields of the passkey information that the tool does not print;
- OpenPGP data outside the list above: cardholder data, fingerprint values, generation times;
- PIV objects other than those listed;
- the PIN and PUK values (only their counters and default flags, on card B);
- the one-time-code application on card A: the report could not read it (`ApplicationNotAvailableError`). On card B it could (slot 1 configured, slot 2 not), the same in both looks.

Three more limits.

- **Two unreadable fields look equal.** On card A the first OpenPGP look read 3 of 6 fields of the attestation slot (algorithm, fingerprint and generation time answered `6B00` to the single-object reads). Two looks that both fail to read a field compare equal, and the program wrote "kept" for that first cycle. A second cycle with a combined read read all six. The result of the first cycle is not trusted for those three fields.
- **The first look may be after a reset.** On card A it was. "Same as the first look" proves "same as a card that was reset once", not "same as a card from the factory".
- **A reset is not undone by being equal.** The test cards held nothing of value. For a card that holds a key, a reset deletes it, and no compare brings it back.

## Differences between the two cards

| Thing | Card A (YubiKey 5 NFC, 5.2.6) | Card B (YubiKey 5C NFC, 5.8.0) |
|---|---|---|
| Factory management key | 3DES | AES-192, default flag yes, touch never |
| GET METADATA in PIV | not available (keys found by attestation) | available (key per slot, counter with total, default flags) |
| PIN counter shown by `ykman piv info` | "3", no total | read through metadata: "3 of 3" |
| OpenPGP attestation chain | statement, template certificate, the vendor OpenPGP root | statement, template certificate, two intermediates, the vendor root |
| OpenPGP single-object reads (algorithm attributes, fingerprints, generation times) | answered `6B00`; a combined read worked | not tried singly; the combined read read all six fields |
| Passkey side | PIN protocol 1 only; no count of stored credentials | PIN protocol 2; "100 left" at first look |
| Passkey PIN change | not tried | stopped once with status `31` (counter 8 to 7; probably a mistyped old PIN, by the person's account, not proven), passed in a second sitting |

Everything else was the same on both cards or was seen on one card only, as the tables above say.

## What this does not show

- **Two cards, two firmware versions.** One card of each. Nothing on firmware 5.7.x, and nothing on a model other than a YubiKey 5 NFC and a YubiKey 5C NFC.
- **Mostly one run per case.** The 14.3 s wait with the PIN sent and no touch is 5 samples over both cards, and the shorter times rest on a touch that was not recorded. It is not shown that the wait is the same on a third card.
- **Not tried at all:** a wrong PIV PIN on card A; delete and move on card A; the strict chain check on card B; the touch flag of a management key read back; the passkey reset with a missed window; a reset after the factory attestation key was replaced (the vendor says it cannot be restored); `ykman config reset`.
- **Not decided:** whether the 14.3 s wait comes from the touch policy (a key with touch "never" in a spare slot, sent a request with no PIN, would separate the cause); whether the OpenPGP attestation key is kept by the reset on firmware 5.8.0 (one OpenPGP attestation statement after the reset on card B, verified, would show it).
- **The vendor files were not checked against their signatures.** Windows, macOS and other drivers were not used.
- **No count of passkey credentials on card A.** The card does not report one.

## What it means for key/yubikeypiv

The first three points restate code and docs of this tree; the last three are consequences of the measurements and are not built.

- **The driver makes only PIN never and PIN once, and touch never.** `internal/card/card.go` has no PIN-always policy, and `Generate` refuses any touch policy but never. A key with PIN always or touch always in a plan slot is refused at adoption (`pin_policy` or `touch_policy`, from the attestation), which matches what the attestation of such a key shows here (policy bytes `03 02` for "always, always"). See [slots-pin-touch.md](../slots-pin-touch.md) for the policies of the plan.
- **The 14.3 s wait cannot reach the driver today,** because no key it adopts needs a touch. If a host ever asked the driver for a touch-always key, a sign call could block for about 14.3 s and return `6982`, which looks the same as a missing PIN. The driver would have to track what it sent and report the cause itself. Not built.
- **A wrong PIN costs one try and is not retried; reading the counter costs none.** The driver sends the PIN once and reads the counter with an empty VERIFY. This agrees with the measurements: a PIN-less VERIFY spent no try on card A (3 of 3 in 3 runs), and one wrong PIN on card B moved the counter from 3 to 2. A missed touch cost no try. When the card is logged in, the empty VERIFY answers success instead of the counter, so the driver treats that as "already verified" and resets the session ([restart-and-session-reset.md](../restart-and-session-reset.md)).
- **The session reset the driver makes is not the PIV reset measured here.** The driver's reset is a warm reset at the PC/SC layer that ends the PIN verification and keeps keys, PIN, PUK, management key and counters. The PIV reset measured here first blocks the PIN and the PUK, then deletes every key and certificate and restores the factory PIN, PUK and management key; the slot `f9` certificate stayed the same. The driver never calls it. The bench has no reset either, so a bench card keeps the keys the kit made until someone deletes them with a tool outside the recipe ([bench.md](../bench.md)). On firmware 5.7 and later (the driver's floor) such a tool can delete one key without a reset.
- **An attestation names the slot that was asked, not the slot where the key was made.** A key moved with MOVE KEY attests under its new slot, with the old policies. The driver checks the slot, serial, firmware and policies of the attestation of the slot it reads, so it proves that the key was made on this card with these policies, not that it was made in that slot. This is a reading of the code and of the move test, not something the driver was run against.
- **A reset check should name what it did not read.** The "clean" state above is equality of what the tools print. For a card that holds keys that matter, a reset deletes them and no compare brings them back; the driver does not offer one.

## Changes made to the cards

Test cards only. Test keys, a changed PIN (changed back), a random management key with touch, a passkey PIN and a key in the OpenPGP signature slot were made. Both cards then went through a PIV, an OpenPGP and a passkey reset. A temporary access rule for the card was installed on the host before each sitting and removed after it.

## Raw logs and vendor files

Raw logs are not published; they contain the cards' serial numbers. The vendor files used for the chain checks were downloaded on 2026-11-12 from `https://developers.yubico.com/PKI/yubico-ca-1.pem`, `https://developers.yubico.com/PKI/yubico-intermediate.pem`, `https://developers.yubico.com/PKI/yubico-piv-ca-1.pem` and `https://developers.yubico.com/PKI/yubico-opgp-ca-1.pem`.
