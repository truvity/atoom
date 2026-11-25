// Command software is the thin main of the software key driver: it serves the key port over keys held in files, on a UNIX
// socket, and does nothing else. The library (github.com/truvity/atoom/key/software) is the driver; a larger system that needs names for
// its callers or a command line of its own wraps the library instead (sluis does, until its host is deleted).
//
//	software serve --socket PATH --state-dir DIR --purposes FILE
//	               [--allow-uid UID]... [--allow-any-uid] [--socket-gid GID] [--socket-mode MODE]
//	software check --state-dir DIR --purposes FILE
//	software version  (also --version)
//
// --purposes names the JSON file in which the host declares its purposes: a name, an algorithm (ES384, ES256, RS256 or ECDH-P384) and
// the operations (sign or agree) of each (github.com/truvity/atoom/key/purpose). The driver holds a key for those names and no other.
//
// The socket is guarded (github.com/truvity/atoom/socket, docs/socket-guard.md): mode 0600 and the uid of the process by default, so
// that no other process may use the keys; --allow-uid names the callers, and a caller outside the set is refused with the reason
// caller. The exit status is 0 on success, 1 on a refusal or a failure, 2 on a usage error.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/truvity/atoom/key/purpose"
	"github.com/truvity/atoom/key/software"
	atoomsocket "github.com/truvity/atoom/socket"
)

// version is stamped at build time (-ldflags "-X main.version=..."); a build without it reports the module version.
var version = ""

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: software serve|check|version")
		return 2
	}
	switch args[0] {
	case "version", "--version":
		_, _ = fmt.Fprintln(stdout, versionString())
		return 0
	case "serve":
		return serve(args[1:], stderr)
	case "check":
		return check(args[1:], stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "software: unknown command %q (serve, check or version)\n", args[0])
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

func serve(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("software serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var guard atoomsocket.Flags
	guard.Register(fs)
	socket := fs.String("socket", "", "the UNIX socket to serve on (required)")
	state := fs.String("state-dir", "", "the directory of the key files, mode 0700 (required)")
	purposes := fs.String("purposes", "", "the JSON file of the purposes the host declares (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *socket == "" || *state == "" || *purposes == "" || fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "software serve: --socket, --state-dir and --purposes are required")
		return 2
	}
	decls, err := readPurposes(*purposes)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "software serve:", err)
		return 2
	}
	cfg, err := guard.Config(*socket)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "software serve:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	host := software.Host{Serve: func(ctx context.Context, path string, h http.Handler) error {
		return atoomsocket.Serve(ctx, cfg, path, h)
	}}
	opts := software.Options{StateDir: *state, Purposes: decls}
	if err := software.Run(ctx, opts, host); err != nil {
		_, _ = fmt.Fprintln(stderr, "software:", err)
		return 1
	}
	return 0
}

func check(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("software check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	state := fs.String("state-dir", "", "the directory of the key files (required)")
	purposes := fs.String("purposes", "", "the JSON file of the purposes the host declares (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *state == "" || *purposes == "" || fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "software check: --state-dir and --purposes are required")
		return 2
	}
	decls, err := readPurposes(*purposes)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "software check:", err)
		return 2
	}
	if err := software.Check(*state, decls); err != nil {
		_, _ = fmt.Fprintln(stderr, "software:", err)
		return 1
	}
	return 0
}

// readPurposes reads the declaration file and checks it, and returns the declarations. A file that cannot be read or is not a
// declaration is a refusal of the command line (status 2), and the message names a path and a word, never a value of the file.
func readPurposes(path string) ([]purpose.Declaration, error) {
	f, err := os.Open(path) //nolint:gosec // the path is the operator's flag
	if err != nil {
		return nil, fmt.Errorf("--purposes: %s cannot be read", path)
	}
	defer func() { _ = f.Close() }()
	set, err := purpose.Parse(f)
	if err != nil {
		return nil, fmt.Errorf("--purposes %s: %w", path, err)
	}
	return set.List(), nil
}
