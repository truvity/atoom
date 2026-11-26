package ccid

import (
	"encoding/binary"
	"fmt"

	"github.com/truvity/atoom/key/usbccid"
)

// USB descriptor types.
const (
	descDevice    = 0x01
	descConfig    = 0x02
	descInterface = 0x04
	descEndpoint  = 0x05
	descCCID      = 0x21 // the CCID class descriptor
)

// classDescriptorLength is the length of the CCID class descriptor (CCID 1.1, table 5.1-1).
const classDescriptorLength = 54

// dwFeatures bits that this layer looks at (CCID 1.1, table 5.1-1).
const (
	featAutoParameters = 0x00000002 // automatic parameter configuration based on the ATR
	featTPDU           = 0x00010000 // exchange level: TPDU
	featShortAPDU      = 0x00020000 // exchange level: short APDU
	featExtAPDU        = 0x00040000 // exchange level: short and extended APDU
	featLevelMask      = featTPDU | featShortAPDU | featExtAPDU
)

// classDescriptor holds the fields of the CCID class descriptor that this layer reads. The others (clocks, data rates, the
// mechanical bits, the PIN support) are not read, and nothing here would use them.
type classDescriptor struct {
	bcdCCID          uint16
	maxSlotIndex     uint8
	voltageSupport   uint8
	protocols        uint32
	features         uint32
	maxMessageLength uint32
}

// layout is what the descriptors say about the one CCID interface that the reader uses: its number, its endpoints and its class
// descriptor. It is read once, at Open.
type layout struct {
	iface        int
	bulkOut      uint8
	bulkIn       uint8
	interruptIn  uint8 // 0 when absent; reported, never used
	maxPacketOut int
	maxPacketIn  int
	class        classDescriptor
}

// ifaceInfo is one CCID interface (class 0x0B, alternate setting 0) as the walk found it.
type ifaceInfo struct {
	number        int
	subclass      uint8
	protocol      uint8
	numEndpoints  int
	endpointsSeen int
	bulkOut       []endpointInfo
	bulkIn        []endpointInfo
	interruptIn   []endpointInfo
	classDescs    [][]byte
}

type endpointInfo struct {
	addr uint8
	mps  int
}

// parseDescriptors reads the raw descriptors that a Port gives (the 18-byte device descriptor, then the one configuration) and
// finds the CCID interface with its bulk endpoints and its class descriptor. It never guesses: an interface that cannot be chosen
// without a guess, a class descriptor that is missing, of another length, or in conflict with the interface, and anything that
// this layer does not handle are refused by name. With a candidate, the interface and the bulk endpoints of the descriptors must be
// the candidate's.
//
// The texts of the refusals name the rule that failed and the facts of the device (numbers of the descriptors). A descriptor holds no
// secret.
func parseDescriptors(b []byte, cand *usbccid.Candidate) (*layout, error) {
	ifs, err := walk(b)
	if err != nil {
		return nil, err
	}
	var withBulk []*ifaceInfo
	for i := range ifs {
		if len(ifs[i].bulkOut) > 0 && len(ifs[i].bulkIn) > 0 {
			withBulk = append(withBulk, &ifs[i])
		}
	}
	var c *ifaceInfo
	switch {
	case cand != nil:
		for _, i := range withBulk {
			if i.number == cand.Interface {
				c = i
			}
		}
		if c == nil {
			return nil, fmt.Errorf("%w: the descriptors hold no CCID interface %d with bulk endpoints, as the candidate says", usbccid.ErrWrongDevice, cand.Interface)
		}
		if len(c.bulkOut) != 1 || len(c.bulkIn) != 1 || c.bulkOut[0].addr != cand.BulkOut || c.bulkIn[0].addr != cand.BulkIn {
			return nil, fmt.Errorf("%w: the bulk endpoints of CCID interface %d are not the candidate's", usbccid.ErrWrongDevice, c.number)
		}
	case len(withBulk) == 0:
		return nil, fmt.Errorf("%w: the descriptors hold no interface of class 0x0B (alternate setting 0) with a bulk OUT and a bulk IN endpoint", ErrNoInterface)
	case len(withBulk) > 1:
		return nil, fmt.Errorf("%w: the descriptors hold %d CCID interfaces and no candidate says which one", ErrSeveralInterfaces, len(withBulk))
	default:
		c = withBulk[0]
	}
	if len(c.bulkOut) != 1 || len(c.bulkIn) != 1 {
		return nil, fmt.Errorf("%w: CCID interface %d lists %d bulk OUT and %d bulk IN endpoints, and this layer takes exactly one of each",
			usbccid.ErrBadDescriptors, c.number, len(c.bulkOut), len(c.bulkIn))
	}
	if len(c.interruptIn) > 1 {
		return nil, fmt.Errorf("%w: CCID interface %d lists %d interrupt IN endpoints", usbccid.ErrBadDescriptors, c.number, len(c.interruptIn))
	}
	if c.endpointsSeen != c.numEndpoints {
		return nil, fmt.Errorf("%w: CCID interface %d has %d endpoint descriptors and bNumEndpoints says %d", usbccid.ErrBadDescriptors, c.number, c.endpointsSeen, c.numEndpoints)
	}
	for _, e := range []endpointInfo{c.bulkOut[0], c.bulkIn[0]} {
		if e.mps < 8 || e.mps > 1024 {
			return nil, fmt.Errorf("%w: a bulk endpoint of CCID interface %d has a packet size of %d, outside 8 to 1024", usbccid.ErrBadDescriptors, c.number, e.mps)
		}
	}
	if c.subclass != 0 || c.protocol != 0 {
		return nil, fmt.Errorf("%w: CCID interface %d has subclass %d and protocol %d, and the bulk CCID transport is subclass 0 and protocol 0", ErrBadClass, c.number, c.subclass, c.protocol)
	}
	switch len(c.classDescs) {
	case 0:
		return nil, fmt.Errorf("%w: CCID interface %d has no class descriptor (type 0x21)", ErrBadClass, c.number)
	case 1:
	default:
		return nil, fmt.Errorf("%w: CCID interface %d has %d class descriptors", ErrBadClass, c.number, len(c.classDescs))
	}
	cd, err := parseClass(c.classDescs[0], c.number)
	if err != nil {
		return nil, err
	}
	if err := cd.check(c.number); err != nil {
		return nil, err
	}
	l := &layout{
		iface:        c.number,
		bulkOut:      c.bulkOut[0].addr,
		bulkIn:       c.bulkIn[0].addr,
		maxPacketOut: c.bulkOut[0].mps,
		maxPacketIn:  c.bulkIn[0].mps,
		class:        cd,
	}
	if len(c.interruptIn) == 1 {
		l.interruptIn = c.interruptIn[0].addr
	}
	return l, nil
}

