package seal

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	sealv1alpha1 "github.com/truvity/atoom/blocks/seal/gen/atoom/seal/v1alpha1"
	"github.com/truvity/atoom/common/refusal"
	storev1alpha1 "github.com/truvity/atoom/store/gen/atoom/store/v1alpha1"
)

// The seal limit and the compromised marks (docs/seal-block.md, "The seal limit", "Compromised generations"). Two more kinds of item,
// beside the wraps:
//
//	<prefix>count/<scope>/<purpose>/<generation>  ->  SealCounter     the seals reserved from the generation
//	<prefix>mark/<scope>/<purpose>/<generation>   ->  GenerationMark  the generation is compromised

// errLimit says the generation has no seal left: the block rotates.
var errLimit = errors.New("seal: the generation has made its seals")

// errCompromised says the generation is marked compromised: it seals no more.
var errCompromised = errors.New("seal: the generation is compromised")

// reserveAttempts bounds the conditional updates of a counter that other instances keep changing under one reservation.
const reserveAttempts = 16

// budget is what an instance has reserved of a generation and not used yet.
type budget struct {
	mu   sync.Mutex
	left uint64
}

func (b *Block) stateKey(kind string, ref keyRef) string {
	return b.opts.KeyPrefix + kind + "/" + scopePart(ref.scope) + "/" + ref.purpose + "/" + strconv.FormatUint(uint64(ref.generation), 10)
}

func (b *Block) budgetOf(ref keyRef) *budget {
	b.budgetMu.Lock()
	defer b.budgetMu.Unlock()
	bu := b.budgets[ref]
	if bu == nil {
		bu = &budget{}
		b.budgets[ref] = bu
	}
	return bu
}

// dropBudgets forgets what this instance reserved of a line's generations (the line was erased, with its counters).
func (b *Block) dropBudgets(l line) {
	b.budgetMu.Lock()
	defer b.budgetMu.Unlock()
	for ref := range b.budgets {
		if ref.line() == l {
			delete(b.budgets, ref)
		}
	}
}

// take takes one seal from this instance's budget of ref, reserving N more in the durable counter when the budget is empty. errLimit
// when the generation has made SealsPerGeneration seals, counted over every instance: what an instance reserved is counted whether it
// was used or not, so the counter is an upper bound of the seals, and a generation never makes more than the limit.
func (b *Block) take(ctx context.Context, ref keyRef) error {
	bu := b.budgetOf(ref)
	bu.mu.Lock()
	defer bu.mu.Unlock()
	if bu.left == 0 {
		n, err := b.reserve(ctx, ref)
		if err != nil {
			return err
		}
		bu.left = n
	}
	bu.left--
	return nil
}

// reserve adds up to N seals to the durable counter of ref by a conditional update, and returns how many it added.
func (b *Block) reserve(ctx context.Context, ref keyRef) (uint64, error) {
	key := []byte(b.stateKey("count", ref))
	for range reserveAttempts {
		var reserved, version uint64
		res, err := b.opts.Items.Get(ctx, connect.NewRequest(&storev1alpha1.ItemGetRequest{Key: key, Linearizable: true}))
		switch {
		case err == nil:
			c := &sealv1alpha1.SealCounter{}
			if proto.Unmarshal(res.Msg.GetItem().GetValue(), c) != nil {
				return 0, errors.New("seal: a counter does not decode")
			}
			reserved, version = c.GetReserved(), res.Msg.GetItem().GetVersion()
		case connect.CodeOf(err) == connect.CodeNotFound:
		default:
			return 0, errStore
		}
		limit := b.opts.SealsPerGeneration
		if reserved >= limit {
			return 0, errLimit
		}
		n := min(uint64(b.opts.SealReservation), limit-reserved)
		v, err := proto.Marshal(&sealv1alpha1.SealCounter{Reserved: reserved + n})
		if err != nil {
			return 0, err
		}
		cond := &storev1alpha1.Condition{Key: key, Expect: &storev1alpha1.Condition_Absent{Absent: true}}
		if version != 0 {
			cond.Expect = &storev1alpha1.Condition_Version{Version: version}
		}
		_, err = b.opts.Items.Transact(ctx, connect.NewRequest(&storev1alpha1.TransactRequest{
			Conditions: []*storev1alpha1.Condition{cond},
			Puts:       []*storev1alpha1.Put{{Key: key, Value: v}},
		}))
		if err == nil {
			return n, nil
		}
		if r, _ := refusal.Reason(err); connect.CodeOf(err) != connect.CodeAborted || r != "version" {
			return 0, errStore
		}
		// Another instance reserved in between: read the counter again.
	}
	return 0, errStore
}

