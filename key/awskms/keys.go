package awskms

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
)

// The algorithms of the key-store port (keystore.proto).
const (
	ES384    = "ES384"     // ECDSA over P-384 with SHA-384 (RFC 7518, section 3.4); KMS ECC_NIST_P384 and ECDSA_SHA_384
	ES256    = "ES256"     // ECDSA over P-256 with SHA-256; KMS ECC_NIST_P256 and ECDSA_SHA_256; the audit-log purpose only
	RS256    = "RS256"     // RSASSA-PKCS1-v1_5 over SHA-256 with RSA-2048; KMS RSA_2048 and RSASSA_PKCS1_V1_5_SHA_256; token-signing-rs256 only
	ECDHP384 = "ECDH-P384" // ECDH over P-384; KMS ECC_NIST_P384 with the key usage KEY_AGREEMENT
)

// purposeAlgorithm maps the key purposes that a key store holds to the algorithm of their key, the same table as the other key drivers
// of this repository (a driver requires no other driver, so each holds its own copy). The purpose pin-release is not here: it is the
// key of a card's PIN release, and a KMS key has no PIN.
var purposeAlgorithm = map[string]string{
	"wrapping":            ECDHP384,
	"token-signing":       ES384,
	"audit-log":           ES256,
	"workload-issuer":     ES384,
	"x509-intermediate":   ES384,
	"ssh-user":            ES384,
	"ssh-host":            ES384,
	"issuance-log":        ES384,
	"release-signing":     ES384,
	"token-signing-rs256": RS256,
}

// Purposes returns the purposes that the driver holds a key for, sorted.
func Purposes() []string {
	out := make([]string, 0, len(purposeAlgorithm))
	for p := range purposeAlgorithm {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

func algorithmFor(purpose string) (string, bool) {
	a, ok := purposeAlgorithm[purpose]
	return a, ok
}

// kmsShape is how an algorithm is a KMS key: its key spec, its key usage, and what Sign needs.
type kmsShape struct {
	spec      types.KeySpec
	usage     types.KeyUsageType
	signing   types.SigningAlgorithmSpec
	digestLen int
}

var shapes = map[string]kmsShape{
	ES384:    {types.KeySpecEccNistP384, types.KeyUsageTypeSignVerify, types.SigningAlgorithmSpecEcdsaSha384, 48},
	ES256:    {types.KeySpecEccNistP256, types.KeyUsageTypeSignVerify, types.SigningAlgorithmSpecEcdsaSha256, 32},
	RS256:    {types.KeySpecRsa2048, types.KeyUsageTypeSignVerify, types.SigningAlgorithmSpecRsassaPkcs1V15Sha256, 32},
	ECDHP384: {types.KeySpecEccNistP384, types.KeyUsageTypeKeyAgreement, "", 0},
}

// Sentinel errors of the key operations; the service maps them to refusals.
var (
	errAbsent    = errors.New("awskms: no key for the purpose")
	errAlgorithm = errors.New("awskms: the key has another algorithm")
	errPeer      = errors.New("awskms: the peer public key is not a P-384 point")
	errState     = errors.New("awskms: the key is not usable")
)

// stateError is a KMS key that exists and cannot be used: disabled, pending deletion or import. The state is the journal's, not the
// caller's.
type stateError struct{ state types.KeyState }

func (e *stateError) Error() string { return "awskms: the key is " + string(e.state) }
func (e *stateError) Unwrap() error { return errState }

// load returns the key of a purpose: from memory when it was read before, else from KMS by its identifier or alias, else (when make is
// true and the host allows it) made. made reports that this call made the key.
func (s *Service) load(ctx context.Context, purpose, algorithm string, create bool) (k *key, made bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if k, ok := s.keys[purpose]; ok {
		if algorithm != "" && k.algorithm != algorithm {
			return nil, false, errAlgorithm
		}
		return k, false, nil
	}
	want, _ := algorithmFor(purpose)
	if algorithm != "" && algorithm != want {
		return nil, false, errAlgorithm
	}
	meta, err := s.describe(ctx, s.reference(purpose))
	fresh := false // this call went through makeKey: KMS may not show a new key everywhere yet
	switch {
	case errors.Is(err, errAbsent) && create && s.create && s.ids[purpose] == "":
		// A purpose that the host names a key for is the deployment's to make: an alias that the driver made would not be the one named.
		meta, made, err = s.makeKey(ctx, purpose, want)
		if err != nil {
			return nil, false, err
		}
		fresh = true
	case err != nil:
		return nil, false, err
	}
	if err := checkShape(meta, want); err != nil {
		return nil, false, err
	}
	var pub *kms.GetPublicKeyOutput
	read := func() (err error) {
		pub, err = s.api.GetPublicKey(ctx, &kms.GetPublicKeyInput{KeyId: meta.Arn})
		return err
	}
	if fresh {
		err = retryNotFound(ctx, read)
	} else {
		err = read()
	}
	if err != nil {
		return nil, false, err
	}
	k = &key{id: aws(meta.Arn), algorithm: want, spki: bytes.Clone(pub.PublicKey)}
	if meta.CreationDate != nil {
		k.createdAt = *meta.CreationDate
	}
	s.keys[purpose] = k
	return k, made, nil
}

// describe reads a KMS key by identifier, ARN or alias. A key that is not there is errAbsent; one that is there and not enabled is a
// *stateError.
func (s *Service) describe(ctx context.Context, ref string) (*types.KeyMetadata, error) {
	out, err := s.api.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: &ref})
	if err != nil {
		var nf *types.NotFoundException
		if errors.As(err, &nf) {
			return nil, errAbsent
		}
		return nil, err
	}
	meta := out.KeyMetadata
	if meta == nil || meta.Arn == nil {
		return nil, errors.New("awskms: DescribeKey answered without key metadata")
	}
	if meta.KeyState != types.KeyStateEnabled {
		return nil, &stateError{state: meta.KeyState}
	}
	return meta, nil
}

