// Command file is the thin main of the Go file driver of the source port: it serves the config source over a directory of signed config
// documents, on a UNIX socket, and does nothing else. The library (github.com/truvity/atoom/source/file) is the driver; a program that
// needs the driver inside its own process links the library instead and needs no socket.
//
//	file serve --socket PATH --dir DIR
//	           [--allow-uid UID]... [--allow-any-uid] [--socket-gid GID] [--socket-mode MODE]
//	file check --dir DIR
//	file version  (also --version)
//
// The socket is guarded (github.com/truvity/atoom/socket, docs/socket-guard.md): mode 0600 and the uid of the process by default;
// --allow-uid names the callers, and a caller outside the set is refused with the reason caller. check opens the directory and says whether
// it can be served; it exits 3 when it cannot. The exit status is 0 on success, 1 on a failure, 2 on a usage error.
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

	"github.com/truvity/atoom/source/file"

	atoomsocket "github.com/truvity/atoom/socket"
)

// version is stamped at build time (-ldflags "-X main.version=..."); a build without it reports the module version.
var version = ""

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: file serve|check|version")
		return 2
	}
	switch args[0] {
	case "version", "--version":
		_, _ = fmt.Fprintln(stdout, versionString())
		return 0
	case "serve":
		return serve(args[1:], stderr)
	case "check":
		return check(args[1:], stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "file: unknown command %q (serve, check or version)\n", args[0])
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
	fs := flag.NewFlagSet("file serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var guard atoomsocket.Flags
	guard.Register(fs)
	socket := fs.String("socket", "", "the UNIX socket to serve on (required)")
	dir := fs.String("dir", "", "the directory of signed config documents, <name>.jws (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *socket == "" || *dir == "" || fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "file serve: --socket and --dir are required")
		return 2
	}
	cfg, err := guard.Config(*socket)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "file serve:", err)
		return 2
	}
	// The directory is opened before the socket exists: no directory, no service.
	d, err := file.Open(*dir)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "file: the config directory:", err)
		return 1
	}
	defer func() { _ = d.Close() }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	host := file.Host{Serve: func(ctx context.Context, path string, h http.Handler) error {
		return atoomsocket.Serve(ctx, cfg, path, h)
	}}
	if err := file.Run(ctx, file.Options{Dir: d}, host); err != nil {
		_, _ = fmt.Fprintln(stderr, "file:", err)
		return 1
	}
	return 0
}

// check opens the directory. A directory that cannot be served is a refusal, not a crash: exit 3, so that a script tells it from a bad flag
// (1 or 2).
func check(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("file check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "", "the directory of signed config documents (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" || fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "file check: --dir is required")
		return 2
	}
	d, err := file.Open(*dir)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "file: check failed:", err)
		return 3
	}
	_ = d.Close()
	_, _ = fmt.Fprintln(stdout, "config directory ok")
	return 0
}
