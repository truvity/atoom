package usbccid

import "errors"

// ErrBusy means that the interface cannot be claimed because another holder has it. Usual holders are a PC/SC service (a smart-card
// daemon such as pcscd), another program's card daemon (the one that gpg starts, for example), another process that uses this
// package, another handle of the same process (a second Device on the same node), or a kernel driver bound to the interface. The
// claim is plain: nothing is detached, so the holder keeps the interface, and this package does not retry. The caller decides whether
// to ask the person to stop the other program, to wait, or to give up. An error from Device.Claim wraps ErrBusy and also the errno
// (errors.Is(err, unix.EBUSY) holds on Linux).
var ErrBusy = errors.New("usbccid: the interface is busy: another process holds it")

// ErrGone means that the device is gone: unplugged, re-enumerated (its bus address is no longer this node) or failed for good (the
// kernel answered ENODEV, ESHUTDOWN, EIO, EPROTO or EILSEQ, or counted more bytes than a buffer holds). The Device latches the
// first such error, and every later call except Release and Close returns an error that wraps ErrGone, with no system call; Release
// and Close return nil. ErrClosed wins over ErrGone after Close. The handle never reconnects by itself: whatever the caller built on
// it (a session, a PIN it holds) is no longer valid, and the caller must drop it and start again with Discover and Open. An error
// that wraps ErrGone also wraps the cause. A reset of the device by another process is not noticed.
var ErrGone = errors.New("usbccid: the device is gone (unplugged, re-enumerated or failed): drop the session and start again")

// ErrTimeout is what a bulk transfer returns when its timeout ran out. The error also wraps the errno (ETIMEDOUT on Linux). The
// device stays usable. A timeout does not say whether the device did the work: for a transfer to a card, the caller above decides
// what a late answer means.
var ErrTimeout = errors.New("usbccid: the transfer timed out")

// ErrClosed is returned by every call after Close, except Release and Close, which return nil.
var ErrClosed = errors.New("usbccid: the device is closed")

// ErrNotSupported is returned by every entry point on a system that is not Linux on amd64 or arm64. The package builds on every
// system so that a program that links it compiles everywhere, but only Linux has usbfs and sysfs.
var ErrNotSupported = errors.New("usbccid: not supported on this system: direct USB access needs Linux (usbfs) on amd64 or arm64")

// The refusals below are errors of the arguments: the kernel was not asked, nothing was latched, and none of them is ErrBusy.

// ErrNoSuchInterface is returned by Claim for an interface number that the descriptors of the device do not hold, or that is not a
// CCID interface (class 0x0B). Only the CCID interface of a card is ever claimed; its other interfaces (a passkey or a one-time-code
// interface) are never touched.
var ErrNoSuchInterface = errors.New("usbccid: the device has no CCID interface with this number")

// ErrNotClaimed is returned by a bulk call on an endpoint of a CCID interface that this handle has not claimed. The kernel would
// claim a free interface by itself for the first transfer, and this package never lets it.
var ErrNotClaimed = errors.New("usbccid: the endpoint belongs to an interface that is not claimed by this handle")

// ErrBadEndpoint is returned by a bulk call whose endpoint is not a bulk endpoint of an interface this handle has claimed, or whose
// direction does not fit the call (BulkOut takes an OUT address, BulkIn an IN address).
var ErrBadEndpoint = errors.New("usbccid: the endpoint is not a bulk endpoint of a claimed interface, or its direction does not fit the call")

// ErrBadLength is returned by a bulk call with a length the layer refuses: more than 64 KiB, or an empty BulkIn.
var ErrBadLength = errors.New("usbccid: the length of the transfer is refused (at most 64 KiB; a read needs a buffer)")

// ErrNotUSBNode is returned by Open for a path that is not a USB device node: not a character device with the major number of the
// USB nodes, a symbolic link, or a node that changed while it was opened. The path was not opened for use.
var ErrNotUSBNode = errors.New("usbccid: not a USB device node")

// ErrWrongDevice is returned by Candidate.Open when the device at the node is not the one Discover found: the device number was
// reused between the two (unplug, then another device plugged). The descriptor is closed.
var ErrWrongDevice = errors.New("usbccid: the device at this node is not the one that was found")
