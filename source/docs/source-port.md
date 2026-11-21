# The source port

`atoom.source.v1alpha1.SourceService`: a **source** holds named values and gives back their bytes. It does not know what the bytes mean and never parses them. The contract is [source.proto](../proto/atoom/source/v1alpha1/source.proto); this page is its rules in prose, and the conformance kit ([README](../conformance/README.md), [the suite](../conformance/docs/suite.md)) holds every driver to them.

Two kinds of source use the one contract:

- **A config source** returns a document, often a signed one, by its name. The caller checks the signature; the driver returns the bytes. A config source may be watched.
- **A secret source** returns the value of a named secret. The caller keeps it in memory; a driver never writes it to disk, a log line or an error, and its version label is never a hash of the value.

The port has three calls:

| Call | What it does |
| --- | --- |
| `Capabilities` | how the source reports a newer version: `NONE`, `NOTIFY` or `POLL` |
| `Read` | the bytes of a name and their version label |
| `Watch` | a stream that says a newer version of a name exists (drivers that `NOTIFY`) |

## The contract

- **A name** is a string the source's backend can hold: a kebab-case document name for the file driver, a credential name for the credential driver. A name that cannot leave the backend (empty, with a `/`, a `..`, a NUL) is never served.
- **`Read`** returns the bytes exactly as the backend holds them, and the **version label**: a string, not a number (a file's digest, a credential's modification time and inode, an AppConfig version). The label is the same for the same content and moves when the content does.
- **`if_none_match`** is the label the caller holds. A read at that label is answered `unchanged: true`, with no bytes and the same label; a read at any other label, or at none, gets the bytes. A caller that holds a label asks again cheaply.

## Capabilities

`Capabilities` says how a caller learns that a newer version of a name exists (`watch`). A caller asks once, at start.

- **`watch`** is one of:
  - `NONE`: never. `Watch` is `unimplemented`. A credential cannot change under a running service; a Lambda layer cannot either.
  - `NOTIFY`: the driver pushes. `Watch` is a stream of `(name, version)`: it says nothing while nothing changes, and says the new label (the empty label for a name that went away) when the content changes. The first look sets what the watcher already holds without telling it: a watcher asks for the version it holds with `Read`, and `Watch` says what changed after it began.
  - `POLL`: the caller asks again. Every `Read` says in `poll_after` when. No driver in this repository polls yet.

## Refusals

Every refusal is a Connect error with the `Refusal` detail of [common](../../common/docs/refusal.md): a reason from a closed list and the path of the field at fault, never a value. The field is always the request's `name`.

| Code | Reason | When |
| --- | --- | --- |
| `invalid_argument` | `schema` | a name the source cannot hold |
| `not_found` | `absent` | no value of that name |
| `failed_precondition` | `bound` | the value is over the driver's size limit, is not a plain file, or is reached through a link the driver does not follow |
| `permission_denied` | `rule` | the backend's own rule refuses the value (the credential driver: a file open to others) |
| `unavailable` | `engine` | the backend failed; the message is the reason only, never the backend's text |
| `unimplemented` | `rule` | `Watch` on a source that cannot be watched |

## Drivers

| Driver | Backend | Watch | Where it fits |
| --- | --- | --- | --- |
| [source/file](../file/README.md) | a directory of `<name>.jws` documents | `NOTIFY` | a host with its signed configuration in a directory, or a mounted ConfigMap |
| [source/credential](../credential/README.md) | systemd credentials (`$CREDENTIALS_DIRECTORY`) or a directory of them | `NONE` | a service that gets its secrets from systemd |
| `source/file` in Go | the same directory, linked into a Go process | `NOTIFY` | a Go consumer that reads its documents in-process (planned, step B2) |
| `source/appconfig`, `source/ssm` | AWS AppConfig, SSM Parameter Store | `POLL`, `NONE` | Lambda and Kubernetes (planned, step B9) |

Both built drivers are Linux only (they open files with `openat2`), and say so in their manifests; the macOS jobs of CI leave them out.

## The call context

Every call carries the deadline and the correlation id of the request it is part of, `Watch` included. The rules are on [the call context](../../docs/call-context.md). A Rust client: `SourceServiceClient` over `atoom_socket::dial`; a Go client: `sourcev1alpha1connect.NewSourceServiceClient(hc, url, call.ClientOptions()...)`.

## A driver of your own

Serve `SourceService` (Go: implement `sourcev1alpha1connect.SourceServiceHandler`; Rust: implement the `SourceService` trait of the crate `atoom-source`), serve it on a socket or mount it in a process, put one value in its backend, and run the kit against it: `source-conformance --socket PATH --present NAME --bytes FILE`. A driver passes the suite or it is not a driver. The kit speaks the contract only, so the driver may be written in any language.
