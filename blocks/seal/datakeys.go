package seal

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	sealv1alpha1 "github.com/truvity/atoom/blocks/seal/gen/atoom/seal/v1alpha1"
	"github.com/truvity/atoom/blocks/seal/internal/wrap"
	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
)

// errNoKey says there is no wrap of the data key at all: the key was never made, or it was erased.
var errNoKey = errors.New("seal: no such data key")

// errNotWrapped says the data key exists and no wrap of it is for this instance yet: another instance made it and no Rewrap has wrapped
// it for this one. A new key is never made then, because it would lose the create race at best and split the scope's data across two
// keys at worst.
var errNotWrapped = errors.New("seal: the data key has no wrap for this instance yet")

// errNoSelf says this instance is not among the recipients, so a key it made would be lost with its cache entry.
var errNoSelf = errors.New("seal: this instance's wrapping key is not among the recipients")

// errKeyPort is a call to the key port that failed.
var errKeyPort = errors.New("seal: the key port failed")

// openTimeout bounds one open of a key from its wrap: the read of the wrap and the ECDH of the key port. A stuck driver ends the open
// with this, and not the callers one by one.
const openTimeout = 20 * time.Second

// flight is one open of a key that is under way: the callers that want the same key wait for it.
type flight struct {
	done  chan struct{}
	entry *entry
	err   error
}

func (b *Block) wrapContext(ref keyRef, recipient string) wrap.Context {
	return wrap.Context{
		Domain: b.opts.WrapDomain, Installation: b.opts.Installation, Scope: scopePart(ref.scope),
		Purpose: ref.purpose, Generation: ref.generation, Recipient: recipient,
	}
}

// withKey runs fn with the data key of ref, opened from this instance's wrap and cached. With create, a key that has no wrap at all is
// made first. The bytes are valid only during fn.
func (b *Block) withKey(ctx context.Context, ref keyRef, create bool, fn func(key []byte) error) error {
	return b.withKeyMarked(ctx, ref, create, func(key []byte, _ bool) error { return fn(key) })
}

// withKeyMarked is withKey with the generation's compromised mark, as read when the key entered the cache.
func (b *Block) withKeyMarked(ctx context.Context, ref keyRef, create bool, fn func(key []byte, compromised bool) error) error {
	for range 3 {
		e := b.cache.get(ref)
		if e == nil {
			var err error
			e, err = b.openShared(ctx, ref)
			if errors.Is(err, errNoKey) && create {
				e, err = b.make(ctx, ref)
			}
			if err != nil {
				return err
			}
		}
		if used, err := e.use(fn); used {
			return err
		}
		// The entry was wiped between get and use (it expired, or was dropped): load it again.
	}
	return errNotWrapped
}

