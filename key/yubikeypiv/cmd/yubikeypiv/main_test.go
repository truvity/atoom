package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truvity/atoom/key/conformance"
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

func TestUsage(t *testing.T) {
	for _, args := range [][]string{
		nil, {"nope"}, {"serve"}, {"serve", "--allow-factory-defaults"}, {"check", "extra"}, {"check"},
		{"serve", "--allow-factory-defaults", "--socket", "/x", "--admin-socket", "/y", "--node", "n"},
		{"serve", "--allow-factory-defaults", "--socket", "/x", "--admin-socket", "/y", "--node", "n", "--purposes", "/none.json"},
		{"check", "--purposes", "/none.json"},
		{"serve", "--allow-factory-defaults", "--socket", "/x", "--admin-socket", "/y", "--node", "n", "--purposes", "/none.json", "--socket-mode", "zz"},
		{"serve", "--allow-factory-defaults", "--socket", "/x", "--admin-socket", "/y", "--node", "n", "--purposes", "/none.json", "--socket-gid", "0"},
		{"serve", "--allow-factory-defaults", "--socket", "/x", "--admin-socket", "/y", "--node", "n", "--purposes", "/none.json", "--allow-any-uid", "--allow-uid", "1"},
	} {
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

// The driver knows no installation (record 0021): --environment is gone, and a host that still passes it gets a usage error.
func TestTheEnvironmentFlagIsGone(t *testing.T) {
	var errb bytes.Buffer
	code := run([]string{"serve", "--environment", "development", "--socket", "/nonexistent/s", "--admin-socket", "/nonexistent/a", "--node", "n", "--purposes", purposesFile(t)}, &bytes.Buffer{}, &errb)
	if code != 2 {
		t.Fatalf("exit %d (%s)", code, errb.String())
	}
}

// Without --allow-factory-defaults the card is named by its serial: without one the start is a usage error, before a socket or a card
// is touched.
func TestWithoutFactoryDefaultsASerialIsRequired(t *testing.T) {
	for _, args := range [][]string{
		{"check", "--purposes", purposesFile(t)},
		{"serve", "--socket", "/nonexistent/s", "--admin-socket", "/nonexistent/a", "--node", "n", "--purposes", purposesFile(t)},
	} {
		var errb bytes.Buffer
		if code := run(args, &bytes.Buffer{}, &errb); code != 2 || !strings.Contains(errb.String(), "serial") {
			t.Fatalf("%s: exit %d (%s)", args[0], code, errb.String())
		}
	}
}

// A declaration that the card cannot follow, or that is not one, is a usage error and the message holds no value of the file.
func TestADeclarationThatIsRefusedIsAUsageError(t *testing.T) {
	base := t.TempDir()
	for name, content := range map[string]string{
		"syntax":     `{"purposes":`,
		"a name":     `{"purposes":[{"name":"Secret_Name","algorithm":"ES256"}]}`,
		"no slot":    `{"purposes":[{"name":"second-intermediate","algorithm":"ES384","operations":["sign"]}]}`,
		"one slot":   `{"purposes":[{"name":"a","algorithm":"ES384","slot":"90"},{"name":"b","algorithm":"ES256","slot":"90"}]}`,
		"no purpose": `{"purposes":[]}`,
	} {
		path := filepath.Join(base, name+".json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		var errb bytes.Buffer
		code := run([]string{"check", "--allow-factory-defaults", "--purposes", path}, &bytes.Buffer{}, &errb)
		// A file that is not a declaration is refused at once (2). One that is a declaration the card cannot follow is refused by the
		// driver before a card is opened (1, with the reason of the card slot map); a build without the tag never gets so far.
		want := map[string]int{"syntax": 2, "a name": 2, "no purpose": 2, "no slot": 1, "one slot": 1}[name]
		if code != want || strings.Contains(errb.String(), "Secret_Name") || strings.Contains(errb.String(), "atoom_card") {
			t.Errorf("%s: exit %d, want %d (%s)", name, code, want, errb.String())
		}
	}
}
