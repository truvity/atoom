// Package conformance is the conformance kit of the key port: the cases that every key driver must pass, run through the driver's
// ConnectRPC endpoint (docs/archive/extraction-plan.md, section 1). The kit speaks the contract atoom.key.v1alpha1 and nothing else, so it tests a
// driver in any language; the command key-conformance runs it against a socket.
//
// A driver passes the kit when every case that its Capabilities do not exclude succeeds. A case that needs an algorithm or the
// attestation that the driver does not declare is skipped and says so (docs/archive/extraction-plan.md, R4): a driver that does not offer a call is
// not failed for it, and it is not passed for it either. A run in which no case ran fails: a green result that tested nothing is not
// green.
//
// The cases use the reference purposes of the contract: token-signing (ES384), wrapping (ECDH-P384), audit-log (ES256),
// token-signing-rs256 (RS256) and pin-release. They make the keys of the purposes they use when those are absent, so a run belongs on
// a state of its own, not on an installation's.
package conformance
