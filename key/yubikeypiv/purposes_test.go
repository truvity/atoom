package yubikeypiv

import "github.com/truvity/atoom/key/purpose"

// refPurposes is the declaration of sluis, the host whose purposes these tests were written for.
func refPurposes() []purpose.Declaration { return purpose.Reference() }
