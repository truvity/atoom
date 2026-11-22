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
// Protobuf runtimes and what their generated code needs, and nothing else (docs/archive/extraction-plan.md, section 5). A driver may require its own backend
// SDK, so it is not listed.
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
		// The two crates the generated messages and the Connect error-detail value need (the `json` feature derives serde; the detail
		// is base64). Both are in connectrpc's own graph, so a port that uses them adds nothing to a consumer's build.
		"serde":  true,
		"base64": true,
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

// blocksDir holds the blocks (record 0008): blocks/<block>, a contract built from other ports and blocks.
const blocksDir = "blocks"

// isBlock reports whether dir is a block.
func isBlock(dir string) bool { return strings.HasPrefix(dir, blocksDir+"/") }

// allowedAtoom reports whether the library at dir may require the atoom library dep.
//
// A library may require: the common types; its own port; the conformance kit of its own port; and a library listed as shared.
// It may not require another driver, another port's code, or itself.
//
// A block (blocks/<block>, record 0008) is built from ports and blocks only: it may require the common types, any port, the shared
// libraries and another block, and never a driver. No library but a block may require a block, and the blocks have no cycle (checkAll).
func allowedAtoom(dir, dep string, s shared) bool {
	port := strings.SplitN(dir, "/", 2)[0]
	switch {
	case dep == dir:
		return false
	case isBlock(dir):
		// A port or common is a library at the first level; a driver is at the second, and only another block is allowed there.
		return (!strings.Contains(dep, "/") && dep != blocksDir && !isTool(dep)) || isBlock(dep) || s[dep]
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
		if allowedAtoom(l.Dir, dep, s) {
			continue
		}
		if isBlock(l.Dir) {
			add("requires %s: a block may require only common, the ports, the shared libraries and other blocks, never a driver", dep)
		} else {
			add("requires %s: a library may require only common, its own port, its port's conformance kit and the shared libraries, never another driver or port", dep)
		}
	}
	if l.Dir == blocksDir {
		add("blocks holds the blocks, one directory each; it is not a library")
	}
	if depth == 1 && !s[l.Dir] && l.Dir != blocksDir {
		// A port, or common: only the runtimes beside atoom's own. A library at the first level that the shared file lists (socket: the
		// plain UNIX-socket host of the Rust mains) is plumbing and not a port, so it may require what it needs.
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
	out = append(out, cycles(libs)...)
	return slices.Compact(out)
}

// cycles names each library that requires itself through other libraries of the same language (a block that requires a block that
// requires it). The rules above already keep ports and drivers out of a cycle; the blocks may require one another, so the graph is
// walked.
func cycles(libs []lib) []string {
	deps := map[string][]string{}
	for _, l := range libs {
		deps[l.Lang+":"+l.Dir] = l.Atoom
	}
	var out []string
	keys := make([]string, 0, len(deps))
	for k := range deps {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, start := range keys {
		lang, dir, _ := strings.Cut(start, ":")
		seen := map[string]bool{}
		stack := append([]string(nil), deps[start]...)
		for len(stack) > 0 {
			d := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if d == dir {
				out = append(out, fmt.Sprintf("%s (%s): requires itself through other libraries: a cycle", dir, lang))
				break
			}
			if seen[d] {
				continue
			}
			seen[d] = true
			stack = append(stack, deps[lang+":"+d]...)
		}
	}
	return out
}
