package file_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/common/call"
	"github.com/truvity/atoom/common/refusal"
	"github.com/truvity/atoom/source/file"
	sourcev1alpha1 "github.com/truvity/atoom/source/gen/atoom/source/v1alpha1"
	"github.com/truvity/atoom/source/gen/atoom/source/v1alpha1/sourcev1alpha1connect"
)

// client mounts the driver over dir on an in-memory transport and returns a generated client of it, with the options that put the
// correlation id of the context on every call.
func client(t *testing.T, dir string, opts ...connect.HandlerOption) sourcev1alpha1connect.SourceServiceClient {
	t.Helper()
	svc, err := file.New(file.Options{Dir: open(t, dir), WatchEvery: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	path, h := svc.Handler(opts...)
	hc := inMemory(t, path, h)
	return sourcev1alpha1connect.NewSourceServiceClient(hc, "http://source", call.ClientOptions()...)
}

func read(c sourcev1alpha1connect.SourceServiceClient, name, held string) (*sourcev1alpha1.ReadResponse, error) {
	res, err := c.Read(context.Background(), connect.NewRequest(&sourcev1alpha1.ReadRequest{Name: name, IfNoneMatch: held}))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

func TestCapabilitiesSaysThatWatchNotifies(t *testing.T) {
	c := client(t, t.TempDir())
	res, err := c.Capabilities(context.Background(), connect.NewRequest(&sourcev1alpha1.CapabilitiesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Msg.GetWatch() != sourcev1alpha1.Watch_NOTIFY {
		t.Fatalf("watch = %v, want NOTIFY", res.Msg.GetWatch())
	}
}

func TestADocumentIsReadAndEveryRefusalNamesAReason(t *testing.T) {
	root := t.TempDir()
	put(t, root, "boundary.jws", []byte("doc one"))
	c := client(t, root)

	first, err := read(c, "boundary", "")
	if err != nil || string(first.GetData()) != "doc one" || first.GetUnchanged() {
		t.Fatalf("first read: %v %v", first, err)
	}
	again, err := read(c, "boundary", first.GetVersion())
	if err != nil || !again.GetUnchanged() || len(again.GetData()) != 0 || again.GetVersion() != first.GetVersion() {
		t.Fatalf("read at the label held: %v %v", again, err)
	}
	other, err := read(c, "boundary", "not-the-label")
	if err != nil || other.GetUnchanged() || string(other.GetData()) != "doc one" {
		t.Fatalf("read at another label: %v %v", other, err)
	}

	for _, tc := range []struct {
		name   string
		code   connect.Code
		reason string
	}{
		{"upstream", connect.CodeNotFound, "absent"},
		{"../boundary", connect.CodeInvalidArgument, "schema"},
	} {
		_, err := read(c, tc.name, "")
		if connect.CodeOf(err) != tc.code {
			t.Fatalf("%s: code %v (%v), want %v", tc.name, connect.CodeOf(err), err, tc.code)
		}
		if reason, _ := refusal.Reason(err); reason != tc.reason {
			t.Fatalf("%s: reason %q, want %q", tc.name, reason, tc.reason)
		}
		if path, _ := refusal.FieldPath(err); path != "name" {
			t.Fatalf("%s: field %q, want name", tc.name, path)
		}
	}

	// A link that leaves the directory, and a directory in the place of a document, are `bound`.
	link(t, "../outside", filepath.Join(root, "audit-log.jws"))
	if err := os.Mkdir(filepath.Join(root, "audit-catalogue.jws"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"audit-log", "audit-catalogue"} {
		_, err := read(c, name, "")
		if connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Fatalf("%s: code %v (%v), want failed_precondition", name, connect.CodeOf(err), err)
		}
		if reason, _ := refusal.Reason(err); reason != "bound" {
			t.Fatalf("%s: reason %q, want bound", name, reason)
		}
	}
}

func TestWatchTellsOfAReplacedAndARemovedDocumentAndOfNothingElse(t *testing.T) {
	root := t.TempDir()
	put(t, root, "key-plan.jws", []byte("one"))
	c := client(t, root)
	held, err := read(c, "key-plan", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := c.Watch(ctx, connect.NewRequest(&sourcev1alpha1.SourceWatchRequest{Name: "key-plan"}))
	if err != nil {
		t.Fatal(err)
	}
	said := make(chan *sourcev1alpha1.SourceWatchResponse, 4)
	go func() {
		for w.Receive() {
			said <- w.Msg()
		}
		close(said)
	}()

	// Nothing changes: nothing is said.
	select {
	case m := <-said:
		t.Fatalf("told of a change that did not happen: %v", m)
	case <-time.After(300 * time.Millisecond):
	}

	// The swap of a document, as a pipeline or a mounted ConfigMap does it: write beside, rename.
	put(t, root, ".next", []byte("two"))
	if err := os.Rename(filepath.Join(root, ".next"), filepath.Join(root, "key-plan.jws")); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-said:
		if m.GetName() != "key-plan" || m.GetVersion() == "" || m.GetVersion() == held.GetVersion() {
			t.Fatalf("told %v, want the new label of key-plan", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("not told within five seconds")
	}

	if err := os.Remove(filepath.Join(root, "key-plan.jws")); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-said:
		if m.GetName() != "key-plan" || m.GetVersion() != "" {
			t.Fatalf("told %v, want the empty label", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("not told of the removal within five seconds")
	}
	cancel()
}

func TestWatchOfAnInvalidNameIsRefused(t *testing.T) {
	c := client(t, t.TempDir())
	w, err := c.Watch(context.Background(), connect.NewRequest(&sourcev1alpha1.SourceWatchRequest{Name: "../x"}))
	if err == nil {
		for w.Receive() {
		}
		err = w.Err()
	}
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("code %v (%v), want invalid_argument", connect.CodeOf(err), err)
	}
	if reason, _ := refusal.Reason(err); reason != "schema" {
		t.Fatalf("reason %q, want schema", reason)
	}
}

// What a handler saw of the call.
type seenCall struct {
	id       string
	deadline time.Duration
	has      bool
}

// capture is an interceptor of the host that records the correlation id and the deadline in the context of the handler.
func capture(out chan<- seenCall) connect.HandlerOption {
	return connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			s := seenCall{id: call.CorrelationID(ctx)}
			if d, ok := ctx.Deadline(); ok {
				s.has, s.deadline = true, time.Until(d)
			}
			out <- s
			return next(ctx, req)
		}
	}))
}

// In process the deadline and the correlation id come from the caller's context, and nothing else: there is no socket to carry a peer.
func TestTheDeadlineAndTheCorrelationIDOfTheCallersContextReachTheHandlerInProcess(t *testing.T) {
	root := t.TempDir()
	put(t, root, "boundary.jws", []byte("x"))
	seen := make(chan seenCall, 2)
	c := client(t, root, capture(seen))

	// A call with neither: the handler has no id and no deadline (Go sets no default; a host that wants one wraps its context).
	if _, err := read(c, "boundary", ""); err != nil {
		t.Fatal(err)
	}
	if s := <-seen; s.id != "" || s.has {
		t.Fatalf("a bare call arrived with %+v", s)
	}

	ctx, cancel := context.WithTimeout(call.WithCorrelationID(context.Background(), "req-42"), 5*time.Second)
	defer cancel()
	if _, err := c.Read(ctx, connect.NewRequest(&sourcev1alpha1.ReadRequest{Name: "boundary"})); err != nil {
		t.Fatal(err)
	}
	s := <-seen
	if s.id != "req-42" {
		t.Fatalf("the correlation id = %q, want req-42", s.id)
	}
	if !s.has || s.deadline <= 0 || s.deadline > 5*time.Second {
		t.Fatalf("the deadline left = %v (has %v), want within 5 s", s.deadline, s.has)
	}

	// An id that is not valid is dropped by the client and never reaches the handler.
	bad := call.WithCorrelationID(context.Background(), "not an id")
	if _, err := c.Read(bad, connect.NewRequest(&sourcev1alpha1.ReadRequest{Name: "boundary"})); err != nil {
		t.Fatal(err)
	}
	if s := <-seen; s.id != "" {
		t.Fatalf("an invalid id arrived: %q", s.id)
	}
}

func TestADeadlineThatHasPassedIsDeadlineExceeded(t *testing.T) {
	root := t.TempDir()
	put(t, root, "boundary.jws", []byte("x"))
	c := client(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond)
	_, err := c.Read(ctx, connect.NewRequest(&sourcev1alpha1.ReadRequest{Name: "boundary"}))
	if connect.CodeOf(err) != connect.CodeDeadlineExceeded {
		t.Fatalf("code %v (%v), want deadline_exceeded", connect.CodeOf(err), err)
	}
}

func TestNewNeedsADirectoryAndRunNeedsAHost(t *testing.T) {
	if _, err := file.New(file.Options{}); err == nil {
		t.Error("New without a Dir succeeded")
	}
	if err := file.Run(context.Background(), file.Options{Dir: open(t, t.TempDir())}, file.Host{}); err == nil {
		t.Error("Run without a Serve succeeded")
	}
}
