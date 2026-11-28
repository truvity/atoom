package pivtest

import "encoding/binary"

// A small BER-TLV reader and writer: enough for the answers this package builds and the tag list of GET DATA. A tag is one byte, or
// several when the low five bits of the first are all set; a length is one byte below 0x80, or 0x81 and one byte, or 0x82 and two.

// tlv returns one data object: the tag, the length of the concatenated values and the values.
func tlv(tag uint32, values ...[]byte) []byte {
	n := 0
	for _, v := range values {
		n += len(v)
	}
	if n > 0xFFFF {
		panic("pivtest: a value of more than 65535 bytes has no length that can be written")
	}
	t := binary.BigEndian.AppendUint32(nil, tag)
	switch {
	case tag > 0xFFFF:
		t = t[1:]
	case tag > 0xFF:
		t = t[2:]
	default:
		t = t[3:]
	}
	out := t
	switch {
	case n < 0x80:
		out = append(out, byte(n))
	case n < 0x100:
		out = append(out, 0x81, byte(n))
	default:
		out = append(out, 0x82, byte(n>>8), byte(n))
	}
	for _, v := range values {
		out = append(out, v...)
	}
	return out
}

// readTLV reads the first data object of b. ok is false when b does not hold a whole one, in the forms above.
func readTLV(b []byte) (tag uint32, value, rest []byte, ok bool) {
	if len(b) == 0 {
		return 0, nil, nil, false
	}
	tag = uint32(b[0])
	i := 1
	if b[0]&0x1F == 0x1F {
		for {
			if i >= len(b) || i > 2 {
				return 0, nil, nil, false
			}
			tag = tag<<8 | uint32(b[i])
			i++
			if b[i-1]&0x80 == 0 {
				break
			}
		}
	}
	if i >= len(b) {
		return 0, nil, nil, false
	}
	n := 0
	switch l := b[i]; {
	case l < 0x80:
		n = int(l)
		i++
	case l == 0x81 && i+1 < len(b):
		n = int(b[i+1])
		i += 2
	case l == 0x82 && i+2 < len(b):
		n = int(b[i+1])<<8 | int(b[i+2])
		i += 3
	default:
		return 0, nil, nil, false
	}
	if len(b)-i < n {
		return 0, nil, nil, false
	}
	return tag, b[i : i+n], b[i+n:], true
}
