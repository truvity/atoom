//go:build linux

package usbccid

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// QA tests of Discover. Every tree is made up under t.TempDir(). Nothing here reads /sys, opens a node or calls DiscoverHost (which is
// only named in a compile-time assertion below).

// DiscoverHost exists with this signature; it is never called by a test.
var _ func(Filter) ([]Candidate, error) = DiscoverHost

// qaTree writes a made-up sysfs tree with exact control over what each file holds (no newline is added).
type qaTree struct {
	t    *testing.T
	scan string
}

func newQATree(t *testing.T) *qaTree {
	t.Helper()
	scan := filepath.Join(t.TempDir(), "devices")
	if err := os.MkdirAll(scan, 0o700); err != nil {
		t.Fatal(err)
	}
	return &qaTree{t: t, scan: scan}
}

func (q *qaTree) file(dir, name, content string) {
	q.t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		q.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		q.t.Fatal(err)
	}
}

// device writes a device directory in the scan directory and returns its path. A field "-" is not written.
func (q *qaTree) device(name, vendor, product, bus, num string) string {
	q.t.Helper()
	dir := filepath.Join(q.scan, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		q.t.Fatal(err)
	}
	for _, f := range [][2]string{{"idVendor", vendor}, {"idProduct", product}, {"busnum", bus}, {"devnum", num}} {
		if f[1] != "-" {
			q.file(dir, f[0], f[1])
		}
	}
	return dir
}

// good writes a device with a complete CCID interface 0 (bulk 02, 82, interrupt 83).
func (q *qaTree) good(name, bus, num string) string {
	q.t.Helper()
	dir := q.device(name, "1234\n", "5678\n", bus+"\n", num+"\n")
	q.iface(dir, name+":1.0", "0b\n", "00\n", [][3]string{{"ep_02", "02\n", "02\n"}, {"ep_82", "82\n", "02\n"}, {"ep_83", "83\n", "03\n"}})
	return dir
}

// iface writes an interface directory with endpoints {dir name, address, attributes}.
func (q *qaTree) iface(devDir, name, class, num string, eps [][3]string) string {
	q.t.Helper()
	dir := filepath.Join(devDir, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		q.t.Fatal(err)
	}
	if class != "-" {
		q.file(dir, "bInterfaceClass", class)
	}
	if num != "-" {
		q.file(dir, "bInterfaceNumber", num)
	}
	for _, e := range eps {
		edir := filepath.Join(dir, e[0])
		if err := os.MkdirAll(edir, 0o700); err != nil {
			q.t.Fatal(err)
		}
		if e[1] != "-" {
			q.file(edir, "bEndpointAddress", e[1])
		}
		if e[2] != "-" {
			q.file(edir, "bmAttributes", e[2])
		}
	}
	return dir
}

func (q *qaTree) discover(f Filter) []Candidate {
	q.t.Helper()
	got, err := Discover(q.scan, filepath.Join(q.t.TempDir(), "nodes"), f)
	if err != nil {
		q.t.Fatal(err)
	}
	return got
}

func portsOf(cs []Candidate) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.PortPath)
	}
	return out
}

// snapshot lists every path under root with its type, size and modification time, without following links.
func snapshot(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s %v %d %d\n", strings.TrimPrefix(p, root), info.Mode(), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// A. The text of a file: what sysfs writes (hex, a newline) and everything it does not.

func TestQAAttributeText(t *testing.T) {
	type tc struct {
		text string
		want uint64 // the value, when ok
		ok   bool
	}
	hex16 := []tc{
		{"1234\n", 0x1234, true}, {"1234", 0x1234, true}, {" 1234 \n", 0x1234, true}, {"\n1234\n\n", 0x1234, true},
		{"\t1234\r\n", 0x1234, true}, {"ABCD\n", 0xABCD, true}, {"abcd\n", 0xABCD, true}, {"0001\n", 1, true}, {"1\n", 1, true},
		{"0\n", 0, true}, {"0000\n", 0, true}, {"00001234\n", 0x1234, true}, {"ffff\n", 0xFFFF, true},
		{"10000\n", 0, false}, {"0x1234\n", 0, false}, {"+1234\n", 0, false}, {"-1234\n", 0, false}, {"-0\n", 0, false}, {"12 34\n", 0, false},
		{"12\n34\n", 0, false}, {"", 0, false}, {"\n", 0, false}, {"   \n", 0, false}, {"1234\x00", 0, false}, {"\x001234", 0, false},
		{"12_34\n", 0, false}, {"١٢٣٤\n", 0, false}, {"1234 # note\n", 0, false}, {"g\n", 0, false},
		{"ffffffffffffffffffffffff\n", 0, false}, {strings.Repeat("1", 1<<16), 0, false}, {"1234\n5678\n", 0, false},
	}
	for _, c := range hex16 {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "v"), []byte(c.text), 0o600); err != nil {
			t.Fatal(err)
		}
		if v, ok := attr16(dir, "v"); ok != c.ok || (ok && uint64(v) != c.want) {
			t.Errorf("attr16(%q) = %#x, %v; want %#x, %v", c.text, v, ok, c.want, c.ok)
		}
		// The same text through the 8-bit reader: ok only when the value fits in a byte.
		v8, ok8 := attr8(dir, "v")
		fits := c.ok && c.want <= 0xFF
		if ok8 != fits || (ok8 && uint64(v8) != c.want) {
			t.Errorf("attr8(%q) = %#x, %v; want %#x, %v", c.text, v8, ok8, c.want, fits)
		}
	}
	dec := []tc{
		{"1\n", 1, true}, {"001\n", 1, true}, {"65535\n", 65535, true}, {" 3\n", 3, true}, {"010\n", 10, true}, {"127", 127, true},
		{"0\n", 0, false}, {"00\n", 0, false}, {"65536\n", 0, false}, {"-1\n", 0, false}, {"+1\n", 0, false}, {"1.5\n", 0, false},
		{"0x10\n", 0, false}, {"1e2\n", 0, false}, {"99999999999999999999\n", 0, false}, {"", 0, false}, {"ff\n", 0, false},
		{"1 2\n", 0, false}, {"1_000\n", 0, false},
	}
	for _, c := range dec {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "n"), []byte(c.text), 0o600); err != nil {
			t.Fatal(err)
		}
		if v, ok := attrDecimal(dir, "n"); ok != c.ok || (ok && uint64(v) != c.want) { //nolint:gosec // a small test value
			t.Errorf("attrDecimal(%q) = %d, %v; want %d, %v", c.text, v, ok, c.want, c.ok)
		}
	}
	if _, ok := attr16(t.TempDir(), "missing"); ok {
		t.Error("a missing file was read")
	}
	if _, ok := attrDecimal(filepath.Join(t.TempDir(), "no-dir"), "n"); ok {
		t.Error("a missing directory was read")
	}
}

