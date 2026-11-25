# The card bench: the driver against a real YubiKey

`just card-bench` runs the YubiKey PIV driver (`key/yubikeypiv`) against a **real card**, by hand, and checks what the fake card cannot: that the driver and the kit pass on the hardware, and that the rules of [which card it opens](../README.md#which-card-it-opens) hold with a real PC/SC stack. The tests of `just go-card` (and so CI) use the fake card in `internal/card/cardtest` and need no card; the bench is for a person with a card in a reader and is never run in CI.

```
just card-bench READER="<reader>" SERIAL=<serial>
```

- `READER` is the **exact PC/SC reader name** of the bench card, as PC/SC lists it (for example from `yubikeypiv`'s refusal for several readers, or from `pcsc_scan`). It must be in the list of readers; the bench opens this reader and no other.
- `SERIAL` is the serial number of the card, checked on the card before anything else is done.
- Both are required: without them the recipe refuses and does nothing. It also refuses when `CI` is set (and in a GitHub Actions run).

## Safety rules

The bench is a test of a card that can be thrown away. Read these before the first run.

1. **Factory credentials only.** The card must be in factory state: PIN `123456`, PUK `12345678`, the default management key. The bench sends the factory PIN and makes keys with the default management key, and nothing else. Do not run it on a card that holds anything that matters, or whose PIN, PUK or management key was changed.
2. **Never slot `f9`.** The Yubico attestation slot is not touched by the bench or by the driver.
3. **Name the reader, so that no other card is opened.** The driver connects to the named reader only. A machine with a card in use elsewhere in another reader is safe because of the name; leave such cards out of the machine anyway when you can. (The several-readers cases below prove that the driver refuses to choose; if the driver were wrong, those are the cases that would touch another reader, with a PIV applet selection and nothing more.)
4. **Stop before a retry counter reaches 0.** The helper (`cardbench unlock`) sends the PIN only to a card that reports all three attempts, sends it once and never retries; a card with fewer attempts is left alone and the run fails. A card whose PIN is not the factory one is therefore refused after at most one wrong attempt (2 left), and the run says so. If a counter is below 3, stop and look at the card: the PUK procedure is the card owner's, not the bench's.
5. **The recipe never changes a PIN, a PUK or a management key, and never resets a card** (no `ykman piv reset`, no change-PIN, no set-management-key, no PUK use). The driver does reset the *session* of the card, as it does at every start and exit (a log-out: no key, PIN or counter changes; see [restart-and-session-reset.md](restart-and-session-reset.md)).
6. **One holder of the card at a time.** The driver holds the card exclusively. Stop `scdaemon`, `ykman`, a browser with a WebAuthn prompt or anything else that talks to the card before the run; the busy-reader case relies on the recipe being the only holder.

## What the run does

Nothing is written to the card before step 3. Each step stops the run at its first failure and says why; the temporary directory (`/tmp/atoom-bench.XXXX`, with the sockets in it, paths well under 104 bytes) and every process the recipe started are removed when it ends, however it ends.

0. **Build** the thin main (`cmd/yubikeypiv`) and the helper (`internal/bench/cmd/cardbench`) with `-tags atoom_card`, and the key conformance kit (`key/conformance/cmd/key-conformance`). Write the kit's declaration of purposes (`key-conformance --print-declaration`) to the temporary directory: every `yubikeypiv` command of the run takes it as `--purposes`. List the readers (connects to no card) and check that `READER` is one of them.
1. **`yubikeypiv check --reader R --serial S --allow-factory-defaults`**: opens the card, applies the firmware floor and verifies the attestation of every key it holds. Read only.
2. **Negative checks**, each of which must be refused with its message:
   - a wrong serial (the serial plus one): "another serial", the card released without a reset;
   - a reader name that does not exist: "is not there";
   - no `--allow-factory-defaults` and no `--serial`: "serial is required", exit status 2, before any card is opened.
3. **Serve and run the kit.** `yubikeypiv serve` on UNIX sockets in the temporary directory, with the socket guard defaults (mode 0600, this uid) and the admin socket open to this uid; `cardbench unlock` delivers the **factory PIN** over the admin socket; `key-conformance --socket <path>` runs and every case must be `ok` (none failed, none skipped). The driver is then stopped, because the card is exclusive.
4. **The reader cases.**
   - With more than one reader present: several readers and none named is refused ("more than one reader and none named", "no card was opened"), and so is a serial alone. With one reader the run says `SKIP` and goes on; a second reader (empty is enough) makes the cases run.
   - A **busy reader**: `cardbench hold` connects to the named reader exclusively and keeps it for up to 30 seconds; `check` against it must be refused with "is in use by another process", naming the reader. The holder is then stopped and lets the reader go without a reset.
5. **`yubikeypiv check`** without factory defaults (with the serial): verifies the attestation of the keys that step 3 made, against Yubico's roots. This is the check that proves the keys were born on the card.

The run ends with `card-bench: PASSED`, or `FAILED` and the log of the driver.

## What it writes to the card

Step 3 runs the kit against a driver started with `--allow-factory-defaults`, where `Ensure` makes the keys of the slot plan on the card with the factory management key. The kit makes the keys of the purposes it declares for itself (`conformance.Declaration`): `conformance-es384` (slot `90`, P-384), `conformance-es256` (`91`, P-256), `conformance-rs256` (`92`, RSA-2048), `conformance-agree` (`93`, ECDH on P-384), `conformance-made-only` (`94`, P-384) and `pin-release` (`87`, P-384). The slots `90` to `94` are rotation spares of [the slot map](card-slot-map.md), which no purpose of the reference map takes, and `87` is the PIN release key's own; the keys are generated on the card with the policies of the plan (PIN once, touch never; `87` PIN never). Before step B13 the kit used the reference purposes of sluis and wrote `9a`, `9d`, `84`, `87` and `88`; a card that an earlier run left keys in those slots is not touched by this one, except `87`, which the PIN release key still uses. Nothing else is written: no certificate, no PIN, no PUK, no management key, no data object.

The keys stay on the card. The **next run adopts them**: `Ensure` finds a key in its slot, verifies its attestation and uses it, so the kit passes again and step 1 verifies them already. A card with keys in these slots that were not made by the driver (imported keys, a different algorithm) is refused by the driver, as it should be. To start from an empty card again the keys have to be deleted with a tool outside this recipe, which is the decision of whoever owns the bench card; the recipe has no reset.

## Build prerequisites

The tagged build is cgo over libpcsclite, natively (nothing is cross-compiled), as `just go-card` does:

| System | Needs |
| --- | --- |
| Linux | `gcc`, `pkg-config` and the pcsc-lite headers (`libpcsclite-dev` on Debian and Ubuntu), and `pcscd` running with a driver for the reader |
| macOS | the PC/SC framework, which the system has |

`tools/scripts/card-env.sh` makes the environment. devbox sets `CC` to a compiler that does not search `/usr/include`, so on a machine whose C library is the host's, name the host's compiler and `pkg-config` (CI does the same):

```
export ATOOM_CARD_CC=/usr/bin/gcc ATOOM_CARD_PKG_CONFIG=/usr/bin/pkg-config
```

On a machine with no such packages (NixOS, say) a Nix shell supplies them. `pcsclite.dev` puts its headers in `include/PCSC`, and piv-go includes `PCSC/winscard.h`, so the include directory is named as well:

```
nix shell nixpkgs#gcc nixpkgs#pkg-config nixpkgs#pcsclite.dev
export PKG_CONFIG_PATH=$(nix build --no-link --print-out-paths nixpkgs#pcsclite.dev)/lib/pkgconfig
export CGO_CFLAGS=-I$(pkg-config --variable=includedir libpcsclite)
export ATOOM_CARD_CC=$(command -v gcc) ATOOM_CARD_PKG_CONFIG=$(command -v pkg-config)
devbox run -- just card-bench READER="<reader>" SERIAL=<serial>
```

(The `nix shell` is entered first; `devbox run` keeps its environment variables but not its `PATH`, and the two `ATOOM_CARD_*` variables carry the compilers across.) `pcscd` must be running for the reader: the pcsc-lite library talks to it, and the Nix package does not start it.

## The helper and its tests

`internal/bench` holds the code the recipe needs and the thin main and the kit do not have: `cardbench readers` (list the readers), `cardbench status` and `cardbench unlock` (over the admin socket; the unlock sends the factory PIN, once, to a card that has all its attempts), and `cardbench hold` (hold a reader for N seconds). It is built with the tag like the thin main. Its tests run without a card: the unlock service is served by the real driver over the fake card (Linux), and the hold runs over the fake readers of `cardtest`.
