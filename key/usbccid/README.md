# key/usbccid

The USB layer under a smart-card driver that speaks CCID over direct USB ([record 0026](../../docs/decisions/0026-one-yubikey-driver-over-direct-usb.md)): it finds the devices that have a CCID interface, opens one usbfs node, claims one interface and moves bulk transfers, each with a timeout. Pure Go, cgo off. It knows no CCID message and no card application; those are built on it.

| | |
| --- | --- |
| Go module | `github.com/truvity/atoom/key/usbccid` (requires `golang.org/x/sys` and no atoom module) |
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

## Tests

A test never opens a node under `/dev/bus/usb` and never reads the real `/sys/bus/usb`: `Discover` takes a made-up tree under `t.TempDir()`, `Open` is given only paths under it, and the layers above use a fake `Port`.
