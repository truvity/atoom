//go:build linux

package usbccid

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

// Every test here reads a made-up sysfs tree under t.TempDir(). None reads /sys, and none opens a node: the device directory the
// tests pass is a path that does not exist, and a test checks that Discover leaves it that way.

// ep is one endpoint of a made-up interface.
type ep struct {
	addr, attrs string // as sysfs writes them: two hex digits
}

var (
	bulkOut = ep{"02", "02"}
	bulkIn  = ep{"82", "02"}
	intrIn  = ep{"83", "03"}
	intrOut = ep{"03", "03"}
)

// ifc is one interface of a made-up device.
type ifc struct {
	class, number string // two hex digits
	eps           []ep
}

var (
	ccidIfc = func(number string) ifc { return ifc{"0b", number, []ep{bulkOut, bulkIn, intrIn}} }
	hidIfc  = func(number string) ifc { return ifc{"03", number, []ep{intrIn, intrOut}} }
)

// dev is one device of a made-up tree. Empty fields are written as a valid value; a field set to "-" is not written at all.
type dev struct {
	port     string // the name of the directory: 1-2.4
	vendor   string // four hex digits
	product  string
	bus, num string // decimal
	ifs      []ifc
}

func (d dev) withDefaults() dev {
	if d.vendor == "" {
		d.vendor = "abcd"
	}
	if d.product == "" {
		d.product = "0001"
	}
	if d.bus == "" {
		d.bus = "1"
	}
	if d.num == "" {
		d.num = "7"
	}
	return d
}

