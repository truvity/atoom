# The destination suite

What `suite::run` holds a destination to, in the order it runs. It speaks the contract through the five calls of the `Destination` trait (over a socket, through `rpc::Client` (`atoom_destination::client::Client`), which maps each call to the contract and each refusal back), writes under the address prefix `conformance/`, and removes what it wrote, whatever happened.

| Step (printed as `ok: ...`) | What it holds a driver to |
| --- | --- |
| *before the suite* | the destination holds nothing under `conformance/`: it needs an empty one |
| `put and list` | `Put` reports the SHA-256 of the body and a non-empty version; `List` shows that document with the same digest, the size and the content type and cache control it was written with |
| `overwrite` | a second `Put` at an address replaces the whole document; a `Put` of the same bytes is harmless |
| `paging and prefix` | a listing by pages of different sizes has every document once, in address order, never a page larger than asked; a prefix lists what starts with it, also inside a segment |
| `a large body` | a binary body near the limit (2 MiB) survives byte for byte |
| `a write is whole` | a reader that lists while a document is replaced sees the old digest or the new one, never a mixture |
| `delete` | `Delete` removes what was written and says so; a second `Delete` says `false`; the document is no longer listed |
| `refusals` | an address that is empty, has a leading or trailing `/`, an empty segment, `..`, `./`, a capital letter, a space or the reserved first segment `.atoom` is refused by `Put` and `Delete`, a prefix of `../` or `/` by `List`, a body over the limit and a header value with a line break by `Put`, all as `invalid`; a refused `Put` leaves no document |
| `no public address` (when `public_base` is not set) | `ReadBack` is `unsupported` and `Purge` answers `None` |
| `public address, purge and read back` (when it is set) | `ReadBack` of the public address gives status 200, the body and the content type and cache control that were written; after a replacement and a `Purge` the next `ReadBack` gives the new document; `Purge` answers `Purged` with an edge and `None` without; a `Purge` or `ReadBack` of another host is `invalid`; a document that is not there is a 404 |

## What the suite fixes beyond the proto comments

These are the choices the contract left open. Every driver makes them, and a consumer may rely on them. They are in the crate documentation of `atoom_destination_conformance` and, as prose, in the [destination port](../../docs/destination-port.md).

- The address, prefix, body and header rules of `check_address`, `check_prefix` and `check_document`, which every driver applies the same way (a driver of the Rust trait calls them).
- `Put` reports the SHA-256 of the body it holds, in lower-case hex, and `List` that of what it holds now.
- A destination with no public address answers `unsupported` to `ReadBack` and `None` to `Purge`.

## What the suite cannot test

It cannot change the destination behind the driver's back, so that an edit made at the destination shows in the digest of `List` is the driver's own test (the dir driver has one). It cannot crash the driver, so that a crash in the middle of a write leaves the old document is a driver test too. It cannot make the destination unavailable, so the `unavailable`/`engine` refusal is a driver test. And it tests `ReadBack` and `Purge` of a destination with a public address only through the flags that say there is one: a driver that fetches a real URL needs a real destination under it.
