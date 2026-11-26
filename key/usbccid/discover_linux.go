//go:build linux

package usbccid

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Discover reads a sysfs USB tree and returns the devices that have a CCID interface (class 0x0B) with both bulk endpoints, in
// the order of bus, then device number. It only reads files below sysfsDir: it opens no device node, claims nothing and sends
// nothing. devDir is the directory the NodePath of each candidate is derived from (a path is made, never opened).
//
// Both directories are required and are not defaulted: a test passes a made-up tree under t.TempDir(), and DiscoverHost passes
// the real ones. An entry that does not look like a USB device, or whose files are missing or malformed, is skipped and is not an
// error; only an unreadable sysfsDir is. Of a device with several CCID interfaces the first by number that has both bulk
// endpoints is described.
//
// The candidates carry no card serial number: see Candidate.
func Discover(sysfsDir, devDir string, f Filter) ([]Candidate, error) {
	if sysfsDir == "" || devDir == "" {
		return nil, errors.New("usbccid: Discover needs the sysfs directory and the device directory (DiscoverHost takes the real ones)")
	}
	entries, err := os.ReadDir(sysfsDir)
	if err != nil {
		return nil, fmt.Errorf("usbccid: read the sysfs USB tree: %w", err)
	}
	var out []Candidate
	for _, e := range entries {
		name := e.Name()
		if !devicePath.MatchString(name) {
			continue // a root hub (usb1) or an interface (1-2:1.0)
		}
		if f.PortPath != "" && f.PortPath != name {
			continue
		}
		c, ok := describe(filepath.Join(sysfsDir, name), name, devDir)
		if !ok || (f.VendorID != 0 && f.VendorID != c.VendorID) {
			continue
		}
		out = append(out, c)
	}
	slices.SortStableFunc(out, func(a, b Candidate) int {
		if a.Bus != b.Bus {
			return a.Bus - b.Bus
		}
		if a.Address != b.Address {
			return a.Address - b.Address
		}
		return strings.Compare(a.PortPath, b.PortPath)
	})
	return out, nil
}

// DiscoverHost is Discover over the real directories of this machine, DefaultSysfsDir and DefaultDevDir. It reads sysfs and opens
// nothing. A test must never call it (see the package comment).
func DiscoverHost(f Filter) ([]Candidate, error) { return Discover(DefaultSysfsDir, DefaultDevDir, f) }

// devicePath is the name sysfs gives a USB device in a port: the bus, a dash and the port numbers from the root hub down.
var devicePath = regexp.MustCompile(`^[0-9]+-[0-9]+(\.[0-9]+)*$`)

// interfaceName is the name of an interface of the device dev: <dev>:<configuration>.<interface>.
func interfaceName(dev string) *regexp.Regexp {
	return regexp.MustCompile("^" + regexp.QuoteMeta(dev) + `:[0-9]+\.[0-9]+$`)
}

// describe reads one device directory. It returns false for a device that has no usable CCID interface or whose files are not as
// sysfs writes them.
func describe(dir, name, devDir string) (Candidate, bool) {
	var c Candidate
	vid, ok1 := attr16(dir, "idVendor")
	pid, ok2 := attr16(dir, "idProduct")
	bus, ok3 := attrDecimal(dir, "busnum")
	dev, ok4 := attrDecimal(dir, "devnum")
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return c, false
	}
	c = Candidate{
		VendorID: vid, ProductID: pid,
		Bus: bus, Address: dev,
		NodePath: filepath.Join(devDir, fmt.Sprintf("%03d", bus), fmt.Sprintf("%03d", dev)),
		PortPath: name,
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return c, false
	}
	re := interfaceName(name)
	found := false
	best := -1
	for _, e := range entries {
		if !re.MatchString(e.Name()) {
			continue
		}
		in, ok := ccidInterface(filepath.Join(dir, e.Name()))
		if !ok || (found && in.number >= best) {
			continue
		}
		found, best = true, in.number
		c.Interface, c.BulkOut, c.BulkIn, c.InterruptIn = in.number, in.bulkOut, in.bulkIn, in.interruptIn
	}
	return c, found
}

type ccidIf struct {
	number                       int
	bulkOut, bulkIn, interruptIn uint8
}

// ccidInterface reads one interface directory. It is a CCID interface only when its class is 0x0B and it has a bulk OUT and a
// bulk IN endpoint.
func ccidInterface(dir string) (ccidIf, bool) {
	class, ok := attr8(dir, "bInterfaceClass")
	if !ok || class != ClassCCID {
		return ccidIf{}, false
	}
	num, ok := attr8(dir, "bInterfaceNumber")
	if !ok {
		return ccidIf{}, false
	}
	r := ccidIf{number: int(num)}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ccidIf{}, false
	}
	for _, e := range entries { // sorted by name, so the endpoints come in a fixed order
		if !strings.HasPrefix(e.Name(), "ep_") {
			continue
		}
		ep := filepath.Join(dir, e.Name())
		addr, ok1 := attr8(ep, "bEndpointAddress")
		attrs, ok2 := attr8(ep, "bmAttributes")
		if !ok1 || !ok2 {
			continue
		}
		in := addr&0x80 != 0
		switch {
		case attrs&3 == 2 && !in && r.bulkOut == 0:
			r.bulkOut = addr
		case attrs&3 == 2 && in && r.bulkIn == 0:
			r.bulkIn = addr
		case attrs&3 == 3 && in && r.interruptIn == 0:
			r.interruptIn = addr
		}
	}
	return r, r.bulkOut != 0 && r.bulkIn != 0
}

// attr8 and attr16 read the hex number of a file of dir that fits in 8 or 16 bits: sysfs writes ids and descriptor fields in hex, followed
// by a newline.
func attr8(dir, name string) (uint8, bool) {
	v, ok := attr(dir, name, 16, 8)
	return uint8(v), ok //nolint:gosec // ParseUint bounded it to 8 bits
}

func attr16(dir, name string) (uint16, bool) {
	v, ok := attr(dir, name, 16, 16)
	return uint16(v), ok //nolint:gosec // ParseUint bounded it to 16 bits
}

// attrDecimal reads the decimal number, 1 to 65535, in a file of dir (the bus number and the device number).
func attrDecimal(dir, name string) (int, bool) {
	v, ok := attr(dir, name, 10, 16)
	return int(v), ok && v > 0 //nolint:gosec // ParseUint bounded it to 16 bits
}

// attr reads the number in file name of dir, in the given base, that fits in bits.
func attr(dir, name string, base, bits int) (uint64, bool) {
	b, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // a file of the tree that the caller named
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(b)), base, bits)
	return v, err == nil
}
