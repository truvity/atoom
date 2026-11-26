// Package dynamodb is the DynamoDB driver of the item port of atoom, atoom.store.v1alpha1.ItemService: versioned items with an expiry and
// declared indexes, kept in one DynamoDB table. The Go module is github.com/truvity/atoom/store/dynamodb.
//
// It is a library and a thin main. The library is the Connect handler and the logic behind it: New takes a DynamoDB client and the name of
// a table and returns a Service that a program mounts in-process (a Lambda function links it in and calls it on an in-memory transport;
// there are no sidecars there), on a server of its own (Handler), or on a server that the host provides (Run). The main (cmd/dynamodb)
// serves the library on a UNIX socket and nothing more.
//
// What it maps (docs/layout.md has the table and the reasons). An item is one DynamoDB item whose sort key is the item's key; a version is
// a number the driver checks and raises in a condition; the expiry is a number the driver compares at read time and DynamoDB's time to
// live frees; a declared index entry is a second item (partition = the index name and value, sort = the key), written and deleted in the
// transaction of the item, so that an index is exactly as consistent as the table. Get and Query are strongly consistent reads when the
// caller asks for a linearizable one. Transact is a read of what it touches, a check of the conditions against it, and a conditional write
// that is a DynamoDB transaction (or one conditional write when one item changes); a write that loses a race reads again and decides again.
//
// What it does not offer, and says so: the change log. Capabilities says tail_log false, TailLog answers unimplemented, and a put or a
// delete that asks for a log row is refused with the reason log and writes nothing.
package dynamodb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/truvity/atoom/store/gen/atoom/store/v1alpha1/storev1alpha1connect"
)

// The attributes of the table. The two key attributes are binary: a key is bytes, and DynamoDB orders binary by unsigned byte, which is
// the order of the contract.
const (
	attrPK      = "pk"  // B: the partition: one constant for the items, the index name and value for an index entry
	attrSK      = "sk"  // B: the item's key
	attrVersion = "v"   // N: the version
	attrValue   = "val" // B: the bytes the consumer gave
	attrExpires = "exp" // N: the expiry in milliseconds since the epoch, set by the consumer
	attrTTL     = "ttl" // N: the expiry in whole seconds, rounded up, which DynamoDB's time to live reads
	attrIndexes = "ix"  // L of M{n: S, v: B}: the declared index entries of the item
)

// TTLAttribute is the name of the attribute that the table's time to live reads: the expiry of an item, in seconds since the epoch.
const TTLAttribute = attrTTL

// DefaultTable is the name of the table when a host names none.
const DefaultTable = "atoom-store"

// API is the part of the DynamoDB client that the driver calls. *dynamodb.Client satisfies it; a test passes a client of its own.
type API interface {
	GetItem(ctx context.Context, in *awsdynamodb.GetItemInput, opts ...func(*awsdynamodb.Options)) (*awsdynamodb.GetItemOutput, error)
	BatchGetItem(ctx context.Context, in *awsdynamodb.BatchGetItemInput, opts ...func(*awsdynamodb.Options)) (*awsdynamodb.BatchGetItemOutput, error)
	Query(ctx context.Context, in *awsdynamodb.QueryInput, opts ...func(*awsdynamodb.Options)) (*awsdynamodb.QueryOutput, error)
	PutItem(ctx context.Context, in *awsdynamodb.PutItemInput, opts ...func(*awsdynamodb.Options)) (*awsdynamodb.PutItemOutput, error)
	DeleteItem(ctx context.Context, in *awsdynamodb.DeleteItemInput, opts ...func(*awsdynamodb.Options)) (*awsdynamodb.DeleteItemOutput, error)
	TransactWriteItems(ctx context.Context, in *awsdynamodb.TransactWriteItemsInput, opts ...func(*awsdynamodb.Options)) (*awsdynamodb.TransactWriteItemsOutput, error)
}

// TableAPI is what EnsureTable and CheckTable call.
type TableAPI interface {
	CreateTable(ctx context.Context, in *awsdynamodb.CreateTableInput, opts ...func(*awsdynamodb.Options)) (*awsdynamodb.CreateTableOutput, error)
	DescribeTable(ctx context.Context, in *awsdynamodb.DescribeTableInput, opts ...func(*awsdynamodb.Options)) (*awsdynamodb.DescribeTableOutput, error)
	UpdateTimeToLive(ctx context.Context, in *awsdynamodb.UpdateTimeToLiveInput, opts ...func(*awsdynamodb.Options)) (*awsdynamodb.UpdateTimeToLiveOutput, error)
}

var (
	_ API      = (*awsdynamodb.Client)(nil)
	_ TableAPI = (*awsdynamodb.Client)(nil)
)

// Options are what the driver needs.
type Options struct {
	// DynamoDB is the client: its region, credentials and endpoint are the host's. Required.
	DynamoDB API
	// Table is the name of the table, made by EnsureTable or by the host's deployment code. Empty is DefaultTable.
	Table string
	// Caller names the caller of a call for the journal line of a refusal; none when nil. The host knows its callers.
	Caller func(context.Context) string
	// Logger receives every line the driver writes. nil means slog.Default(), read at each line; the host decides where the lines go,
	// or that they go nowhere.
	Logger *slog.Logger
}

// Host is what a program that runs the driver provides.
type Host struct {
	// Serve serves the handler of the item service on the host's own listener until ctx ends, and returns nil when it ends so.
	Serve func(ctx context.Context, path string, h http.Handler) error
	// HandlerOptions are the Connect options of the handler: the read limit, the interceptors of the host.
	HandlerOptions []connect.HandlerOption
}

