//go:build linux

package usbccid

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// E. A fuzz target over what a sysfs tree may hold: the text of every file Discover reads, the names of the directories, and the
// filter. The tree is made up under t.TempDir(); nothing reads /sys or opens a node. With a plain `go test` only the seeds run. The
// invariants are the ones the types promise: no panic, no error for a readable directory, a result inside bounds, sorted, stable
// between two calls, honouring the filter, and the good device that is always there is found or filtered, never lost or changed.

// qaName makes a directory name that the file system takes: no slash, no NUL, not empty, not "." or "..", not too long.
func qaName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '/' || r == 0 {
			return '_'
		}
		return r
	}, s)
	if len(s) > 200 {
		s = s[:200]
	}
	if s == "" || s == "." || s == ".." {
		return "x"
	}
	return s
}

func FuzzDiscoverTree(f *testing.F) {
	// name flags: 1 = raw device name, 2 = raw interface name, 4 = raw endpoint names
	add := func(flags uint8, bus, p1 uint16, p2 uint8, vendor, product, busnum, devnum, class, ifnum, addr1, attr1, addr2, attr2 string, rawName, rawIf, rawEp string, portFilter string, vendorFilter uint16) {
		f.Add(flags, bus, p1, p2, vendor, product, busnum, devnum, class, ifnum, addr1, attr1, addr2, attr2, rawName, rawIf, rawEp, portFilter, vendorFilter)
	}
	add(0, 1, 2, 4, "1234\n", "5678\n", "1\n", "7\n", "0b\n", "00\n", "02\n", "02\n", "82\n", "02\n", "", "", "", "", 0)
	add(0, 1, 2, 0, "abcd", "0001", "3", "100", "0B", "ff", "01", "02", "81", "02", "", "", "", "1-2", 0xABCD)
	add(0, 12, 3, 5, " 1234 \n", "\t5678", "99999999999999999999", "0", "11", "100", "zz", "-1", "", "0x", "", "", "", "12-3.5", 1)
	add(1, 0, 0, 0, "1234", "5678", "1", "1", "0b", "00", "02", "02", "82", "02", "1-1", "", "", "", 0)
	add(1, 0, 0, 0, "1234", "5678", "1", "1", "0b", "00", "02", "02", "82", "02", "usb1", "usb1:1.0", "ep_", "usb1", 0)
	add(3, 0, 0, 0, "1234", "5678", "1", "1", "0b", "00", "02", "02", "82", "02", "1-1", "1-1:1.0", "ep_02", "1-1", 0x1234)
	add(7, 0, 0, 0, "ffff", "ffff", "65535", "65535", "0b", "00", "02", "02", "82", "03", "1-1.1.1.1", "1-1.1.1.1:9.9", "ep_8", "1-1.1.1.1", 0xFFFF)
	add(7, 0, 0, 0, "\x00", "\n\n", "1\x00", "\r", "0b\x00", "", "ÿ", "\xff\xfe", strings.Repeat("1", 5000), "0", "..", "a/b", "ep_/../..", "../..", 0)
	add(0, 65535, 65535, 255, "0000", "0000", "65535", "65535", "0b", "00", "0f", "02", "8f", "02", "", "", "", "", 0)

	f.Fuzz(func(t *testing.T, flags uint8, bus, p1 uint16, p2 uint8, vendor, product, busnum, devnum, class, ifnum, addr1, attr1, addr2, attr2 string,
		rawName, rawIf, rawEp string, portFilter string, vendorFilter uint16) {
		scan := filepath.Join(t.TempDir(), "devices")
		if err := os.MkdirAll(scan, 0o700); err != nil {
			t.Skip(err)
		}
		write := func(dir, name, content string) {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Skip(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
				t.Skip(err)
			}
		}
		// The device that is always there and always good.
		const goodPort = "9-9"
		gdir := filepath.Join(scan, goodPort)
		write(gdir, "idVendor", "1234\n")
		write(gdir, "idProduct", "0001\n")
		write(gdir, "busnum", "9\n")
		write(gdir, "devnum", "9\n")
		write(filepath.Join(gdir, goodPort+":1.0"), "bInterfaceClass", "0b\n")
		write(filepath.Join(gdir, goodPort+":1.0"), "bInterfaceNumber", "00\n")
		for _, e := range [][3]string{{"ep_02", "02\n", "02\n"}, {"ep_81", "81\n", "02\n"}} {
			write(filepath.Join(gdir, goodPort+":1.0", e[0]), "bEndpointAddress", e[1])
			write(filepath.Join(gdir, goodPort+":1.0", e[0]), "bmAttributes", e[2])
		}
		// The device of the fuzzer.
		name := fmt.Sprintf("%d-%d", bus, p1)
		if p2 != 0 {
			name += "." + strconv.Itoa(int(p2))
		}
		if flags&1 != 0 {
			name = qaName(rawName)
		}
		if name == goodPort {
			t.Skip("the name of the good device")
		}
		ifName := name + ":1." + strconv.Itoa(int(p2))
		if flags&2 != 0 {
			ifName = qaName(rawIf)
		}
		ep1, ep2 := "ep_"+qaName(addr1), "ep_"+qaName(addr2)
		if flags&4 != 0 {
			ep1 = qaName(rawEp)
		}
		ddir := filepath.Join(scan, name)
		write(ddir, "idVendor", vendor)
		write(ddir, "idProduct", product)
		write(ddir, "busnum", busnum)
		write(ddir, "devnum", devnum)
		idir := filepath.Join(ddir, ifName)
		write(idir, "bInterfaceClass", class)
		write(idir, "bInterfaceNumber", ifnum)
		write(filepath.Join(idir, ep1), "bEndpointAddress", addr1)
		write(filepath.Join(idir, ep1), "bmAttributes", attr1)
		if ep2 != ep1 {
			write(filepath.Join(idir, ep2), "bEndpointAddress", addr2)
			write(filepath.Join(idir, ep2), "bmAttributes", attr2)
		}

		const devDir = "/made/up/dev"
		flt := Filter{VendorID: vendorFilter, PortPath: portFilter}
		got, err := Discover(scan, devDir, flt)
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		if len(got) > 2 {
			t.Fatalf("%d candidates from two devices: %+v", len(got), got)
		}
		again, _ := Discover(scan, devDir, flt)
		if !reflect.DeepEqual(got, again) {
			t.Fatalf("two calls differ: %+v / %+v", got, again)
		}
		sawGood := false
		for i, c := range got {
			if !devicePath.MatchString(c.PortPath) || c.PortPath == "" {
				t.Fatalf("port path %q is not a device in a port", c.PortPath)
			}
			if c.Bus < 1 || c.Bus > 65535 || c.Address < 1 || c.Address > 65535 {
				t.Fatalf("bus %d address %d out of bounds", c.Bus, c.Address)
			}
			if want := filepath.Join(devDir, fmt.Sprintf("%03d", c.Bus), fmt.Sprintf("%03d", c.Address)); c.NodePath != want {
				t.Fatalf("node path %q, want %q", c.NodePath, want)
			}
			if c.Interface < 0 || c.Interface > 255 {
				t.Fatalf("interface %d", c.Interface)
			}
			if c.BulkOut == 0 || c.BulkOut&0x80 != 0 || c.BulkIn&0x80 == 0 || (c.InterruptIn != 0 && c.InterruptIn&0x80 == 0) {
				t.Fatalf("endpoints %#x %#x %#x", c.BulkOut, c.BulkIn, c.InterruptIn)
			}
			if flt.PortPath != "" && c.PortPath != flt.PortPath {
				t.Fatalf("port path %q passed the filter %q", c.PortPath, flt.PortPath)
			}
			if flt.VendorID != 0 && c.VendorID != flt.VendorID {
				t.Fatalf("vendor %#x passed the filter %#x", c.VendorID, flt.VendorID)
			}
			if c.PortPath == goodPort {
				sawGood = true
				if c != (Candidate{VendorID: 0x1234, ProductID: 1, Bus: 9, Address: 9, NodePath: devDir + "/009/009", PortPath: goodPort, Interface: 0, BulkOut: 2, BulkIn: 0x81}) {
					t.Fatalf("the good device changed: %+v", c)
				}
			} else if c.PortPath != name {
				t.Fatalf("a candidate %q that was not made", c.PortPath)
			}
			if i > 0 {
				p := got[i-1]
				if p.Bus > c.Bus || (p.Bus == c.Bus && p.Address > c.Address) || (p.Bus == c.Bus && p.Address == c.Address && p.PortPath >= c.PortPath) {
					t.Fatalf("not sorted: %+v before %+v", p, c)
				}
			}
			// What the text of the fuzzer says, read again by the rules of the format.
			if c.PortPath == name {
				v, ok := strconv.ParseUint(strings.TrimSpace(vendor), 16, 16)
				if ok != nil || uint16(v) != c.VendorID {
					t.Fatalf("vendor text %q gave %#x", vendor, c.VendorID)
				}
				b, ok := strconv.ParseUint(strings.TrimSpace(busnum), 10, 16)
				if ok != nil || int(b) != c.Bus {
					t.Fatalf("bus text %q gave %d", busnum, c.Bus)
				}
			}
		}
		wantGood := (flt.PortPath == "" || flt.PortPath == goodPort) && (flt.VendorID == 0 || flt.VendorID == 0x1234)
		if wantGood != sawGood {
			t.Fatalf("the good device: found %v, want %v (filter %+v)", sawGood, wantGood, flt)
		}
		if !slices.IsSortedFunc(got, func(a, b Candidate) int { return a.Bus - b.Bus }) {
			t.Fatal("not sorted by bus")
		}
	})
}
