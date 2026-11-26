package ccid

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"testing"
)

// C. The empty packet. A message of the device whose length is an exact multiple of the packet size of the bulk IN endpoint may be
// followed by an empty packet. The reader reads exactly the message (so that empty packet is still on the endpoint), and consumes
// that one empty read at the start of the next message, so that the next exchange does not take it for its answer. A device that does
// not send it is served as well. The fake of fake_test.go sends it unless noZLP is set.

// pattern is distinct bytes for a response of n bytes, so that a response mixed up with another one is seen.
func pattern(n, salt int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte((i*7 + salt*13 + i>>8) & 0xFF)
	}
	return b
}

func TestQAEmptyPacketAtEveryBoundary(t *testing.T) {
	for _, mps16 := range []uint16{8, 16, 64, 512} {
		mps := int(mps16)
		for _, zlp := range []bool{true, false} {
			t.Run(fmt.Sprintf("packet %d, empty packet sent %v", mps, zlp), func(t *testing.T) {
				var want []byte
				f := newFake(descSpec{mpsIn: mps16, mpsOut: mps16, maxMsg: 2300}, cardHandler(func([]byte) []byte { return want }))
				r, err := Open(f, Options{})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := r.PowerOn(testBudget); err != nil {
					t.Fatal(err)
				}
				f.noZLP = !zlp
				// Responses of 1, 2, 3 and 5 whole packets (with their header), and one byte on each side, back to back and mixed with
				// short ones.
				var totals []int
				for _, k := range []int{1, 2, 3, 5} {
					for _, d := range []int{-1, 0, 1} {
						if tot := k*mps + d; tot >= 12 && tot <= 2300 {
							totals = append(totals, tot)
						}
					}
				}
				n := 0
				for _, a := range totals {
					for _, b := range []int{a, 10 + 2} {
						for _, tot := range []int{a, b, 10 + 2} {
							n++
							want = pattern(tot-10, n)
							got, err := r.Transmit(apduSelect, testBudget)
							if err != nil || !bytes.Equal(got, want) {
								t.Fatalf("response of %d bytes in all (call %d): got %d bytes, err %v", tot, n, len(got), err)
							}
						}
					}
				}
				if len(f.in) > 1 || len(f.in) == 1 && len(f.in[0]) != 0 {
					t.Errorf("packets left on the endpoint: %d", len(f.in))
				}
			})
		}
	}
}

// The device sends the empty packet or not, answer by answer, and the lengths wander over the boundaries: every response is exact.
func TestQAEmptyPacketComesAndGoes(t *testing.T) {
	for _, mps16 := range []uint16{8, 64, 512} {
		mps := int(mps16)
		t.Run(fmt.Sprint(mps), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(uint64(mps), 7)) //nolint:gosec // a fixed seed, for a repeatable test
			var want []byte
			h := func(f *fakeDevice, m []byte) {
				if m[0] == msgXfrBlock {
					f.noZLP = rng.IntN(2) == 0
					tot := (1+rng.IntN(2300/mps))*mps + rng.IntN(3) - 1
					tot = min(max(tot, 12), 2300)
					want = pattern(tot-10, tot)
					f.send(msg(rspDataBlock, m[6], stOK, 0, 0, want))
					return
				}
				cardHandler(echoSW)(f, m)
			}
			f := newFake(descSpec{mpsIn: mps16, mpsOut: mps16, maxMsg: 2300}, h)
			r, err := Open(f, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.PowerOn(testBudget); err != nil {
				t.Fatal(err)
			}
			for i := range 400 {
				got, err := r.Transmit(apduSelect, testBudget)
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("call %d: %d bytes, want %d, err %v", i, len(got), len(want), err)
				}
			}
		})
	}
}

