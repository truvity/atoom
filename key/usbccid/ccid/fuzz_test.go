package ccid

import (
	"errors"
	"testing"
	"time"
)

// FuzzAnswers feeds the reader whatever bytes a hostile or broken reader could send in answer to one command, cut into packets as the
// data says. Whatever arrives, the reader neither panics nor loops, never returns more than MaxResponseAPDU bytes, never sends
// a second message after an error that ends the session, and ends the session on every error but a card-level failure of the only
// message.
func FuzzAnswers(f *testing.F) {
	ok := msg(rspDataBlock, 3, stOK, 0, 0, []byte{0x90, 0})
	f.Add(ok, uint8(0))
	f.Add(append(append([]byte(nil), ok...), msg(rspDataBlock, 3, stOK, 0, 0, nil)...), uint8(7))
	f.Add(msg(rspDataBlock, 3, stFailed|1, 0xFE, 0, nil), uint8(0))
	f.Add(msg(rspDataBlock, 3, stTimeExt, 1, 0, nil), uint8(0))
	f.Add(msg(rspDataBlock, 3, stOK, 0, chainBegin, []byte{1, 2}), uint8(1))
	f.Add(msg(rspDataBlock, 2, stOK, 0, 0, nil), uint8(0))
	f.Add(make([]byte, 64), uint8(3))
	f.Add([]byte{}, uint8(0))
	f.Fuzz(func(t *testing.T, data []byte, cut uint8) {
		h := func(f *fakeDevice, m []byte) {
			if m[0] != msgXfrBlock {
				cardHandler(echoSW)(f, m)
				return
			}
			// Cut the bytes into packets of sizes that the cut byte and the data choose (at most 70 bytes each).
			rest := data
			for i := 0; len(rest) > 0 && i < 40; i++ {
				n := (int(cut) + i*int(cut) + int(rest[0])) % 71
				n = min(n, len(rest))
				f.sendRaw(rest[:n])
				rest = rest[n:]
			}
			if len(rest) > 0 {
				f.sendRaw(rest)
			}
		}
		r, fk := openPowered(t, Options{}, h)
		outs := fk.nOut
		resp, err := r.Transmit(apduSelect, 50*time.Millisecond)
		if len(resp) > MaxResponseAPDU {
			t.Fatalf("a response of %d bytes", len(resp))
		}
		if err == nil {
			return
		}
		if resp != nil {
			t.Fatal("a response with an error")
		}
		var ce *CommandError
		lost := errors.Is(err, ErrSessionLost)
		if !lost && !errors.As(err, &ce) {
			t.Fatalf("an error that is neither a lost session nor a card-level failure: %v", err)
		}
		if fk.nOut < outs+1 {
			t.Fatal("no message sent for the command")
		}
		if lost {
			mark := fk.mark()
			if _, err2 := r.Transmit(apduSelect, testBudget); !errors.Is(err2, err) {
				t.Fatalf("second call: %v", err2)
			}
			if len(fk.callsAfter(mark)) != 0 {
				t.Fatal("the port was used after the loss")
			}
		}
	})
}
