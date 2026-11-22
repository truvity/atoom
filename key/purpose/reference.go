package purpose

// Reference returns the declaration of sluis, the first host: the eleven purposes that the key drivers held in their closed lists before
// the host declared them (decision 0008). A host that wants them as they were passes this, or the part of it that it enables, to the
// driver; no driver holds the list. On a card each takes its slot from the reference map of key/slotplan (Default), and the purpose
// pin-release is the card's PIN release key (docs/key-port.md, "The PIN release"). It returns a new slice every time.
func Reference() []Declaration {
	sign := func() []Operation { return []Operation{Sign} }
	return []Declaration{
		{Name: "wrapping", Algorithm: ECDHP384, Operations: []Operation{Agree}},
		{Name: "token-signing", Algorithm: ES384, Operations: sign()},
		{Name: "audit-log", Algorithm: ES256, Operations: sign()},
		{Name: "pin-release", Algorithm: ES384, Operations: sign()},
		{Name: "workload-issuer", Algorithm: ES384, Operations: sign()},
		{Name: "x509-intermediate", Algorithm: ES384, Operations: sign()},
		{Name: "ssh-user", Algorithm: ES384, Operations: sign()},
		{Name: "ssh-host", Algorithm: ES384, Operations: sign()},
		{Name: "issuance-log", Algorithm: ES384, Operations: sign()},
		{Name: "release-signing", Algorithm: ES384, Operations: sign()},
		{Name: "token-signing-rs256", Algorithm: RS256, Operations: sign()},
	}
}
