package dynamodb

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	storev1alpha1 "github.com/truvity/atoom/store/gen/atoom/store/v1alpha1"
)

// maxAttempts is how many times a Transact reads, decides and writes again after it loses a race: each loss means another writer changed
// something it touched, so the next read sees a state that is newer than the last.
const maxAttempts = 8

// Transact applies conditions, then deletes, then puts, all or none.
//
// It reads every key it touches (strongly consistent), checks the conditions against what it read, and writes with a condition that the
// keys are still as read: a put checks the version it read (or that the item is absent), a delete the version of the item it removes, a key
// that is only named by a condition is a condition check. A write that finds the item changed has lost a race; it reads again and decides
// again, so the conditions of the caller are always judged against the state the write is made on. One put with one condition on the same
// key is one conditional PutItem and not a transaction (the contract names it, for counters).
func (s *Service) Transact(ctx context.Context, req *connect.Request[storev1alpha1.TransactRequest]) (*connect.Response[storev1alpha1.TransactResponse], error) {
	expected, err := checkTransact(req.Msg)
	if err != nil {
		return nil, s.refused(ctx, "Transact", err)
	}
	keys := touched(req.Msg)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, backoff(attempt)); err != nil {
				return nil, s.failed(ctx, "Transact", err)
			}
		}
		current, err := s.batchGet(ctx, keys, true)
		if err != nil {
			return nil, s.failed(ctx, "Transact", err)
		}
		now := s.nowMs()
		for i, c := range req.Msg.GetConditions() {
			it := current[string(c.GetKey())]
			held := false
			if expected[i].absent {
				held = !it.live(now)
			} else {
				held = it.live(now) && it.version == expected[i].version
			}
			if !held {
				return nil, s.refused(ctx, "Transact", refuse(connect.CodeAborted, "version", fmt.Sprintf("conditions[%d]", i)))
			}
		}
		plan, written, err := plan(req.Msg, current)
		if err != nil {
			return nil, s.refused(ctx, "Transact", err)
		}
		lost, err := s.write(ctx, plan)
		if err != nil {
			return nil, s.failed(ctx, "Transact", err)
		}
		if lost {
			continue
		}
		res := &storev1alpha1.TransactResponse{}
		for _, it := range written {
			res.Items = append(res.Items, it.driverItem())
		}
		return connect.NewResponse(res), nil
	}
	// Eight reads in a row found the keys changed under them: the engine is busy with other writers of the same keys. Not a failed condition
	// (those are judged on every read), so not aborted/version: a retry is the caller's call.
	return nil, s.failed(ctx, "Transact", errors.New("dynamodb: the transaction lost its race eight times"))
}

// touched is every key that a request names, once, in the order of its first mention.
func touched(req *storev1alpha1.TransactRequest) [][]byte {
	var keys [][]byte
	seen := map[string]bool{}
	add := func(k []byte) {
		if !seen[string(k)] {
			seen[string(k)] = true
			keys = append(keys, k)
		}
	}
	for _, c := range req.GetConditions() {
		add(c.GetKey())
	}
	for _, p := range req.GetPuts() {
		add(p.GetKey())
	}
	for _, d := range req.GetDeletes() {
		add(d.GetKey())
	}
	return keys
}

// action is one write of the plan: a DynamoDB transaction item, and what a single write of it would be.
type action struct {
	item types.TransactWriteItem
	// kind is put, delete or check, so that a plan of one write that is not a check runs as one conditional call.
	kind string
}

const (
	kindPut    = "put"
	kindDelete = "delete"
	kindCheck  = "check"
)

// guard is the condition that a key is still as it was read: the version of the item that was there, or that none was.
func guard(it *stored) (string, map[string]string, map[string]types.AttributeValue) {
	if it == nil {
		return "attribute_not_exists(#pk)", map[string]string{"#pk": attrPK}, nil
	}
	return "#v = :old", map[string]string{"#v": attrVersion}, map[string]types.AttributeValue{":old": unum(it.version)}
}

