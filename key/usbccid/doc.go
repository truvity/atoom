// Package usbccid is the USB layer under a smart-card driver that speaks CCID over direct USB: it finds the devices that have a
// CCID interface, opens one usbfs node, claims one interface and moves bulk transfers, each with a timeout. It is pure Go (cgo off,
// the standard library and golang.org/x/sys only) and knows nothing of CCID messages, of APDUs or of any card application. The
// CCID reader and the card applications are built on it (record 0026).
//
// # What it offers
//
//   - Discover reads a sysfs USB tree and lists the devices with a CCID interface (interface class 0x0B). It opens nothing and
//     sends nothing. The directory to read is a parameter; DiscoverHost reads the real tree of the machine.
//   - Open opens the usbfs node of one device (/dev/bus/usb/BBB/DDD) once and keeps the file descriptor. The Device it returns is
//     a Port: Descriptors, Claim, Release, BulkOut, BulkIn and Close. Port is also the seam of the tests above this package.
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
//   - ErrBusy: the interface is held by another process.
//   - ErrGone: the device is unplugged, was re-enumerated or failed for good. The Device latches the first such error, and every
//     later call returns an error that wraps ErrGone without touching the kernel. The handle never reconnects: the program above
//     drops everything it knew of the card (its session, a PIN it held) and starts again with Discover and Open.
//   - ErrTimeout: a transfer's timeout ran out. The device stays usable.
//   - ErrClosed: the Device was closed.
//   - ErrNotSupported: the system is not Linux. The package still builds there, so that a program that links it compiles for
//     macOS, and every entry point returns this error.
//
// # Deadlines
//
// A bulk transfer blocks in the kernel until it ends or its timeout runs out. A context cannot cancel it once started, and so
// this package takes none: the timeout of each call is the only bound, and the caller derives it from its own deadline before
// each call (and checks its context between calls). Close waits for a transfer in progress, so the timeout bounds that wait too.
//
// # Safety rule for tests
//
// A test of this package or of a program above it must never open a node under /dev/bus/usb and never read the real
// /sys/bus/usb tree: a machine that runs the tests may have real security keys plugged in. Tests give Discover a made-up tree
// under t.TempDir(), give Open only paths under t.TempDir(), and use a fake Port for everything above.
package usbccid
