// Command dynamodb is the thin main of the DynamoDB item driver: it serves the item port over a DynamoDB table, on a UNIX socket, and
// does nothing else. The library (github.com/truvity/atoom/store/dynamodb) is the driver; a program that needs the driver inside its own
// process (a Lambda function) links the library instead and needs no socket.
//
//	dynamodb serve --socket PATH [--table NAME] [--create-table]
//	               [--allow-uid UID]... [--allow-any-uid] [--socket-gid GID] [--socket-mode MODE]
//	dynamodb create-table [--table NAME]
//	dynamodb check [--table NAME]
//	dynamodb version
//
// The region, the credentials and an endpoint of another place (AWS_ENDPOINT_URL_DYNAMODB, for DynamoDB Local) come from the AWS SDK's
// own configuration chain: the environment, the shared files, the role of the host. The socket is guarded
// (github.com/truvity/atoom/socket, docs/socket-guard.md): mode 0600 and the uid of the process by default, so that no other process may
// read or write an item; --allow-uid names the callers, and a caller outside the set is refused with the reason caller. The exit status
// is 0 on success, 1 on a failure, 2 on a usage error.
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

	"github.com/aws/aws-sdk-go-v2/config"
	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"

	atoomsocket "github.com/truvity/atoom/socket"
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
	var guard atoomsocket.Flags
	guard.Register(fs)
	socket := fs.String("socket", "", "the UNIX socket to serve on (required)")
	tbl := fs.String("table", dynamodb.DefaultTable, "the name of the table")
	create := fs.Bool("create-table", false, "make the table first if it is absent, and enable its time to live (for a test or a first start)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *socket == "" || fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "dynamodb serve: --socket is required")
		return 2
	}
	cfg, err := guard.Config(*socket)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "dynamodb serve:", err)
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
		return atoomsocket.Serve(ctx, cfg, path, h)
	}}
	if err := dynamodb.Run(ctx, dynamodb.Options{DynamoDB: api, Table: *tbl}, host); err != nil {
		_, _ = fmt.Fprintln(stderr, "dynamodb:", err)
		return 1
	}
	return 0
}
