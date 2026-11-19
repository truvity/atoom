// Command dynamodb is the thin main of the DynamoDB item driver: it serves the item port over a DynamoDB table, on a UNIX socket, and
// does nothing else. The library (github.com/truvity/atoom/store/dynamodb) is the driver; a program that needs the driver inside its own
// process (a Lambda function) links the library instead and needs no socket.
//
//	dynamodb serve --socket PATH [--table NAME] [--create-table] [--socket-mode 0600]
//	dynamodb create-table [--table NAME]
//	dynamodb check [--table NAME]
//	dynamodb version
//
// The region, the credentials and an endpoint of another place (AWS_ENDPOINT_URL_DYNAMODB, for DynamoDB Local) come from the AWS SDK's
// own configuration chain: the environment, the shared files, the role of the host. The socket is mode 0600 by default, so that the file
// system decides who may call: any process that can open the socket may read and write every item. The exit status is 0 on success, 1 on a
// failure, 2 on a usage error.
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

	"github.com/aws/aws-sdk-go-v2/config"
	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/truvity/atoom/store/dynamodb"
)

// version is stamped at build time (-ldflags "-X main.version=..."); a build without it reports the module version.
var version = ""

// client is what the main needs of DynamoDB.
type client interface {
	dynamodb.API
	dynamodb.TableAPI
}

// newClient makes the DynamoDB client from the SDK's default configuration; a test replaces it.
var newClient = func(ctx context.Context) (client, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return awsdynamodb.NewFromConfig(cfg), nil
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: dynamodb serve|create-table|check|version")
		return 2
	}
	switch args[0] {
	case "version":
		_, _ = fmt.Fprintln(stdout, versionString())
		return 0
	case "serve":
		return serve(args[1:], stderr)
	case "create-table":
		return table(args[1:], stderr, true)
	case "check":
		return table(args[1:], stderr, false)
	default:
		_, _ = fmt.Fprintf(stderr, "dynamodb: unknown command %q (serve, create-table, check or version)\n", args[0])
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

// table makes the table (create-table) or checks its keys (check).
func table(args []string, stderr io.Writer, create bool) int {
	name := "dynamodb check"
	if create {
		name = "dynamodb create-table"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	tbl := fs.String("table", dynamodb.DefaultTable, "the name of the table")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	api, err := newClient(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "dynamodb: the AWS configuration:", err)
		return 1
	}
	if create {
		err = dynamodb.EnsureTable(ctx, api, *tbl)
	} else {
		err = dynamodb.CheckTable(ctx, api, *tbl)
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "dynamodb:", err)
		return 1
	}
	return 0
}

func serve(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("dynamodb serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", "", "the UNIX socket to serve on (required)")
	tbl := fs.String("table", dynamodb.DefaultTable, "the name of the table")
	create := fs.Bool("create-table", false, "make the table first if it is absent, and enable its time to live (for a test or a first start)")
	mode := fs.String("socket-mode", "0600", "the mode of the socket, octal")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *socket == "" || fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "dynamodb serve: --socket is required")
		return 2
	}
	m, err := strconv.ParseUint(*mode, 8, 32)
	if err != nil || m > 0o777 {
		_, _ = fmt.Fprintln(stderr, "dynamodb serve: --socket-mode is not an octal mode")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	api, err := newClient(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "dynamodb: the AWS configuration:", err)
		return 1
	}
	if *create {
		if err := dynamodb.EnsureTable(ctx, api, *tbl); err != nil {
			_, _ = fmt.Fprintln(stderr, "dynamodb:", err)
			return 1
		}
	}
	host := dynamodb.Host{Serve: func(ctx context.Context, path string, h http.Handler) error {
		return serveUnix(ctx, *socket, os.FileMode(m), path, h) //nolint:gosec // m is at most 0o777
	}}
	if err := dynamodb.Run(ctx, dynamodb.Options{DynamoDB: api, Table: *tbl}, host); err != nil {
		_, _ = fmt.Fprintln(stderr, "dynamodb:", err)
		return 1
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
