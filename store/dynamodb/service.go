package dynamodb

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"time"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/smithy-go"
	"google.golang.org/protobuf/types/known/timestamppb"

	storev1alpha1 "github.com/truvity/atoom/store/gen/atoom/store/v1alpha1"
)

// msToTime is the time of a number of milliseconds since the epoch; tsToMs is the other way (the engines keep expiry in milliseconds).
func msToTime(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

func tsToMs(t *timestamppb.Timestamp) int64 {
	return t.GetSeconds()*1000 + int64(t.GetNanos())/1_000_000
}

func (s *Service) nowMs() int64 { return s.now().UnixMilli() }

// Capabilities says what the driver offers: DynamoDB keeps no change log the contract could use, so tail_log is false.
func (*Service) Capabilities(context.Context, *connect.Request[storev1alpha1.CapabilitiesRequest]) (*connect.Response[storev1alpha1.CapabilitiesResponse], error) {
	return connect.NewResponse(&storev1alpha1.CapabilitiesResponse{Store: "dynamodb", TailLog: false}), nil
}

// Get reads one item. A strongly consistent read is DynamoDB's ConsistentRead, which is what linearizable asks for.
func (s *Service) Get(ctx context.Context, req *connect.Request[storev1alpha1.ItemGetRequest]) (*connect.Response[storev1alpha1.ItemGetResponse], error) {
	key := req.Msg.GetKey()
	if err := checkKey(key, "key"); err != nil {
		return nil, s.refused(ctx, "Get", err)
	}
	out, err := s.api.GetItem(ctx, &awsdynamodb.GetItemInput{
		TableName:      aws.String(s.table),
		Key:            itemKey(key),
		ConsistentRead: aws.Bool(req.Msg.GetLinearizable()),
	})
	if err != nil {
		return nil, s.failed(ctx, "Get", err)
	}
	if len(out.Item) == 0 {
		return nil, refuse(connect.CodeNotFound, "absent", "")
	}
	it, err := decode(out.Item)
	if err != nil {
		return nil, s.failed(ctx, "Get", err)
	}
	if !it.live(s.nowMs()) {
		return nil, refuse(connect.CodeNotFound, "absent", "")
	}
	return connect.NewResponse(&storev1alpha1.ItemGetResponse{Item: it.driverItem()}), nil
}

// Query lists items by key prefix, or by index entry, in key order, one page at a time. It reads one item more than the page holds, to
// know whether there is a next page, and skips what has expired.
func (s *Service) Query(ctx context.Context, req *connect.Request[storev1alpha1.QueryRequest]) (*connect.Response[storev1alpha1.QueryResponse], error) {
	m := req.Msg
	if len(m.GetPrefix()) > 0 {
		if err := checkKey(m.GetPrefix(), "prefix"); err != nil {
			return nil, s.refused(ctx, "Query", err)
		}
	}
	size, err := pageSize(m.GetPageSize())
	if err != nil {
		return nil, s.refused(ctx, "Query", err)
	}
	if err := checkToken(m.GetPageToken(), m.GetPrefix()); err != nil {
		return nil, s.refused(ctx, "Query", err)
	}
	if m.GetIndex() == "" && len(m.GetIndexValue()) > 0 {
		return nil, s.refused(ctx, "Query", refuse(connect.CodeInvalidArgument, "index", "index"))
	}
	var found []*stored
	if m.GetIndex() == "" {
		found, err = s.listByPrefix(ctx, m, size+1)
	} else {
		found, err = s.listByIndex(ctx, m, size+1)
	}
	if err != nil {
		return nil, s.failed(ctx, "Query", err)
	}
	res := &storev1alpha1.QueryResponse{}
	if len(found) > size {
		found = found[:size]
		res.NextPageToken = bytes.Clone(found[len(found)-1].key)
	}
	for _, it := range found {
		res.Items = append(res.Items, it.driverItem())
	}
	return connect.NewResponse(res), nil
}

// keyCondition is the key condition of a Query of the items (or of the entries of one index value) under a key prefix.
func keyCondition(pk []byte, prefix []byte) (string, map[string]string, map[string]types.AttributeValue) {
	names := map[string]string{"#pk": attrPK}
	values := map[string]types.AttributeValue{":pk": bin(pk)}
	expr := "#pk = :pk"
	if len(prefix) > 0 {
		names["#sk"] = attrSK
		values[":pre"] = bin(prefix)
		expr += " AND begins_with(#sk, :pre)"
	}
	return expr, names, values
}

// startKey is where a Query goes on after the page token: the sort key just after the last key of the previous page.
func startKey(pk, token []byte) map[string]types.AttributeValue {
	if len(token) == 0 {
		return nil
	}
	return map[string]types.AttributeValue{attrPK: bin(pk), attrSK: bin(token)}
}

// listByPrefix reads the live items under the prefix, after the token, until it has want of them or the collection ends.
func (s *Service) listByPrefix(ctx context.Context, m *storev1alpha1.QueryRequest, want int) ([]*stored, error) {
	expr, names, values := keyCondition([]byte{partItem}, m.GetPrefix())
	start := startKey([]byte{partItem}, m.GetPageToken())
	var found []*stored
	for len(found) < want {
		out, err := s.api.Query(ctx, &awsdynamodb.QueryInput{
			TableName:                 aws.String(s.table),
			KeyConditionExpression:    aws.String(expr),
			ExpressionAttributeNames:  names,
			ExpressionAttributeValues: values,
			ConsistentRead:            aws.Bool(m.GetLinearizable()),
			ExclusiveStartKey:         start,
			Limit:                     aws.Int32(int32(want - len(found))), //nolint:gosec // at most 501
		})
		if err != nil {
			return nil, err
		}
		now := s.nowMs()
		for _, raw := range out.Items {
			it, err := decode(raw)
			if err != nil {
				return nil, err
			}
			if it.live(now) {
				found = append(found, it)
			}
		}
		if len(out.LastEvaluatedKey) == 0 {
			break
		}
		start = out.LastEvaluatedKey
	}
	return found, nil
}

// listByIndex reads the entries of one index value under the prefix, then the items they name, and keeps those that are live and still
// declare the entry (an entry that outlived its item, or that a rewrite removed and a weaker read still shows, names nothing).
func (s *Service) listByIndex(ctx context.Context, m *storev1alpha1.QueryRequest, want int) ([]*stored, error) {
	e := entry{name: m.GetIndex(), value: m.GetIndexValue()}
	if len(e.name) > maxIndexName || len(e.value) > maxIndexValue || bytes.IndexByte([]byte(e.name), 0) >= 0 {
		// No item can carry such an entry (a put with one is refused): nothing matches.
		return nil, nil
	}
	pk := entryPKBytes(e)
	expr, names, values := keyCondition(pk, m.GetPrefix())
	names["#sk"] = attrSK // the projection names it whether or not the key condition does
	start := startKey(pk, m.GetPageToken())
	var found []*stored
	for len(found) < want {
		limit := min(want-len(found), 100)
		out, err := s.api.Query(ctx, &awsdynamodb.QueryInput{
			TableName:                 aws.String(s.table),
			KeyConditionExpression:    aws.String(expr),
			ExpressionAttributeNames:  names,
			ExpressionAttributeValues: values,
			ConsistentRead:            aws.Bool(m.GetLinearizable()),
			ExclusiveStartKey:         start,
			Limit:                     aws.Int32(int32(limit)), //nolint:gosec // at most 100
			ProjectionExpression:      aws.String("#sk"),
		})
		if err != nil {
			return nil, err
		}
		keys := make([][]byte, 0, len(out.Items))
		for _, raw := range out.Items {
			sk, ok := raw[attrSK].(*types.AttributeValueMemberB)
			if !ok {
				return nil, errBadItem
			}
			keys = append(keys, sk.Value)
		}
		items, err := s.batchGet(ctx, keys, m.GetLinearizable())
		if err != nil {
			return nil, err
		}
		now := s.nowMs()
		for _, k := range keys {
			it := items[string(k)]
			if it.live(now) && hasEntry(it.indexes, e) {
				found = append(found, it)
			}
		}
		if len(out.LastEvaluatedKey) == 0 {
			break
		}
		start = out.LastEvaluatedKey
	}
	return found, nil
}

// batchGet reads the items of some keys (at most 100 at a time), as a map by key; a key with no item has no entry.
func (s *Service) batchGet(ctx context.Context, keys [][]byte, consistent bool) (map[string]*stored, error) {
	out := make(map[string]*stored, len(keys))
	for len(keys) > 0 {
		n := min(len(keys), 100)
		pending := make([]map[string]types.AttributeValue, 0, n)
		for _, k := range keys[:n] {
			pending = append(pending, itemKey(k))
		}
		keys = keys[n:]
		for attempt := 0; len(pending) > 0; attempt++ {
			res, err := s.api.BatchGetItem(ctx, &awsdynamodb.BatchGetItemInput{
				RequestItems: map[string]types.KeysAndAttributes{
					s.table: {Keys: pending, ConsistentRead: aws.Bool(consistent)},
				},
			})
			if err != nil {
				return nil, err
			}
			for _, raw := range res.Responses[s.table] {
				it, err := decode(raw)
				if err != nil {
					return nil, err
				}
				out[string(it.key)] = it
			}
			pending = res.UnprocessedKeys[s.table].Keys
			if len(pending) > 0 {
				if attempt >= 8 {
					return nil, errors.New("dynamodb: BatchGetItem left keys unprocessed")
				}
				if err := sleep(ctx, backoff(attempt)); err != nil {
					return nil, err
				}
			}
		}
	}
	return out, nil
}

// refused logs a refusal that the driver made itself (the method, the code and the reason, never a value) and returns it.
func (s *Service) refused(ctx context.Context, method string, err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		slog.Warn("refused", "method", method, "code", ce.Code().String(), "message", ce.Message(), "caller", s.callerOf(ctx))
	}
	return err
}

