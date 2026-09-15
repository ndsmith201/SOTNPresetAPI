package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"sotnpresetapi/internal/authors"
	"sotnpresetapi/internal/backfill"
)

func main() {
	table := flag.String("table", "", "DynamoDB catalog table (required)")
	pool := flag.String("user-pool", "", "Cognito user pool ID (required)")
	region := flag.String("region", "us-east-1", "AWS region")
	profile := flag.String("profile", "", "Optional AWS shared-config profile")
	apply := flag.Bool("apply", false, "Write missing usernames; default is a read-only preview")
	flag.Parse()
	if *table == "" || *pool == "" || flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	opts := []func(*config.LoadOptions) error{config.WithRegion(*region)}
	if *profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(*profile))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("Table: %s; pool: %s; region: %s; apply: %t\n", *table, *pool, *region, *apply)
	resolver := &authors.Cognito{Client: cognitoidentityprovider.NewFromConfig(cfg), PoolID: *pool}
	result, err := backfill.Options(ctx, dynamodb.NewFromConfig(cfg), resolver, *table, *apply)
	fmt.Printf("Scanned: %d; already named: %d; resolved: %d; updated: %d; unresolved: %d; changed concurrently: %d; failed: %d\n", result.Scanned, result.AlreadyNamed, result.Resolved, result.Updated, result.Unresolved, result.Changed, result.Failed)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if !*apply {
		fmt.Println("Preview only. Run with -apply to save the resolved usernames.")
	}
}