// The same texts through Discover, in the files of a device: a device whose file is not as sysfs writes it is skipped, not an error.
func TestQAFieldsOfADeviceThroughDiscover(t *testing.T) {
	q := newQATree(t)
	q.good("1-1", "1", "2")
	bad := []struct{ name, vendor, product, bus, num string }{
		{"1-2", "0x1234", "5678", "1", "3"}, {"1-3", "1234", "56 78", "1", "4"}, {"1-4", "1234", "5678", "65536", "5"},
		{"1-5", "1234", "5678", "1", "-"}, {"1-6", "1234", "5678", "1", "0"}, {"1-7", "1234", "", "1", "7"},
		{"1-8", "1234", "5678", "1", "99999999999999999999999"}, {"1-9", "\n", "5678", "1", "9"}, {"1-10", "1234", "5678", " ", "10"},
		{"1-11", "12345", "5678", "1", "11"}, {"1-12", "1234", "5678", "1.0", "12"},
	}
	for _, b := range bad {
		dir := q.device(b.name, b.vendor, b.product, b.bus, b.num)
		q.iface(dir, b.name+":1.0", "0b\n", "00\n", [][3]string{{"ep_02", "02\n", "02\n"}, {"ep_82", "82\n", "02\n"}})
	}
	if got := portsOf(q.discover(Filter{})); !reflect.DeepEqual(got, []string{"1-1"}) {
		t.Errorf("got %v, want only 1-1", got)
	}
	// Texts that sysfs may carry and that are fine.
	q2 := newQATree(t)
	dir := q2.device("3-4.5.6", " 0AbC \n", "\t00ff", " 007\r\n", "0042")
	q2.iface(dir, "3-4.5.6:1.0", "0B", "0f \n", [][3]string{{"ep_01", "01 \n", " 2"}, {"ep_81", "\n81\n", "02\n"}})
	got := q2.discover(Filter{})
	if len(got) != 1 || got[0].VendorID != 0x0ABC || got[0].ProductID != 0xFF || got[0].Bus != 7 || got[0].Address != 42 ||
		got[0].Interface != 15 || got[0].BulkOut != 1 || got[0].BulkIn != 0x81 || got[0].PortPath != "3-4.5.6" {
		t.Errorf("got %+v", got)
	}
}

