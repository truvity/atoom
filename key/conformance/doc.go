// Package conformance is the conformance kit of the key port: the cases that every key driver must pass, run through the driver's
// ConnectRPC endpoint (docs/archive/extraction-plan.md, section 1). The kit speaks the contract atoom.key.v1alpha1 and nothing else, so it tests a
// driver in any language; the command key-conformance runs it against a socket.
//
// A driver passes the kit when every case that its Capabilities do not exclude succeeds. A case that needs an algorithm or the
// attestation that the driver does not declare is skipped and says so (docs/archive/extraction-plan.md, R4): a driver that does not offer a call is
// not failed for it, and it is not passed for it either. A run in which no case ran fails: a green result that tested nothing is not
// green.
//
// The kit declares its own purposes (Declaration): conformance-es384, conformance-es256, conformance-rs256, conformance-agree,
// conformance-made-only and the PIN release key's name pin-release. The host starts the driver under test with that declaration, and a
// case fails when the driver does not serve the purposes it needs. No name in it is one that a driver ever held in a list. The cases make
// the keys of the purposes they use when those are absent, so a run belongs on a state of its own, not on an installation's.
package conformance
