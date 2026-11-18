package main

import (
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"
)

// modulePrefix is the module path of the repository: a Go library at directory d has the path modulePrefix + d.
const modulePrefix = "github.com/truvity/atoom/"

// cratePrefix starts the name of every Rust library of the repository: the library at directory d is atoom-<d with dashes>.
const cratePrefix = "atoom-"

// portExternal lists what a port (a library one level below the root) may require besides atoom libraries: the ConnectRPC and
// Protobuf runtimes and nothing else (docs/plan.md, section 5). A driver may require its own backend SDK, so it is not listed.
var portExternal = map[string]map[string]bool{
	"go": {
		"connectrpc.com/connect":                                     true,
		"google.golang.org/protobuf":                                 true,
		"buf.build/go/protovalidate":                                 true,
		"buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go": true,
	},
	"rust": {
		"connectrpc":       true,
		"connectrpc-build": true,
		"buffa":            true,
		"buffa-types":      true,
	},
}

// lib is one library as the rules see it: a Go module or a Rust crate.
type lib struct {
	Dir      string   // the directory under the root, with slashes: "key/software"
	Lang     string   // "go" or "rust"
	Name     string   // the module path or the crate name
	Atoom    []string // the directories of the atoom libraries it requires directly
	External []string // the other direct dependencies, by module path or crate name
	Replaces []string // the module paths that a replace directive redirects (Go)
}

// shared names libraries that several drivers of one port may use (key/slotplan, key/pinrelease): they are libraries, not drivers.
type shared map[string]bool

// isTool reports whether a directory holds the repository's own tooling, which is not a library and is neither released nor checked
// by the rules.
func isTool(dir string) bool {
	return dir == "tools" || strings.HasPrefix(dir, "tools/")
}

// wantName is the module path or the crate name that a library at dir must have.
func wantName(lang, dir string) string {
	if lang == "go" {
		return modulePrefix + dir
	}
	return cratePrefix + strings.ReplaceAll(dir, "/", "-")
}

// allowedAtoom reports whether the library at dir may require the atoom library dep.
//
// A library may require: the common types; its own port; the conformance kit of its own port; and a library listed as shared.
// It may not require another driver, another port's code, or itself.
func allowedAtoom(dir, dep string, s shared) bool {
	port := strings.SplitN(dir, "/", 2)[0]
	switch {
	case dep == dir:
		return false
	case dep == "common":
		return true
	case dep == port:
		return true
	case dep == port+"/conformance":
		return true
	case s[dep]:
		return true
	}
	return false
}

// check returns the problems of one library, in a stable order.
func check(l lib, s shared) []string {
	var out []string
	add := func(format string, args ...any) {
		out = append(out, fmt.Sprintf("%s (%s %s): %s", l.Dir, l.Lang, l.Name, fmt.Sprintf(format, args...)))
	}
	if path.Clean(l.Dir) != l.Dir || l.Dir == "." || strings.HasPrefix(l.Dir, "/") || strings.HasPrefix(l.Dir, "..") {
		add("not a directory below the root")
		return out
	}
	depth := strings.Count(l.Dir, "/") + 1
	if depth > 2 {
		add("a library sits at most two levels below the root (<port>/<driver>); this one is %d deep", depth)
	}
	if want := wantName(l.Lang, l.Dir); l.Name != want {
		add("the name must be %s", want)
	}
	for _, dep := range l.Atoom {
		if !allowedAtoom(l.Dir, dep, s) {
			add("requires %s: a library may require only common, its own port, its port's conformance kit and the shared libraries, never another driver or port", dep)
		}
	}
	if depth == 1 {
		// A port, or common: only the runtimes beside atoom's own.
		for _, dep := range l.External {
			if !portExternal[l.Lang][dep] {
				add("a port requires only the ConnectRPC and Protobuf runtimes; it requires %s", dep)
			}
		}
	}
	for _, r := range l.Replaces {
		if strings.HasPrefix(r, modulePrefix) {
			add("replaces %s: a committed go.mod never carries a replace of an atoom module (go.work does that for development)", r)
		}
	}
	sort.Strings(out)
	return out
}

// checkAll applies the rules to every library, tooling excluded, and also checks that every library the code requires exists.
func checkAll(libs []lib, s shared) []string {
	var out []string
	known := map[string]bool{}
	for _, l := range libs {
		known[l.Lang+":"+l.Dir] = true
	}
	for _, l := range libs {
		if isTool(l.Dir) {
			continue
		}
		out = append(out, check(l, s)...)
		for _, dep := range l.Atoom {
			if !known[l.Lang+":"+dep] {
				out = append(out, fmt.Sprintf("%s (%s %s): requires %s, which is not a library of this repository", l.Dir, l.Lang, l.Name, dep))
			}
		}
	}
	return slices.Compact(out)
}