// The interface fields: the class must be 0x0B exactly, the number must be a byte, and the endpoint files must be bytes.
func TestQAInterfaceAndEndpointText(t *testing.T) {
	good := [][3]string{{"ep_02", "02\n", "02\n"}, {"ep_82", "82\n", "02\n"}}
	cases := []struct {
		name       string
		class, num string
		eps        [][3]string
		want       bool
		iface      int
	}{
		{"class 0b", "0b\n", "00\n", good, true, 0},
		{"class 0B", "0B\n", "00\n", good, true, 0},
		{"class b", "b\n", "00\n", good, true, 0},
		{"class 11 is not 0b", "11\n", "00\n", good, false, 0},
		{"class 0x0b", "0x0b\n", "00\n", good, false, 0},
		{"class 10b does not fit a byte", "10b\n", "00\n", good, false, 0},
		{"class 8b", "8b\n", "00\n", good, false, 0},
		{"class empty", "\n", "00\n", good, false, 0},
		{"number ff", "0b\n", "ff\n", good, true, 255},
		{"number 100 does not fit a byte", "0b\n", "100\n", good, false, 0},
		{"number -1", "0b\n", "-1\n", good, false, 0},
		{"number text", "0b\n", "one\n", good, false, 0},
		{"endpoint address 3 digits", "0b\n", "00\n", [][3]string{{"ep_02", "102\n", "02\n"}, {"ep_82", "82\n", "02\n"}}, false, 0},
		{"endpoint attribute 3 digits", "0b\n", "00\n", [][3]string{{"ep_02", "02\n", "102\n"}, {"ep_82", "82\n", "02\n"}}, false, 0},
		{"endpoint attribute with the sync bits set", "0b\n", "00\n", [][3]string{{"ep_02", "02\n", "0a\n"}, {"ep_82", "82\n", "0e\n"}}, true, 0},
		{"endpoint without a file", "0b\n", "00\n", [][3]string{{"ep_02", "-", "02\n"}, {"ep_82", "82\n", "02\n"}}, false, 0},
		{"no endpoint", "0b\n", "00\n", nil, false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := newQATree(t)
			dir := q.device("1-1", "1234\n", "5678\n", "1\n", "2\n")
			q.iface(dir, "1-1:1.0", c.class, c.num, c.eps)
			got := q.discover(Filter{})
			if (len(got) == 1) != c.want || len(got) > 1 {
				t.Fatalf("got %+v, want a candidate: %v", got, c.want)
			}
			if c.want && got[0].Interface != c.iface {
				t.Errorf("interface %d, want %d", got[0].Interface, c.iface)
			}
		})
	}
}

