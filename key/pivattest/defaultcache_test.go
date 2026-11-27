package pivattest_test

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvity/atoom/key/pivattest"
	"github.com/truvity/atoom/key/pivattest/attesttest"
)

// Default keeps only a success. Through the seam (the cache behind Default, with the clock given) a build at a time before the pinned
// certificates are valid fails, is not kept, and the next build, at a good time, is kept; the clock of the machine is not read.
func TestDefaultAfterAFailedBuild(t *testing.T) {
	var c pivattest.DefaultCache
	early := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		v, err := c.Get(func() time.Time { return early })
		if v != nil || !errors.Is(err, pivattest.ErrNoAnchor) {
			t.Fatalf("a build in 2000: %v, %v", v, err)
		}
		if c.Kept() != nil {
			t.Fatal("a failed build was kept")
		}
	}
	good, err := c.Get(func() time.Time { return t2030 })
	if err != nil || good == nil || c.Kept() != good {
		t.Fatalf("a build in 2030: %v", err)
	}
	// kept: a later call with a clock that would fail still gets it, and so does every call at once
	if v, err := c.Get(func() time.Time { return early }); v != good || err != nil {
		t.Fatalf("the kept verifier: %v, %v", v == good, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if v, err := c.Get(func() time.Time { return early }); v != good || err != nil {
				t.Error("a concurrent call did not get the kept verifier")
			}
		}()
	}
	wg.Wait()
}

// Under concurrent calls, with the clock failing: every call fails, nothing is kept; then, with the clock right, every call gets one verifier.
func TestDefaultUnderConcurrentCalls(t *testing.T) {
	var c pivattest.DefaultCache
	early := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if v, err := c.Get(func() time.Time { return early }); v != nil || err == nil {
				t.Error("a failing build gave a verifier")
			}
		}()
	}
	wg.Wait()
	if c.Kept() != nil {
		t.Fatal("a failed build was kept")
	}
	got := make([]*pivattest.Verifier, 16)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := c.Get(func() time.Time { return t2030 })
			if err != nil {
				t.Error(err)
			}
			got[i] = v
		}()
	}
	wg.Wait()
	for _, v := range got {
		if v == nil || v != got[0] {
			t.Fatal("the concurrent calls did not get one verifier")
		}
	}
	// the build over the vendor's files at a fixed time: also through the seam of the files
	f := embedded(t)
	if _, err := pivattest.BuildFromAt(f.rootPEM, f.bundlePEM, pivattest.Pins(), early); !errors.Is(err, pivattest.ErrNoAnchor) {
		t.Errorf("the vendor's files at 2000: %v", err)
	}
	if v, err := pivattest.BuildFromAt(f.rootPEM, f.bundlePEM, pivattest.Pins(), t2030); err != nil || v == nil {
		t.Errorf("the vendor's files at 2030: %v", err)
	}
	// Default itself, as the public function: the same verifier whoever asks (the clock of the machine is inside the validity of the files)
	d1, err := pivattest.Default()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if d, err := pivattest.Default(); d != d1 || err != nil {
				t.Error("Default is not one verifier")
			}
		}()
	}
	wg.Wait()
}

// A damaged or cut block in the middle of a bundle. The comment of NewVerifierFromPEM says the whole blocks around it are read. A damaged
// block (armor or base64) is skipped alone. A block that is cut where the next block begins on the same line (no newline at the cut) also
// swallows the next block: that is pinned here as it is, and the comment does not say it.
func TestADamagedBlockInTheMiddleOfABundle(t *testing.T) {
	p := fixedPKI(t, attesttest.PKIOptions{})
	// the bundle of the test PKI is the intermediate, the FIDO one and the PIV one: the middle block is the FIDO one
	blocks := strings.SplitAfter(string(p.BundlePEM()), "-----END CERTIFICATE-----\n")
	blocks = blocks[:3]
	slot := fixedSlot(t, p, attesttest.SlotOptions{})
	build := func(bs ...string) (*pivattest.Verifier, error) {
		return pivattest.NewVerifierFromPEM(p.RootPEM(), []byte(strings.Join(bs, "")), p.RootSHA256())
	}
	mid := blocks[1]
	damages := map[string]string{
		"a character that is not base64": strings.Replace(mid, "-----BEGIN CERTIFICATE-----\n", "-----BEGIN CERTIFICATE-----\n!", 1),
		"the begin armor damaged":        strings.Replace(mid, "-----BEGIN CERTIFICATE-----", "-----BEGIN CERTIFICATE----", 1),
		"the end armor damaged":          strings.Replace(mid, "-----END CERTIFICATE-----", "-----END CERTIFICATE----", 1),
		"cut, with a newline":            mid[:len(mid)/2] + "\n",
		"the begin line only":            "-----BEGIN CERTIFICATE-----\n",
		"text":                           "a damaged block was here\n",
	}
	for name, d := range damages {
		v, err := build(blocks[0], d, blocks[2])
		if err != nil {
			t.Errorf("%s: the blocks around it are not read: %v", name, err)
			continue
		}
		if ev, err := v.VerifyAt(t2030, slot, p.F9); err != nil || !ev.Generated {
			t.Errorf("%s: %+v, %v", name, ev, err)
		}
	}
	// cut at a place where the next block begins on the same line: the next block is lost with it
	if _, err := build(blocks[0], mid[:len(mid)/2], blocks[2]); !errors.Is(err, pivattest.ErrNoAnchor) {
		t.Errorf("a block cut with no newline before the PIV block: %v", err)
	}
	// damage in the block that links the PIV intermediate to the root: the PIV intermediate cannot be admitted
	if _, err := build(strings.Replace(blocks[0], "-----BEGIN CERTIFICATE-----\n", "-----BEGIN CERTIFICATE-----\n!", 1), blocks[1], blocks[2]); !errors.Is(err, pivattest.ErrNoAnchor) {
		t.Errorf("the link block damaged: %v", err)
	}
	// the vendor's bundle: any damaged block that is not a pinned one leaves the table satisfied and the anchors as they were
	f := embedded(t)
	all := strings.SplitAfter(string(f.bundlePEM), "-----END CERTIFICATE-----\n")
	all = all[:len(all)-1]
	pinned := map[string]bool{}
	for _, pin := range pivattest.Pins() {
		pinned[pin.SHA256] = true
	}
	base, err := pivattest.BuildFromAt(f.rootPEM, f.bundlePEM, pivattest.Pins(), t2030)
	if err != nil {
		t.Fatal(err)
	}
	for k, d := range f.bundleDER {
		bs := slices.Clone(all)
		bs[k] = strings.Replace(bs[k], "-----BEGIN CERTIFICATE-----\n", "-----BEGIN CERTIFICATE-----\n!", 1)
		v, err := pivattest.BuildFromAt(f.rootPEM, []byte(strings.Join(bs, "")), pivattest.Pins(), t2030)
		if pinned[sha(d)] {
			if !errors.Is(err, pivattest.ErrPinTable) && !errors.Is(err, pivattest.ErrNoAnchor) {
				t.Errorf("block %d (%s) is pinned and was damaged: %v", k, cnOf(t, d), err)
			}
			continue
		}
		if err != nil || !slices.Equal(subjects(v), subjects(base)) {
			t.Errorf("block %d (%s) is not pinned and was damaged: %v", k, cnOf(t, d), err)
		}
	}
}
