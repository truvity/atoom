package awskms_test

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/key/awskms"
	"github.com/truvity/atoom/key/awskms/internal/kmsfake"
	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
	"github.com/truvity/atoom/key/gen/atoom/key/v1alpha1/keyv1alpha1connect"
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

// provoker builds the driver with the options and returns a function that makes it write a line of each kind it can: the start of Run,
// a key made, a refusal, KMS failed, a key that cannot be used, the loss of the race to make a key, and a lost key that could not be
// scheduled for deletion.
func provoker(t *testing.T, opts awskms.Options) func() {
	t.Helper()
	opts.CreateKeys = true
	main := kmsfake.New()
	c, _ := start(t, main, opts)
	host := awskms.Host{Serve: func(context.Context, string, http.Handler) error { return nil }}
	runOpts := opts
	runOpts.KMS, runOpts.Purposes = kmsClient(t, main), hostPurposes()

	unusable := kmsfake.New()
	k, _ := unusable.AddKey("alias/atoom/token-signing", "ECC_NIST_P384", "SIGN_VERIFY")
	unusable.SetState(k.ID, "Disabled")
	cu, _ := start(t, unusable, opts)

	racing := kmsfake.New()
	cA, _ := start(t, racing, opts)
	cB, _ := start(t, racing, opts)

	return func() {
		t.Helper()
		ctx := context.Background()
		if err := awskms.Run(ctx, runOpts, host); err != nil {
			t.Fatalf("Run: %v", err)
		}
		ensure(t, c, "token-signing", "ES384")
		_, err := c.Ensure(ctx, connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: "nope", Algorithm: "ES384"}))
		want(t, err, connect.CodeInvalidArgument, "purpose")
		main.Fail("Sign", "AccessDeniedException")
		_, err = c.Sign(ctx, connect.NewRequest(&keyv1alpha1.SignRequest{Purpose: "token-signing", Digest: digest384(), Algorithm: "ES384"}))
		want(t, err, connect.CodeInternal, "store")

		_, err = cu.Ensure(ctx, connect.NewRequest(&keyv1alpha1.EnsureRequest{Purpose: "token-signing", Algorithm: "ES384"}))
		want(t, err, connect.CodeFailedPrecondition, "store")

		var gate sync.WaitGroup
		gate.Add(2)
		racing.SetHook(func(op string) {
			if op == "CreateAlias" {
				gate.Done()
				gate.Wait()
			}
		})
		racing.Fail("ScheduleKeyDeletion", "AccessDeniedException")
		var wg sync.WaitGroup
		for _, c := range []keyv1alpha1connect.KeyStoreServiceClient{cA, cB} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ensure(t, c, "token-signing", "ES384")
			}()
		}
		wg.Wait()
	}
}

var logged = []string{
	"aws kms key driver starting",
	"key made",
	"refused",
	"KMS failed",
	"the KMS key cannot be used",
	"another caller made the key first; keeping its key",
	"a key without an alias could not be scheduled for deletion",
}

func TestALoggerOptionReceivesTheLinesOfTheDriver(t *testing.T) {
	mine, theDefault := &logRecorder{}, &logRecorder{}
	asDefaultLogger(t, theDefault)
	provoker(t, awskms.Options{Logger: slog.New(mine)})()
	for _, msg := range logged {
		mine.line(t, msg)
	}
	if got := theDefault.got(); len(got) != 0 {
		t.Fatalf("with a Logger the default logger got %v", theDefault.messages())
	}
}

func TestWithoutALoggerTheLinesGoToTheDefaultLoggerReadAtEachLine(t *testing.T) {
	provoke := provoker(t, awskms.Options{}) // built first: the default is replaced after
	theDefault := &logRecorder{}
	asDefaultLogger(t, theDefault)
	provoke()
	for _, msg := range logged {
		theDefault.line(t, msg)
	}
}

// The lines of the code at master (awskms/awskms.go, keys.go and service.go, 7375d26), with their levels and attribute keys.
func TestTheLogLinesAreWhatTheCodeAtMasterWrote(t *testing.T) {
	mine := &logRecorder{}
	provoker(t, awskms.Options{Logger: slog.New(mine)})()
	wantLine(t, mine.line(t, "aws kms key driver starting"), slog.LevelInfo, "alias_prefix", "create_keys", "purposes")
	wantLine(t, mine.line(t, "key made"), slog.LevelInfo, "purpose", "algorithm", "alias")
	wantLine(t, mine.line(t, "another caller made the key first; keeping its key"), slog.LevelInfo, "purpose", "alias")
	wantLine(t, mine.line(t, "a key without an alias could not be scheduled for deletion"), slog.LevelWarn, "key", "error")
	wantLine(t, mine.line(t, "refused"), slog.LevelWarn, "method", "code", "reason", "field", "caller")
	wantLine(t, mine.line(t, "the KMS key cannot be used"), slog.LevelError, "method", "state")
	wantLine(t, mine.line(t, "KMS failed"), slog.LevelError, "method", "code", "error")
}
