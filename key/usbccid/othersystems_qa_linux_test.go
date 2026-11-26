//go:build linux && !android && (amd64 || arm64)

package usbccid

import (
	"go/ast"
	gobuild "go/build"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// F. The build for the other systems (everything but linux on amd64 and arm64, riscv64 and the 32-bit linux ones included), held from Linux. The file notsupported_test.go runs only where the build is not Linux (a
// macOS run is the only way to see it execute), so these tests read the sources and the build constraints instead: which files
// each system builds, that the files for other systems import nothing from x/sys, that the entry points of device_other.go are the
// same as those of the Linux files, that each of them only returns ErrNotSupported, and that notsupported_test.go names every one.

func TestQAFilesPerSystem(t *testing.T) {
	shared := []string{"candidate.go", "doc.go", "errors.go", "port.go"}
	for _, sys := range []struct{ goos, goarch string }{{"darwin", "arm64"}, {"darwin", "amd64"}, {"windows", "amd64"}, {"freebsd", "amd64"}, {"linux", "arm64"}, {"linux", "amd64"}, {"linux", "riscv64"}, {"linux", "386"}, {"linux", "arm"}, {"linux", "ppc64le"}, {"linux", "s390x"}, {"linux", "loong64"}, {"android", "arm64"}, {"android", "amd64"}} {
		for _, cgo := range []bool{false, true} {
			ctx := gobuild.Default
			ctx.GOOS, ctx.GOARCH, ctx.CgoEnabled = sys.goos, sys.goarch, cgo
			pkg, err := ctx.ImportDir(".", 0)
			if err != nil {
				t.Errorf("%s/%s cgo=%v: %v", sys.goos, sys.goarch, cgo, err)
				continue
			}
			if len(pkg.CgoFiles) != 0 || slices.Contains(pkg.Imports, "C") {
				t.Errorf("%s/%s: cgo files %v, imports C: the module must be pure Go", sys.goos, sys.goarch, pkg.CgoFiles)
			}
			var want, wantTests []string
			if sys.goos == "linux" && (sys.goarch == "amd64" || sys.goarch == "arm64") { // android satisfies the linux tag and gets the stub
				want = append(slices.Clone(shared), "device_linux.go", "discover_linux.go", "layout_linux.go")
				if !slices.Contains(pkg.Imports, "golang.org/x/sys/unix") {
					t.Errorf("linux imports %v", pkg.Imports)
				}
				if slices.Contains(pkg.GoFiles, "device_other.go") || slices.Contains(pkg.TestGoFiles, "notsupported_test.go") {
					t.Errorf("linux builds the files for other systems: %v %v", pkg.GoFiles, pkg.TestGoFiles)
				}
			} else {
				want = append(slices.Clone(shared), "device_other.go")
				wantTests = []string{"notsupported_test.go"}
				for _, imp := range pkg.Imports {
					if imp != "errors" && imp != "time" {
						t.Errorf("%s: the package imports %q: nothing from x/sys or any other module may be needed off linux/amd64 and linux/arm64", sys.goos, imp)
					}
				}
				for _, f := range pkg.TestGoFiles {
					if strings.Contains(f, "_linux_test.go") {
						t.Errorf("%s: a Linux test file is built: %s", sys.goos, f)
					}
				}
			}
			slices.Sort(want)
			got := slices.Clone(pkg.GoFiles)
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Errorf("%s/%s: builds %v, want %v", sys.goos, sys.goarch, got, want)
			}
			if wantTests != nil {
				gotTests := slices.Clone(pkg.TestGoFiles)
				slices.Sort(gotTests)
				if !slices.Equal(gotTests, wantTests) {
					t.Errorf("%s: test files %v, want %v", sys.goos, gotTests, wantTests)
				}
			}
			if len(pkg.XTestGoFiles) != 0 {
				t.Errorf("unexpected external test files %v", pkg.XTestGoFiles)
			}
		}
	}
}

// sigs lists the exported functions and methods of a file as "Recv.Name(param types)(result types)", without parameter names.
func sigs(t *testing.T, fset *token.FileSet, files ...string) []string {
	t.Helper()
	var out []string
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || !fd.Name.IsExported() {
				continue
			}
			recv := ""
			if fd.Recv != nil {
				recv = types.ExprString(fd.Recv.List[0].Type) + "."
				if !strings.Contains(recv, "Device") && !strings.Contains(recv, "Candidate") {
					continue
				}
			}
			out = append(out, recv+fd.Name.Name+"("+fieldTypes(fd.Type.Params)+")("+fieldTypes(fd.Type.Results)+")")
		}
	}
	slices.Sort(out)
	return out
}

