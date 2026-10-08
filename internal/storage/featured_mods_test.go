package storage

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"sotnpresetapi/internal/catalog"
)

type featuredModClient struct {
	Client
	transactionInput *dynamodb.TransactWriteItemsInput
	queryInput       *dynamodb.QueryInput
	getInputs        []*dynamodb.GetItemInput
	getRows          []map[string]types.AttributeValue
	transactionError error
	queryError       error
	getError         error
	queryRows        []map[string]types.AttributeValue
}

func (c *featuredModClient) TransactWriteItems(_ context.Context, in *dynamodb.TransactWriteItemsInput, _ ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	c.transactionInput = in
	return &dynamodb.TransactWriteItemsOutput{}, c.transactionError
}

func (c *featuredModClient) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	i := len(c.getInputs)
	c.getInputs = append(c.getInputs, in)
	var row map[string]types.AttributeValue
	if i < len(c.getRows) {
		row = c.getRows[i]
	}
	return &dynamodb.GetItemOutput{Item: row}, c.getError
}

func (c *featuredModClient) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	c.queryInput = in
	return &dynamodb.QueryOutput{Items: c.queryRows}, c.queryError
}

func sampleFeaturedMod() catalog.FeaturedMod {
	return catalog.FeaturedMod{
		ID: "0123456789abcdef0123456789abcdef", Title: "A featured mod",
		Description: "A new adventure", Image: "featured-mods/0123456789abcdef0123456789abcdef.png",
		Patch:       "featured-mods/0123456789abcdef0123456789abcdef.ppf",
		ReleaseTime: "2099-02-03T04:05:06Z", CreatedBy: "publisher-sub",
		CreatedAt: "2026-10-07T15:04:05.12-07:00",
	}
}

func TestFeaturedModPersistence(t *testing.T) {
	client := &featuredModClient{}
	d := Dynamo{Client: client, Table: "catalog-test"}
	mod := sampleFeaturedMod()
	mod.DownloadAvailable = true // This must be recomputed when read.
	mod.DownloadURL = "/temporary/download"
	if err := d.SaveFeaturedMod(context.Background(), mod); err != nil {
		t.Fatal(err)
	}
	if client.transactionInput == nil || len(client.transactionInput.TransactItems) != 2 {
		t.Fatalf("publication and ID lookup must be atomic: %+v", client.transactionInput)
	}
	in := client.transactionInput.TransactItems[0].Put
	if in == nil || aws.ToString(in.TableName) != d.Table || aws.ToString(in.ConditionExpression) != "attribute_not_exists(pk)" {
		t.Fatalf("incorrect conditional write: %+v", in)
	}
	if !reflect.DeepEqual(in.Item["pk"], str(featuredModPartition)) || !reflect.DeepEqual(in.Item["sk"], str("2026-10-07T22:04:05.120000000Z#"+mod.ID)) {
		t.Fatalf("incorrect publication key: %+v", in.Item)
	}
	if _, exists := in.Item["downloadAvailable"]; exists {
		t.Fatal("stored download availability can become stale after release")
	}
	if _, exists := in.Item["downloadUrl"]; exists {
		t.Fatal("derived download URLs must not be persisted")
	}
	lookup := client.transactionInput.TransactItems[1].Put
	if lookup == nil || aws.ToString(lookup.TableName) != d.Table || aws.ToString(lookup.ConditionExpression) != "attribute_not_exists(pk)" || len(lookup.Item) != 3 ||
		!reflect.DeepEqual(lookup.Item["pk"], str("featured-mod#"+mod.ID)) || !reflect.DeepEqual(lookup.Item["sk"], str("metadata")) || !reflect.DeepEqual(lookup.Item["entrySK"], in.Item["sk"]) {
		t.Fatalf("ID lookup must reference canonical metadata without duplicating it: %+v", lookup)
	}
	client.queryRows = []map[string]types.AttributeValue{in.Item}
	got, err := d.LatestFeaturedMod(context.Background())
	mod.DownloadAvailable = false
	mod.DownloadURL = ""
	if err != nil || !reflect.DeepEqual(got, mod) {
		t.Fatalf("metadata/image-key round trip: %+v %v; want %+v", got, err, mod)
	}
	query := client.queryInput
	if query == nil || aws.ToString(query.TableName) != d.Table || aws.ToString(query.KeyConditionExpression) != "pk = :kind" ||
		!reflect.DeepEqual(query.ExpressionAttributeValues[":kind"], str(featuredModPartition)) ||
		query.Limit == nil || *query.Limit != 1 || query.ScanIndexForward == nil || *query.ScanIndexForward ||
		query.ConsistentRead == nil || !*query.ConsistentRead || query.FilterExpression != nil {
		t.Fatalf("latest publication query must include unreleased mods: %+v", query)
	}
}

