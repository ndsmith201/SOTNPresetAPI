package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"sotnpresetapi/internal/catalog"
)

const featuredModPartition = "featured-mods"
const featuredModSortTimeLayout = "2006-01-02T15:04:05.000000000Z"

// Image holds the private object key. The image adapter supplies a signed URL
// on reads, while download availability is derived from the release time.
type featuredModRow struct {
	PK          string `dynamodbav:"pk"`
	SK          string `dynamodbav:"sk"`
	ID          string `dynamodbav:"id"`
	Title       string `dynamodbav:"title"`
	Description string `dynamodbav:"description"`
	Image       string `dynamodbav:"image"`
	Patch       string `dynamodbav:"patch"`
	ReleaseTime string `dynamodbav:"releaseTime"`
	CreatedBy   string `dynamodbav:"createdBy"`
	CreatedAt   string `dynamodbav:"createdAt"`
}

func featuredModRecord(mod catalog.FeaturedMod) (featuredModRow, error) {
	createdAt, err := time.Parse(time.RFC3339Nano, mod.CreatedAt)
	if err != nil {
		return featuredModRow{}, fmt.Errorf("invalid featured mod creation time: %w", err)
	}
	if !catalog.ValidID(mod.ID) {
		return featuredModRow{}, errors.New("invalid featured mod ID")
	}
	return featuredModRow{
		PK: featuredModPartition,
		SK: createdAt.UTC().Format(featuredModSortTimeLayout) + "#" + mod.ID,
		ID: mod.ID, Title: mod.Title, Description: mod.Description, Image: mod.Image, Patch: mod.Patch,
		ReleaseTime: mod.ReleaseTime, CreatedBy: mod.CreatedBy, CreatedAt: mod.CreatedAt,
	}, nil
}

// SaveFeaturedMod appends one publication without replacing an earlier entry.
func (d Dynamo) SaveFeaturedMod(ctx context.Context, mod catalog.FeaturedMod) error {
	row, err := featuredModRecord(mod)
	if err != nil {
		return err
	}
	attributes, err := attributevalue.MarshalMap(row)
	if err != nil {
		return err
	}
	lookup := key("featured-mod#"+mod.ID, "metadata")
	lookup["entrySK"] = str(row.SK)
	_, err = d.Client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{Put: &types.Put{TableName: aws.String(d.Table), Item: attributes, ConditionExpression: aws.String("attribute_not_exists(pk)")}},
			{Put: &types.Put{TableName: aws.String(d.Table), Item: lookup, ConditionExpression: aws.String("attribute_not_exists(pk)")}},
		},
	})
	if retryable(err) {
		return catalog.ErrConflict
	}
	return err
}

// LatestFeaturedMod returns the latest publication, including upcoming releases.
func (d Dynamo) LatestFeaturedMod(ctx context.Context) (catalog.FeaturedMod, error) {
	out, err := d.Client.Query(ctx, &dynamodb.QueryInput{
		TableName: aws.String(d.Table), KeyConditionExpression: aws.String("pk = :kind"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":kind": str(featuredModPartition)},
		Limit:                     aws.Int32(1), ScanIndexForward: aws.Bool(false), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return catalog.FeaturedMod{}, err
	}
	if len(out.Items) == 0 {
		return catalog.FeaturedMod{}, catalog.ErrNotFound
	}
	return decodeFeaturedMod(out.Items[0])
}

// GetFeaturedMod follows the ID lookup to the same canonical publication row.
func (d Dynamo) GetFeaturedMod(ctx context.Context, id string) (catalog.FeaturedMod, error) {
	if !catalog.ValidID(id) {
		return catalog.FeaturedMod{}, catalog.ErrNotFound
	}
	lookup, err := d.Client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(d.Table), Key: key("featured-mod#"+id, "metadata"), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return catalog.FeaturedMod{}, err
	}
	if len(lookup.Item) == 0 {
		return catalog.FeaturedMod{}, catalog.ErrNotFound
	}
	entrySK, ok := lookup.Item["entrySK"].(*types.AttributeValueMemberS)
	if !ok || entrySK.Value == "" {
		return catalog.FeaturedMod{}, errors.New("invalid stored featured mod lookup")
	}
	out, err := d.Client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(d.Table), Key: key(featuredModPartition, entrySK.Value), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return catalog.FeaturedMod{}, err
	}
	if len(out.Item) == 0 {
		return catalog.FeaturedMod{}, catalog.ErrNotFound
	}
	mod, err := decodeFeaturedMod(out.Item)
	if err != nil {
		return catalog.FeaturedMod{}, err
	}
	if mod.ID != id {
		return catalog.FeaturedMod{}, errors.New("invalid stored featured mod lookup")
	}
	return mod, nil
}

func decodeFeaturedMod(attributes map[string]types.AttributeValue) (catalog.FeaturedMod, error) {
	var row featuredModRow
	if err := attributevalue.UnmarshalMap(attributes, &row); err != nil {
		return catalog.FeaturedMod{}, err
	}
	mod := catalog.FeaturedMod{
		ID: row.ID, Title: row.Title, Description: row.Description, Image: row.Image, Patch: row.Patch,
		ReleaseTime: row.ReleaseTime, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt,
	}
	canonical, err := featuredModRecord(mod)
	if err != nil {
		return catalog.FeaturedMod{}, fmt.Errorf("invalid stored featured mod: %w", err)
	}
	if row.PK != canonical.PK || row.SK != canonical.SK {
		return catalog.FeaturedMod{}, errors.New("invalid stored featured mod key")
	}
	return mod, nil
}
