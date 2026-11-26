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
// A layout exists only for SIMPLE descriptors: one configuration; its length and bNumInterfaces as declared; every interface at
// alternate setting 0 only, and no number twice; as many endpoint descriptors after each interface as its bNumEndpoints says; and no
// endpoint address twice in the configuration. This layer serves one known kind of device, so it refuses everything else at Open
// (ErrBadDescriptors, ErrSeveralConfigurations) instead of copying the rules the kernel applies to what is left open (the active
// configuration, which usbfs does not name; the fall back to another alternate setting; the choice among duplicates and the
// endpoints beyond a count). With nothing left for either side to choose, the layout cannot differ from the kernel's view of who
// owns an endpoint.
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

// parseLayout reads the raw descriptors that a usbfs node returns: the 18-byte device descriptor and the one configuration with its
// interfaces and endpoints, and accepts only simple descriptors (see layout). A refusal is an error that names the rule and holds
// no byte of the descriptors; it is ErrSeveralConfigurations for the one case of a device that declares other than one
// configuration, and a plain error for the rest (Open wraps it in ErrBadDescriptors). Descriptors of other types between the
// interface and endpoint descriptors (class-specific ones such as HID and CCID, interface associations) are skipped by their length,
// wherever they stand.
func parseLayout(b []byte) (layout, error) {
	var l layout
	if len(b) < 18 || b[0] != 18 || b[1] != descDevice {
		return l, errors.New("the data does not start with a device descriptor")
	}
	l.vendor, l.product = le16(b[8:]), le16(b[10:])
	if b[17] != 1 {
		return l, ErrSeveralConfigurations
	}
	cfg := b[18:]
	if len(cfg) < 9 || cfg[0] < 9 || int(cfg[0]) > len(cfg) || cfg[1] != descConfig {
		return l, errors.New("no configuration descriptor follows the device descriptor")
	}
	if int(le16(cfg[2:])) != len(cfg) {
		return l, errors.New("the length of the configuration is not the number of bytes present")
	}
	ifs, err := parseConfig(cfg)
	if err != nil {
		return l, err
	}
	if int(cfg[4]) != len(ifs) {
		return l, errors.New("bNumInterfaces is not the number of interfaces listed")
	}
	l.ifaces = ifs
	return l, nil
}

// parseConfig reads the interfaces of one configuration descriptor and the descriptors after it, under the rules of a simple
// configuration.
func parseConfig(cfg []byte) ([]ifaceDesc, error) {
	var (
		out  []ifaceDesc
		want []int // bNumEndpoints of each interface of out
		seen = map[uint8]bool{}
	)
	for p := cfg[cfg[0]:]; len(p) > 0; {
		n := int(p[0])
		if n < 2 || n > len(p) {
			return nil, errors.New("a descriptor is shorter than its header or runs past the end")
		}
		d := p[:n]
		p = p[n:]
		switch d[1] {
		case descInterface:
			if n < 9 {
				return nil, errors.New("an interface descriptor is shorter than 9 bytes")
			}
			if d[3] != 0 {
				return nil, errors.New("an interface has an alternate setting other than 0")
			}
			if slices.ContainsFunc(out, func(i ifaceDesc) bool { return i.number == int(d[2]) }) {
				return nil, errors.New("an interface number is listed twice")
			}
			out = append(out, ifaceDesc{number: int(d[2]), class: d[5]})
			want = append(want, int(d[4]))
		case descEndpoint:
			if n < 7 {
				return nil, errors.New("an endpoint descriptor is shorter than 7 bytes")
			}
			if len(out) == 0 {
				return nil, errors.New("an endpoint descriptor stands before any interface")
			}
			if !validEndpoint(d[2]) {
				return nil, errors.New("an endpoint address is not a number 1 to 15 with the reserved bits clear")
			}
			if seen[d[2]] {
				return nil, errors.New("an endpoint address is listed twice")
			}
			seen[d[2]] = true
			cur := &out[len(out)-1]
			cur.eps = append(cur.eps, endpoint{addr: d[2], attrs: d[3]})
		}
	}
	for i := range out {
		if len(out[i].eps) != want[i] {
			return nil, errors.New("an interface has not as many endpoint descriptors as its bNumEndpoints")
		}
	}
	return out, nil
}
