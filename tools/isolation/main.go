// Command isolation holds the repository to its dependency rules (docs/plan.md, section 5; decision 0003).
//
//	go run ./tools/isolation [-root .] [-shared tools/isolation/shared.txt]
//
// A library may require atoom libraries only as the rules allow: the common types, its own port, its port's conformance kit and the
// libraries listed in the shared file. A port requires nothing outside the ConnectRPC and Protobuf runtimes. A committed go.mod never
// replaces an atoom module. Every go.mod is listed in go.work, and every Cargo.toml is a member of the workspace. The rules run over
// the Go modules (from go.work and go.mod) and the Rust crates (from `cargo metadata`).
//
// It exits 1 with one line per problem, and 2 when it cannot read the repository.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	root := flag.String("root", ".", "the root of the repository")
	sharedFile := flag.String("shared", "tools/isolation/shared.txt", "libraries that several drivers of one port may use, one directory per line (relative to the root)")
	flag.Parse()

	abs, err := filepath.Abs(*root)
	if err != nil {
		fatal(err)
	}
	s, err := readShared(filepath.Join(abs, *sharedFile))
	if err != nil {
		fatal(err)
	}
	goLibs, problems, err := goLibraries(abs)
	if err != nil {
		fatal(err)
	}
	rustLibs, rustProblems, err := rustLibraries(abs)
	if err != nil {
		fatal(err)
	}
	problems = append(problems, rustProblems...)
	libs := append(goLibs, rustLibs...)
	problems = append(problems, checkAll(libs, s)...)
	sort.Strings(problems)
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "isolation: "+p)
		}
		fmt.Fprintf(os.Stderr, "isolation: %d problem(s)\n", len(problems))
		os.Exit(1)
	}
	n := 0
	for _, l := range libs {
		if !isTool(l.Dir) {
			n++
		}
	}
	fmt.Printf("isolation: %d libraries (%d Go, %d Rust), no problem\n", n, count(goLibs), count(rustLibs))
}

func count(libs []lib) int {
	n := 0
	for _, l := range libs {
		if !isTool(l.Dir) {
			n++
		}
	}
	return n
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "isolation: "+err.Error())
	os.Exit(2)
}

// readShared reads the list of shared libraries. A missing file is an empty list; lines starting with # and blank lines are ignored.
func readShared(file string) (shared, error) {
	s := shared{}
	f, err := os.Open(file) // #nosec G304 -- the path is the tool's own flag
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		s[line] = true
	}
	return s, sc.Err()
}
