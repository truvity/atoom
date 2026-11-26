package sqs_test

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	transportv1alpha1 "github.com/truvity/atoom/transport/gen/atoom/transport/v1alpha1"
	"github.com/truvity/atoom/transport/sqs"
	"github.com/truvity/atoom/transport/sqs/internal/sqsfake"
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

// provoker builds the driver with the options and returns a function that makes it write a line of each kind it can: SQS failed, and a
// message that the driver did not write.
func provoker(t *testing.T, opts sqs.Options) func() {
	t.Helper()
	f := sqsfake.New()
	opts.SQS, opts.QueueURL, opts.Emitter, opts.WaitTime = f, sqsfake.QueueURL, "test@host", time.Second
	c, _ := mount(t, opts)
	return func() {
		t.Helper()
		f.Fail("SendMessage", "ServiceUnavailable")
		_, err := c.Emit(context.Background(), connect.NewRequest(&transportv1alpha1.EmitRequest{Message: msg("a")}))
		wantRefusal(t, err, connect.CodeUnavailable, "engine", "")
		f.Fail("SendMessage", "")
		f.Put("other", "not base64 !")
		emit(t, c, msg("a"))
		k := receive(t, c)
		if got := k.take(t, 1); got[0].GetMessage().GetId() != "a" {
			t.Fatalf("%v", uids(got))
		}
	}
}

var logged = []string{
	"SQS failed",
	"SQS: a message the driver did not write is left in the queue",
}

func TestALoggerOptionReceivesTheLinesOfTheDriver(t *testing.T) {
	mine, theDefault := &logRecorder{}, &logRecorder{}
	asDefaultLogger(t, theDefault)
	provoker(t, sqs.Options{Logger: slog.New(mine)})()
	for _, msg := range logged {
		mine.line(t, msg)
	}
	if got := theDefault.got(); len(got) != 0 {
		t.Fatalf("with a Logger the default logger got %v", theDefault.messages())
	}
}

func TestWithoutALoggerTheLinesGoToTheDefaultLoggerReadAtEachLine(t *testing.T) {
	provoke := provoker(t, sqs.Options{}) // built first: the default is replaced after
	theDefault := &logRecorder{}
	asDefaultLogger(t, theDefault)
	provoke()
	for _, msg := range logged {
		theDefault.line(t, msg)
	}
}

// The lines of the code at master (sqs/sqs.go, 7375d26): slog.Error("SQS failed", "call", what, "err", err) and
// slog.Error("SQS: a message the driver did not write is left in the queue", "message_id", msgID). The driver writes no line when it
// starts.
func TestTheLogLinesAreWhatTheCodeAtMasterWrote(t *testing.T) {
	mine := &logRecorder{}
	provoker(t, sqs.Options{Logger: slog.New(mine)})()
	wantLine(t, mine.line(t, "SQS failed"), slog.LevelError, "call", "err")
	wantLine(t, mine.line(t, "SQS: a message the driver did not write is left in the queue"), slog.LevelError, "message_id")
}
