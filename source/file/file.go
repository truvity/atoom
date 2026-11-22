// Package file is the file driver of the source port of atoom, atoom.source.v1alpha1.SourceService, in Go: the config source contract
// over a directory of signed config documents. The Go module is github.com/truvity/atoom/source/file, and it is the same driver as the
// Rust crate atoom-source-file that sits in the same directory (docs/file.md): the same names, the same label, the same refusals and the
// same Watch, so that a Go consumer reads its documents in its own process and a Rust one through a sidecar, and the two agree.
//
//   - A document is asked for by the kebab-case name of its kind (boundary, upstream) and read from <name>.jws in the directory. The driver
//     returns the bytes and never parses them; the signature is the caller's to check.
//   - The version label is the SHA-384 of the file, in hex, so the same bytes give the same label whichever way they arrived.
//   - A path is opened below the directory with os.Root. A symlink that stays below it is followed, because a mounted ConfigMap is a tree
//     of them and its update is the swap of one of them; one that leaves it, an absolute one included, is refused.
//   - A file is opened without blocking, so that a FIFO in the directory cannot hang the open, and anything that is not a regular file is
//     refused.
//   - Watch looks at the file again every [WatchEvery] and says when the label changes.
//
// It is a library and a thin main. The library is the Connect handler and the logic behind it: [New] takes an open [Dir] and returns a
// [Service] that a program mounts in-process (docs/in-process.md: the handler on an in-memory transport, no socket), on a server of its own
// ([Service.Handler]), or on a server that the host provides ([Run]). The main (cmd/file) serves the library on a UNIX socket and nothing
// more.
package file

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/common/refusal"
	sourcev1alpha1 "github.com/truvity/atoom/source/gen/atoom/source/v1alpha1"
	"github.com/truvity/atoom/source/gen/atoom/source/v1alpha1/sourcev1alpha1connect"
)

const (
	// Extension is the extension of a document.
	Extension = ".jws"
	// DocumentMax is the size over which a document is refused: the payload is a third larger in base64url than the file, and the
	// largest config service limit known is 2 MiB.
	DocumentMax = 4 * 1024 * 1024
	// WatchEvery is how often a watcher looks at its file again.
	WatchEvery = 200 * time.Millisecond
)

// The reasons a read is refused. [Service] turns each into a Connect code and a Refusal reason.
var (
	// ErrName is a name that is not a kind's kebab-case name.
	ErrName = errors.New("file: the name is not a kebab-case name")
	// ErrAbsent is a document that is not there.
	ErrAbsent = errors.New("file: no such document")
	// ErrBound is a document over [DocumentMax], one that is not a regular file, or one reached through a link the driver does not follow.
	ErrBound = errors.New("file: the document is over the limit, is not a regular file, or is behind a link that leaves the directory")
)

// Document is a document read from the directory.
type Document struct {
	// Data is the bytes of the file, as the file holds them.
	Data []byte
	// Version is the SHA-384 of Data, in lower-case hex.
	Version string
}

// Dir is an open directory of documents. It is safe for concurrent use.
type Dir struct {
	root *os.Root
}

// Open opens dir, which must be a directory.
func Open(dir string) (*Dir, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &Dir{root: root}, nil
}

// Close closes the directory. A reader that is running is not interrupted by it, and a read after it fails.
func (d *Dir) Close() error { return d.root.Close() }

// Read reads the document of the kind with this kebab-case name.
//
// It returns [ErrName] for a name that is not kebab-case, [ErrAbsent] when there is no such file, [ErrBound] for a file that is too large,
// is not a regular file or is reached through a link that leaves the directory, and the error of the filesystem otherwise.
func (d *Dir) Read(name string) (Document, error) {
	if !ValidName(name) {
		return Document{}, ErrName
	}
	// O_NONBLOCK: the open of a FIFO returns at once, and the regular-file check below refuses it.
	f, err := d.root.OpenFile(name+Extension, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return Document{}, classify(err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return Document{}, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > DocumentMax {
		return Document{}, ErrBound
	}
	// One byte over the limit, so a file that grew since Stat is caught too.
	data, err := io.ReadAll(io.LimitReader(f, DocumentMax+1))
	if err != nil {
		return Document{}, err
	}
	if len(data) > DocumentMax {
		return Document{}, ErrBound
	}
	return Document{Data: data, Version: label(data)}, nil
}

// Version returns the version label of the document now, and false when there is none (an absent document).
//
// It fails as [Dir.Read] does, except that an absent document is not an error.
func (d *Dir) Version(name string) (string, bool, error) {
	doc, err := d.Read(name)
	switch {
	case err == nil:
		return doc.Version, true, nil
	case errors.Is(err, ErrAbsent):
		return "", false, nil
	default:
		return "", false, err
	}
}

// classify turns the error of an open into the driver's own.
func classify(err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return ErrAbsent
	// ELOOP, EXDEV and the like: a link that goes where it must not, or too many of them. os.Root says an escape with an error that it
	// does not export, so the words are the only way to tell (a test pins them).
	case errors.Is(err, syscall.ELOOP), errors.Is(err, syscall.EXDEV), strings.Contains(err.Error(), "path escapes from parent"):
		return ErrBound
	default:
		return err
	}
}

// ValidName reports whether name is a kind's kebab-case name: 1 to 63 characters of lower-case letters, digits and "-", starting with a
// letter. It cannot hold a "/" or a ".", so it cannot leave the directory.
func ValidName(name string) bool {
	if name == "" || len(name) > 63 || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for i := 1; i < len(name); i++ {
		c := name[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// label is the version label: the SHA-384 of the bytes, in lower-case hex.
func label(data []byte) string {
	sum := sha512.Sum384(data)
	return hex.EncodeToString(sum[:])
}

// Options are what the driver needs.
type Options struct {
	// Dir is the open directory. Required.
	Dir *Dir
	// WatchEvery is how often Watch looks at a document again; zero is [WatchEvery].
	WatchEvery time.Duration
}

// Host is what a program that runs the driver provides.
type Host struct {
	// Serve serves h at path on the host's server until ctx ends. For a socket it is a closure around socket.Serve.
	Serve func(ctx context.Context, path string, h http.Handler) error
	// HandlerOptions are the Connect options of the handler: the read limit, the interceptors of the host.
	HandlerOptions []connect.HandlerOption
}

// Service is the config source of one directory: the SourceService.
type Service struct {
	sourcev1alpha1connect.UnimplementedSourceServiceHandler
	dir   *Dir
	every time.Duration
}

var _ sourcev1alpha1connect.SourceServiceHandler = (*Service)(nil)

// New makes the service of the directory in opts.
func New(opts Options) (*Service, error) {
	if opts.Dir == nil {
		return nil, errors.New("file: Options.Dir is required")
	}
	every := opts.WatchEvery
	if every <= 0 {
		every = WatchEvery
	}
	return &Service{dir: opts.Dir, every: every}, nil
}

// Handler returns the Connect handler of the source service, to mount on a server or an in-memory transport.
func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	path, h := sourcev1alpha1connect.NewSourceServiceHandler(s, opts...)
	// Watch has to send the headers of its response before it has anything to say, so that the call of a watcher returns and the stream is
	// open (silence is then an open stream, as it is on the Rust driver). Connect sends the headers with the first message, so the handler
	// is given the response writer to flush with.
	return path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), writerKey{}, w)))
	})
}

