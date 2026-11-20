package socket

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/truvity/atoom/common/call"
)

// shutdownGrace bounds the graceful shutdown of a server.
const shutdownGrace = 5 * time.Second

// Config is the configuration of one server on one socket.
type Config struct {
	// Socket is the path of the socket.
	Socket string
	// Mode is the mode of the socket file; 0 means 0600, which lets the owner connect and nobody else.
	Mode os.FileMode
	// GID, when not 0, is the group of the socket file. A socket for a caller of another uid is given a group and the mode 0660, so
	// that the members of the group, and only they, can open it; the process must be a member of the group. It changes who may open
	// the socket and nothing else: the guard still decides, from the peer's uid, who may call. The default, 0, leaves the group the
	// kernel gave.
	GID uint32
	// Allow decides who may call. The zero value admits the uid this process runs as.
	Allow Allow
}

// Serve listens on cfg.Socket, serves h at path (HTTP/1.1 and HTTP/2 without TLS) and returns when ctx ends, after a graceful
// shutdown bounded by 5 seconds. The peer credentials of each connection are read once; every call passes the guard (cfg.Allow)
// before its handler is reached, and a call from a uid that is not admitted is answered permission_denied with the reason "caller",
// without its message being read. A handler reads the caller with [PeerFromContext]. Behind the guard, [call.Inherit] gives the context of
// a handler the correlation id of the request (call.CorrelationID), and the Connect handler gives it the deadline of the caller
// (Connect-Timeout-Ms), so that a handler that calls on with its own context hands both down (docs/call-context.md).
//
// Its signature is the Serve of the Host of the key and store drivers: pass a closure around it.
func Serve(ctx context.Context, cfg Config, path string, h http.Handler) error {
	mode := cfg.Mode
	if mode == 0 {
		mode = 0o600
	}
	ln, err := listen(cfg.Socket, mode, cfg.GID)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle(path, h)
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	g := guard{allow: cfg.Allow, own: uint32(os.Geteuid())} //nolint:gosec // a uid is non-negative
	srv := &http.Server{
		Handler:           g.middleware(newDenyHandlers(), call.Inherit(mux)),
		Protocols:         &protocols,
		ReadHeaderTimeout: 10 * time.Second,
		ConnContext: func(c context.Context, conn net.Conn) context.Context {
			return context.WithValue(c, peerKey{}, peerOf(conn))
		},
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	defer func() { _ = os.Remove(cfg.Socket) }()
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		_ = srv.Close()
	}
	return nil
}

// listen makes the socket. A stale socket file is replaced; a symlink, another kind of file, or a socket that a live server holds is
// refused. The socket is created under a temporary name, given its group and mode, and renamed to its path, so that it is never
// reachable with the wrong mode or group.
func listen(path string, mode os.FileMode, gid uint32) (net.Listener, error) {
	fi, err := os.Lstat(path)
	switch {
	case err == nil:
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			return nil, fmt.Errorf("socket path %q is a symlink", path)
		case fi.Mode()&os.ModeSocket == 0:
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		c, derr := net.DialTimeout("unix", path, time.Second)
		if derr == nil {
			_ = c.Close()
			return nil, fmt.Errorf("%s is in use", path)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("remove the stale socket: %w", err)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("socket path %q: %w", path, err)
	}

	var rnd [6]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return nil, err
	}
	tmp := path + ".tmp-" + hex.EncodeToString(rnd[:])
	ln, err := net.Listen("unix", tmp)
	if err != nil {
		return nil, err
	}
	ul := ln.(*net.UnixListener)
	ul.SetUnlinkOnClose(false)
	if gid != 0 {
		// The group is set before the mode, and both before the final name, so that the socket is never reachable by a group that was
		// not asked for.
		if err := os.Chown(tmp, -1, int(gid)); err != nil {
			_ = ul.Close()
			_ = os.Remove(tmp)
			return nil, fmt.Errorf("set the socket group: %w", err)
		}
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = ul.Close()
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("set the socket mode: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = ul.Close()
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("put the socket in place: %w", err)
	}
	return ln, nil
}
