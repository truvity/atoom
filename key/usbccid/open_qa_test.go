//go:build linux && !android && (amd64 || arm64)

package usbccid

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Open checks the node before it opens it. Every path here is under t.TempDir(); the only numbers taken from a real device are the
// major and minor of /dev/null (1 and 3), used as numbers in a made-up stat result: no device node is ever opened.

// openFDs counts the open descriptors of the process.
func openFDs(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("no /proc/self/fd")
	}
	return len(ents)
}

// within runs f and fails the test, without waiting for f, when it does not end in time: nothing here may block on a FIFO.
func within(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s blocked", what)
	}
}

// qaNodes makes the paths that an Open can be given: each is something that is not a USB node.
func qaNodes(t *testing.T) (map[string]string, string) {
	t.Helper()
	dir := t.TempDir()
	reg := filepath.Join(dir, "regular")
	if err := os.WriteFile(reg, qaDefaultDesc(), 0o600); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(dir, "unreadable")
	if err := os.WriteFile(locked, qaDefaultDesc(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{"link to a regular file": reg, "link to dev-null": "/dev/null", "link to a directory": sub, "link to a fifo": fifo, "dangling link": filepath.Join(dir, "gone"),
		"link to a link": filepath.Join(dir, "link to a regular file")}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	nodes := map[string]string{"regular file": reg, "unreadable regular file": locked, "directory": sub, "fifo": fifo}
	for name := range links {
		nodes[name] = filepath.Join(dir, name)
	}
	return nodes, dir
}

// Each of them is refused through the exported Open and through Candidate.Open with ErrNotUSBNode, quickly (a FIFO is looked at with
// O_PATH, never opened), and no descriptor is left open by a loop of refusals. A missing path is the documented error of the file
// system.
func TestQAOpenRefusesWhatIsNotAUSBNode(t *testing.T) {
	nodes, dir := qaNodes(t)
	before := openFDs(t)
	for name, p := range nodes {
		within(t, "Open of "+name, func() {
			d, err := Open(p)
			if !errors.Is(err, ErrNotUSBNode) || d != nil {
				t.Errorf("%s: Open: %v, %v", name, d, err)
			}
			d, err = Candidate{NodePath: p}.Open()
			if !errors.Is(err, ErrNotUSBNode) || d != nil {
				t.Errorf("%s: Candidate.Open: %v, %v", name, d, err)
			}
		})
	}
	for _, p := range []string{filepath.Join(dir, "missing"), filepath.Join(dir, "regular", "below-a-file"), "", filepath.Join(dir, "missing", "deeper")} {
		d, err := Open(p)
		if d != nil || err == nil || errors.Is(err, ErrNotUSBNode) {
			t.Errorf("Open(%q): %v, %v", p, d, err)
		}
		if p != "" && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, unix.ENOTDIR) {
			t.Errorf("Open(%q): %v, want the error of the file system", p, err)
		}
	}
	for range 200 {
		for _, p := range nodes {
			_, _ = Open(p)
		}
		_, _ = Open(filepath.Join(dir, "missing"))
	}
	if after := openFDs(t); after != before {
		t.Errorf("%d descriptors open before the refusals, %d after", before, after)
	}
}

// A directory the user may not search: the refusal wraps the errno, and errors.Is(err, os.ErrPermission) tells it.
func TestQAOpenPermissionDenied(t *testing.T) {
	if unix.Geteuid() == 0 {
		t.Skip("root may search any directory")
	}
	dir := t.TempDir()
	closed := filepath.Join(dir, "closed")
	if err := os.Mkdir(closed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(closed, "node"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(closed, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(closed, 0o700) }) //nolint:gosec // a directory needs the execute bit to be removed
	_, err := Open(filepath.Join(closed, "node"))
	var pe *os.PathError
	if !errors.Is(err, os.ErrPermission) || !errors.Is(err, unix.EACCES) || !errors.As(err, &pe) || errors.Is(err, ErrNotUSBNode) {
		t.Errorf("a node in a directory that cannot be searched: %v", err)
	}
}

// checkNode from made-up stat results: only a character device with major 189 is a USB node. The numbers of /dev/null (1, 3) are
// used as numbers and nothing is opened.
func TestQACheckNode(t *testing.T) {
	const minor = 3
	for major := uint32(0); major < 400; major++ {
		st := &unix.Stat_t{Mode: unix.S_IFCHR | 0o600, Rdev: unix.Mkdev(major, minor)}
		if got := checkNode("/made/up", st, true) == nil; got != (major == 189) {
			t.Errorf("character device of major %d accepted: %v", major, got)
		}
		if err := checkNode("/made/up", st, true); err != nil && !errors.Is(err, ErrNotUSBNode) {
			t.Errorf("major %d: %v", major, err)
		}
	}
	for _, m := range []uint32{189, 1 << 12, 1<<32 - 1} { // other minors and the largest majors
		for _, mi := range []uint32{0, 1, 127, 128, 255, 1 << 20, 1<<32 - 1} {
			st := &unix.Stat_t{Mode: unix.S_IFCHR, Rdev: unix.Mkdev(m, mi)}
			if got := checkNode("/made/up", st, true) == nil; got != (m == 189) {
				t.Errorf("major %d minor %d accepted: %v", m, mi, got)
			}
		}
	}
	for name, mode := range map[string]uint32{"block": unix.S_IFBLK, "fifo": unix.S_IFIFO, "socket": unix.S_IFSOCK, "directory": unix.S_IFDIR, "link": unix.S_IFLNK, "regular": unix.S_IFREG, "none": 0} {
		for _, major := range []uint32{189, 1, 0} {
			st := &unix.Stat_t{Mode: mode | 0o666, Rdev: unix.Mkdev(major, 1)}
			if err := checkNode("/made/up", st, true); !errors.Is(err, ErrNotUSBNode) {
				t.Errorf("%s of major %d: %v", name, major, err)
			}
		}
	}
	// The test rule: only a regular file, whatever its numbers.
	for name, c := range map[string]struct {
		mode uint32
		want bool
	}{"regular": {unix.S_IFREG, true}, "char 189": {unix.S_IFCHR, false}, "directory": {unix.S_IFDIR, false}, "fifo": {unix.S_IFIFO, false}, "link": {unix.S_IFLNK, false}} {
		st := &unix.Stat_t{Mode: c.mode, Rdev: unix.Mkdev(189, 1)}
		if got := checkNode("/made/up", st, false) == nil; got != c.want {
			t.Errorf("test rule, %s: accepted %v", name, got)
		}
	}
}

// openChecked: the check runs on the O_PATH descriptor and again on the opened one; the O_PATH descriptor is closed on every path, and
// so is the opened one when the second check refuses.
func TestQAOpenCheckedClosesEveryDescriptor(t *testing.T) {
	nodes, dir := qaNodes(t)
	reg := nodes["regular file"]
	before := openFDs(t)
	for range 100 {
		// the first check refuses
		if fd, err := openChecked(reg, func(*unix.Stat_t) error { return ErrNotUSBNode }, unix.O_RDWR); err == nil || fd != -1 || !errors.Is(err, ErrNotUSBNode) {
			t.Fatalf("a refusing check: %d %v", fd, err)
		}
		// the first check passes and the second refuses
		calls := 0
		fd, err := openChecked(reg, func(*unix.Stat_t) error {
			calls++
			if calls > 1 {
				return ErrNotUSBNode
			}
			return nil
		}, unix.O_RDWR)
		if !errors.Is(err, ErrNotUSBNode) || fd != -1 || calls != 2 {
			t.Fatalf("a check that refuses the second time: %d %v after %d calls", fd, err, calls)
		}
		// both pass: one descriptor is kept
		fd, err = openChecked(reg, func(*unix.Stat_t) error { return nil }, unix.O_RDWR)
		if err != nil || fd < 0 {
			t.Fatal(err)
		}
		if after := openFDs(t); after != before+1 {
			t.Fatalf("%d descriptors after a good open, want %d", after, before+1)
		}
		_ = unix.Close(fd)
		// the path is missing, a link, a directory
		for _, p := range []string{filepath.Join(dir, "missing"), nodes["link to a regular file"], nodes["directory"], nodes["fifo"]} {
			_, _ = openChecked(p, func(st *unix.Stat_t) error {
				if st.Mode&unix.S_IFMT != unix.S_IFREG {
					return ErrNotUSBNode
				}
				return nil
			}, unix.O_RDWR)
		}
	}
	if after := openFDs(t); after != before {
		t.Errorf("%d descriptors before, %d after", before, after)
	}
}

// What the opened descriptor looks like: read-write, close-on-exec, non-blocking. The path is opened once and the descriptor is the
// one that was checked: the inode, not the name.
func TestQAOpenedDescriptorFlags(t *testing.T) {
	nodes, _ := qaNodes(t)
	d, err := open(nodes["regular file"], false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	fl, err := unix.FcntlInt(uintptr(d.fd), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if fl&unix.O_ACCMODE != unix.O_RDWR || fl&unix.O_NONBLOCK == 0 {
		t.Errorf("flags %#o: want read-write and non-blocking", fl)
	}
	fd, err := unix.FcntlInt(uintptr(d.fd), unix.F_GETFD, 0)
	if err != nil || fd&unix.FD_CLOEXEC == 0 {
		t.Errorf("close-on-exec: %v %v", fd, err)
	}
	// Under the test rule, everything but a regular file is still refused, and the link is not followed.
	for _, name := range []string{"directory", "fifo", "link to a regular file", "link to dev-null", "dangling link", "link to a link"} {
		within(t, name, func() {
			if _, err := open(nodes[name], false); !errors.Is(err, ErrNotUSBNode) {
				t.Errorf("%s under the test rule: %v", name, err)
			}
		})
	}
}

// The way a test opens a made-up file as a device, open(path, false), is not reachable from the exported API: read from the source,
// the way DiscoverHost was held. In the files that are not tests, the function open is called once, from Open and with the literal
// true; checkNode is called once, from open, with its own parameter; and a Device is built once, in open. The exported API has no
// parameter of the kind.
func TestQATheTestRuleOfOpenIsUnreachable(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var openCalls, checkCalls, literals []string
	for _, f := range files {
		if len(f) > 8 && f[len(f)-8:] == "_test.go" {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fd, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					id, ok := x.Fun.(*ast.Ident)
					if !ok {
						return true
					}
					switch id.Name {
					case "open":
						arg := ""
						if len(x.Args) == 2 {
							arg = fmt.Sprint(x.Args[1])
							if a, ok := x.Args[1].(*ast.Ident); ok {
								arg = a.Name
							}
						}
						openCalls = append(openCalls, fd.Name.Name+" open(_, "+arg+")")
					case "checkNode":
						arg := ""
						if len(x.Args) == 3 {
							if a, ok := x.Args[2].(*ast.Ident); ok {
								arg = a.Name
							}
						}
						checkCalls = append(checkCalls, fd.Name.Name+" checkNode(_, _, "+arg+")")
					}
				case *ast.CompositeLit:
					if id, ok := x.Type.(*ast.Ident); ok && id.Name == "Device" {
						literals = append(literals, fd.Name.Name)
					}
				}
				return true
			})
			// No exported function takes a bool.
			if fd.Name.IsExported() && fd.Type.Params != nil {
				for _, p := range fd.Type.Params.List {
					if id, ok := p.Type.(*ast.Ident); ok && id.Name == "bool" {
						t.Errorf("%s takes a bool: a switch of the kind of the test rule", fd.Name.Name)
					}
				}
			}
		}
	}
	if len(openCalls) != 1 || openCalls[0] != "Open open(_, true)" {
		t.Errorf("calls of open in the library: %v, want only Open with the literal true", openCalls)
	}
	if len(checkCalls) != 1 || checkCalls[0] != "open checkNode(_, _, usb)" {
		t.Errorf("calls of checkNode in the library: %v", checkCalls)
	}
	if len(literals) != 1 || literals[0] != "open" {
		t.Errorf("a Device is built in %v, want only in open", literals)
	}
}

// Candidate.Open closes the descriptor when verify refuses. A real node is needed to run it end to end (Open takes only a character
// device of the USB major number), so what is held is the source: in openCandidate, the branch that handles the error of verify
// calls Close before it returns, and the error is returned as it is (it wraps ErrWrongDevice), and verify is held by TestQAVerify.
func TestQACandidateOpenClosesWhenVerifyRefuses(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "device_linux.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name.Name != "openCandidate" {
			continue
		}
		ast.Inspect(fd, func(n ast.Node) bool {
			ifs, ok := n.(*ast.IfStmt)
			if !ok {
				return true
			}
			call, ok := ifs.Init.(*ast.AssignStmt)
			if !ok || len(call.Rhs) != 1 {
				return true
			}
			ce, ok := call.Rhs[0].(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := ce.Fun.(*ast.SelectorExpr); !ok || sel.Sel.Name != "verify" {
				return true
			}
			closes, returnsErr := false, false
			ast.Inspect(ifs.Body, func(m ast.Node) bool {
				switch x := m.(type) {
				case *ast.CallExpr:
					if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Close" {
						closes = true
					}
				case *ast.ReturnStmt:
					if len(x.Results) == 2 {
						if id, ok := x.Results[1].(*ast.Ident); ok && id.Name == "err" {
							returnsErr = true
						}
					}
				}
				return true
			})
			found = closes && returnsErr
			return true
		})
	}
	if !found {
		t.Error("openCandidate does not close the device and return the error when verify refuses it")
	}
}
