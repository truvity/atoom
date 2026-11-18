module github.com/truvity/atoom/tools

go 1.27.0

toolchain go1.27.4

// The generators that the ports' buf.gen.yaml files run (`just gen`): the version of connect here is the one that makes every committed
// Go stub. They run in module mode, away from go.work (GOWORK=off): a workspace resolves the sibling modules a port requires for
// builds, but not for `go tool`.
require (
	connectrpc.com/connect v1.21.1
	google.golang.org/protobuf v1.36.12
)

tool connectrpc.com/connect/cmd/protoc-gen-connect-go
