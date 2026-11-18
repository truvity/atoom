package yubikeypiv

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"errors"
	"log/slog"

	"github.com/truvity/atoom/key/pinrelease"
)

// The driver does not verify the key plan of a PIN release: the host does, through PeerKeys (sluis verifies its signed KeyPlan, the
// publisher's signature, the kind and the seq, and reads the slot map from the bytes it verified). The driver keeps what is the
// card's: the checks of the other side's signature, and of the boundary and the node, with the driver's own crypto.
//
// What the driver does not check: revocation, and that the document is the newest one. Both are the host's, through PeerKeys, which
// sees the store and the sequence of the documents.

// peerKey asks the host for the pin-release key of a peer node, and refuses the node of this card and an empty one whatever the host
// answers. The error is ErrUnlisted for a node that is not a listed peer, and another error for a document that is refused.
func (s *Service) peerKey(ctx context.Context, keyPlan []byte, node string) (*ecdsa.PublicKey, error) {
	if s.peers == nil {
		return nil, errNoPeers
	}
	pub, err := s.peers.PeerKey(ctx, keyPlan, node)
	if err != nil {
		return nil, err
	}
	if node == "" || node == s.node || pub == nil || pub.Curve != elliptic.P384() {
		return nil, ErrUnlisted
	}
	return pub, nil
}

var errNoPeers = errors.New("the driver has no way to check a key plan")

// verifySigned checks an ES384 signature, ASN.1 DER, over SHA-384 of a text (RFC 7518, section 3.4).
func verifySigned(pub *ecdsa.PublicKey, text, sig []byte) bool {
	return len(sig) > 0 && ecdsa.VerifyASN1(pub, pinrelease.Digest(text), sig)
}

// logPeerRefusal writes the journal line of a refused peer: its word, and never a value.
func logPeerRefusal(method string, err error, node string) {
	if errors.Is(err, ErrUnlisted) {
		slog.Warn("a PIN release refused: not a listed peer", "call", method, "node", node)
		return
	}
	slog.Warn("a PIN release refused: the KeyPlan", "call", method, "why", err.Error())
}
