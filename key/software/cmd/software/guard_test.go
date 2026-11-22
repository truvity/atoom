package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/common/refusal"
	"github.com/truvity/atoom/key/conformance"
	keyv1alpha1 "github.com/truvity/atoom/key/gen/atoom/key/v1alpha1"
	"github.com/truvity/atoom/key/purpose"
)

// purposesFile writes the declaration of the kit as the JSON file that --purposes takes, and returns its path.
func purposesFile(t *testing.T) string {
	t.Helper()
	raw, err := purpose.Marshal(purpose.MustNew(conformance.Declaration()))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "purposes.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// serveWith runs the main with the guard flags `extra` and waits for its socket. The returned function stops it (the way a service
// manager does) and says how it ended.
func serveWith(t *testing.T, extra ...string) (socket string, stop func() int) {
	t.Helper()

	dir, err := os.MkdirTemp("", "g")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket = filepath.Join(dir, "s")
	args := append([]string{"serve", "--socket", socket, "--state-dir", filepath.Join(dir, "st"), "--purposes", purposesFile(t), "--environment", "development"}, extra...)
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

func capabilities(socket string) error {
	_, err := conformance.Dial(socket).Capabilities(context.Background(), connect.NewRequest(&keyv1alpha1.CapabilitiesRequest{}))
	return err
}

func me() uint32 { return uint32(os.Geteuid()) } //nolint:gosec // a uid is non-negative

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
			err := capabilities(socket)
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
	socket, stop := serveWith(t, "--socket-gid", strconv.Itoa(gid))
	fi, err := os.Stat(socket)
	if err != nil || fi.Mode().Perm() != 0o660 || int(fi.Sys().(*syscall.Stat_t).Gid) != gid {
		t.Errorf("socket with --socket-gid: %v %v; want mode 660 and group %d", fi, err, gid)
	}
	if code := stop(); code != 0 {
		t.Errorf("serve ended with %d", code)
	}
	socket, stop = serveWith(t, "--socket-gid", strconv.Itoa(gid), "--socket-mode", "0640")
	if fi, err := os.Stat(socket); err != nil || fi.Mode().Perm() != 0o640 {
		t.Errorf("socket with --socket-mode 0640: %v %v", fi, err)
	}
	if code := stop(); code != 0 {
		t.Errorf("serve ended with %d", code)
	}
}

func TestABadGuardFlagIsAUsageError(t *testing.T) {
	for _, bad := range [][]string{{"--socket-gid", "0"}, {"--allow-uid", "root"}, {"--allow-any-uid", "--allow-uid", "1"}, {"--socket-mode", "1777"}} {
		args := append([]string{"serve", "--socket", "/x", "--state-dir", "/y", "--environment", "development"}, bad...)
		var errb bytes.Buffer
		if code := run(args, &bytes.Buffer{}, &errb); code != 2 {
			t.Errorf("run(%v) = %d, want 2 (%s)", bad, code, errb.String())
		}
	}
}
