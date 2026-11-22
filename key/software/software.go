// Package software is the software key driver of atoom: the key port, atoom.key.v1alpha1, served over keys held in files (sluis
// ADR 0006, "Floors in code"; ADR 0011, "The local installation"). It holds a key for every purpose its host declares, ES256, ES384,
// RS256 or ECDH-P384 as the declaration says (key/purpose), and for no other name. The Go module is github.com/truvity/atoom/key/software.
//
// It is a library and a thin main. The library is the Connect handler and the logic behind it: New opens a state directory and returns
// a Service that a program mounts in-process, on an in-memory transport or on a server of its own (Handler), and Run does the
// same on a server that the host provides (Host). The main (cmd/software) serves the library on a UNIX socket and nothing more: the
// peer check, the registry owner and the command line of a larger system are the host's, and stay in that system.
//
// It exists for the local installation, for CI, for a smoke installation without cards and for a consumer that keeps a key in a
// file. It has no audit emitter: a driver is private to its owner, and the caller audits what it asks for. Whether it may run in
// production is the host's choice (Options.RefuseProduction): sluis turns the floor on, atoom's own main leaves it off.
package software

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/truvity/atoom/common/refusal"
	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
	"github.com/truvity/atoom/key/gen/atoom/key/v1alpha1/keyv1alpha1connect"
	"github.com/truvity/atoom/key/purpose"
	"github.com/truvity/atoom/key/software/internal/keystore"
)

// The environments of an installation.
const (
	Development = "development"
	Staging     = "staging"
	Production  = "production"
)

// RefusedInProduction is what the driver says when the floor refuses production.
const RefusedInProduction = "software key driver refused in production"

// The errors of the floor.
var (
	// ErrProduction is a start in production by a host that turned the floor on.
	ErrProduction = errors.New(RefusedInProduction)
	// ErrEnvironment is an environment that is not development, staging or production.
	ErrEnvironment = errors.New("the environment must be development, staging or production")
)

// Problems is a state directory or a key file that the driver refuses. Each problem is a path and a word, never a byte of a key.
type Problems = keystore.ProblemsError

// Floor is the floor in code (sluis ADR 0006): when refuseProduction is true the driver does not run in production. It returns
// ErrProduction for production and ErrEnvironment for a value that is not an environment; an empty environment is accepted (it names
// nothing).
func Floor(environment string, refuseProduction bool) error {
	switch environment {
	case "", Development, Staging:
		return nil
	case Production:
		if refuseProduction {
			return ErrProduction
		}
		return nil
	default:
		return ErrEnvironment
	}
}

// Options are what the driver needs.
type Options struct {
	// StateDir is the directory of the key files, mode 0700 and owned by the driver's user.
	StateDir string
	// Purposes are the purposes the host declares: the only names the driver holds a key for, each with its algorithm and its
	// operations (key/purpose). Required. A key file in the state directory of a purpose that is not declared, or of another algorithm
	// than the declared one, refuses the start. The field Slot of a declaration is read by the card driver only.
	Purposes []purpose.Declaration
	// Environment names the installation: development, staging or production. Empty names nothing. Staging logs a warning at the start.
	Environment string
	// RefuseProduction makes New and Run refuse the environment production, before anything is opened (sluis's floor). The default
	// is off: a consumer that keeps a key in a file in production may.
	RefuseProduction bool
	// Caller names the caller of a call for the journal line of a refusal; none when nil. The host knows its callers.
	Caller func(context.Context) string
}

// Host is what a program that runs the driver provides.
type Host struct {
	// Serve serves the handler of the key-store service on the host's own listener until ctx ends, and returns nil when it ends so.
	Serve func(ctx context.Context, path string, h http.Handler) error
	// HandlerOptions are the Connect options of the handler: the read limit, the interceptors of the host.
	HandlerOptions []connect.HandlerOption
}

// New applies the floor, opens the state directory and returns the service. In production with RefuseProduction nothing is opened and
// no key is read. A refused state directory is a *Problems error.
func New(opts Options) (*Service, error) {
	if err := Floor(opts.Environment, opts.RefuseProduction); err != nil {
		return nil, err
	}
	set, err := purpose.New(opts.Purposes)
	if err != nil {
		return nil, err
	}
	if opts.Environment == Staging {
		slog.Warn("software keys in staging: anyone who can read the volume has the keys", "environment", Staging)
	}
	st, err := keystore.Open(opts.StateDir, set)
	if err != nil {
		return nil, err
	}
	return &Service{store: st, purposes: set, caller: opts.Caller}, nil
}

