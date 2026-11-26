// Package appconfig is the AWS AppConfig driver of the source port of atoom, atoom.source.v1alpha1.SourceService, in Go: the config source
// contract over the configurations that AppConfig deploys. The Go module is github.com/truvity/atoom/source/appconfig. A Go program, a
// Lambda function for one, links it and mounts its handler in its own process; the thin main (cmd/appconfig) serves it on a UNIX socket.
//
//   - A document is asked for by a kebab-case name (site, policy-bundle). The name is the identifier of a configuration profile (its name or
//     id) in the application and environment the host gives, unless [Options.Profiles] maps it to another identifier. The driver returns the
//     bytes that AppConfig deployed and never parses them; the signature is the caller's to check.
//   - The version label is the SHA-384 of the bytes, in hex, as in the file driver: the same bytes give the same label, whichever deployment
//     brought them. AppConfig's own version label is optional and set by people, so it is not used.
//   - It reads through the AppConfig data plane: StartConfigurationSession once for each name, then GetLatestConfiguration with the token
//     of the last answer, never sooner than AppConfig's poll interval. Between two polls a read is answered from what the last poll brought.
//   - Capabilities says POLL: every read says in poll_after when a new poll will be made. Watch is unimplemented.
//
// It is a library and a thin main. The library is the Connect handler and the logic behind it: [New] takes an AppConfig data client
// ([API]; *appconfigdata.Client is one) and returns a [Service] that a program mounts in-process (docs/in-process.md of socket), on a server
// of its own ([Service.Handler]), or on a server that the host provides ([Run]).
package appconfig

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/appconfigdata"
	"github.com/aws/smithy-go"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/truvity/atoom/common/refusal"
	sourcev1alpha1 "github.com/truvity/atoom/source/gen/atoom/source/v1alpha1"
	"github.com/truvity/atoom/source/gen/atoom/source/v1alpha1/sourcev1alpha1connect"
)

const (
	// DocumentMax is the size over which a document is refused, the same as the file driver's. AppConfig's own store holds at most 2 MB.
	DocumentMax = 4 * 1024 * 1024
	// LeastPollInterval is the lower bound AppConfig puts on RequiredMinimumPollIntervalInSeconds.
	LeastPollInterval = 15 * time.Second
	// MostPollInterval is the upper bound AppConfig puts on RequiredMinimumPollIntervalInSeconds.
	MostPollInterval = 24 * time.Hour
	// pollAfterFloor is the least poll_after a read says: never zero, so that a caller never spins.
	pollAfterFloor = time.Second
)

// The reasons a read is refused. [Service] turns each into a Connect code and a Refusal reason.
var (
	// ErrName is a name that is not a kebab-case name.
	ErrName = errors.New("appconfig: the name is not a kebab-case name")
	// ErrAbsent is a name with no configuration profile, or a profile with nothing deployed in the environment.
	ErrAbsent = errors.New("appconfig: no such configuration")
	// ErrBound is a configuration over [DocumentMax].
	ErrBound = errors.New("appconfig: the configuration is over the limit")
	// ErrEngine is a failure of AppConfig; the error it wraps is for the operator's log.
	ErrEngine = errors.New("appconfig: AppConfig failed")
)

// API is the part of the AppConfig data client the driver calls; *appconfigdata.Client is one.
type API interface {
	StartConfigurationSession(ctx context.Context, in *appconfigdata.StartConfigurationSessionInput, opts ...func(*appconfigdata.Options)) (*appconfigdata.StartConfigurationSessionOutput, error)
	GetLatestConfiguration(ctx context.Context, in *appconfigdata.GetLatestConfigurationInput, opts ...func(*appconfigdata.Options)) (*appconfigdata.GetLatestConfigurationOutput, error)
}

// Options are what the driver needs.
type Options struct {
	// AppConfig is the AppConfig data client. Required.
	AppConfig API
	// Application is the identifier (name or id) of the AppConfig application. Required.
	Application string
	// Environment is the identifier (name or id) of the AppConfig environment within the application: an AppConfig resource that the
	// host names, nothing the driver knows the meaning of. Required.
	Environment string
	// Profiles maps a document name to the identifier of its configuration profile, for a profile whose name is not the document's name
	// (an id, or a name that is not kebab-case). A name not in the map is its own profile identifier. Optional.
	Profiles map[string]string
	// PollInterval is the RequiredMinimumPollIntervalInSeconds of every session, between [LeastPollInterval] and [MostPollInterval]; zero leaves it to
	// AppConfig (60 seconds). AppConfig may answer a longer interval, and the driver follows the answer.
	PollInterval time.Duration
	// Now is the clock; nil is time.Now. Tests set it.
	Now func() time.Time
	// Logger receives every line the driver writes. nil means slog.Default(), read at each line; the host decides where the lines go,
	// or that they go nowhere.
	Logger *slog.Logger
}

