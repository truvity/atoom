package index_test

import (
	"errors"
	"io/fs"
	"os"
	"testing"

	"github.com/truvity/atoom/index"
)

func vectors(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	root := os.DirFS("testdata/declarations")
	files, err := fs.Glob(root, dir+"/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no vector in %s: %v", dir, err)
	}
	out := map[string][]byte{}
	for _, f := range files {
		b, err := fs.ReadFile(root, f)
		if err != nil {
			t.Fatal(err)
		}
		out[f] = b
	}
	return out
}

// The same vectors as the Rust twin's: the two languages accept and refuse the same declarations.
func TestTheGoodVectorsAreAccepted(t *testing.T) {
	for name, b := range vectors(t, "good") {
		if _, err := index.ParseDeclaration(b); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestTheBadVectorsAreRefused(t *testing.T) {
	for name, b := range vectors(t, "bad") {
		_, err := index.ParseDeclaration(b)
		if !errors.Is(err, index.ErrDeclaration) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
