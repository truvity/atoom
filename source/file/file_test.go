package file_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/truvity/atoom/source/file"
)

// put writes a document below dir, making the directories on the way.
func put(t *testing.T, dir, rel string, data []byte) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func link(t *testing.T, target, at string) {
	t.Helper()
	if err := os.Symlink(target, at); err != nil {
		t.Fatal(err)
	}
}

func open(t *testing.T, dir string) *file.Dir {
	t.Helper()
	d, err := file.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// answer is the text of the refusal of a read, or "ok".
func answer(d *file.Dir, name string) string {
	_, err := d.Read(name)
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, file.ErrName):
		return "name"
	case errors.Is(err, file.ErrAbsent):
		return "absent"
	case errors.Is(err, file.ErrBound):
		return "bound"
	default:
		return "io: " + err.Error()
	}
}

func TestTheBytesComeBackAsTheyAreWithTheSHA384OfTheFileAsTheLabel(t *testing.T) {
	root := t.TempDir()
	d := open(t, root)
	put(t, root, "boundary.jws", []byte(`{"payload":"x"}`))
	a, err := d.Read("boundary")
	if err != nil {
		t.Fatal(err)
	}
	if string(a.Data) != `{"payload":"x"}` {
		t.Fatalf("the bytes changed: %q", a.Data)
	}
	if len(a.Version) != 96 || strings.Trim(a.Version, "0123456789abcdef") != "" {
		t.Fatalf("the label is not 96 lower-case hex digits: %q", a.Version)
	}
	// SHA-384 of the empty input, to pin the algorithm: another hash of the same length would pass the check above.
	put(t, root, "empty.jws", nil)
	e, err := d.Read("empty")
	if err != nil {
		t.Fatal(err)
	}
	const sha384Empty = "38b060a751ac96384cd9327eb1b1e36a21fdb71114be07434c0cc7bf63f6e1da274edebfe76f65fbd51ad2f14898b95b"
	if e.Version != sha384Empty {
		t.Fatalf("label of an empty document = %s, want the SHA-384 of nothing", e.Version)
	}
	again, _ := d.Read("boundary")
	if again.Version != a.Version {
		t.Fatal("one content, two labels")
	}
	put(t, root, "boundary.jws", []byte(`{"payload":"y"}`))
	if b, _ := d.Read("boundary"); b.Version == a.Version {
		t.Fatal("two contents, one label")
	}
}

func TestAnAbsentDocumentIsAbsentAndHasNoLabel(t *testing.T) {
	d := open(t, t.TempDir())
	if got := answer(d, "upstream"); got != "absent" {
		t.Fatalf("answer = %s", got)
	}
	v, there, err := d.Version("upstream")
	if err != nil || there || v != "" {
		t.Fatalf("Version of an absent document = %q, %v, %v", v, there, err)
	}
}

func TestANameIsAKindsKebabCaseNameAndNothingElse(t *testing.T) {
	d := open(t, t.TempDir())
	for _, bad := range []string{"", "../boundary", "a/b", "a.b", "Boundary", "1a", "a_b", "-a", strings.Repeat("a", 64), "a\x00b", "ä"} {
		if got := answer(d, bad); got != "name" {
			t.Errorf("%q: %s, want name", bad, got)
		}
	}
	for _, ok := range []string{"a", "boundary", "key-plan", "a1", strings.Repeat("a", 63)} {
		if !file.ValidName(ok) {
			t.Errorf("%q is a valid name", ok)
		}
	}
}