// Service implements the item port over one DynamoDB table.
type Service struct {
	storev1alpha1connect.UnimplementedItemServiceHandler
	api    API
	table  string
	caller func(context.Context) string
	logger *slog.Logger
	now    func() time.Time
}

// New returns the service. It calls nothing: the table is the host's (EnsureTable makes one for a test or a first start).
func New(opts Options) (*Service, error) {
	if opts.DynamoDB == nil {
		return nil, errors.New("dynamodb: Options.DynamoDB is required")
	}
	table := opts.Table
	if table == "" {
		table = DefaultTable
	}
	if err := checkTableName(table); err != nil {
		return nil, err
	}
	return &Service{api: opts.DynamoDB, table: table, caller: opts.Caller, logger: opts.Logger, now: time.Now}, nil
}

// log is the logger of the driver: the host's Options.Logger, or slog.Default() read now when the host gave none.
func (s *Service) log() *slog.Logger {
	if s.logger != nil {
		return s.logger
	}
	return slog.Default()
}

// checkTableName holds a name to what DynamoDB accepts: 3 to 255 of letters, digits, '_', '-' and '.'.
func checkTableName(name string) error {
	if len(name) < 3 || len(name) > 255 {
		return fmt.Errorf("dynamodb: the table name %q must be 3 to 255 characters", name)
	}
	for _, r := range name {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.'
		if !ok {
			return fmt.Errorf("dynamodb: the table name %q holds a character that DynamoDB refuses", name)
		}
	}
	return nil
}

// Handler returns the Connect handler of the item service, to mount on a server or an in-memory transport.
func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return storev1alpha1connect.NewItemServiceHandler(s, opts...)
}

// Run serves the item port on the host's server until ctx ends.
func Run(ctx context.Context, opts Options, host Host) error {
	if host.Serve == nil {
		return errors.New("dynamodb: the host has no Serve")
	}
	svc, err := New(opts)
	if err != nil {
		return err
	}
	svc.log().Info("dynamodb item driver starting", "table", svc.table)
	path, h := svc.Handler(host.HandlerOptions...)
	return host.Serve(ctx, path, h)
}

// ErrTableMismatch is a table whose keys are not the ones the driver writes.
var ErrTableMismatch = errors.New("dynamodb: the table's key schema is not (pk: binary hash, sk: binary range)")

// EnsureTable makes the table if it is absent, waits until it is active, and enables the time to live on the attribute ttl. It is for a
// test, a local installation and a first start; a deployment that makes its tables itself (Terraform, CloudFormation) makes the same
// shape (docs/layout.md, "The table") and calls CheckTable, or nothing. It is idempotent: a table that is there with the right keys is
// left as it is, and one with other keys is ErrTableMismatch.
func EnsureTable(ctx context.Context, api TableAPI, table string) error {
	if table == "" {
		table = DefaultTable
	}
	if err := checkTableName(table); err != nil {
		return err
	}
	_, err := api.CreateTable(ctx, &awsdynamodb.CreateTableInput{
		TableName: aws.String(table),
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: aws.String(attrPK), AttributeType: types.ScalarAttributeTypeB},
			{AttributeName: aws.String(attrSK), AttributeType: types.ScalarAttributeTypeB},
		},
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String(attrPK), KeyType: types.KeyTypeHash},
			{AttributeName: aws.String(attrSK), KeyType: types.KeyTypeRange},
		},
		BillingMode: types.BillingModePayPerRequest,
	})
	if err != nil {
		var inUse *types.ResourceInUseException
		if !errors.As(err, &inUse) {
			return fmt.Errorf("create the table: %w", err)
		}
	}
	if err := waitActive(ctx, api, table); err != nil {
		return err
	}
	if err := CheckTable(ctx, api, table); err != nil {
		return err
	}
	_, err = api.UpdateTimeToLive(ctx, &awsdynamodb.UpdateTimeToLiveInput{
		TableName:               aws.String(table),
		TimeToLiveSpecification: &types.TimeToLiveSpecification{AttributeName: aws.String(attrTTL), Enabled: aws.Bool(true)},
	})
	if err != nil {
		// DynamoDB answers a ValidationException when the time to live is already on: that is the state that was asked for.
		var v interface{ ErrorCode() string }
		if errors.As(err, &v) && v.ErrorCode() == "ValidationException" {
			return nil
		}
		return fmt.Errorf("enable the time to live: %w", err)
	}
	return nil
}

func waitActive(ctx context.Context, api TableAPI, table string) error {
	for range 120 {
		out, err := api.DescribeTable(ctx, &awsdynamodb.DescribeTableInput{TableName: aws.String(table)})
		if err == nil && out.Table != nil && out.Table.TableStatus == types.TableStatusActive {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("dynamodb: the table %s did not become active", table)
}

// CheckTable reads the table's description and holds its key schema to the one the driver writes.
func CheckTable(ctx context.Context, api TableAPI, table string) error {
	if table == "" {
		table = DefaultTable
	}
	out, err := api.DescribeTable(ctx, &awsdynamodb.DescribeTableInput{TableName: aws.String(table)})
	if err != nil {
		return fmt.Errorf("describe the table: %w", err)
	}
	t := out.Table
	if t == nil || len(t.KeySchema) != 2 {
		return ErrTableMismatch
	}
	types2 := map[string]types.ScalarAttributeType{}
	for _, d := range t.AttributeDefinitions {
		types2[aws.ToString(d.AttributeName)] = d.AttributeType
	}
	for _, k := range t.KeySchema {
		name := aws.ToString(k.AttributeName)
		want := map[string]types.KeyType{attrPK: types.KeyTypeHash, attrSK: types.KeyTypeRange}[name]
		if want == "" || k.KeyType != want || types2[name] != types.ScalarAttributeTypeB {
			return ErrTableMismatch
		}
	}
	return nil
}