// openShared opens the key of ref from this instance's wrap, once for all the callers that ask for it at the same time. The work runs in
// a goroutine of its own, on the first caller's context without its cancellation (its values, the correlation id among them, stay) and
// under openTimeout, so a caller that goes away does not fail the others. Every caller waits for the open or for its own context.
func (b *Block) openShared(ctx context.Context, ref keyRef) (*entry, error) {
	b.flightMu.Lock()
	f, ok := b.flights[ref]
	if !ok {
		f = &flight{done: make(chan struct{})}
		b.flights[ref] = f
		go b.runFlight(context.WithoutCancel(ctx), ref, f)
	}
	b.flightMu.Unlock()
	select {
	case <-f.done:
		return f.entry, f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *Block) runFlight(ctx context.Context, ref keyRef, f *flight) {
	defer func() {
		if r := recover(); r != nil {
			f.entry, f.err = nil, fmt.Errorf("seal: opening a data key panicked (%T)", r)
		}
		b.flightMu.Lock()
		delete(b.flights, ref)
		b.flightMu.Unlock()
		close(f.done)
	}()
	ctx, cancel := context.WithTimeout(ctx, openTimeout)
	defer cancel()
	key, err := b.openOwn(ctx, ref)
	if err != nil {
		f.err = err
		return
	}
	f.entry, f.err = b.putOpened(ctx, ref, key)
}

// putOpened reads the generation's mark and caches an opened key with it: the mark is read each time the cache refills. A mark that
// cannot be read keeps the key out of the cache, failing closed.
func (b *Block) putOpened(ctx context.Context, ref keyRef, key []byte) (*entry, error) {
	compromised, err := b.readMark(ctx, ref)
	if err != nil {
		clear(key)
		return nil, err
	}
	return b.cache.put(ref, key, compromised), nil
}

// openOwn reads this instance's wrap of ref and opens it: errNoKey when there is no wrap of the key at all, errNotWrapped when there
// are wraps and none is this instance's.
func (b *Block) openOwn(ctx context.Context, ref keyRef) ([]byte, error) {
	w, found, err := b.getWrap(ctx, ref, b.recipient)
	if err != nil {
		return nil, err
	}
	if found {
		return b.unwrap(ctx, ref, w)
	}
	others, err := b.listWraps(ctx, b.linePrefix(ref.line()))
	if err != nil {
		return nil, err
	}
	// The listing is read after the Get, so a key that another caller made in between shows up here, its wrap for this instance
	// included: open that one rather than answer that there is none.
	notWrapped := false
	for _, o := range others {
		if o.ref != ref {
			continue
		}
		if o.recipient == b.recipient {
			return b.unwrap(ctx, ref, o.wrap)
		}
		notWrapped = true
	}
	if notWrapped {
		return nil, errNotWrapped
	}
	return nil, errNoKey
}

// unwrap opens a wrap addressed to this instance: the key port does the ECDH with the wrapping key, the block derives the key-encryption
// key and unwraps. The shared secret is zeroed after use.
func (b *Block) unwrap(ctx context.Context, ref keyRef, w *sealv1alpha1.DataKeyWrap) ([]byte, error) {
	if w.GetPurpose() != ref.purpose || w.GetGeneration() != ref.generation || w.GetRecipient() != b.recipient {
		return nil, errors.New("seal: the wrap does not match its key")
	}
	if _, err := wrap.ParsePublic(w.GetEphemeralPublic()); err != nil {
		return nil, errors.New("seal: the wrap has a bad ephemeral key")
	}
	res, err := b.opts.Keys.Ecdh(ctx, connect.NewRequest(&keyv1alpha1.EcdhRequest{Purpose: b.opts.WrappingPurpose, PeerPublic: w.GetEphemeralPublic()}))
	if err != nil {
		return nil, errKeyPort
	}
	secret := res.Msg.GetSharedSecret()
	defer clear(secret)
	key, err := wrap.Unwrap(b.wrapContext(ref, b.recipient), secret, w.GetWrappedKey())
	if err != nil {
		return nil, errors.New("seal: the wrap does not open")
	}
	return key, nil
}

// recipient is a wrapping public key with its label.
type recipient struct {
	label string
	pub   *ecdh.PublicKey
}

// recipients returns who a data key is wrapped for, this instance included, sorted by label and each once.
func (b *Block) recipients(ctx context.Context) ([]recipient, error) {
	if b.opts.Recipients == nil {
		return []recipient{{label: b.recipient, pub: b.wrapping}}, nil
	}
	keys, err := b.opts.Recipients(ctx)
	if err != nil {
		return nil, fmt.Errorf("seal: the recipients: %w", err)
	}
	seen := map[string]bool{}
	var out []recipient
	for _, k := range keys {
		if k == nil {
			continue
		}
		l := recipientLabel(k)
		if seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, recipient{label: l, pub: k})
	}
	if !seen[b.recipient] {
		return nil, errNoSelf
	}
	return out, nil
}