func write(t *testing.T, dir, name, value string) {
	t.Helper()
	if value == "-" {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// build makes the tree like the kernel does: the devices are real directories under <root>/devices, and the directory the library
// scans, <root>/bus/usb/devices, holds a symbolic link to each. It returns the directory to scan.
func build(t *testing.T, devs ...dev) string {
	t.Helper()
	root := t.TempDir()
	scan := filepath.Join(root, "bus", "usb", "devices")
	devices := filepath.Join(root, "devices")
	for _, p := range []string{scan, devices} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range devs {
		d = d.withDefaults()
		dir := filepath.Join(devices, d.port)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		write(t, dir, "idVendor", d.vendor)
		write(t, dir, "idProduct", d.product)
		write(t, dir, "busnum", d.bus)
		write(t, dir, "devnum", d.num)
		for _, i := range d.ifs {
			idir := filepath.Join(dir, d.port+":1."+strconv.Itoa(hexToInt(t, i.number)))
			if err := os.MkdirAll(idir, 0o700); err != nil {
				t.Fatal(err)
			}
			write(t, idir, "bInterfaceClass", i.class)
			write(t, idir, "bInterfaceNumber", i.number)
			for _, e := range i.eps {
				edir := filepath.Join(idir, "ep_"+e.addr)
				if err := os.MkdirAll(edir, 0o700); err != nil {
					t.Fatal(err)
				}
				write(t, edir, "bEndpointAddress", e.addr)
				write(t, edir, "bmAttributes", e.attrs)
			}
		}
		if err := os.Symlink(dir, filepath.Join(scan, d.port)); err != nil {
			t.Fatal(err)
		}
	}
	return scan
}

func hexToInt(t *testing.T, s string) int {
	t.Helper()
	if s == "-" {
		return 0
	}
	v, err := strconv.ParseInt(s, 16, 32)
	if err != nil {
		t.Fatal(err)
	}
	return int(v)
}

// nodeDir is the made-up device directory. It does not exist, and Discover must not create or open anything in it.
func nodeDir(t *testing.T) string { return filepath.Join(t.TempDir(), "dev-bus-usb") }

func discover(t *testing.T, scan string, f Filter) []Candidate {
	t.Helper()
	nodes := nodeDir(t)
	got, err := Discover(scan, nodes, f)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(nodes); !os.IsNotExist(err) {
		t.Errorf("Discover touched the device directory: %v", err)
	}
	return got
}

func TestDiscoverOneCCIDInterface(t *testing.T) {
	scan := build(t, dev{port: "1-2.4", vendor: "1234", product: "5678", bus: "1", num: "7", ifs: []ifc{hidIfc("00"), ccidIfc("01"), hidIfc("02")}})
	nodes := nodeDir(t)
	got, err := Discover(scan, nodes, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	want := []Candidate{{
		VendorID: 0x1234, ProductID: 0x5678, Bus: 1, Address: 7,
		NodePath: filepath.Join(nodes, "001", "007"), PortPath: "1-2.4",
		Interface: 1, BulkOut: 0x02, BulkIn: 0x82, InterruptIn: 0x83,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestDiscoverNodePathIsThreeDigitsAndTheRealOneIsUsedByDiscoverHostOnly(t *testing.T) {
	scan := build(t, dev{port: "12-3", bus: "12", num: "104", ifs: []ifc{ccidIfc("00")}})
	got, err := Discover(scan, "/dev/bus/usb", Filter{})
	if err != nil || len(got) != 1 {
		t.Fatalf("%v, %v", got, err)
	}
	if got[0].NodePath != "/dev/bus/usb/012/104" { // a path is made; nothing is opened
		t.Errorf("node path %q", got[0].NodePath)
	}
	if DefaultSysfsDir != "/sys/bus/usb/devices" || DefaultDevDir != "/dev/bus/usb" {
		t.Errorf("the real directories changed: %q, %q", DefaultSysfsDir, DefaultDevDir)
	}
}

func TestDiscoverNoCCIDInterface(t *testing.T) {
	scan := build(t,
		dev{port: "1-1", ifs: []ifc{hidIfc("00")}},
		dev{port: "1-2", num: "8", ifs: []ifc{{"08", "00", []ep{bulkOut, bulkIn}}}}, // mass storage
		dev{port: "1-3", num: "9"},                                                  // no interface at all
	)
	if got := discover(t, scan, Filter{}); len(got) != 0 {
		t.Errorf("got %+v, want none", got)
	}
}

func TestDiscoverEmptyTree(t *testing.T) {
	if got := discover(t, build(t), Filter{}); len(got) != 0 {
		t.Errorf("got %+v", got)
	}
}

func TestDiscoverTwoDevicesInOrder(t *testing.T) {
	scan := build(t,
		dev{port: "2-1", vendor: "1234", bus: "2", num: "3", ifs: []ifc{ccidIfc("00")}},
		dev{port: "1-4", vendor: "1234", bus: "1", num: "9", ifs: []ifc{ccidIfc("01")}},
		dev{port: "1-2", vendor: "4321", bus: "1", num: "5", ifs: []ifc{ccidIfc("00")}},
	)
	got := discover(t, scan, Filter{})
	var ports []string
	for _, c := range got {
		ports = append(ports, c.PortPath)
	}
	if want := []string{"1-2", "1-4", "2-1"}; !reflect.DeepEqual(ports, want) {
		t.Errorf("ports %v, want %v (by bus, then device number)", ports, want)
	}
	if got[1].Interface != 1 || got[0].VendorID != 0x4321 {
		t.Errorf("fields mixed up: %+v", got)
	}
}

func TestDiscoverSeveralInterfacesAndTwoCCID(t *testing.T) {
	// The lower CCID interface number wins, whatever the order the directories are listed in; a CCID interface without a bulk IN
	// endpoint is passed over.
	scan := build(t, dev{port: "1-5", ifs: []ifc{
		hidIfc("00"),
		{"0b", "01", []ep{bulkOut, intrIn}},
		{"0b", "03", []ep{{"05", "02"}, {"85", "02"}}},
		{"0b", "02", []ep{{"04", "02"}, {"84", "02"}, {"86", "03"}}},
	}})
	got := discover(t, scan, Filter{})
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	c := got[0]
	if c.Interface != 2 || c.BulkOut != 0x04 || c.BulkIn != 0x84 || c.InterruptIn != 0x86 {
		t.Errorf("got %+v, want interface 2 with endpoints 04 84 86", c)
	}
}

func TestDiscoverNoInterruptEndpoint(t *testing.T) {
	scan := build(t, dev{port: "1-1", ifs: []ifc{{"0b", "00", []ep{bulkOut, bulkIn}}}})
	got := discover(t, scan, Filter{})
	if len(got) != 1 || got[0].InterruptIn != 0 {
		t.Errorf("got %+v, want one candidate with no interrupt endpoint", got)
	}
}

func TestDiscoverSkipsMalformedEntries(t *testing.T) {
	good := ccidIfc("00")
	scan := build(t,
		dev{port: "1-1", ifs: []ifc{good}},                                            // the one good device
		dev{port: "1-2", vendor: "-", ifs: []ifc{good}},                               // no idVendor
		dev{port: "1-3", vendor: "zzzz", ifs: []ifc{good}},                            // not hex
		dev{port: "1-4", vendor: "10500", ifs: []ifc{good}},                           // does not fit in 16 bits
		dev{port: "1-5", product: "", bus: "0", ifs: []ifc{good}},                     // bus 0
		dev{port: "1-6", num: "0", ifs: []ifc{good}},                                  // device number 0
		dev{port: "1-7", num: "x", ifs: []ifc{good}},                                  // not a number
		dev{port: "1-8", bus: "-", ifs: []ifc{good}},                                  // no busnum
		dev{port: "1-9", ifs: []ifc{{"-", "00", []ep{bulkOut, bulkIn}}}},              // no class
		dev{port: "1-10", ifs: []ifc{{"0b", "-", []ep{bulkOut, bulkIn}}}},             // no interface number
		dev{port: "1-11", ifs: []ifc{{"0b", "00", []ep{{"02", "-"}, bulkIn}}}},        // an endpoint with no attributes: no bulk out
		dev{port: "1-12", ifs: []ifc{{"0b", "00", []ep{{"zz", "02"}, bulkIn}}}},       // an endpoint address that is not hex
		dev{port: "1-13", ifs: []ifc{{"0b", "00", []ep{{"82", "02"}, {"83", "02"}}}}}, // two bulk IN, no bulk OUT
		dev{port: "1-14", ifs: []ifc{{"0b", "00", []ep{{"02", "03"}, bulkIn}}}},       // an interrupt OUT is not a bulk OUT
	)
	// Things in the directory that are not devices.
	for _, name := range []string{"usb1", "1-1:1.0", "garbage", "1-", "-1", "1-2.", "a-b"} {
		if err := os.MkdirAll(filepath.Join(scan, name), 0o700); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(scan, name), "idVendor", "1234")
	}
	if err := os.WriteFile(filepath.Join(scan, "1-99"), []byte("a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := discover(t, scan, Filter{})
	if len(got) != 1 || got[0].PortPath != "1-1" {
		t.Errorf("got %+v, want only 1-1", got)
	}
}

func TestDiscoverFilters(t *testing.T) {
	scan := build(t,
		dev{port: "1-1", vendor: "1234", num: "2", ifs: []ifc{ccidIfc("00")}},
		dev{port: "1-2", vendor: "1234", num: "3", ifs: []ifc{ccidIfc("00")}},
		dev{port: "1-3", vendor: "4321", num: "4", ifs: []ifc{ccidIfc("00")}},
		dev{port: "1-3.1", vendor: "1234", num: "5", ifs: []ifc{ccidIfc("00")}},
	)
	ports := func(f Filter) []string {
		var out []string
		for _, c := range discover(t, scan, f) {
			out = append(out, c.PortPath)
		}
		return out
	}
	cases := []struct {
		name string
		f    Filter
		want []string
	}{
		{"none", Filter{}, []string{"1-1", "1-2", "1-3", "1-3.1"}},
		{"vendor", Filter{VendorID: 0x1234}, []string{"1-1", "1-2", "1-3.1"}},
		{"other vendor", Filter{VendorID: 0x4321}, []string{"1-3"}},
		{"unknown vendor", Filter{VendorID: 0x0001}, nil},
		{"port", Filter{PortPath: "1-2"}, []string{"1-2"}},
		{"port is exact, not a prefix", Filter{PortPath: "1-3"}, []string{"1-3"}},
		{"port of a hub below", Filter{PortPath: "1-3.1"}, []string{"1-3.1"}},
		{"unknown port", Filter{PortPath: "1-9"}, nil},
		{"vendor and port", Filter{VendorID: 0x1234, PortPath: "1-3.1"}, []string{"1-3.1"}},
		{"vendor and port that disagree", Filter{VendorID: 0x4321, PortPath: "1-2"}, nil},
	}
	for _, c := range cases {
		if got := ports(c.f); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDiscoverErrors(t *testing.T) {
	if _, err := Discover("", "/dev/bus/usb", Filter{}); err == nil {
		t.Error("an empty sysfs directory was accepted: it must never default to the real tree")
	}
	if _, err := Discover(t.TempDir(), "", Filter{}); err == nil {
		t.Error("an empty device directory was accepted")
	}
	if _, err := Discover(filepath.Join(t.TempDir(), "missing"), "/dev/bus/usb", Filter{}); !os.IsNotExist(unwrapAll(err)) {
		t.Errorf("a missing sysfs directory: %v", err)
	}
}

func unwrapAll(err error) error {
	for err != nil {
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return err
		}
		err = u.Unwrap()
	}
	return err
}