type writerKey struct{}

// openStream sends the headers of the response of a stream now, when the handler is served over HTTP (it always is, but a test may call
// Watch directly).
func openStream(ctx context.Context) {
	if w, ok := ctx.Value(writerKey{}).(http.ResponseWriter); ok {
		_ = http.NewResponseController(w).Flush()
	}
}

// Run serves the source port on the host's server until ctx ends.
func Run(ctx context.Context, opts Options, host Host) error {
	if host.Serve == nil {
		return errors.New("file: the host has no Serve")
	}
	svc, err := New(opts)
	if err != nil {
		return err
	}
	path, h := svc.Handler(host.HandlerOptions...)
	return host.Serve(ctx, path, h)
}

// refuse is the Connect error of an error of the directory; the field at fault is the request's name.
func refuse(err error) error {
	switch {
	case errors.Is(err, ErrName):
		return refusal.New(connect.CodeInvalidArgument, "schema", "name")
	case errors.Is(err, ErrAbsent):
		return refusal.New(connect.CodeNotFound, "absent", "name")
	case errors.Is(err, ErrBound):
		return refusal.New(connect.CodeFailedPrecondition, "bound", "name")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	default:
		// The message is the reason only, never the text of the filesystem: that is for the operator's log.
		slog.Error("config directory failed", "err", err)
		return refusal.New(connect.CodeUnavailable, "engine", "")
	}
}

// Capabilities says that Watch notifies.
func (s *Service) Capabilities(_ context.Context, _ *connect.Request[sourcev1alpha1.CapabilitiesRequest]) (*connect.Response[sourcev1alpha1.CapabilitiesResponse], error) {
	return connect.NewResponse(&sourcev1alpha1.CapabilitiesResponse{Watch: sourcev1alpha1.Watch_NOTIFY}), nil
}

// Read returns the bytes of a document and its label, or nothing but the label when the caller holds it already.
func (s *Service) Read(_ context.Context, req *connect.Request[sourcev1alpha1.ReadRequest]) (*connect.Response[sourcev1alpha1.ReadResponse], error) {
	doc, err := s.dir.Read(req.Msg.GetName())
	if err != nil {
		return nil, refuse(err)
	}
	if held := req.Msg.GetIfNoneMatch(); held != "" && held == doc.Version {
		return connect.NewResponse(&sourcev1alpha1.ReadResponse{Unchanged: true, Version: doc.Version}), nil
	}
	return connect.NewResponse(&sourcev1alpha1.ReadResponse{Data: doc.Data, Version: doc.Version}), nil
}

// Watch says when the label of a document changes. The first look sets what the watcher holds without telling it: a watcher asks for the
// version it holds with Read, and Watch says what changed after it began. An absent document is told as the empty label. A failing look ends
// the stream with its error, and the watcher dials again.
func (s *Service) Watch(ctx context.Context, req *connect.Request[sourcev1alpha1.SourceWatchRequest], stream *connect.ServerStream[sourcev1alpha1.SourceWatchResponse]) error {
	name := req.Msg.GetName()
	if !ValidName(name) {
		return refuse(ErrName)
	}
	seen, held, err := s.dir.Version(name)
	if err != nil {
		return refuse(err)
	}
	openStream(ctx)
	tick := time.NewTicker(s.every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
		now, there, err := s.dir.Version(name)
		if err != nil {
			return refuse(err)
		}
		if now == seen && there == held {
			continue
		}
		seen, held = now, there
		if err := stream.Send(&sourcev1alpha1.SourceWatchResponse{Name: name, Version: now}); err != nil {
			return err
		}
	}
}
