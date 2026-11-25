package index

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"google.golang.org/protobuf/encoding/protojson"

	indexv1alpha2 "github.com/truvity/atoom/index/gen/atoom/index/v1alpha2"
)

// The limits and the fixed names of a declaration (record 0025): the same as the Rust twin, atoom_index::declaration, and held to the
// same test vectors (testdata/declarations).
const (
	// MaxColumns is the most declared columns.
	MaxColumns = 32
	// MaxKinds is the most declared log kinds.
	MaxKinds = 32
	// MaxKind is the most bytes of a log kind's name.
	MaxKind = 128
)

// Fixed are the names of the fixed columns and fields of a row and a log, which no declared column may take.
var Fixed = []string{"scope", "kind", "log", "tenant", "hash", "seq", "time_ms", "entry_id", "entry", "id"}

var columnName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// DeclarationError is a declaration that is refused: every problem, each naming the field at fault.
type DeclarationError struct {
	Problems []string
}

func (e *DeclarationError) Error() string {
	return "the index declaration is refused: " + strings.Join(e.Problems, "; ")
}

// ParseDeclaration reads the JSON form of a declaration (the proto JSON mapping of atoom.index.v1alpha2.Declaration, the file of a thin
// main's --declaration) and checks it.
func ParseDeclaration(b []byte) (*indexv1alpha2.Declaration, error) {
	d := &indexv1alpha2.Declaration{}
	if err := protojson.Unmarshal(b, d); err != nil {
		return nil, &DeclarationError{Problems: []string{"file: not the JSON of a declaration"}}
	}
	if err := CheckDeclaration(d); err != nil {
		return nil, err
	}
	return d, nil
}

func kindOK(k string) bool {
	if k == "" || len(k) > MaxKind {
		return false
	}
	return !strings.ContainsFunc(k, unicode.IsControl)
}

// CheckDeclaration holds a declaration to the rules: at least one kind and at most MaxKinds, each named, none twice; at most MaxColumns
// columns, each with a name of [a-z][a-z0-9_]{0,31} that is not a fixed one and not taken twice, a type of text or int, and words only on
// a text column.
func CheckDeclaration(d *indexv1alpha2.Declaration) error {
	if d == nil {
		return &DeclarationError{Problems: []string{"declaration: none"}}
	}
	var p []string
	if n := len(d.GetKinds()); n == 0 || n > MaxKinds {
		p = append(p, fmt.Sprintf("kinds: from 1 to %d", MaxKinds))
	}
	var kinds []string
	for i, k := range d.GetKinds() {
		switch {
		case !kindOK(k):
			p = append(p, fmt.Sprintf("kinds[%d]: 1 to %d bytes, no control character", i, MaxKind))
		case slices.Contains(kinds, k):
			p = append(p, fmt.Sprintf("kinds[%d]: declared twice", i))
		default:
			kinds = append(kinds, k)
		}
	}
	if len(d.GetColumns()) > MaxColumns {
		p = append(p, fmt.Sprintf("columns: at most %d", MaxColumns))
	}
	var names []string
	for i, c := range d.GetColumns() {
		n := c.GetName()
		switch {
		case !columnName.MatchString(n):
			p = append(p, fmt.Sprintf("columns[%d].name: [a-z][a-z0-9_]{0,31}", i))
		case slices.Contains(Fixed, n):
			p = append(p, fmt.Sprintf("columns[%d].name: the name of a fixed column", i))
		case slices.Contains(names, n):
			p = append(p, fmt.Sprintf("columns[%d].name: declared twice", i))
		default:
			names = append(names, n)
		}
		switch c.GetType() {
		case indexv1alpha2.ColumnType_COLUMN_TYPE_TEXT:
		case indexv1alpha2.ColumnType_COLUMN_TYPE_INT:
			if c.GetWords() {
				p = append(p, fmt.Sprintf("columns[%d].words: only a text column is reached by words", i))
			}
		default:
			p = append(p, fmt.Sprintf("columns[%d].type: COLUMN_TYPE_TEXT or COLUMN_TYPE_INT", i))
		}
	}
	if len(p) > 0 {
		return &DeclarationError{Problems: p}
	}
	return nil
}

// ErrDeclaration matches every *DeclarationError with errors.Is.
var ErrDeclaration = errors.New("index: the declaration is refused")

// Is makes errors.Is(err, ErrDeclaration) true for a *DeclarationError.
func (e *DeclarationError) Is(target error) bool { return target == ErrDeclaration }
