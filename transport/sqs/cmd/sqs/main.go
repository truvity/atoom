// Command sqs is the thin main of the Amazon SQS driver of the transport port: it serves the transport over one SQS FIFO queue, both sides
// (Emit, Receive and Ack) on one UNIX socket, and does nothing else. The library (github.com/truvity/atoom/transport/sqs) is the driver; a
// program that needs the driver inside its own process (a Lambda function that emits) links the library instead and needs no socket.
//
//	sqs serve --socket PATH --queue-url URL [--emitter NAME] [--message-group ID] [--wait-time D|0s] [--visibility-timeout D]
//	          [--allow-uid UID]... [--allow-any-uid] [--socket-gid GID] [--socket-mode MODE]
//	sqs version  (also --version)
//
// Every message emitted through the socket carries the --emitter name (default local). The region, the credentials and an endpoint of
// another place (AWS_ENDPOINT_URL_SQS) come from the AWS SDK's own configuration chain: the environment, the shared files, the role of the
// host. The socket is guarded (github.com/truvity/atoom/socket, docs/socket-guard.md): mode 0600 and the uid of the process by default;
// --allow-uid names the callers, and a caller outside the set is refused with the reason caller. The exit status is 0 on success, 1 on a
// failure, 2 on a usage error.
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
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"

	atoomsocket "github.com/truvity/atoom/socket"
	"github.com/truvity/atoom/transport/sqs"
)

// version is stamped at build time (-ldflags "-X main.version=..."); a build without it reports the module version.
var version = ""

// newClient makes the SQS client from the SDK's default configuration; a test replaces it.
var newClient = func(ctx context.Context) (sqs.API, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return awssqs.NewFromConfig(cfg), nil
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: sqs serve|version")
		return 2
	}
	switch args[0] {
	case "version", "--version":
		_, _ = fmt.Fprintln(stdout, versionString())
		return 0
	case "serve":
		return serve(args[1:], stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "sqs: unknown command %q (serve or version)\n", args[0])
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
	fs := flag.NewFlagSet("sqs serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var guard atoomsocket.Flags
	guard.Register(fs)
	socket := fs.String("socket", "", "the UNIX socket to serve on (required)")
	queue := fs.String("queue-url", "", "the URL of the FIFO queue, ending in .fifo (required)")
	emitter := fs.String("emitter", "local", "the emitter name every message emitted through the socket carries")
	group := fs.String("message-group", sqs.DefaultMessageGroupID, "the message group of every emit; the order is kept within it")
	wait := fs.Duration("wait-time", sqs.DefaultWaitTime, "the long poll of each receive, whole seconds up to 20s; 0s polls short (an answer at once, a pause after an empty one)")
	visibility := fs.Duration("visibility-timeout", 0, "the visibility timeout of the messages a receive takes, whole seconds (default: the queue's)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *socket == "" || *queue == "" || fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "sqs serve: --socket and --queue-url are required")
		return 2
	}
	cfg, err := guard.Config(*socket)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "sqs serve:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	api, err := newClient(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "sqs: the AWS configuration:", err)
		return 1
	}
	host := sqs.Host{Serve: func(ctx context.Context, path string, h http.Handler) error {
		return atoomsocket.Serve(ctx, cfg, path, h)
	}}
	opts := sqs.Options{SQS: api, QueueURL: *queue, Emitter: *emitter, MessageGroupID: *group, WaitTime: *wait, ShortPoll: *wait == 0, VisibilityTimeout: *visibility}
	if err := sqs.Run(ctx, opts, host); err != nil {
		_, _ = fmt.Fprintln(stderr, "sqs:", err)
		if errors.Is(err, sqs.ErrOptions) {
			return 2
		}
		return 1
	}
	return 0
}
