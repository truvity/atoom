// Command token is the thin main of the token block: it serves TokenService on a UNIX socket, over a seal block, and does nothing else.
// A host in another language reaches the block through it (record 0008: one implementation, in Go; sluis's Rust signer is such a host).
//
//	token serve --socket PATH --purposes FILE --seal-socket PATH
//	            [--allow-uid UID]... [--allow-any-uid] [--socket-gid GID] [--socket-mode MODE]
//	token serve --socket PATH --purposes FILE --key-socket PATH --store-socket PATH --installation NAME [the guard flags]
//	token key-purposes --purposes FILE
//	token check --purposes FILE
//	token version  (also --version)
//
// --purposes names the JSON file in which the host declares the block's purposes (github.com/truvity/atoom/blocks/token,
// ParseDeclaration): each a name, its algorithm, the typ values it signs and its kid rule.
//
// With --seal-socket the block calls a seal block that another process serves (sluis key@). With --key-socket and --store-socket it
// hosts a seal block in this process over those two drivers, with the signing purposes of the declaration and no other, and is its only
// caller; key-purposes prints the declaration that key driver must be started with (the JSON file of its --purposes).
//
// The socket is guarded (github.com/truvity/atoom/socket, docs/socket-guard.md): mode 0600 and the uid of the process by default;
// --allow-uid names the callers. The guard is the access rule of this main: every caller it admits may sign by every purpose, so a host
// checks its claims before it calls (record 0020). The exit status is 0 on success, 1 on a failure, 2 on a usage error.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/truvity/atoom/blocks/seal"
	"github.com/truvity/atoom/blocks/seal/gen/atoom/seal/v1alpha1/sealv1alpha1connect"
	"github.com/truvity/atoom/blocks/token"
	"github.com/truvity/atoom/common/call"
	"github.com/truvity/atoom/key/gen/atoom/key/v1alpha1/keyv1alpha1connect"
	"github.com/truvity/atoom/key/purpose"
	atoomsocket "github.com/truvity/atoom/socket"
	"github.com/truvity/atoom/store/gen/atoom/store/v1alpha1/storev1alpha1connect"
)

// version is stamped at build time (-ldflags "-X main.version=..."); a build without it reports the module version.
var version = ""

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: token serve|key-purposes|check|version")
		return 2
	}
	switch args[0] {
	case "version", "--version":
		_, _ = fmt.Fprintln(stdout, versionString())
		return 0
	case "serve":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return serve(ctx, args[1:], stderr)
	case "key-purposes":
		return keyPurposes(args[1:], stdout, stderr)
	case "check":
		return check(args[1:], stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "token: unknown command %q (serve, key-purposes, check or version)\n", args[0])
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

// errUsage is a refusal of the command line.
var errUsage = errors.New("usage")

// serve serves until ctx ends.
func serve(ctx context.Context, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("token serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var guard atoomsocket.Flags
	guard.Register(fs)
	socket := fs.String("socket", "", "the UNIX socket to serve on (required)")
	purposes := fs.String("purposes", "", "the JSON file of the block's purposes (required)")
	sealSocket := fs.String("seal-socket", "", "the UNIX socket of the seal block that holds the keys")
	keySocket := fs.String("key-socket", "", "host a seal block in this process over the key driver on this socket")
	storeSocket := fs.String("store-socket", "", "and the store driver on this socket")
	installation := fs.String("installation", "", "the installation of the hosted seal block")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	hosted := *keySocket != "" || *storeSocket != "" || *installation != ""
	switch {
	case *socket == "" || *purposes == "" || fs.NArg() != 0:
		_, _ = fmt.Fprintln(stderr, "token serve: --socket and --purposes are required")
		return 2
	case hosted == (*sealSocket != ""):
		_, _ = fmt.Fprintln(stderr, "token serve: --seal-socket, or --key-socket, --store-socket and --installation")
		return 2
	case hosted && (*keySocket == "" || *storeSocket == "" || *installation == ""):
		_, _ = fmt.Fprintln(stderr, "token serve: --key-socket, --store-socket and --installation go together")
		return 2
	}
	decls, err := readPurposes(*purposes)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "token serve:", err)
		return 2
	}
	cfg, err := guard.Config(*socket)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "token serve:", err)
		return 2
	}
	sealClient, closeSeal, err := sealFor(ctx, decls, *sealSocket, *keySocket, *storeSocket, *installation)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "token serve:", err)
		if errors.Is(err, errUsage) {
			return 2
		}
		return 1
	}
	defer closeSeal()
	b, err := token.New(ctx, token.Options{Seal: sealClient, Purposes: decls, Rules: token.AllowAll()})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "token:", err)
		return 1
	}
	path, h := b.Handler()
	if err := atoomsocket.Serve(ctx, cfg, path, h); err != nil {
		_, _ = fmt.Fprintln(stderr, "token:", err)
		return 1
	}
	return 0
}