// checkShape holds a found key to the algorithm of its purpose: a purpose has one algorithm for its whole life and a key is never
// replaced, so a key of another spec or usage is refused, not used and not recreated. A key that KMS did not make (imported material,
// or a key store of the customer's) is refused too: the key must have been born in KMS.
func checkShape(meta *types.KeyMetadata, algorithm string) error {
	sh := shapes[algorithm]
	if meta.KeySpec != sh.spec || meta.KeyUsage != sh.usage {
		return errAlgorithm
	}
	if meta.Origin != "" && meta.Origin != types.OriginTypeAwsKms && meta.Origin != types.OriginTypeAwsCloudhsm {
		return errAlgorithm
	}
	return nil
}

// makeKey makes the KMS key of a purpose under its alias. The alias is the lock: CreateAlias refuses a name that is taken, so of two
// callers that make a key at once one keeps its key and the other schedules its own for deletion and reads the winner's.
func (s *Service) makeKey(ctx context.Context, purpose, algorithm string) (meta *types.KeyMetadata, made bool, err error) {
	sh := shapes[algorithm]
	desc := fmt.Sprintf("atoom key for the purpose %s (%s)", purpose, algorithm)
	created, err := s.api.CreateKey(ctx, &kms.CreateKeyInput{
		KeySpec:     sh.spec,
		KeyUsage:    sh.usage,
		Description: &desc,
		Tags: []types.Tag{
			{TagKey: ptr(TagPurpose), TagValue: &purpose},
			{TagKey: ptr(TagAlgorithm), TagValue: &algorithm},
			{TagKey: ptr(TagManagedBy), TagValue: ptr(managedBy)},
		},
	})
	if err != nil {
		return nil, false, err
	}
	if created.KeyMetadata == nil || created.KeyMetadata.KeyId == nil {
		return nil, false, errors.New("awskms: CreateKey answered without key metadata")
	}
	id := *created.KeyMetadata.KeyId
	alias := s.Alias(purpose)
	err = retryNotFound(ctx, func() error {
		_, err := s.api.CreateAlias(ctx, &kms.CreateAliasInput{AliasName: &alias, TargetKeyId: &id})
		return err
	})
	if err == nil {
		slog.Info("key made", "purpose", purpose, "algorithm", algorithm, "alias", alias)
		return created.KeyMetadata, true, nil
	}
	// The key that was just made has no alias, so nothing can reach it: it goes whatever happened.
	defer s.discard(ctx, id)
	var exists *types.AlreadyExistsException
	if !errors.As(err, &exists) {
		return nil, false, err
	}
	slog.Info("another caller made the key first; keeping its key", "purpose", purpose, "alias", alias)
	meta, err = s.describe(ctx, alias)
	return meta, false, err
}

// discard schedules a KMS key that the driver made and cannot name for deletion, after the shortest waiting time. A failure is logged and
// does not fail the call: the key costs its monthly price until someone deletes it, and has no alias.
func (s *Service) discard(ctx context.Context, id string) {
	// The caller's deadline may be spent; the deletion is housekeeping.
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	days := int32(pendingWindowDays)
	if _, err := s.api.ScheduleKeyDeletion(dctx, &kms.ScheduleKeyDeletionInput{KeyId: &id, PendingWindowInDays: &days}); err != nil {
		slog.Warn("a key without an alias could not be scheduled for deletion", "key", id, "error", err.Error())
	}
}

// retryNotFound runs f again, a few times and after a short wait, while it answers NotFoundException. The KMS API is eventually
// consistent: a key that CreateKey has just returned may not be visible to the next call for a moment, and the calls that follow a
// creation (CreateAlias, GetPublicKey) are the ones that see it.
func retryNotFound(ctx context.Context, f func() error) error {
	for attempt := 0; ; attempt++ {
		err := f()
		var nf *types.NotFoundException
		if err == nil || !errors.As(err, &nf) || attempt >= 4 {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 50 * time.Millisecond):
		}
	}
}

func ptr[T any](v T) *T { return &v }

func aws(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