func TestFeaturedModCreationTimeSortOrder(t *testing.T) {
	var previous string
	for _, createdAt := range []string{
		"2026-10-08T00:00:00Z",
		"2026-10-07T17:00:00.001-07:00",
		"2026-10-08T00:00:00.001000001Z",
		"2026-10-08T00:00:00.9Z",
		"2026-10-08T00:00:01Z",
	} {
		mod := sampleFeaturedMod()
		mod.CreatedAt = createdAt
		row, err := featuredModRecord(mod)
		if err != nil {
			t.Fatal(err)
		}
		if previous != "" && row.SK <= previous {
			t.Fatalf("publication at %s sorts before previous instant: %s <= %s", createdAt, row.SK, previous)
		}
		previous = row.SK
	}
}

func TestFeaturedModStoreErrors(t *testing.T) {
	ctx := context.Background()
	databaseError := errors.New("database unavailable")
	for _, test := range []struct {
		name string
		err  error
		want error
	}{
		{"duplicate publication", &types.TransactionCanceledException{CancellationReasons: []types.CancellationReason{{Code: aws.String("ConditionalCheckFailed")}}}, catalog.ErrConflict},
		{"concurrent publication", &types.TransactionConflictException{}, catalog.ErrConflict},
		{"database error", databaseError, databaseError},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &featuredModClient{transactionError: test.err}
			if err := (Dynamo{Client: client, Table: "test"}).SaveFeaturedMod(ctx, sampleFeaturedMod()); !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
		})
	}
	for _, change := range []func(*catalog.FeaturedMod){
		func(mod *catalog.FeaturedMod) { mod.CreatedAt = "invalid" },
		func(mod *catalog.FeaturedMod) { mod.ID = "invalid" },
	} {
		mod := sampleFeaturedMod()
		change(&mod)
		client := &featuredModClient{}
		if err := (Dynamo{Client: client, Table: "test"}).SaveFeaturedMod(ctx, mod); err == nil || client.transactionInput != nil {
			t.Fatalf("invalid publication reached Dynamo: %+v %v", mod, err)
		}
	}
	client := &featuredModClient{}
	d := Dynamo{Client: client, Table: "test"}
	if _, err := d.LatestFeaturedMod(ctx); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("empty partition: %v", err)
	}
	client.queryError = databaseError
	if _, err := d.LatestFeaturedMod(ctx); !errors.Is(err, databaseError) {
		t.Fatalf("query error: %v", err)
	}
	client.queryError = nil
	client.queryRows = []map[string]types.AttributeValue{{"id": str("invalid")}}
	if _, err := d.LatestFeaturedMod(ctx); err == nil {
		t.Fatal("accepted malformed stored publication")
	}
}

func TestGetFeaturedModByPublicationID(t *testing.T) {
	for _, releaseTime := range []string{"2099-01-01T00:00:00Z", "2000-01-01T00:00:00Z"} {
		mod := sampleFeaturedMod()
		mod.ReleaseTime = releaseTime
		row, err := featuredModRecord(mod)
		if err != nil {
			t.Fatal(err)
		}
		attributes, err := attributevalue.MarshalMap(row)
		if err != nil {
			t.Fatal(err)
		}
		client := &featuredModClient{getRows: []map[string]types.AttributeValue{{"entrySK": str(row.SK)}, attributes}}
		d := Dynamo{Client: client, Table: "test"}
		got, err := d.GetFeaturedMod(context.Background(), mod.ID)
		if err != nil || !reflect.DeepEqual(got, mod) {
			t.Fatalf("release %s: %+v %v", releaseTime, got, err)
		}
		if len(client.getInputs) != 2 || !reflect.DeepEqual(client.getInputs[0].Key, key("featured-mod#"+mod.ID, "metadata")) ||
			!reflect.DeepEqual(client.getInputs[1].Key, key(featuredModPartition, row.SK)) {
			t.Fatalf("incorrect ID/canonical lookup: %+v", client.getInputs)
		}
		for _, in := range client.getInputs {
			if aws.ToString(in.TableName) != d.Table || in.ConsistentRead == nil || !*in.ConsistentRead {
				t.Fatalf("lookup must read strongly consistently: %+v", in)
			}
		}
	}
}

