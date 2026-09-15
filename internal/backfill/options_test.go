package backfill

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type fakeDB struct {
	rows             []map[string]types.AttributeValue
	queries, updates int
	conflict         bool
}

func (d *fakeDB) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	d.queries++
	if aws.ToString(in.KeyConditionExpression) != "#pk = :kind" || stringValue(in.ExpressionAttributeValues[":kind"]) != "options" {
		return nil, errors.New("must only query options")
	}
	if len(in.ExclusiveStartKey) == 0 {
		return &dynamodb.QueryOutput{Items: d.rows[:1], LastEvaluatedKey: map[string]types.AttributeValue{"pk": str("options"), "sk": d.rows[0]["sk"]}}, nil
	}
	return &dynamodb.QueryOutput{Items: d.rows[1:]}, nil
}
func (d *fakeDB) UpdateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	d.updates++
	if aws.ToString(in.UpdateExpression) != "SET #username = :username" || len(in.ExpressionAttributeNames) != 2 || !strings.Contains(aws.ToString(in.ConditionExpression), "attribute_exists(pk)") || !strings.Contains(aws.ToString(in.ConditionExpression), "#creator = :creator") || !strings.Contains(aws.ToString(in.ConditionExpression), "attribute_not_exists(#username)") {
		return nil, errors.New("unsafe backfill write")
	}
	if d.conflict {
		return nil, &types.ConditionalCheckFailedException{}
	}
	for _, row := range d.rows {
		if stringValue(row["sk"]) != stringValue(in.Key["sk"]) {
			continue
		}
		if stringValue(row["createdBy"]) != stringValue(in.ExpressionAttributeValues[":creator"]) || stringValue(row["createdByUsername"]) != "" {
			return nil, &types.ConditionalCheckFailedException{}
		}
		row["createdByUsername"] = in.ExpressionAttributeValues[":username"]
		return &dynamodb.UpdateItemOutput{}, nil
	}
	return nil, errors.New("missing row")
}

type fakeResolver struct {
	calls map[string]int
	fail  bool
}

func (r *fakeResolver) Username(_ context.Context, subject string) (string, error) {
	r.calls[subject]++
	if r.fail {
		return "", errors.New("Cognito unavailable")
	}
	if subject == "missing" {
		return "", nil
	}
	return "runner", nil
}
func row(id, subject string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"pk": str("options"), "sk": str(strings.Repeat(id, 32)), "createdBy": str(subject), "data": str(`{"value":"unchanged"}`), "upvotes": &types.AttributeValueMemberN{Value: "12"}}
}

func TestBackfillPreviewApplyAndRerun(t *testing.T) {
	named := row("c", "named")
	named["createdByUsername"] = str("existing name")
	db := &fakeDB{rows: []map[string]types.AttributeValue{row("a", "creator"), row("b", "creator"), named, row("d", "missing")}}
	for _, apply := range []bool{false, true, true} {
		resolver := &fakeResolver{calls: map[string]int{}}
		before := db.updates
		result, err := Options(context.Background(), db, resolver, "catalog", apply)
		if err != nil || result.Scanned != 4 || result.Unresolved != 1 {
			t.Fatal(result, err)
		}
		if resolver.calls["creator"] > 1 || resolver.calls["named"] != 0 {
			t.Fatal("unnecessary lookup", resolver.calls)
		}
		if !apply && (db.updates != 0 || stringValue(db.rows[0]["createdByUsername"]) != "") {
			t.Fatal("preview wrote to DB")
		}
		if apply && before == 0 && (result.Updated != 2 || result.AlreadyNamed != 1) {
			t.Fatal(result)
		}
		if apply && before > 0 && (result.Updated != 0 || result.AlreadyNamed != 3 || resolver.calls["creator"] != 0) {
			t.Fatal("rerun not idempotent", result, resolver.calls)
		}
	}
	if stringValue(named["createdByUsername"]) != "existing name" || stringValue(db.rows[0]["createdBy"]) != "creator" || stringValue(db.rows[0]["data"]) != `{"value":"unchanged"}` || db.rows[0]["upvotes"].(*types.AttributeValueMemberN).Value != "12" {
		t.Fatal("backfill overwrote unrelated data")
	}
}

func TestBackfillFailuresAndConcurrentChanges(t *testing.T) {
	for _, failedLookup := range []bool{false, true} {
		db := &fakeDB{rows: []map[string]types.AttributeValue{row("a", "creator"), row("b", "creator")}, conflict: true}
		resolver := &fakeResolver{calls: map[string]int{}, fail: failedLookup}
		result, err := Options(context.Background(), db, resolver, "catalog", true)
		if resolver.calls["creator"] != 1 || result.Updated != 0 {
			t.Fatal(result, resolver.calls)
		}
		if failedLookup && (err == nil || result.Failed != 2 || db.updates != 0) {
			t.Fatal(result, err)
		}
		if !failedLookup && (err != nil || result.Changed != 2) {
			t.Fatal(result, err)
		}
	}
}