// Host is what a program that runs the driver provides.
type Host struct {
	// Serve serves h at path on the host's server until ctx ends. For a socket it is a closure around socket.Serve.
	Serve func(ctx context.Context, path string, h http.Handler) error
	// HandlerOptions are the Connect options of the handler: the read limit, the interceptors of the host.
	HandlerOptions []connect.HandlerOption
}

// Document is a document as the last poll brought it.
type Document struct {
	// Data is the bytes AppConfig deployed.
	Data []byte
	// Version is the SHA-384 of Data, in lower-case hex.
	Version string
	// PollAfter is how long until the driver polls AppConfig again for this name.
	PollAfter time.Duration
}

// Service is the config source of one AppConfig application and environment: the SourceService.
type Service struct {
	sourcev1alpha1connect.UnimplementedSourceServiceHandler
	api      API
	app, env string
	profiles map[string]string
	interval *int32 // RequiredMinimumPollIntervalInSeconds, nil = AppConfig's default
	now      func() time.Time
	logger   *slog.Logger

	mu    sync.Mutex
	names map[string]*entry
}

// entry is the state of one name: its session and what the last poll brought. Its lock is held across a poll, so that a token is used once.
type entry struct {
	mu      sync.Mutex
	token   string // the token of the next GetLatestConfiguration; empty = start a session
	data    []byte
	version string
	have    bool
	next    time.Time // the time of the next poll
}

// reset drops the session and what it brought; the lock is the caller's.
func (e *entry) reset() {
	e.token, e.data, e.version, e.have, e.next = "", nil, "", false, time.Time{}
}

var _ sourcev1alpha1connect.SourceServiceHandler = (*Service)(nil)

// ErrOptions is an [Options] that cannot make a driver.
var ErrOptions = errors.New("appconfig: the options are incomplete")

// New makes the service of the application and environment in opts. It makes no call: the first read of a name starts its session.
func New(opts Options) (*Service, error) {
	if opts.AppConfig == nil || opts.Application == "" || opts.Environment == "" {
		return nil, errors.Join(ErrOptions, errors.New("appconfig: Options.AppConfig, Options.Application and Options.Environment are required"))
	}
	var poll *int32
	if opts.PollInterval != 0 {
		if opts.PollInterval < LeastPollInterval || opts.PollInterval > MostPollInterval || opts.PollInterval%time.Second != 0 {
			return nil, errors.Join(ErrOptions, errors.New("appconfig: Options.PollInterval is whole seconds between 15 s and 24 h"))
		}
		poll = aws.Int32(int32(opts.PollInterval / time.Second))
	}
	profiles := make(map[string]string, len(opts.Profiles))
	for name, id := range opts.Profiles {
		if !ValidName(name) || id == "" {
			return nil, errors.Join(ErrOptions, errors.New("appconfig: Options.Profiles maps kebab-case names to non-empty identifiers"))
		}
		profiles[name] = id
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{api: opts.AppConfig, app: opts.Application, env: opts.Environment, profiles: profiles, interval: poll, now: now, logger: opts.Logger, names: map[string]*entry{}}, nil
}

// ValidName reports whether name is a kebab-case name: 1 to 63 characters of lower-case letters, digits and "-", starting with a letter
// (the rule of the file driver, so that one name reads the same document from either).
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

func (s *Service) entry(name string) *entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.names[name]
	if !ok {
		e = &entry{}
		s.names[name] = e
	}
	return e
}

func (s *Service) profile(name string) string {
	if id, ok := s.profiles[name]; ok {
		return id
	}
	return name
}

// Document returns the document of the name: from a poll of AppConfig when one is due, and from the last poll otherwise.
//
// It returns [ErrName] for a name that is not kebab-case, [ErrAbsent] for a name with no profile or nothing deployed, [ErrBound] for a
// document over [DocumentMax] and an error that wraps [ErrEngine] for a failure of AppConfig. After any error the name's session is
// dropped, so that the next read starts a new one and gets the whole document again.
func (s *Service) Document(ctx context.Context, name string) (Document, error) {
	if !ValidName(name) {
		return Document{}, ErrName
	}
	e := s.entry(name)
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.have || !s.now().Before(e.next) {
		if err := s.poll(ctx, name, e); err != nil {
			e.reset()
			return Document{}, err
		}
	}
	after := e.next.Sub(s.now())
	if after < pollAfterFloor {
		after = pollAfterFloor
	}
	return Document{Data: e.data, Version: e.version, PollAfter: after}, nil
}