// Handler returns the Connect handler of the key-store service, to mount on a server or an in-memory transport.
func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return keyv1alpha1connect.NewKeyStoreServiceHandler(s, opts...)
}

// Present returns the purposes that have a key, for the start-up log.
func (s *Service) Present() []string { return s.store.Present() }

// Run applies the floor, opens the state directory and serves the key-store port on the host's server until ctx ends. The floor
// comes first: in production, with RefuseProduction, nothing is opened and no key is read.
func Run(ctx context.Context, opts Options, host Host) error {
	if host.Serve == nil {
		return errors.New("software: the host has no Serve")
	}
	svc, err := New(opts)
	if err != nil {
		return err
	}
	slog.Info("software key driver starting", "environment", opts.Environment, "purposes", svc.Present())
	path, h := svc.Handler(host.HandlerOptions...)
	return host.Serve(ctx, path, h)
}

// Check verifies the state directory and the keys in it against the host's purposes. It reads, and writes nothing. A problem is a
// *Problems error; purposes that are not a declaration are a *purpose.Error.
func Check(dir string, purposes []purpose.Declaration) error {
	set, err := purpose.New(purposes)
	if err != nil {
		return err
	}
	if _, problems := keystore.Check(dir, set); len(problems) > 0 {
		return &Problems{Problems: problems}
	}
	return nil
}

// Service implements the key-store port over a keystore.Store.
type Service struct {
	keyv1alpha1connect.UnimplementedKeyStoreServiceHandler
	store    *keystore.Store
	purposes *purpose.Set
	caller   func(context.Context) string
}

// The reasons of the refusals of the port (a closed list).
const (
	reasonPurpose   = "purpose"
	reasonAlgorithm = "algorithm"
	reasonDigest    = "digest"
	reasonPeer      = "peer_public"
	reasonAbsent    = "absent"
	reasonStore     = "store"
)

// refuse logs a refusal, with the method and the reason and no value, and returns it.
func (s *Service) refuse(ctx context.Context, method string, code connect.Code, reason, field string) *connect.Error {
	slog.Warn("refused", "method", method, "code", code.String(), "reason", reason, "field", field, "caller", s.callerOf(ctx))
	return refusal.New(code, reason, field)
}

// Capabilities says what the store can do.
func (s *Service) Capabilities(context.Context, *connect.Request[keyv1alpha1.CapabilitiesRequest]) (*connect.Response[keyv1alpha1.CapabilitiesResponse], error) {
	return connect.NewResponse(&keyv1alpha1.CapabilitiesResponse{
		Store: "software", Attests: false, Algorithms: []string{keystore.ES384, keystore.ES256, keystore.RS256, keystore.ECDHP384},
		Purposes: s.purposes.Infos(),
	}), nil
}

// Ensure makes the key of a purpose when it is absent and returns its public key.
func (s *Service) Ensure(ctx context.Context, req *connect.Request[keyv1alpha1.EnsureRequest]) (*connect.Response[keyv1alpha1.EnsureResponse], error) {
	name, algorithm := req.Msg.GetPurpose(), req.Msg.GetAlgorithm()
	decl, ok := s.purposes.Lookup(name)
	if !ok {
		return nil, s.refuse(ctx, "Ensure", connect.CodeInvalidArgument, reasonPurpose, "purpose")
	}
	if algorithm != keystore.ES384 && algorithm != keystore.ES256 && algorithm != keystore.RS256 && algorithm != keystore.ECDHP384 {
		return nil, s.refuse(ctx, "Ensure", connect.CodeInvalidArgument, reasonAlgorithm, "algorithm")
	}
	if algorithm != decl.Algorithm {
		// An existing key has another algorithm, or the purpose is not for this one: the key is never replaced.
		return nil, s.refuse(ctx, "Ensure", connect.CodeFailedPrecondition, reasonAlgorithm, "algorithm")
	}
	spki, created, err := s.store.Ensure(name, algorithm)
	if err != nil {
		return nil, s.failed(ctx, "Ensure", err)
	}
	if created {
		slog.Info("key made", "purpose", name, "algorithm", algorithm)
	}
	res := &keyv1alpha1.EnsureResponse{SpkiDer: spki, Created: created}
	if at, err := s.store.CreatedAt(name); err == nil {
		res.CreatedAt = timestamppb.New(at)
	}
	return connect.NewResponse(res), nil
}

