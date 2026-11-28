package pivtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// allowedIdentifiers lists every package that a source file of the package may import, and of each the identifiers that it may select: what
// the sources use today and no more. A new import, or a new identifier of one, is a deliberate edit of this table, and a reviewer sees it.
// Nothing here reaches a file, a device, the network, the clock, randomness, a stream of the process or a goroutine: of fmt only the text
// functions (Sprintf, Appendf) and the State that Format is given (not Print*, Fprint*, Scan*, which touch a stream), of sync only the Mutex (not WaitGroup,
// whose Go starts a goroutine with no go statement), and of time only the duration (not Now, Sleep, After, Since, Until, NewTimer, Tick,
// LoadLocation and the type Time).
var allowedIdentifiers = map[string][]string{
	"bytes":           {"Equal", "TrimRight"},
	"encoding/binary": {"BigEndian"},
	"fmt":             {"Appendf", "Sprintf", "State"},
	"sync":            {"Mutex"},
	"time":            {"Duration", "Nanosecond", "Microsecond", "Millisecond", "Second", "Minute", "Hour"},
}

// allowedImports is the list of the packages of allowedIdentifiers, and durationNames the identifiers of time.
var (
	allowedImports = slices.Sorted(maps.Keys(allowedIdentifiers))
	durationNames  = allowedIdentifiers["time"]
)

// devicePrefixes start a string that names a path of the machine.
var devicePrefixes = []string{"/dev", "/sys", "/proc", "/run"}

// sourceProblems lists what a source file of the package must not hold. It is an allow-list: the imports are those of allowedIdentifiers,
// each at most once, with no dot import, no blank import and no "C"; of each package only the identifiers that the table lists; no go
// statement, no channel and no select; no string that names a path of the machine.
//
// The import list is the real guard: no allowed package can open a file, a device or a socket, so no path can be used, however it is built.
// The check of the strings only catches the plain forms (a path can be built in pieces, or from bytes, and it is not caught); it is there to
// make a plain path in a later change a failing test, and it is not a proof.
func sourceProblems(name string, src []byte) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var out []string
	at := func(n ast.Node) string { return " at " + fset.Position(n.Pos()).String() }
	seen := map[string]bool{}
	local := map[string]string{} // the name an import is used by, and its path
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if _, ok := allowedIdentifiers[p]; !ok {
			out = append(out, "imports "+p+", which is not in the list of allowed imports"+at(imp))
		} else if seen[p] {
			out = append(out, "imports "+p+" twice"+at(imp))
		}
		seen[p] = true
		use := path.Base(p)
		if imp.Name != nil {
			use = imp.Name.Name
		}
		if use == "." || use == "_" {
			out = append(out, "a "+use+" import of "+p+at(imp))
			continue
		}
		local[use] = p
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.BasicLit:
			if n.Kind != token.STRING {
				break
			}
			s, _ := strconv.Unquote(n.Value)
			for _, pre := range devicePrefixes {
				if strings.HasPrefix(s, pre) || strings.Contains(s, pre+"/") {
					out = append(out, "a string that names a path of the machine ("+strconv.Quote(s)+")"+at(n))
					break
				}
			}
		case *ast.SelectorExpr:
			if id, ok := n.X.(*ast.Ident); ok {
				if p, imported := local[id.Name]; imported {
					if allowed, listed := allowedIdentifiers[p]; listed && !slices.Contains(allowed, n.Sel.Name) {
						out = append(out, p+"."+n.Sel.Name+", which is not in the list of allowed identifiers"+at(n))
					}
				}
			}
		case *ast.GoStmt:
			out = append(out, "a go statement"+at(n))
		case *ast.SelectStmt:
			out = append(out, "a select statement"+at(n))
		case *ast.ChanType:
			out = append(out, "a channel type"+at(n))
		}
		return true
	})
	return out, nil
}

// folderProblems lists the files of the package's folder that are not Go sources, a README or README.md (exact names) or testdata: an assembly, C or object file is a
// way past every check of the Go sources.
func folderProblems(names []string) []string {
	var out []string
	for _, n := range names {
		switch {
		case strings.HasSuffix(n, ".go"), n == "testdata", n == "README", n == "README.md":
		default:
			out = append(out, "the folder holds "+n+", which is not a Go source")
		}
	}
	return out
}

// TestNoMachineClockNoSleepNoDevicePathInTheSources reads the package's own sources: the card must not be able to reach the clock, a device,
// a goroutine or the outside, whatever a later change does.
func TestNoMachineClockNoSleepNoDevicePathInTheSources(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	for _, p := range folderProblems(names) {
		t.Error(p)
	}
	checked := 0
	for _, name := range names {
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++
		src, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatal(err)
		}
		problems, err := sourceProblems(name, src)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range problems {
			t.Errorf("%s: %s", name, p)
		}
	}
	if checked < 8 {
		t.Errorf("the check read only %d source files", checked)
	}
}

