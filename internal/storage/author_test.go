package storage

import (
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"sotnpresetapi/internal/catalog"
)

func TestAuthorUsernameDynamoRoundTrip(t *testing.T) {
	item := catalog.Item{ID: "0123456789abcdef0123456789abcdef", Kind: "options", CreatedBy: "creator-sub", CreatedByUsername: "runner", Data: json.RawMessage(`{"comment":"Test"}`)}
	row, err := attributevalue.MarshalMap(item)
	if err != nil {
		t.Fatal(err)
	}
	row["data"] = str(string(item.Data))
	decoded, err := decode(row)
	if err != nil || decoded.CreatedByUsername != "runner" || decoded.CreatedBy != "creator-sub" {
		t.Fatal(decoded, err)
	}
	item.CreatedByUsername = ""
	row, err = attributevalue.MarshalMap(item)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := row["createdByUsername"]; exists {
		t.Fatal("missing usernames must remain eligible for backfill")
	}
}