// walk reads the descriptors under the rules of the layer below (one configuration, whose length is the number of bytes present,
// every interface at alternate setting 0) and returns the interfaces of class 0x0B.
func walk(b []byte) ([]ifaceInfo, error) {
	if len(b) < 18 || b[0] != 18 || b[1] != descDevice {
		return nil, fmt.Errorf("%w: the data does not start with a device descriptor", usbccid.ErrBadDescriptors)
	}
	if b[17] != 1 {
		return nil, usbccid.ErrSeveralConfigurations
	}
	cfg := b[18:]
	if len(cfg) < 9 || cfg[0] < 9 || int(cfg[0]) > len(cfg) || cfg[1] != descConfig {
		return nil, fmt.Errorf("%w: no configuration descriptor follows the device descriptor", usbccid.ErrBadDescriptors)
	}
	if int(le16(cfg[2:])) != len(cfg) {
		return nil, fmt.Errorf("%w: the length of the configuration is not the number of bytes present", usbccid.ErrBadDescriptors)
	}
	var (
		out      []ifaceInfo
		cur      *ifaceInfo // the CCID interface being read, or nil
		numbers  = map[uint8]bool{}
		endpoint = map[uint8]bool{}
	)
	for p := cfg[cfg[0]:]; len(p) > 0; {
		n := int(p[0])
		if n < 2 || n > len(p) {
			return nil, fmt.Errorf("%w: a descriptor is shorter than its header or runs past the end", usbccid.ErrBadDescriptors)
		}
		d := p[:n]
		p = p[n:]
		switch d[1] {
		case descInterface:
			if n < 9 {
				return nil, fmt.Errorf("%w: an interface descriptor is shorter than 9 bytes", usbccid.ErrBadDescriptors)
			}
			if d[3] != 0 {
				return nil, fmt.Errorf("%w: an interface has an alternate setting other than 0", usbccid.ErrBadDescriptors)
			}
			if numbers[d[2]] {
				return nil, fmt.Errorf("%w: an interface number is listed twice", usbccid.ErrBadDescriptors)
			}
			numbers[d[2]] = true
			cur = nil
			if d[5] == usbccid.ClassCCID {
				out = append(out, ifaceInfo{number: int(d[2]), subclass: d[6], protocol: d[7], numEndpoints: int(d[4])})
				cur = &out[len(out)-1]
			}
		case descCCID:
			if cur != nil {
				cur.classDescs = append(cur.classDescs, d)
			}
		case descEndpoint:
			if n < 7 {
				return nil, fmt.Errorf("%w: an endpoint descriptor is shorter than 7 bytes", usbccid.ErrBadDescriptors)
			}
			if d[2]&0x0F == 0 || d[2]&0x70 != 0 {
				return nil, fmt.Errorf("%w: an endpoint address is not a number 1 to 15 with the reserved bits clear", usbccid.ErrBadDescriptors)
			}
			if endpoint[d[2]] {
				return nil, fmt.Errorf("%w: an endpoint address is listed twice", usbccid.ErrBadDescriptors)
			}
			endpoint[d[2]] = true
			if cur == nil {
				continue
			}
			cur.endpointsSeen++
			e := endpointInfo{addr: d[2], mps: int(le16(d[4:])) & 0x7FF}
			in := d[2]&0x80 != 0
			switch {
			case d[3]&3 == 2 && !in:
				cur.bulkOut = append(cur.bulkOut, e)
			case d[3]&3 == 2 && in:
				cur.bulkIn = append(cur.bulkIn, e)
			case d[3]&3 == 3 && in:
				cur.interruptIn = append(cur.interruptIn, e)
			}
		}
	}
	if int(cfg[4]) != len(numbers) {
		return nil, fmt.Errorf("%w: bNumInterfaces is not the number of interfaces listed", usbccid.ErrBadDescriptors)
	}
	return out, nil
}

