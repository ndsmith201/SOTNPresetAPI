package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"sotnpresetapi/internal/catalog"
)

type Client interface {
	PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	TransactWriteItems(context.Context, *dynamodb.TransactWriteItemsInput, ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error)
}

type Dynamo struct {
	Client Client
	Table  string
}

func str(s string) types.AttributeValue { return &types.AttributeValueMemberS{Value: s} }
func num(n int) types.AttributeValue    { return &types.AttributeValueMemberN{Value: strconv.Itoa(n)} }
func key(pk, sk string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"pk": str(pk), "sk": str(sk)}
}

func (d Dynamo) Create(ctx context.Context, item catalog.Item) error {
	row, err := attributevalue.MarshalMap(item)
	if err != nil {
		return err
	}
	row["data"] = str(string(item.Data))
	_, err = d.Client.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(d.Table), Item: row, ConditionExpression: aws.String("attribute_not_exists(pk)")})
	var conditional *types.ConditionalCheckFailedException
	if errors.As(err, &conditional) {
		return catalog.ErrConflict
	}
	return err
}

func decode(row map[string]types.AttributeValue) (catalog.Item, error) {
	var item catalog.Item
	if err := attributevalue.UnmarshalMap(row, &item); err != nil {
		return item, err
	}
	payload, ok := row["data"].(*types.AttributeValueMemberS)
	if !ok || !json.Valid([]byte(payload.Value)) {
		return item, errors.New("invalid stored JSON")
	}
	item.Data = json.RawMessage(payload.Value)
	return item, nil
}

func (d Dynamo) Get(ctx context.Context, kind, id string) (catalog.Item, error) {
	out, err := d.Client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(d.Table), Key: key(kind, id), ConsistentRead: aws.Bool(true)})
	if err != nil {
		return catalog.Item{}, err
	}
	if len(out.Item) == 0 {
		return catalog.Item{}, catalog.ErrNotFound
	}
	return decode(out.Item)
}

func (d Dynamo) List(ctx context.Context, kind string, limit int, after string) (catalog.Page, error) {
	in := &dynamodb.QueryInput{TableName: aws.String(d.Table), KeyConditionExpression: aws.String("pk = :kind"), ExpressionAttributeValues: map[string]types.AttributeValue{":kind": str(kind)}, Limit: aws.Int32(int32(limit)), ConsistentRead: aws.Bool(true)}
	if after != "" {
		in.ExclusiveStartKey = key(kind, after)
	}
	out, err := d.Client.Query(ctx, in)
	if err != nil {
		return catalog.Page{}, err
	}
	page := catalog.Page{Items: []catalog.Item{}}
	for _, row := range out.Items {
		item, err := decode(row)
		if err != nil {
			return catalog.Page{}, err
		}
		page.Items = append(page.Items, item)
	}
	if sk, ok := out.LastEvaluatedKey["sk"].(*types.AttributeValueMemberS); ok {
		page.NextCursor = sk.Value
	}
	return page, nil
}

func signCount(value, sign int) int {
	if value == sign {
		return 1
	}
	return 0
}

// Vote updates the per-user vote and aggregate counters in one transaction.
// Comparing the previous vote prevents retries and concurrent requests from
// counting the same user's vote twice. Counters use atomic deltas across users.
func (d Dynamo) Vote(ctx context.Context, kind, id, user string, value int) (catalog.Item, error) {
	if value < -1 || value > 1 || user == "" {
		return catalog.Item{}, errors.New("invalid vote")
	}
	voteKey := key("vote#"+kind+"#"+id, user)
	for attempt := 0; attempt < 6; attempt++ {
		if _, err := d.Get(ctx, kind, id); err != nil {
			return catalog.Item{}, err
		}
		previous, err := d.Client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(d.Table), Key: voteKey, ConsistentRead: aws.Bool(true)})
		if err != nil {
			return catalog.Item{}, err
		}
		old := 0
		exists := len(previous.Item) > 0
		if exists {
			stored, ok := previous.Item["value"].(*types.AttributeValueMemberN)
			if !ok {
				return catalog.Item{}, errors.New("invalid stored vote")
			}
			old, err = strconv.Atoi(stored.Value)
			if err != nil || (old != -1 && old != 1) {
				return catalog.Item{}, errors.New("invalid stored vote")
			}
		}
		if old == value {
			return d.Get(ctx, kind, id)
		}
		condition := "attribute_not_exists(pk)"
		var values map[string]types.AttributeValue
		if exists {
			condition = "#v = :old"
			values = map[string]types.AttributeValue{":old": num(old)}
		}
		var names map[string]string
		if exists {
			names = map[string]string{"#v": "value"}
		}
		var voteWrite types.TransactWriteItem
		if value == 0 {
			voteWrite.Delete = &types.Delete{TableName: aws.String(d.Table), Key: voteKey, ConditionExpression: aws.String(condition), ExpressionAttributeValues: values, ExpressionAttributeNames: names}
		} else {
			row := key("vote#"+kind+"#"+id, user)
			row["value"] = num(value)
			voteWrite.Put = &types.Put{TableName: aws.String(d.Table), Item: row, ConditionExpression: aws.String(condition), ExpressionAttributeValues: values, ExpressionAttributeNames: names}
		}
		token := make([]byte, 16)
		if _, err := rand.Read(token); err != nil {
			return catalog.Item{}, err
		}
		_, err = d.Client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
			ClientRequestToken: aws.String(hex.EncodeToString(token)),
			TransactItems: []types.TransactWriteItem{
				{Update: &types.Update{TableName: aws.String(d.Table), Key: key(kind, id), ConditionExpression: aws.String("attribute_exists(pk)"),
					UpdateExpression:          aws.String("ADD upvotes :up, downvotes :down, score :score"),
					ExpressionAttributeValues: map[string]types.AttributeValue{":up": num(signCount(value, 1) - signCount(old, 1)), ":down": num(signCount(value, -1) - signCount(old, -1)), ":score": num(value - old)}}},
				voteWrite,
			},
		})
		if err == nil {
			return d.Get(ctx, kind, id)
		}
		if !retryable(err) {
			return catalog.Item{}, err
		}
		timer := time.NewTimer(time.Duration(10*(1<<attempt)) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return catalog.Item{}, ctx.Err()
		case <-timer.C:
		}
	}
	return catalog.Item{}, catalog.ErrConflict
}

func retryable(err error) bool {
	var canceled *types.TransactionCanceledException
	if errors.As(err, &canceled) {
		found := false
		for _, r := range canceled.CancellationReasons {
			code := aws.ToString(r.Code)
			switch code {
			case "ConditionalCheckFailed", "TransactionConflict":
				found = true
			case "None", "":
			default:
				return false
			}
		}
		return found
	}
	var conflict *types.TransactionConflictException
	return errors.As(err, &conflict)
}