// The empty packet after a message of any kind: the answer to reset, the answers that carry only a header (packet size 10), a time
// extension, a stale answer and the blocks of a chain.
func TestQAEmptyPacketAfterEveryKindOfMessage(t *testing.T) {
	for _, zlp := range []bool{true, false} {
		t.Run(fmt.Sprintf("empty packet sent %v", zlp), func(t *testing.T) {
			t.Run("answer to reset", func(t *testing.T) {
				atr := append([]byte{0x3B, 0x8C, 0x80, 0x01}, bytes.Repeat([]byte{0x41}, 10)...) // 10+14 = 24 = 3 packets of 8
				f := newFake(descSpec{mpsIn: 8, mpsOut: 8}, func(f *fakeDevice, m []byte) {
					f.noZLP = !zlp
					if m[0] == msgIccPowerOn {
						f.send(msg(rspDataBlock, m[6], stOK, 0, 0, atr))
						return
					}
					cardHandler(echoSW)(f, m)
				})
				r, err := Open(f, Options{})
				if err != nil {
					t.Fatal(err)
				}
				for range 3 {
					if got, err := r.PowerOn(testBudget); err != nil || !bytes.Equal(got, atr) {
						t.Fatalf("%x %v", got, err)
					}
					if _, err := r.Transmit(apduSelect, testBudget); err != nil {
						t.Fatal(err)
					}
				}
			})
			t.Run("headers only, at a packet size of 10", func(t *testing.T) {
				f := newFake(descSpec{mpsIn: 10, mpsOut: 10}, func(f *fakeDevice, m []byte) {
					f.noZLP = !zlp
					cardHandler(echoSW)(f, m)
				})
				r, err := Open(f, Options{})
				if err != nil {
					t.Fatal(err)
				}
				for range 4 {
					if st, err := r.SlotStatus(testBudget); err != nil || st != SlotActive {
						t.Fatalf("%v %v", st, err)
					}
				}
				if _, err := r.PowerOn(testBudget); err != nil {
					t.Fatal(err)
				}
				if got, err := r.Transmit(apduSelect, testBudget); err != nil || !bytes.Equal(got, append(append([]byte(nil), apduSelect...), 0x90, 0)) {
					t.Fatalf("%x %v", got, err)
				}
				if err := r.PowerOff(testBudget); err != nil {
					t.Fatal(err)
				}
			})
			t.Run("time extension of a whole packet", func(t *testing.T) {
				r, _ := openPowered(t, Options{}, func(f *fakeDevice, m []byte) {
					f.noZLP = !zlp
					if m[0] == msgXfrBlock {
						f.send(msg(rspDataBlock, m[6], stTimeExt, 1, 0, bytes.Repeat([]byte{9}, 54))) // 64 bytes
						f.send(msg(rspDataBlock, m[6], stTimeExt, 1, 0, bytes.Repeat([]byte{9}, 54)))
						f.send(msg(rspDataBlock, m[6], stOK, 0, 0, []byte{0x90, 0}))
						return
					}
					cardHandler(echoSW)(f, m)
				})
				for range 3 {
					if got, err := r.Transmit(apduSelect, testBudget); err != nil || !bytes.Equal(got, []byte{0x90, 0}) {
						t.Fatalf("%x %v", got, err)
					}
				}
			})
			t.Run("stale answer of a whole packet", func(t *testing.T) {
				r, _ := openPowered(t, Options{}, func(f *fakeDevice, m []byte) {
					f.noZLP = !zlp
					if m[0] == msgXfrBlock {
						f.send(msg(rspDataBlock, m[6]-1, stOK, 0, 0, bytes.Repeat([]byte{0xEE}, 54)))
						f.send(msg(rspDataBlock, m[6], stOK, 0, 0, []byte{0x90, 0}))
						return
					}
					cardHandler(echoSW)(f, m)
				})
				for range 3 {
					if got, err := r.Transmit(apduSelect, testBudget); err != nil || !bytes.Equal(got, []byte{0x90, 0}) {
						t.Fatalf("%x %v", got, err)
					}
				}
			})
			t.Run("blocks of a response chain of whole packets", func(t *testing.T) {
				blocks := [][]byte{bytes.Repeat([]byte{1}, 54), bytes.Repeat([]byte{2}, 54), bytes.Repeat([]byte{3}, 54), {0x90, 0}}
				cc := &chainCard{respParts: blocks}
				h := cc.handler()
				r, _ := openPowered(t, Options{}, func(f *fakeDevice, m []byte) { f.noZLP = !zlp; h(f, m) })
				var want []byte
				for _, b := range blocks {
					want = append(want, b...)
				}
				for range 3 {
					if got, err := r.Transmit(apduSelect, testBudget); err != nil || !bytes.Equal(got, want) {
						t.Fatalf("%d bytes, %v", len(got), err)
					}
				}
			})
			t.Run("acknowledgements of a command chain of whole packets", func(t *testing.T) {
				// Acks have no data, so they are 10 bytes: whole packets at a packet size of 10.
				cc := &chainCard{respParts: [][]byte{{0x90, 0}}}
				h := cc.handler()
				f := newFake(descSpec{mpsIn: 10, mpsOut: 10}, func(f *fakeDevice, m []byte) { f.noZLP = !zlp; h(f, m) })
				r, err := Open(f, Options{})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := r.PowerOn(testBudget); err != nil {
					t.Fatal(err)
				}
				for range 3 {
					if got, err := r.Transmit(bytes.Repeat([]byte{5}, 700), testBudget); err != nil || !bytes.Equal(got, []byte{0x90, 0}) {
						t.Fatalf("%x %v", got, err)
					}
				}
			})
		})
	}
}

