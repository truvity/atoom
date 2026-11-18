// Command yubikeypiv is the thin main of the YubiKey PIV key driver: it serves the key port over the keys of a card, on a UNIX socket,
// and the unlock service on a local admin socket, and does nothing else. The library (github.com/truvity/atoom/key/yubikeypiv) is the
// driver; a larger system that needs a peer check, an owner, a signed key plan or a command line of its own wraps the library instead
// (sluis does). This main takes no key plan, so it releases no PIN to a peer: the PIN is delivered on the admin socket alone.
//
//	yubikeypiv serve --socket PATH --admin-socket PATH --node NAME --environment development|staging|production
//	                 [--boundary NAME] [--serial N] [--state-dir DIR] [--socket-mode 0600] [--admin-uid N]
//	yubikeypiv check [--serial N] [--environment E]
//	yubikeypiv version
//
// The driver holds the card exclusively and starts locked: it has no PIN until an administrator delivers it on the admin socket. A build
// without the build tag atoom_card has the same commands and refuses to open a card; the executable that talks to a card is built
// natively with the tag and CGO_ENABLED=1. The exit status is 0 on success, 1 on a refusal or a failure, 2 on a usage error.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"syscall"
	"time"

	"github.com/truvity/atoom/key/yubikeypiv"
)

// version is stamped at build time (-ldflags "-X main.version=..."); a build without it reports the module version.
var version = ""

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: yubikeypiv serve|check|version")
		return 2
	}
	switch args[0] {
	case "version":
		_, _ = fmt.Fprintln(stdout, versionString())
		return 0
	case "serve":
		return serve(args[1:], stderr)
	case "check":
		return check(args[1:], stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "yubikeypiv: unknown command %q (serve, check or version)\n", args[0])
		return 2
	}
}

func versionString() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
		return bi.Main.Version
	}
	return "(devel)"
}

// fail prints the error and returns the exit status: 2 for a problem of the command line, 1 otherwise.
func fail(stderr io.Writer, err error) int {
	_, _ = fmt.Fprintln(stderr, "yubikeypiv:", err)
	if errors.Is(err, yubikeypiv.ErrEnvironment) || errors.Is(err, yubikeypiv.ErrSerial) {
		return 2
	}
	return 1
}

func serve(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("yubikeypiv serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", "", "the UNIX socket of the key-store service (required)")
	adminSocket := fs.String("admin-socket", "", "the absolute path of the local admin socket that takes the PIN (required)")
	node := fs.String("node", "", "the node whose card this is (required)")
	env := fs.String("environment", "", "development, staging or production (required); only development makes a key with the factory management key")
	boundary := fs.String("boundary", "", "the boundary of the node, which a PIN release names")
	serial := fs.Uint("serial", 0, "the serial number of the card the driver is bound to; required outside development (in development, none means the only card)")
	state := fs.String("state-dir", "", "the driver's own state directory, mode 0700: it holds the stop that a refused released PIN leaves")
	mode := fs.String("socket-mode", "0600", "the mode of the key-store socket, octal")
	adminUID := fs.Uint("admin-uid", 0, "the uid that may use the admin socket (the socket is mode 0600 as well)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *env == "" || *socket == "" || *adminSocket == "" || *node == "" || fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "yubikeypiv serve: --environment, --socket, --admin-socket and --node are required")
		return 2
	}
	m, err := strconv.ParseUint(*mode, 8, 32)
	if err != nil || m > 0o777 {
		_, _ = fmt.Fprintln(stderr, "yubikeypiv serve: --socket-mode is not an octal mode")
		return 2
	}
	if *serial > 1<<32-1 {
		_, _ = fmt.Fprintln(stderr, "yubikeypiv serve: --serial is not a serial number")
		return 2
	}
	// The environment is checked before anything is opened: no socket, no card.
	if err := yubikeypiv.Floor(*env); err != nil {
		return fail(stderr, err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	host := yubikeypiv.Host{Serve: func(ctx context.Context, path string, h http.Handler) error {
		return serveUnix(ctx, *socket, os.FileMode(m), path, h) //nolint:gosec // m is at most 0o777
	}}
	uid := uint32(*adminUID) //nolint:gosec // a uid fits 32 bits
	admin := yubikeypiv.AdminConfig{Socket: *adminSocket, Allow: func(peer uint32) bool { return peer == uid }}
	opts := yubikeypiv.Options{Environment: *env, Node: *node, Boundary: *boundary, Serial: uint32(*serial), StateDir: *state}
	if err := yubikeypiv.Run(ctx, opts, host, admin); err != nil {
		return fail(stderr, err)
	}
	return 0
}

// check opens the card, applies the firmware floor and verifies the attestation of every key it holds. It writes nothing. The card is
// exclusive, so it cannot run beside a driver that serves.
func check(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("yubikeypiv check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	env := fs.String("environment", yubikeypiv.Staging, "development, staging or production; check makes no key, so it only has to be a real one")
	serial := fs.Uint("serial", 0, "the serial number of the card; required outside development")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 || *serial > 1<<32-1 {
		_, _ = fmt.Fprintln(stderr, "yubikeypiv check: unexpected arguments")
		return 2
	}
	if err := yubikeypiv.Check(yubikeypiv.Options{Environment: *env, Serial: uint32(*serial)}); err != nil {
		return fail(stderr, err)
	}
	return 0
}

// serveUnix serves h at path on a UNIX socket until ctx ends: HTTP/1.1 and HTTP/2 without TLS. A stale socket file is replaced; a live
// one, a symlink or another kind of file is refused. The socket has its mode from the moment it is reachable (it is made under a
// temporary name and renamed).
func serveUnix(ctx context.Context, socket string, mode os.FileMode, path string, h http.Handler) error {
	ln, err := listenUnix(socket, mode)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle(path, h)
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	srv := &http.Server{Handler: mux, Protocols: &protocols, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	defer func() { _ = os.Remove(socket) }()
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		_ = srv.Close()
	}
	return nil
}

func listenUnix(socket string, mode os.FileMode) (net.Listener, error) {
	if fi, err := os.Lstat(socket); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket", socket)
		}
		if c, derr := net.DialTimeout("unix", socket, time.Second); derr == nil {
			_ = c.Close()
			return nil, fmt.Errorf("%s is in use", socket)
		}
		if err := os.Remove(socket); err != nil {
			return nil, fmt.Errorf("remove the stale socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	tmp := fmt.Sprintf("%s.tmp-%d", socket, os.Getpid())
	_ = os.Remove(tmp)
	ln, err := net.Listen("unix", tmp)
	if err != nil {
		return nil, err
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := os.Chmod(tmp, mode); err != nil {
		_ = ln.Close()
		_ = os.Remove(tmp)
		return nil, err
	}
	if err := os.Rename(tmp, socket); err != nil {
		_ = ln.Close()
		_ = os.Remove(tmp)
		return nil, err
	}
	return ln, nil
}
