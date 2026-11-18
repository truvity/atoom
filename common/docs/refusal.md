# Refusals

A Connect error carries a code and, in atoom and its consumers, one detail: a `Refusal`.

```proto
message Refusal {
  string reason = 1;      // from a closed list; never a value
  string field_path = 2;  // the path of the field at fault; never its value
}
```

## Rules

- **The reason is a word from a closed list**, and the list belongs to the contract that refuses (the key port lists its own, the item port its own). The reason is also the message of the error. It never holds a value of the request.
- **The field path names the field at fault**, for example `indexes.0.name`, or is empty when no field is at fault. It never holds the value in the field.
- **The detail travels with the error** between services of one deployment, so the caller can act on the reason without parsing a message.
- **At an edge that faces a public caller the detail is removed.** `Strip` returns the code of the error and a fixed message for that code (the lower-case name of the code, `permission denied`), with no detail, no metadata and nothing of the cause. An error that is not a Connect error becomes `internal`. A public caller learns what to do, never why someone else may do more.
- **The wire name** of the detail is `atoom.common.v1alpha1.Refusal`. A Connect peer writes that bare name, a gRPC peer the Any type URL (`type.googleapis.com/atoom.common.v1alpha1.Refusal`); both readers accept either, and the value in padded or unpadded base64.

## Go

```go
import "github.com/truvity/atoom/common/refusal"

err := refusal.New(connect.CodeInvalidArgument, "digest", "request.digest") // code, reason, field path

reason, ok := refusal.Reason(err)    // sees through fmt.Errorf("...: %w", err)
path, ok := refusal.FieldPath(err)
edge := refusal.Strip(err)           // the code and a fixed message, no detail
```

## Rust

```rust
use atoom_common::refusal;
use connectrpc::error::ErrorCode;

let err = refusal::new(ErrorCode::InvalidArgument, "digest", "request.digest");
let reason: Option<String> = refusal::reason(&err);
let detail = refusal::detail(&err); // the whole Refusal, with the field path
```

## IndexEntry

`IndexEntry` is one declared index of an item, computed by the writer: the name of the declared index and the value to match under it. The item port (step 3 of the [plan](../../docs/plan.md)) and its consumers use it.

## Compatibility

The package is `v1alpha1`. The field numbers are those of sluis's `sluis.type.v2alpha1` messages, so the bytes of a message did not change with the move; the full name did, and the full name is the wire name of the detail and part of the RPC path of any service that mentions it. The tests of both languages pin the full names and the bytes.
