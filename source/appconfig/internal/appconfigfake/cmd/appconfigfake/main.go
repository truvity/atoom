// Command appconfigfake serves the fake AppConfig data plane of the driver's tests on a loopback port, for the conformance run of the source
// port (tools/scripts/conformance-source.sh): the thin main of the driver is pointed at it with AWS_ENDPOINT_URL_APPCONFIGDATA. It is a
// test program, not a driver, and it is not released.
//
//	appconfigfake --url-file PATH --application APP --environment ENV --deploy PROFILE=FILE...
//
// It writes the URL it serves on to --url-file once it listens, and serves until SIGINT or SIGTERM.
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
	"strings"
	"syscall"
	"time"

	"github.com/truvity/atoom/source/appconfig/internal/appconfigfake"
)

type deploys []string

func (d *deploys) String() string     { return strings.Join(*d, ",") }
func (d *deploys) Set(v string) error { *d = append(*d, v); return nil }

func main() {
	urlFile := flag.String("url-file", "", "where to write the URL served on (required)")
	app := flag.String("application", "", "the application (required)")
	env := flag.String("environment", "", "the environment (required)")
	var ds deploys
	flag.Var(&ds, "deploy", "PROFILE=FILE: deploy the bytes of FILE as the profile (repeatable)")
	flag.Parse()
	if *urlFile == "" || *app == "" || *env == "" || len(ds) == 0 {
		_, _ = fmt.Fprintln(os.Stderr, "appconfigfake: --url-file, --application, --environment and --deploy are required")
		os.Exit(2)
	}
	f := appconfigfake.New()
	for _, d := range ds {
		profile, path, ok := strings.Cut(d, "=")
		if !ok {
			_, _ = fmt.Fprintln(os.Stderr, "appconfigfake: --deploy is PROFILE=FILE")
			os.Exit(2)
		}
		data, err := os.ReadFile(path) //nolint:gosec // the path is the test's flag
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "appconfigfake:", err)
			os.Exit(1)
		}
		f.Deploy(*app, *env, profile, data)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "appconfigfake:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*urlFile, []byte("http://"+l.Addr().String()+"\n"), 0o600); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "appconfigfake:", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := &http.Server{Handler: f, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
		_, _ = fmt.Fprintln(os.Stderr, "appconfigfake:", err)
		os.Exit(1)
	}
}
