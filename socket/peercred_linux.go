//go:build linux

package socket

import (
	"fmt"
	"net"
	"syscall"
)

// peerCred reads SO_PEERCRED of a UNIX socket: the pid, uid and gid of the process that connected, as the kernel recorded them at
// connect time (unix(7)).
func peerCred(c *net.UnixConn) (Peer, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return Peer{}, err
	}
	var (
		cred *syscall.Ucred
		serr error
	)
	if err := raw.Control(func(fd uintptr) {
		cred, serr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return Peer{}, err
	}
	if serr != nil {
		return Peer{}, fmt.Errorf("SO_PEERCRED: %w", serr)
	}
	return Peer{UID: cred.Uid, GID: cred.Gid, PID: cred.Pid}, nil
}