// wrapFor wraps dataKey for one recipient.
func (b *Block) wrapFor(ref keyRef, r recipient, dataKey []byte) (wrapItem, error) {
	eph, wrapped, err := wrap.Wrap(b.wrapContext(ref, r.label), r.pub, dataKey)
	if err != nil {
		return wrapItem{}, err
	}
	return wrapItem{ref: ref, recipient: r.label, wrap: &sealv1alpha1.DataKeyWrap{
		Purpose: ref.purpose, Generation: ref.generation, Recipient: r.label, EphemeralPublic: eph, WrappedKey: wrapped,
	}}, nil
}

// make makes the data key of ref when no wrap of it exists, one maker at a time: 32 random bytes from the operating system, wrapped for
// every recipient and written in one transaction of conditional creates. A create that loses means another instance made the key first:
// this one is discarded and the winner's is opened.
func (b *Block) make(ctx context.Context, ref keyRef) (*entry, error) {
	b.makeMu.Lock()
	defer b.makeMu.Unlock()
	if e := b.cache.get(ref); e != nil {
		return e, nil
	}
	key, err := b.openOwn(ctx, ref)
	switch {
	case err == nil:
		return b.putOpened(ctx, ref, key)
	case !errors.Is(err, errNoKey):
		return nil, err
	}
	recips, err := b.recipients(ctx)
	if err != nil {
		return nil, err
	}
	key = make([]byte, wrap.DataKeyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("seal: random: %w", err)
	}
	items := make([]wrapItem, 0, len(recips))
	for _, r := range recips {
		it, err := b.wrapFor(ref, r, key)
		if err != nil {
			clear(key)
			return nil, fmt.Errorf("seal: wrap: %w", err)
		}
		items = append(items, it)
	}
	switch err := b.createWraps(ctx, items); {
	case err == nil:
	case errors.Is(err, errTaken):
		clear(key)
		key, err := b.openOwn(ctx, ref) // errNotWrapped if the winner left this instance out
		if err != nil {
			return nil, err
		}
		return b.putOpened(ctx, ref, key)
	default:
		clear(key)
		return nil, err
	}
	b.observe(ctx, Event{Action: EventMake, Purpose: ref.purpose, Scope: ref.scope, Generation: ref.generation, Count: len(items)})
	for _, it := range items {
		b.observe(ctx, Event{Action: EventWrap, Purpose: ref.purpose, Scope: ref.scope, Generation: ref.generation, Recipient: it.recipient})
	}
	return b.cache.put(ref, key, false), nil
}

// current returns the current generation of a line: the highest that has a wrap, or 0 when the key was never made (or was erased).
func (b *Block) current(ctx context.Context, l line) (uint32, error) {
	if g, ok := b.cache.generation(l); ok {
		return g, nil
	}
	wraps, err := b.listWraps(ctx, b.linePrefix(l))
	if err != nil {
		return 0, err
	}
	var g uint32
	for _, w := range wraps {
		g = max(g, w.ref.generation)
	}
	if g > 0 {
		b.cache.setGeneration(l, g)
	}
	return g, nil
}

// rotate makes generation after+1 of a line, or finds that another instance made it, and makes it the current one here.
func (b *Block) rotate(ctx context.Context, l line) (uint32, error) {
	g, err := b.current(ctx, l)
	if err != nil {
		return 0, err
	}
	// Another instance may have rotated since this one cached the generation: read the line again.
	wraps, err := b.listWraps(ctx, b.linePrefix(l))
	if err != nil {
		return 0, err
	}
	for _, w := range wraps {
		g = max(g, w.ref.generation)
	}
	next := keyRef{scope: l.scope, purpose: l.purpose, generation: g + 1}
	if _, err := b.make(ctx, next); err != nil {
		return 0, err
	}
	b.cache.setGeneration(l, next.generation)
	b.observe(ctx, Event{Action: EventRotate, Purpose: l.purpose, Scope: l.scope, Generation: next.generation})
	return next.generation, nil
}

