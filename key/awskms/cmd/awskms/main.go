// Command awskms is the thin main of the AWS KMS key driver: it serves the key port over KMS keys, on a UNIX socket, and does nothing else.
// The library (github.com/truvity/atoom/key/awskms) is the driver; a program that needs the driver inside its own process (a Lambda
// function) links the library instead and needs no socket.
//
//	awskms serve --socket PATH [--alias-prefix atoom] [--create-keys]
//	             [--allow-uid UID]... [--allow-any-uid] [--socket-gid GID] [--socket-mode MODE]
//	awskms version
//
// The region, the credentials and an endpoint of another place (AWS_ENDPOINT_URL_KMS) come from the AWS SDK's own configuration chain:
// the environment, the shared files, the role of the host. The socket is guarded
// (github.com/truvity/atoom/socket, docs/socket-guard.md): mode 0600 and the uid of the process by default, so that no other process may
// sign with the keys; --allow-uid names the callers, and a caller outside the set is refused with the reason caller. The exit status is 0
// on success, 1 on a failure, 2 on a usage error.
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

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/truvity/atoom/key/awskms"
	atoomsocket "github.com/truvity/atoom/socket"
)

// version is stamped at build time (-ldflags "-X main.version=..."); a build without it reports the module version.
var version = ""

// newClient makes the KMS client from the SDK's default configuration; a test replaces it.
var newClient = func(ctx context.Context) (awskms.API, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return kms.NewFromConfig(cfg), nil
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: awskms serve|version")
		return 2
	}
	switch args[0] {
	case "version":
		_, _ = fmt.Fprintln(stdout, versionString())
		return 0
	case "serve":
		return serve(args[1:], stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "awskms: unknown command %q (serve or version)\n", args[0])
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
	fs := flag.NewFlagSet("awskms serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var guard atoomsocket.Flags
	guard.Register(fs)
	socket := fs.String("socket", "", "the UNIX socket to serve on (required)")
	prefix := fs.String("alias-prefix", awskms.DefaultAliasPrefix, "the prefix of the aliases of the keys: alias/<prefix>/<purpose>")
	create := fs.Bool("create-keys", false, "let Ensure make the key of a purpose that has none (CreateKey, CreateAlias, tags)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *socket == "" || fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "awskms serve: --socket is required")
		return 2
	}
	cfg, err := guard.Config(*socket)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "awskms serve:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	api, err := newClient(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "awskms: the AWS configuration:", err)
		return 1
	}
	host := awskms.Host{Serve: func(ctx context.Context, path string, h http.Handler) error {
		return atoomsocket.Serve(ctx, cfg, path, h)
	}}
	if err := awskms.Run(ctx, awskms.Options{KMS: api, AliasPrefix: *prefix, CreateKeys: *create}, host); err != nil {
		_, _ = fmt.Fprintln(stderr, "awskms:", err)
		if errors.Is(err, awskms.ErrAliasPrefix) {
			return 2
		}
		return 1
	}
	return 0
}
