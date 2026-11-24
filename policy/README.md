# policy

The Cedar policy loader ([record 0019](../docs/decisions/0019-the-policy-bundle-and-its-loader.md), carrying out [record 0011](../docs/decisions/0011-cedar-as-a-library.md)): a signed policy bundle read through the [source port](../source/docs/source-port.md), verified, validated against its schema in strict mode and swapped in atomically, and decisions with the Cedar engine of the language that carry the digest of their set. It is a library **around an engine**, which a consumer links directly: not a port, a driver or a block ([architecture](../docs/architecture.md#the-kinds-of-thing)). The page is [docs/policy-loader.md](docs/policy-loader.md).

| | Go | Rust |
| --- | --- | --- |
| Library | module `github.com/truvity/atoom/policy` | crate `atoom-policy` (`atoom_policy`) |
| Engine | `github.com/cedar-policy/cedar-go` v1.9.3, strict validation from `x/exp/schema/validate` | `cedar-policy` `=4.14.1`, the reference |
| Signed document | `VerifyDocument`, `Compare`, `PublisherFromSPKI` | `verify`, `sequence`, `Publisher::from_spki` |
| Bundle and set | `ParseBundle`, `Compile`, `CompileVerified`, `Set.Decide` | `Bundle::parse`, `Set::compile`, `Set::from_verified`, `Set::decide` |
| Loader | `New(ctx, Options)`, `Loader.Current`, `Loader.Reload`, `Loader.Run` | `Loader::start(Options)`, `current`, `reload`, `run` |
| Test set | `go test` runs [`testdata/`](testdata/) | `cargo test` runs the same files |

```go
src := sourcev1alpha1connect.NewSourceServiceClient(hc, "http://source", call.ClientOptions()...) // source/file in process, or a socket
pub, _ := policy.PublisherFromSPKI("publisher-1", spkiDER)                                         // the host pins its keys
l, err := policy.New(ctx, policy.Options{Source: src, Publishers: []policy.Publisher{pub}, OnReload: audit})
if err != nil { /* no valid set: do not start */ }
go l.Run(ctx)
d, err := l.Decide(policy.Request{Principal: user, Action: action, Resource: route, Context: ctx, Entities: entities})
// d.Allow, d.Reasons, d.Digest; a *policy.Misfit is a denial
```

```rust
let opts = atoom_policy::Options::new(source_client, vec![Publisher::from_spki("publisher-1", &spki_der, vec![])?]);
let loader = Arc::new(Loader::start(opts).await?); // no valid set: do not start
tokio::spawn({ let l = loader.clone(); async move { l.run().await } });
let set = loader.current();
let entities = set.entities_from_json(entities_json)?;
let context = set.context_from_json(&action, context_json)?;
let d = set.decide(principal, action, resource, context, &entities)?; // Err(Misfit) is a denial
```

Known gaps of `cedar-go` (listed in the test set): no templates (a bundle with a template or a link is refused as `unsupported`), and a policy that no request can satisfy fails validation where `cedar-policy` only warns.

Dependencies: the `source` port, `socket` (the in-process transport of the tests, and in Rust the client type), the engine, and in Rust `aws-lc-rs` (ECDSA P-384 and SHA-384), `base64`, `serde_json` and `tokio`; the Go library verifies with the standard library. Where the code came from is in [provenance](../docs/provenance.md#policy).

Status: new in this release; the API may change until session and sluis have adopted it.