// brokenSources each break one rule of the allow-list. Every one must be found.
var brokenSources = map[string]string{
	"os":                              "package x\nimport \"os\"\nvar _ = os.Args\n",
	"os/exec":                         "package x\nimport \"os/exec\"\nvar _ = exec.Command\n",
	"net":                             "package x\nimport \"net\"\nvar _ = net.Dial\n",
	"unsafe":                          "package x\nimport \"unsafe\"\nvar _ = unsafe.Sizeof(0)\n",
	"x/sys":                           "package x\nimport \"golang.org/x/sys/unix\"\nvar _ = unix.Getpid\n",
	"a third-party clock":             "package x\nimport \"example.com/clock\"\nvar f = clock.Now\n",
	"io/ioutil":                       "package x\nimport \"io/ioutil\"\nvar f = ioutil.ReadFile\n",
	"path/filepath":                   "package x\nimport \"path/filepath\"\nvar f = filepath.Glob\n",
	"log":                             "package x\nimport \"log\"\nfunc f() { log.Println(1) }\n",
	"context.WithTimeout":             "package x\nimport \"context\"\nfunc f() { _, _ = context.WithTimeout(context.Background(), 1) }\n",
	"cgo":                             "package x\nimport \"C\"\n",
	"math/rand":                       "package x\nimport \"math/rand\"\nvar _ = rand.Int\n",
	"crypto/rand":                     "package x\nimport \"crypto/rand\"\nvar _ = rand.Reader\n",
	"testing":                         "package x\nimport \"testing\"\nvar _ testing.TB\n",
	"a dot import of time":            "package x\nimport . \"time\"\nfunc f() { _ = Now() }\n",
	"a blank import of fmt":           "package x\nimport _ \"fmt\"\n",
	"time imported twice":             "package x\nimport (\n\t\"time\"\n\tt \"time\"\n)\nfunc f() { _ = time.Now(); _ = t.Duration(0) }\n",
	"time.Now":                        "package x\nimport \"time\"\nvar _ = time.Now\n",
	"time.Now under an alias":         "package x\nimport clock \"time\"\nfunc f() { _ = clock.Now() }\n",
	"time.Sleep":                      "package x\nimport \"time\"\nfunc f() { time.Sleep(1) }\n",
	"time.After":                      "package x\nimport \"time\"\nfunc f() { _ = time.After(1) }\n",
	"time.Since":                      "package x\nimport \"time\"\nfunc f() { _ = time.Since(time.Time{}) }\n",
	"time.Until":                      "package x\nimport \"time\"\nfunc f() { _ = time.Until(time.Time{}) }\n",
	"time.NewTimer":                   "package x\nimport \"time\"\nfunc f() { _ = time.NewTimer(1) }\n",
	"time.Tick":                       "package x\nimport \"time\"\nfunc f() { _ = time.Tick(1) }\n",
	"time.AfterFunc":                  "package x\nimport \"time\"\nfunc f() { _ = time.AfterFunc(1, nil) }\n",
	"time.LoadLocation":               "package x\nimport \"time\"\nvar f = time.LoadLocation\n",
	"time.Time (a type of the clock)": "package x\nimport \"time\"\nvar t time.Time\n",
	"fmt.Print":                       "package x\nimport \"fmt\"\nfunc f() { fmt.Print(1) }\n",
	"fmt.Println":                     "package x\nimport \"fmt\"\nfunc f() { fmt.Println(1) }\n",
	"fmt.Printf":                      "package x\nimport \"fmt\"\nfunc f() { fmt.Printf(\"x\") }\n",
	"fmt.Fprintf":                     "package x\nimport \"fmt\"\nfunc f(w fmt.State) { _, _ = fmt.Fprintf(w, \"x\") }\n",
	"fmt.Fprintln":                    "package x\nimport \"fmt\"\nfunc f(w fmt.State) { _, _ = fmt.Fprintln(w, 1) }\n",
	"fmt.Scan":                        "package x\nimport \"fmt\"\nfunc f() { var s string; _, _ = fmt.Scan(&s) }\n",
	"fmt.Scanln":                      "package x\nimport \"fmt\"\nfunc f() { var s string; _, _ = fmt.Scanln(&s) }\n",
	"fmt.Sscan under an alias":        "package x\nimport f \"fmt\"\nfunc g() { var s string; _, _ = f.Sscan(\"1\", &s) }\n",
	"sync.WaitGroup":                  "package x\nimport \"sync\"\nfunc f() { var wg sync.WaitGroup; wg.Go(func() {}) }\n",
	"sync.Once":                       "package x\nimport \"sync\"\nvar _ sync.Once\n",
	"sync/atomic":                     "package x\nimport \"sync/atomic\"\nvar _ atomic.Int32\n",
	"bytes.NewReader":                 "package x\nimport \"bytes\"\nvar _ = bytes.NewReader\n",
	"binary.Read":                     "package x\nimport \"encoding/binary\"\nvar _ = binary.Read\n",
	"a go statement":                  "package x\nfunc f() { go func() {}() }\n",
	"a channel type":                  "package x\nvar c chan int\n",
	"a select statement":              "package x\nfunc f(c chan int) {\n\tselect {\n\tcase <-c:\n\tdefault:\n\t}\n}\n",
	"/dev/bus":                        "package x\nvar p = \"/dev/bus/usb/001/002\"\n",
	"/dev/ and bus/usb apart":         "package x\nvar p = \"/dev/\" + \"bus/usb\"\n",
	"/sys/bus":                        "package x\nvar p = \"/sys/bus/usb/devices\"\n",
	"a raw string":                    "package x\nvar p = `/dev/bus/usb/001/002`\n",
	"an escaped slash":                "package x\nvar p = \"\\x2fdev/bus/usb\"\n",
	"a reader":                        "package x\nvar p = \"/dev/hidraw0\"\n",
	"a serial device":                 "package x\nvar p = \"/dev/ttyUSB0\"\n",
	"the smart-card socket":           "package x\nvar p = \"/run/pcscd/pcscd.comm\"\n",
	"/proc":                           "package x\nvar p = \"/proc/self/mem\"\n",
	"a path inside a text":            "package x\nvar p = \"open /dev/bus/usb/001/002 failed\"\n",
}

