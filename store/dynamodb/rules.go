package dynamodb

import (
	"bytes"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/common/refusal"
	storev1alpha1 "github.com/truvity/atoom/store/gen/atoom/store/v1alpha1"
)

// The rules of the item contract that every engine applies in the same way (store/conformance/src/rules.rs, the Rust engines' copy), and
// the limits of this engine.
const (
	// maxPage is the most items a page of Query holds.
	maxPage = 500
	// defaultPage is the page size of a Query that names none.
	defaultPage = 100
	// maxOps is the most operations (conditions, puts and deletes together) one Transact carries, by the contract.
	maxOps = 256
	// maxKey is the longest key a driver takes.
	maxKey = 512

	// maxActions is the most writes one DynamoDB transaction holds. It is DynamoDB's limit, and lower than the contract's: a Transact that
	// needs more is refused (batch), never split, because a split is not all or none. Each put is one write and one more for each index
	// entry it adds or removes, each delete one and one more for each entry it removes, and a condition on a key that is not written one.
	maxActions = 100
	// maxValue is the longest value a put takes. DynamoDB's item limit is 400 KB for the whole item, attribute names included.
	maxValue = 380 * 1024
	// maxIndexName and maxIndexValue bound an index entry, whose name and value are part of a partition key (at most 2,048 bytes).
	maxIndexName  = 255
	maxIndexValue = 1024
)

func refuse(code connect.Code, reason, field string) *connect.Error {
	return refusal.New(code, reason, field)
}

func batch(field string) *connect.Error { return refuse(connect.CodeInvalidArgument, "batch", field) }

// checkKey holds a key to the contract: printable ASCII without a space, 1 to 512 bytes.
func checkKey(key []byte, path string) error {
	if len(key) == 0 || len(key) > maxKey {
		return refuse(connect.CodeInvalidArgument, "address", path)
	}
	for _, b := range key {
		if b < 0x21 || b > 0x7e {
			return refuse(connect.CodeInvalidArgument, "address", path)
		}
	}
	return nil
}

// pageSize is the page size of a Query: 0 is the default, more than the maximum is the maximum, a negative size is refused.
func pageSize(n int32) (int, error) {
	switch {
	case n < 0:
		return 0, batch("page_size")
	case n == 0:
		return defaultPage, nil
	case int(n) > maxPage:
		return maxPage, nil
	}
	return int(n), nil
}

// checkToken holds a page token to the contract: the last key of the previous page, so a key under the prefix.
func checkToken(token, prefix []byte) error {
	if len(token) == 0 {
		return nil
	}
	if err := checkKey(token, "page_token"); err != nil {
		return err
	}
	if !bytes.HasPrefix(token, prefix) {
		return refuse(connect.CodeInvalidArgument, "address", "page_token")
	}
	return nil
}

// expectation is what a condition expects of its key.
type expectation struct {
	absent  bool
	version uint64
}

// checkTransact checks a Transact request before anything is read and returns what each condition expects, in order.
func checkTransact(req *storev1alpha1.TransactRequest) ([]expectation, error) {
	if len(req.GetConditions())+len(req.GetPuts())+len(req.GetDeletes()) > maxOps {
		return nil, batch("conditions")
	}
	expected := make([]expectation, 0, len(req.GetConditions()))
	for i, c := range req.GetConditions() {
		if err := checkKey(c.GetKey(), fmt.Sprintf("conditions[%d].key", i)); err != nil {
			return nil, err
		}
		switch e := c.GetExpect().(type) {
		case *storev1alpha1.Condition_Absent:
			if !e.Absent {
				// absent: false says nothing a driver could enforce.
				return nil, batch(fmt.Sprintf("conditions[%d].expect", i))
			}
			expected = append(expected, expectation{absent: true})
		case *storev1alpha1.Condition_Version:
			expected = append(expected, expectation{version: e.Version})
		default:
			return nil, batch(fmt.Sprintf("conditions[%d].expect", i))
		}
	}
	written := map[string]bool{}
	for i, p := range req.GetPuts() {
		if err := checkKey(p.GetKey(), fmt.Sprintf("puts[%d].key", i)); err != nil {
			return nil, err
		}
		if written[string(p.GetKey())] {
			return nil, batch(fmt.Sprintf("puts[%d].key", i))
		}
		written[string(p.GetKey())] = true
		if p.GetLog() {
			// The driver keeps no change log: a put that asks for a row is refused and written without none never.
			return nil, refuse(connect.CodeUnimplemented, "log", fmt.Sprintf("puts[%d].log", i))
		}
		if len(p.GetValue()) > maxValue {
			return nil, batch(fmt.Sprintf("puts[%d].value", i))
		}
		for j, e := range p.GetIndexes() {
			path := fmt.Sprintf("puts[%d].indexes[%d]", i, j)
			switch {
			case e.GetName() == "":
				return nil, refuse(connect.CodeInvalidArgument, "index", path+".name")
			case len(e.GetName()) > maxIndexName || strings.ContainsRune(e.GetName(), 0):
				return nil, refuse(connect.CodeInvalidArgument, "index", path+".name")
			case len(e.GetValue()) > maxIndexValue:
				return nil, refuse(connect.CodeInvalidArgument, "index", path+".value")
			}
		}
	}
	for i, d := range req.GetDeletes() {
		if err := checkKey(d.GetKey(), fmt.Sprintf("deletes[%d].key", i)); err != nil {
			return nil, err
		}
		if written[string(d.GetKey())] {
			return nil, batch(fmt.Sprintf("deletes[%d].key", i))
		}
		written[string(d.GetKey())] = true
		if d.GetLog() {
			return nil, refuse(connect.CodeUnimplemented, "log", fmt.Sprintf("deletes[%d].log", i))
		}
	}
	return expected, nil
}