// sealFor is the client of the seal block: on a socket, or a block hosted here over a key driver and a store driver.
func sealFor(ctx context.Context, decls []token.Purpose, sealSocket, keySocket, storeSocket, installation string) (sealv1alpha1connect.SealServiceClient, func(), error) {
	if sealSocket != "" {
		return sealv1alpha1connect.NewSealServiceClient(atoomsocket.Client(sealSocket), "http://seal", call.ClientOptions()...), func() {}, nil
	}
	ps, err := token.SealPurposes(decls)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: --purposes: %w", errUsage, err)
	}
	s, err := seal.New(ctx, seal.Options{
		Keys:         keyv1alpha1connect.NewKeyStoreServiceClient(atoomsocket.Client(keySocket), "http://key", call.ClientOptions()...),
		Items:        storev1alpha1connect.NewItemServiceClient(atoomsocket.Client(storeSocket), "http://store", call.ClientOptions()...),
		Purposes:     ps,
		Rules:        seal.AllowAll(), // the token block in this process is its one caller
		Installation: installation,
	})
	if err != nil {
		return nil, nil, err
	}
	hc, stop := atoomsocket.InProcess(s.Handler())
	return sealv1alpha1connect.NewSealServiceClient(hc, "http://seal", call.ClientOptions()...), func() { stop(); s.Close() }, nil
}

func keyPurposes(args []string, stdout, stderr io.Writer) int {
	decls, code := purposesFlag("token key-purposes", args, stderr)
	if code != 0 {
		return code
	}
	ps, err := token.SealPurposes(decls)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "token key-purposes:", err)
		return 2
	}
	set, err := purpose.New(seal.KeyPurposes("", ps))
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "token key-purposes:", err)
		return 1
	}
	raw, err := purpose.Marshal(set)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "token key-purposes:", err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, string(raw))
	return 0
}

func check(args []string, stderr io.Writer) int {
	_, code := purposesFlag("token check", args, stderr)
	return code
}

func purposesFlag(name string, args []string, stderr io.Writer) ([]token.Purpose, int) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	purposes := fs.String("purposes", "", "the JSON file of the block's purposes (required)")
	if err := fs.Parse(args); err != nil {
		return nil, 2
	}
	if *purposes == "" || fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, name+": --purposes is required")
		return nil, 2
	}
	decls, err := readPurposes(*purposes)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, name+":", err)
		return nil, 2
	}
	return decls, 0
}

// readPurposes reads the declaration file. The message names the path and a word, never a value of the file.
func readPurposes(path string) ([]token.Purpose, error) {
	f, err := os.Open(path) //nolint:gosec // the path is the operator's flag
	if err != nil {
		return nil, fmt.Errorf("--purposes: %s cannot be read", path)
	}
	defer func() { _ = f.Close() }()
	decls, err := token.ParseDeclaration(f)
	if err != nil {
		return nil, fmt.Errorf("--purposes %s: %w", path, err)
	}
	return decls, nil
}
