# key/pinrelease

The wire format of the PIN release between boards: the signed text of a request and of its answer, and the sealing of the PIN to the request. The package holds no key and no PIN: the card drivers seal and open (the PIN never leaves them), and a key service builds and checks the texts.

| | |
| --- | --- |
| Go module | `github.com/truvity/atoom/key/pinrelease` (no requirement at all) |
| API | `RequestText`, `ParseRequest`, `AnswerText`, `ParseAnswer`, `Digest`, `Seal`, `Open`, `NewID`, `UUIDv7Time`; `PurposePinRelease`, `SlotPinRelease` |
| Documentation | [docs/README.md](docs/README.md) |
| Where it came from | [PROVENANCE.md](PROVENANCE.md) |

What stays out: the reader of the slot map of a signed key plan (`SlotMapOf`, `DecodeSlotMap`, `SignerKey`) needs sluis's signed configuration and stays in sluis; the card driver asks its host for the peer's key through an interface instead.
