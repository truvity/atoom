package main

import (
	"strings"
	"testing"
)

func goLib(dir string, atoom, external []string) lib {
	return lib{Dir: dir, Lang: "go", Name: modulePrefix + dir, Atoom: atoom, External: external}
}

func TestAllowedDependencies(t *testing.T) {
	s := shared{"key/slotplan": true, "socket": true}
	libs := []lib{
		// A shared library at the first level is plumbing, not a port: it may require what it needs, and any driver or kit may require it.
		{Dir: "socket", Lang: "rust", Name: "atoom-socket", External: []string{"hyper", "tokio", "anything"}},
		{Dir: "store", Lang: "rust", Name: "atoom-store", Atoom: []string{"common"}, External: []string{"connectrpc"}},
		{Dir: "store/memory", Lang: "rust", Name: "atoom-store-memory", Atoom: []string{"store", "socket", "common"}, External: []string{"tokio"}},
		goLib("common", nil, []string{"google.golang.org/protobuf"}),
		goLib("key", []string{"common"}, []string{"connectrpc.com/connect", "google.golang.org/protobuf"}),
		goLib("key/conformance", []string{"key", "common"}, []string{"github.com/anything/goes"}),
		goLib("key/software", []string{"key", "key/conformance", "common"}, []string{"github.com/some/sdk"}),
		goLib("key/slotplan", nil, nil),
		goLib("key/yubikeypiv", []string{"key", "key/slotplan"}, []string{"github.com/go-piv/piv-go"}),
		goLib("store", []string{"common"}, []string{"connectrpc.com/connect"}),
		goLib("store/rqlite", []string{"store"}, nil),
		goLib("tools", nil, []string{"anything"}),
		// A block is built from ports, shared libraries and other blocks, with any library it needs.
		goLib("socket", []string{"common"}, nil),
		goLib("blocks/seal", []string{"common", "key", "store", "socket"}, []string{"github.com/go-jose/go-jose/v4"}),
		goLib("blocks/token", []string{"blocks/seal", "key", "common"}, nil),
		{Dir: "common", Lang: "rust", Name: "atoom-common", External: []string{"base64", "buffa", "connectrpc", "connectrpc-build", "serde"}},
	}
	if got := checkAll(libs, s); len(got) != 0 {
		t.Fatalf("a valid tree has problems:\n%s", strings.Join(got, "\n"))
	}
}

