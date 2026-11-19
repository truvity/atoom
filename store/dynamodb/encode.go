package dynamodb

import (
	"bytes"
	"cmp"
	"errors"
	"slices"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1alpha1 "github.com/truvity/atoom/common/gen/atoom/common/v1alpha1"
	storev1alpha1 "github.com/truvity/atoom/store/gen/atoom/store/v1alpha1"
)

// The partitions of the table. An item's partition is one constant: every item is in one item collection, so that a listing by key prefix
// is one Query on the sort key (docs/layout.md, "Partitions"). An index entry's partition is made of the index name and value.
const (
	partItem  = 0x01
	partEntry = 0x02
)

// entry is one declared index entry.
type entry struct {
	name  string
	value []byte
}

// stored is an item as the table holds it.
type stored struct {
	key     []byte
	version uint64
	value   []byte
	expMs   int64 // the expiry in milliseconds; meaningful when hasExp
	hasExp  bool
	indexes []entry // sorted by name, then value, without repeats
}

// live reports whether the item is present to a reader: one past its expiry is absent, whatever the table still holds.
func (s *stored) live(nowMs int64) bool {
	return s != nil && (!s.hasExp || s.expMs > nowMs)
}

// ttlSeconds is the value of the time-to-live attribute for an expiry: the whole second at or after it, so that DynamoDB frees an item
// never before it is expired.
func ttlSeconds(expMs int64) int64 {
	if expMs <= 0 {
		return 0
	}
	return (expMs + 999) / 1000
}

func itemPK() types.AttributeValue { return &types.AttributeValueMemberB{Value: []byte{partItem}} }

func entryPKBytes(e entry) []byte {
	b := make([]byte, 0, 2+len(e.name)+len(e.value))
	b = append(b, partEntry)
	b = append(b, e.name...)
	b = append(b, 0)
	return append(b, e.value...)
}

func bin(b []byte) types.AttributeValue {
	// A nil slice is an empty binary value, not a missing one.
	return &types.AttributeValueMemberB{Value: append([]byte{}, b...)}
}

func num(n int64) types.AttributeValue {
	return &types.AttributeValueMemberN{Value: strconv.FormatInt(n, 10)}
}

func unum(n uint64) types.AttributeValue {
	return &types.AttributeValueMemberN{Value: strconv.FormatUint(n, 10)}
}

// itemKey is the primary key of the item of a key.
func itemKey(key []byte) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{attrPK: itemPK(), attrSK: bin(key)}
}

// entryKey is the primary key of an index entry of an item.
func entryKey(e entry, key []byte) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{attrPK: bin(entryPKBytes(e)), attrSK: bin(key)}
}

// sortEntries sorts by name, then value, and drops repeats.
func sortEntries(in []entry) []entry {
	out := slices.Clone(in)
	slices.SortFunc(out, func(a, b entry) int {
		if c := cmp.Compare(a.name, b.name); c != 0 {
			return c
		}
		return bytes.Compare(a.value, b.value)
	})
	return slices.CompactFunc(out, func(a, b entry) bool { return a.name == b.name && bytes.Equal(a.value, b.value) })
}

func entriesOf(in []*commonv1alpha1.IndexEntry) []entry {
	out := make([]entry, 0, len(in))
	for _, e := range in {
		out = append(out, entry{name: e.GetName(), value: bytes.Clone(e.GetValue())})
	}
	return sortEntries(out)
}

func hasEntry(set []entry, e entry) bool {
	return slices.ContainsFunc(set, func(x entry) bool { return x.name == e.name && bytes.Equal(x.value, e.value) })
}

// attributes is the item as DynamoDB stores it.
func (s *stored) attributes() map[string]types.AttributeValue {
	m := itemKey(s.key)
	m[attrVersion] = unum(s.version)
	m[attrValue] = bin(s.value)
	if s.hasExp {
		m[attrExpires] = num(s.expMs)
		m[attrTTL] = num(ttlSeconds(s.expMs))
	}
	if len(s.indexes) > 0 {
		list := make([]types.AttributeValue, 0, len(s.indexes))
		for _, e := range s.indexes {
			list = append(list, &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{
				"n": &types.AttributeValueMemberS{Value: e.name},
				"v": bin(e.value),
			}})
		}
		m[attrIndexes] = &types.AttributeValueMemberL{Value: list}
	}
	return m
}

// entryAttributes is an index entry of an item as DynamoDB stores it: its key, and the time to live of the item, so that an entry is
// freed with the item it names.
func (s *stored) entryAttributes(e entry) map[string]types.AttributeValue {
	m := entryKey(e, s.key)
	if s.hasExp {
		m[attrTTL] = num(ttlSeconds(s.expMs))
	}
	return m
}

var errBadItem = errors.New("dynamodb: an item of the table is not in the shape the driver writes")

// decode reads an item of the table. A malformed one is an error: the table is the driver's, and a foreign item in it is a fault to see,
// not a value to guess.
func decode(m map[string]types.AttributeValue) (*stored, error) {
	sk, ok := m[attrSK].(*types.AttributeValueMemberB)
	if !ok {
		return nil, errBadItem
	}
	v, ok := m[attrVersion].(*types.AttributeValueMemberN)
	if !ok {
		return nil, errBadItem
	}
	ver, err := strconv.ParseUint(v.Value, 10, 64)
	if err != nil {
		return nil, errBadItem
	}
	s := &stored{key: bytes.Clone(sk.Value), version: ver}
	if val, ok := m[attrValue].(*types.AttributeValueMemberB); ok {
		s.value = bytes.Clone(val.Value)
	}
	if e, ok := m[attrExpires].(*types.AttributeValueMemberN); ok {
		ms, err := strconv.ParseInt(e.Value, 10, 64)
		if err != nil {
			return nil, errBadItem
		}
		s.expMs, s.hasExp = ms, true
	}
	if l, ok := m[attrIndexes].(*types.AttributeValueMemberL); ok {
		for _, el := range l.Value {
			mm, ok := el.(*types.AttributeValueMemberM)
			if !ok {
				return nil, errBadItem
			}
			n, ok1 := mm.Value["n"].(*types.AttributeValueMemberS)
			val, ok2 := mm.Value["v"].(*types.AttributeValueMemberB)
			if !ok1 || !ok2 {
				return nil, errBadItem
			}
			s.indexes = append(s.indexes, entry{name: n.Value, value: bytes.Clone(val.Value)})
		}
		s.indexes = sortEntries(s.indexes)
	}
	return s, nil
}

// driverItem is the item as the contract carries it.
func (s *stored) driverItem() *storev1alpha1.DriverItem {
	it := &storev1alpha1.DriverItem{Key: bytes.Clone(s.key), Version: s.version, Value: bytes.Clone(s.value)}
	if s.hasExp {
		it.ExpiresAt = timestamppb.New(msToTime(s.expMs))
	}
	for _, e := range s.indexes {
		it.Indexes = append(it.Indexes, &commonv1alpha1.IndexEntry{Name: e.name, Value: bytes.Clone(e.value)})
	}
	return it
}
