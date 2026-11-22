// Command key-conformance runs the conformance kit of the key port against a driver that listens on a UNIX socket.
//
//	key-conformance --socket PATH [--http1]
//	key-conformance --print-declaration
//
// The kit declares its own purposes (conformance.Declaration) and the driver under test must serve them: the host starts the driver with
// the declaration that --print-declaration writes to its standard output, as the JSON file that the thin mains take as --purposes.
// A case that needs a purpose the driver does not serve fails and says so.
//
// It prints one line for each case and exits 0 when no case failed and at least one ran, 1 when a case failed or none ran, and 2 on
// a usage error. A card driver must be unlocked first; the suite makes the keys of its own purposes when they are absent, so run it
// against a state of its own.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/truvity/atoom/key/conformance"
	"github.com/truvity/atoom/key/purpose"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("key-conformance", flag.ContinueOnError)
	socket := fs.String("socket", "", "the UNIX socket of the driver (required)")
	http1 := fs.Bool("http1", false, "speak HTTP/1.1 to a driver that does not serve h2c")
	printDecl := fs.Bool("print-declaration", false, "write the kit's declaration of purposes as JSON and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *printDecl && *socket == "" && fs.NArg() == 0 {
		set, err := purpose.New(conformance.Declaration())
		if err == nil {
			var raw []byte
			if raw, err = purpose.Marshal(set); err == nil {
				_, err = fmt.Fprintln(os.Stdout, string(raw))
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "key-conformance:", err)
			return 1
		}
		return 0
	}
	if *socket == "" || fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "key-conformance: --socket is required")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	c := conformance.Dial(*socket)
	if *http1 {
		c = conformance.DialHTTP1(*socket)
	}
	if err := conformance.Run(ctx, c, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "key-conformance:", err)
		return 1
	}
	return 0
}
