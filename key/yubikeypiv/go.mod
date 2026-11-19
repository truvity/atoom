module github.com/truvity/atoom/key/yubikeypiv

go 1.27.0

toolchain go1.27.4

require (
	connectrpc.com/connect v1.21.1
	github.com/go-piv/piv-go/v2 v2.6.0
	github.com/truvity/atoom/common v0.1.0
	github.com/truvity/atoom/key v0.1.0
	github.com/truvity/atoom/key/conformance v0.1.0
	github.com/truvity/atoom/key/pinrelease v0.1.0
	github.com/truvity/atoom/key/slotplan v0.1.0
	github.com/truvity/atoom/socket v0.1.0
	golang.org/x/sys v0.43.0 // indirect
	google.golang.org/protobuf v1.36.12
)

require golang.org/x/crypto v0.50.0 // indirect
