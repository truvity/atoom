package bench_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/truvity/atoom/key/gen/atoom/key/v1alpha1/keyv1alpha1connect"
	"github.com/truvity/atoom/key/purpose"
	"github.com/truvity/atoom/key/yubikeypiv"
	"github.com/truvity/atoom/key/yubikeypiv/internal/bench"
	"github.com/truvity/atoom/key/yubikeypiv/internal/card/cardtest"
)

// startDriver serves the driver over the fake card, with the admin socket open to this process's uid, and returns a client of the admin
// socket. The driver can be unlocked only on Linux (locked memory, SO_PEERCRED), so elsewhere the test skips.
func startDriver(t *testing.T, c *cardtest.Card) keyv1alpha1connect.UnlockServiceClient {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("SKIP: the driver can be unlocked only on Linux (locked memory, SO_PEERCRED); on " + runtime.GOOS + " it stays locked")
	}
	dir, err := os.MkdirTemp("", "b")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "a.sock")
	me := uint32(os.Getuid()) //nolint:gosec // a uid is non-negative
	host := yubikeypiv.Host{Serve: func(ctx context.Context, _ string, _ http.Handler) error {
		<-ctx.Done()
		return nil
	}}
	opts := yubikeypiv.Options{Purposes: purpose.Reference(), AllowFactoryDefaults: true, Node: "bench", Serial: c.SerialNo, Open: c.Opener()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- yubikeypiv.Run(ctx, opts, host, yubikeypiv.AdminConfig{Socket: socket, Allow: func(uid uint32) bool { return uid == me }})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Run did not return")
		}
	})
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		select {
		case err := <-done:
			t.Fatalf("Run returned early: %v", err)
		default:
		}
		if fi, err := os.Lstat(socket); err == nil && fi.Mode()&os.ModeSocket != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the admin socket did not appear")
		}
	}
	return bench.Dial(socket)
}

func TestUnlockSendsTheFactoryPINOnceToAFactoryCard(t *testing.T) {
	c := cardtest.New()
	cl := startDriver(t, c)
	st, err := bench.GetStatus(context.Background(), cl)
	if err != nil || !st.Locked || st.PINRetries != bench.FactoryRetries || st.Node != "bench" {
		t.Fatalf("status before: %+v, %v", st, err)
	}
	var out bytes.Buffer
	if err := bench.Unlock(context.Background(), cl, &out); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if st, err := bench.GetStatus(context.Background(), cl); err != nil || st.Locked {
		t.Fatalf("status after: %+v, %v; want unlocked", st, err)
	}
	if n := c.Attempts(); n != 1 {
		t.Fatalf("the card was sent %d PINs, want 1", n)
	}
	// A second call finds the driver unlocked and sends nothing.
	out.Reset()
	if err := bench.Unlock(context.Background(), cl, &out); err != nil || !strings.Contains(out.String(), "already") {
		t.Fatalf("second Unlock: %v (%s)", err, out.String())
	}
	if n := c.Attempts(); n != 1 {
		t.Fatalf("the card was sent %d PINs after the second call, want 1", n)
	}
}

func TestUnlockSendsNoPINToACardThatIsNotInFactoryState(t *testing.T) {
	c := cardtest.New()
	c.Retries = 2 // somebody has already tried a wrong PIN
	cl := startDriver(t, c)
	err := bench.Unlock(context.Background(), cl, &bytes.Buffer{})
	if !errors.Is(err, bench.ErrNotFactory) {
		t.Fatalf("Unlock = %v, want ErrNotFactory", err)
	}
	if n := c.Attempts(); n != 0 {
		t.Fatalf("the card was sent %d PINs, want none", n)
	}
}

func TestUnlockDoesNotRetryAPINTheCardRefuses(t *testing.T) {
	c := cardtest.New()
	c.PIN = "654321" // the PIN was changed: the factory PIN is wrong
	cl := startDriver(t, c)
	err := bench.Unlock(context.Background(), cl, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "not retried") || !strings.Contains(err.Error(), "2 PIN attempts left") {
		t.Fatalf("Unlock = %v, want a refusal that says it was not retried and how many attempts are left", err)
	}
	if n := c.Attempts(); n != 1 {
		t.Fatalf("the card was sent %d PINs, want exactly 1", n)
	}
	if n := c.RetriesLeft(); n != 2 {
		t.Fatalf("the card has %d attempts left, want 2", n)
	}
}

func TestGetStatusWithNothingListeningIsAnError(t *testing.T) {
	dir, err := os.MkdirTemp("", "b")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := bench.GetStatus(ctx, bench.Dial(filepath.Join(dir, "none.sock"))); err == nil {
		t.Fatal("a status from a socket nobody serves")
	}
}

func TestHoldConnectsToTheNamedReaderOnlyAndLetsItGo(t *testing.T) {
	c := cardtest.New()
	other := cardtest.New()
	b := cardtest.NewBench(cardtest.Reader{Name: "Reader A 00 00", Card: c}, cardtest.Reader{Name: "Reader B 00 01", Card: other})
	var out bytes.Buffer
	if err := bench.Hold(context.Background(), b.Opener(), "Reader A 00 00", time.Millisecond, &out); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if b.Opens("Reader A 00 00") != 1 || b.Opens("Reader B 00 01") != 0 {
		t.Fatalf("connections: A %d, B %d; want 1 and 0", b.Opens("Reader A 00 00"), b.Opens("Reader B 00 01"))
	}
	if !c.Closed() {
		t.Fatal("the connection was not let go")
	}
	if !strings.Contains(out.String(), "held:") || !strings.Contains(out.String(), "released:") {
		t.Fatalf("output %q says neither held nor released", out.String())
	}
}

func TestHoldEndsWhenItsContextEnds(t *testing.T) {
	c := cardtest.New()
	b := cardtest.NewBench(cardtest.Reader{Name: "Reader A 00 00", Card: c})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	var out syncBuffer
	go func() { done <- bench.Hold(ctx, b.Opener(), "Reader A 00 00", time.Hour, &out) }()
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(out.String(), "held:"); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the reader was not held")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Hold: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Hold did not end with its context")
	}
}

func TestHoldOfAReaderThatIsNotThereOrHeldFails(t *testing.T) {
	b := cardtest.NewBench(cardtest.Reader{Name: "Reader A 00 00", Card: cardtest.New(), Busy: true})
	if err := bench.Hold(context.Background(), b.Opener(), "No Such Reader", time.Millisecond, &bytes.Buffer{}); err == nil {
		t.Fatal("Hold of an unknown reader succeeded")
	}
	if err := bench.Hold(context.Background(), b.Opener(), "Reader A 00 00", time.Millisecond, &bytes.Buffer{}); err == nil {
		t.Fatal("Hold of a reader that another process holds succeeded")
	}
	if err := bench.Hold(context.Background(), b.Opener(), "", time.Millisecond, &bytes.Buffer{}); err == nil {
		t.Fatal("Hold with no reader succeeded")
	}
}
