// Command s3server runs the in-memory S3 fake of the driver's tests (internal/s3fake) on a loopback port, for the conformance run of the
// blob port (tools/scripts/conformance-blob.sh): the thin main of the driver is pointed at it. It is a test program, not a driver, and it
// is not released.
//
//	s3server --url-file PATH --bucket NAME
//
// It writes the URL it serves on to --url-file once it is ready, and serves until SIGINT or SIGTERM.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/truvity/atoom/blob/s3/internal/s3fake"
)

func main() {
	urlFile := flag.String("url-file", "", "where to write the URL served on (required)")
	bucket := flag.String("bucket", "", "the bucket the fake holds (required)")
	flag.Parse()
	if *urlFile == "" || *bucket == "" {
		_, _ = fmt.Fprintln(os.Stderr, "s3server: --url-file and --bucket are required")
		os.Exit(2)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "s3server:", err)
		os.Exit(1)
	}
	srv := &http.Server{Handler: s3fake.New(*bucket), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			_, _ = fmt.Fprintln(os.Stderr, "s3server:", err)
			os.Exit(1)
		}
	}()
	if err := os.WriteFile(*urlFile, []byte("http://"+ln.Addr().String()+"\n"), 0o600); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "s3server:", err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	<-ctx.Done()
	_ = srv.Close()
}
