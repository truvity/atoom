package yubikeypiv_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"

	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
	"github.com/truvity/atoom/key/yubikeypiv"
	"github.com/truvity/atoom/key/yubikeypiv/internal/card/cardtest"
)

// logLine is one line a logRecorder received.
type logLine struct {
	level slog.Level
	msg   string
	attrs []slog.Attr
}

// keys are the attribute keys of the line, in the order the driver wrote them.
func (l logLine) keys() []string {
	var ks []string
	for _, a := range l.attrs {
		ks = append(ks, a.Key)
	}
	return ks
}

// logRecorder is an in-memory slog.Handler that records the lines it receives.
type logRecorder struct {
	mu    sync.Mutex
	lines []logLine
}

func (r *logRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *logRecorder) Handle(_ context.Context, rec slog.Record) error {
	line := logLine{level: rec.Level, msg: rec.Message}
	rec.Attrs(func(a slog.Attr) bool {
		line.attrs = append(line.attrs, a)
		return true
	})
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, line)
	return nil
}
func (r *logRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *logRecorder) WithGroup(string) slog.Handler      { return r }

// got is a copy of the lines received so far.
func (r *logRecorder) got() []logLine {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.lines)
}

// line is the first line received with the message, or a failure of the test.
func (r *logRecorder) line(t *testing.T, msg string) logLine {
	t.Helper()
	for _, l := range r.got() {
		if l.msg == msg {
			return l
		}
	}
	t.Fatalf("no line %q was logged; got %v", msg, r.messages())
	return logLine{}
}

func (r *logRecorder) messages() []string {
	var ms []string
	for _, l := range r.got() {
		ms = append(ms, l.msg)
	}
	return ms
}

// asDefaultLogger makes the recorder the default logger for the test and puts the old one back at its end.
func asDefaultLogger(t *testing.T, r *logRecorder) {
	t.Helper()
	old := slog.Default()
	slog.SetDefault(slog.New(r))
	t.Cleanup(func() { slog.SetDefault(old) })
}

// wantLine holds a line to the level and the attribute keys, in order, that the code at master (7375d26) wrote.
func wantLine(t *testing.T, l logLine, level slog.Level, keys ...string) {
	t.Helper()
	if l.level != level || !slices.Equal(l.keys(), keys) {
		t.Fatalf("line %q: level %s and keys %v, want %s and %v", l.msg, l.level, l.keys(), level, keys)
	}
}

// The lines of the start of the driver, which provoker writes when it is called, and the lines of the calls that its function makes.
var (
	startedWith = []string{
		"a released PIN was refused by the card before: no PIN release until the board is unlocked with sluis node unlock",
		"card session reset: the card was found PIN-verified and the driver held no PIN",
		"card session reset",
		"yubikey-piv key driver starting, locked",
	}
	loggedLater = []string{
		"admin call refused",
		"unlock refused: the card did not accept the PIN",
		"key made on the card",
		"refused",
		"the PIN release stop cannot be removed: it holds again after a restart",
		"unlocked: the driver holds the PIN in locked memory",
	}
)

// provoker starts the driver over the fake card with the options, as the existing tests do, and returns a function that makes it write a
// line of each kind it can. The start writes the lines of the start-up path: a stop of the PIN release found in the state directory (a
// directory that cannot be removed, so that the unlock cannot clear it), a card left PIN-verified and reset, and the line of Run; a
// second driver made on a clean card, without Run, writes the other line of the reset. The function then makes a caller that is not an
// administrator, a wrong PIN, a key made, a call refused as locked, and an unlock that clears the stop. The lines of the PIN release
// between two drivers, and those of a card that fails or is gone, are not provoked here.
func provoker(t *testing.T, opts yubikeypiv.Options) func() {
	t.Helper()
	skipUnlessLinux(t)
	state := stateDir(t)
	if err := os.MkdirAll(filepath.Join(state, "pin-release.refused", "held"), 0o700); err != nil {
		t.Fatal(err)
	}
	opts.Purposes, opts.Node, opts.StateDir = refPurposes(), "node-a", state
	var administrator atomic.Bool
	c := cardtest.New()
	c.Verify() // left PIN-verified by an earlier driver: the reset at the start is a warning
	r := startOpts(t, c, factoryDefaults, func(uint32) bool { return administrator.Load() }, opts)
	clean := newOpts(cardtest.New())
	clean.Logger = opts.Logger
	svc, err := yubikeypiv.New(clean)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return func() {
		t.Helper()
		ctx := context.Background()
		_, err := r.admin.Status(ctx, connect.NewRequest(&keyv1alpha1.StatusRequest{}))
		if connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("Status by a non-administrator: %v, want permission_denied", err)
		}
		administrator.Store(true)
		_, err = r.admin.Unlock(ctx, connect.NewRequest(&keyv1alpha1.UnlockRequest{Pin: []byte("654321")}))
		wantRefused(t, err, connect.CodePermissionDenied, "pin")
		ensure(t, r.key, "token-signing", "ES384")
		_, err = sign(r.key, "token-signing", "ES384", digest384())
		wantRefused(t, err, connect.CodeFailedPrecondition, "locked")
		r.unlock(t, "123456")
	}
}

