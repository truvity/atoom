//go:build darwin || freebsd

package socket

import (
	"errors"
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// peerCred reads LOCAL_PEERCRED of a UNIX socket (what getpeereid(3) answers): the uid and the first gid of the process that
// connected. macOS does not give the pid here, so PID is 0.
func peerCred(c *net.UnixConn) (Peer, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return Peer{}, err
	}
	var (
		cred *unix.Xucred
		serr error
	)
	if err := raw.Control(func(fd uintptr) {
		cred, serr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil {
		return Peer{}, err
	}
	if serr != nil {
		return Peer{}, fmt.Errorf("LOCAL_PEERCRED: %w", serr)
	}
	if cred.Ngroups < 1 {
		return Peer{}, errors.New("LOCAL_PEERCRED: no group")
	}
	return Peer{UID: cred.Uid, GID: cred.Groups[0]}, nil
}
