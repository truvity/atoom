package policy

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"

	sourcev1alpha1 "github.com/truvity/atoom/source/gen/atoom/source/v1alpha1"
	"github.com/truvity/atoom/source/gen/atoom/source/v1alpha1/sourcev1alpha1connect"
)

// The defaults of Options.
const (
	// DefaultName is the name the bundle is read by through the source port (the file driver serves it from policy-bundle.jws).
	DefaultName = "policy-bundle"
	// DefaultKind is the kind the protected header must name.
	DefaultKind = "PolicyBundle"
	// DefaultMinInterval is the least time between two reads of the source by Run.
	DefaultMinInterval = time.Second
	// DefaultPollEvery is how often Run reads a POLL source that gives no poll_after, and how often it reads a NOTIFY source while its
	// watch cannot be opened.
	DefaultPollEvery = 30 * time.Second
	// readTimeout bounds one call to the source.
	readTimeout = 10 * time.Second
)

// Options are what a host gives the loader.
type Options struct {
	// Source is the client of the source port that serves the bundle. Required.
	Source sourcev1alpha1connect.SourceServiceClient
	// Name is the name the bundle is read by; DefaultName when empty.
	Name string
	// Kind is the kind the protected header must name; DefaultKind when empty. A host whose bundles a second party co-signs names the
	// kind that party's key co-signs.
	Kind string
	// Publishers are the pinned keys. At least one has no CoSigns. Required.
	Publishers []Publisher
	// MinInterval is the least time between two reads by Run; DefaultMinInterval when zero.
	MinInterval time.Duration
	// PollEvery: see DefaultPollEvery; used when zero.
	PollEvery time.Duration
	// OnReload, when set, is told the outcome of the start and of every reload that is not Unchanged, for the host's audit record.
	OnReload func(Outcome)
}

// Result is what one pass of the loader did.
type Result int

// The results of a pass.
const (
	// Applied: a new set is in force.
	Applied Result = iota
	// Unchanged: the source has the version in force, or a document with the seq and digest in force.
	Unchanged
	// Rejected: the document or the bundle was refused (a *Refused, a *Fault, or a sequence error); the set in force stays.
	Rejected
	// Unavailable: the source could not be read; the set in force stays.
	Unavailable
)

func (r Result) String() string {
	switch r {
	case Applied:
		return "applied"
	case Unchanged:
		return "unchanged"
	case Rejected:
		return "rejected"
	case Unavailable:
		return "unavailable"
	}
	return "unknown"
}

// Outcome is the result of one pass, the digest and seq of the set in force after it, and the error of a Rejected or Unavailable pass.
type Outcome struct {
	Result Result
	Digest string
	Seq    uint64
	Err    error
}

// ErrOlder and ErrConflict are the sequence refusals of a reload (step 5).
var (
	ErrOlder    = errors.New("policy: signed document: step 5: a lower seq than the one in force")
	ErrConflict = errors.New("policy: signed document: step 5: the same seq with another payload")
)

// Loader keeps the set of one bundle in force, by the loading rules of record 0019.
type Loader struct {
	opts    Options
	current atomic.Pointer[Set]
	mu      sync.Mutex // one pass at a time
	version string     // the version label of the source for the set in force
	digest  [48]byte
	last    time.Time // the time of the last read by Run
	// pollAfter is the poll_after of the last answer of a POLL source.
	pollAfter time.Duration
}

// New reads, verifies and compiles the bundle once: the cold start. It returns an error, and no loader, when there is no valid set: a
// host does not start without one.
func New(ctx context.Context, opts Options) (*Loader, error) {
	if opts.Source == nil {
		return nil, errors.New("policy: Options.Source is required")
	}
	if len(opts.Publishers) == 0 {
		return nil, errors.New("policy: Options.Publishers is required")
	}
	if opts.Name == "" {
		opts.Name = DefaultName
	}
	if opts.Kind == "" {
		opts.Kind = DefaultKind
	}
	if opts.MinInterval <= 0 {
		opts.MinInterval = DefaultMinInterval
	}
	if opts.PollEvery <= 0 {
		opts.PollEvery = DefaultPollEvery
	}
	l := &Loader{opts: opts}
	out := l.Reload(ctx)
	if out.Result != Applied {
		return nil, fmt.Errorf("policy: no valid policy set at start: %w", out.Err)
	}
	return l, nil
}

// Current is the set in force. It is never nil after New.
func (l *Loader) Current() *Set { return l.current.Load() }

// Decide decides r with the set in force (Set.Decide). It does no I/O.
func (l *Loader) Decide(r Request) (Decision, error) { return l.Current().Decide(r) }

