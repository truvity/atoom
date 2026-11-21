# The key conformance kit

The kit and how to run it are in [../README.md](../README.md). This page is for the author of a driver who wants to know what the kit assumes.

- **It makes keys.** The cases call `Ensure` for the reference purposes and leave the keys behind. Run it against a state of its own.
- **It uses reference purposes.** Today each driver holds a closed list of purposes, so the kit uses the names of that list: `ES256` is the `audit-log` key's and `RS256` the `token-signing-rs256` key's, the names of [key/slotplan](../../slotplan/README.md)'s reference map. The port is meant to take any purpose a host declares ([key port](../../docs/key-port.md), step B13 of the [plan](../../../docs/plan.md)); the kit will then declare its own purposes.
- **It tests behaviour, not storage.** A driver may keep a key in a file, on a card or in a cloud service; the kit sees only the answers.
- **A skipped case is not a pass.** `Capabilities` decides which cases apply; the summary line counts them apart (`11 cases, 10 ok, 0 failed, 1 skipped`).
- **A card must be unlocked.** A case that signs with a key that needs the PIN fails with the reason `locked` otherwise.
