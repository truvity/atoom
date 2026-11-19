// Command yubikeypiv is the thin main of the YubiKey PIV key driver: it serves the key port over the keys of a card, on a UNIX socket,
// and the unlock service on a local admin socket, and does nothing else. The library (github.com/truvity/atoom/key/yubikeypiv) is the
// driver; a larger system that needs a peer check, an owner, a signed key plan or a command line of its own wraps the library instead
// (sluis does). This main takes no key plan, so it releases no PIN to a peer: the PIN is delivered on the admin socket alone.
//
//	yubikeypiv serve --socket PATH --admin-socket PATH --node NAME --environment development|staging|production
//	                 [--boundary NAME] [--serial N] [--state-dir DIR] [--admin-uid N]
//	                 [--allow-uid UID]... [--allow-any-uid] [--socket-gid GID] [--socket-mode MODE]
//	yubikeypiv check [--serial N] [--environment E]
//	yubikeypiv version
//
// The key socket is guarded (github.com/truvity/atoom/socket, docs/socket-guard.md): mode 0600 and the uid of the process by default;
// the guard flags do not apply to the admin socket, which admits --admin-uid only. The driver holds the card exclusively and starts locked: it has no PIN until an administrator delivers it on the admin socket. A build
// without the build tag atoom_card has the same commands and refuses to open a card; the executable that talks to a card is built
// natively with the tag and CGO_ENABLED=1. The exit status is 0 on success, 1 on a refusal or a failure, 2 on a usage error.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/truvity/atoom/key/yubikeypiv"
	atoomsocket "github.com/truvity/atoom/socket"
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
	var guard atoomsocket.Flags
	guard.Register(fs)
	socket := fs.String("socket", "", "the UNIX socket of the key-store service (required)")
	adminSocket := fs.String("admin-socket", "", "the absolute path of the local admin socket that takes the PIN (required)")
	node := fs.String("node", "", "the node whose card this is (required)")
	env := fs.String("environment", "", "development, staging or production (required); only development makes a key with the factory management key")
	boundary := fs.String("boundary", "", "the boundary of the node, which a PIN release names")
	serial := fs.Uint("serial", 0, "the serial number of the card the driver is bound to; required outside development (in development, none means the only card)")
	state := fs.String("state-dir", "", "the driver's own state directory, mode 0700: it holds the stop that a refused released PIN leaves")
	adminUID := fs.Uint("admin-uid", 0, "the uid that may use the admin socket (the socket is mode 0600 as well)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *env == "" || *socket == "" || *adminSocket == "" || *node == "" || fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "yubikeypiv serve: --environment, --socket, --admin-socket and --node are required")
		return 2
	}
	cfg, err := guard.Config(*socket)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "yubikeypiv serve:", err)
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
		return atoomsocket.Serve(ctx, cfg, path, h)
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