// Reload is one pass: read the source with the version label in force, verify, compare the seq, compile, and swap the set in, or keep
// the set in force. It is safe to call at any time; Run calls it.
func (l *Loader) Reload(ctx context.Context) Outcome {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.pass(ctx)
	if cur := l.current.Load(); cur != nil {
		out.Digest, out.Seq = cur.Digest(), cur.Seq()
	}
	if out.Result != Unchanged && l.opts.OnReload != nil {
		l.opts.OnReload(out)
	}
	return out
}

func (l *Loader) pass(ctx context.Context) Outcome {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	resp, err := l.opts.Source.Read(ctx, connect.NewRequest(&sourcev1alpha1.ReadRequest{Name: l.opts.Name, IfNoneMatch: l.version}))
	if err != nil {
		return Outcome{Result: Unavailable, Err: fmt.Errorf("policy: read %q: %w", l.opts.Name, err)}
	}
	m := resp.Msg
	if pa := m.GetPollAfter(); pa != nil {
		l.pollAfter = pa.AsDuration()
	}
	cur := l.current.Load()
	if m.GetUnchanged() && cur != nil {
		return Outcome{Result: Unchanged}
	}
	if m.GetUnchanged() {
		return Outcome{Result: Unavailable, Err: errors.New("policy: the source answered unchanged with nothing in force")}
	}
	v, err := VerifyDocument(m.GetData(), l.opts.Kind, l.opts.Publishers)
	if err != nil {
		return Outcome{Result: Rejected, Err: err}
	}
	var seq uint64
	if cur != nil {
		seq = cur.Seq()
	}
	switch Compare(cur != nil, seq, l.digest, v) {
	case Same:
		l.version = m.GetVersion()
		return Outcome{Result: Unchanged}
	case Older:
		return Outcome{Result: Rejected, Err: ErrOlder}
	case Conflict:
		return Outcome{Result: Rejected, Err: ErrConflict}
	case Newer:
	}
	set, err := CompileVerified(v)
	if err != nil {
		return Outcome{Result: Rejected, Err: err}
	}
	l.current.Store(set)
	l.version, l.digest = m.GetVersion(), v.Digest
	return Outcome{Result: Applied}
}

// Run keeps the set current until ctx ends, as the source says it can: NOTIFY, a watch of the name, and a read after each message;
// POLL, a read after poll_after; NONE, nothing (a Lambda layer does not change), and Run returns at once. Two reads are at least
// MinInterval apart. A failed read or a refused document keeps the set in force.
func (l *Loader) Run(ctx context.Context) error {
	caps, err := l.opts.Source.Capabilities(ctx, connect.NewRequest(&sourcev1alpha1.CapabilitiesRequest{}))
	if err != nil {
		return fmt.Errorf("policy: the capabilities of the source: %w", err)
	}
	switch caps.Msg.GetWatch() {
	case sourcev1alpha1.Watch_NONE:
		return nil
	case sourcev1alpha1.Watch_NOTIFY:
		return l.runWatch(ctx)
	default:
		return l.runPoll(ctx)
	}
}

// throttled waits until MinInterval has passed since the last read, then reads. It returns false when ctx ended.
func (l *Loader) throttled(ctx context.Context) bool {
	if wait := time.Until(l.last.Add(l.opts.MinInterval)); wait > 0 {
		if !sleep(ctx, wait) {
			return false
		}
	}
	l.last = time.Now()
	l.Reload(ctx)
	return ctx.Err() == nil
}

func (l *Loader) runPoll(ctx context.Context) error {
	for {
		wait := l.opts.PollEvery
		if d := l.pollAfterHeld(); d > 0 {
			wait = d
		}
		if !sleep(ctx, max(wait, l.opts.MinInterval)) || !l.throttled(ctx) {
			return nil
		}
	}
}

func (l *Loader) runWatch(ctx context.Context) error {
	for ctx.Err() == nil {
		stream, err := l.opts.Source.Watch(ctx, connect.NewRequest(&sourcev1alpha1.SourceWatchRequest{Name: l.opts.Name}))
		if err != nil {
			// The watch cannot be opened: read on a timer until it can.
			if !sleep(ctx, l.opts.PollEvery) || !l.throttled(ctx) {
				return nil
			}
			continue
		}
		// A change may have come between the last read and the opening of the watch.
		if !l.throttled(ctx) {
			_ = stream.Close()
			return nil
		}
		for stream.Receive() {
			if !l.throttled(ctx) {
				_ = stream.Close()
				return nil
			}
		}
		_ = stream.Close()
		if !sleep(ctx, l.opts.MinInterval) {
			return nil
		}
	}
	return nil
}

func (l *Loader) pollAfterHeld() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.pollAfter
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
