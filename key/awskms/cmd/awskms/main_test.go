package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/truvity/atoom/key/awskms"
	"github.com/truvity/atoom/key/awskms/internal/kmsfake"
	"github.com/truvity/atoom/key/conformance"
)

func TestUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"nope"}, {"serve"}, {"serve", "--socket", "/x", "--socket-mode", "zz"}, {"serve", "--socket", "/x", "extra"}} {
		var out, errb bytes.Buffer
		if code := run(args, &out, &errb); code != 2 {
			t.Errorf("run(%v) = %d, want 2 (%s)", args, code, errb.String())
		}
	}
}

func TestVersion(t *testing.T) {
	// `version` and `--version` say the same, and the release smoke runs the second (tools/release/smoke.sh).
	for _, arg := range []string{"version", "--version"} {
		var out bytes.Buffer
		if code := run([]string{arg}, &out, &bytes.Buffer{}); code != 0 || strings.TrimSpace(out.String()) == "" {
			t.Fatalf("%s: %d %q", arg, code, out.String())
		}
	}
}

// useFake points the main at a fake KMS for the duration of the test.
func useFake(t *testing.T) *kmsfake.Fake {
	t.Helper()
	f := kmsfake.New()
	client, stop := f.Serve()
	t.Cleanup(stop)
	old := newClient
	t.Cleanup(func() { newClient = old })
	newClient = func(context.Context) (awskms.API, error) { return client, nil }
	return f
}

func TestABadAliasPrefixIsAUsageError(t *testing.T) {
	useFake(t)
	var errb bytes.Buffer
	code := run([]string{"serve", "--socket", filepath.Join(t.TempDir(), "s"), "--alias-prefix", "aws/x"}, &bytes.Buffer{}, &errb)
	if code != 2 {
		t.Fatalf("exit %d (%s)", code, errb.String())
	}
}

// The main serves the library on a socket and the kit passes against it, over h2c and over HTTP/1.1.
func TestTheKitPassesAgainstTheSocket(t *testing.T) {
	f := useFake(t)
	dir, err := os.MkdirTemp("", "k")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")
	done := make(chan int, 1)
	go func() {
		done <- run([]string{"serve", "--socket", socket, "--create-keys", "--alias-prefix", "ci/kit"}, &bytes.Buffer{}, &bytes.Buffer{})
	}()
	deadline := time.Now().Add(5 * time.Second)
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
	fi, err := os.Stat(socket)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode: %v %v", fi, err)
	}
	for name, c := range map[string]conformance.Client{"h2c": conformance.Dial(socket), "http1": conformance.DialHTTP1(socket)} {
		var out bytes.Buffer
		if err := conformance.Run(context.Background(), c, &out); err != nil {
			t.Errorf("%s: %v\n%s", name, err, out.String())
		}
	}
	if f.KeyByAlias("alias/ci/kit/token-signing") == nil {
		t.Error("the keys were not made under the alias prefix of the flag")
	}
	p, _ := os.FindProcess(os.Getpid())
	_ = p.Signal(os.Interrupt)
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("serve ended with %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Error("serve did not end")
	}
}
