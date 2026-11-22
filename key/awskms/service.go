package awskms

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/smithy-go"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/truvity/atoom/common/refusal"
	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
	"github.com/truvity/atoom/key/purpose"
)

// The reasons of the refusals of the port (a closed list; docs/key-port.md of the key port).
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

// Capabilities says what the store can do: the four algorithms, the purposes the host declared, and no attestation (KMS cannot say where
// a key was born).
func (s *Service) Capabilities(context.Context, *connect.Request[keyv1alpha1.CapabilitiesRequest]) (*connect.Response[keyv1alpha1.CapabilitiesResponse], error) {
	return connect.NewResponse(&keyv1alpha1.CapabilitiesResponse{
		Store: "aws-kms", Attests: false, Algorithms: []string{ES384, ES256, RS256, ECDHP384}, Purposes: s.purposes.Infos(),
	}), nil
}

// Ensure finds the KMS key of a purpose, or makes it when the host allows, and returns its public key.
func (s *Service) Ensure(ctx context.Context, req *connect.Request[keyv1alpha1.EnsureRequest]) (*connect.Response[keyv1alpha1.EnsureResponse], error) {
	name, algorithm := req.Msg.GetPurpose(), req.Msg.GetAlgorithm()
	want, ok := s.algorithmFor(name)
	if !ok {
		return nil, s.refuse(ctx, "Ensure", connect.CodeInvalidArgument, reasonPurpose, "purpose")
	}
	if _, ok := shapes[algorithm]; !ok {
		return nil, s.refuse(ctx, "Ensure", connect.CodeInvalidArgument, reasonAlgorithm, "algorithm")
	}
	if algorithm != want {
		// An existing key has another algorithm, or the purpose is not for this one: the key is never replaced.
		return nil, s.refuse(ctx, "Ensure", connect.CodeFailedPrecondition, reasonAlgorithm, "algorithm")
	}
	k, made, err := s.load(ctx, name, algorithm, true)
	if err != nil {
		return nil, s.failed(ctx, "Ensure", err)
	}
	res := &keyv1alpha1.EnsureResponse{SpkiDer: bytes.Clone(k.spki), Created: made}
	if !k.createdAt.IsZero() {
		res.CreatedAt = timestamppb.New(k.createdAt)
	}
	return connect.NewResponse(res), nil
}

// Sign signs a digest with the key of a purpose, in place: KMS Sign with the message type DIGEST. An ES256 or ES384 signature is DER
// (key/signature converts it to the raw r||s of a JWS), as KMS returns it. An RS256 signature is the
// RSASSA-PKCS1-v1_5 octet string in signature_der, which has no DER form (keystore.proto), and that is what KMS returns.
func (s *Service) Sign(ctx context.Context, req *connect.Request[keyv1alpha1.SignRequest]) (*connect.Response[keyv1alpha1.SignResponse], error) {
	name := req.Msg.GetPurpose()
	decl, ok := s.purposes.Lookup(name)
	if !ok {
		return nil, s.refuse(ctx, "Sign", connect.CodeInvalidArgument, reasonPurpose, "purpose")
	}
	want := decl.Algorithm
	// The algorithm must be the purpose's own: ES256 and RS256 sign SHA-256, ES384 SHA-384. The key-agreement purpose signs nothing,
	// whatever the algorithm asked.
	if a := req.Msg.GetAlgorithm(); (a != ES384 && a != ES256 && a != RS256) || (want != ECDHP384 && a != want) {
		return nil, s.refuse(ctx, "Sign", connect.CodeInvalidArgument, reasonAlgorithm, "algorithm")
	}
	if want == ECDHP384 {
		return nil, s.refuse(ctx, "Sign", connect.CodeFailedPrecondition, reasonAlgorithm, "purpose")
	}
	// A purpose declared without the operation is not one this host lets sign.
	if !decl.Allows(purpose.Sign) {
		return nil, s.refuse(ctx, "Sign", connect.CodeInvalidArgument, reasonPurpose, "purpose")
	}
	sh := shapes[want]
	if len(req.Msg.GetDigest()) != sh.digestLen {
		return nil, s.refuse(ctx, "Sign", connect.CodeInvalidArgument, reasonDigest, "digest")
	}
	k, _, err := s.load(ctx, name, want, false)
	if err != nil {
		return nil, s.failed(ctx, "Sign", err)
	}
	out, err := s.api.Sign(ctx, &kms.SignInput{
		KeyId:            &k.id,
		Message:          req.Msg.GetDigest(),
		MessageType:      "DIGEST",
		SigningAlgorithm: sh.signing,
	})
	if err != nil {
		return nil, s.failed(ctx, "Sign", err)
	}
	return connect.NewResponse(&keyv1alpha1.SignResponse{SignatureDer: out.Signature}), nil
}

