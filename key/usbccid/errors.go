package usbccid

import "errors"

// ErrBusy means that the interface cannot be claimed because another holder has it. Usual holders are a PC/SC service (a smart-card
// daemon such as pcscd), another program's card daemon (the one that gpg starts, for example), another process that uses this
// package, or a kernel driver bound to the interface. The claim is plain: nothing is detached, so the holder keeps the interface,
// and this package does not retry. The caller decides whether to ask the person to stop the other program, to wait, or to give up.
// An error from Device.Claim wraps ErrBusy and also the errno (errors.Is(err, unix.EBUSY) holds on Linux).
var ErrBusy = errors.New("usbccid: the interface is busy: another process holds it")

// ErrGone means that the device is gone: unplugged, re-enumerated (its bus address is no longer this node) or failed for good (the
// kernel answered ENODEV, ESHUTDOWN, EIO, EPROTO or EILSEQ). The Device latches the first such error, and every later call returns
// an error that wraps ErrGone, with no system call. The handle never reconnects by itself: whatever the caller built on it (a
// session, a PIN it holds) is no longer valid, and the caller must drop it and start again with Discover and Open. An error that
// wraps ErrGone also wraps the errno that caused it.
var ErrGone = errors.New("usbccid: the device is gone (unplugged, re-enumerated or failed): drop the session and start again")

// ErrTimeout is what a bulk transfer returns when its timeout ran out. The error also wraps the errno (ETIMEDOUT on Linux). The
// device stays usable. A timeout does not say whether the device did the work: for a transfer to a card, the caller above decides
// what a late answer means.
var ErrTimeout = errors.New("usbccid: the transfer timed out")

// ErrClosed is returned by every call after Close.
var ErrClosed = errors.New("usbccid: the device is closed")

// ErrNotSupported is returned by every entry point on a system that is not Linux. The package builds on every system so that a
// program that links it compiles everywhere, but only Linux has usbfs and sysfs.
var ErrNotSupported = errors.New("usbccid: not supported on this system: direct USB access needs Linux (usbfs)")
