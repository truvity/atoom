package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/truvity/atoom/key/conformance"
)

func TestUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"nope"}, {"serve"}, {"serve", "--socket", "/x"}, {"serve", "--socket", "/x", "--state-dir", "/y"}, {"check"}, {"check", "--state-dir", "/y"},
		{"serve", "--socket", "/x", "--state-dir", "/y", "--purposes", "/y/none.json"}, {"serve", "--socket", "/x", "--state-dir", "/y", "--purposes", "/y/none.json", "--socket-mode", "zz"}} {
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

func TestAnUnknownEnvironmentIsAUsageError(t *testing.T) {
	base := t.TempDir()
	var errb bytes.Buffer
	code := run([]string{"serve", "--socket", filepath.Join(base, "s"), "--state-dir", filepath.Join(base, "st"), "--purposes", purposesFile(t), "--environment", "prod"}, &bytes.Buffer{}, &errb)
	if code != 2 {
		t.Fatalf("exit %d (%s)", code, errb.String())
	}
}

func TestProductionIsRefusedOnlyWhenAsked(t *testing.T) {
	base := t.TempDir()
	var errb bytes.Buffer
	code := run([]string{"serve", "--socket", filepath.Join(base, "s"), "--state-dir", filepath.Join(base, "st"), "--purposes", purposesFile(t), "--environment", "production", "--refuse-production"}, &bytes.Buffer{}, &errb)
	if code != 1 || !strings.Contains(errb.String(), "refused in production") {
		t.Fatalf("exit %d (%s)", code, errb.String())
	}
	if _, err := os.Lstat(filepath.Join(base, "st")); err == nil {
		t.Fatal("the state directory was made")
	}
}

func TestCheckRefusesAWideDirectory(t *testing.T) {
	st := filepath.Join(t.TempDir(), "st")
	purposes := purposesFile(t)
	if err := os.Mkdir(st, 0o700); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"check", "--state-dir", st, "--purposes", purposes}, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatalf("check of an empty 0700 directory: %d", code)
	}
	if err := os.Chmod(st, 0o755); err != nil { //nolint:gosec // the point of the test
		t.Fatal(err)
	}
	if code := run([]string{"check", "--state-dir", st, "--purposes", purposes}, &bytes.Buffer{}, &bytes.Buffer{}); code != 1 {
		t.Fatalf("check of a 0755 directory: %d", code)
	}
}

// The main serves the library on a socket and the kit passes against it, over h2c and over HTTP/1.1: the conformance job of CI
// does the same with the built binaries.
func TestTheKitPassesAgainstTheSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "k")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")
	done := make(chan int, 1)
	go func() {
		done <- run([]string{"serve", "--socket", socket, "--state-dir", filepath.Join(dir, "st"), "--purposes", purposesFile(t), "--environment", "development"}, &bytes.Buffer{}, &bytes.Buffer{})
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
	// SIGTERM is the way a service manager stops it; the test cancels through the signal context of run by sending the signal.
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

// A declaration file that is not a declaration is a usage error, and the message names a word and no value of the file.
func TestADeclarationThatIsRefusedIsAUsageError(t *testing.T) {
	base := t.TempDir()
	for name, content := range map[string]string{
		"syntax":    `{"purposes":`,
		"a name":    `{"purposes":[{"name":"Secret_Name","algorithm":"ES256"}]}`,
		"algorithm": `{"purposes":[{"name":"ok","algorithm":"PS256"}]}`,
		"none":      `{"purposes":[]}`,
	} {
		path := filepath.Join(base, name+".json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		var errb bytes.Buffer
		code := run([]string{"serve", "--socket", filepath.Join(base, "s"), "--state-dir", filepath.Join(base, "st"), "--purposes", path}, &bytes.Buffer{}, &errb)
		if code != 2 || !strings.Contains(errb.String(), "--purposes") || strings.Contains(errb.String(), "Secret_Name") {
			t.Errorf("%s: exit %d (%s)", name, code, errb.String())
		}
		if _, err := os.Lstat(filepath.Join(base, "st")); err == nil {
			t.Errorf("%s: the state directory was made", name)
		}
	}
}