func TestALoggerOptionReceivesTheLinesOfTheDriver(t *testing.T) {
	mine, theDefault := &logRecorder{}, &logRecorder{}
	asDefaultLogger(t, theDefault)
	provoker(t, yubikeypiv.Options{Logger: slog.New(mine)})()
	for _, msg := range append(slices.Clone(startedWith), loggedLater...) {
		mine.line(t, msg)
	}
	if got := theDefault.got(); len(got) != 0 {
		t.Fatalf("with a Logger the default logger got %v", theDefault.messages())
	}
}

func TestWithoutALoggerTheLinesGoToTheDefaultLoggerReadAtEachLine(t *testing.T) {
	provoke := provoker(t, yubikeypiv.Options{}) // built first: the default is replaced after
	theDefault := &logRecorder{}
	asDefaultLogger(t, theDefault)
	provoke()
	for _, msg := range loggedLater {
		theDefault.line(t, msg)
	}
}

// The lines of the start-up path are written when the driver is made, and go to the default logger of that moment.
func TestWithoutALoggerTheStartUpLinesGoToTheDefaultLogger(t *testing.T) {
	theDefault := &logRecorder{}
	asDefaultLogger(t, theDefault)
	provoker(t, yubikeypiv.Options{})()
	for _, msg := range append(slices.Clone(startedWith), loggedLater...) {
		theDefault.line(t, msg)
	}
}

// The lines of the code at master (yubikeypiv/*.go, 7375d26), with their levels and attribute keys, the start-up path, the guard of the
// admin socket and the two helpers of the stop of the PIN release (refusedMark.restore and refusedMark.clear) among them.
func TestTheLogLinesAreWhatTheCodeAtMasterWrote(t *testing.T) {
	mine := &logRecorder{}
	provoker(t, yubikeypiv.Options{Logger: slog.New(mine)})()
	wantLine(t, mine.line(t, startedWith[0]), slog.LevelWarn)
	wantLine(t, mine.line(t, startedWith[1]), slog.LevelWarn, "where", "card", "driver_holds_pin")
	wantLine(t, mine.line(t, startedWith[2]), slog.LevelInfo, "where", "card", "driver_holds_pin")
	wantLine(t, mine.line(t, startedWith[3]), slog.LevelInfo, "factory_defaults", "boundary", "purposes")
	wantLine(t, mine.line(t, "admin call refused"), slog.LevelInfo, "reason", "uid", "path")
	wantLine(t, mine.line(t, "unlock refused: the card did not accept the PIN"), slog.LevelWarn, "retries")
	wantLine(t, mine.line(t, "key made on the card"), slog.LevelInfo, "purpose", "slot", "algorithm")
	wantLine(t, mine.line(t, "refused"), slog.LevelWarn, "method", "code", "reason", "field", "caller")
	wantLine(t, mine.line(t, "the PIN release stop cannot be removed: it holds again after a restart"), slog.LevelError, "error")
	wantLine(t, mine.line(t, "unlocked: the driver holds the PIN in locked memory"), slog.LevelInfo)
}

// A line goes through the helper that resolves nil to slog.Default() or through a *slog.Logger the host gave: the package-level
// functions of log/slog name no logger of the host, so none is called in the module's own code.
func TestNoPackageLevelSlogCallInTheDriver(t *testing.T) {
	banned := []string{"Info", "Warn", "Error", "Debug", "Log", "LogAttrs"}
	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		name := "slog"
		for _, imp := range file.Imports {
			if imp.Path.Value == `"log/slog"` && imp.Name != nil {
				name = imp.Name.Name
			}
		}
		for _, decl := range file.Decls {
			fn, _ := decl.(*ast.FuncDecl)
			inHelper := fn != nil && fn.Recv != nil && fn.Name.Name == "log"
			ast.Inspect(decl, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == name {
					if slices.Contains(banned, sel.Sel.Name) || (sel.Sel.Name == "Default" && !inHelper) {
						t.Errorf("%s: slog.%s outside the helper that resolves a nil Logger", fset.Position(sel.Pos()), sel.Sel.Name)
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
