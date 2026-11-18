# The key conformance kit

The kit and how to run it are in [../README.md](../README.md). This page is for the author of a driver who wants to know what the kit assumes.

- **It makes keys.** The cases call `Ensure` for the reference purposes and leave the keys behind. Run it against a state of its own.
- **It uses sluis's reference purposes.** The contract's purposes are an open list in principle and a closed one in practice: `ES256` is the `audit-log` key's and `RS256` the `token-signing-rs256` key's. A driver that serves other purposes maps them in its own tests; the kit's names are the ones of [key/slotplan](../../slotplan/README.md)'s reference map.
- **It tests behaviour, not storage.** A driver may keep a key in a file, on a card or in a cloud service; the kit sees only the answers.
- **A skipped case is not a pass.** `Capabilities` decides which cases apply; the summary line counts them apart (`11 cases, 10 ok, 0 failed, 1 skipped`).
- **A card must be unlocked.** A case that signs with a key that needs the PIN fails with the reason `locked` otherwise.
