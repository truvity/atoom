package ccid

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The tests of this package never wait for the machine's clock. The Reader takes its clock as an option (Options.Clock), the tests give it a
// virtual one (usbccidtest.Clock), and the fake Port moves that clock where time passes; so a test that sleeps, or that waits on a timer, is
// a test whose outcome depends on how fast the machine is, and one such test already failed on a slow runner. This file keeps the sleeps
// from coming back: it reads the source of every _test.go file of the package and fails on a use of a function of the time package that
// waits.

// waitingTimeFuncs are the functions of the time package that make a test wait for real time, or hand it a channel or a callback that real
// time fires. AfterFunc is on the list with the four that were asked for: it is the same timer with a callback.
var waitingTimeFuncs = []string{"Sleep", "After", "AfterFunc", "Tick", "NewTimer", "NewTicker"}

// timeWaits parses one Go source file and returns, as "file:line:col: use", every use of a waiting function of the time package in it. The
// package is found under any name it is imported by: "time", a name of the file's own ("import tm "time""), or none at all (a dot import),
// and a name that no import gives it is not the time package (a local variable called time is not looked at). A qualified use is reported
// wherever it stands, called or not (a function value that is kept is a sleep that is called later); with a dot import only a call of the
// bare name is reported, because the bare name After or Tick is also the name of a field or of a method that has nothing to do with time.
// The cost is a false report in a file that dot-imports time and declares a function of its own with one of these names; none does.
func timeWaits(filename string, src []byte) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{} // the names the file gives the time package
	dot := false
	for _, imp := range f.Imports {
		if path, err := strconv.Unquote(imp.Path.Value); err != nil || path != "time" {
			continue
		}
		switch {
		case imp.Name == nil:
			names["time"] = true
		case imp.Name.Name == ".":
			dot = true
		case imp.Name.Name == "_":
			// imported for its effects only: no name to use it by
		default:
			names[imp.Name.Name] = true
		}
	}
	var found []string
	report := func(pos token.Pos, use string) {
		found = append(found, fmt.Sprintf("%s: %s", fset.Position(pos), use))
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok && names[id.Name] && slices.Contains(waitingTimeFuncs, x.Sel.Name) {
				report(x.Pos(), id.Name+"."+x.Sel.Name)
			}
		case *ast.CallExpr:
			if id, ok := x.Fun.(*ast.Ident); ok && dot && slices.Contains(waitingTimeFuncs, id.Name) {
				report(x.Pos(), id.Name+" (dot import of time)")
			}
		}
		return true
	})
	return found, nil
}

// No _test.go file of this package, whatever its build constraints, uses a waiting function of the time package. (A test may still keep a
// deadline for itself, with t.Deadline or a context with a generous timeout, to stop one that hangs: neither is in this list.)
func TestNoTestOfThePackageWaitsOnTheClock(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(files, "nosleep_test.go") || !slices.Contains(files, "clock_test.go") || len(files) < 10 {
		t.Fatalf("the scan found %d files: %v; it does not read the package", len(files), files)
	}
	for _, name := range files {
		src, err := os.ReadFile(name) //nolint:gosec // a file of this folder, named by the glob above
		if err != nil {
			t.Fatal(err)
		}
		found, err := timeWaits(name, src)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		for _, use := range found {
			t.Errorf("%s: a test must not wait for time; move a virtual clock (Options.Clock, usbccidtest.Clock) instead", use)
		}
	}
}

