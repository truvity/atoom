// Command nats is the thin main of the NATS JetStream driver of the transport port: it serves the transport over one JetStream stream and
// durable consumer, both sides (Emit, Receive and Ack) on one UNIX socket, and does nothing else. The library
// (github.com/truvity/atoom/transport/nats) is the driver; a program that needs the driver inside its own process links the library instead
// and needs no socket.
//
//	nats serve --socket PATH --url URL --stream NAME --subject SUBJECT --consumer NAME [--create] [--ack-wait D]
//	           [--creds FILE | --token-file FILE] [--tls-ca FILE] [--emitter NAME] [--wait-time D]
//	           [--allow-uid UID]... [--allow-any-uid] [--socket-gid GID] [--socket-mode MODE]
//	nats version  (also --version)
//
// The connection is the deployment's: --creds names a NATS credentials file (a user JWT and its seed), --token-file a file that holds a
// token (one the broker maps to a NATS user, a workload's identity token for one); the driver reads them and knows nothing of how they were
// made. Every message emitted through the socket carries the --emitter name (default local). The socket is guarded
// (github.com/truvity/atoom/socket, docs/socket-guard.md): mode 0600 and the uid of the process by default; --allow-uid names the callers,
// and a caller outside the set is refused with the reason caller. The exit status is 0 on success, 1 on a failure, 2 on a usage error.
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

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	atoomsocket "github.com/truvity/atoom/socket"
	"github.com/truvity/atoom/transport/nats"
)

// version is stamped at build time (-ldflags "-X main.version=..."); a build without it reports the module version.
var version = ""

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: nats serve|version")
		return 2
	}
	switch args[0] {
	case "version", "--version":
		_, _ = fmt.Fprintln(stdout, versionString())
		return 0
	case "serve":
		return serve(args[1:], stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "nats: unknown command %q (serve or version)\n", args[0])
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
	fs := flag.NewFlagSet("nats serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var guard atoomsocket.Flags
	guard.Register(fs)
	socket := fs.String("socket", "", "the UNIX socket to serve on (required)")
	url := fs.String("url", "", "the NATS server or servers, comma separated (required)")
	stream := fs.String("stream", "", "the JetStream stream of the log (required)")
	subject := fs.String("subject", "", "the subject an emit publishes to (required)")
	consumer := fs.String("consumer", "", "the durable consumer that Receive pulls from (required)")
	create := fs.Bool("create", false, "make the stream and the consumer when they are not there")
	ackWait := fs.Duration("ack-wait", nats.DefaultAckWait, "the ack wait of a consumer the driver makes")
	creds := fs.String("creds", "", "a NATS credentials file (user JWT and seed)")
	tokenFile := fs.String("token-file", "", "a file holding the token to present")
	tlsCA := fs.String("tls-ca", "", "a PEM file of the CAs that sign the server's certificate")
	emitter := fs.String("emitter", "local", "the emitter name every message emitted through the socket carries")
	wait := fs.Duration("wait-time", nats.DefaultWaitTime, "how long one pull waits for a message")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *socket == "" || *url == "" || *stream == "" || *subject == "" || *consumer == "" || fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "nats serve: --socket, --url, --stream, --subject and --consumer are required")
		return 2
	}
	if *creds != "" && *tokenFile != "" {
		_, _ = fmt.Fprintln(stderr, "nats serve: --creds and --token-file are exclusive")
		return 2
	}
	cfg, err := guard.Config(*socket)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "nats serve:", err)
		return 2
	}
	copts := []natsgo.Option{natsgo.Name("atoom-transport-nats"), natsgo.MaxReconnects(-1)}
	switch {
	case *creds != "":
		copts = append(copts, natsgo.UserCredentials(*creds))
	case *tokenFile != "":
		// Read at every connect, so that a token the deployment renews (a projected workload token) is the current one.
		path := *tokenFile
		copts = append(copts, natsgo.TokenHandler(func() string {
			b, err := os.ReadFile(path) //nolint:gosec // the path is the operator's flag
			if err != nil {
				return ""
			}
			return strings.TrimSpace(string(b))
		}))
	}
	if *tlsCA != "" {
		copts = append(copts, natsgo.RootCAs(*tlsCA))
	}
	opts := nats.Options{Stream: *stream, Subject: *subject, Consumer: *consumer, Create: *create, AckWait: *ackWait, Emitter: *emitter, WaitTime: *wait}
	if _, err := nats.New(withPlaceholder(opts)); err != nil {
		_, _ = fmt.Fprintln(stderr, "nats serve:", err)
		return 2
	}
	nc, err := natsgo.Connect(*url, copts...)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "nats: the connection:", err)
		return 1
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "nats: JetStream:", err)
		return 1
	}
	opts.JetStream = js
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	host := nats.Host{Serve: func(ctx context.Context, path string, h http.Handler) error {
		return atoomsocket.Serve(ctx, cfg, path, h)
	}}
	if err := nats.Run(ctx, opts, host); err != nil {
		_, _ = fmt.Fprintln(stderr, "nats:", err)
		if errors.Is(err, nats.ErrOptions) {
			return 2
		}
		return 1
	}
	return 0
}

// withPlaceholder fills the connection with a placeholder, so that the options are checked before a connection is made.
func withPlaceholder(o nats.Options) nats.Options {
	o.JetStream = placeholder{}
	return o
}

type placeholder struct{ jetstream.JetStream }
