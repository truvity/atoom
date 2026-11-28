package pivtest

import "fmt"

// The ids of the data objects, as GET DATA names them (the tag list 5C 03 <id>).
const (
	ObjectSlot9A      uint32 = 0x5FC105 // certificate of slot 9a, authentication
	ObjectSlot9C      uint32 = 0x5FC10A // certificate of slot 9c, signature
	ObjectSlot9D      uint32 = 0x5FC10B // certificate of slot 9d, key management
	ObjectSlot9E      uint32 = 0x5FC101 // certificate of slot 9e, card authentication
	ObjectRetired1    uint32 = 0x5FC10D // certificate of slot 82; slot 82+i is ObjectRetired1+i, up to slot 95 (0x5FC120)
	ObjectAttestation uint32 = 0x5FFF01 // the card's attestation certificate, slot f9
)

const tagObject = 0x53

// PutObject seeds the data object id with data, exactly as GET DATA will return it: a real object is 53 L ... (see CertificateObject). Empty
// data removes the object. It is how a test gives the card what a ceremony would have written.
func (card *Card) PutObject(id uint32, data []byte) {
	c := card.st()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(data) == 0 {
		delete(c.objects, id)
		return
	}
	c.objects[id] = append([]byte(nil), data...)
}

// MaxCertificateDER is the longest DER certificate that CertificateObject wraps: the whole object must fit a two-byte length.
const MaxCertificateDER = 0xFFFF - 9

// CertificateObject wraps a DER certificate as a certificate object holds it: 53 L { 70 L <DER>, 71 01 00, FE 00 }. The 71 byte says the
// certificate is not compressed. It panics for a certificate longer than MaxCertificateDER (65526 bytes): the object would have no length
// that can be written, and a corrupt object is never returned.
func CertificateObject(der []byte) []byte {
	if len(der) > MaxCertificateDER {
		panic(fmt.Sprintf("pivtest: a certificate of %d bytes does not fit a data object: the most is %d", len(der), MaxCertificateDER))
	}
	return tlv(tagObject, tlv(0x70, der), tlv(0x71, []byte{0x00}), tlv(0xFE))
}

// getData is GET DATA: P1 P2 3F FF and the tag list 5C 03 <id>. An object that was not seeded is 6A82.
func (c *state) getData(cmd *command) reply {
	if cmd.p1 != 0x3F || cmd.p2 != 0xFF {
		return status(swWrongP1P2) // assumed: R8
	}
	// Only the one form a driver sends: 5C 03 and the three bytes of the id. The long forms of the length (5C 81 03, 5C 82 00 03) are refused.
	if len(cmd.data) != 5 || cmd.data[0] != 0x5C || cmd.data[1] != 0x03 {
		return status(swWrongData) // assumed: R8
	}
	list := cmd.data[2:]
	obj, found := c.objects[uint32(list[0])<<16|uint32(list[1])<<8|uint32(list[2])]
	if !found {
		return status(swNotFound)
	}
	return reply{data: obj, sw: swOK}
}
