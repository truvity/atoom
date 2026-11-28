// Package pivtest is TEST-ONLY support for the tests of a program that speaks PIV to a YubiKey, such as the card driver of record 0026.
// No program imports it, and no part of it is a real card: it is a model of the PIV application of a card at the level of command and
// response APDUs, so that the code above a card reader can be tested with no card, no reader and no smart-card service.
//
// pivtest is the simulated card and lives here because it needs the attesttest PKI. (This part of the card does not use that PKI yet: it
// imports the standard library only.)
//
// # What a green run proves
//
// A green run over this model proves the driver against the model, not against a card. A model is made of what its author read and what
// they guessed. What the vendor's public documentation, the standard (NIST SP 800-73-4, ISO 7816-4) and the research pages of this
// repository state is built as stated. What none of them states is a guess, and every guess is marked in the source with the comment
// "assumed: R<n>" at the code and at its test, and listed in the table below. A row of that table is a thing to measure on a card before
// anyone relies on it. Nothing here is copied from a real card: the answer to reset, the default serial number and every object are made up.
//
// # What it is
//
// A Card is a deterministic state machine: no goroutine, no timer, no clock, no I/O and no randomness that decides an outcome. It never
// reads the machine's clock; a command that makes a real card wait returns the wait as a duration from Exchange (always zero in this part).
// New makes one, PowerOn starts a session and returns the answer to reset, Exchange takes one command APDU and returns one response APDU
// (the data, then SW1 SW2), and PowerOff ends the session. A reader simulator can take any type with those three methods.
//
// The card state survives PowerOff: the firmware version, the serial number, the PIN and the PUK, their counters, their maxima and whether
// each is still the factory value, and the data objects (PutObject seeds them). The session state does not: the selected application, the
// verified PIN, the buffer of a chained command and the rest of a response that waits for GET RESPONSE. Every session starts at SELECT.
//
// # The commands of this part
//
//	SELECT                  00 A4 04 00     the PIV application, by its 5-byte or its 11-byte AID
//	GET VERSION             00 FD 00 00     three bytes: major, minor, patch
//	GET SERIAL              00 F8 00 00     four bytes, big-endian
//	VERIFY                  00 20 00 80     the PIN; with no data it reads the counter and spends no try
//	CHANGE REFERENCE DATA   00 24 00 80/81  the PIN or the PUK: the old value, then the new one
//	GET METADATA            00 F7 00 80/81  of the PIN or the PUK: default flag and tries
//	GET DATA                00 CB 3F FF     an object: 5C 03 <id>
//	GET RESPONSE            00 C0 00 00     the rest of an answer that was cut
//
// A PIN or a PUK is 6 to 8 bytes and travels padded with FF to 8, so a value never ends in FF (New refuses one that does). VERIFY and
// CHANGE REFERENCE DATA answer 63Cx for a wrong value, with x the tries left; 6983 for a PIN or a PUK that is blocked (no tries left) when a value is sent, also
// for the right value (an empty VERIFY of a blocked PIN reads the counter: 63C0); and 9000 for a right value, which brings the counter back to its maximum. Answers that do not fit the Le of the command are cut and followed by
// 61xx (Options.ResponseMode says when). Extended APDUs are accepted unless Options.NoExtendedAPDU is set. CLA 10 chains a command: the
// parts are collected, each answers 9000 with no data, and the last part (CLA 00) runs the whole. Another INS answers 6D00 and another CLA
// 6E00. A command that is shorter than four bytes, or whose lengths do not match its size, answers 6700 and never panics.
//
// Printing a Card, by value or by pointer, with any verb, shows at most its firmware, serial number and counters and nothing of the PIN, the
// PUK or the objects: a Card is one func value that returns its state, a kind that fmt never follows, so no print can reach more than an address, in a field or not, and a copy of a Card is a second
// handle to the same card. Options is different: it is the caller's own input and carries the PIN and the PUK in the clear, so a test that
// prints its Options, or a struct that holds them, prints them; its Format method keeps them out of the verbs that reach it, and no method can
// do more.
//
// The management key, PUT DATA, touch, key generation, signing, key agreement, attestation, delete, move and RSA are not part of this card
// yet.
//
// # The data objects
//
// GET DATA returns the bytes that PutObject seeded, as they were seeded: a real object is 53 L ... and a certificate object is
// 53 L { 70 L <DER>, 71 01 00, FE 00 }, which CertificateObject builds from a DER certificate of up to 65526 bytes (MaxCertificateDER; it
// panics for a longer one, which no length can describe). An object that was not seeded is 6A82. GET DATA takes the one form of the tag list
// 5C 03 <id>. The
// ids are:
//
//	5FC105           certificate of slot 9a (ObjectSlot9A)
//	5FC10A           certificate of slot 9c (ObjectSlot9C)
//	5FC10B           certificate of slot 9d (ObjectSlot9D)
//	5FC101           certificate of slot 9e (ObjectSlot9E)
//	5FC10D + i       certificate of retired slot 82 + i, i = 0 to 19 (ObjectRetired1 + i)
//	5FFF01           the card's attestation certificate, slot f9 (ObjectAttestation)
//
// # The log and the tripwire
//
// Log returns one entry for each command received, with its CLA, INS, P1, P2, the length of its data and the status word. It never holds
// the data of a command: not the PIN of a VERIFY or of a CHANGE REFERENCE DATA, not the body of an object. Forbidden returns the commands
// that a driver must never send: INS FB (reset of the application, which deletes every key), FA (set the retry counters) and FE (import
// a key). They answer 6D00 and are recorded whatever the card answers. RequireNoForbidden fails a test that sent one. The tripwire is a
// list of rules in tripwire.go: a later rule is one more entry.
//
// Log and Forbidden are instrumentation, not card state: they are not cleared by a power cycle.
//
// # What is assumed
//
// Each row is a rule that is a guess, not a measurement. The source marks it "assumed: R<n>" at the code and at its test.
//
//	R2   A SELECT answered 9000 clears "PIN verified"; one that is not changes nothing.
//	R7   Before SELECT, every instruction other than SELECT answers 6985.
//	R8   The refusals for a command that is not well formed: 6700 for a data length that the command does not take, 6A88 for a P2 of VERIFY,
//	     CHANGE REFERENCE DATA or GET METADATA that is not a reference, 6A86 for a P1 or P2 that the command does not take, 6A80 for a new
//	     PIN or PUK of fewer than 6 or more than 8 bytes (checked before a try is spent) and for a tag list of GET DATA that is not 5C 03 <id> (the one short form: 5C 81 03 and 5C 82 00 03 are refused).
//	     P1 and P2 are held strictly here (SELECT takes only 04 00, for one): a real card may accept more. A driver that fails on a P1 or P2
//	     in this card is told to look at this row, and to measure the card, before it is changed to fit the model.
//	R10  GET RESPONSE with nothing waiting answers 6A86, and any command other than GET RESPONSE drops what waits. A GET RESPONSE that is
//	     refused (a P1 or P2 that is not 00, or data) drops what waits as well.
//	R14  GET METADATA of the PIN and the PUK carries the algorithm tag 01 with the value FF ("none"); the vendor's page does not say that a
//	     PIN has the tag.
//	R20  A wrong value in VERIFY, or a wrong old PIN in CHANGE REFERENCE DATA, leaves the PIN not verified. A change of
//	     the PIN that succeeds leaves "PIN verified" as it was.
//	R21  Chaining: a part with an INS, P1 or P2 other than the first drops the chain and answers 6700, so does a chain of more than 65535
//	     bytes, counting the last part, and a last part with another header than the chain runs alone.
//	R22  A command with no Le is read as a command with the largest Le: 256 bytes, or 65536 when it came extended in the mode Extended.
//
// What the research pages measured, and is built as measured: an empty VERIFY reads the counter and spends no try (63Cx when the PIN is
// not verified, 9000 when it is), a wrong PIN answers 63Cx with x the tries left, and a right PIN brings the counter back to its maximum.
package pivtest
