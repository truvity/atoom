// Package awskms is the AWS KMS key driver of atoom: the key port, atoom.key.v1alpha1, served over asymmetric keys that live in AWS
// KMS and never leave it. The Go module is github.com/truvity/atoom/key/awskms.
//
// It is a library and a thin main, like every driver of this repository. The library is the Connect handler and the logic behind
// it: New takes a KMS client and returns a Service that a program mounts in-process (a Lambda function links it in and calls it on an
// in-memory transport; there are no sidecars there), on a server of its own (Handler), or on a server that the host provides (Run).
// The main (cmd/awskms) serves the library on a UNIX socket and nothing more.
//
// What it maps (docs/layout.md has the full table). A purpose is a name that the host declares (Options.Purposes: its algorithm ES256,
// ES384, RS256 or ECDH-P384, and the operations it allows) and a KMS key found by its alias, alias/<prefix>/<purpose>, or by an
// identifier that the host names for it (Options.KeyIDs); the key carries the tags atoom:purpose and atoom:algorithm. Ensure finds the
// key, or makes it when Options.CreateKeys allows, and returns its public key. Sign is Sign with a digest. PublicKey is GetPublicKey.
// Ecdh is DeriveSharedSecret on a KEY_AGREEMENT key. A key is never replaced, never imported and never exported.
//
// What it does not offer, and says so. KMS cannot attest where a key was born, so Attest and VerifyAttestation answer unimplemented and
// Capabilities says attests false. There is no PIN, so the three calls of the PIN release answer unimplemented as well, and a purpose
// named pin-release is only a name here. The contract has no call that makes a data key, so GenerateDataKey is not used (docs/layout.md,
// "What is not here").
package awskms

import (
	"context"
	"crypto/ecdh"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	keyv1alpha1connect "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1/keyv1alpha1connect"
	"github.com/truvity/atoom/key/purpose"
)

// DefaultAliasPrefix is the prefix of the aliases of the keys that the driver finds and makes: alias/atoom/<purpose>.
const DefaultAliasPrefix = "atoom"

// The tags of a key that the driver makes. The alias finds a key; the tags say what the key is for and who made it, so that a person
// reading the KMS console, or a policy that conditions on a tag, can tell.
const (
	TagPurpose   = "atoom:purpose"
	TagAlgorithm = "atoom:algorithm"
	TagManagedBy = "atoom:managed-by"
	managedBy    = "atoom-key-awskms"
)

// pendingWindowDays is the shortest waiting time that KMS allows before it deletes a key. A key that the driver made and lost the
// race for its alias is scheduled for deletion with it.
const pendingWindowDays = 7