// Sign signs a digest with the key of a purpose, in place. An ES256 or ES384 signature is DER (key/signature converts it to the raw
// r||s of a JWS); an RS256 signature is the RSASSA-PKCS1-v1_5 octet string in signature_der, which has no DER form (keystore.proto).
func (s *Service) Sign(ctx context.Context, req *connect.Request[keyv1alpha1.SignRequest]) (*connect.Response[keyv1alpha1.SignResponse], error) {
	name := req.Msg.GetPurpose()
	decl, ok := s.purposes.Lookup(name)
	if !ok {
		return nil, s.refuse(ctx, "Sign", connect.CodeInvalidArgument, reasonPurpose, "purpose")
	}
	// The algorithm must be the purpose's own: ES256 and RS256 sign SHA-256, ES384 SHA-384. The key-agreement purpose signs nothing.
	if a := req.Msg.GetAlgorithm(); (a != keystore.ES384 && a != keystore.ES256 && a != keystore.RS256) || (decl.Algorithm != keystore.ECDHP384 && a != decl.Algorithm) {
		return nil, s.refuse(ctx, "Sign", connect.CodeInvalidArgument, reasonAlgorithm, "algorithm")
	}
	// A purpose declared without the operation is not one this host lets sign.
	if decl.Algorithm != keystore.ECDHP384 && !decl.Allows(purpose.Sign) {
		return nil, s.refuse(ctx, "Sign", connect.CodeInvalidArgument, reasonPurpose, "purpose")
	}
	sig, err := s.store.Sign(name, req.Msg.GetDigest())
	if err != nil {
		return nil, s.failed(ctx, "Sign", err)
	}
	return connect.NewResponse(&keyv1alpha1.SignResponse{SignatureDer: sig}), nil
}

// PublicKey returns the public key of a purpose.
func (s *Service) PublicKey(ctx context.Context, req *connect.Request[keyv1alpha1.PublicKeyRequest]) (*connect.Response[keyv1alpha1.PublicKeyResponse], error) {
	name := req.Msg.GetPurpose()
	if _, ok := s.purposes.Lookup(name); !ok {
		return nil, s.refuse(ctx, "PublicKey", connect.CodeInvalidArgument, reasonPurpose, "purpose")
	}
	spki, err := s.store.PublicKey(name)
	if err != nil {
		return nil, s.failed(ctx, "PublicKey", err)
	}
	return connect.NewResponse(&keyv1alpha1.PublicKeyResponse{SpkiDer: spki}), nil
}

// Ecdh derives the shared secret of the key-agreement key with a peer.
func (s *Service) Ecdh(ctx context.Context, req *connect.Request[keyv1alpha1.EcdhRequest]) (*connect.Response[keyv1alpha1.EcdhResponse], error) {
	name := req.Msg.GetPurpose()
	decl, ok := s.purposes.Lookup(name)
	if !ok {
		return nil, s.refuse(ctx, "Ecdh", connect.CodeInvalidArgument, reasonPurpose, "purpose")
	}
	if decl.Algorithm == keystore.ECDHP384 && !decl.Allows(purpose.Agree) {
		return nil, s.refuse(ctx, "Ecdh", connect.CodeInvalidArgument, reasonPurpose, "purpose")
	}
	secret, err := s.store.Ecdh(name, req.Msg.GetPeerPublic())
	if err != nil {
		return nil, s.failed(ctx, "Ecdh", err)
	}
	return connect.NewResponse(&keyv1alpha1.EcdhResponse{SharedSecret: secret}), nil
}

// failed maps an error of the store to its refusal.
func (s *Service) failed(ctx context.Context, method string, err error) *connect.Error {
	switch {
	case errors.Is(err, keystore.ErrAbsent):
		return s.refuse(ctx, method, connect.CodeNotFound, reasonAbsent, "purpose")
	case errors.Is(err, keystore.ErrAlgorithm):
		return s.refuse(ctx, method, connect.CodeFailedPrecondition, reasonAlgorithm, "purpose")
	case errors.Is(err, keystore.ErrDigest):
		return s.refuse(ctx, method, connect.CodeInvalidArgument, reasonDigest, "digest")
	case errors.Is(err, keystore.ErrPeer):
		return s.refuse(ctx, method, connect.CodeInvalidArgument, reasonPeer, "peer_public")
	}
	slog.Error("store failed", "method", method, "error", err.Error())
	return refusal.New(connect.CodeInternal, reasonStore, "")
}

// callerOf names the caller for the journal, or "" when the host names none.
func (s *Service) callerOf(ctx context.Context) string {
	if s.caller == nil {
		return ""
	}
	return s.caller(ctx)
}
