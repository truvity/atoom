//go:build linux && (amd64 || arm64)

package usbccid

import (
	"errors"
	"slices"
)

// USB descriptor types.
const (
	descDevice    = 0x01
	descConfig    = 0x02
	descInterface = 0x04
	descEndpoint  = 0x05
)

// ifaceDesc is one interface, alternate setting 0, as the descriptors of the device hold it.
type ifaceDesc struct {
	number int
	class  uint8
	bulk   []uint8 // the addresses of its bulk endpoints, direction bit included, in the order of the descriptors
}

// has reports whether addr is one of the bulk endpoints of the interface.
func (i *ifaceDesc) has(addr uint8) bool { return slices.Contains(i.bulk, addr) }

// layout is what the descriptors of a device say about the device and its interfaces. It is read once, at Open, and it is what a
// claim and a bulk call are held to: an interface that is not here, or not a CCID interface, is never claimed, and an endpoint that
// is not a bulk endpoint of a claimed interface is never used.
type layout struct {
	vendor, product uint16
	ifaces          []ifaceDesc
}

// find returns the interface with this number, or nil.
func (l *layout) find(n int) *ifaceDesc {
	for i := range l.ifaces {
		if l.ifaces[i].number == n {
			return &l.ifaces[i]
		}
	}
	return nil
}

// validEndpoint reports whether addr is the address of an endpoint a CCID interface can have: the number is 1 to 15 (endpoint 0 is
// the control endpoint) and the reserved bits 4 to 6 are clear.
func validEndpoint(addr uint8) bool {
	n := addr & 0x0F
	return n >= 1 && addr&0x70 == 0
}

func le16(b []byte) uint16 { return uint16(b[0]) | uint16(b[1])<<8 }

// parseLayout reads the raw descriptors that a usbfs node returns: the 18-byte device descriptor, then each configuration with its
// interfaces and endpoints. Only alternate setting 0 of an interface counts. When several configurations hold an interface with
// the same number, the first one counts. On a malformed descriptor it returns what it read before it, with the error, so that the
// caller can hold a malformed device to what is certain.
func parseLayout(b []byte) (layout, error) {
	var l layout
	if len(b) < 18 || b[0] != 18 || b[1] != descDevice {
		return l, errors.New("usbccid: descriptors do not start with a device descriptor")
	}
	l.vendor, l.product = le16(b[8:]), le16(b[10:])
	rest := b[18:]
	for len(rest) >= 4 {
		if rest[0] < 4 || int(rest[0]) > len(rest) || rest[1] != descConfig {
			return l, errors.New("usbccid: malformed configuration descriptor")
		}
		total := int(le16(rest[2:]))
		if total < int(rest[0]) || total > len(rest) {
			return l, errors.New("usbccid: configuration descriptor length out of range")
		}
		cfg := rest[:total]
		rest = rest[total:]
		ifs, err := parseConfig(cfg)
		for _, i := range ifs {
			if l.find(i.number) == nil {
				l.ifaces = append(l.ifaces, i)
			}
		}
		if err != nil {
			return l, err
		}
	}
	return l, nil
}

// parseConfig reads the interfaces of one configuration descriptor with its following descriptors.
func parseConfig(cfg []byte) ([]ifaceDesc, error) {
	if len(cfg) < 9 || int(cfg[0]) > len(cfg) || cfg[0] < 9 {
		return nil, errors.New("usbccid: configuration descriptor too short")
	}
	var (
		out []ifaceDesc
		cur *ifaceDesc
	)
	for p := cfg[cfg[0]:]; len(p) > 0; {
		n := int(p[0])
		if n < 2 || n > len(p) {
			return out, errors.New("usbccid: malformed descriptor inside configuration")
		}
		d := p[:n]
		p = p[n:]
		switch d[1] {
		case descInterface:
			if n < 9 {
				return out, errors.New("usbccid: interface descriptor too short")
			}
			cur = nil
			if d[3] == 0 { // alternate setting 0 only
				out = append(out, ifaceDesc{number: int(d[2]), class: d[5]})
				cur = &out[len(out)-1]
			}
		case descEndpoint:
			if cur == nil || n < 7 {
				continue
			}
			if addr, attrs := d[2], d[3]; attrs&3 == 2 && validEndpoint(addr) {
				cur.bulk = append(cur.bulk, addr)
			}
		}
	}
	return out, nil
}
