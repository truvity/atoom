// Package bench holds the small helpers of the card bench (docs/bench.md, `just card-bench`): the run of the driver against a real YubiKey
// that a person starts by hand. The recipe drives the driver through its sockets with the programs it has (the thin main, the conformance
// kit); what it lacks is an unlock over the admin socket, a read of the unlock status, and a process that holds a reader, and those are here.
//
// SAFETY: the bench works on a card in factory state and on nothing else. The helpers send the factory PIN, and only to a card that says
// it has all of its PIN attempts, once, and they never retry. They change no PIN, no PUK and no management key, and reset no card.
package bench

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"connectrpc.com/connect"

	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
	"github.com/truvity/atoom/key/gen/atoom/key/v1alpha1/keyv1alpha1connect"
	"github.com/truvity/atoom/key/yubikeypiv/internal/card"
)

const (
	// FactoryPIN is the PIN of a YubiKey as it leaves the factory, and the only PIN the bench sends.
	FactoryPIN = "123456"
	// FactoryRetries is the number of PIN attempts of a card in factory state. The bench sends the PIN only to a card that reports all of them,
	// so that a card whose PIN is not the factory one, and which a wrong guess would bring toward a block, is never sent a guess.
	FactoryRetries = 3
)

// Dial returns a client of the unlock service on the admin socket of a driver. It speaks HTTP/2 without TLS (h2c), as the driver's own
// admin server and sluis's `node unlock` do.
func Dial(socket string) keyv1alpha1connect.UnlockServiceClient {
	var protocols http.Protocols
	protocols.SetUnencryptedHTTP2(true)
	return keyv1alpha1connect.NewUnlockServiceClient(&http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		Protocols: &protocols,
	}}, "http://admin")
}

// Status is what the admin socket says of the driver.
type Status struct {
	Node   string
	Locked bool
	// PINRetries is the PIN attempts the card has left, and -1 when the driver cannot say (a driver that holds the PIN cannot read the counter
	// without ending its login).
	PINRetries int
}

// GetStatus asks the driver whether it holds the PIN and how many attempts the card has. The call changes no counter.
func GetStatus(ctx context.Context, c keyv1alpha1connect.UnlockServiceClient) (Status, error) {
	res, err := c.Status(ctx, connect.NewRequest(&keyv1alpha1.StatusRequest{}))
	if err != nil {
		return Status{}, fmt.Errorf("bench: status: %w", err)
	}
	return Status{Node: res.Msg.GetNode(), Locked: res.Msg.GetLocked(), PINRetries: int(res.Msg.GetPinRetries())}, nil
}

// ErrNotFactory is a card that does not report all of its PIN attempts: the bench does not send it the factory PIN.
var ErrNotFactory = errors.New("bench: the card does not report all of its PIN attempts, so it is not in factory state; the PIN was not sent")

// Unlock delivers the factory PIN over the admin socket, once. It asks for the status first and refuses (ErrNotFactory) unless the driver is
// locked and the card reports all of its attempts; a driver that is unlocked already is left as it is. A PIN the card refuses is not
// sent again: the error says how many attempts are left, and a person looks at the card. When it returns nil the driver is unlocked.
func Unlock(ctx context.Context, c keyv1alpha1connect.UnlockServiceClient, out io.Writer) error {
	before, err := GetStatus(ctx, c)
	if err != nil {
		return err
	}
	if !before.Locked {
		_, _ = fmt.Fprintln(out, "unlock: the driver is unlocked already")
		return nil
	}
	if before.PINRetries != FactoryRetries {
		return fmt.Errorf("%w (attempts left: %d, needed: %d)", ErrNotFactory, before.PINRetries, FactoryRetries)
	}
	pin := []byte(FactoryPIN)
	_, err = c.Unlock(ctx, connect.NewRequest(&keyv1alpha1.UnlockRequest{Pin: pin}))
	clear(pin)
	if err != nil {
		after, serr := GetStatus(ctx, c)
		if serr != nil {
			return fmt.Errorf("bench: unlock refused, not retried: %w", err)
		}
		return fmt.Errorf("bench: unlock refused, not retried (the card has %d PIN attempts left): %w", after.PINRetries, err)
	}
	after, err := GetStatus(ctx, c)
	if err != nil {
		return err
	}
	if after.Locked {
		return errors.New("bench: unlock was accepted and the driver is still locked")
	}
	_, _ = fmt.Fprintln(out, "unlock: the driver holds the PIN")
	return nil
}

// Hold connects to the named reader, as another program that uses the card would (exclusively), says so on out and keeps the connection
// for d or until ctx ends, then lets it go without resetting the card. A driver that opens the reader meanwhile must refuse with the
// error of a reader that another process holds. It connects to the named reader only.
func Hold(ctx context.Context, open card.Opener, reader string, d time.Duration, out io.Writer) error {
	if reader == "" {
		return errors.New("bench: hold needs the name of a reader")
	}
	c, err := open(card.Selector{Reader: reader})
	if err != nil {
		return fmt.Errorf("bench: hold: %w", err)
	}
	_, _ = fmt.Fprintf(out, "held: %q for at most %s\n", reader, d)
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
	if r, ok := c.(interface{ Release() error }); ok {
		err = r.Release()
	} else {
		err = c.Close()
	}
	if err != nil {
		return fmt.Errorf("bench: hold: let the reader go: %w", err)
	}
	_, _ = fmt.Fprintf(out, "released: %q\n", reader)
	return nil
}
