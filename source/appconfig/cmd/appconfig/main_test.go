package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truvity/atoom/source/appconfig"
	"github.com/truvity/atoom/source/appconfig/internal/appconfigfake"
)

const (
	app = "example-app"
	env = "example-env"
)

// useFake points the main at a fake AppConfig for the duration of the test.
func useFake(t *testing.T) *appconfigfake.Fake {
	t.Helper()
	f := appconfigfake.New()
	client, stop := f.Serve()
	t.Cleanup(stop)
	old := newClient
	t.Cleanup(func() { newClient = old })
	newClient = func(context.Context) (appconfig.API, error) { return client, nil }
	return f
}

func TestUsage(t *testing.T) {
	base := []string{"serve", "--socket", "/x", "--application", app, "--environment", env}
	for _, args := range [][]string{
		nil, {"nope"}, {"serve"}, {"serve", "--socket", "/x"}, {"serve", "--socket", "/x", "--application", app},
		{"serve", "--application", app, "--environment", env},
		append(append([]string(nil), base...), "extra"), append(append([]string(nil), base...), "--socket-mode", "zz"),
		append(append([]string(nil), base...), "--profile", "Bad=x"), append(append([]string(nil), base...), "--profile", "site="),
		append(append([]string(nil), base...), "--profile", "site=a", "--profile", "site=b"),
		{"check"}, {"check", "--application", app, "--environment", env}, {"check", "--nope"},
	} {
		var out, errb bytes.Buffer
		if code := run(args, &out, &errb); code != 2 {
			t.Errorf("run(%v) = %d, want 2 (%s)", args, code, errb.String())
		}
	}
}

// An interval AppConfig does not take is a usage error, found before a socket is made.
func TestAPollIntervalOutOfRangeIsAUsageError(t *testing.T) {
	useFake(t)
	socket := filepath.Join(t.TempDir(), "s")
	var errb bytes.Buffer
	code := run([]string{"serve", "--socket", socket, "--application", app, "--environment", env, "--poll-interval", "5s"}, &bytes.Buffer{}, &errb)
	if code != 2 {
		t.Fatalf("serve = %d, want 2 (%s)", code, errb.String())
	}
	if _, err := os.Lstat(socket); err == nil {
		t.Fatal("a socket was made for options that cannot make a driver")
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

// `check` of a document that cannot be served is a refusal: exit 3, with the reason and not AppConfig's text.
func TestCheck(t *testing.T) {
	f := useFake(t)
	f.Deploy(app, env, "abc1234", []byte("signed"))
	var out, errb bytes.Buffer
	if code := run([]string{"check", "--application", app, "--environment", env, "--name", "site", "--profile", "site=abc1234"}, &out, &errb); code != 0 ||
		!strings.Contains(out.String(), "ok: 6 bytes") {
		t.Fatalf("check = %d %q %q", code, out.String(), errb.String())
	}
	out.Reset()
	errb.Reset()
	if code := run([]string{"check", "--application", app, "--environment", env, "--name", "nothing"}, &out, &errb); code != 3 ||
		!strings.Contains(errb.String(), "no such configuration") {
		t.Fatalf("check of an absent document = %d %q", code, errb.String())
	}
	f.Fail("StartConfigurationSession", "InternalServerException")
	errb.Reset()
	if code := run([]string{"check", "--application", app, "--environment", env, "--name", "site"}, &out, &errb); code != 3 ||
		strings.Contains(errb.String(), "fake") {
		t.Fatalf("check while AppConfig fails = %d %q", code, errb.String())
	}
}
