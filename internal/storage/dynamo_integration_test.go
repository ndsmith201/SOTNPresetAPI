package storage

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"sotnpresetapi/internal/catalog"
)

func integrationStore(t *testing.T) Dynamo {
	t.Helper()
	endpoint := os.Getenv("DYNAMODB_ENDPOINT")
	if endpoint == "" {
		t.Skip("set DYNAMODB_ENDPOINT to run against DynamoDB Local")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") {
		t.Fatal("tests require a local HTTP endpoint")
	}
	client := dynamodb.NewFromConfig(aws.Config{Region: "us-west-2", Credentials: credentials.NewStaticCredentialsProvider("local", "local", "")}, func(o *dynamodb.Options) { o.BaseEndpoint = aws.String(endpoint) })
	table := fmt.Sprintf("sotn-test-%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = client.CreateTable(ctx, &dynamodb.CreateTableInput{TableName: aws.String(table), BillingMode: types.BillingModePayPerRequest,
		AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("pk"), AttributeType: types.ScalarAttributeTypeS}, {AttributeName: aws.String("sk"), AttributeType: types.ScalarAttributeTypeS}},
		KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String("pk"), KeyType: types.KeyTypeHash}, {AttributeName: aws.String("sk"), KeyType: types.KeyTypeRange}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := client.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(table)}); err != nil {
			t.Error(err)
		}
	})
	return Dynamo{Client: client, Table: table}
}

func createItem(t *testing.T, d Dynamo, kind string) catalog.Item {
	t.Helper()
	body := `{"metadata":{"id":"test","name":"Test"},"custom":9007199254740993}`
	if kind == "options" {
		body = `{"comment":"Test","category":"gameplay","type":"word","value":"1"}`
	}
	item, err := catalog.NewItem(kind, "creator", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Create(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	return item
}

func TestDynamoPersistencePaginationAndVotes(t *testing.T) {
	d := integrationStore(t)
	ctx := context.Background()
	for _, kind := range []string{"options", "presets"} {
		t.Run(kind, func(t *testing.T) {
			item := createItem(t, d, kind)
			if err := d.Create(ctx, item); !errors.Is(err, catalog.ErrConflict) {
				t.Fatalf("duplicate create: %v", err)
			}
			stored, err := d.Get(ctx, kind, item.ID)
			if err != nil || string(stored.Data) != string(item.Data) {
				t.Fatalf("round trip: %+v %v", stored, err)
			}
			for _, step := range []struct {
				user            string
				value           int
				up, down, score int64
			}{
				{"alice", 1, 1, 0, 1}, {"alice", 1, 1, 0, 1}, {"bob", -1, 1, 1, 0}, {"alice", -1, 0, 2, -2},
				{"alice", 0, 0, 1, -1}, {"alice", 0, 0, 1, -1}, {"bob", 1, 1, 0, 1}, {"bob", 0, 0, 0, 0},
			} {
				got, err := d.Vote(ctx, kind, item.ID, step.user, step.value)
				if err != nil {
					t.Fatal(err)
				}
				if got.Upvotes != step.up || got.Downvotes != step.down || got.Score != step.score {
					t.Fatalf("step %+v: %+v", step, got)
				}
			}
			for i := 0; i < 4; i++ {
				createItem(t, d, kind)
			}
			seen := map[string]bool{}
			after := ""
			for pages := 0; ; pages++ {
				if pages > 5 {
					t.Fatal("pagination did not terminate")
				}
				page, err := d.List(ctx, kind, 2, after)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range page.Items {
					if seen[entry.ID] || entry.Kind != kind {
						t.Fatal("duplicate or wrong kind")
					}
					seen[entry.ID] = true
				}
				after = page.NextCursor
				if after == "" {
					break
				}
			}
			if len(seen) != 5 {
				t.Fatalf("listed %d items", len(seen))
			}
		})
	}
	if _, err := d.Vote(ctx, "options", strings.Repeat("0", 32), "alice", 1); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatal(err)
	}
	missingVote, err := d.Client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(d.Table), Key: key("vote#options#"+strings.Repeat("0", 32), "alice")})
	if err != nil || len(missingVote.Item) != 0 {
		t.Fatal("orphan vote", err)
	}
}

func TestDynamoConcurrentVoting(t *testing.T) {
	d := integrationStore(t)
	item := createItem(t, d, "options")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Duplicate simultaneous requests from one user still count once.
	run := func(users []string, value int) {
		var wg sync.WaitGroup
		for _, user := range users {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 5; i++ {
					_, err := d.Vote(ctx, "options", item.ID, user, value)
					if errors.Is(err, catalog.ErrConflict) {
						continue
					}
					if err != nil {
						t.Error(err)
					}
					return
				}
				t.Error("retry budget exhausted")
			}()
		}
		wg.Wait()
	}
	run([]string{"alice", "alice", "alice", "alice", "alice", "alice"}, 1)
	got, err := d.Get(ctx, "options", item.ID)
	if err != nil || got.Upvotes != 1 || got.Score != 1 {
		t.Fatalf("duplicate votes: %+v %v", got, err)
	}
	run([]string{"bob", "carol", "dave", "erin", "frank", "grace"}, -1)
	got, err = d.Get(ctx, "options", item.ID)
	if err != nil || got.Upvotes != 1 || got.Downvotes != 6 || got.Score != -5 {
		t.Fatalf("concurrent voters: %+v %v", got, err)
	}
	run([]string{"alice", "bob", "carol", "dave", "erin", "frank", "grace"}, 0)
	got, err = d.Get(ctx, "options", item.ID)
	if err != nil || got.Upvotes != 0 || got.Downvotes != 0 || got.Score != 0 {
		t.Fatalf("removal: %+v %v", got, err)
	}
}

func TestRetryClassification(t *testing.T) {
	for _, code := range []string{"ConditionalCheckFailed", "TransactionConflict", "ValidationError", "ProvisionedThroughputExceeded"} {
		err := &types.TransactionCanceledException{CancellationReasons: []types.CancellationReason{{Code: aws.String(code)}}}
		want := code == "ConditionalCheckFailed" || code == "TransactionConflict"
		if retryable(err) != want {
			t.Fatal(code)
		}
	}
	if retryable(errors.New("unknown")) {
		t.Fatal("unexpected retry")
	}
}
