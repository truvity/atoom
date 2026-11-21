# key/pinrelease

The wire format of the PIN release between boards: the signed text of a request and of its answer, and the sealing of the PIN to the request. The package holds no key and no PIN: the card drivers seal and open (the PIN never leaves them), and the host builds and checks the texts.

| | |
| --- | --- |
| Go module | `github.com/truvity/atoom/key/pinrelease` (no requirement at all) |
| API | `RequestText`, `ParseRequest`, `AnswerText`, `ParseAnswer`, `Digest`, `Seal`, `Open`, `NewID`, `UUIDv7Time`; `PurposePinRelease`, `SlotPinRelease` |
| Documentation | [docs/README.md](docs/README.md) |
| Where it came from | [provenance](../../docs/provenance.md#key-pinrelease) |

What stays out: the package does not read a signed key plan or a slot map. The card driver asks its host for the peer's pin-release key through an interface (`PeerKeys`), and the host decides which peers are listed.