func TestGetFeaturedModLookupErrors(t *testing.T) {
	mod := sampleFeaturedMod()
	other := mod
	other.ID = strings.Repeat("f", 32)
	otherRow, err := featuredModRecord(other)
	if err != nil {
		t.Fatal(err)
	}
	otherAttributes, err := attributevalue.MarshalMap(otherRow)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		rows []map[string]types.AttributeValue
		want error
	}{
		{"missing ID", nil, catalog.ErrNotFound},
		{"missing canonical row", []map[string]types.AttributeValue{{"entrySK": str("entry")}}, catalog.ErrNotFound},
		{"invalid lookup", []map[string]types.AttributeValue{{"entrySK": num(1)}}, nil},
		{"invalid canonical row", []map[string]types.AttributeValue{{"entrySK": str("entry")}, {"id": str("invalid")}}, nil},
		{"lookup references another ID", []map[string]types.AttributeValue{{"entrySK": str(otherRow.SK)}, otherAttributes}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &featuredModClient{getRows: test.rows}
			_, err := (Dynamo{Client: client, Table: "test"}).GetFeaturedMod(context.Background(), mod.ID)
			if err == nil || test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
		})
	}
	client := &featuredModClient{}
	d := Dynamo{Client: client, Table: "test"}
	if _, err := d.GetFeaturedMod(context.Background(), "invalid"); !errors.Is(err, catalog.ErrNotFound) || len(client.getInputs) != 0 {
		t.Fatalf("invalid ID reached Dynamo: %v", err)
	}
	databaseError := errors.New("database unavailable")
	client.getError = databaseError
	if _, err := d.GetFeaturedMod(context.Background(), mod.ID); !errors.Is(err, databaseError) {
		t.Fatalf("lookup error: %v", err)
	}
}

func TestDynamoLatestFeaturedModIncludesUpcomingRelease(t *testing.T) {
	d := integrationStore(t)
	ctx := context.Background()
	if _, err := d.LatestFeaturedMod(ctx); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("empty partition: %v", err)
	}
	older := sampleFeaturedMod()
	older.CreatedAt = "2026-10-08T00:00:00Z"
	older.ReleaseTime = "2100-01-01T00:00:00Z"
	newer := sampleFeaturedMod()
	newer.ID = strings.Repeat("f", 32)
	newer.CreatedAt = "2026-10-08T00:00:00.001Z"
	newer.ReleaseTime = "2099-01-01T00:00:00Z"
	newer.Title = "New publication, earlier upcoming release"
	for _, mod := range []catalog.FeaturedMod{newer, older} {
		if err := d.SaveFeaturedMod(ctx, mod); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.SaveFeaturedMod(ctx, newer); !errors.Is(err, catalog.ErrConflict) {
		t.Fatalf("duplicate publication: %v", err)
	}
	duplicateID := newer
	duplicateID.CreatedAt = "2026-10-08T00:00:00.002Z"
	if err := d.SaveFeaturedMod(ctx, duplicateID); !errors.Is(err, catalog.ErrConflict) {
		t.Fatalf("duplicate ID with another publication time: %v", err)
	}
	got, err := d.LatestFeaturedMod(ctx)
	if err != nil || !reflect.DeepEqual(got, newer) {
		t.Fatalf("latest publication must ignore release order: %+v %v", got, err)
	}
	for _, mod := range []catalog.FeaturedMod{older, newer} {
		got, err := d.GetFeaturedMod(ctx, mod.ID)
		if err != nil || !reflect.DeepEqual(got, mod) {
			t.Fatalf("lookup publication %s: %+v %v", mod.ID, got, err)
		}
	}
	// Independent catalog partitions must not leak into the featured query.
	createItem(t, d, "options")
	got, err = d.LatestFeaturedMod(ctx)
	if err != nil || got.ID != newer.ID {
		t.Fatalf("catalog data changed latest featured mod: %+v %v", got, err)
	}
	row, err := featuredModRecord(newer)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := d.Client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(d.Table), Key: key(featuredModPartition, row.SK), ConsistentRead: aws.Bool(true)})
	if err != nil {
		t.Fatal(err)
	}
	var persisted featuredModRow
	if err := attributevalue.UnmarshalMap(stored.Item, &persisted); err != nil || persisted.Image != newer.Image || persisted.Patch != newer.Patch {
		t.Fatalf("image/PPF keys were not persisted: %+v %v", persisted, err)
	}
}