// rotateFrom moves a line past generation g: to the newest generation when another instance has already made one, or to g+1, which it
// makes. The seal limit and a compromised mark use it, so that two instances that meet the same limit make one generation, not two.
func (b *Block) rotateFrom(ctx context.Context, l line, g uint32, reason string) (uint32, error) {
	b.cache.forgetGeneration(l)
	cur, err := b.current(ctx, l)
	if err != nil {
		return 0, err
	}
	if cur > g {
		return cur, nil
	}
	next := keyRef{scope: l.scope, purpose: l.purpose, generation: g + 1}
	if _, err := b.make(ctx, next); err != nil {
		return 0, err
	}
	b.cache.setGeneration(l, next.generation)
	b.observe(ctx, Event{Action: EventRotate, Purpose: l.purpose, Scope: l.scope, Generation: next.generation, Reason: reason})
	return next.generation, nil
}

// erase deletes every wrap of a line, every generation and every recipient, with the counters and marks of its generations, and drops
// the line from the cache.
func (b *Block) erase(ctx context.Context, l line) (int, error) {
	wraps, err := b.listWraps(ctx, b.linePrefix(l))
	if err != nil {
		return 0, err
	}
	n, err := b.deleteWraps(ctx, wraps)
	if err == nil {
		err = b.deleteLineState(ctx, l)
	}
	b.cache.dropLine(l)
	b.dropBudgets(l)
	if n > 0 {
		b.observe(ctx, Event{Action: EventErase, Purpose: l.purpose, Scope: l.scope, Count: n})
	}
	return n, err
}

// rewrap is one rewrap pass: for each data key this instance can open, it writes a wrap for every recipient that lacks one, and it drops
// from the cache every key whose wrap for this instance is gone (an Erase on another instance). It is idempotent: a create that finds the
// wrap there is fine.
func (b *Block) rewrap(ctx context.Context) (int, error) {
	recips, err := b.recipients(ctx)
	if err != nil {
		return 0, err
	}
	all, err := b.listWraps(ctx, b.wrapsPrefix())
	if err != nil {
		return 0, err
	}
	have := map[keyRef]map[string]bool{}
	for _, w := range all {
		if have[w.ref] == nil {
			have[w.ref] = map[string]bool{}
		}
		have[w.ref][w.recipient] = true
	}
	b.dropErased(have)
	if err := b.dropMarked(ctx); err != nil {
		return 0, err
	}
	written := 0
	var errs []error
	for ref, got := range have {
		if !got[b.recipient] {
			continue // not this instance's to open: another will wrap it for this one
		}
		var missing []recipient
		for _, r := range recips {
			if !got[r.label] {
				missing = append(missing, r)
			}
		}
		if len(missing) == 0 {
			continue
		}
		err := b.withKey(ctx, ref, false, func(key []byte) error {
			for _, r := range missing {
				it, err := b.wrapFor(ref, r, key)
				if err != nil {
					return err
				}
				switch err := b.createWraps(ctx, []wrapItem{it}); {
				case err == nil:
					written++
					b.observe(ctx, Event{Action: EventWrap, Purpose: ref.purpose, Scope: ref.scope, Generation: ref.generation, Recipient: r.label})
				case errors.Is(err, errTaken): // another instance was first
				default:
					return err
				}
			}
			return nil
		})
		if err != nil {
			errs = append(errs, err)
		}
	}
	return written, errors.Join(errs...)
}

// dropErased drops from the cache the data keys that have no wrap for this instance any more. The listing of the pass is the proof that
// the wrap is gone; a key made after the listing was taken is dropped for nothing and opened again from its wrap.
func (b *Block) dropErased(have map[keyRef]map[string]bool) {
	lines := map[line]bool{}
	for ref := range have {
		lines[ref.line()] = true
	}
	b.cache.mu.Lock()
	var gone []keyRef
	for ref := range b.cache.m {
		if !have[ref][b.recipient] {
			gone = append(gone, ref)
		}
	}
	for l := range b.cache.current {
		if !lines[l] {
			delete(b.cache.current, l)
		}
	}
	b.cache.mu.Unlock()
	for _, ref := range gone {
		b.cache.drop(ref)
	}
}