// parseClass reads the class descriptor and refuses one that is not exactly 54 bytes of the type 0x21.
func parseClass(d []byte, iface int) (classDescriptor, error) {
	switch {
	case len(d) < classDescriptorLength:
		return classDescriptor{}, fmt.Errorf("%w: the class descriptor of CCID interface %d is %d bytes, and it is %d", ErrBadClass, iface, len(d), classDescriptorLength)
	case len(d) > classDescriptorLength:
		return classDescriptor{}, fmt.Errorf("%w: the class descriptor of CCID interface %d is %d bytes, and this layer knows only the %d-byte form", ErrBadClass, iface, len(d), classDescriptorLength)
	}
	return classDescriptor{
		bcdCCID:          le16(d[2:]),
		maxSlotIndex:     d[4],
		voltageSupport:   d[5],
		protocols:        le32(d[6:]),
		features:         le32(d[40:]),
		maxMessageLength: le32(d[44:]),
	}, nil
}

// check applies the rules of what this layer accepts, in this order, each refusal naming its rule: the version, exactly one slot,
// the exchange level, the automatic parameters, the voltage, the protocols and the message length.
func (c classDescriptor) check(iface int) error {
	if c.bcdCCID>>8 != 1 {
		return fmt.Errorf("%w: bcdCCID is 0x%04x of CCID interface %d, and this layer knows version 1", ErrBadClass, c.bcdCCID, iface)
	}
	if c.maxSlotIndex != 0 {
		return fmt.Errorf("%w: bMaxSlotIndex is %d, and this layer accepts exactly one slot", ErrSeveralSlots, c.maxSlotIndex)
	}
	switch lv := c.features & featLevelMask; lv {
	case featShortAPDU, featExtAPDU:
	case featTPDU:
		return c.levelError("TPDU")
	case 0:
		return c.levelError("character")
	default:
		return fmt.Errorf("%w: dwFeatures 0x%08x names more than one exchange level", ErrBadClass, c.features)
	}
	if c.features&featAutoParameters == 0 {
		return fmt.Errorf("%w: dwFeatures 0x%08x does not declare automatic parameter configuration from the ATR (bit 0x00000002), and this layer never sets parameters", ErrParameters, c.features)
	}
	if c.voltageSupport == 0 {
		return fmt.Errorf("%w: bVoltageSupport is 0, so the reader declares no voltage, and power-on asks the reader to choose", ErrVoltage)
	}
	if c.protocols&3 == 0 {
		return fmt.Errorf("%w: dwProtocols 0x%08x declares neither T=0 nor T=1", ErrBadClass, c.protocols)
	}
	if c.maxMessageLength < MinMessageLength {
		return fmt.Errorf("%w: dwMaxCCIDMessageLength is %d, below the %d that one short APDU needs (MinMessageLength)", ErrBadClass, c.maxMessageLength, MinMessageLength)
	}
	return nil
}

func (c classDescriptor) levelError(name string) error {
	return fmt.Errorf("%w: the reader works at %s level (dwFeatures 0x%08x, dwMaxCCIDMessageLength %d), and this layer needs short APDU or extended APDU level", ErrExchangeLevel, name, c.features, c.maxMessageLength)
}

// extended reports whether the reader takes extended APDUs.
func (c classDescriptor) extended() bool { return c.features&featLevelMask == featExtAPDU }

func le16(b []byte) uint16 { return binary.LittleEndian.Uint16(b) }

func le32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }
