// Package keyid makes the key_id that the key port returns with a signature and with a public key (keystore.proto, SignResponse and
// PublicKeyResponse): the name of the key that signed, so that a caller can publish the right public key (as the kid of a JWS) after a
// rotation. Every id ends with the lower-case hex SHA-256 of the key's SubjectPublicKeyInfo in DER, so a caller can check an id against a
// public key it holds; in front of it a driver puts what places the key in its store. A caller compares ids for equality and treats them as
// opaque otherwise.
package keyid

import (
	"crypto/sha256"
	"encoding/hex"
)

// Fingerprint is the lower-case hex SHA-256 of a SubjectPublicKeyInfo in DER: the whole id of a software key.
func Fingerprint(spki []byte) string {
	sum := sha256.Sum256(spki)
	return hex.EncodeToString(sum[:])
}

// Software is the id of a key held in a file: the fingerprint.
func Software(spki []byte) string { return Fingerprint(spki) }

// Card is the id of a key in a PIV slot: the slot (two lower-case hex digits), a colon and the fingerprint.
func Card(slot string, spki []byte) string { return slot + ":" + Fingerprint(spki) }

// KMS is the id of a KMS key: its ARN, a "#" and the fingerprint. An asymmetric KMS key has no versions and its public key never changes,
// so the ARN names the key and the fingerprint ties the id to the public key that was read.
func KMS(arn string, spki []byte) string { return arn + "#" + Fingerprint(spki) }
