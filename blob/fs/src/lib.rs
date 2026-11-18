//! The `fs` driver of the blob port: the blob contract (`atoom.blob.v1alpha1`, see `blob/docs/blob-port.md`) over a local directory.
//!
//! - `create` is the write-once fence: the bytes go to a temporary file in the target's directory, are fsynced, then `link()`ed to the
//!   name, which fails if the name exists, and the directory is fsynced. Of many racing creators exactly one wins.
//! - `put` overwrites only a pointer ([`POINTERS`]), under an entity tag.
//! - Every path is checked textually and then opened with `openat2(RESOLVE_NO_SYMLINKS | RESOLVE_BENEATH)` below the root, so no
//!   `..`, absolute path or symlink leaves it; where `openat2` answers `ENOSYS` the path is walked with `openat(O_NOFOLLOW)`.
//!
//! The directory itself ([`Fs`]) is the library `atoom-blob-fsdir`, which `blob/fsreadonly` shares; this crate adds the backend
//! suite ([`suite`]) and the thin main `blob-fs`. [`service::Service`]`(fs).router()` is the ConnectRPC handler.

pub use atoom_blob_fsdir::{Error, Fs, POINTERS, service};

pub mod suite;
