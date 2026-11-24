//! The Cedar policy loader (record 0019): a signed policy bundle read through the source port, verified, validated against its schema
//! in strict mode and swapped in atomically, and decisions with `cedar-policy` that carry the digest of their set.
//!
//! The layers, each usable alone:
//!
//! - [`verify`]: the signed document (a JWS in the general JSON serialisation, ES384, pinned kids), steps 1 to 4; [`sequence`], step 5.
//! - [`Bundle::parse`] and [`Set::compile`]: the payload (schema, policies, templates, links), parsed and validated in strict mode.
//! - [`Set::decide`]: one decision over the entities the caller gives, with no I/O, carrying the digest of the set.
//! - [`Loader::start`], [`Loader::reload`] and [`Loader::run`]: the cold start, the atomic swap, the last good set and the refresh.
//!
//! Cedar is used as a library (record 0011): the request, the context and the entities are `cedar-policy`'s own types. The Go twin is
//! the module `github.com/truvity/atoom/policy`; both run the decision test set in `testdata/`.

mod bundle;
mod document;
mod json;
mod loader;
mod set;

pub use bundle::{Bundle, Class, EntityRef, Fault, Link};
pub use document::{
    MAX_DOCUMENT, NotP384, Publisher, Refused, Sequence, Verified, hex, sequence, verify,
};
pub use loader::{
    DEFAULT_KIND, DEFAULT_MIN_INTERVAL, DEFAULT_NAME, DEFAULT_POLL_EVERY, LoadError, Loader,
    OnReload, Options, Outcome, Pass,
};
pub use set::{Decision, Misfit, Set, uid};

/// The engine itself, for the types a caller builds a request with.
pub use cedar_policy;