// aliasPrefixPattern is what an alias prefix may hold: KMS allows letters, digits, "/", "_" and "-" in an alias name, and refuses the
// names that begin alias/aws/.
var aliasPrefixPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+(/[a-zA-Z0-9_-]+)*$`)

// API is the part of the KMS client that the driver calls. *kms.Client satisfies it; a test passes a client of its own.
type API interface {
	CreateKey(ctx context.Context, in *kms.CreateKeyInput, opts ...func(*kms.Options)) (*kms.CreateKeyOutput, error)
	CreateAlias(ctx context.Context, in *kms.CreateAliasInput, opts ...func(*kms.Options)) (*kms.CreateAliasOutput, error)
	DescribeKey(ctx context.Context, in *kms.DescribeKeyInput, opts ...func(*kms.Options)) (*kms.DescribeKeyOutput, error)
	GetPublicKey(ctx context.Context, in *kms.GetPublicKeyInput, opts ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error)
	Sign(ctx context.Context, in *kms.SignInput, opts ...func(*kms.Options)) (*kms.SignOutput, error)
	DeriveSharedSecret(ctx context.Context, in *kms.DeriveSharedSecretInput, opts ...func(*kms.Options)) (*kms.DeriveSharedSecretOutput, error)
	ScheduleKeyDeletion(ctx context.Context, in *kms.ScheduleKeyDeletionInput, opts ...func(*kms.Options)) (*kms.ScheduleKeyDeletionOutput, error)
}

var _ API = (*kms.Client)(nil)

// ErrAliasPrefix is an alias prefix that KMS would refuse.
var ErrAliasPrefix = errors.New("awskms: the alias prefix may hold letters, digits, '_', '-' and '/' between them, and may not begin with aws/")

// Options are what the driver needs.
type Options struct {
	// KMS is the client of the KMS service: its region, credentials and endpoint are the host's. Required.
	KMS API
	// Purposes are the purposes the host declares: the only names the driver finds or makes a key for, each with its algorithm and its
	// operations (key/purpose). Required. The field Slot of a declaration is read by the card driver only. Each purpose is a KMS key
	// of the spec and usage of its algorithm; a key of another spec is refused, never replaced.
	Purposes []purpose.Declaration
	// AliasPrefix is the prefix of the aliases of the purposes' keys: alias/<AliasPrefix>/<purpose>. Empty is DefaultAliasPrefix. One
	// AWS account and region can hold several installations, each under a prefix of its own.
	AliasPrefix string
	// KeyIDs names the KMS key of a purpose by key ID, key ARN or alias name, and wins over the alias that the prefix gives. A host
	// whose keys are made by its deployment code (Terraform, CloudFormation) names them here; sluis names the ARN of each key in its
	// signed key plan. Only a declared purpose may be named. A purpose that is not listed is found by its alias. The key is read once and its identifier is then kept, so an
	// alias that is moved afterwards does not move the driver; a rotation under an alias takes a restart.
	KeyIDs map[string]string
	// CreateKeys lets Ensure make the key of a purpose that has none: CreateKey, CreateAlias and the tags. The default is off, so that a
	// role that holds only Sign and GetPublicKey is enough and the key policy stays the deployment's. With it off, Ensure of a purpose
	// whose key is absent answers not_found (absent).
	CreateKeys bool
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

// Service implements the key-store port over AWS KMS.
type Service struct {
	keyv1alpha1connect.UnimplementedKeyStoreServiceHandler
	api      API
	purposes *purpose.Set
	prefix   string
	ids      map[string]string
	create   bool
	caller   func(context.Context) string

	mu   sync.Mutex
	keys map[string]*key // by purpose; a key's identifier, public key and time never change
}

// key is a KMS key that the driver has found or made, as it was read once.
type key struct {
	id        string // the ARN, so that no alias is looked up again
	algorithm string
	spki      []byte
	createdAt time.Time
}

// New returns the service. It calls nothing: the first call that needs a key looks it up.
func New(opts Options) (*Service, error) {
	if opts.KMS == nil {
		return nil, errors.New("awskms: Options.KMS is required")
	}
	set, err := purpose.New(opts.Purposes)
	if err != nil {
		return nil, err
	}
	prefix := opts.AliasPrefix
	if prefix == "" {
		prefix = DefaultAliasPrefix
	}
	if !aliasPrefixPattern.MatchString(prefix) || len(prefix) > 200 || prefix == "aws" || strings.HasPrefix(prefix, "aws/") {
		return nil, ErrAliasPrefix
	}
	ids := make(map[string]string, len(opts.KeyIDs))
	for name, id := range opts.KeyIDs {
		if _, ok := set.Lookup(name); !ok {
			return nil, fmt.Errorf("awskms: Options.KeyIDs names %q, which the host did not declare", name)
		}
		if id == "" {
			return nil, fmt.Errorf("awskms: Options.KeyIDs gives the purpose %q an empty key", name)
		}
		ids[name] = id
	}
	return &Service{api: opts.KMS, purposes: set, prefix: prefix, ids: ids, create: opts.CreateKeys, caller: opts.Caller, keys: map[string]*key{}}, nil
}

// Handler returns the Connect handler of the key-store service, to mount on a server or an in-memory transport.
func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return keyv1alpha1connect.NewKeyStoreServiceHandler(s, opts...)
}

// Run serves the key-store port on the host's server until ctx ends.
func Run(ctx context.Context, opts Options, host Host) error {
	if host.Serve == nil {
		return errors.New("awskms: the host has no Serve")
	}
	svc, err := New(opts)
	if err != nil {
		return err
	}
	slog.Info("aws kms key driver starting", "alias_prefix", svc.prefix, "create_keys", svc.create, "purposes", svc.purposes.Names())
	path, h := svc.Handler(host.HandlerOptions...)
	return host.Serve(ctx, path, h)
}

// Alias returns the alias name of the key of a purpose under the driver's prefix, as alias/<prefix>/<purpose>.
func (s *Service) Alias(purpose string) string {
	return "alias/" + s.prefix + "/" + purpose
}

// reference is what the driver names the key of a purpose by when it asks KMS: the host's identifier, or the alias.
func (s *Service) reference(purpose string) string {
	if id, ok := s.ids[purpose]; ok {
		return id
	}
	return s.Alias(purpose)
}

// peerSPKI is the SubjectPublicKeyInfo of a SEC1 uncompressed P-384 point, which is what KMS takes for DeriveSharedSecret. A point that
// is not on the curve is an error (ecdh checks it).
func peerSPKI(sec1 []byte) ([]byte, error) {
	pub, err := ecdh.P384().NewPublicKey(sec1)
	if err != nil {
		return nil, errPeer
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, errPeer
	}
	return der, nil
}
