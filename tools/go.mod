module github.com/truvity/atoom/tools

go 1.27.0

toolchain go1.27.4

require (
	connectrpc.com/connect v1.21.1 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

// protoc-gen-connect-go runs as a Go tool, so its version is the one pinned here: the ConnectRPC runtime's (versions.yaml, go_connect).
tool connectrpc.com/connect/cmd/protoc-gen-connect-go