// The command direction: a message whose length is an exact multiple of the packet size of the endpoint goes out as one BulkOut of
// exactly that length. The reader sends no empty packet after it (no second BulkOut): usbfs sets no zero-packet flag for a bulk
// transfer either, and whether a reader needs one is a question for a real reader.
func TestQACommandOfAWholeNumberOfPackets(t *testing.T) {
	for _, c := range []struct {
		mps, maxMsg int
		cmds        []int // lengths of the command APDU
	}{
		{64, 271, []int{54, 118, 182, 246, 53, 55}},
		{512, 1100, []int{502, 1014, 501, 503}},
		{8, 271, []int{6, 14, 22, 30, 5, 7}},
	} {
		t.Run(fmt.Sprint(c.mps), func(t *testing.T) {
			f := newFake(descSpec{mpsIn: uint16(c.mps), mpsOut: uint16(c.mps), maxMsg: uint32(c.maxMsg)}, cardHandler(echoSW)) //nolint:gosec // small
			r, err := Open(f, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.PowerOn(testBudget); err != nil {
				t.Fatal(err)
			}
			for _, n := range c.cmds {
				outs, calls := len(f.outs), f.count("out")
				apdu := pattern(n, n)
				if got, err := r.Transmit(apdu, testBudget); err != nil || !bytes.Equal(got[:n], apdu) {
					t.Fatalf("command of %d bytes: %v", n, err)
				}
				if len(f.outs)-outs != 1 || f.count("out")-calls != 1 {
					t.Errorf("command of %d bytes: %d BulkOut calls, want one (no empty packet)", n, f.count("out")-calls)
				}
				if m := f.outs[len(f.outs)-1]; len(m) != 10+n {
					t.Errorf("command of %d bytes: a message of %d bytes", n, len(m))
				}
			}
		})
	}
}

// A new Reader on a device whose last message of the old session ended on a packet boundary: the empty packet that follows it is still
// on the endpoint. The drain of the new session reads it and drops it, so the first call works (found by this test before the drain).
func TestQANewSessionDropsTheEmptyPacketOfTheOldOne(t *testing.T) {
	f := newFake(descSpec{}, cardHandler(func([]byte) []byte { return bytes.Repeat([]byte{1}, 54) })) // 64 bytes with the header
	r1, err := Open(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r1.PowerOn(testBudget); err != nil {
		t.Fatal(err)
	}
	if _, err := r1.Transmit(apduSelect, testBudget); err != nil {
		t.Fatal(err)
	}
	if len(f.in) != 1 || len(f.in[0]) != 0 {
		t.Fatalf("the empty packet is not waiting: %d packets", len(f.in))
	}
	if err := r1.Close(); err != nil {
		t.Fatal(err)
	}
	r2, err := Open(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r2.SlotStatus(testBudget); err != nil {
		t.Errorf("the first call of the new session: %v", err)
	}
}
