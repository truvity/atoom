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

// ErrTimeout is what a bulk transfer returns when its timeout ran out. The error also wraps the errno (ETIMEDOUT on Linux).
// The handle can still be called, but the stream of that endpoint is out of step: usbfs copies a read to the caller only when the
// transfer succeeds, so what had already arrived is lost. The layer above must treat its session as lost and close and open again;
// this layer never aborts or resynchronises for it. A timeout does not say whether the device did the work: for a transfer to a
// card, the caller above decides what a late answer means.
var ErrTimeout = errors.New("usbccid: the transfer timed out")

// ErrStalled is returned by a bulk call when the endpoint stalled (the kernel answered EPIPE), and by every later bulk call of the same
// handle without asking the kernel: the first stall is latched for the handle. This layer never clears a halt, so a stalled endpoint
// would answer a bare EPIPE for ever; a stall ends the use of the handle: close it and open again. Release and Close still return nil,
// ErrGone wins if the device then goes, and ErrClosed wins after Close. The error also wraps the errno (EPIPE on Linux).
var ErrStalled = errors.New("usbccid: the endpoint stalled, and this layer never clears a halt: close the handle and open again")

// ErrClosed is returned by every call after Close, except Release and Close, which return nil.
var ErrClosed = errors.New("usbccid: the device is closed")

// ErrNotSupported is returned by every entry point on a system that is not Linux on amd64 or arm64, except Release and Close of a Device, which return
// nil there as on a Device that was never opened. The package builds on every
// system so that a program that links it compiles everywhere, but only Linux has usbfs and sysfs.
var ErrNotSupported = errors.New("usbccid: not supported on this system: direct USB access needs Linux (usbfs) on amd64 or arm64")

// The refusals below are errors of the arguments or of the state of the handle: the kernel was not asked, nothing was latched, and none
// of them is ErrBusy.

// ErrNoSuchInterface is returned by Claim for an interface number that the descriptors of the device do not hold, or that is not a
// CCID interface (class 0x0B) with both a bulk OUT and a bulk IN endpoint. Only the CCID interface of a card is ever claimed; its other interfaces (a passkey or a one-time-code
// interface) are never touched.
var ErrNoSuchInterface = errors.New("usbccid: the device has no CCID interface with this number")

// ErrAlreadyClaimed is returned by Claim of an interface that this handle holds already. The kernel is not asked (its claim is per
// file descriptor, so it would answer success again), and the claim stays as it was. Two readers on one Port are refused with it,
// where the second would otherwise share the first one's claim and its pipe.
var ErrAlreadyClaimed = errors.New("usbccid: this handle holds the interface already")

// ErrNotClaimed is returned by a bulk call on an endpoint of a CCID interface that this handle has not claimed. The kernel would
// claim a free interface by itself for the first transfer, and this package never lets it.
var ErrNotClaimed = errors.New("usbccid: the endpoint belongs to an interface that is not claimed by this handle")

// ErrBadEndpoint is returned by a bulk call whose endpoint is not a bulk endpoint of an interface this handle has claimed, or whose
// direction does not fit the call (BulkOut takes an OUT address, BulkIn an IN address). An address that is listed by more than one
// interface (or twice by one) is refused too: usbfs would pick an interface by itself and claim it.
var ErrBadEndpoint = errors.New("usbccid: the endpoint is not a bulk endpoint of a claimed interface, or its direction does not fit the call")

// ErrBadLength is returned by a bulk call with a length the layer refuses: more than 64 KiB, or an empty BulkIn.
var ErrBadLength = errors.New("usbccid: the length of the transfer is refused (at most 64 KiB; a read needs a buffer)")

// ErrNotUSBNode is returned by Open for a path that is not a USB device node: not a character device with the major number of the
// USB nodes, a symbolic link, or a node that changed while it was opened. The path was not opened for use.
var ErrNotUSBNode = errors.New("usbccid: not a USB device node")

// ErrWrongDevice is returned by Candidate.Open when the device at the node is not the one Discover found: the device number was
// reused between the two (unplug, then another device plugged). The descriptor is closed.
var ErrWrongDevice = errors.New("usbccid: the device at this node is not the one that was found")

// ErrSeveralConfigurations is returned by Open for a device whose device descriptor declares other than one configuration. The kernel
// knows only the active configuration, and usbfs does not say which one that is, so this layer cannot know which interfaces and
// endpoints the kernel would claim; it does not choose between configurations and never selects one, and nothing can be done with
// such a device. It is raised at Open and nowhere else.
var ErrSeveralConfigurations = errors.New("usbccid: the device declares several configurations, and this layer does not choose between them")

// ErrBadDescriptors is returned by Open when the node gives no descriptors, or they are not simple: this layer accepts one
// configuration whose length and bNumInterfaces are as declared, every interface at alternate setting 0 only and listed once, as many
// endpoint descriptors after each interface as its bNumEndpoints, no endpoint address twice, at most 32 interfaces and no interface
// number of 64 or more (usbfs keeps its claims in a 64-bit mask). The error text names the rule that
// failed and holds no byte of the descriptors. A device whose layout is not certain is never used.
var ErrBadDescriptors = errors.New("usbccid: the descriptors of the device are missing or malformed")

// ErrNoProcSelfFd is returned by Open when the node cannot be reopened through /proc/self/fd, which it needs (it opens the node first
// with O_PATH, checks what it is, and reopens that same inode). The node exists; /proc is not mounted or is hidden. It is not an
// error that wraps os.ErrNotExist.
var ErrNoProcSelfFd = errors.New("usbccid: /proc/self/fd is not available")
