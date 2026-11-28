package pivtest

// A rule of the tripwire names a command that a driver must never send, and why. A match is recorded in Forbidden whatever the card answers
// (before SELECT it answers 6985 as to any command; after it, these answer 6D00). To forbid more, append a rule: it sees the command as
// it was received, and, for a chained command, once more joined, so a rule that reads the data may match only the whole.
type rule struct {
	why   string
	match func(*command) bool
}

func ins(b byte) func(*command) bool { return func(c *command) bool { return c.ins == b } }

var tripwire = []rule{
	{"INS FB resets the application and deletes every key", ins(insReset)},
	{"INS FA sets the retry counters of the PIN and the PUK", ins(insSetRetries)},
	{"INS FE imports a key", ins(insImportKey)},
}

// trip returns the reason of the first rule that matches, or "".
func trip(c *command) string {
	for _, r := range tripwire {
		if r.match(c) {
			return r.why
		}
	}
	return ""
}

// Forbidden returns the commands that a driver must never send and that this card received, in order, each with the reason in Why.
func (card *Card) Forbidden() []Entry {
	c := card.st()
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Entry(nil), c.forbidden...)
}

// TestingT is the part of testing.TB that RequireNoForbidden uses. The package does not import testing, and a *testing.T or *testing.B is
// accepted as it is.
type TestingT interface {
	Helper()
	Fatalf(format string, args ...any)
}

// RequireNoForbidden fails the test if the card received a command of the tripwire.
func RequireNoForbidden(t TestingT, c *Card) {
	t.Helper()
	if f := c.Forbidden(); len(f) > 0 {
		e := f[0]
		t.Fatalf("pivtest: the driver sent %d forbidden command(s); the first is CLA %02X INS %02X P1 %02X P2 %02X (%s)", len(f), e.CLA, e.INS, e.P1, e.P2, e.Why)
	}
}
