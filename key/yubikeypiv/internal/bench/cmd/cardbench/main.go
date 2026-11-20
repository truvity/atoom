// Command cardbench is the helper of the card bench (key/yubikeypiv/docs/bench.md, `just card-bench`). It does the three things that the
// recipe cannot do with the thin main and the conformance kit:
//
//	cardbench readers                              list the PC/SC reader names, one on each line (connects to no card)
//	cardbench status --admin-socket PATH           print the node, the lock state and the PIN attempts the driver reports
//	cardbench unlock --admin-socket PATH           send the FACTORY PIN once over the admin socket, to a card that has all its attempts
//	cardbench hold --reader NAME --seconds N       hold the named reader for N seconds (1 to 300), so that a driver finds it busy
//
// It is built with the tag atoom_card, like the thin main. It changes no PIN, no PUK and no management key, and resets no card. The exit
// status is 0 on success, 1 on a refusal or a failure, 2 on a usage error.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/truvity/atoom/key/yubikeypiv/internal/bench"
	"github.com/truvity/atoom/key/yubikeypiv/internal/card"
)

const maxHold = 300 // seconds

// deps are what the commands touch outside the process: the real ones in main, fakes in the tests.
type deps struct {
	readers func() ([]string, error)
	open    card.Opener
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, deps{readers: card.ListReaders, open: card.Open}))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, d deps) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: cardbench readers|status|unlock|hold")
		return 2
	}
	switch args[0] {
	case "readers":
		return readers(args[1:], stdout, stderr, d)
	case "status", "unlock":
		return admin(ctx, args[0], args[1:], stdout, stderr)
	case "hold":
		return hold(ctx, args[1:], stdout, stderr, d)
	default:
		_, _ = fmt.Fprintf(stderr, "cardbench: unknown command %q (readers, status, unlock or hold)\n", args[0])
		return 2
	}
}

func readers(args []string, stdout, stderr io.Writer, d deps) int {
	if len(args) != 0 {
		_, _ = fmt.Fprintln(stderr, "cardbench readers: no arguments")
		return 2
	}
	names, err := d.readers()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cardbench readers:", err)
		return 1
	}
	for _, n := range names {
		_, _ = fmt.Fprintln(stdout, n)
	}
	return 0
}

func admin(ctx context.Context, name string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cardbench "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("admin-socket", "", "the admin socket of the driver (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *socket == "" || fs.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "cardbench %s: --admin-socket is required\n", name)
		return 2
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c := bench.Dial(*socket)
	if name == "unlock" {
		if err := bench.Unlock(ctx, c, stdout); err != nil {
			_, _ = fmt.Fprintln(stderr, "cardbench unlock:", err)
			return 1
		}
		return 0
	}
	st, err := bench.GetStatus(ctx, c)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cardbench status:", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "node=%s locked=%t pin_retries=%d\n", st.Node, st.Locked, st.PINRetries)
	return 0
}

func hold(ctx context.Context, args []string, stdout, stderr io.Writer, d deps) int {
	fs := flag.NewFlagSet("cardbench hold", flag.ContinueOnError)
	fs.SetOutput(stderr)
	reader := fs.String("reader", "", "the exact PC/SC reader name to hold (required)")
	seconds := fs.Int("seconds", 0, fmt.Sprintf("how long to hold it, 1 to %d (required)", maxHold))
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *reader == "" || fs.NArg() != 0 || *seconds < 1 || *seconds > maxHold {
		_, _ = fmt.Fprintf(stderr, "cardbench hold: --reader and --seconds (1 to %d) are required\n", maxHold)
		return 2
	}
	if err := bench.Hold(ctx, d.open, *reader, time.Duration(*seconds)*time.Second, stdout); err != nil {
		_, _ = fmt.Fprintln(stderr, "cardbench hold:", err)
		return 1
	}
	return 0
}
