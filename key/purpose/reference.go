package purpose

// Reference returns the declaration of sluis, the first host: the eleven purposes that the key drivers held in their closed lists before
// the host declared them (decision 0008). A host that wants them as they were passes this, or the part of it that it enables, to the
// driver; no driver holds the list. Each carries its slot of the reference map of key/slotplan (Reference), because a card driver
// takes no slot that a declaration does not name (record 0021); the other drivers ignore it. The purpose pin-release is the card's
// PIN release key (docs/key-port.md, "The PIN release"). It returns a new slice every time.
func Reference() []Declaration {
	sign := func() []Operation { return []Operation{Sign} }
	return []Declaration{
		{Name: "wrapping", Algorithm: ECDHP384, Operations: []Operation{Agree}, Slot: "9d"},
		{Name: "token-signing", Algorithm: ES384, Operations: sign(), Slot: "9a"},
		{Name: "audit-log", Algorithm: ES256, Operations: sign(), Slot: "84"},
		{Name: "pin-release", Algorithm: ES384, Operations: sign(), Slot: "87"},
		{Name: "workload-issuer", Algorithm: ES384, Operations: sign(), Slot: "86"},
		{Name: "x509-intermediate", Algorithm: ES384, Operations: sign(), Slot: "9c"},
		{Name: "ssh-user", Algorithm: ES384, Operations: sign(), Slot: "82"},
		{Name: "ssh-host", Algorithm: ES384, Operations: sign(), Slot: "83"},
		{Name: "issuance-log", Algorithm: ES384, Operations: sign(), Slot: "89"},
		{Name: "release-signing", Algorithm: ES384, Operations: sign(), Slot: "85"},
		{Name: "token-signing-rs256", Algorithm: RS256, Operations: sign(), Slot: "88"},
	}
}
