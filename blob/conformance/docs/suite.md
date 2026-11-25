# The blob suites

The cases of `blob_suite` and `blob_read_suite`, in the order they run. Each one speaks the contract and nothing else, over a client of the kit.

## The write suite

| Case | What it holds a driver to |
| --- | --- |
| `create_and_get` | `Create` answers a non-empty tag; `Get` returns the bytes and the same tag |
| `create_is_write_once` | a second `Create` of a path is `already_exists`, reason `taken`, field `path`, and the object keeps its first bytes |
| `the_tag_follows_the_bytes` | two paths with the same bytes get the same tag; the same size with other bytes gets another |
| `absent_object` | `Get` of a path that holds nothing is `not_found`, reason `absent`, field `path` |
| `malformed_paths` | the empty path, `../x`, `/x`, `a//b`, `a/../b` and a path with a NUL are each `invalid_argument`, reason `path`, field `path`, for `Create` and for `Get`; the prefix `../` is the same for `List`, field `prefix` |
| `put_overwrites_only_a_pointer` | `Put` over an object that is not a pointer is `permission_denied`/`rule`; for each of the kit's pointer names (`kit-head` and `kit-state`, `--print-pointers`): a `Put` under a tag at an absent pointer, and a `Put` with no tag at a pointer that is there, are `aborted`/`version`; a `Put` with no tag creates it; a `Put` under its tag replaces it and gives a new tag; a `Put` under the old tag is `aborted`/`version`; `Get` shows the last accepted `Put` |
| `list_pages_in_byte_order` | five objects are listed by pages of the default size, 1, 2 and 3: every path once, in byte order, the token empty on the last page and a page never larger than asked; a narrower prefix and a prefix inside a segment list what they should |

## The read-only suite

| Case | What it holds a driver to |
| --- | --- |
| `get_gives_the_bytes_and_a_tag` | `Get` of the fixture returns exactly its bytes and a non-empty tag, the same on a second read |
| `absent_object` | as above |
| `malformed_paths_read` | the paths of `malformed_paths` that a read can show are refused as above |
| `list_has_the_fixture` | a listing of the fixture's path has it, in byte order |
| `every_write_is_refused` | `Create` and `Put` are `permission_denied`/`rule`, field `path`, and the fixture is as it was |

## What the suite fixes beyond the proto comments

These are the choices the contract left open. Every driver makes them, and a consumer may rely on them. They are in the crate documentation of `atoom_blob_conformance` and, as prose, in the [blob port](../../docs/blob-port.md).

- A refusal always names the request's `path` as the field at fault (`prefix` for a `List`), and a path the driver cannot hold is `path` and one it can hold and does not have is `absent`.
- The pointers are the objects whose last segment is a name the host declared; the kit runs the driver with its own names, and a driver started without them fails `put_overwrites_only_a_pointer`.
- The tag is a function of the bytes.

## What the suite cannot test

It cannot make the backend unavailable, so the `unavailable`/`engine` refusal is a driver test. It cannot race two creators without a second client, so the race of many creators for one path is the `fs` driver's backend suite (`suite::run_temp`), and so are the rules of the backend itself (modes, the root checks, links, the fallback of `openat2`, the removal of temporary files). It cannot remove what it wrote, so a second run against the same root leaves a second prefix beside the first.
