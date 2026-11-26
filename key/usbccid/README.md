# key/usbccid

The USB layer under a smart-card driver that speaks CCID over direct USB ([record 0026](../../docs/decisions/0026-one-yubikey-driver-over-direct-usb.md)): it finds the devices that have a CCID interface, opens one usbfs node, claims one interface and moves bulk transfers, each with a timeout. Pure Go, cgo off. It knows no CCID message and no card application; those are built on it.

| | |
| --- | --- |
| Go module | `github.com/truvity/atoom/key/usbccid` (requires `golang.org/x/sys` and no atoom module) |
| Packages | `usbccid` (the USB layer, this page) and `usbccid/ccid` (the CCID reader on a `Port`, below) |
| API | `Discover`, `DiscoverHost` (`Candidate`, `Filter`) read a sysfs tree; `Open` gives a `Device`, which is a `Port`: `Descriptors`, `Claim`, `Release`, `BulkOut`, `BulkIn`, `Close`; the errors `ErrBusy`, `ErrGone`, `ErrTimeout`, `ErrStalled`, `ErrClosed`, `ErrNotSupported` |
| Platforms | Linux (amd64 and arm64). On any other system the module builds and every entry point returns `ErrNotSupported` (`Release` and `Close` return nil) |
| Documentation | the package comment (`go doc`), the doc comment of each name |
| Where it came from | [provenance](../../docs/provenance.md#key-usbccid) |

## What it does and does not do

- **Discovery** reads files under a directory it is given and opens nothing. A candidate has the vendor and product id, the bus and device number, the usbfs node path derived from them, the physical port path as sysfs names it, and the interface number and endpoint addresses of the CCID interface (class `0x0B`). A filter narrows by vendor id and by port path. sysfs gives no card serial: the caller reads the serial from the card after the claim and compares it before anything else.
- **The claim is plain and exclusive.** If another holder has the interface (a PC/SC service, another program's card daemon, another handle of the same process), the claim fails with `ErrBusy`, and the library does not retry. It never detaches a kernel driver, never resets the device and never reconnects.
- **Only the CCID interface is ever touched.** `Open` reads the descriptors; `Claim` takes only a CCID interface (class `0x0B`) that they hold, and a bulk call goes only to a bulk endpoint of an interface this handle has claimed, in the direction of the call. The other interfaces of a card (a passkey, a one-time code) are never claimed, not even by the kernel on a first transfer. `Open` takes only a character device of the USB major number, and `Candidate.Open` checks that the device is the one `Discover` found. `Open` accepts only simple descriptors and refuses the rest with `ErrBadDescriptors` (or `ErrSeveralConfigurations`): one configuration, every interface at alternate setting 0 only and listed once, as many endpoint descriptors as `bNumEndpoints` says, no endpoint address twice, lengths as declared (the kernel's rules for anything else, such as the active configuration or the fall back to another alternate setting, are not copied; see the package comment). A usual composite security key is simple.
- **A gone device is final.** The first fatal error (unplugged, re-enumerated, failed) is latched; every later call fails with `ErrGone`. The program above drops its session and starts again.
- **Each transfer has a timeout (1 ms to ten minutes, at most 64 KiB), and that is its only bound.** A blocking bulk transfer cannot be cancelled by a context once it started, so the library takes no context. The package comment says what the layer above must not assume after a timeout.
- `Close` releases the interface and the node, and is safe to call twice.

## The CCID reader: `usbccid/ccid`

A reader that sends **one command APDU** to the card and returns its **response APDU**, over the bulk endpoints of a CCID interface that a `Port` has claimed (import path `github.com/truvity/atoom/key/usbccid/ccid`). It is a package of this module, not a module of its own: it adds no dependency, and it has no system call and no build constraint, because every byte goes through a `Port`, so it builds on every system and a test drives it with a fake `Port`. It knows no card application, no status word and no `61xx` continuation; those belong to the layer above.

| | |
| --- | --- |
| API | `Open(port, Options)` gives a `Reader`: `SlotStatus`, `PowerOn` (returns the answer to reset), `PowerOff`, `Transmit(apdu, budget)`, `Close`, `Info` |
| Accepts | exactly one slot; short APDU or extended APDU exchange level (character and TPDU level are refused); automatic parameter configuration from the ATR; a declared voltage; a class descriptor of the 54-byte form that agrees with the interface. Anything else is refused by name (`ErrSeveralSlots`, `ErrExchangeLevel`, `ErrParameters`, `ErrVoltage`, `ErrBadClass`, `ErrNoInterface`, `ErrSeveralInterfaces`) |
| Budget | each call takes the total time of the exchange, time-extension messages (a card waiting for a touch) included, at most `MaxBudget` (90 s); no context is taken, because a blocking transfer cannot be cancelled |
| Lost session | a timeout, a failed transfer, a short write, a protocol error, a budget that ran out, or a stall, a gone device or a closed port of the `Port` ends the session: `ErrSessionLost` wraps the cause, every later call returns it without touching the port, and the caller closes and opens again. It never aborts, resynchronises, retries or sends a control transfer |
| Limits | `MaxShortCommandAPDU` 261, `MaxCommandAPDU` 65544, `MaxResponseAPDU` 65538, `MaxStaleAnswers` 3, `MaxATR` 33, `MaxDrainReads` 8, `MaxDrainTime` 250 ms, `DrainReadTimeout` 20 ms, `MaxBudget` 90 s, `MinMessageLength` 271, `MaxMessageLength` 65536; each refusal names its limit |
| Card-level failures | a mute card, a parity or protocol error, no card, a command the reader does not support and the rest are `*CommandError` (the status bytes, never a byte of an APDU), each matching a named error with `errors.Is` |
| Sessions | sequence numbers come from one random-seeded counter of the process; the first exchange of a session drains the IN pipe (only reads, at most 8 reads, 250 ms, 20 ms on a quiet device) so that a late answer of an earlier session is not taken for an answer. After any error the card may have carried out the command |
| Secrets | commands and responses pass through two buffers of the reader that are cleared before each call returns; the caller's slices, the `Port`'s and the kernel's copies are not the reader's to clear |
| Not known | whether a power cycle ends a verified PIN state on a given card: that is measured on a real card, and nothing here claims it |

The package comment (`go doc`) holds the rules and the section "What the layer above must not assume".

## Tests

A test never opens a node under `/dev/bus/usb` and never reads the real `/sys/bus/usb`: `Discover` takes a made-up tree under `t.TempDir()`, `Open` is given only paths under it, and the layers above use a fake `Port`; the tests of `ccid` run against one, byte for byte, with no device.