func fieldTypes(l *ast.FieldList) string {
	if l == nil {
		return ""
	}
	var parts []string
	for _, f := range l.List {
		n := max(len(f.Names), 1)
		for range n {
			parts = append(parts, types.ExprString(f.Type))
		}
	}
	return strings.Join(parts, ",")
}

// Every source file of the real implementation (named _linux.go) carries the one constraint, so that a file added later cannot make
// the package fail to build on linux/riscv64; every test file for linux carries it too, so that the test binary builds there.
func TestQAEveryLinuxFileCarriesTheConstraint(t *testing.T) {
	const realBuild = "//go:build linux && !android && (amd64 || arm64)\n"
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range files {
		name := f.Name()
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		b, err := os.ReadFile(name) //nolint:gosec // a source file of this package
		if err != nil {
			t.Fatal(err)
		}
		first, _, _ := strings.Cut(string(b), "\n")
		first += "\n"
		switch {
		case strings.HasSuffix(name, "_linux.go") || strings.HasSuffix(name, "_linux_test.go"):
			n++
			if first != realBuild {
				t.Errorf("%s starts with %q, want %q", name, first, realBuild)
			}
		case name == "device_other.go" || name == "notsupported_test.go":
			if first != "//go:build !(linux && !android && (amd64 || arm64))\n" {
				t.Errorf("%s starts with %q", name, first)
			}
		default:
			if strings.HasPrefix(first, "//go:build") {
				// A file with a constraint of its own must be one of the two kinds above, or be a test of the real code.
				if first != realBuild {
					t.Errorf("%s has an unexpected constraint %q", name, first)
				}
			}
		}
	}
	if n < 5 {
		t.Errorf("only %d linux files found", n)
	}
}

func TestQAOtherSystemsOfferTheSameEntryPointsAsLinux(t *testing.T) {
	fset := token.NewFileSet()
	linux := sigs(t, fset, "device_linux.go", "discover_linux.go")
	other := sigs(t, fset, "device_other.go")
	if !slices.Equal(linux, other) {
		t.Errorf("the entry points differ:\nlinux: %v\nother: %v", linux, other)
	}
	for _, want := range []string{"Open(string)(*Device,error)", "Discover(string,string,Filter)([]Candidate,error)", "DiscoverHost(Filter)([]Candidate,error)",
		"*Device.Close()(error)", "*Device.BulkIn(uint8,[]byte,time.Duration)(int,error)", "*Device.Claim(int)(error)"} {
		if !slices.Contains(other, want) {
			t.Errorf("device_other.go lacks %s: %v", want, other)
		}
	}
	// Both files declare the type and the compile-time proof that it is a Port.
	for _, name := range []string{"device_linux.go", "device_other.go"} {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		hasType, hasPort := false, false
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, s := range gd.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					hasType = hasType || s.Name.Name == "Device"
				case *ast.ValueSpec:
					hasPort = hasPort || (len(s.Names) == 1 && s.Names[0].Name == "_" && types.ExprString(s.Type) == "Port")
				}
			}
		}
		if !hasType || !hasPort {
			t.Errorf("%s: type Device %v, assertion of Port %v", name, hasType, hasPort)
		}
	}
}

