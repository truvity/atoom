# blocks/seal

The `seal` block ([record 0008](../../docs/decisions/0008-blocks.md)): data keys per purpose and scope, wrapped through the [key port](../../key/docs/key-port.md) and kept in the [store port](../../store/docs/store-port.md); values sealed and opened under them, keyed hashes, and signing by purpose. It moved from sluis key@ ([record 0013](../../docs/decisions/0013-what-moves-from-sluis.md)). It is built from the two ports and nothing else: no backend code, its dependencies passed in by its host, the rules supplied by its host. The page is [docs/seal-block.md](docs/seal-block.md).

| | |
| --- | --- |
| Contract | `atoom.seal.v1alpha1.SealService` ([seal.proto](proto/atoom/seal/v1alpha1/seal.proto)): `Capabilities`, `Seal`, `Open` (batched), `Mac`, `Sign`, `PublicKey`, `Rotate`, `Erase`, `Rewrap`, `MarkCompromised`; Go stubs in `gen/` |
| Library | Go module `github.com/truvity/atoom/blocks/seal`: `seal.New(ctx, seal.Options{Keys, Items, Purposes, Rules, Installation, ...})` returns the block; `Block.Handler()` is the ConnectRPC handler, to mount in-process (`socket.InProcess`) or on a socket (`socket.Serve`); `seal.KeyPurposes` is the block's declaration to its key driver |
| Kit | package `conformance` (`conformance.Run(ctx, client, out)`, with the kit's `Purposes()` and `Rules()`), and the command `seal-conformance` (`--key-socket K --store-socket S` hosts the block over two drivers and runs the kit in-process; `--socket P` runs it against a block that another host serves; `--print-declaration`) |

```go
keys := keyv1alpha1connect.NewKeyStoreServiceClient(keyHC, "http://key", call.ClientOptions()...)       // key/awskms, key/software, a card
items := storev1alpha1connect.NewItemServiceClient(storeHC, "http://store", call.ClientOptions()...)    // store/dynamodb, store/memory, ...
b, err := seal.New(ctx, seal.Options{
    Keys: keys, Items: items, Installation: "site-1",
    Purposes: []seal.Purpose{{Name: "session", Use: seal.UseSeal, Code: 1}, {Name: "session-id", Use: seal.UseMac}},
    Rules:    myRules, // who may do what: the host's
})
hc, stop := socket.InProcess(b.Handler())
defer stop()
client := sealv1alpha1connect.NewSealServiceClient(hc, "http://seal", call.ClientOptions()...)
```

`just conformance` runs the kit over the memory drivers of the two ports, `key/software` and `store/memory`, through their sockets (`tools/scripts/conformance-seal.sh`); the block's own tests run it over in-memory stand-ins and test what the kit cannot (several recipients, the cache, a port that does not answer).

Dependencies: the ports `key` and `store`, `common`, `socket` (the in-memory transport of the command and the tests), the ConnectRPC and Protobuf runtimes, and `github.com/go-jose/go-jose/v4` for AES key wrap (RFC 3394), as sluis key@ had it.
