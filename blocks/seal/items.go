package seal

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	sealv1alpha1 "github.com/truvity/atoom/blocks/seal/gen/atoom/seal/v1alpha1"
	"github.com/truvity/atoom/common/refusal"
	storev1alpha1 "github.com/truvity/atoom/store/gen/atoom/store/v1alpha1"
)

// The wraps are items of the store port (docs/seal-block.md, "Wraps in the store"):
//
//	<prefix>wrap/<scope>/<purpose>/<generation>/<recipient>  ->  DataKeyWrap
//
// with "-" for the installation's own scope. The value is already ciphertext.

// errTaken is a create that found the key in use: another instance made the data key, or wrote the wrap, first.
var errTaken = errors.New("seal: the wrap exists")

// errStore is a store call that failed for another reason than the contract's answers.
var errStore = errors.New("seal: the store failed")

// maxDeletes is the most deletes the block puts in one Transact: the strictest driver of the port (DynamoDB) takes 100 writes.
const maxDeletes = 100

func scopePart(scope string) string {
	if scope == "" {
		return "-"
	}
	return scope
}

func (b *Block) wrapsPrefix() string { return b.opts.KeyPrefix + "wrap/" }

func (b *Block) scopePrefix(scope string) string { return b.wrapsPrefix() + scopePart(scope) + "/" }

func (b *Block) linePrefix(l line) string { return b.scopePrefix(l.scope) + l.purpose + "/" }

func (b *Block) wrapKey(ref keyRef, recipient string) string {
	return b.linePrefix(ref.line()) + strconv.FormatUint(uint64(ref.generation), 10) + "/" + recipient
}

// wrapItem is a wrap with its reference and recipient, as listed.
type wrapItem struct {
	ref       keyRef
	recipient string
	wrap      *sealv1alpha1.DataKeyWrap
}

// parseWrapKey reads a key under the wraps prefix. It is false for a key the block did not write.
func (b *Block) parseWrapKey(key string) (keyRef, string, bool) {
	rest, ok := strings.CutPrefix(key, b.wrapsPrefix())
	if !ok {
		return keyRef{}, "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 4 {
		return keyRef{}, "", false
	}
	g, err := strconv.ParseUint(parts[2], 10, 32)
	if err != nil || g == 0 {
		return keyRef{}, "", false
	}
	scope := parts[0]
	if scope == "-" {
		scope = ""
	}
	return keyRef{scope: scope, purpose: parts[1], generation: uint32(g)}, parts[3], true
}

// getWrap reads the wrap of ref for a recipient; found is false when there is none.
func (b *Block) getWrap(ctx context.Context, ref keyRef, recipient string) (*sealv1alpha1.DataKeyWrap, bool, error) {
	res, err := b.opts.Items.Get(ctx, connect.NewRequest(&storev1alpha1.ItemGetRequest{Key: []byte(b.wrapKey(ref, recipient)), Linearizable: true}))
	if err != nil {
		if connect.CodeOf(err) == connect.CodeNotFound {
			return nil, false, nil
		}
		return nil, false, errStore
	}
	w := &sealv1alpha1.DataKeyWrap{}
	if err := proto.Unmarshal(res.Msg.GetItem().GetValue(), w); err != nil {
		return nil, false, errors.New("seal: a wrap does not decode")
	}
	return w, true, nil
}

// listWraps lists the wraps under a key prefix, page by page, at linearizable reads, so a wrap that another instance has just written
// is seen.
func (b *Block) listWraps(ctx context.Context, prefix string) ([]wrapItem, error) {
	var out []wrapItem
	var token []byte
	for range 100_000 {
		res, err := b.opts.Items.Query(ctx, connect.NewRequest(&storev1alpha1.QueryRequest{
			Prefix: []byte(prefix), PageSize: 500, PageToken: token, Linearizable: true,
		}))
		if err != nil {
			return nil, errStore
		}
		for _, it := range res.Msg.GetItems() {
			ref, recipient, ok := b.parseWrapKey(string(it.GetKey()))
			if !ok {
				continue
			}
			w := &sealv1alpha1.DataKeyWrap{}
			if proto.Unmarshal(it.GetValue(), w) != nil {
				continue
			}
			out = append(out, wrapItem{ref: ref, recipient: recipient, wrap: w})
		}
		token = res.Msg.GetNextPageToken()
		if len(token) == 0 {
			return out, nil
		}
	}
	return nil, errStore
}

// createWraps writes wraps with conditional creates, all or none. errTaken when one of them exists.
func (b *Block) createWraps(ctx context.Context, items []wrapItem) error {
	req := &storev1alpha1.TransactRequest{}
	for _, it := range items {
		v, err := proto.Marshal(it.wrap)
		if err != nil {
			return err
		}
		k := []byte(b.wrapKey(it.ref, it.recipient))
		req.Conditions = append(req.Conditions, &storev1alpha1.Condition{Key: k, Expect: &storev1alpha1.Condition_Absent{Absent: true}})
		req.Puts = append(req.Puts, &storev1alpha1.Put{Key: k, Value: v})
	}
	if _, err := b.opts.Items.Transact(ctx, connect.NewRequest(req)); err != nil {
		if r, ok := refusal.Reason(err); ok && r == "version" && connect.CodeOf(err) == connect.CodeAborted {
			return errTaken
		}
		return errStore
	}
	return nil
}

// deleteWraps removes wraps, in batches the strictest driver takes. It returns how many it removed before a failure.
func (b *Block) deleteWraps(ctx context.Context, items []wrapItem) (int, error) {
	done := 0
	for start := 0; start < len(items); start += maxDeletes {
		batch := items[start:min(start+maxDeletes, len(items))]
		req := &storev1alpha1.TransactRequest{}
		for _, it := range batch {
			req.Deletes = append(req.Deletes, &storev1alpha1.Delete{Key: []byte(b.wrapKey(it.ref, it.recipient))})
		}
		if _, err := b.opts.Items.Transact(ctx, connect.NewRequest(req)); err != nil {
			return done, errStore
		}
		done += len(batch)
	}
	return done, nil
}