// Every entry point of device_other.go is a one-line return of ErrNotSupported (with zero values before it), and nothing else: no
// state, no system call, nothing that could differ between systems. The exceptions are Release and Close of a Device, which are a
// one-line `return nil` (a Device of these systems holds nothing).
func TestQAOtherSystemsEntryPointsReturnErrNotSupportedExceptReleaseAndClose(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "device_other.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	src, _ := os.ReadFile("device_other.go")
	if !strings.HasPrefix(string(src), "//go:build !(linux && !android && (amd64 || arm64))\n") {
		t.Errorf("device_other.go starts with %q, want the constraint //go:build !(linux && !android && (amd64 || arm64))", strings.SplitN(string(src), "\n", 2)[0])
	}
	n := 0
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || !fd.Name.IsExported() {
			continue
		}
		n++
		if fd.Body == nil || len(fd.Body.List) != 1 {
			t.Errorf("%s: body is not a single statement", fd.Name.Name)
			continue
		}
		ret, ok := fd.Body.List[0].(*ast.ReturnStmt)
		if !ok || len(ret.Results) == 0 {
			t.Errorf("%s: not a return", fd.Name.Name)
			continue
		}
		if fd.Recv != nil && (fd.Name.Name == "Release" || fd.Name.Name == "Close") {
			if id, ok := ret.Results[0].(*ast.Ident); !ok || id.Name != "nil" || len(ret.Results) != 1 {
				t.Errorf("%s: returns %s, want nil", fd.Name.Name, types.ExprString(ret.Results[0]))
			}
			continue
		}
		last, ok := ret.Results[len(ret.Results)-1].(*ast.Ident)
		if !ok || last.Name != "ErrNotSupported" {
			t.Errorf("%s: returns %s, want ErrNotSupported last", fd.Name.Name, types.ExprString(ret.Results[len(ret.Results)-1]))
		}
		for _, r := range ret.Results[:len(ret.Results)-1] {
			if id, ok := r.(*ast.Ident); ok && (id.Name == "nil") {
				continue
			}
			if lit, ok := r.(*ast.BasicLit); ok && lit.Value == "0" {
				continue
			}
			t.Errorf("%s: result %s before ErrNotSupported is not nil or 0", fd.Name.Name, types.ExprString(r))
		}
	}
	if n != 9 { // Open, Descriptors, Claim, Release, BulkOut, BulkIn, Close, Discover, DiscoverHost (Candidate.Open is in candidate.go)
		t.Errorf("%d exported entry points in device_other.go, want 9", n)
	}
}

// The test that runs off Linux (notsupported_test.go) must call each of the entry points; read here because it cannot run here. It
// also must be built only off Linux.
func TestQANotSupportedTestNamesEveryEntryPoint(t *testing.T) {
	fset := token.NewFileSet()
	src, err := os.ReadFile("notsupported_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(src), "//go:build !(linux && !android && (amd64 || arm64))\n") {
		t.Errorf("notsupported_test.go is built where it should not be: %q", strings.SplitN(string(src), "\n", 2)[0])
	}
	f, err := parser.ParseFile(fset, "notsupported_test.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			used[x.Name] = true
		case *ast.SelectorExpr:
			used[x.Sel.Name] = true
		}
		return true
	})
	for _, name := range []string{"Open", "Discover", "DiscoverHost", "Descriptors", "Claim", "Release", "BulkOut", "BulkIn", "Close", "ErrNotSupported", "Candidate", "Filter", "Port"} {
		if !used[name] {
			t.Errorf("notsupported_test.go never mentions %s", name)
		}
	}
}

// The stub is run here. notsupported_test.go is built only off linux/amd64 and linux/arm64, so no Linux run of the module executes it;
// this test copies the files of the stub into a scratch module, changes their constraints to `linux` (the stub and its test are then
// the only files there), and runs `go test` on them. It shows the behaviour of the stub on this system; what only a macOS run can show
// is that the same files build and pass with GOOS=darwin (the compile checks of the test binary cover the build).
func TestQAStubRunsOnThisSystem(t *testing.T) {
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go tool on the path")
	}
	dir := t.TempDir()
	const stubTag = "//go:build !(linux && !android && (amd64 || arm64))\n"
	for _, name := range []string{"go.mod", "candidate.go", "device_other.go", "doc.go", "errors.go", "port.go", "notsupported_test.go"} {
		b, err := os.ReadFile(name) //nolint:gosec // a file of this package
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(string(b), stubTag) {
			b = append([]byte("//go:build linux\n"), b[len(stubTag):]...)
		} else if name == "device_other.go" || name == "notsupported_test.go" {
			t.Fatalf("%s does not start with the constraint of the stub", name)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil { //nolint:gosec // a fixed list of names of this package, under t.TempDir()
			t.Fatal(err)
		}
	}
	cmd := exec.Command(goTool, "test", "-count=1", "-v", ".") //nolint:gosec // the go tool of this machine, on files this test wrote
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=", "GOPROXY=off")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: TestNotSupported") {
		t.Errorf("the stub test does not pass here: %v\n%s", err, out)
	}
}