// The scanner reports what breaks the rule, under every name of the time package, and nothing else.
func TestTimeWaitsScanner(t *testing.T) {
	const head = "package p\n\nimport \"time\"\n\n"
	breaking := []struct {
		name string
		src  string
		want []string // the uses reported, in order
	}{
		{"Sleep", head + "func f() { time.Sleep(time.Second) }\n", []string{"time.Sleep"}},
		{"After in a select", head + "func f(c chan int) {\n\tselect {\n\tcase <-c:\n\tcase <-time.After(1):\n\t}\n}\n", []string{"time.After"}},
		{"AfterFunc", head + "func f() { time.AfterFunc(1, func() {}) }\n", []string{"time.AfterFunc"}},
		{"Tick", head + "func f() { <-time.Tick(1) }\n", []string{"time.Tick"}},
		{"NewTimer", head + "func f() { _ = time.NewTimer(1) }\n", []string{"time.NewTimer"}},
		{"NewTicker", head + "func f() { _ = time.NewTicker(1) }\n", []string{"time.NewTicker"}},
		{"in a goroutine and a closure", head + "func f() {\n\tgo func() {\n\t\tdefer func() { time.Sleep(1) }()\n\t}()\n}\n", []string{"time.Sleep"}},
		{"in a hook of a struct", head + "var h = struct{ f func() }{f: func() { time.Sleep(1) }}\n", []string{"time.Sleep"}},
		{"kept as a value", head + "var sleep = time.Sleep\n", []string{"time.Sleep"}},
		{"under another name", "package p\n\nimport tm \"time\"\n\nfunc f() { tm.Sleep(1) }\n", []string{"tm.Sleep"}},
		{"under another name, a timer", "package p\n\nimport clock \"time\"\n\nfunc f() { _ = clock.NewTimer(1) }\n", []string{"clock.NewTimer"}},
		{"through a dot import", "package p\n\nimport . \"time\"\n\nfunc f() { Sleep(Second) }\n", []string{"Sleep (dot import of time)"}},
		{"through a dot import, After", "package p\n\nimport . \"time\"\n\nfunc f() { <-After(1) }\n", []string{"After (dot import of time)"}},
		{"in a grouped import", "package p\n\nimport (\n\t\"fmt\"\n\t\"time\"\n)\n\nfunc f() { fmt.Println(); time.Sleep(1) }\n", []string{"time.Sleep"}},
		{"two of them", head + "func f() {\n\ttime.Sleep(1)\n\t<-time.After(2)\n}\n", []string{"time.Sleep", "time.After"}},
	}
	for _, c := range breaking {
		t.Run("breaks/"+c.name, func(t *testing.T) {
			got, err := timeWaits("x_test.go", []byte(c.src))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("reported %q, want the uses %q", got, c.want)
			}
			for i := range got {
				if !strings.HasPrefix(got[i], "x_test.go:") || !strings.HasSuffix(got[i], ": "+c.want[i]) {
					t.Errorf("report %d is %q, want it to end in %q", i, got[i], c.want[i])
				}
			}
		})
	}
	clean := []struct {
		name string
		src  string
	}{
		{"durations and the clock's reading", head + "const d = 5 * time.Second\n\nfunc f() (time.Time, time.Duration) { return time.Now(), time.Since(time.Now()) }\n"},
		{"After as a method of a time", head + "func f(a, b time.Time) bool { return a.After(b) }\n"},
		{"a method named Sleep or After of a type of the package", head + "type c struct{}\n\nfunc (c) Sleep()   {}\nfunc (c) After()   {}\nfunc f(x c)       { x.Sleep(); x.After() }\n"},
		{"the names in a string and a comment", head + "// time.Sleep and time.After are not called here\nvar s = \"time.Sleep(1)\"\n"},
		{"a package of another name that has a Sleep", "package p\n\nimport clock \"example.com/clock\"\n\nfunc f() { clock.Sleep(1) }\n"},
		{"a name that is no import", "package p\n\nfunc f(time interface{ Sleep() }) { time.Sleep() }\n"},
		{"a blank import", "package p\n\nimport _ \"time\"\n\nfunc f() {}\n"},
		{"a dot import that calls other functions", "package p\n\nimport . \"time\"\n\nfunc f(a, b Time) bool { _ = Now(); _ = Since(a); return a.After(b) }\n"},
		{"a dot import and a field named After", "package p\n\nimport . \"time\"\n\ntype s struct{ After Duration }\n\nvar v = s{After: Second}\n"},
		{"a file with no import at all", "package p\n\nfunc f() {}\n"},
	}
	for _, c := range clean {
		t.Run("passes/"+c.name, func(t *testing.T) {
			got, err := timeWaits("x_test.go", []byte(c.src))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 0 {
				t.Errorf("reported %q in a source that waits for nothing", got)
			}
		})
	}
	t.Run("a source that does not parse is an error", func(t *testing.T) {
		if got, err := timeWaits("x_test.go", []byte("package p\n\nfunc f( {")); err == nil {
			t.Errorf("no error, reported %q", got)
		}
	})
}
