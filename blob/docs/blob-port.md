# The blob port

`atoom.blob.v1alpha1.BlobService`: a **blob store** keeps objects under paths. It does not know what the bytes mean. It was made for an append-only audit log in the layout of `tlog-tiles`: almost every object is written once and never changes, and two objects (the checkpoint, and the state of the writer) are pointers that move under a condition. The contract is [blob.proto](../proto/atoom/blob/v1alpha1/blob.proto); this page is its rules in prose, and the conformance kit ([README](../conformance/README.md), [the suite](../conformance/docs/suite.md)) holds every driver to them.

The port has four calls:

| Call | What it does |
| --- | --- |
| `Create` | writes an object that must not exist (the write-once fence); `already_exists` when the path holds one |
| `Put` | overwrites a **pointer** under a condition: the entity tag the object must have; an empty tag means it must be absent |
| `Get` | the bytes of an object and its entity tag |
| `List` | the paths under a prefix, in byte order, one page at a time |

There is no delete and no way to rewrite a sealed object. That is the point of the port: the one who holds it cannot change history.

## The contract

- **A path** is segments joined by `/`. A segment is not empty, does not start with a dot (so there is no `.` and no `..`), has no NUL and is at most 255 bytes; a path is at most 1024 bytes and does not start with `/`. A path that is not so is refused, whatever the call.
- **`Create`** is the fence: of any number of callers that race to create one path exactly one wins, and the others are `already_exists`. An object that was created is never replaced by a `Create`.
- **The entity tag** is a string that depends on the bytes only: the same bytes give the same tag at any path, other bytes give another tag (the `fs` drivers use the SHA-384 of the bytes, in hex). `Create` and `Put` answer the tag of what they wrote, and `Get` the tag of what it read.
- **`Put`** is for pointers. The last segment of a pointer's path is `checkpoint` or `writer-state`; a `Put` anywhere else is refused. An empty `if_match` means the pointer must be absent, and any other value is the tag it must have; a pointer that is not as the call says is `aborted`. The compare and the replace are one step, also between two processes that serve one store.
- **`List`** answers the paths that start with the prefix, in byte order. The prefix may end inside a segment. A page has at most `page_size` paths (0 is a default, 1000 for the `fs` drivers, and no page is larger than 10 000); `page_token` is what the last page said, and the token of the next page is empty on the last page. A path that is created while a listing runs may or may not be in it.

## Refusals

Every refusal is a Connect error with the `Refusal` detail of [common](../../common/docs/refusal.md): a reason from a closed list and the path of the field at fault, never a value.

| Code | Reason | When |
| --- | --- | --- |
| `invalid_argument` | `path` | a path, a prefix or a page token that is not acceptable; the field is `path` or `prefix` |
| `not_found` | `absent` | no object at the path |
| `already_exists` | `taken` | `Create` of a path that holds an object |
| `aborted` | `version` | `Put` with a tag that is not the pointer's |
| `permission_denied` | `rule` | `Put` on a path that is not a pointer; any write to a read-only driver |
| `unavailable` | `engine` | the backend failed; the message is the reason only, never the backend's text |

## Drivers

| Driver | Backend | Where it fits |
| --- | --- | --- |
| [blob/fs](../fs/README.md) | a local directory | a host with its log on a disk |
| [blob/fsreadonly](../fsreadonly/README.md) | the same directory, read only | a second process under another user that reads the log and can change nothing |

Both are Linux only (they open every path with `openat2`), and say so in their manifests; the macOS jobs of CI leave them out.

## Every call carries the deadline and the correlation id

Every call of the port carries what is left of the caller's deadline (`Connect-Timeout-Ms`) and the correlation id of the request (`Sluis-Correlation-Id`, 1 to 64 characters of letters, digits, `-` and `_`), and the thin main of every driver reads both and hands them to its handler, which hands them down to the calls it makes. A callee never extends its caller's deadline, and an id of another shape is dropped. A client of the blob port: `BlobServiceClient`, Rust over `atoom_socket::dial`, Go `blobv1alpha1connect.NewBlobServiceClient(hc, url, call.ClientOptions()...)`. The rules and how a caller outside a handler states them are on [the call context](../../docs/call-context.md).

## A driver of your own

Serve `BlobService` (Go: implement `blobv1alpha1connect.BlobServiceHandler`; Rust: implement the `BlobService` trait of the crate `atoom-blob`), serve it on a socket or mount it in a process, and run the kit against it: `blob-conformance --socket PATH`. A driver that only reads passes the read-only suite: `blob-conformance --socket PATH --read-only --present PATH --bytes FILE`. A driver passes the suite or it is not a driver. The kit speaks the contract only, so the driver may be written in any language.
