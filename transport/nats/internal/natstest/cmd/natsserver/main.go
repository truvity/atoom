// Command natsserver runs the embedded NATS server of the driver's tests (internal/natstest) on a loopback port, for the conformance run
// of the transport port (tools/scripts/conformance-transport.sh): the thin main of the driver is pointed at it. It is a test program, not a
// driver, and it is not released.
//
//	natsserver --url-file PATH --dir DIR [--token-file PATH]
//
// It writes the URL it serves on to --url-file once it is ready, and serves until SIGINT or SIGTERM.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/truvity/atoom/transport/nats/internal/natstest"
)

func main() {
	urlFile := flag.String("url-file", "", "where to write the URL served on (required)")
	dir := flag.String("dir", "", "the storage directory of JetStream (required)")
	tokenFile := flag.String("token-file", "", "a file holding the token every client must present")
	flag.Parse()
	if *urlFile == "" || *dir == "" {
		_, _ = fmt.Fprintln(os.Stderr, "natsserver: --url-file and --dir are required")
		os.Exit(2)
	}
	var token string
	if *tokenFile != "" {
		b, err := os.ReadFile(*tokenFile)
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "natsserver:", err)
			os.Exit(1)
		}
		token = strings.TrimSpace(string(b))
	}
	url, stop, err := natstest.Start(natstest.Options{Dir: *dir, Token: token})
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "natsserver:", err)
		os.Exit(1)
	}
	defer stop()
	if err := os.WriteFile(*urlFile, []byte(url+"\n"), 0o600); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "natsserver:", err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	<-ctx.Done()
}
