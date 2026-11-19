package socket_test

import (
	"context"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"

	commonv1alpha1 "github.com/truvity/atoom/common/gen/atoom/common/v1alpha1"
	"github.com/truvity/atoom/common/refusal"
	"github.com/truvity/atoom/socket"
)

// The messages of the test service are Refusal (the one message every atoom library has), so that the module needs no port: the
// handler answers who it saw, in Reason (the uid) and FieldPath (the pid).
const procedure = "/atoom.test.v1.Who/Ask"

func me() uint32 { return uint32(os.Geteuid()) } //nolint:gosec // a uid is non-negative

// shortDir is a directory with a short name: a UNIX socket path is at most 104 bytes on macOS, and the name of a test in t.TempDir() is
// long.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "g")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

type running struct {
	socket string
	calls  *atomic.Int32
	stop   func()
}

// start serves the test service on a socket in a fresh directory.
func start(t *testing.T, cfg socket.Config) running {
	t.Helper()
	if cfg.Socket == "" {
		cfg.Socket = filepath.Join(shortDir(t), "s")
	}
	var calls atomic.Int32
	h := connect.NewUnaryHandler(procedure, func(ctx context.Context, _ *connect.Request[commonv1alpha1.Refusal]) (*connect.Response[commonv1alpha1.Refusal], error) {
		calls.Add(1)
		out := &commonv1alpha1.Refusal{Reason: "none"}
		if p, ok := socket.PeerFromContext(ctx); ok {
			out.Reason = strconv.FormatUint(uint64(p.UID), 10)
			out.FieldPath = strconv.FormatInt(int64(p.PID), 10)
		}
		return connect.NewResponse(out), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- socket.Serve(ctx, cfg, procedure, h) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if c, err := net.Dial("unix", cfg.Socket); err == nil {
			_ = c.Close()
			break
		}
		select {
		case err := <-done:
			t.Fatalf("the server ended before it served: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("no socket in 5 seconds")
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop := func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	}
	t.Cleanup(func() {
		select {
		case <-ctx.Done():
		default:
			stop()
		}
	})
	return running{socket: cfg.Socket, calls: &calls, stop: stop}
}

func (r running) client() *http.Client {
	var protocols http.Protocols
	protocols.SetUnencryptedHTTP2(true)
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", r.socket)
		},
		Protocols: &protocols,
	}}
}

func (r running) ask(t *testing.T) (*commonv1alpha1.Refusal, error) {
	t.Helper()
	c := connect.NewClient[commonv1alpha1.Refusal, commonv1alpha1.Refusal](r.client(), "http://unix"+procedure)
	res, err := c.CallUnary(context.Background(), connect.NewRequest(&commonv1alpha1.Refusal{}))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

func wantCallerRefusal(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("the call was admitted; want permission_denied with the reason caller")
	}
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("code %v; want permission_denied (%v)", connect.CodeOf(err), err)
	}
	if reason, _ := refusal.Reason(err); reason != socket.ReasonCaller {
		t.Fatalf("reason %q; want %q (%v)", reason, socket.ReasonCaller, err)
	}
}

func TestTheDefaultAdmitsTheUIDOfTheProcessAndHandsItToTheHandler(t *testing.T) {
	r := start(t, socket.Config{})
	got, err := r.ask(t)
	if err != nil {
		t.Fatalf("the same uid is refused: %v", err)
	}
	if got.GetReason() != strconv.FormatUint(uint64(me()), 10) {
		t.Fatalf("the handler saw uid %q; want %d", got.GetReason(), me())
	}
	if runtime.GOOS == "linux" && got.GetFieldPath() != strconv.Itoa(os.Getpid()) {
		t.Fatalf("the handler saw pid %q; want %d", got.GetFieldPath(), os.Getpid())
	}
}

func TestAListedUIDIsAdmitted(t *testing.T) {
	r := start(t, socket.Config{Allow: socket.AllowUIDs(me()+1, me())})
	if _, err := r.ask(t); err != nil {
		t.Fatalf("a listed uid is refused: %v", err)
	}
}

