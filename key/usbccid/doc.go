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
//   - ErrNoSuchInterface, ErrNotClaimed, ErrBadEndpoint, ErrBadLength: a call refused for its arguments, before the kernel was asked.
//   - ErrNotUSBNode: Open was given a path that is not a USB device node. ErrWrongDevice: Candidate.Open found another device at
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
// # What the layer above must not assume
//
//   - A transfer that timed out is abandoned, and what it had already moved is lost. A late answer may arrive on the next read, so
//     the layer above matches the sequence number of its own messages, and should treat its session as lost after a timeout.
//   - The layer never clears a halt (a stalled endpoint): a stall is final for this handle.
//   - A read buffer must hold the largest message expected: a larger one is lost with an overflow error. The maximum packet size of
//     an endpoint is not read here.
//   - A message whose length is an exact multiple of the packet size is followed by an empty packet, which the next read returns as
//     zero bytes.
//   - A reset of the device by another process is not noticed until a call fails.
//
// # Safety rule for tests
//
// A test of this package or of a program above it must never open a node under /dev/bus/usb and never read the real
// /sys/bus/usb tree: a machine that runs the tests may have real security keys plugged in. Tests give Discover a made-up tree
// under t.TempDir(), give Open only paths under t.TempDir(), and use a fake Port for everything above.
package usbccid
