// Package usbccid is the USB layer under a smart-card driver that speaks CCID over direct USB: it finds the devices that have a
// CCID interface, opens one usbfs node, claims one interface and moves bulk transfers, each with a timeout. It is pure Go (cgo off,
// the standard library and golang.org/x/sys only) and knows nothing of CCID messages, of APDUs or of any card application. The
// CCID reader and the card applications are built on it (record 0026).
//
// # What it offers
//
//   - Discover reads a sysfs USB tree and lists the devices with a CCID interface (interface class 0x0B). It opens nothing and
//     sends nothing. The directory to read is a parameter; DiscoverHost reads the real tree of the machine.
//   - Open opens the usbfs node of one device (/dev/bus/usb/BBB/DDD) once and keeps the file descriptor. It opens only a character
//     device of the USB major number, and reads and parses the descriptors. The Device it returns is a Port: Descriptors, Claim,
//     Release, BulkOut, BulkIn and Close. Port is also the seam of the tests above this package. Candidate.Open also checks that the
//     device it opened is the one Discover found.
//   - Claim takes only a CCID interface that the descriptors hold, and a bulk call goes only to a bulk endpoint of an interface this
//     handle has claimed, in the direction of the call. The kernel's usbfs would claim a free interface by itself for a transfer
//     to one of its endpoints; this package never lets it, so the other interfaces of a card (a passkey, a one-time code) stay
//     untouched.
//
// # What it never does
//
// It never detaches a kernel driver, never resets the device, never selects a configuration or clears a halt, never retries and
// never reconnects. The claim is the plain USBDEVFS_CLAIMINTERFACE: if another process holds the interface, or a kernel driver is
// bound to it, the claim fails with ErrBusy and the interface stays with its holder. The package builds exactly three ioctl
// requests (bulk transfer, claim, release), and a test says so.
//
// # Errors
//
// A caller tells the failures apart with errors.Is:
//
//   - ErrBusy: the interface is held by another process (or by another handle of this one).
//   - ErrGone: the device is unplugged, was re-enumerated or failed for good. The Device latches the first such error, and every
//     later call except Release and Close returns an error that wraps ErrGone without touching the kernel; Release and Close return
//     nil. The handle never reconnects: the program above drops everything it knew of the card (its session, a PIN it held) and
//     starts again with Discover and Open.
//   - ErrTimeout: a transfer's timeout ran out. The device stays usable.
//   - ErrClosed: the Device was closed. After Close, Release and Close return nil and every other call returns ErrClosed (also
//     when the device is gone as well).
//   - ErrNoSuchInterface (also for a number outside 0 to 255), ErrNotClaimed, ErrBadEndpoint (also an address that two interfaces
//     list), ErrBadLength: a call refused before the kernel was asked.
//   - ErrNotUSBNode: Open was given a path that is not a USB device node. ErrBadDescriptors: its descriptors are missing, malformed or not simple (below). ErrSeveralConfigurations: the device declares other than one configuration. ErrNoProcSelfFd: /proc/self/fd is not available. ErrWrongDevice: Candidate.Open found another device at
//     the node than Discover did.
//   - ErrNotSupported: the system is not Linux on amd64 or arm64. The package still builds there, so that a program that links it
//     compiles for macOS, and every entry point returns this error.
//
// # Deadlines
//
// A bulk transfer blocks in the kernel until it ends or its timeout runs out. A context cannot cancel it once started, and so
// this package takes none: the timeout of each call is the only bound, and the caller derives it from its own deadline before
// each call (and checks its context between calls). A timeout is at least 1 ms and at most ten minutes. Descriptors, Release and Close
// wait behind a transfer in progress, so the timeout bounds that wait too. A system call that the kernel interrupted is reported,
// not retried. A Device that is never closed keeps its file descriptor until the process ends.
//
// # Which devices it accepts
//
// This layer serves one known kind of device, and accepts only SIMPLE descriptors; Open refuses all others, with ErrBadDescriptors
// (the text names the rule that failed and holds no byte of the descriptors) or, for the first rule, ErrSeveralConfigurations.
// Simple means: exactly one configuration; its total length is the number of bytes present and its bNumInterfaces the number of
// distinct interfaces listed; every interface descriptor has alternate setting 0, and no interface number is listed twice; each
// interface is followed (before the next interface descriptor) by exactly bNumEndpoints endpoint descriptors; no endpoint address
// appears twice in the configuration, and each is a number 1 to 15 with the reserved bits clear; every descriptor is at least as
// long as its type needs and does not run past the end. Descriptors of other types (class-specific ones, interface associations)
// are skipped by their length, wherever they stand. The reason is that the kernel's rules for what is left open (the active
// configuration, which usbfs does not name; the fall back to another alternate setting; duplicates; endpoints beyond a count)
// would otherwise have to be copied, and any difference would let usbfs claim an interface by itself. A usual composite security key
// (HID interfaces and a CCID interface, each at alternate setting 0 only) is simple.
//
// # What the layer above must not assume
//
//   - A transfer that timed out is abandoned, and what it had already moved is lost. A late answer may arrive on the next read, so
//     the layer above matches the sequence number of its own messages, and should treat its session as lost after a timeout.
//   - A BulkOut that timed out may have sent part of a message: the stream is out of step, and the session is to be treated as lost.
//     BulkOut returns a count, which the caller must compare with the length it gave.
//   - The overflow error (a message longer than the read buffer) and the stall error are ordinary errors: they do not latch, and the
//     layer never clears a halt, so a stalled endpoint stays stalled.
//   - A read buffer must hold the largest message expected: a larger one is lost with an overflow error. The maximum packet size of
//     an endpoint is not read here.
//   - When a message is an exact multiple of the packet size it is followed by an empty packet. A read whose buffer is exactly as
//     long as the message ends before that packet, and the next read returns it as zero bytes; a read with a longer buffer ends on
//     the empty packet in the same call.
//   - Only alternate setting 0 of an interface is known, and the layer never selects another. The interrupt endpoint of the CCID
//     interface is reported by Discover and never used.
//   - The descriptors are a snapshot taken at Open. A reset of the device by another process is not noticed until a call fails.
//   - A device whose descriptors are not simple is refused at Open (see "Which devices it accepts").
//
// # Safety rule for tests
//
// A test of this package or of a program above it must never open a node under /dev/bus/usb and never read the real
// /sys/bus/usb tree: a machine that runs the tests may have real security keys plugged in. Tests give Discover a made-up tree
// under t.TempDir(), give Open only paths under t.TempDir(), and use a fake Port for everything above.
package usbccid
