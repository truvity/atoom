package attesttest

import (
	"bytes"
	_ "embed" // the committed corpus
	"encoding/json"
	"fmt"
	"os"
	"time"
)

//go:embed corpus/corpus.json
var embedded []byte

// The classes of an error in a golden result. A chain error is the one of the standard library's verification; an extension error names the
// extension; a construction error names why a verifier was not built.
const (
	ClassChain      = "chain"
	ClassFirmware   = "firmware"
	ClassSerial     = "serial"
	ClassPolicy     = "policy"
	ClassFormFactor = "form_factor"

	ClassRootCount = "root_count"
	ClassRootPin   = "root_pin"
	ClassParse     = "parse"
	ClassNoAnchor  = "no_anchor"
)

// Result is what a check gave for a case: the facts it returned, or the class of its error. The facts are those of the check the library is
// equal to (the old driver's verifier); the Has flags of the library are not in it, because that check had none: a flag is true exactly when
// its extension is in the slot certificate.
type Result struct {
	OK bool `json:"ok"`

	Slot       byte   `json:"slot"`
	Firmware   [3]int `json:"firmware"`
	Serial     uint32 `json:"serial"`
	FormFactor byte   `json:"form_factor"`
	PIN        int    `json:"pin"`
	Touch      int    `json:"touch"`

	// Class is the class of the error, when the check refused. Its text is not kept: the standard library's text of a chain error holds the
	// time of the check, and the texts of the two checks differ for an extension error on purpose.
	Class string `json:"class,omitempty"`
}

// PKIData is the verifier side of a case: a root file, a bundle and the SHA-256 of the root.
type PKIData struct {
	RootPEM    string `json:"root_pem"`
	BundlePEM  string `json:"bundle_pem"`
	RootSHA256 string `json:"root_sha256"`
}

// Case is one slot certificate and its signer, to be checked by the verifier built from the PKI of that name.
type Case struct {
	Name   string `json:"name"`
	Why    string `json:"why"`
	PKI    string `json:"pki"`
	Slot   []byte `json:"slot"`   // DER
	Signer []byte `json:"signer"` // DER
	Want   Result `json:"want"`
}

// Construction is a root file and a bundle that a verifier is built from, and whether it was.
type Construction struct {
	Name           string `json:"name"`
	Why            string `json:"why"`
	RootPEM        string `json:"root_pem"`
	BundlePEM      string `json:"bundle_pem"`
	WantRootSHA256 string `json:"want_root_sha256"`
	WantOK         bool   `json:"want_ok"`
	WantClass      string `json:"want_class,omitempty"`
}

// Corpus is the committed set of cases.
type Corpus struct {
	// GoldenSource says where the results of the cases came from. A corpus whose results were not given by the old check says so here.
	GoldenSource string `json:"golden_source"`
	// At is a time inside the validity of every certificate that is meant to be valid, and outside the others: a replay checks the chain at
	// this time. The check the results come from used the current time, and the validity windows are chosen so that both agree for decades.
	At            time.Time          `json:"at"`
	PKIs          map[string]PKIData `json:"pkis"`
	Cases         []Case             `json:"cases"`
	Constructions []Construction     `json:"constructions"`
}

// Load reads the corpus committed in this package.
func Load() (*Corpus, error) { return Parse(embedded) }

// Parse reads a corpus from its JSON form.
func Parse(b []byte) (*Corpus, error) {
	var c Corpus
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("attesttest: the corpus does not parse: %w", err)
	}
	return &c, nil
}

// Write writes the corpus to a file in its JSON form.
func (c *Corpus) Write(path string) error {
	b, err := json.MarshalIndent(c, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644) //nolint:gosec // a test file of the repository
}