func TestAnotherUIDIsRefusedWithTheReasonCallerBeforeTheHandlerRuns(t *testing.T) {
	// The caller is this process; the server is told that only another uid may call, which is what a caller of another uid meets.
	r := start(t, socket.Config{Allow: socket.AllowUIDs(me() + 1)})
	_, err := r.ask(t)
	wantCallerRefusal(t, err)
	if n := r.calls.Load(); n != 0 {
		t.Fatalf("a refused call reached the handler %d times", n)
	}
}

func TestAnEmptySetAdmitsNobodyAndTheListDoesNotImplyTheOwnUID(t *testing.T) {
	r := start(t, socket.Config{Allow: socket.AllowUIDs()})
	_, err := r.ask(t)
	wantCallerRefusal(t, err)
}

func TestAnyUIDIsThePlainBehaviourAndStillHandsOverThePeer(t *testing.T) {
	r := start(t, socket.Config{Allow: socket.AllowAnyUID()})
	got, err := r.ask(t)
	if err != nil {
		t.Fatalf("any uid refused: %v", err)
	}
	if got.GetReason() != strconv.FormatUint(uint64(me()), 10) && got.GetReason() != "none" {
		t.Fatalf("the handler saw uid %q", got.GetReason())
	}
}

// The refusal has the right shape for each protocol and reads no message.
func TestARefusedCallIsAnsweredInTheShapeOfItsProtocolWithoutReadingTheMessage(t *testing.T) {
	r := start(t, socket.Config{Allow: socket.AllowUIDs(me() + 1)})
	for _, tc := range []struct {
		name, contentType string
		status            int
		want              []string
	}{
		{"connect unary", "application/proto", http.StatusForbidden, []string{"permission_denied", "atoom.common.v1alpha1.Refusal"}},
		{"connect unary json", "application/json", http.StatusForbidden, []string{"permission_denied"}},
		{"connect stream", "application/connect+json", http.StatusOK, []string{"permission_denied"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A body that is not a message at all: a guard that decoded first would answer invalid_argument.
			req, _ := http.NewRequest(http.MethodPost, "http://unix"+procedure, strings.NewReader("\xff\xff not a message"))
			req.Header.Set("Content-Type", tc.contentType)
			req.Header.Set("Connect-Protocol-Version", "1")
			res, err := r.client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = res.Body.Close() }()
			body, _ := io.ReadAll(res.Body)
			if res.StatusCode != tc.status {
				t.Fatalf("status %d; want %d (%s)", res.StatusCode, tc.status, body)
			}
			for _, w := range tc.want {
				if !strings.Contains(string(body), w) {
					t.Fatalf("the answer lacks %q: %s", w, body)
				}
			}
		})
	}
	if r.calls.Load() != 0 {
		t.Fatal("a refused call reached the handler")
	}
}

func TestTheGroupAndTheModeAreAppliedBeforeTheSocketHasItsName(t *testing.T) {
	gid := uint32(os.Getegid()) //nolint:gosec // a gid is non-negative
	r := start(t, socket.Config{Mode: 0o660, GID: gid})
	fi, err := os.Stat(r.socket)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o660 {
		t.Fatalf("mode %o; want 660", got)
	}
	if got := fi.Sys().(*syscall.Stat_t).Gid; got != gid {
		t.Fatalf("group %d; want %d", got, gid)
	}
	// The default is 0600.
	d := start(t, socket.Config{})
	fi, err = os.Stat(d.socket)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("default mode %o; want 600", got)
	}
}

func TestAGroupTheProcessIsNotInIsRefusedAndLeavesNoSocket(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("as root a chown to any group succeeds: this case is not exercised")
	}
	dir := shortDir(t)
	cfg := socket.Config{Socket: filepath.Join(dir, "s"), GID: uint32(os.Getegid()) + 54321} //nolint:gosec // a gid is non-negative
	err := socket.Serve(context.Background(), cfg, procedure, http.NotFoundHandler())
	if err == nil || !strings.Contains(err.Error(), "socket group") {
		t.Fatalf("err %v; want the group refused", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("left behind: %v", entries)
	}
}

