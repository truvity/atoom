//go:build linux && (amd64 || arm64)

package usbccid

import (
	"errors"
	"fmt"
	"slices"
)

// USB descriptor types.
const (
	descDevice    = 0x01
	descConfig    = 0x02
	descInterface = 0x04
	descEndpoint  = 0x05
)

// endpoint is one endpoint descriptor of an interface, of any transfer type.
type endpoint struct {
	addr  uint8 // bEndpointAddress, direction bit included
	attrs uint8 // bmAttributes
}

// key is the address as the kernel looks an endpoint up: the number and the direction bit. The reserved bits do not count, so that a
// descriptor with a reserved bit set still owns the address it would have without it.
func (e endpoint) key() uint8 { return e.addr & 0x8F }

// bulk reports whether the endpoint is a bulk endpoint with an address a CCID interface can have.
func (e endpoint) bulk() bool { return e.attrs&3 == 2 && validEndpoint(e.addr) }

// ifaceDesc is one interface at alternate setting 0, as the descriptors of the device hold it, with EVERY endpoint of it, whatever
// the transfer type, up to the bNumEndpoints that the kernel counts.
type ifaceDesc struct {
	number int
	class  uint8
	eps    []endpoint
}

// layout is what the descriptors of a device say about the device and its interfaces. It is read once, at Open (a snapshot), and it
// is what a claim and a bulk call are held to: an interface that is not here, or not a CCID interface, is never claimed, and an
// endpoint that is not a bulk endpoint of exactly one interface, which is claimed, is never used.
//
// A device that declares more than one configuration (bNumConfigurations other than 1) has no usable layout: the kernel knows only
// its active configuration, and usbfs does not say which one that is, so this layer cannot know which interfaces and endpoints the
// kernel will claim. Claim and every bulk call are refused for such a device (ErrSeveralConfigurations); the layer does not choose.
type layout struct {
	vendor, product uint16
	configs         int // bNumConfigurations of the device descriptor
	ifaces          []ifaceDesc
}

// several reports whether the layout is unusable because the device declares other than one configuration.
func (l *layout) several() bool { return l.configs != 1 }

// find returns the interface with this number, or nil.
func (l *layout) find(n int) *ifaceDesc {
	for i := range l.ifaces {
		if l.ifaces[i].number == n {
			return &l.ifaces[i]
		}
	}
	return nil
}

// bulkOwner returns the one interface that owns addr as a bulk endpoint. The address must be listed exactly once among the
// endpoints of all interfaces (at alternate setting 0, of any type) and be a bulk endpoint there: an address that two interfaces
// list, or that one interface lists twice, is ambiguous (the kernel would pick an interface by itself), and an address that is not
// listed, or is listed as another type, is no bulk endpoint. Both give ErrBadEndpoint.
func (l *layout) bulkOwner(addr uint8) (*ifaceDesc, error) {
	var owner *ifaceDesc
	var ep endpoint
	n := 0
	for i := range l.ifaces {
		for _, e := range l.ifaces[i].eps {
			if e.key() == addr&0x8F {
				owner, ep = &l.ifaces[i], e
				n++
			}
		}
	}
	switch {
	case n == 0:
		return nil, ErrBadEndpoint
	case n > 1:
		return nil, fmt.Errorf("%w: the address is listed %d times by the interfaces of the device", ErrBadEndpoint, n)
	case !ep.bulk() || ep.addr != addr:
		return nil, ErrBadEndpoint
	}
	return owner, nil
}

// validEndpoint reports whether addr is the address of an endpoint a CCID interface can have: the number is 1 to 15 (endpoint 0 is
// the control endpoint) and the reserved bits 4 to 6 are clear.
func validEndpoint(addr uint8) bool {
	n := addr & 0x0F
	return n >= 1 && addr&0x70 == 0
}

func le16(b []byte) uint16 { return uint16(b[0]) | uint16(b[1])<<8 }

// parseLayout reads the raw descriptors that a usbfs node returns: the 18-byte device descriptor, then the configuration with its
// interfaces and endpoints. It follows the rules the kernel follows, because what matters is who owns an endpoint for the kernel:
//   - only alternate setting 0 of an interface counts;
//   - of several interface descriptors with the same number and alternate setting 0 in the configuration, the first counts;
//   - of the endpoint descriptors after an interface descriptor, at most bNumEndpoints count, and the rest are ignored.
//
// A device with other than one configuration is not read further (see layout): the layout holds the ids and the count only. Any
// malformed descriptor is an error, and the layout is then not to be used.
func parseLayout(b []byte) (layout, error) {
	var l layout
	if len(b) < 18 || b[0] != 18 || b[1] != descDevice {
		return l, errors.New("usbccid: descriptors do not start with a device descriptor")
	}
	l.vendor, l.product, l.configs = le16(b[8:]), le16(b[10:]), int(b[17])
	if l.several() {
		return l, nil
	}
	rest := b[18:]
	if len(rest) < 4 || rest[0] < 9 || int(rest[0]) > len(rest) || rest[1] != descConfig {
		return l, errors.New("usbccid: malformed configuration descriptor")
	}
	total := int(le16(rest[2:]))
	if total < int(rest[0]) || total > len(rest) {
		return l, errors.New("usbccid: configuration descriptor length out of range")
	}
	ifs, err := parseConfig(rest[:total])
	l.ifaces = ifs
	return l, err
}

// parseConfig reads the interfaces of one configuration descriptor with its following descriptors.
func parseConfig(cfg []byte) ([]ifaceDesc, error) {
	var (
		out    []ifaceDesc
		cur    *ifaceDesc // the interface whose endpoints are being read; nil when they are to be ignored
		budget int        // the endpoint descriptors of cur that still count (bNumEndpoints)
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
			cur, budget = nil, 0
			if d[3] != 0 { // alternate setting 0 only
				continue
			}
			if slices.ContainsFunc(out, func(i ifaceDesc) bool { return i.number == int(d[2]) }) {
				continue // the first descriptor of a number counts, as in the kernel
			}
			out = append(out, ifaceDesc{number: int(d[2]), class: d[5]})
			cur, budget = &out[len(out)-1], int(d[4])
		case descEndpoint:
			if n < 7 {
				return out, errors.New("usbccid: endpoint descriptor too short")
			}
			if cur == nil || budget == 0 {
				continue
			}
			budget--
			cur.eps = append(cur.eps, endpoint{addr: d[2], attrs: d[3]})
		}
	}
	return out, nil
}
