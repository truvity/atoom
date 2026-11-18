package software

import (
	"context"

	"connectrpc.com/connect"

	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
)

// The PIN release calls of the port (ADR 0006, "Touch and PIN"), as a store without a card answers them. A software
// store has no PIN and is never locked, so it has nothing to release and nothing to be released to; and it attests
// nothing (Capabilities says attests false). key@ reads these refusals as "this node takes no part in the PIN
// release": its slot map entry has no attestation, and a peer refuses a request that names it.
const (
	reasonUnlocked      = "unlocked"
	reasonNoPIN         = "no_pin"
	reasonNoAttestation = "no_attestation"
)

// Attest refuses: a software key has no attestation.
func (s *Service) Attest(ctx context.Context, _ *connect.Request[keyv1alpha1.AttestRequest]) (*connect.Response[keyv1alpha1.AttestResponse], error) {
	return nil, s.refuse(ctx, "Attest", connect.CodeFailedPrecondition, reasonNoAttestation, "")
}

// VerifyAttestation refuses: the software store holds no attestation anchors.
func (s *Service) VerifyAttestation(ctx context.Context, _ *connect.Request[keyv1alpha1.VerifyAttestationRequest]) (*connect.Response[keyv1alpha1.VerifyAttestationResponse], error) {
	return nil, s.refuse(ctx, "VerifyAttestation", connect.CodeFailedPrecondition, reasonNoAttestation, "")
}

// BeginPinRelease refuses as an unlocked store does: there is no PIN to wait for.
func (s *Service) BeginPinRelease(ctx context.Context, _ *connect.Request[keyv1alpha1.BeginPinReleaseRequest]) (*connect.Response[keyv1alpha1.BeginPinReleaseResponse], error) {
	return nil, s.refuse(ctx, "BeginPinRelease", connect.CodeFailedPrecondition, reasonUnlocked, "")
}

// SealPin refuses: there is no PIN to seal.
func (s *Service) SealPin(ctx context.Context, _ *connect.Request[keyv1alpha1.SealPinRequest]) (*connect.Response[keyv1alpha1.SealPinResponse], error) {
	return nil, s.refuse(ctx, "SealPin", connect.CodeFailedPrecondition, reasonNoPIN, "")
}

// FinishPinRelease refuses as an unlocked store does.
func (s *Service) FinishPinRelease(ctx context.Context, _ *connect.Request[keyv1alpha1.FinishPinReleaseRequest]) (*connect.Response[keyv1alpha1.FinishPinReleaseResponse], error) {
	return nil, s.refuse(ctx, "FinishPinRelease", connect.CodeFailedPrecondition, reasonUnlocked, "")
}
