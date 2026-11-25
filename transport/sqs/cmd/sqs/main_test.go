package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/common/call"
	"github.com/truvity/atoom/common/refusal"
	atoomsocket "github.com/truvity/atoom/socket"
	transportv1alpha1 "github.com/truvity/atoom/transport/gen/atoom/transport/v1alpha1"
	"github.com/truvity/atoom/transport/gen/atoom/transport/v1alpha1/transportv1alpha1connect"
	"github.com/truvity/atoom/transport/sqs"
	"github.com/truvity/atoom/transport/sqs/internal/sqsfake"
)

// useFake points the main at an in-process fake queue for the duration of the test.
func useFake(t *testing.T) *sqsfake.Fake {
	t.Helper()
	f := sqsfake.New()
	old := newClient
	t.Cleanup(func() { newClient = old })
	newClient = func(context.Context) (sqs.API, error) { return f, nil }
	return f
}

func TestUsage(t *testing.T) {
	base := []string{"serve", "--socket", "/x", "--queue-url", sqsfake.QueueURL}
	with := func(extra ...string) []string { return append(append([]string(nil), base...), extra...) }
	for _, args := range [][]string{
		nil, {"nope"}, {"serve"}, {"serve", "--socket", "/x"}, {"serve", "--queue-url", sqsfake.QueueURL}, with("extra"),
		with("--socket-mode", "zz"), with("--wait-time", "30s"),
	} {
		useFake(t)
		var out, errb bytes.Buffer
		if code := run(args, &out, &errb); code != 2 {
			t.Errorf("run(%v) = %d, want 2 (%s)", args, code, errb.String())
		}
	}
	// A standard queue is refused before a socket is made.
	useFake(t)
	socket := filepath.Join(t.TempDir(), "s")
	if code := run([]string{"serve", "--socket", socket, "--queue-url", strings.TrimSuffix(sqsfake.QueueURL, ".fifo")}, &bytes.Buffer{}, &bytes.Buffer{}); code != 2 {
		t.Fatalf("a standard queue: %d, want 2", code)
	}
	if _, err := os.Lstat(socket); err == nil {
		t.Fatal("a socket was made for a standard queue")
	}
}

func TestVersion(t *testing.T) {
	for _, arg := range []string{"version", "--version"} {
		var out bytes.Buffer
		if code := run([]string{arg}, &out, &bytes.Buffer{}); code != 0 || strings.TrimSpace(out.String()) == "" {
			t.Fatalf("%s: %d %q", arg, code, out.String())
		}
	}
}

func serveWith(t *testing.T, extra ...string) (socket string, stop func() int) {
	t.Helper()
	useFake(t)
	dir, err := os.MkdirTemp("", "g")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket = filepath.Join(dir, "s")
	args := append([]string{"serve", "--socket", socket, "--queue-url", sqsfake.QueueURL, "--wait-time", "1s"}, extra...)
	done := make(chan int, 1)
	go func() { done <- run(args, &bytes.Buffer{}, &bytes.Buffer{}) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if fi, err := os.Lstat(socket); err == nil && fi.Mode()&os.ModeSocket != 0 {
			break
		}
		select {
		case code := <-done:
			t.Fatalf("serve returned early: %d", code)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the socket did not appear")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return socket, func() int {
		p, _ := os.FindProcess(os.Getpid())
		_ = p.Signal(os.Interrupt)
		select {
		case code := <-done:
			return code
		case <-time.After(10 * time.Second):
			t.Error("serve did not end")
			return -1
		}
	}
}

func dial(socket string) transportv1alpha1connect.TransportServiceClient {
	return transportv1alpha1connect.NewTransportServiceClient(atoomsocket.Client(socket), "http://transport", call.ClientOptions()...)
}

func emitOne(socket string) error {
	_, err := dial(socket).Emit(context.Background(), connect.NewRequest(&transportv1alpha1.EmitRequest{
		Message: &transportv1alpha1.AuditMessage{Uid: "u-" + strconv.FormatInt(time.Now().UnixNano(), 10), Record: []byte("{}")}, Blocking: true}))
	return err
}

func me() uint32 { return uint32(os.Geteuid()) } //nolint:gosec // a uid is non-negative

func TestTheMainServesThroughItsSocket(t *testing.T) {
	socket, stop := serveWith(t)
	err := emitOne(socket)
	var got *transportv1alpha1.Delivery
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		stream, rerr := dial(socket).Receive(ctx, connect.NewRequest(&transportv1alpha1.ReceiveRequest{}))
		if rerr == nil && stream.Receive() {
			got = stream.Msg().GetDeliveries()[0]
		}
		cancel()
	}
	if code := stop(); code != 0 {
		t.Errorf("serve ended with %d", code)
	}
	if err != nil || got == nil || got.GetEmitter() != "local" {
		t.Fatalf("through the socket: %v %v", got, err)
	}
}

func TestTheGuardAdmitsTheListedUIDsAndRefusesTheOthersWithTheReasonCaller(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
		want  bool
	}{
		{"the default admits the uid of the process", nil, true},
		{"a listed uid", []string{"--allow-uid", strconv.Itoa(int(me()) + 1), "--allow-uid", strconv.Itoa(int(me()))}, true},
		{"any uid", []string{"--allow-any-uid"}, true},
		{"another uid only", []string{"--allow-uid", strconv.Itoa(int(me()) + 1)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			socket, stop := serveWith(t, tc.flags...)
			err := emitOne(socket)
			if code := stop(); code != 0 {
				t.Errorf("serve ended with %d", code)
			}
			if tc.want {
				if err != nil {
					t.Fatalf("the call was refused: %v", err)
				}
				return
			}
			if connect.CodeOf(err) != connect.CodePermissionDenied {
				t.Fatalf("code %v (%v); want permission_denied", connect.CodeOf(err), err)
			}
			if reason, _ := refusal.Reason(err); reason != "caller" {
				t.Fatalf("reason %q; want caller", reason)
			}
		})
	}
}

func TestTheSocketGroupAndModeAreApplied(t *testing.T) {
	gid := os.Getegid()
	socket, stop := serveWith(t, "--socket-gid", strconv.Itoa(gid), "--socket-mode", "0640")
	fi, err := os.Stat(socket)
	if err != nil || fi.Mode().Perm() != 0o640 || int(fi.Sys().(*syscall.Stat_t).Gid) != gid {
		t.Errorf("socket: %v %v", fi, err)
	}
	if code := stop(); code != 0 {
		t.Errorf("serve ended with %d", code)
	}
}
