package dynamodb_test

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/smithy-go"

	"github.com/truvity/atoom/store/dynamodb"
	storev1alpha1 "github.com/truvity/atoom/store/gen/atoom/store/v1alpha1"
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

// failingAPI is a DynamoDB client that fails every read with err, so that no DynamoDB Local is needed.
type failingAPI struct {
	dynamodb.API
	err error
}

func (f failingAPI) GetItem(context.Context, *awsdynamodb.GetItemInput, ...func(*awsdynamodb.Options)) (*awsdynamodb.GetItemOutput, error) {
	return nil, f.err
}

// provoker builds the driver with the options and returns a function that makes it write a line of each kind it can: the start of Run, a
// refusal, a failure of DynamoDB, and a request that DynamoDB refused as invalid.
func provoker(t *testing.T, opts dynamodb.Options) func() {
	t.Helper()
	opts.DynamoDB = failingAPI{err: errors.New("the engine is down")}
	down, err := dynamodb.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.DynamoDB = failingAPI{err: &smithy.GenericAPIError{Code: "ValidationException", Message: "malformed"}}
	invalid, err := dynamodb.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	host := dynamodb.Host{Serve: func(context.Context, string, http.Handler) error { return nil }}
	get := func(svc *dynamodb.Service, key string) error {
		_, err := svc.Get(context.Background(), connect.NewRequest(&storev1alpha1.ItemGetRequest{Key: []byte(key)}))
		return err
	}
	return func() {
		t.Helper()
		if err := dynamodb.Run(context.Background(), opts, host); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if got := connect.CodeOf(get(down, "a/1")); got != connect.CodeUnavailable {
			t.Fatalf("a failure of the engine: %v", got)
		}
		if got := connect.CodeOf(get(invalid, "a/1")); got != connect.CodeInvalidArgument {
			t.Fatalf("a request refused as invalid: %v", got)
		}
		if got := connect.CodeOf(get(down, "")); got != connect.CodeInvalidArgument {
			t.Fatalf("an empty key: %v", got)
		}
	}
}

var logged = []string{
	"dynamodb item driver starting",
	"refused",
	"DynamoDB failed",
	"DynamoDB refused a request as invalid",
}

func TestALoggerOptionReceivesTheLinesOfTheDriver(t *testing.T) {
	mine, theDefault := &logRecorder{}, &logRecorder{}
	asDefaultLogger(t, theDefault)
	provoker(t, dynamodb.Options{Logger: slog.New(mine)})()
	for _, msg := range logged {
		mine.line(t, msg)
	}
	if got := theDefault.got(); len(got) != 0 {
		t.Fatalf("with a Logger the default logger got %v", theDefault.messages())
	}
}

func TestWithoutALoggerTheLinesGoToTheDefaultLoggerReadAtEachLine(t *testing.T) {
	provoke := provoker(t, dynamodb.Options{}) // built first: the default is replaced after
	theDefault := &logRecorder{}
	asDefaultLogger(t, theDefault)
	provoke()
	for _, msg := range logged {
		theDefault.line(t, msg)
	}
}

// The lines of the code at master (dynamodb/dynamodb.go and service.go, 7375d26), with their levels and attribute keys.
func TestTheLogLinesAreWhatTheCodeAtMasterWrote(t *testing.T) {
	mine := &logRecorder{}
	provoker(t, dynamodb.Options{Logger: slog.New(mine)})()
	wantLine(t, mine.line(t, "dynamodb item driver starting"), slog.LevelInfo, "table")
	wantLine(t, mine.line(t, "refused"), slog.LevelWarn, "method", "code", "message", "caller")
	wantLine(t, mine.line(t, "DynamoDB failed"), slog.LevelError, "method", "error", "caller")
	wantLine(t, mine.line(t, "DynamoDB refused a request as invalid"), slog.LevelError, "method", "error")
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