// A. Endpoints: which one is taken for each role.
func TestQAEndpointRoles(t *testing.T) {
	type eps = [][3]string
	cases := []struct {
		name                         string
		eps                          eps
		ok                           bool
		bulkOut, bulkIn, interruptIn uint8
	}{
		{"all three", eps{{"ep_01", "01\n", "02\n"}, {"ep_81", "81\n", "02\n"}, {"ep_82", "82\n", "03\n"}}, true, 0x01, 0x81, 0x82},
		{"highest addresses", eps{{"ep_0f", "0f\n", "02\n"}, {"ep_8f", "8f\n", "02\n"}}, true, 0x0F, 0x8F, 0},
		{"interrupt IN absent", eps{{"ep_02", "02\n", "02\n"}, {"ep_82", "82\n", "02\n"}}, true, 0x02, 0x82, 0},
		{"interrupt OUT only is no interrupt IN", eps{{"ep_02", "02\n", "02\n"}, {"ep_82", "82\n", "02\n"}, {"ep_03", "03\n", "03\n"}}, true, 0x02, 0x82, 0},
		{"bulk OUT only", eps{{"ep_02", "02\n", "02\n"}}, false, 0, 0, 0},
		{"bulk IN only", eps{{"ep_82", "82\n", "02\n"}}, false, 0, 0, 0},
		{"interrupt only", eps{{"ep_83", "83\n", "03\n"}}, false, 0, 0, 0},
		{"isochronous and control are not bulk", eps{{"ep_02", "02\n", "01\n"}, {"ep_82", "82\n", "00\n"}}, false, 0, 0, 0},
		{"two bulk IN: the first by name", eps{{"ep_02", "02\n", "02\n"}, {"ep_83", "83\n", "02\n"}, {"ep_81", "81\n", "02\n"}}, true, 0x02, 0x81, 0},
		{"two bulk OUT: the first by name", eps{{"ep_04", "04\n", "02\n"}, {"ep_03", "03\n", "02\n"}, {"ep_82", "82\n", "02\n"}}, true, 0x03, 0x82, 0},
		{"two interrupt IN: the first by name", eps{{"ep_02", "02\n", "02\n"}, {"ep_82", "82\n", "02\n"}, {"ep_86", "86\n", "03\n"}, {"ep_85", "85\n", "03\n"}}, true, 0x02, 0x82, 0x85},
		{"a directory that is not ep_ is not an endpoint", eps{{"endpoint1", "02\n", "02\n"}, {"ep_82", "82\n", "02\n"}}, false, 0, 0, 0},
		{"ep_ with an empty rest", eps{{"ep_", "02\n", "02\n"}, {"ep_82", "82\n", "02\n"}}, true, 0x02, 0x82, 0},
		{"control endpoint 0 is ignored", eps{{"ep_00", "00\n", "00\n"}, {"ep_02", "02\n", "02\n"}, {"ep_82", "82\n", "02\n"}}, true, 0x02, 0x82, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := newQATree(t)
			dir := q.device("2-3", "1234\n", "5678\n", "2\n", "9\n")
			q.iface(dir, "2-3:1.1", "0b\n", "01\n", c.eps)
			got := q.discover(Filter{})
			if !c.ok {
				if len(got) != 0 {
					t.Fatalf("got %+v, want none", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("got %+v", got)
			}
			g := got[0]
			if g.BulkOut != c.bulkOut || g.BulkIn != c.bulkIn || g.InterruptIn != c.interruptIn || g.Interface != 1 {
				t.Errorf("bulk out %#x in %#x interrupt %#x interface %d; want %#x %#x %#x 1", g.BulkOut, g.BulkIn, g.InterruptIn, g.Interface, c.bulkOut, c.bulkIn, c.interruptIn)
			}
			if g.BulkOut&0x80 != 0 || g.BulkIn&0x80 == 0 || (g.InterruptIn != 0 && g.InterruptIn&0x80 == 0) {
				t.Errorf("the direction bits are wrong: %+v", g)
			}
		})
	}
}

// A. Names in a device directory that look like interfaces but are not this device's: they are not read.
func TestQAInterfaceNamesAreAnchored(t *testing.T) {
	q := newQATree(t)
	dir := q.device("1-1", "1234\n", "5678\n", "1\n", "2\n")
	ccid := [][3]string{{"ep_02", "02\n", "02\n"}, {"ep_82", "82\n", "02\n"}}
	for i, name := range []string{"1-1:1.0.0", "1-1:1.", "1-1:.0", "1-1:a.b", "1-1:1.0x", "x1-1:1.0", "1-1.1:1.0", "1-1:1-0", "1-1:1.0 ", "11-1:1.0", "1-1", ":1.0", "1-1:1.0\n"} {
		q.iface(dir, name, "0b\n", fmt.Sprintf("%02x\n", i), ccid)
	}
	if got := q.discover(Filter{}); len(got) != 0 {
		t.Errorf("got %+v, want none: no directory of the device is an interface of it", got)
	}
	// One real interface among them is found, and it is the one taken.
	q.iface(dir, "1-1:1.7", "0b\n", "07\n", ccid)
	if got := q.discover(Filter{}); len(got) != 1 || got[0].Interface != 7 {
		t.Errorf("got %+v", got)
	}
}

// A. Entries that are not what they should be (files for folders, folders for files, links that dangle or loop).
func TestQAWrongKindsOfEntry(t *testing.T) {
	q := newQATree(t)
	q.good("1-1", "1", "1")
	// A device that is a file.
	q.file(q.scan, "1-2", "device")
	// A device whose attribute is a directory.
	d := q.good("1-3", "1", "3")
	_ = os.Remove(filepath.Join(d, "idVendor"))
	if err := os.Mkdir(filepath.Join(d, "idVendor"), 0o700); err != nil {
		t.Fatal(err)
	}
	// An interface that is a file.
	d = q.device("1-4", "1234\n", "5678\n", "1\n", "4\n")
	q.file(d, "1-4:1.0", "interface")
	// An interface whose class is a directory.
	d = q.device("1-5", "1234\n", "5678\n", "1\n", "5\n")
	idir := q.iface(d, "1-5:1.0", "-", "00\n", [][3]string{{"ep_02", "02\n", "02\n"}, {"ep_82", "82\n", "02\n"}})
	if err := os.Mkdir(filepath.Join(idir, "bInterfaceClass"), 0o700); err != nil {
		t.Fatal(err)
	}
	// An endpoint that is a file (the bulk IN is then missing).
	d = q.device("1-6", "1234\n", "5678\n", "1\n", "6\n")
	idir = q.iface(d, "1-6:1.0", "0b\n", "00\n", [][3]string{{"ep_02", "02\n", "02\n"}})
	q.file(idir, "ep_82", "endpoint")
	// An endpoint whose attribute file is a directory.
	d = q.device("1-7", "1234\n", "5678\n", "1\n", "7\n")
	idir = q.iface(d, "1-7:1.0", "0b\n", "00\n", [][3]string{{"ep_02", "02\n", "02\n"}, {"ep_82", "82\n", "-"}})
	if err := os.Mkdir(filepath.Join(idir, "ep_82", "bmAttributes"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Links: dangling, a loop on itself, two that point at each other, one that points at the scan directory, one to a file.
	for name, target := range map[string]string{"1-8": filepath.Join(q.scan, "gone"), "1-9": "1-9", "1-10": "1-11", "1-11": "1-10", "1-12": q.scan, "1-13": filepath.Join(q.scan, "1-2")} {
		if err := os.Symlink(target, filepath.Join(q.scan, name)); err != nil {
			t.Fatal(err)
		}
	}
	// Links inside a device: an interface that is its own parent, one that points at the device, an endpoint that is a loop.
	d = q.device("1-14", "1234\n", "5678\n", "1\n", "14\n")
	for name, target := range map[string]string{"1-14:1.0": ".", "1-14:1.1": "..", "1-14:1.2": "1-14:1.2", "1-14:1.3": filepath.Join(q.scan, "nowhere")} {
		if err := os.Symlink(target, filepath.Join(d, name)); err != nil {
			t.Fatal(err)
		}
	}
	d = q.device("1-15", "1234\n", "5678\n", "1\n", "15\n")
	idir = q.iface(d, "1-15:1.0", "0b\n", "00\n", [][3]string{{"ep_82", "82\n", "02\n"}})
	if err := os.Symlink("ep_02", filepath.Join(idir, "ep_02")); err != nil {
		t.Fatal(err)
	}
	done := make(chan []Candidate, 1)
	go func() { done <- q.discover(Filter{}) }()
	select {
	case got := <-done:
		if !reflect.DeepEqual(portsOf(got), []string{"1-1"}) {
			t.Errorf("got %v, want only 1-1", portsOf(got))
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Discover did not end over links that loop")
	}
}

// A symbolic link that leaves the scan directory is followed: that is how sysfs is laid out (the entries of /sys/bus/usb/devices link
// to ../../../devices/...), and it is the only way a device is seen. The links of the real layout work; a link out of the tree to a
// directory that has no device in it is not an error.
func TestQALinksOutOfTheTreeAreTheSysfsLayout(t *testing.T) {
	root := t.TempDir()
	scan := filepath.Join(root, "bus", "usb", "devices")
	outside := filepath.Join(root, "devices", "pci0", "usb1", "1-2")
	if err := os.MkdirAll(scan, 0o700); err != nil {
		t.Fatal(err)
	}
	q := &qaTree{t: t, scan: filepath.Dir(outside)}
	d := q.device("1-2", "1234\n", "5678\n", "1\n", "2\n")
	q.iface(d, "1-2:1.0", "0b\n", "00\n", [][3]string{{"ep_02", "02\n", "02\n"}, {"ep_82", "82\n", "02\n"}})
	if err := os.Symlink(filepath.Join("..", "..", "..", "devices", "pci0", "usb1", "1-2"), filepath.Join(scan, "1-2")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "..", "..", "devices", "pci0", "usb1", "none"), filepath.Join(scan, "1-3")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "..", "..", "devices", "pci0"), filepath.Join(scan, "1-4")); err != nil {
		t.Fatal(err)
	}
	got, err := Discover(scan, filepath.Join(root, "nodes"), Filter{})
	if err != nil || !reflect.DeepEqual(portsOf(got), []string{"1-2"}) {
		t.Errorf("got %+v, %v", got, err)
	}
	// The scan directory itself may be a link.
	link := filepath.Join(root, "scan-link")
	if err := os.Symlink(scan, link); err != nil {
		t.Fatal(err)
	}
	if got, err := Discover(link, filepath.Join(root, "nodes"), Filter{}); err != nil || len(got) != 1 {
		t.Errorf("through a link to the scan directory: %+v, %v", got, err)
	}
}

// A. A real sysfs listing: root hubs, hubs, interfaces of every device in the same flat directory, and the devices below a hub
// physically inside the hub's directory. Only the device with a CCID interface is a candidate, and the hub is not given the
// interface of its child.
func TestQASysfsLayoutWithHubs(t *testing.T) {
	root := t.TempDir()
	phys := filepath.Join(root, "devices", "pci0")
	scan := filepath.Join(root, "bus", "usb", "devices")
	if err := os.MkdirAll(scan, 0o700); err != nil {
		t.Fatal(err)
	}
	q := &qaTree{t: t, scan: phys}
	ccid := [][3]string{{"ep_02", "02\n", "02\n"}, {"ep_82", "82\n", "02\n"}, {"ep_83", "83\n", "03\n"}}
	hubEP := [][3]string{{"ep_81", "81\n", "03\n"}}
	// The root hub of bus 1, itself carrying a CCID-looking interface: the name usb1 is not a device in a port.
	rh := q.device("usb1", "1d6b\n", "0002\n", "1\n", "1\n")
	q.iface(rh, "usb1:1.0", "0b\n", "00\n", ccid)
	q.iface(rh, "1-0:1.0", "09\n", "00\n", hubEP)
	// A hub in port 2, with no CCID interface, and a CCID device in port 2.4 below it, a directory inside the hub's directory.
	hub := q.device("1-2", "05e3\n", "0608\n", "1\n", "3\n")
	q.iface(hub, "1-2:1.0", "09\n", "00\n", hubEP)
	child := filepath.Join(hub, "1-2.4")
	q2 := &qaTree{t: t, scan: hub}
	q2.device("1-2.4", "1234\n", "5678\n", "1\n", "7\n")
	q2.iface(child, "1-2.4:1.0", "03\n", "00\n", [][3]string{{"ep_81", "81\n", "03\n"}})
	q2.iface(child, "1-2.4:1.1", "0b\n", "01\n", ccid)
	// A second device that holds a CCID interface under another bus.
	other := &qaTree{t: t, scan: phys}
	d := other.device("2-1", "4321\n", "0001\n", "2\n", "2\n")
	other.iface(d, "2-1:1.0", "0b\n", "00\n", ccid)
	// The flat listing of /sys/bus/usb/devices: a link to each device, each interface and each root hub.
	links := map[string]string{
		"usb1":      rh,
		"1-0:1.0":   filepath.Join(rh, "1-0:1.0"),
		"1-2":       hub,
		"1-2:1.0":   filepath.Join(hub, "1-2:1.0"),
		"1-2.4":     child,
		"1-2.4:1.0": filepath.Join(child, "1-2.4:1.0"),
		"1-2.4:1.1": filepath.Join(child, "1-2.4:1.1"),
		"2-1":       d,
		"2-1:1.0":   filepath.Join(d, "2-1:1.0"),
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(scan, name)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Discover(scan, "/made/up/dev", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(portsOf(got), []string{"1-2.4", "2-1"}) {
		t.Fatalf("got %+v, want 1-2.4 and 2-1", got)
	}
	if got[0].Interface != 1 || got[0].Address != 7 || got[0].NodePath != "/made/up/dev/001/007" || got[1].NodePath != "/made/up/dev/002/002" {
		t.Errorf("fields: %+v", got)
	}
	// The same, with the hub alone as the port filter: it has no CCID interface of its own.
	if got, _ := Discover(scan, "/made/up/dev", Filter{PortPath: "1-2"}); len(got) != 0 {
		t.Errorf("the hub 1-2 was taken for a CCID device: %+v", got)
	}
}

// A. The filters.
func TestQAFilterPortPathIsExactAndNeverATraversal(t *testing.T) {
	q := newQATree(t)
	names := []string{"1-2", "1-2.4", "1-2.45", "1-2.4.1", "1-20", "11-2", "1-2.40", "10-2"}
	for i, n := range names {
		q.good(n, "1", strconv.Itoa(i+1))
	}
	for _, n := range names {
		got := portsOf(q.discover(Filter{PortPath: n}))
		if !reflect.DeepEqual(got, []string{n}) {
			t.Errorf("port %q: got %v", n, got)
		}
		// Combined with the vendor of that device, and with another vendor.
		if got := portsOf(q.discover(Filter{PortPath: n, VendorID: 0x1234})); !reflect.DeepEqual(got, []string{n}) {
			t.Errorf("port %q with its vendor: got %v", n, got)
		}
		if got := q.discover(Filter{PortPath: n, VendorID: 0x1235}); len(got) != 0 {
			t.Errorf("port %q with another vendor: got %v", n, got)
		}
	}
	for _, n := range []string{"1-", "1-2.", "1", "-2", "1-2 ", " 1-2", "1-2\n", "1-2:1.0", "usb1", "../1-2", "./1-2", "1-2/", "/1-2", "1-2/../1-2.4", "1-*", "1-2.4*", "1-2.", "1-2.4\x00", "..", "."} {
		if got := q.discover(Filter{PortPath: n}); len(got) != 0 {
			t.Errorf("port %q matched %+v", n, got)
		}
	}
	// A vendor filter alone, and a vendor 0 device with the zero filter.
	if got := q.discover(Filter{VendorID: 0x1234}); len(got) != len(names) {
		t.Errorf("vendor filter: %d of %d", len(got), len(names))
	}
	d := q.device("5-5", "0000\n", "0000\n", "5\n", "5\n")
	q.iface(d, "5-5:1.0", "0b\n", "00\n", [][3]string{{"ep_02", "02\n", "02\n"}, {"ep_82", "82\n", "02\n"}})
	if got := q.discover(Filter{}); len(got) != len(names)+1 {
		t.Errorf("the zero filter lets the vendor 0 device through: %d", len(got))
	}
	if got := q.discover(Filter{VendorID: 0x1234}); len(got) != len(names) {
		t.Errorf("a vendor filter must not let the vendor 0 device through: %d", len(got))
	}
}

// A. The derived node path, for bus and device numbers of one, two and three digits, and beyond.
func TestQANodePath(t *testing.T) {
	cases := []struct {
		bus, num int
		want     string
	}{
		{1, 1, "001/001"}, {1, 9, "001/009"}, {9, 9, "009/009"}, {1, 10, "001/010"}, {10, 1, "010/001"}, {1, 99, "001/099"}, {99, 99, "099/099"},
		{1, 100, "001/100"}, {100, 100, "100/100"}, {1, 127, "001/127"}, {127, 128, "127/128"}, {255, 255, "255/255"}, {1, 999, "001/999"},
		{999, 1, "999/001"}, {1000, 1, "1000/001"}, {1, 1000, "001/1000"}, {65535, 65535, "65535/65535"},
	}
	for _, devDir := range []string{"/made/up/dev", "/made/up/dev/", "/made//up/./dev//", "relative/dev", "with space/dev dir"} {
		q := newQATree(t)
		for i, c := range cases {
			name := fmt.Sprintf("%d-%d", i+1, i+1)
			q.good(name, strconv.Itoa(c.bus), strconv.Itoa(c.num))
		}
		got, err := Discover(q.scan, devDir, Filter{})
		if err != nil || len(got) != len(cases) {
			t.Fatalf("%q: %v %v", devDir, got, err)
		}
		for _, g := range got {
			var want string
			for _, c := range cases {
				if c.bus == g.Bus && c.num == g.Address {
					want = filepath.Join(devDir, c.want)
				}
			}
			if g.NodePath != want {
				t.Errorf("devDir %q bus %d address %d: node path %q, want %q", devDir, g.Bus, g.Address, g.NodePath, want)
			}
		}
	}
	if _, err := os.Stat("relative"); err == nil {
		t.Error("a relative device directory was created in the working directory")
	}
}

// A. The order is by bus, then device number as numbers (not as text), then port path; it does not depend on the order in which the
// entries exist or are listed, and repeating the call gives the same answer.
func TestQAOrderIsStable(t *testing.T) {
	type d struct{ port, bus, num string }
	devs := []d{{"2-1", "2", "1"}, {"10-1", "10", "1"}, {"1-3", "1", "100"}, {"1-1", "1", "9"}, {"1-2", "1", "10"}, {"1-10", "1", "50"}, {"1-9", "1", "50"}, {"3-1", "3", "7"}, {"1-4", "1", "9"}}
	want := []string{"1-1", "1-4", "1-2", "1-10", "1-9", "1-3", "2-1", "3-1", "10-1"}
	for seed := range len(devs) * 2 { // every rotation of the list, forwards and backwards
		shuffled := slices.Clone(devs)
		if seed%2 == 1 {
			slices.Reverse(shuffled)
		}
		shuffled = append(shuffled[seed/2:], shuffled[:seed/2]...)
		q := newQATree(t)
		for _, x := range shuffled {
			q.good(x.port, x.bus, x.num)
		}
		first, err := Discover(q.scan, "/made/up", Filter{})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(portsOf(first), want) {
			t.Fatalf("seed %d: order %v, want %v", seed, portsOf(first), want)
		}
		for range 3 {
			again, _ := Discover(q.scan, "/made/up", Filter{})
			if !reflect.DeepEqual(first, again) {
				t.Fatalf("seed %d: two calls differ", seed)
			}
		}
	}
}

// A. Many entries: Discover ends, quickly, and returns what is there.
func TestQAManyEntries(t *testing.T) {
	q := newQATree(t)
	const junk, plain, withCCID, manyIfaces, manyEPs = 3000, 800, 200, 1500, 1000
	for i := range junk {
		switch i % 3 {
		case 0:
			q.file(q.scan, fmt.Sprintf("usb%d", i), "x")
		case 1:
			d := filepath.Join(q.scan, fmt.Sprintf("%d-%d:1.0", i, i))
			q.file(d, "bInterfaceClass", "0b\n")
		default:
			q.file(q.scan, "garbage-"+strconv.Itoa(i), "")
		}
	}
	for i := range plain {
		q.device(fmt.Sprintf("7-%d", i+1), "1234\n", "5678\n", "7\n", strconv.Itoa(i+1)+"\n")
	}
	for i := range withCCID {
		q.good(fmt.Sprintf("8-%d", i+1), "8", strconv.Itoa(i+1))
	}
	d := q.device("9-1", "1234\n", "5678\n", "9\n", "1\n")
	for i := range manyIfaces {
		q.iface(d, fmt.Sprintf("9-1:1.%d", i+1), "03\n", fmt.Sprintf("%02x\n", i%256), nil)
	}
	q.iface(d, "9-1:1.0", "0b\n", "00\n", [][3]string{{"ep_02", "02\n", "02\n"}, {"ep_82", "82\n", "02\n"}})
	d = q.device("9-2", "1234\n", "5678\n", "9\n", "2\n")
	eps := make([][3]string, 0, manyEPs+2)
	for i := range manyEPs {
		eps = append(eps, [3]string{fmt.Sprintf("ep_x%04d", i), "00\n", "00\n"})
	}
	eps = append(eps, [3]string{"ep_zz", "02\n", "02\n"}, [3]string{"ep_zzz", "82\n", "02\n"})
	q.iface(d, "9-2:1.0", "0b\n", "00\n", eps)

	start := time.Now()
	got, err := Discover(q.scan, "/made/up", Filter{})
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != withCCID+2 {
		t.Errorf("%d candidates, want %d", len(got), withCCID+2)
	}
	if took > 20*time.Second {
		t.Errorf("Discover took %v over %d entries", took, junk+plain+withCCID+2)
	}
	if !slices.IsSortedFunc(got, func(a, b Candidate) int {
		if a.Bus != b.Bus {
			return a.Bus - b.Bus
		}
		return a.Address - b.Address
	}) {
		t.Error("the result is not sorted")
	}
	t.Logf("%d entries in %v", junk+plain+withCCID+manyIfaces+manyEPs, took)
}

// A. Discover writes nothing and opens nothing: the made-up tree is the same after the call, and a node of the device directory
// that would block an open (a named pipe with no other end) does not block the call.
func TestQADiscoverOnlyReads(t *testing.T) {
	q := newQATree(t)
	q.good("1-1", "1", "7")
	q.good("1-2.3", "1", "8")
	dev := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dev, "001"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"007", "008"} {
		if err := unix.Mkfifo(filepath.Join(dev, "001", n), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	beforeTree, beforeDev := snapshot(t, filepath.Dir(q.scan)), snapshot(t, dev)
	type result struct {
		got []Candidate
		err error
	}
	done := make(chan result, 1)
	go func() {
		got, err := Discover(q.scan, dev, Filter{})
		done <- result{got, err}
	}()
	select {
	case r := <-done:
		if r.err != nil || len(r.got) != 2 || r.got[0].NodePath != filepath.Join(dev, "001", "007") {
			t.Errorf("%+v, %v", r.got, r.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Discover blocked: it opened a node of the device directory")
	}
	if after := snapshot(t, filepath.Dir(q.scan)); after != beforeTree {
		t.Errorf("the tree changed:\n%s\n--- after ---\n%s", beforeTree, after)
	}
	if after := snapshot(t, dev); after != beforeDev {
		t.Errorf("the device directory changed:\n%s\n--- after ---\n%s", beforeDev, after)
	}
}

// A. The safety property: Discover has no default. An empty directory is an error and the result is nil, whichever of the two is
// empty and whatever the filter; a directory that cannot be read is an error too.
func TestQADiscoverNeverFallsBackToTheRealTree(t *testing.T) {
	q := newQATree(t)
	q.good("1-1", "1", "2")
	for _, f := range []Filter{{}, {VendorID: 0x1234}, {PortPath: "1-1"}} {
		for _, args := range [][2]string{{"", ""}, {"", t.TempDir()}, {q.scan, ""}, {"", "/dev/bus/usb"}, {"", DefaultDevDir}} {
			got, err := Discover(args[0], args[1], f)
			if err == nil || got != nil {
				t.Errorf("Discover(%q, %q, %+v) = %v, %v: an empty directory must be refused", args[0], args[1], f, got, err)
			}
		}
	}
	// A directory of the wrong kind or one that is not there: an error, a nil result, no fall back.
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{file, filepath.Join(t.TempDir(), "missing"), " ", "\n"} {
		got, err := Discover(p, "/made/up", Filter{})
		if err == nil || got != nil {
			t.Errorf("Discover(%q) = %v, %v", p, got, err)
		}
	}
	if _, err := Discover(filepath.Join(t.TempDir(), "missing"), "/made/up", Filter{}); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing directory: %v, want fs.ErrNotExist through the chain", err)
	}
	if unix.Geteuid() != 0 {
		locked := t.TempDir()
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o700) }) //nolint:gosec // a directory needs the execute bit to be removed
		if _, err := Discover(locked, "/made/up", Filter{}); !errors.Is(err, fs.ErrPermission) {
			t.Errorf("an unreadable directory: %v", err)
		}
	}
}

// A. The same property, held on the source: the two real directories (DefaultSysfsDir, DefaultDevDir and their paths) are named in
// the declarations of candidate.go and used in one function only, DiscoverHost. Nothing else in the package mentions /sys or
// /dev/bus, and no non-test file reads the process environment or the working directory to find a tree.
func TestQARealDirectoriesAreOnlyReachedByDiscoverHost(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var users []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fd, isFunc := decl.(*ast.FuncDecl)
			where := f
			if isFunc {
				where = f + ":" + fd.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.Ident:
					if x.Name == "DefaultSysfsDir" || x.Name == "DefaultDevDir" {
						users = append(users, where+" uses "+x.Name)
					}
				case *ast.BasicLit:
					if v, err := strconv.Unquote(x.Value); err == nil && x.Kind == token.STRING && (strings.HasPrefix(v, "/sys") || strings.HasPrefix(v, "/dev/")) {
						users = append(users, where+" has the literal "+x.Value)
					}
				case *ast.SelectorExpr:
					if id, ok := x.X.(*ast.Ident); ok && id.Name == "os" && (x.Sel.Name == "Getenv" || x.Sel.Name == "Getwd" || x.Sel.Name == "LookupEnv" || x.Sel.Name == "Environ") {
						users = append(users, where+" calls os."+x.Sel.Name)
					}
				}
				return true
			})
		}
	}
	slices.Sort(users)
	for _, u := range users {
		if strings.HasPrefix(u, "candidate.go ") || strings.HasPrefix(u, "discover_linux.go:DiscoverHost ") {
			continue
		}
		t.Errorf("unexpected reference to the real tree: %s", u)
	}
	if !slices.ContainsFunc(users, func(u string) bool {
		return strings.HasPrefix(u, "discover_linux.go:DiscoverHost uses DefaultSysfsDir")
	}) {
		t.Errorf("DiscoverHost no longer uses the default directories: %v", users)
	}
}

