package socket

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/common/refusal"
)

// ReasonCaller is the reason of the Refusal of a caller the guard does not admit.
const ReasonCaller = "caller"

type allowKind uint8

const (
	sameUID allowKind = iota // the zero value
	listedUIDs
	anyUID
	hostRule
)

// Allow says who may call a socket. The file mode of the socket is the first wall and this is the second: a process that can open the
// socket is still refused (permission_denied, reason "caller") unless its uid is admitted. The zero value admits the uid this process
// runs as, which is what the default mode 0600 allowed already, except that root is refused too.
type Allow struct {
	kind  allowKind
	uids  []uint32
	admit func(p Peer, procedure string) (refuse string)
}

// AllowSameUID admits the uid this process runs as, and no other.
func AllowSameUID() Allow { return Allow{} }

// AllowUIDs admits exactly these uids. The uid of this process is not implied: list it to admit it. No uid at all admits nobody.
func AllowUIDs(uids ...uint32) Allow { return Allow{kind: listedUIDs, uids: slices.Clone(uids)} }

// AllowAnyUID admits every uid that can open the socket: the file permission is the only guard.
func AllowAnyUID() Allow { return Allow{kind: anyUID} }

// AllowFunc admits the callers that the host's own rule admits: for every call, admit is asked with the peer the kernel reported and
// the procedure (the path the caller sent: what was asked, not who asked), before the message is read. It returns "" to admit the
// call, or the reason of the refusal: the call is then answered permission_denied with a Refusal of that reason. A call whose peer
// could not be read is refused with the reason "caller" without asking it. admit runs on the goroutine of the request: it decides
// from what it is handed, and does not block. A nil admit admits nobody.
func AllowFunc(admit func(p Peer, procedure string) (refuse string)) Allow {
	if admit == nil {
		return AllowUIDs()
	}
	return Allow{kind: hostRule, admit: admit}
}

// Admits reports whether a caller with this uid is admitted by a process that runs as own. A rule of [AllowFunc] is asked with a peer
// that carries only the uid and an empty procedure; the server asks it with the whole peer.
func (a Allow) Admits(uid, own uint32) bool {
	return a.decide(Peer{UID: uid}, own, "") == ""
}

// decide returns "" when the call is admitted, and the reason of its refusal otherwise.
func (a Allow) decide(p Peer, own uint32, procedure string) string {
	var ok bool
	switch a.kind {
	case listedUIDs:
		ok = slices.Contains(a.uids, p.UID)
	case anyUID:
		ok = true
	case hostRule:
		if reason := a.admit(p, procedure); reason != "" {
			return reason
		}
		ok = true
	default:
		ok = p.UID == own
	}
	if ok {
		return ""
	}
	return ReasonCaller
}

// Refused is a call the guard refused, as Config.OnRefused is told of it: the reason, the peer the kernel reported (Known is false
// when the credentials could not be read) and the procedure, which is whatever the caller sent and not a name the host can trust.
// Never a byte of the message.
type Refused struct {
	Reason    string
	Peer      Peer
	Known     bool
	Procedure string
}

// guard is the check of one server: who is the caller, and is it admitted.
type guard struct {
	allow     Allow
	own       uint32
	onRefused func(Refused)
}

// check returns nil when the caller of ctx may call procedure, and the refusal otherwise. It looks at the transport only: ctx carries
// the credentials the server read from the socket.
func (g guard) check(ctx context.Context, procedure string) *connect.Error {
	if g.allow.kind == anyUID {
		return nil
	}
	p, ok := PeerFromContext(ctx)
	reason := ReasonCaller
	if ok {
		reason = g.allow.decide(p, g.own, procedure)
		if reason == "" {
			return nil
		}
	}
	// The path is what the caller sent, so it is not logged.
	slog.Info("call refused", "reason", reason, "known", ok, "uid", p.UID, "pid", p.PID)
	if g.onRefused != nil {
		g.onRefused(Refused{Reason: reason, Peer: p, Known: ok, Procedure: procedure})
	}
	return refusal.New(connect.CodePermissionDenied, reason, "")
}

// denyHandlers answer a refused call without decoding it. A Connect handler decodes a unary message before any interceptor runs, so
// the server checks the caller first, at the HTTP layer, and hands a refused call to a handler whose codec decodes nothing and whose
// body is empty, so that the answer has the right shape for the protocol (Connect unary, Connect stream, gRPC) and no byte of the
// request is parsed.
type denyHandlers struct {
	unary, stream http.Handler
}

type denyKey struct{}

// discardCodec decodes nothing and encodes nothing.
type discardCodec struct{ name string }

func (c discardCodec) Name() string              { return c.name }
func (discardCodec) Marshal(any) ([]byte, error) { return nil, nil }
func (discardCodec) Unmarshal([]byte, any) error { return nil }

func newDenyHandlers() denyHandlers {
	failure := func(ctx context.Context) error {
		err, _ := ctx.Value(denyKey{}).(*connect.Error)
		if err == nil {
			err = refusal.New(connect.CodePermissionDenied, ReasonCaller, "")
		}
		return err
	}
	opts := []connect.HandlerOption{
		connect.WithCodec(discardCodec{"proto"}),
		connect.WithCodec(discardCodec{"json"}),
	}
	unary := connect.NewUnaryHandler("/deny", func(ctx context.Context, _ *connect.Request[struct{}]) (*connect.Response[struct{}], error) {
		return nil, failure(ctx)
	}, opts...)
	stream := connect.NewBidiStreamHandler("/deny", func(ctx context.Context, _ *connect.BidiStream[struct{}, struct{}]) error {
		return failure(ctx)
	}, opts...)
	return denyHandlers{unary: unary, stream: stream}
}

// serve answers r with the refusal err. A Connect unary request is the only shape that needs the unary handler; the streaming and
// gRPC shapes are answered by the stream handler, which never reads the body.
func (d denyHandlers) serve(w http.ResponseWriter, r *http.Request, err *connect.Error) {
	r = r.WithContext(context.WithValue(r.Context(), denyKey{}, err))
	ct := strings.ToLower(r.Header.Get("Content-Type"))
	if strings.HasPrefix(ct, "application/connect+") || strings.HasPrefix(ct, "application/grpc") {
		d.stream.ServeHTTP(w, r)
		return
	}
	r.Body = http.NoBody
	d.unary.ServeHTTP(w, r)
}

// middleware refuses a call before its handler, and so before its message, is reached.
func (g guard) middleware(d denyHandlers, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := g.check(r.Context(), r.URL.Path); err != nil {
			d.serve(w, r, err)
			return
		}
		next.ServeHTTP(w, r)
	})
}
