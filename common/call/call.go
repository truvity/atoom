// Package call is the deadline and the correlation id of a call, handed down to the calls it makes (docs/call-context.md in the
// repository root).
//
// The deadline needs little: Connect sends the deadline of a client's context as the Connect-Timeout-Ms header and a Connect handler
// makes it the deadline of its context, so a handler that calls on with its own context passes on what is left. A callee never extends
// its caller's deadline, because a context can only be narrowed. The correlation id needs this package: [Inherit] reads it from the
// header into the context of the handler, [WithCorrelationID] and [CorrelationID] set and read it, and [ClientOptions] puts it on every
// call of a generated client. The Rust twin is the module call of the crate atoom-socket.
package call

import (
	"context"
	"net/http"
	"time"

	"connectrpc.com/connect"
)

// HeaderCorrelation is the header that carries the correlation id: one id for a request and every call it causes.
const HeaderCorrelation = "Sluis-Correlation-Id"

// idMax is the longest correlation id.
const idMax = 64

type correlationKey struct{}

// ValidCorrelationID reports whether id is 1 to 64 characters of letters, digits, "-" and "_". Anything else is dropped and never
// echoed or passed on, so a hostile header cannot reach a log line.
func ValidCorrelationID(id string) bool {
	if id == "" || len(id) > idMax {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// WithCorrelationID returns ctx carrying the correlation id. An id that [ValidCorrelationID] refuses is dropped, and ctx is returned
// as it was.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	if !ValidCorrelationID(id) {
		return ctx
	}
	return context.WithValue(ctx, correlationKey{}, id)
}

// CorrelationID returns the correlation id of ctx, or "" when there is none.
func CorrelationID(ctx context.Context) string {
	id, _ := ctx.Value(correlationKey{}).(string)
	return id
}

// Inherit wraps a handler so that the correlation id of the request header, when it is valid, is the correlation id of the context the
// handler runs with. socket.Serve does it for every server it serves; a host that mounts a handler in its own server (a Lambda
// function, a test) wraps it itself. The deadline needs nothing here: the Connect handler reads Connect-Timeout-Ms itself.
func Inherit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := r.Header.Get(HeaderCorrelation); ValidCorrelationID(id) {
			r = r.WithContext(WithCorrelationID(r.Context(), id))
		}
		next.ServeHTTP(w, r)
	})
}

// ClientOptions returns the options of a generated New<Service>Client that put the correlation id of the context on every call, unary
// and streaming:
//
//	c := storev1alpha1connect.NewItemServiceClient(hc, url, call.ClientOptions()...)
//
// The deadline of the context goes out without any option, as Connect-Timeout-Ms.
func ClientOptions() []connect.ClientOption {
	return []connect.ClientOption{connect.WithInterceptors(Propagate())}
}

// Propagate is the interceptor that [ClientOptions] installs.
func Propagate() connect.Interceptor { return propagate{} }

type propagate struct{}

func (propagate) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if id := CorrelationID(ctx); id != "" {
			req.Header().Set(HeaderCorrelation, id)
		}
		return next(ctx, req)
	}
}

func (propagate) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		if id := CorrelationID(ctx); id != "" {
			conn.RequestHeader().Set(HeaderCorrelation, id)
		}
		return conn
	}
}

func (propagate) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// DefaultDeadline is an interceptor that gives a call that has no deadline the deadline d; a call that has one keeps it, so a default
// never extends a caller's deadline. On a client it covers unary calls (the life of a stream is its caller's); on a handler it
// covers unary and streaming methods. A zero or negative d gives nothing. A host that wants every call of a client to carry a
// deadline adds it beside [ClientOptions]:
//
//	c := storev1alpha1connect.NewItemServiceClient(hc, url, append(call.ClientOptions(), connect.WithInterceptors(call.DefaultDeadline(30*time.Second)))...)
func DefaultDeadline(d time.Duration) connect.Interceptor { return defaultDeadline{d} }

type defaultDeadline struct{ d time.Duration }

func (dd defaultDeadline) apply(ctx context.Context) (context.Context, context.CancelFunc) {
	if dd.d <= 0 {
		return ctx, func() {}
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, dd.d)
}

func (dd defaultDeadline) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx, cancel := dd.apply(ctx)
		defer cancel()
		return next(ctx, req)
	}
}

func (defaultDeadline) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (dd defaultDeadline) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx, cancel := dd.apply(ctx)
		defer cancel()
		return next(ctx, conn)
	}
}
