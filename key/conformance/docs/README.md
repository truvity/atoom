# The key conformance kit

The kit and how to run it are in [../README.md](../README.md). This page is for the author of a driver who wants to know what the kit assumes.

- **It makes keys.** The cases call `Ensure` for the reference purposes and leave the keys behind. Run it against a state of its own.
- **It declares its own purposes.** The port takes any purpose a host declares ([key port](../../docs/key-port.md)), so the kit declares the purposes its cases use (`conformance.Declaration()`), none of them a name that a driver ever held, and the driver under test is started with that declaration (`key-conformance --print-declaration`). On a card the declaration names the spare slots `90` to `94` for them and takes `87` for the PIN release key, so the kit shares no slot with the reference map's purposes. A driver that serves other purposes as well passes: the kit asks only for its own.
- **It tests behaviour, not storage.** A driver may keep a key in a file, on a card or in a cloud service; the kit sees only the answers.
- **A skipped case is not a pass.** `Capabilities` decides which cases apply; the summary line counts them apart (`12 cases, 11 ok, 0 failed, 1 skipped`).
- **A card must be unlocked.** A case that signs with a key that needs the PIN fails with the reason `locked` otherwise.
