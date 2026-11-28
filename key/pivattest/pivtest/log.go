package pivtest

// Entry is one command the card received. It holds the header (as far as the command had one: a command of three bytes has a CLA, an INS
// and a P1, and its P2 is zero), the length of the data and the status word, and nothing of the data: a log that held a PIN or the body of
// an object would leak it into every test that prints the log.
type Entry struct {
	CLA, INS, P1, P2 byte
	DataLen          int    // the length of the data of this APDU (a part of a chained command counts alone)
	SW               uint16 // the status word of the answer, 61xx included
	Malformed        bool   // shorter than four bytes, or lengths that do not match its size: DataLen is then 0 and the header is the bytes that were there, the others zero
	Why              string // only in Forbidden: what the driver must not do
}

// record appends the entry of one command. sent is the length of the data of the APDU as it came, before a chain joined it to the parts
// before.
func (c *state) record(cmd *command, sent int, malformed bool, sw uint16) {
	e := Entry{CLA: cmd.cla, INS: cmd.ins, P1: cmd.p1, P2: cmd.p2, DataLen: sent, SW: sw, Malformed: malformed}
	c.log = append(c.log, e)
	if cmd.why != "" {
		e.Why = cmd.why
		c.forbidden = append(c.forbidden, e)
	}
}

// Log returns one entry for each command received since New, in order. The log is not card state: a power cycle keeps it.
func (card *Card) Log() []Entry {
	c := card.st()
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Entry(nil), c.log...)
}
