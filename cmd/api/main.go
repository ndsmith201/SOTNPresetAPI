package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"sotnpresetapi/internal/authors"
	"sotnpresetapi/internal/catalog"
	"sotnpresetapi/internal/storage"
	"sotnpresetapi/internal/transport"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	table := os.Getenv("TABLE_NAME")
	if table == "" {
		slog.Error("TABLE_NAME is required")
		os.Exit(1)
	}
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		slog.Error("load AWS config", "error", err)
		os.Exit(1)
	}
	bucket := os.Getenv("FEATURED_MOD_BUCKET")
	if bucket == "" {
		slog.Error("FEATURED_MOD_BUCKET is required")
		os.Exit(1)
	}
	api := catalog.API{Store: storage.FeaturedMods{
		Dynamo: storage.Dynamo{Client: dynamodb.NewFromConfig(cfg), Table: table},
		Assets: storage.S3Assets{Client: s3.NewFromConfig(cfg), Bucket: bucket},
	}}
	pool := os.Getenv("USER_POOL_ID")
	if pool == "" {
		slog.Error("USER_POOL_ID is required")
		os.Exit(1)
	}
	resolver := &authors.Cognito{Client: cognitoidentityprovider.NewFromConfig(cfg), PoolID: pool}
	lambda.Start(transport.LambdaHandler(api, resolver))
}