// poll asks AppConfig for the latest configuration of the name, starting a session first when there is none, and once more with a new
// session when the token held is no longer good (it lives 24 hours).
func (s *Service) poll(ctx context.Context, name string, e *entry) error {
	fresh := e.token == ""
	for {
		if e.token == "" {
			out, err := s.api.StartConfigurationSession(ctx, &appconfigdata.StartConfigurationSessionInput{
				ApplicationIdentifier:                aws.String(s.app),
				EnvironmentIdentifier:                aws.String(s.env),
				ConfigurationProfileIdentifier:       aws.String(s.profile(name)),
				RequiredMinimumPollIntervalInSeconds: s.interval,
			})
			if err != nil {
				return classify(err)
			}
			e.token = aws.ToString(out.InitialConfigurationToken)
			e.have = false
		}
		out, err := s.api.GetLatestConfiguration(ctx, &appconfigdata.GetLatestConfigurationInput{ConfigurationToken: aws.String(e.token)})
		if err != nil {
			// A token that is no longer good is BadRequestException; one new session is tried, never more.
			if code(err) == "BadRequestException" && !fresh {
				e.token, fresh = "", true
				continue
			}
			return classify(err)
		}
		e.token = aws.ToString(out.NextPollConfigurationToken)
		if e.token == "" {
			return errors.Join(ErrEngine, errors.New("appconfig: GetLatestConfiguration gave no next token"))
		}
		interval := time.Duration(out.NextPollIntervalInSeconds) * time.Second
		if interval < pollAfterFloor {
			interval = pollAfterFloor
		}
		e.next = s.now().Add(interval)
		switch {
		case len(out.Configuration) > DocumentMax:
			return ErrBound
		case len(out.Configuration) > 0:
			e.data = append([]byte(nil), out.Configuration...)
			e.version = label(e.data)
			e.have = true
		case !e.have:
			// The first answer of a session is the whole document; an empty one is a profile with nothing deployed.
			return ErrAbsent
		}
		return nil
	}
}

// code is the error code of an AWS error, or "".
func code(err error) string {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return ae.ErrorCode()
	}
	return ""
}

// classify turns an error of the AppConfig client into the driver's own.
func classify(err error) error {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case code(err) == "ResourceNotFoundException":
		return ErrAbsent
	default:
		return errors.Join(ErrEngine, err)
	}
}

// label is the version label: the SHA-384 of the bytes, in lower-case hex.
func label(data []byte) string {
	sum := sha512.Sum384(data)
	return hex.EncodeToString(sum[:])
}

// Handler returns the Connect handler of the source service, to mount on a server or an in-memory transport.
func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return sourcev1alpha1connect.NewSourceServiceHandler(s, opts...)
}

// Run serves the source port on the host's server until ctx ends.
func Run(ctx context.Context, opts Options, host Host) error {
	if host.Serve == nil {
		return errors.New("appconfig: the host has no Serve")
	}
	svc, err := New(opts)
	if err != nil {
		return err
	}
	path, h := svc.Handler(host.HandlerOptions...)
	return host.Serve(ctx, path, h)
}

// log is the logger of the driver: the host's Options.Logger, or slog.Default() read now when the host gave none.
func (s *Service) log() *slog.Logger {
	if s.logger != nil {
		return s.logger
	}
	return slog.Default()
}

// refuse is the Connect error of an error of a read; the field at fault is the request's name.
func (s *Service) refuse(err error) error {
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
		// The message is the reason only, never AppConfig's text: that is for the operator's log.
		s.log().Error("AppConfig failed", "err", err)
		return refusal.New(connect.CodeUnavailable, "engine", "")
	}
}

// Capabilities says that the source is polled.
func (s *Service) Capabilities(_ context.Context, _ *connect.Request[sourcev1alpha1.CapabilitiesRequest]) (*connect.Response[sourcev1alpha1.CapabilitiesResponse], error) {
	return connect.NewResponse(&sourcev1alpha1.CapabilitiesResponse{Watch: sourcev1alpha1.Watch_POLL}), nil
}

// Read returns the bytes of a document and its label, or nothing but the label when the caller holds it already,
// and in poll_after when to ask again.
func (s *Service) Read(ctx context.Context, req *connect.Request[sourcev1alpha1.ReadRequest]) (*connect.Response[sourcev1alpha1.ReadResponse], error) {
	doc, err := s.Document(ctx, req.Msg.GetName())
	if err != nil {
		return nil, s.refuse(err)
	}
	res := &sourcev1alpha1.ReadResponse{Version: doc.Version, PollAfter: durationpb.New(doc.PollAfter)}
	if held := req.Msg.GetIfNoneMatch(); held != "" && held == doc.Version {
		res.Unchanged = true
	} else {
		res.Data = doc.Data
	}
	return connect.NewResponse(res), nil
}

// Watch is unimplemented: a polled source says in every read when to ask again.
func (s *Service) Watch(_ context.Context, _ *connect.Request[sourcev1alpha1.SourceWatchRequest], _ *connect.ServerStream[sourcev1alpha1.SourceWatchResponse]) error {
	return refusal.New(connect.CodeUnimplemented, "rule", "name")
}
