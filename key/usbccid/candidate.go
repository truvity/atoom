package usbccid

// The real directories. They are the defaults of DiscoverHost and nothing else: Discover takes its directories as parameters, and a
// test never passes these two (see the package comment).
const (
	// DefaultSysfsDir is the sysfs directory that lists the USB devices and their interfaces.
	DefaultSysfsDir = "/sys/bus/usb/devices"
	// DefaultDevDir is the directory of the usbfs nodes: one directory per bus, one node per device (BBB/DDD).
	DefaultDevDir = "/dev/bus/usb"
)

// ClassCCID is the interface class of a smart-card (CCID) interface.
const ClassCCID = 0x0B

// Candidate is one USB device with a CCID interface, as sysfs describes it. It says nothing about the card in the reader, and
// holding a Candidate holds nothing: the device is not opened.
//
// sysfs does not give the serial number of a card (and a USB serial string, if there is one, is not what a card application calls
// its serial). A caller that must pick one card of several reads the serial from the card after it claimed the interface, and
// compares it before it sends anything else (record 0026); the vendor id and the port path only narrow the choice.
type Candidate struct {
	// VendorID and ProductID are the USB ids of the device.
	VendorID, ProductID uint16
	// Bus and Address are the bus number and the device number on that bus (sysfs busnum and devnum). The device number changes
	// each time the device enumerates, so it is only valid for the moment of the scan.
	Bus, Address int
	// NodePath is the usbfs node derived from them: <dev dir>/BBB/DDD, three digits each, for example /dev/bus/usb/001/004. It
	// is a path and nothing more: Discover does not open it.
	NodePath string
	// PortPath is the physical port path as sysfs names the device: the bus number, a dash, and the port numbers from the root
	// hub down, joined by dots (for example "1-2.4"). It stays the same while the device sits in the same port, also when its
	// device number changes.
	PortPath string
	// Interface is the interface number (bInterfaceNumber) of the CCID interface. If a device has several, the first by number.
	Interface int
	// BulkOut and BulkIn are the addresses of the bulk endpoints of the CCID interface (BulkIn has its direction bit set).
	// InterruptIn is the address of its interrupt IN endpoint, or 0 when it has none. A candidate has both bulk endpoints.
	BulkOut, BulkIn, InterruptIn uint8
}

// Filter narrows what Discover returns. The zero Filter lets every candidate through.
type Filter struct {
	// VendorID keeps the devices of this USB vendor id. Zero (no vendor has id 0) means any.
	VendorID uint16
	// PortPath keeps the device in this physical port, as Candidate.PortPath spells it. Empty means any.
	PortPath string
}

// Open opens the usbfs node of the candidate: Open(c.NodePath).
func (c Candidate) Open() (*Device, error) { return Open(c.NodePath) }
