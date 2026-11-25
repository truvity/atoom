// Command token-conformance runs the kit of the token block.
//
//	token-conformance --key-socket PATH --store-socket PATH [--installation NAME]
//	token-conformance --socket PATH [--without-rules]
//	token-conformance --print-declaration
//	token-conformance --print-purposes
//
// With --key-socket and --store-socket it hosts a seal block over the key driver and the store driver that listen on those sockets, and
// the token block over it, both in this process with the kit's purposes and rules, and runs the kit against the token block through an
// in-memory transport (socket.InProcess). The key driver must be started with the declaration that --print-declaration writes. With
// --socket it runs the kit against a token block that another host serves (the thin main, started with the declaration file that
// --print-purposes writes); --without-rules skips the one case that needs the kit's rules, for a host whose rules are its own (the thin
// main's access rule is its socket guard).
//
// It prints one line for each case and exits 0 when no case failed, 1 when one failed, and 2 on a usage error.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/truvity/atoom/blocks/seal"
	"github.com/truvity/atoom/blocks/seal/gen/atoom/seal/v1alpha1/sealv1alpha1connect"
	"github.com/truvity/atoom/blocks/token"
	"github.com/truvity/atoom/blocks/token/conformance"
	"github.com/truvity/atoom/blocks/token/gen/atoom/token/v1alpha1/tokenv1alpha1connect"
	"github.com/truvity/atoom/common/call"
	"github.com/truvity/atoom/key/gen/atoom/key/v1alpha1/keyv1alpha1connect"
	"github.com/truvity/atoom/key/purpose"
	"github.com/truvity/atoom/socket"
	"github.com/truvity/atoom/store/gen/atoom/store/v1alpha1/storev1alpha1connect"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("token-conformance", flag.ContinueOnError)
	fs.SetOutput(stderr)
	keySocket := fs.String("key-socket", "", "the UNIX socket of the key driver the seal block is hosted over")
	storeSocket := fs.String("store-socket", "", "the UNIX socket of the store driver the seal block is hosted over")
	installation := fs.String("installation", "token-conformance", "the installation of the hosted seal block")
	blockSocket := fs.String("socket", "", "the UNIX socket of a token block that another host serves")
	printDecl := fs.Bool("print-declaration", false, "write the key purposes the key driver must serve, as JSON, and exit")
	printPurposes := fs.Bool("print-purposes", false, "write the kit's purposes as the declaration file of the thin main, and exit")
	noRules := fs.Bool("without-rules", false, "with --socket: the block's rules are not the kit's (the thin main's are its socket guard), so skip the case of the rules")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	hosted := *keySocket != "" || *storeSocket != ""
	switch {
	case fs.NArg() != 0:
		_, _ = fmt.Fprintln(stderr, "token-conformance: no arguments are taken")
		return 2
	case *printDecl && *printPurposes:
		_, _ = fmt.Fprintln(stderr, "token-conformance: one of --print-declaration and --print-purposes")
		return 2
	case (*printDecl || *printPurposes) && !hosted && *blockSocket == "":
		if *printPurposes {
			_, _ = fmt.Fprintln(stdout, string(token.MarshalDeclaration(conformance.Purposes())))
			return 0
		}
		set, err := purpose.New(seal.KeyPurposes("", conformance.SealPurposes()))
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "token-conformance:", err)
			return 1
		}
		raw, err := purpose.Marshal(set)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "token-conformance:", err)
			return 1
		}
		_, _ = fmt.Fprintln(stdout, string(raw))
		return 0
	case hosted && (*keySocket == "" || *storeSocket == "" || *blockSocket != ""):
		_, _ = fmt.Fprintln(stderr, "token-conformance: --key-socket and --store-socket go together, and without --socket")
		return 2
	case !hosted && *blockSocket == "":
		_, _ = fmt.Fprintln(stderr, "token-conformance: --key-socket and --store-socket, or --socket")
		return 2
	case *noRules && *blockSocket == "":
		_, _ = fmt.Fprintln(stderr, "token-conformance: --without-rules goes with --socket")
		return 2
	}
	var client conformance.Client
	if hosted {
		s, err := seal.New(ctx, seal.Options{
			Keys:         keyv1alpha1connect.NewKeyStoreServiceClient(socket.Client(*keySocket), "http://key", call.ClientOptions()...),
			Items:        storev1alpha1connect.NewItemServiceClient(socket.Client(*storeSocket), "http://store", call.ClientOptions()...),
			Purposes:     conformance.SealPurposes(),
			Rules:        seal.AllowAll(),
			Installation: *installation,
		})
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "token-conformance:", err)
			return 1
		}
		defer s.Close()
		shc, sstop := socket.InProcess(s.Handler())
		defer sstop()
		b, err := token.New(ctx, token.Options{
			Seal:     sealv1alpha1connect.NewSealServiceClient(shc, "http://seal", call.ClientOptions()...),
			Purposes: conformance.Purposes(),
			Rules:    conformance.Rules(),
		})
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "token-conformance:", err)
			return 1
		}
		thc, tstop := socket.InProcess(b.Handler())
		defer tstop()
		client = tokenv1alpha1connect.NewTokenServiceClient(thc, "http://token", call.ClientOptions()...)
	} else {
		client = tokenv1alpha1connect.NewTokenServiceClient(socket.Client(*blockSocket), "http://token", call.ClientOptions()...)
	}
	cases := conformance.Cases()
	if *noRules {
		cases = conformance.CasesWithoutRules()
	}
	if err := conformance.RunCases(ctx, client, cases, stdout); err != nil {
		_, _ = fmt.Fprintln(stderr, "token-conformance:", err)
		return 1
	}
	return 0
}
