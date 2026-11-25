// Command s3 is the thin main of the Amazon S3 driver of the blob port: it serves the blob contract over one S3 bucket, under a key
// prefix, on one UNIX socket, and does nothing else. The library (github.com/truvity/atoom/blob/s3) is the driver; a program that needs
// the driver inside its own process links the library instead and needs no socket.
//
//	s3 serve --socket PATH --bucket NAME [--prefix P/] [--pointer NAME]... [--path-style]
//	         [--allow-uid UID]... [--allow-any-uid] [--socket-gid GID] [--socket-mode MODE]
//	s3 version  (also --version)
//
// --pointer (repeatable) declares a pointer: an object whose last path segment is NAME may be overwritten by a conditional Put; without
// it every object is written once. --path-style addresses the bucket in the path rather than the host name (an S3-compatible server on
// one host). The region, the credentials and an endpoint of another place (AWS_ENDPOINT_URL_S3) come from the AWS SDK's own
// configuration chain: the environment, the shared files, the role of the host. The socket is guarded (github.com/truvity/atoom/socket,
// docs/socket-guard.md): mode 0600 and the uid of the process by default; --allow-uid names the callers, and a caller outside the set is
// refused with the reason caller. The exit status is 0 on success, 1 on a failure, 2 on a usage error.
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
	"strings"
	"syscall"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/truvity/atoom/blob/s3"
	atoomsocket "github.com/truvity/atoom/socket"
)

// version is stamped at build time (-ldflags "-X main.version=..."); a build without it reports the module version.
var version = ""

// newClient makes the S3 client from the SDK's default configuration; a test replaces it.
var newClient = func(ctx context.Context, pathStyle bool) (s3.API, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		o.UsePathStyle = pathStyle
		// Checksums only where S3 requires them: the body goes plain, so that a server of S3's protocol that does not take chunked
		// trailers serves it too. The port's own tag is the SHA-384 of the bytes either way.
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	}), nil
}

type names []string

func (n *names) String() string     { return strings.Join(*n, ",") }
func (n *names) Set(v string) error { *n = append(*n, v); return nil }

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: s3 serve|version")
		return 2
	}
	switch args[0] {
	case "version", "--version":
		_, _ = fmt.Fprintln(stdout, versionString())
		return 0
	case "serve":
		return serve(args[1:], stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "s3: unknown command %q (serve or version)\n", args[0])
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
	fs := flag.NewFlagSet("s3 serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var guard atoomsocket.Flags
	guard.Register(fs)
	socket := fs.String("socket", "", "the UNIX socket to serve on (required)")
	bucket := fs.String("bucket", "", "the bucket (required)")
	prefix := fs.String("prefix", "", "the key prefix of every path: empty, or path segments ending in /")
	var pointers names
	fs.Var(&pointers, "pointer", "declare a pointer: an object whose last path segment is NAME may be overwritten (repeatable)")
	pathStyle := fs.Bool("path-style", false, "address the bucket in the path (an S3-compatible server on one host)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *socket == "" || *bucket == "" || fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "s3 serve: --socket and --bucket are required")
		return 2
	}
	cfg, err := guard.Config(*socket)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "s3 serve:", err)
		return 2
	}
	opts := s3.Options{Bucket: *bucket, Prefix: *prefix, Pointers: pointers}
	// The options are checked before the AWS configuration is read: a bad flag is a usage error.
	opts.S3 = noAPI{}
	if _, err := s3.New(opts); err != nil {
		_, _ = fmt.Fprintln(stderr, "s3 serve:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	api, err := newClient(ctx, *pathStyle)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "s3: the AWS configuration:", err)
		return 1
	}
	opts.S3 = api
	host := s3.Host{Serve: func(ctx context.Context, path string, h http.Handler) error {
		return atoomsocket.Serve(ctx, cfg, path, h)
	}}
	if err := s3.Run(ctx, opts, host); err != nil {
		_, _ = fmt.Fprintln(stderr, "s3:", err)
		if errors.Is(err, s3.ErrOptions) {
			return 2
		}
		return 1
	}
	return 0
}

// noAPI stands in for the client while the flags are checked; it is never called.
type noAPI struct{ s3.API }
