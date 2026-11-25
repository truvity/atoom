// Command appconfig is the thin main of the AWS AppConfig driver of the source port: it serves the config source over the configurations
// that AppConfig deploys to one application and environment, on a UNIX socket, and does nothing else. The library
// (github.com/truvity/atoom/source/appconfig) is the driver; a program that needs the driver inside its own process (a Lambda function)
// links the library instead and needs no socket.
//
//	appconfig serve --socket PATH --application APP --environment ENV [--profile NAME=ID]... [--poll-interval DURATION]
//	                [--allow-uid UID]... [--allow-any-uid] [--socket-gid GID] [--socket-mode MODE]
//	appconfig check --application APP --environment ENV --name NAME [--profile NAME=ID]...
//	appconfig version  (also --version)
//
// The region, the credentials and an endpoint of another place (AWS_ENDPOINT_URL_APPCONFIGDATA) come from the AWS SDK's own configuration
// chain: the environment, the shared files, the role of the host. The socket is guarded (github.com/truvity/atoom/socket,
// docs/socket-guard.md): mode 0600 and the uid of the process by default; --allow-uid names the callers, and a caller outside the set is
// refused with the reason caller. check reads one document and says whether it can be served; it exits 3 when it cannot. The exit status
// is 0 on success, 1 on a failure, 2 on a usage error.
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
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/appconfigdata"

	atoomsocket "github.com/truvity/atoom/socket"
	"github.com/truvity/atoom/source/appconfig"
)

// version is stamped at build time (-ldflags "-X main.version=..."); a build without it reports the module version.
var version = ""

// newClient makes the AppConfig data client from the SDK's default configuration; a test replaces it.
var newClient = func(ctx context.Context) (appconfig.API, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return appconfigdata.NewFromConfig(cfg), nil
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: appconfig serve|check|version")
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
		_, _ = fmt.Fprintf(stderr, "appconfig: unknown command %q (serve, check or version)\n", args[0])
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

// profiles is the repeatable --profile NAME=ID.
type profiles map[string]string

func (p profiles) String() string { return "" }

func (p profiles) Set(v string) error {
	name, id, ok := strings.Cut(v, "=")
	if !ok || !appconfig.ValidName(name) || id == "" {
		return errors.New("NAME=ID, with a kebab-case NAME and a non-empty ID")
	}
	if _, dup := p[name]; dup {
		return errors.New("the name is given twice")
	}
	p[name] = id
	return nil
}

// common are the flags of serve and check that say where the documents are.
type common struct {
	app, env string
	profiles profiles
}

func (c *common) register(fs *flag.FlagSet) {
	c.profiles = profiles{}
	fs.StringVar(&c.app, "application", "", "the AppConfig application, its name or id (required)")
	fs.StringVar(&c.env, "environment", "", "the AppConfig environment of the application, its name or id (required)")
	fs.Var(c.profiles, "profile", "NAME=ID: the configuration profile of a document whose profile is not its name (repeatable)")
}

func serve(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("appconfig serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var guard atoomsocket.Flags
	guard.Register(fs)
	var c common
	c.register(fs)
	socket := fs.String("socket", "", "the UNIX socket to serve on (required)")
	poll := fs.Duration("poll-interval", 0, "the least interval between two polls of a document, whole seconds from 15s to 24h (default: AppConfig's, 60s)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *socket == "" || c.app == "" || c.env == "" || fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "appconfig serve: --socket, --application and --environment are required")
		return 2
	}
	cfg, err := guard.Config(*socket)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "appconfig serve:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	api, err := newClient(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "appconfig: the AWS configuration:", err)
		return 1
	}
	host := appconfig.Host{Serve: func(ctx context.Context, path string, h http.Handler) error {
		return atoomsocket.Serve(ctx, cfg, path, h)
	}}
	opts := appconfig.Options{AppConfig: api, Application: c.app, Environment: c.env, Profiles: c.profiles, PollInterval: *poll}
	if err := appconfig.Run(ctx, opts, host); err != nil {
		_, _ = fmt.Fprintln(stderr, "appconfig:", err)
		if errors.Is(err, appconfig.ErrOptions) {
			return 2
		}
		return 1
	}
	return 0
}

// check reads one document. A document that cannot be served is a refusal, not a crash: exit 3, so that a script tells it from a bad flag
// (1 or 2).
func check(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("appconfig check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c common
	c.register(fs)
	name := fs.String("name", "", "the document to read (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if c.app == "" || c.env == "" || *name == "" || fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "appconfig check: --application, --environment and --name are required")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	api, err := newClient(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "appconfig: the AWS configuration:", err)
		return 1
	}
	svc, err := appconfig.New(appconfig.Options{AppConfig: api, Application: c.app, Environment: c.env, Profiles: c.profiles})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "appconfig check:", err)
		return 2
	}
	doc, err := svc.Document(ctx, *name)
	if err != nil {
		// The reason only: AppConfig's text may name resources, and goes to the log of serve, not here.
		reason := "AppConfig failed"
		switch {
		case errors.Is(err, appconfig.ErrName):
			reason = "the name is not kebab-case"
		case errors.Is(err, appconfig.ErrAbsent):
			reason = "no such configuration"
		case errors.Is(err, appconfig.ErrBound):
			reason = "the configuration is over the limit"
		}
		_, _ = fmt.Fprintln(stderr, "appconfig: check failed:", reason)
		return 3
	}
	_, _ = fmt.Fprintf(stdout, "configuration ok: %d bytes, version %s\n", len(doc.Data), doc.Version)
	return 0
}