func TestAConfigMapTreeOfLinksIsFollowedAndItsSwapIsSeen(t *testing.T) {
	// The layout of a mounted ConfigMap: name -> ..data/name, ..data -> ..<stamp>.
	root := t.TempDir()
	put(t, root, "..2026_11_04/boundary.jws", []byte("v1"))
	link(t, "..2026_11_04", filepath.Join(root, "..data"))
	link(t, "..data/boundary.jws", filepath.Join(root, "boundary.jws"))
	d := open(t, root)
	first, err := d.Read("boundary")
	if err != nil || string(first.Data) != "v1" {
		t.Fatalf("read through the tree: %v %q", err, first.Data)
	}
	// The swap of the update: ..data now names another directory.
	put(t, root, "..2026_11_05/boundary.jws", []byte("v2"))
	link(t, "..2026_11_05", filepath.Join(root, "..data.tmp"))
	if err := os.Rename(filepath.Join(root, "..data.tmp"), filepath.Join(root, "..data")); err != nil {
		t.Fatal(err)
	}
	second, err := d.Read("boundary")
	if err != nil || string(second.Data) != "v2" || second.Version == first.Version {
		t.Fatalf("after the swap: %v %q", err, second.Data)
	}
}

func TestLinksAndDotDotThatStayBelowTheDirectoryAreFollowed(t *testing.T) {
	root := t.TempDir()
	put(t, root, "real/a.jws", []byte("a"))
	if err := os.MkdirAll(filepath.Join(root, "sub/deep"), 0o700); err != nil {
		t.Fatal(err)
	}
	link(t, "sub/deep/../../real/a.jws", filepath.Join(root, "viadots.jws"))
	link(t, "../real/a.jws", filepath.Join(root, "sub/up.jws"))
	link(t, "sub/up.jws", filepath.Join(root, "twice.jws"))
	d := open(t, root)
	for _, n := range []string{"viadots", "twice"} {
		if got := answer(d, n); got != "ok" {
			t.Errorf("%s: %s", n, got)
		}
	}
}

