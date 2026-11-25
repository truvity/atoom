// Package token is the token block of atoom (docs/token-block.md, record 0020): compact JWS (RFC 7515) of a host's claims, signed by
// purpose through the seal block, the key set (RFC 7517) of the current key of each purpose and of the public keys the host hands in, and
// the verification of a token against those keys.
//
// The block is built from the seal block alone, and holds no key: seal hashes what the block hands it and the key port signs the digest.
// Its host passes in the client of seal, declares the purposes (the types each signs and the rule of its kid), supplies the rules, and
// serves Block.Handler, in its process (socket.InProcess) or on a socket (socket.Serve, or the thin main cmd/token). The claims are the
// host's: the block checks that they are one strict JSON object and signs those bytes, and never reads a claim. The block is stateless:
// which keys besides the current ones a key set holds (a previous key after a rotation) is the host's, until the rotation ring of step
// B8b.
package token