// readMark reads whether a generation is marked compromised.
func (b *Block) readMark(ctx context.Context, ref keyRef) (bool, error) {
	res, err := b.opts.Items.Get(ctx, connect.NewRequest(&storev1alpha1.ItemGetRequest{Key: []byte(b.stateKey("mark", ref)), Linearizable: true}))
	if err != nil {
		if connect.CodeOf(err) == connect.CodeNotFound {
			return false, nil
		}
		return false, errStore
	}
	m := &sealv1alpha1.GenerationMark{}
	if proto.Unmarshal(res.Msg.GetItem().GetValue(), m) != nil {
		return false, errors.New("seal: a mark does not decode")
	}
	return m.GetCompromised(), nil
}

// writeMark marks a generation compromised. A mark is never taken back: it is written over whatever is there.
func (b *Block) writeMark(ctx context.Context, ref keyRef) error {
	v, err := proto.Marshal(&sealv1alpha1.GenerationMark{Compromised: true})
	if err != nil {
		return err
	}
	_, err = b.opts.Items.Transact(ctx, connect.NewRequest(&storev1alpha1.TransactRequest{
		Puts: []*storev1alpha1.Put{{Key: []byte(b.stateKey("mark", ref)), Value: v}},
	}))
	if err != nil {
		return errStore
	}
	return nil
}

// listKeys lists the keys of the items under a prefix.
func (b *Block) listKeys(ctx context.Context, prefix string) ([]string, error) {
	var out []string
	var token []byte
	for range 100_000 {
		res, err := b.opts.Items.Query(ctx, connect.NewRequest(&storev1alpha1.QueryRequest{Prefix: []byte(prefix), PageSize: 500, PageToken: token, Linearizable: true}))
		if err != nil {
			return nil, errStore
		}
		for _, it := range res.Msg.GetItems() {
			out = append(out, string(it.GetKey()))
		}
		if token = res.Msg.GetNextPageToken(); len(token) == 0 {
			return out, nil
		}
	}
	return nil, errStore
}

// deleteLineState deletes the counters and the marks of every generation of a line.
func (b *Block) deleteLineState(ctx context.Context, l line) error {
	for _, kind := range []string{"count", "mark"} {
		keys, err := b.listKeys(ctx, b.opts.KeyPrefix+kind+"/"+scopePart(l.scope)+"/"+l.purpose+"/")
		if err != nil {
			return err
		}
		for start := 0; start < len(keys); start += maxDeletes {
			req := &storev1alpha1.TransactRequest{}
			for _, k := range keys[start:min(start+maxDeletes, len(keys))] {
				req.Deletes = append(req.Deletes, &storev1alpha1.Delete{Key: []byte(k)})
			}
			if _, err := b.opts.Items.Transact(ctx, connect.NewRequest(req)); err != nil {
				return errStore
			}
		}
	}
	return nil
}

// dropMarked drops from the cache every key whose generation is marked and whose entry does not know it yet, and forgets the current
// generation of its line: a Rewrap pass is how a mark reaches an instance before its cache entry ends.
func (b *Block) dropMarked(ctx context.Context) error {
	keys, err := b.listKeys(ctx, b.opts.KeyPrefix+"mark/")
	if err != nil {
		return err
	}
	marked := map[keyRef]bool{}
	for _, k := range keys {
		parts := strings.Split(strings.TrimPrefix(k, b.opts.KeyPrefix+"mark/"), "/")
		if len(parts) != 3 {
			continue
		}
		g, err := strconv.ParseUint(parts[2], 10, 32)
		if err != nil {
			continue
		}
		scope := parts[0]
		if scope == "-" {
			scope = ""
		}
		marked[keyRef{scope: scope, purpose: parts[1], generation: uint32(g)}] = true
	}
	b.cache.mu.Lock()
	var stale []keyRef
	for ref, e := range b.cache.m {
		if marked[ref] && !e.compromised {
			stale = append(stale, ref)
		}
	}
	b.cache.mu.Unlock()
	for _, ref := range stale {
		b.cache.drop(ref)
		b.cache.forgetGeneration(ref.line())
	}
	return nil
}
