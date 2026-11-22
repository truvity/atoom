// Command seal-conformance runs the kit of the seal block.
//
//	seal-conformance --key-socket PATH --store-socket PATH [--installation NAME]
//	seal-conformance --socket PATH
//	seal-conformance --print-declaration
//
// With --key-socket and --store-socket it hosts the block in this process, over the key driver and the store driver that listen on those
// sockets, with the kit's purposes and rules, and runs the kit against it through an in-memory transport (socket.InProcess): the block's
// contract is ConnectRPC in-process too. The key driver must be started with the declaration that --print-declaration writes, the JSON
// file that the thin mains take as --purposes. With --socket it runs the kit against a block that another host serves on a socket, with
// the kit's purposes and rules (conformance.Purposes, conformance.Rules).
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
	"github.com/truvity/atoom/blocks/seal/conformance"
	"github.com/truvity/atoom/blocks/seal/gen/atoom/seal/v1alpha1/sealv1alpha1connect"
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
	fs := flag.NewFlagSet("seal-conformance", flag.ContinueOnError)
	fs.SetOutput(stderr)
	keySocket := fs.String("key-socket", "", "the UNIX socket of the key driver the block is hosted over")
	storeSocket := fs.String("store-socket", "", "the UNIX socket of the store driver the block is hosted over")
	installation := fs.String("installation", "seal-conformance", "the installation the hosted block binds its wraps to")
	blockSocket := fs.String("socket", "", "the UNIX socket of a block that another host serves")
	printDecl := fs.Bool("print-declaration", false, "write the key purposes the key driver must serve, as JSON, and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	hosted := *keySocket != "" || *storeSocket != ""
	switch {
	case fs.NArg() != 0:
		_, _ = fmt.Fprintln(stderr, "seal-conformance: no arguments are taken")
		return 2
	case *printDecl && !hosted && *blockSocket == "":
		set, err := purpose.New(seal.KeyPurposes("", conformance.Purposes()))
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "seal-conformance:", err)
			return 1
		}
		raw, err := purpose.Marshal(set)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "seal-conformance:", err)
			return 1
		}
		_, _ = fmt.Fprintln(stdout, string(raw))
		return 0
	case hosted && (*keySocket == "" || *storeSocket == "" || *blockSocket != ""):
		_, _ = fmt.Fprintln(stderr, "seal-conformance: --key-socket and --store-socket go together, and without --socket")
		return 2
	case !hosted && *blockSocket == "":
		_, _ = fmt.Fprintln(stderr, "seal-conformance: --key-socket and --store-socket, or --socket")
		return 2
	}
	var client conformance.Client
	if hosted {
		b, err := seal.New(ctx, seal.Options{
			Keys:         keyv1alpha1connect.NewKeyStoreServiceClient(socket.Client(*keySocket), "http://key", call.ClientOptions()...),
			Items:        storev1alpha1connect.NewItemServiceClient(socket.Client(*storeSocket), "http://store", call.ClientOptions()...),
			Purposes:     conformance.Purposes(),
			Rules:        conformance.Rules(),
			Installation: *installation,
		})
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "seal-conformance:", err)
			return 1
		}
		defer b.Close()
		hc, stop := socket.InProcess(b.Handler())
		defer stop()
		client = sealv1alpha1connect.NewSealServiceClient(hc, "http://seal", call.ClientOptions()...)
	} else {
		client = sealv1alpha1connect.NewSealServiceClient(socket.Client(*blockSocket), "http://seal", call.ClientOptions()...)
	}
	if err := conformance.Run(ctx, client, stdout); err != nil {
		_, _ = fmt.Fprintln(stderr, "seal-conformance:", err)
		return 1
	}
	return 0
}
