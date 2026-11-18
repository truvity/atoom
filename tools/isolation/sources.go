package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// skipDir names the directories that the walks never enter.
func skipDir(name string) bool {
	switch name {
	case ".git", "target", "node_modules", "testdata", ".devbox", ".direnv":
		return true
	}
	return false
}

// run runs a command in dir and returns its standard output.
func run(dir, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...) // #nosec G204 -- fixed tool names, arguments built by this program
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// find returns the directories (relative, with slashes) that hold a file with the given name.
func find(root, file string) ([]string, error) {
	var dirs []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == file {
			rel, err := filepath.Rel(root, filepath.Dir(p))
			if err != nil {
				return err
			}
			dirs = append(dirs, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(dirs)
	return dirs, err
}

type goModJSON struct {
	Module  struct{ Path string }
	Require []struct {
		Path     string
		Indirect bool
	}
	Replace []struct {
		Old struct{ Path string }
	}
}

type goWorkJSON struct {
	Use []struct{ DiskPath string }
}

// goLibraries reads every Go module of the tree. It reports a go.mod that go.work does not list, and a module that go.work lists and
// that has no go.mod.
func goLibraries(root string) ([]lib, []string, error) {
	var problems []string
	found, err := find(root, "go.mod")
	if err != nil {
		return nil, nil, err
	}
	out, err := run(root, "go", "work", "edit", "-json")
	if err != nil {
		return nil, nil, err
	}
	var work goWorkJSON
	if err := json.Unmarshal(out, &work); err != nil {
		return nil, nil, err
	}
	used := map[string]bool{}
	for _, u := range work.Use {
		used[filepath.ToSlash(filepath.Clean(u.DiskPath))] = true
	}
	var libs []lib
	for _, dir := range found {
		if dir == "." {
			problems = append(problems, "go.mod at the root: the repository root is not a module; every library has its own directory")
			continue
		}
		if !used[dir] {
			problems = append(problems, fmt.Sprintf("%s: has a go.mod that go.work does not list", dir))
		}
		l, err := readGoMod(root, dir)
		if err != nil {
			return nil, nil, err
		}
		libs = append(libs, l)
	}
	have := map[string]bool{}
	for _, d := range found {
		have[d] = true
	}
	for u := range used {
		if !have[u] {
			problems = append(problems, fmt.Sprintf("%s: go.work lists it, and it has no go.mod", u))
		}
	}
	return libs, problems, nil
}

func readGoMod(root, dir string) (lib, error) {
	out, err := run(root, "go", "mod", "edit", "-json", filepath.Join(dir, "go.mod"))
	if err != nil {
		return lib{}, err
	}
	return parseGoMod(dir, out)
}

// parseGoMod turns the output of `go mod edit -json` into a library.
func parseGoMod(dir string, data []byte) (lib, error) {
	var m goModJSON
	if err := json.Unmarshal(data, &m); err != nil {
		return lib{}, fmt.Errorf("%s: %w", dir, err)
	}
	l := lib{Dir: dir, Lang: "go", Name: m.Module.Path}
	for _, r := range m.Require {
		if r.Indirect {
			continue
		}
		if rest, ok := strings.CutPrefix(r.Path, modulePrefix); ok {
			l.Atoom = append(l.Atoom, rest)
		} else {
			l.External = append(l.External, r.Path)
		}
	}
	for _, r := range m.Replace {
		l.Replaces = append(l.Replaces, r.Old.Path)
	}
	return l, nil
}

type cargoMetadata struct {
	Packages []struct {
		Name         string `json:"name"`
		ManifestPath string `json:"manifest_path"`
		Dependencies []struct {
			Name string `json:"name"`
		} `json:"dependencies"`
	} `json:"packages"`
}

// rustLibraries reads every crate of the workspace from `cargo metadata`, and reports a Cargo.toml that is not a member. Without a
// Cargo.toml at the root there is no Rust library.
func rustLibraries(root string) ([]lib, []string, error) {
	if _, err := os.Stat(filepath.Join(root, "Cargo.toml")); os.IsNotExist(err) {
		return nil, nil, nil
	}
	out, err := run(root, "cargo", "metadata", "--no-deps", "--format-version", "1")
	if err != nil {
		return nil, nil, err
	}
	return parseCargoMetadata(root, out)
}

// parseCargoMetadata turns `cargo metadata --no-deps` output into libraries, and checks the manifests of the tree against it.
func parseCargoMetadata(root string, data []byte) ([]lib, []string, error) {
	var m cargoMetadata
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, nil, err
	}
	dirOf := map[string]string{} // crate name -> directory
	var libs []lib
	members := map[string]bool{}
	for _, p := range m.Packages {
		rel, err := filepath.Rel(root, filepath.Dir(p.ManifestPath))
		if err != nil {
			return nil, nil, err
		}
		dir := filepath.ToSlash(rel)
		dirOf[p.Name] = dir
		members[dir] = true
	}
	var problems []string
	for _, p := range m.Packages {
		dir := dirOf[p.Name]
		l := lib{Dir: dir, Lang: "rust", Name: p.Name}
		for _, d := range p.Dependencies {
			if !strings.HasPrefix(d.Name, cratePrefix) {
				l.External = append(l.External, d.Name)
				continue
			}
			target, ok := dirOf[d.Name]
			if !ok {
				problems = append(problems, fmt.Sprintf("%s (rust %s): requires %s, which is not a crate of this workspace", dir, p.Name, d.Name))
				continue
			}
			l.Atoom = append(l.Atoom, target)
		}
		libs = append(libs, l)
	}
	found, err := find(root, "Cargo.toml")
	if err != nil {
		return nil, nil, err
	}
	for _, dir := range found {
		if dir != "." && !members[dir] {
			problems = append(problems, fmt.Sprintf("%s: has a Cargo.toml that the workspace does not list as a member", dir))
		}
	}
	return libs, problems, nil
}
