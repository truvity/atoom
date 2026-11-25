# blocks/token

The `token` block ([record 0008](../../docs/decisions/0008-blocks.md), [record 0020](../../docs/decisions/0020-the-token-block-is-stateless.md)): compact JWS (RFC 7515) of a host's claims, signed by purpose through the [`seal` block](../seal/README.md); the key set (RFC 7517) of the current key of each purpose and of the public keys the host hands in, each with the kid rule of its purpose; and the verification of a token against those keys. It is built from the seal block and nothing else: it holds no key, its dependency is passed in by its host, and its claims rules are the host's. The page is [docs/token-block.md](docs/token-block.md).

| | |
| --- | --- |
| Contract | `atoom.token.v1alpha1.TokenService` ([token.proto](proto/atoom/token/v1alpha1/token.proto)): `Capabilities`, `Sign`, `KeySet`, `Verify`; Go stubs in `gen/`, Rust stubs in the crate `atoom-blocks-token` (this directory, made by `build.rs`) |
| Library | Go module `github.com/truvity/atoom/blocks/token`: `token.New(ctx, token.Options{Seal, Purposes, Rules})` returns the block; `Block.Handler()` is the ConnectRPC handler, to mount in-process (`socket.InProcess`) or on a socket (`socket.Serve`); `token.SealPurposes` is the block's declaration to a seal block that a host starts for it |
| Thin main | `token serve --socket P --purposes FILE --seal-socket S`, or `--key-socket K --store-socket S --installation NAME` to host a seal block in the same process; `token key-purposes`, `token check`, `token version` (`cmd/token`) |
| Kit | package `conformance` (`conformance.Run(ctx, client, out)`, with the kit's `Purposes()` and `Rules()`), and the command `token-conformance` (`--key-socket K --store-socket S` hosts a seal block and the token block over two drivers and runs the kit in-process; `--socket P` runs it against a block that another host serves; `--print-declaration`, `--print-purposes`) |

```go
keys, _ := route.New(map[string]keyv1alpha1connect.KeyStoreServiceClient{ // one client in front of a driver per purpose (key/route)
    "wrapping": kms, "identity-header": kms, "client-assertion": software,
})
ps := []token.Purpose{
    {Name: "client-assertion", Algorithm: purpose.ES256, Types: []string{"JWT"}},
    {Name: "identity-header", Algorithm: purpose.ES256, Types: []string{"session-identity+jwt"}},
}
sps, _ := token.SealPurposes(ps)
s, _ := seal.New(ctx, seal.Options{Keys: keys, Items: items, Purposes: append(myPurposes, sps...), Rules: sealRules, Installation: "site-1"})
shc, stop := socket.InProcess(s.Handler())
defer stop()
b, _ := token.New(ctx, token.Options{
    Seal:     sealv1alpha1connect.NewSealServiceClient(shc, "http://seal", call.ClientOptions()...),
    Purposes: ps,
    Rules:    token.AllowAll(), // the host's claims rules run before it calls
})
thc, stop2 := socket.InProcess(b.Handler())
defer stop2()
client := tokenv1alpha1connect.NewTokenServiceClient(thc, "http://token", call.ClientOptions()...)
```

`just conformance` runs the kit over a seal block on the memory drivers of its ports, `key/software` and `store/memory`, through their sockets, once in the kit's process and once behind the thin main (`tools/scripts/conformance-token.sh`); the block's own tests run it over a seal block on stand-ins and test what the kit cannot (a key that changes behind the block, a seal block that does not answer or refuses, a signature that does not verify, the facts, session's shape of a driver per purpose).

Dependencies: the block `seal`, the ports `key` and `store` (the thin main and the kit host a seal block over them), `common`, `socket` (the in-memory transport and the thin main's socket), and the ConnectRPC and Protobuf runtimes. No JOSE library: the JWS, the JWK and the thumbprint are a few lines over the standard library, and the kit checks them with its own.
