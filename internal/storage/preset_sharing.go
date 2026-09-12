package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"sotnpresetapi/internal/catalog"
)

// SavePreset reserves the name and writes its payload atomically. Updates only
// replace data, preserving catalog identity, creation metadata and live votes.
func (d Dynamo) SavePreset(ctx context.Context, submitted catalog.Item, previous *catalog.Item) (catalog.Item, error) {
	return d.saveNamedItem(ctx, submitted, previous, "presets", "preset-name", catalog.PresetName)
}

// SaveOption applies the same atomic name reservation and payload update to options.
func (d Dynamo) SaveOption(ctx context.Context, submitted catalog.Item, previous *catalog.Item) (catalog.Item, error) {
	if previous != nil && (submitted.CreatedBy == "" || previous.CreatedBy != submitted.CreatedBy) {
		return catalog.Item{}, catalog.ErrConflict
	}
	return d.saveNamedItem(ctx, submitted, previous, "options", "option-name", catalog.OptionName)
}

func (d Dynamo) saveNamedItem(ctx context.Context, submitted catalog.Item, previous *catalog.Item, kind, namePartition string, itemName func(json.RawMessage) string) (catalog.Item, error) {
	name := itemName(submitted.Data)
	if submitted.Kind != kind || name == "" {
		return catalog.Item{}, errors.New("invalid named item")
	}
	id := submitted.ID
	var change types.TransactWriteItem
	if previous == nil {
		row, err := attributevalue.MarshalMap(submitted)
		if err != nil {
			return catalog.Item{}, err
		}
		row["data"] = str(string(submitted.Data))
		change.Put = &types.Put{TableName: aws.String(d.Table), Item: row, ConditionExpression: aws.String("attribute_not_exists(pk)")}
	} else {
		if previous.Kind != kind || itemName(previous.Data) != name {
			return catalog.Item{}, catalog.ErrConflict
		}
		id = previous.ID
		change.Update = &types.Update{
			TableName: aws.String(d.Table), Key: key(kind, id),
			ConditionExpression:       aws.String("attribute_exists(pk) AND #data = :previous"),
			UpdateExpression:          aws.String("SET #data = :data"),
			ExpressionAttributeNames:  map[string]string{"#data": "data"},
			ExpressionAttributeValues: map[string]types.AttributeValue{":previous": str(string(previous.Data)), ":data": str(string(submitted.Data))},
		}
		if kind == "options" {
			change.Update.ConditionExpression = aws.String("attribute_exists(pk) AND #data = :previous AND createdBy = :author")
			change.Update.ExpressionAttributeValues[":author"] = str(submitted.CreatedBy)
		}
	}
	hash := sha256.Sum256([]byte(name))
	nameRow := key(namePartition, hex.EncodeToString(hash[:]))
	nameRow["itemId"] = str(id)
	_, err := d.Client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []types.TransactWriteItem{
		{Put: &types.Put{TableName: aws.String(d.Table), Item: nameRow,
			ConditionExpression:       aws.String("attribute_not_exists(pk) OR itemId = :id"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":id": str(id)}}},
		change,
	}})
	if retryable(err) {
		return catalog.Item{}, catalog.ErrConflict
	}
	if err != nil {
		return catalog.Item{}, err
	}
	return d.Get(ctx, kind, id)
}
