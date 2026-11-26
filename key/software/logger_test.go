package software_test

import (
	"context"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"connectrpc.com/connect"

	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
	"github.com/truvity/atoom/key/purpose"
	"github.com/truvity/atoom/key/software"
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

// provoker builds the driver with the options and returns a function that makes it write a line of each kind it can: a key made, a
// refusal, and the line of Run.
func provoker(t *testing.T, opts software.Options) func() {
	t.Helper()
	served := startWith(t, opts)
	opts.StateDir, opts.Purposes = filepath.Join(shortDir(t), "state"), purpose.Reference()
	host := software.Host{Serve: func(context.Context, string, http.Handler) error { return nil }}
	return func() {
		t.Helper()
		ensure(t, served.client, "token-signing", "ES384")
		_, err := served.client.Ensure(context.Background(), connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: "nope", Algorithm: "ES384"}))
		wantRefused(t, err, connect.CodeInvalidArgument, "purpose")
		if err := software.Run(context.Background(), opts, host); err != nil {
			t.Fatalf("Run: %v", err)
		}
	}
}

func TestALoggerOptionReceivesTheLinesOfTheDriver(t *testing.T) {
	mine, theDefault := &logRecorder{}, &logRecorder{}
	asDefaultLogger(t, theDefault)
	provoker(t, software.Options{Logger: slog.New(mine)})()
	mine.line(t, "software key driver starting")
	mine.line(t, "key made")
	mine.line(t, "refused")
	if got := theDefault.got(); len(got) != 0 {
		t.Fatalf("with a Logger the default logger got %v", theDefault.messages())
	}
}

func TestWithoutALoggerTheLinesGoToTheDefaultLoggerReadAtEachLine(t *testing.T) {
	provoke := provoker(t, software.Options{}) // built first: the default is replaced after
	theDefault := &logRecorder{}
	asDefaultLogger(t, theDefault)
	provoke()
	theDefault.line(t, "software key driver starting")
	theDefault.line(t, "key made")
	theDefault.line(t, "refused")
}

// The lines of the code at master (software/software.go, 7375d26): slog.Info("software key driver starting", "purposes", ...),
// slog.Info("key made", "purpose", ..., "algorithm", ...) and slog.Warn("refused", "method", ..., "code", ..., "reason", ..., "field",
// ..., "caller", ...). The line "store failed" (slog.Error, "method", "error") needs a store that breaks under the driver and is not
// reached.
func TestTheLogLinesAreWhatTheCodeAtMasterWrote(t *testing.T) {
	mine := &logRecorder{}
	provoker(t, software.Options{Logger: slog.New(mine)})()
	wantLine(t, mine.line(t, "software key driver starting"), slog.LevelInfo, "purposes")
	wantLine(t, mine.line(t, "key made"), slog.LevelInfo, "purpose", "algorithm")
	wantLine(t, mine.line(t, "refused"), slog.LevelWarn, "method", "code", "reason", "field", "caller")
}