// PublicKey returns the public key of a purpose. It is read from KMS once, the first time the key is used, and kept: the public half of a
// KMS key never changes.
func (s *Service) PublicKey(ctx context.Context, req *connect.Request[keyv1alpha1.PublicKeyRequest]) (*connect.Response[keyv1alpha1.PublicKeyResponse], error) {
	name := req.Msg.GetPurpose()
	if _, ok := s.algorithmFor(name); !ok {
		return nil, s.refuse(ctx, "PublicKey", connect.CodeInvalidArgument, reasonPurpose, "purpose")
	}
	k, _, err := s.load(ctx, name, "", false)
	if err != nil {
		return nil, s.failed(ctx, "PublicKey", err)
	}
	return connect.NewResponse(&keyv1alpha1.PublicKeyResponse{SpkiDer: bytes.Clone(k.spki)}), nil
}

// Ecdh derives the shared secret of the key-agreement key with a peer: KMS DeriveSharedSecret, which answers the raw secret (the x
// coordinate, with no key-derivation function) that the contract names. The peer's point is checked here, before KMS, so that a point
// off the curve is the caller's refusal and not a KMS error.
func (s *Service) Ecdh(ctx context.Context, req *connect.Request[keyv1alpha1.EcdhRequest]) (*connect.Response[keyv1alpha1.EcdhResponse], error) {
	name := req.Msg.GetPurpose()
	decl, ok := s.purposes.Lookup(name)
	if !ok {
		return nil, s.refuse(ctx, "Ecdh", connect.CodeInvalidArgument, reasonPurpose, "purpose")
	}
	want := decl.Algorithm
	if want != ECDHP384 {
		return nil, s.refuse(ctx, "Ecdh", connect.CodeFailedPrecondition, reasonAlgorithm, "purpose")
	}
	// A purpose declared without the operation is not one this host lets agree.
	if !decl.Allows(purpose.Agree) {
		return nil, s.refuse(ctx, "Ecdh", connect.CodeInvalidArgument, reasonPurpose, "purpose")
	}
	spki, err := peerSPKI(req.Msg.GetPeerPublic())
	if err != nil {
		return nil, s.failed(ctx, "Ecdh", err)
	}
	k, _, err := s.load(ctx, name, want, false)
	if err != nil {
		return nil, s.failed(ctx, "Ecdh", err)
	}
	out, err := s.api.DeriveSharedSecret(ctx, &kms.DeriveSharedSecretInput{
		KeyId:                 &k.id,
		KeyAgreementAlgorithm: "ECDH",
		PublicKey:             spki,
	})
	if err != nil {
		return nil, s.failed(ctx, "Ecdh", err)
	}
	return connect.NewResponse(&keyv1alpha1.EcdhResponse{SharedSecret: out.SharedSecret}), nil
}

// failed maps an error to its refusal. What KMS said goes to the journal and not to the caller: a refusal names a reason and never a value.
func (s *Service) failed(ctx context.Context, method string, err error) *connect.Error {
	var state *stateError
	switch {
	case errors.Is(err, errAbsent):
		return s.refuse(ctx, method, connect.CodeNotFound, reasonAbsent, "purpose")
	case errors.Is(err, errAlgorithm):
		return s.refuse(ctx, method, connect.CodeFailedPrecondition, reasonAlgorithm, "purpose")
	case errors.Is(err, errPeer):
		return s.refuse(ctx, method, connect.CodeInvalidArgument, reasonPeer, "peer_public")
	case errors.As(err, &state):
		slog.Error("the KMS key cannot be used", "method", method, "state", string(state.state))
		return refusal.New(connect.CodeFailedPrecondition, reasonStore, "")
	case errors.Is(err, context.Canceled):
		return refusal.New(connect.CodeCanceled, "canceled", "")
	case errors.Is(err, context.DeadlineExceeded):
		return refusal.New(connect.CodeDeadlineExceeded, reasonStore, "")
	}
	code := connect.CodeInternal
	var api smithy.APIError
	var netErr net.Error
	switch {
	case errors.As(err, &api):
		code = codeOfAPIError(api.ErrorCode())
	case errors.As(err, &netErr):
		code = connect.CodeUnavailable
	}
	slog.Error("KMS failed", "method", method, "code", code.String(), "error", err.Error())
	return refusal.New(code, reasonStore, "")
}

// codeOfAPIError maps the error code of a KMS answer to the Connect code of the refusal. The ones that a retry may cure are unavailable; the
// ones that name a key that cannot be used now (disabled, in a state that refuses the call, with its key store down) are
// failed_precondition; the rest, an access denial included, are internal: the journal has the code, the caller has the word store.
func codeOfAPIError(code string) connect.Code {
	switch code {
	case "ThrottlingException", "LimitExceededException", "RequestLimitExceeded", "DependencyTimeoutException", "KMSInternalException",
		"InternalFailure", "ServiceUnavailable", "ServiceUnavailableException", "RequestTimeout", "RequestTimeoutException":
		return connect.CodeUnavailable
	case "DisabledException", "KMSInvalidStateException", "KeyUnavailableException", "InvalidKeyUsageException", "IncorrectKeyException",
		"InvalidGrantTokenException", "XksKeyNotFoundException", "CustomKeyStoreInvalidStateException":
		return connect.CodeFailedPrecondition
	default:
		return connect.CodeInternal
	}
}

// callerOf names the caller for the journal, or "" when the host names none.
func (s *Service) callerOf(ctx context.Context) string {
	if s.caller == nil {
		return ""
	}
	return s.caller(ctx)
}