// plan builds the writes of a request against what was read, and the items that its puts make. The conditions have already held.
func plan(req *storev1alpha1.TransactRequest, current map[string]*stored) ([]action, []*stored, error) {
	var actions []action
	var written []*stored
	covered := map[string]bool{} // the keys whose item a write of the plan guards
	for _, d := range req.GetDeletes() {
		old := current[string(d.GetKey())]
		if old == nil {
			continue // nothing to remove; a delete of an absent key is not an error and writes nothing
		}
		covered[string(d.GetKey())] = true
		cond, names, values := guard(old)
		actions = append(actions, action{kind: kindDelete, item: types.TransactWriteItem{Delete: &types.Delete{
			TableName: aws.String(""), Key: itemKey(old.key), ConditionExpression: aws.String(cond),
			ExpressionAttributeNames: names, ExpressionAttributeValues: values,
		}}})
		for _, e := range old.indexes {
			actions = append(actions, action{kind: kindDelete, item: types.TransactWriteItem{Delete: &types.Delete{
				TableName: aws.String(""), Key: entryKey(e, old.key),
			}}})
		}
	}
	for _, p := range req.GetPuts() {
		old := current[string(p.GetKey())]
		covered[string(p.GetKey())] = true
		next := &stored{key: bytes.Clone(p.GetKey()), version: 1, value: bytes.Clone(p.GetValue()), indexes: entriesOf(p.GetIndexes())}
		if old != nil {
			// Over an item past its expiry that nothing has freed the version continues: it never goes back.
			next.version = old.version + 1
		}
		if p.GetExpiresAt() != nil {
			next.expMs, next.hasExp = tsToMs(p.GetExpiresAt()), true
		}
		written = append(written, next)
		cond, names, values := guard(old)
		actions = append(actions, action{kind: kindPut, item: types.TransactWriteItem{Put: &types.Put{
			TableName: aws.String(""), Item: next.attributes(), ConditionExpression: aws.String(cond),
			ExpressionAttributeNames: names, ExpressionAttributeValues: values,
		}}})
		var oldEntries []entry
		oldTTL := int64(-1)
		if old != nil {
			oldEntries = old.indexes
			if old.hasExp {
				oldTTL = ttlSeconds(old.expMs)
			}
		}
		newTTL := int64(-1)
		if next.hasExp {
			newTTL = ttlSeconds(next.expMs)
		}
		for _, e := range next.indexes {
			if hasEntry(oldEntries, e) && oldTTL == newTTL {
				continue // the entry is there and frees at the same time: nothing to write
			}
			actions = append(actions, action{kind: kindPut, item: types.TransactWriteItem{Put: &types.Put{
				TableName: aws.String(""), Item: next.entryAttributes(e),
			}}})
		}
		for _, e := range oldEntries {
			if !hasEntry(next.indexes, e) {
				actions = append(actions, action{kind: kindDelete, item: types.TransactWriteItem{Delete: &types.Delete{
					TableName: aws.String(""), Key: entryKey(e, p.GetKey()),
				}}})
			}
		}
	}
	// A key that a condition names and no write guards is held by a condition check, so that the condition is true at the write too.
	for _, c := range req.GetConditions() {
		k := string(c.GetKey())
		if covered[k] {
			continue
		}
		covered[k] = true
		cond, names, values := guard(current[k])
		actions = append(actions, action{kind: kindCheck, item: types.TransactWriteItem{ConditionCheck: &types.ConditionCheck{
			TableName: aws.String(""), Key: itemKey(c.GetKey()), ConditionExpression: aws.String(cond),
			ExpressionAttributeNames: names, ExpressionAttributeValues: values,
		}}})
	}
	if len(actions) > maxActions {
		field := "puts"
		if len(req.GetPuts()) == 0 {
			field = "deletes"
		}
		return nil, nil, batch(field)
	}
	return actions, written, nil
}

// write runs the plan. It reports lost when a condition of the write failed because another writer changed a key since the read: the caller
// reads again. Any other failure is an error.
func (s *Service) write(ctx context.Context, actions []action) (lost bool, err error) {
	switch {
	case len(actions) == 0:
		return false, nil
	case len(actions) == 1 && actions[0].kind != kindCheck:
		return s.writeOne(ctx, actions[0])
	}
	items := make([]types.TransactWriteItem, 0, len(actions))
	for _, a := range actions {
		items = append(items, s.named(a.item))
	}
	_, err = s.api.TransactWriteItems(ctx, &awsdynamodb.TransactWriteItemsInput{TransactItems: items})
	if err == nil {
		return false, nil
	}
	var canceled *types.TransactionCanceledException
	if errors.As(err, &canceled) {
		for _, r := range canceled.CancellationReasons {
			switch aws.ToString(r.Code) {
			case "ConditionalCheckFailed", "TransactionConflict":
				return true, nil
			}
		}
	}
	var inProgress *types.TransactionInProgressException
	if errors.As(err, &inProgress) {
		return true, nil
	}
	return false, err
}

// writeOne is the one write of a plan of one: a conditional PutItem or DeleteItem, which costs half a transaction.
func (s *Service) writeOne(ctx context.Context, a action) (lost bool, err error) {
	var werr error
	switch {
	case a.item.Put != nil:
		p := a.item.Put
		_, werr = s.api.PutItem(ctx, &awsdynamodb.PutItemInput{
			TableName: aws.String(s.table), Item: p.Item, ConditionExpression: p.ConditionExpression,
			ExpressionAttributeNames: p.ExpressionAttributeNames, ExpressionAttributeValues: p.ExpressionAttributeValues,
		})
	case a.item.Delete != nil:
		d := a.item.Delete
		_, werr = s.api.DeleteItem(ctx, &awsdynamodb.DeleteItemInput{
			TableName: aws.String(s.table), Key: d.Key, ConditionExpression: d.ConditionExpression,
			ExpressionAttributeNames: d.ExpressionAttributeNames, ExpressionAttributeValues: d.ExpressionAttributeValues,
		})
	}
	var failed *types.ConditionalCheckFailedException
	if errors.As(werr, &failed) {
		return true, nil
	}
	return false, werr
}

// named gives a transaction item the table's name (the plan is built without it).
func (s *Service) named(it types.TransactWriteItem) types.TransactWriteItem {
	switch {
	case it.Put != nil:
		p := *it.Put
		p.TableName = aws.String(s.table)
		it.Put = &p
	case it.Delete != nil:
		d := *it.Delete
		d.TableName = aws.String(s.table)
		it.Delete = &d
	case it.ConditionCheck != nil:
		c := *it.ConditionCheck
		c.TableName = aws.String(s.table)
		it.ConditionCheck = &c
	}
	return it
}