func TestALinkThatLeavesTheDirectoryIsRefusedHoweverItGoes(t *testing.T) {
	out := t.TempDir()
	put(t, out, "secret.jws", []byte("s"))
	tmp := t.TempDir()
	// Every escape below reaches a file that exists, so a refusal is not an absence.
	put(t, tmp, "secret.jws", []byte("s"))
	base := filepath.Join(tmp, "conf")
	if err := os.MkdirAll(filepath.Join(base, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A relative target above the directory.
	link(t, "../secret.jws", filepath.Join(base, "up.jws"))
	// An absolute target, even one that names a file below the directory.
	link(t, filepath.Join(out, "secret.jws"), filepath.Join(base, "abs.jws"))
	put(t, base, "real.jws", []byte("r"))
	link(t, filepath.Join(base, "real.jws"), filepath.Join(base, "absinside.jws"))
	// A directory link above, and a file reached through it.
	link(t, "../..", filepath.Join(base, "sub/parent"))
	link(t, "sub/parent/secret.jws", filepath.Join(base, "viadir.jws"))
	// More `..` than the depth, in the middle of a path.
	link(t, "sub/../../secret.jws", filepath.Join(base, "mid.jws"))
	// A chain whose last link is the escape.
	link(t, "chain2.jws", filepath.Join(base, "chain1.jws"))
	link(t, "../secret.jws", filepath.Join(base, "chain2.jws"))
	d := open(t, base)
	for _, n := range []string{"up", "abs", "absinside", "viadir", "mid", "chain1"} {
		if got := answer(d, n); got != "bound" {
			t.Errorf("%s: %s, want bound", n, got)
		}
	}
	if got := answer(d, "real"); got != "ok" {
		t.Errorf("real: %s", got)
	}
}

func TestALoopOfLinksIsRefused(t *testing.T) {
	root := t.TempDir()
	link(t, "b.jws", filepath.Join(root, "a.jws"))
	link(t, "a.jws", filepath.Join(root, "b.jws"))
	link(t, "self.jws", filepath.Join(root, "self.jws"))
	d := open(t, root)
	for _, n := range []string{"a", "self"} {
		if got := answer(d, n); got != "bound" {
			t.Errorf("%s: %s, want bound", n, got)
		}
	}
}

// os.Root follows 8 links; the Rust driver follows 40. A mounted ConfigMap has two, so nothing real is near either limit.
func TestAChainOfLinksIsFollowedUpToTheLimitOfOsRoot(t *testing.T) {
	root := t.TempDir()
	put(t, root, "end.jws", []byte("e"))
	prev := "end.jws"
	for i := range 12 {
		name := "l" + string(rune('a'+i)) + ".jws"
		link(t, prev, filepath.Join(root, name))
		prev = name
	}
	d := open(t, root)
	if got := answer(d, "la"); got != "ok" {
		t.Errorf("one link: %s", got)
	}
	if got := answer(d, "ld"); got != "ok" {
		t.Errorf("four links: %s", got)
	}
	if got := answer(d, "ll"); got != "bound" {
		t.Errorf("twelve links: %s, want bound", got)
	}
}

func TestADirectoryOrALinkToOneIsNotADocument(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dir.jws"), 0o700); err != nil {
		t.Fatal(err)
	}
	link(t, "dir.jws", filepath.Join(root, "linked.jws"))
	d := open(t, root)
	for _, n := range []string{"dir", "linked"} {
		if got := answer(d, n); got != "bound" {
			t.Errorf("%s: %s, want bound", n, got)
		}
	}
}

func TestADocumentOverTheLimitIsNotReadAndOneAtTheLimitIs(t *testing.T) {
	root := t.TempDir()
	d := open(t, root)
	for name, size := range map[string]int64{"at-limit": file.DocumentMax, "over-limit": file.DocumentMax + 1} {
		f, err := os.Create(filepath.Join(root, name+".jws")) //nolint:gosec // a path of the test's own temporary directory
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(size); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	if got := answer(d, "at-limit"); got != "ok" {
		t.Errorf("at the limit: %s", got)
	}
	if got := answer(d, "over-limit"); got != "bound" {
		t.Errorf("over the limit: %s, want bound", got)
	}
}

// A FIFO with no writer blocks a plain open for ever: the open must not hang, and the FIFO is refused.
func TestAFifoIsRefusedAtOnceAndDoesNotHangTheOpen(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "boundary.jws"), 0o600); err != nil {
		t.Fatal(err)
	}
	link(t, "boundary.jws", filepath.Join(root, "upstream.jws"))
	d := open(t, root)
	for _, n := range []string{"boundary", "upstream"} {
		got := make(chan string, 1)
		go func() { got <- answer(d, n) }()
		select {
		case a := <-got:
			if a != "bound" {
				t.Errorf("%s: %s, want bound", n, a)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: the open of a FIFO hung", n)
		}
	}
}

func TestADirectoryThatIsMissingOrIsAFileIsNotOpened(t *testing.T) {
	root := t.TempDir()
	if _, err := file.Open(filepath.Join(root, "nothing")); err == nil {
		t.Error("a missing directory was opened")
	}
	put(t, root, "boundary.jws", []byte("x"))
	if _, err := file.Open(filepath.Join(root, "boundary.jws")); err == nil {
		t.Error("a file was opened as the directory")
	}
}

// A document that is not JSON, not UTF-8, or empty comes back byte for byte: the driver never parses.
func TestADocumentThatIsNotJSONOrUTF8ComesBackByteForByte(t *testing.T) {
	root := t.TempDir()
	d := open(t, root)
	for name, bytes := range map[string][]byte{
		"boundary":       []byte("{\"payload\": [1, 2,\n"),
		"key-plan":       []byte("  \xff\xfe\x00 not utf-8, leading and trailing space \r\n\n"),
		"token-lifetime": {},
	} {
		put(t, root, name+".jws", bytes)
		got, err := d.Read(name)
		if err != nil || string(got.Data) != string(bytes) || got.Version == "" {
			t.Errorf("%s: %v %q", name, err, got.Data)
		}
	}
}
