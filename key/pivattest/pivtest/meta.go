package pivtest

// The tags of GET METADATA (the vendor's command, INS F7).
const (
	tagAlgorithm = 0x01
	tagIsDefault = 0x05
	tagRetries   = 0x06

	algorithmNone = 0xFF // the algorithm that a PIN and a PUK state: assumed: R14
)

// getMetadata answers for the PIN (P2 80) and the PUK (P2 81): the algorithm (none), whether the value is the default, and the tries in
// all and left. Any other reference is 6A88 here: the management key and the slots are not part of this card yet.
func (c *state) getMetadata(cmd *command) reply {
	if cmd.p1 != 0 {
		return status(swWrongP1P2) // assumed: R8
	}
	r := c.ref(cmd.p2)
	if r == nil {
		return status(swRefNotFound)
	}
	if len(cmd.data) != 0 {
		return status(swWrongLength) // assumed: R8
	}
	def := byte(0)
	if r.isDefault {
		def = 1
	}
	return reply{sw: swOK, data: append(append(
		tlv(tagAlgorithm, []byte{algorithmNone}), // assumed: R14
		tlv(tagIsDefault, []byte{def})...),
		tlv(tagRetries, []byte{r.max, r.left})...)}
}
