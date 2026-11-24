// Package policy loads a signed Cedar policy bundle through the source port and decides with cedar-go, by the loading rules of record
// 0019 (docs/decisions/0019-the-policy-bundle-and-its-loader.md). The Rust crate atoom-policy in the same directory does the same with
// cedar-policy, and the two run the one decision test set in testdata/.
//
// The layers, each usable alone:
//
//   - VerifyDocument: the signed document (a JWS in the general JSON serialisation, ES384, pinned kids), steps 1 to 4; Compare, step 5.
//   - ParseBundle and Compile: the payload (schema, policies, templates, links), parsed and validated against the schema in strict mode.
//   - Set.Decide: one decision over the entities the caller gives, with no I/O, carrying the digest of the set.
//   - New, Loader.Reload and Loader.Run: the cold start, the atomic swap, the last good set and the refresh over the source port.
//
// Cedar is used as a library (record 0011): the request and the entities are cedar-go's own types.
package policy