// A. The name of a device directory is the whole of the port path pattern: names that merely contain one, or that are interfaces,
// root hubs or anything else, are not devices even when their files are complete and good.
func TestQAScanNamesAreAnchored(t *testing.T) {
	q := newQATree(t)
	q.good("1-1", "1", "1")
	for i, name := range []string{"x1-2", "1-2x", "1-2.", ".1-2", "1-2-3", "1-2 ", " 1-2", "1-2\n", "\n1-2", "1-2.4a", "1-2..4", "usb1-2", "usb1", "1-2:1.0", "1-2.4:1.0",
		"1_2", "1–2", "١-٢", "1-2.٤", "01-02x", "1-", "-2", "1", "", "ep_1-2"} {
		if name == "" {
			continue
		}
		q.good(name, "1", strconv.Itoa(i+10))
	}
	if got := portsOf(q.discover(Filter{})); !reflect.DeepEqual(got, []string{"1-1"}) {
		t.Errorf("got %v, want only 1-1", got)
	}
	// And the shapes that are devices are all taken: leading zeros, several digits, deep paths.
	q = newQATree(t)
	for i, name := range []string{"1-2", "01-02", "10-20", "1-2.3", "1-2.3.4.5.6.7", "123-456.789", "9-0"} {
		q.good(name, "1", strconv.Itoa(i+1))
	}
	if got := q.discover(Filter{}); len(got) != 7 {
		t.Errorf("got %v, want 7 devices", portsOf(got))
	}
}