// TestTheSourceCheckCatchesWhatItLooksFor holds the check to account: each source that breaks a rule is found to break it, a clean one is not,
// and a folder with an assembly or C file is refused.
func TestTheSourceCheckCatchesWhatItLooksFor(t *testing.T) {
	for name, src := range brokenSources {
		problems, err := sourceProblems("x.go", []byte(src))
		if err != nil {
			t.Errorf("%s: %v", name, err)
		} else if len(problems) == 0 {
			t.Errorf("not caught: %s", name)
		}
	}
	for name, src := range map[string]string{
		"time.Duration and its constants":                  "package x\nimport \"time\"\nvar d = 5 * time.Second\nfunc f(x time.Duration) time.Duration { return x + time.Hour }\n",
		"an import under another name":                     "package x\nimport clock \"time\"\nfunc f(d clock.Duration) clock.Duration { return d }\n",
		"every allowed import":                             "package x\nimport (\n\t\"bytes\"\n\t\"encoding/binary\"\n\t\"fmt\"\n\t\"sync\"\n\t\"time\"\n)\nvar _ = bytes.Equal\nvar _ = binary.BigEndian\nvar _ = fmt.Sprintf\nvar _ sync.Mutex\nvar _ time.Duration\n",
		"strings that merely say dev":                      "package x\nvar a, b = \"development bus\", \"/usr/bus/x\"\n",
		"a field and a function by the names of the clock": "package x\ntype c struct{ Now int }\nfunc Sleep() {}\nfunc f(x c) int { Sleep(); return x.Now }\n",
		"a variable called time that is not the package":   "package x\nfunc f(time struct{ Now int }) int { return time.Now }\n",
	} {
		problems, err := sourceProblems("x.go", []byte(src))
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if len(problems) != 0 {
			t.Errorf("%s: %q", name, problems)
		}
	}
	for _, name := range []string{"x.s", "x.S", "x.c", "x.h", "x.syso", "x.cc", "x.o", "README.s", "README.S", "README.c", "README.h", "README.syso", "README.cc",
		"README.go.s", "README_amd64.s", "README.o", "README.txt", "readme.md", "README.md.s", "testdata.s", "Testdata"} {
		if len(folderProblems([]string{"card.go", "card_test.go", name})) != 1 {
			t.Errorf("a folder with %s is not refused", name)
		}
	}
	if p := folderProblems([]string{"card.go", "card_test.go", "README", "README.md", "testdata"}); len(p) != 0 {
		t.Errorf("a clean folder is refused: %q", p)
	}
	if _, err := sourceProblems("x.go", []byte("package x\nfunc (")); err == nil {
		t.Error("a source that does not parse was accepted")
	}
}