func TestASocketIsMadeFresh(t *testing.T) {
	dir := shortDir(t)
	// A stale socket (nobody listens) is replaced.
	stale := filepath.Join(dir, "stale")
	l, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = l.Close()
	live := start(t, socket.Config{Socket: stale})
	// A live listener is never taken over.
	err = socket.Serve(context.Background(), socket.Config{Socket: live.socket}, procedure, http.NotFoundHandler())
	if err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("err %v; want in use", err)
	}
	// A symlink or a plain file in the way is refused.
	link := filepath.Join(dir, "link")
	if err := os.Symlink(filepath.Join(dir, "elsewhere"), link); err != nil {
		t.Fatal(err)
	}
	err = socket.Serve(context.Background(), socket.Config{Socket: link}, procedure, http.NotFoundHandler())
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("err %v; want a symlink refused", err)
	}
	file := filepath.Join(dir, "plain")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = socket.Serve(context.Background(), socket.Config{Socket: file}, procedure, http.NotFoundHandler())
	if err == nil || !strings.Contains(err.Error(), "not a socket") {
		t.Fatalf("err %v; want not a socket", err)
	}
	// The socket file is removed when the server stops.
	live.stop()
	if _, err := os.Lstat(live.socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the socket file stays after the server stopped: %v", err)
	}
}

func TestAllowDecidesByUID(t *testing.T) {
	for _, tc := range []struct {
		name string
		a    socket.Allow
		uid  uint32
		want bool
	}{
		{"same uid, same", socket.AllowSameUID(), 5, true},
		{"same uid, root is another uid", socket.AllowSameUID(), 0, false},
		{"zero value is the same uid", socket.Allow{}, 0, false},
		{"listed", socket.AllowUIDs(0, 9), 9, true},
		{"the own uid is not implied by a list", socket.AllowUIDs(0, 9), 5, false},
		{"any", socket.AllowAnyUID(), 1, true},
	} {
		if got := tc.a.Admits(tc.uid, 5); got != tc.want {
			t.Errorf("%s: %v; want %v", tc.name, got, tc.want)
		}
	}
}

func parse(t *testing.T, args ...string) (socket.Config, error) {
	t.Helper()
	var f socket.Flags
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	f.Register(fs)
	if err := fs.Parse(args); err != nil {
		return socket.Config{}, err
	}
	return f.Config("/s")
}

func TestTheFlags(t *testing.T) {
	cfg, err := parse(t)
	if err != nil || cfg.Mode != 0 || cfg.GID != 0 || cfg.Allow.Admits(1, 2) || !cfg.Allow.Admits(2, 2) {
		t.Fatalf("default: %+v, %v", cfg, err)
	}
	cfg, err = parse(t, "--allow-uid", "1001", "--allow-uid", "7")
	if err != nil || !cfg.Allow.Admits(1001, 5) || !cfg.Allow.Admits(7, 5) || cfg.Allow.Admits(5, 5) {
		t.Fatalf("uids: %+v, %v", cfg, err)
	}
	cfg, err = parse(t, "--socket-gid", "70")
	if err != nil || cfg.GID != 70 || cfg.Mode != 0o660 {
		t.Fatalf("gid: %+v, %v", cfg, err)
	}
	cfg, err = parse(t, "--socket-gid", "70", "--socket-mode", "0640")
	if err != nil || cfg.Mode != 0o640 {
		t.Fatalf("gid and mode: %+v, %v", cfg, err)
	}
	cfg, err = parse(t, "--allow-any-uid")
	if err != nil || !cfg.Allow.Admits(1, 2) {
		t.Fatalf("any: %+v, %v", cfg, err)
	}
	for _, bad := range [][]string{
		{"--socket-gid", "0"}, {"--socket-gid", "x"}, {"--socket-mode", "zz"}, {"--socket-mode", "1777"},
		{"--allow-uid", "root"}, {"--allow-any-uid", "--allow-uid", "1"},
	} {
		if _, err := parse(t, bad...); err == nil {
			t.Errorf("%v must be refused", bad)
		}
	}
}
