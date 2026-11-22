package seal

import (
	"sync"
	"time"
)

// keyRef names a data key: its scope ("" for the installation's own), its purpose and its generation.
type keyRef struct {
	scope, purpose string
	generation     uint32
}

// line names the generations of one data key: its scope and purpose.
type line struct{ scope, purpose string }

func (r keyRef) line() line { return line{r.scope, r.purpose} }

// entry is an opened data key in the cache. Users hold the read lock while they use the bytes, so the key is never zeroed under a
// running operation.
type entry struct {
	mu      sync.RWMutex
	key     []byte
	expires time.Time
	dead    bool
	// compromised is the generation's mark as the block read it when it put the entry (docs/seal-block.md, "Compromised generations").
	compromised bool
}

// use runs fn with the key and the mark. It returns used == false when the entry was wiped before fn could run.
func (e *entry) use(fn func(key []byte, compromised bool) error) (used bool, err error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.dead {
		return false, nil
	}
	return true, fn(e.key, e.compromised)
}

// wipe zeroes the key. Go gives no guarantee that no copy of the bytes lives elsewhere (the garbage collector may have moved the
// buffer, and the AES key schedule that crypto/aes expanded from it stays in the heap until it is collected), so this is best effort.
func (e *entry) wipe() {
	e.mu.Lock()
	defer e.mu.Unlock()
	clear(e.key)
	e.key = nil
	e.dead = true
}

// cache holds opened data keys, and data keys only: never a wrapping key or an authority key, which stay in the key store. Each entry
// lives for the time to live of the block's options. It also remembers the current generation of each line it has seen, for as long.
type cache struct {
	mu      sync.Mutex
	m       map[keyRef]*entry
	current map[line]generationEntry
	ttl     time.Duration
	now     func() time.Time
	// closed is set when the block closes: a key put after it is used by its call and not kept.
	closed bool
}

type generationEntry struct {
	generation uint32
	expires    time.Time
}

func newCache(ttl time.Duration, now func() time.Time) *cache {
	return &cache{m: map[keyRef]*entry{}, current: map[line]generationEntry{}, ttl: ttl, now: now}
}

// get returns the live entry of ref, or nil. An expired entry is wiped and dropped.
func (c *cache) get(ref keyRef) *entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.m[ref]
	if e == nil {
		return nil
	}
	if !c.now().Before(e.expires) {
		delete(c.m, ref)
		go e.wipe()
		return nil
	}
	return e
}

// put stores the key under ref and takes ownership of the slice. A key already there is replaced and wiped.
func (c *cache) put(ref keyRef, key []byte, compromised bool) *entry {
	e := &entry{key: key, expires: c.now().Add(c.ttl), compromised: compromised}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return e // the block is closed: the call uses the key, and the cache does not keep it
	}
	old := c.m[ref]
	c.m[ref] = e
	c.mu.Unlock()
	if old != nil {
		go old.wipe()
	}
	return e
}

// generation returns the current generation of a line when the cache knows it.
func (c *cache) generation(l line) (uint32, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	g, ok := c.current[l]
	if !ok || !c.now().Before(g.expires) {
		delete(c.current, l)
		return 0, false
	}
	return g.generation, true
}

// setGeneration remembers the current generation of a line. A lower one than the cache holds is ignored: a generation never goes back.
func (c *cache) setGeneration(l line, g uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.current[l]; ok && old.generation > g && c.now().Before(old.expires) {
		return
	}
	c.current[l] = generationEntry{generation: g, expires: c.now().Add(c.ttl)}
}

// forgetGeneration forgets the current generation of a line, so the next call reads it from the store.
func (c *cache) forgetGeneration(l line) {
	c.mu.Lock()
	delete(c.current, l)
	c.mu.Unlock()
}

// dropLine removes every entry of a line and its current generation, and wipes the keys.
func (c *cache) dropLine(l line) {
	c.mu.Lock()
	var dead []*entry
	for ref, e := range c.m {
		if ref.line() == l {
			delete(c.m, ref)
			dead = append(dead, e)
		}
	}
	delete(c.current, l)
	c.mu.Unlock()
	for _, e := range dead {
		e.wipe()
	}
}

// drop removes the entry of ref and wipes its key. It does nothing when there is none.
func (c *cache) drop(ref keyRef) {
	c.mu.Lock()
	e := c.m[ref]
	delete(c.m, ref)
	c.mu.Unlock()
	if e != nil {
		e.wipe()
	}
}

// close stops the cache from keeping keys and wipes what it holds.
func (c *cache) close() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.forget()
}

// forget drops and wipes every entry, and forgets the current generations.
func (c *cache) forget() {
	c.mu.Lock()
	all := c.m
	c.m = map[keyRef]*entry{}
	c.current = map[line]generationEntry{}
	c.mu.Unlock()
	for _, e := range all {
		e.wipe()
	}
}
