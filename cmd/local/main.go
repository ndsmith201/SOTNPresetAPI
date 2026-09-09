// Local development only: no AWS account or authentication service required.
// The production Lambda entry point cannot enable this authentication bypass.
package main

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"sotnpresetapi/internal/catalog"
	"sotnpresetapi/internal/storage"
)

func main() {
	endpoint := os.Getenv("DYNAMODB_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:8000"
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") {
		log.Fatal("DYNAMODB_ENDPOINT must be a local HTTP endpoint")
	}
	client := dynamodb.NewFromConfig(aws.Config{Region: "us-west-2", Credentials: credentials.NewStaticCredentialsProvider("local", "local", "")}, func(o *dynamodb.Options) { o.BaseEndpoint = aws.String(endpoint) })
	const table = "sotn-catalog-local"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = client.CreateTable(ctx, &dynamodb.CreateTableInput{TableName: aws.String(table), BillingMode: types.BillingModePayPerRequest,
		AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("pk"), AttributeType: types.ScalarAttributeTypeS}, {AttributeName: aws.String("sk"), AttributeType: types.ScalarAttributeTypeS}},
		KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String("pk"), KeyType: types.KeyTypeHash}, {AttributeName: aws.String("sk"), KeyType: types.KeyTypeRange}}})
	var exists *types.ResourceInUseException
	if err != nil && !errors.As(err, &exists) {
		log.Fatal(err)
	}
	api := catalog.API{Store: storage.Dynamo{Client: client, Table: table}}
	server := &http.Server{Addr: "127.0.0.1:8080", ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, catalog.MaxBodyBytes))
			if err != nil {
				http.Error(w, "body exceeds 128 KiB", 413)
				return
			}
			query := map[string]string{}
			for k := range r.URL.Query() {
				query[k] = r.URL.Query().Get(k)
			}
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			res := api.Handle(ctx, catalog.Request{Method: r.Method, Path: r.URL.Path, ContentType: r.Header.Get("Content-Type"), Subject: r.Header.Get("X-Dev-User"), Query: query, Body: body})
			for k, v := range res.Headers {
				w.Header().Set(k, v)
			}
			w.WriteHeader(res.Status)
			_, _ = w.Write([]byte(res.Body))
		})}
	log.Print("LOCAL DEVELOPMENT ONLY: http://127.0.0.1:8080; X-Dev-User selects a test identity")
	log.Fatal(server.ListenAndServe())
}