func TestWrongDependenciesFail(t *testing.T) {
	s := shared{"socket": true}
	cases := []struct {
		name string
		libs []lib
		want string
	}{
		{
			name: "a driver requires another driver",
			libs: []lib{
				goLib("key", nil, nil),
				goLib("key/software", []string{"key"}, nil),
				goLib("key/awskms", []string{"key", "key/software"}, nil),
			},
			want: "key/awskms (go github.com/truvity/atoom/key/awskms): requires key/software",
		},
		{
			name: "a driver requires another port",
			libs: []lib{
				goLib("key", nil, nil),
				goLib("store", nil, nil),
				goLib("key/software", []string{"key", "store"}, nil),
			},
			want: "requires store",
		},
		{
			name: "a port requires a backend SDK",
			libs: []lib{goLib("key", nil, []string{"github.com/aws/aws-sdk-go-v2"})},
			want: "a port requires only the ConnectRPC and Protobuf runtimes; it requires github.com/aws/aws-sdk-go-v2",
		},
		{
			name: "a Rust port that is not shared requires tokio",
			libs: []lib{{Dir: "store", Lang: "rust", Name: "atoom-store", External: []string{"connectrpc", "tokio"}}},
			want: "a port requires only the ConnectRPC and Protobuf runtimes; it requires tokio",
		},
		{
			name: "a port requires a driver of itself",
			libs: []lib{
				goLib("key", []string{"key/software"}, nil),
				goLib("key/software", []string{"key"}, nil),
			},
			want: "requires key/software",
		},
		{
			name: "a library that does not exist",
			libs: []lib{goLib("key", []string{"common"}, nil)},
			want: "requires common, which is not a library of this repository",
		},
		{
			name: "a wrong module path",
			libs: []lib{{Dir: "key", Lang: "go", Name: "github.com/truvity/atoom/keys"}},
			want: "the name must be github.com/truvity/atoom/key",
		},
		{
			name: "a wrong crate name",
			libs: []lib{{Dir: "key/software", Lang: "rust", Name: "atoom-key"}},
			want: "the name must be atoom-key-software",
		},
		{
			name: "a replace of an atoom module",
			libs: []lib{{Dir: "key", Lang: "go", Name: modulePrefix + "key", Replaces: []string{modulePrefix + "common"}}},
			want: "a committed go.mod never carries a replace",
		},
		{
			name: "a block requires a driver",
			libs: []lib{
				goLib("key", nil, nil),
				goLib("key/software", []string{"key"}, nil),
				goLib("blocks/seal", []string{"key", "key/software"}, nil),
			},
			want: "blocks/seal (go github.com/truvity/atoom/blocks/seal): requires key/software: a block may require only common, the ports",
		},
		{
			name: "a port requires a block",
			libs: []lib{
				goLib("key", []string{"blocks/seal"}, nil),
				goLib("blocks/seal", []string{"key"}, nil),
			},
			want: "key (go github.com/truvity/atoom/key): requires blocks/seal",
		},
		{
			name: "a driver requires a block",
			libs: []lib{
				goLib("key", nil, nil),
				goLib("key/software", []string{"key", "blocks/seal"}, nil),
				goLib("blocks/seal", []string{"key"}, nil),
			},
			want: "key/software (go github.com/truvity/atoom/key/software): requires blocks/seal",
		},
		{
			name: "two blocks require each other",
			libs: []lib{
				goLib("blocks/seal", []string{"blocks/token"}, nil),
				goLib("blocks/token", []string{"blocks/seal"}, nil),
			},
			want: "blocks/seal (go): requires itself through other libraries: a cycle",
		},
		{
			name: "blocks is not a library",
			libs: []lib{goLib("blocks", nil, nil)},
			want: "blocks holds the blocks",
		},
		{
			name: "three levels deep",
			libs: []lib{goLib("key/software/deep", nil, nil)},
			want: "at most two levels",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := strings.Join(checkAll(c.libs, s), "\n")
			if !strings.Contains(got, c.want) {
				t.Fatalf("want a problem containing %q, got:\n%s", c.want, got)
			}
		})
	}
}

func TestParseGoMod(t *testing.T) {
	data := []byte(`{
	  "Module": {"Path": "github.com/truvity/atoom/key/software"},
	  "Require": [
	    {"Path": "github.com/truvity/atoom/key", "Version": "v0.1.0"},
	    {"Path": "github.com/truvity/atoom/key/awskms", "Version": "v0.1.0"},
	    {"Path": "example.com/sdk", "Version": "v1.0.0"},
	    {"Path": "example.com/indirect", "Version": "v1.0.0", "Indirect": true}
	  ],
	  "Replace": [{"Old": {"Path": "github.com/truvity/atoom/key"}, "New": {"Path": "../"}}]
	}`)
	l, err := parseGoMod("key/software", data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(l.Atoom, ",") != "key,key/awskms" || strings.Join(l.External, ",") != "example.com/sdk" || len(l.Replaces) != 1 {
		t.Fatalf("parsed wrongly: %+v", l)
	}
	got := strings.Join(check(l, shared{}), "\n")
	if !strings.Contains(got, "requires key/awskms") || !strings.Contains(got, "replaces github.com/truvity/atoom/key") {
		t.Fatalf("problems missing:\n%s", got)
	}
}

func TestParseCargoMetadata(t *testing.T) {
	root := t.TempDir()
	data := []byte(`{"packages": [
	  {"name": "atoom-common", "manifest_path": "` + root + `/common/Cargo.toml", "dependencies": []},
	  {"name": "atoom-store-rqlite", "manifest_path": "` + root + `/store/rqlite/Cargo.toml",
	   "dependencies": [{"name": "atoom-common"}, {"name": "atoom-key-software"}, {"name": "serde"}]},
	  {"name": "atoom-key-software", "manifest_path": "` + root + `/key/software/Cargo.toml", "dependencies": []}
	]}`)
	libs, problems, err := parseCargoMetadata(root, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 || len(libs) != 3 {
		t.Fatalf("libs %d, problems %v", len(libs), problems)
	}
	got := strings.Join(checkAll(libs, shared{}), "\n")
	if !strings.Contains(got, "store/rqlite (rust atoom-store-rqlite): requires key/software") {
		t.Fatalf("the cross-driver crate dependency passed:\n%s", got)
	}
}