// failed maps an error of the engine to its refusal. What DynamoDB said goes to the journal and not to the caller: the refusal names a
// code and the reason engine, never a value.
func (s *Service) failed(ctx context.Context, method string, err error) error {
	var ce *connect.Error
	switch {
	case errors.As(err, &ce):
		return err
	case errors.Is(err, context.Canceled):
		return refuse(connect.CodeCanceled, "engine", "")
	case errors.Is(err, context.DeadlineExceeded):
		return refuse(connect.CodeDeadlineExceeded, "engine", "")
	}
	var api smithy.APIError
	var netErr net.Error
	switch {
	case errors.As(err, &api):
		if api.ErrorCode() == "ValidationException" {
			// A request the engine refuses as malformed: the driver's own mistake or a limit it did not know; the journal says which.
			slog.Error("DynamoDB refused a request as invalid", "method", method, "error", err.Error())
			return refuse(connect.CodeInvalidArgument, "batch", "")
		}
		// Everything else (a throttle, a missing table, an access denial) is an engine that cannot answer: the contract has one word for it,
		// and the journal has the code.
	case errors.As(err, &netErr):
		// A network failure is the same.
	}
	slog.Error("DynamoDB failed", "method", method, "error", err.Error(), "caller", s.callerOf(ctx))
	return refuse(connect.CodeUnavailable, "engine", "")
}

// callerOf names the caller for the journal, or "" when the host names none.
func (s *Service) callerOf(ctx context.Context) string {
	if s.caller == nil {
		return ""
	}
	return s.caller(ctx)
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// backoff is the wait before retry number attempt (from 0): short and growing, with no jitter source in the library (the callers of a
// race are the jitter).
func backoff(attempt int) time.Duration {
	return time.Duration(10<<min(attempt, 6)) * time.Millisecond
}
