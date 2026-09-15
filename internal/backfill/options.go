package backfill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"sotnpresetapi/internal/catalog"
)

type Client interface {
	Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	UpdateItem(context.Context, *dynamodb.UpdateItemInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
}

type Summary struct {
	Scanned, AlreadyNamed, Resolved, Updated, Unresolved, Changed, Failed int
}

type lookup struct {
	name string
	err  error
}

func stringValue(value types.AttributeValue) string {
	if value, ok := value.(*types.AttributeValueMemberS); ok {
		return value.Value
	}
	return ""
}
func str(value string) types.AttributeValue { return &types.AttributeValueMemberS{Value: value} }

// Options is dry-run unless apply is true. It only adds missing display names;
// conditional updates protect deleted rows, changed owners, and concurrent backfills.
func Options(ctx context.Context, client Client, resolver catalog.UsernameResolver, table string, apply bool) (Summary, error) {
	var summary Summary
	if strings.TrimSpace(table) == "" {
		return summary, errors.New("table is required")
	}
	in := &dynamodb.QueryInput{
		TableName: aws.String(table), ConsistentRead: aws.Bool(true), Limit: aws.Int32(100),
		KeyConditionExpression:    aws.String("#pk = :kind"),
		ExpressionAttributeNames:  map[string]string{"#pk": "pk", "#sk": "sk", "#creator": "createdBy", "#username": "createdByUsername"},
		ExpressionAttributeValues: map[string]types.AttributeValue{":kind": str("options")},
		ProjectionExpression:      aws.String("#pk, #sk, #creator, #username"),
	}
	cache := make(map[string]lookup)
	var firstLookupError error
	seenCursors := make(map[string]bool)
	for {
		page, err := client.Query(ctx, in)
		if err != nil {
			return summary, fmt.Errorf("query options: %w", err)
		}
		for _, row := range page.Items {
			if err := ctx.Err(); err != nil {
				return summary, err
			}
			summary.Scanned++
			if value, exists := row["createdByUsername"]; exists {
				name, isText := value.(*types.AttributeValueMemberS)
				if !isText || name.Value != "" {
					summary.AlreadyNamed++
					continue
				}
			}
			id, subject := stringValue(row["sk"]), stringValue(row["createdBy"])
			if stringValue(row["pk"]) != "options" || !catalog.ValidID(id) || subject == "" {
				summary.Unresolved++
				continue
			}
			result, ok := cache[subject]
			if !ok {
				lookupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
				result.name, result.err = resolver.Username(lookupCtx, subject)
				cancel()
				result.name = strings.TrimSpace(result.name)
				cache[subject] = result
			}
			if result.err != nil {
				if firstLookupError == nil {
					firstLookupError = result.err
				}
				summary.Failed++
				continue
			}
			if result.name == "" {
				summary.Unresolved++
				continue
			}
			summary.Resolved++
			if !apply {
				continue
			}
			_, err := client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
				TableName: aws.String(table), Key: map[string]types.AttributeValue{"pk": str("options"), "sk": str(id)},
				UpdateExpression:          aws.String("SET #username = :username"),
				ConditionExpression:       aws.String("attribute_exists(pk) AND #creator = :creator AND (attribute_not_exists(#username) OR #username = :empty)"),
				ExpressionAttributeNames:  map[string]string{"#creator": "createdBy", "#username": "createdByUsername"},
				ExpressionAttributeValues: map[string]types.AttributeValue{":creator": str(subject), ":username": str(result.name), ":empty": str("")},
			})
			var conditional *types.ConditionalCheckFailedException
			switch {
			case errors.As(err, &conditional):
				summary.Changed++
			case err != nil:
				return summary, fmt.Errorf("update option %s: %w", id, err)
			default:
				summary.Updated++
			}
		}
		if len(page.LastEvaluatedKey) == 0 {
			break
		}
		cursor, err := json.Marshal(page.LastEvaluatedKey)
		if err != nil {
			return summary, err
		}
		if seenCursors[string(cursor)] {
			return summary, errors.New("DynamoDB returned a repeated pagination key")
		}
		seenCursors[string(cursor)] = true
		in.ExclusiveStartKey = page.LastEvaluatedKey
	}
	if summary.Failed > 0 {
		return summary, fmt.Errorf("Cognito lookup failed for %d options; rerun to retry missing names: %w", summary.Failed, firstLookupError)
	}
	return summary, nil
}
