package storage

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type featuredAssetRoundTripper func(*http.Request) (*http.Response, error)

func (f featuredAssetRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func offlineFeaturedS3Client(transport http.RoundTripper) *s3.Client {
	return s3.NewFromConfig(aws.Config{
		Region: "us-west-2", Credentials: credentials.NewStaticCredentialsProvider("offline-key", "offline-secret", ""),
		HTTPClient: &http.Client{Transport: transport}, RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
	}, func(o *s3.Options) {
		o.BaseEndpoint = aws.String("https://s3.invalid")
		o.UsePathStyle = true
		o.RetryMaxAttempts = 1
	})
}

func TestS3FeaturedAssetPresigningIsOfflineAndExpires(t *testing.T) {
	requests := 0
	client := offlineFeaturedS3Client(featuredAssetRoundTripper(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("presigning must not make HTTP requests")
	}))
	assets := S3Assets{Client: client, Bucket: "featured-test"}
	for _, key := range []string{"images/" + sampleFeaturedMod().ID + ".png", "patches/" + sampleFeaturedMod().ID + ".ppf"} {
		signed, err := assets.URL(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := url.Parse(signed)
		if err != nil || parsed.Host != "s3.invalid" || parsed.Path != "/featured-test/"+key || parsed.Scheme != "https" {
			t.Fatalf("incorrect signed object URL: %s %v", signed, err)
		}
		query := parsed.Query()
		if query.Get("X-Amz-Expires") != "900" || query.Get("X-Amz-Algorithm") != "AWS4-HMAC-SHA256" || query.Get("X-Amz-Signature") == "" ||
			!strings.Contains(query.Get("X-Amz-Credential"), "/us-west-2/s3/aws4_request") {
			t.Fatalf("URL must be signed for 15 minutes: %s", signed)
		}
	}
	if requests != 0 {
		t.Fatalf("presigning made %d network requests", requests)
	}
}

func TestS3FeaturedUploadsAndDeletion(t *testing.T) {
	var requests []*http.Request
	var bodies []string
	client := offlineFeaturedS3Client(featuredAssetRoundTripper(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r)
		var data []byte
		if r.Body != nil {
			var err error
			data, err = io.ReadAll(r.Body)
			if err != nil {
				return nil, err
			}
		}
		bodies = append(bodies, string(data))
		status := http.StatusOK
		if r.Method == http.MethodDelete {
			status = http.StatusNoContent
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	}))
	assets := S3Assets{Client: client, Bucket: "featured-test"}
	imageKey, patchKey := "images/"+sampleFeaturedMod().ID+".png", "patches/"+sampleFeaturedMod().ID+".ppf"
	if err := assets.Upload(context.Background(), imageKey, []byte("image-bytes"), "image/png"); err != nil {
		t.Fatal(err)
	}
	if err := assets.Upload(context.Background(), patchKey, []byte("PPF30-patch-bytes"), "application/octet-stream"); err != nil {
		t.Fatal(err)
	}
	if err := assets.Delete(context.Background(), patchKey); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 3 {
		t.Fatalf("got %d object requests, want 3", len(requests))
	}
	for i, key := range []string{imageKey, patchKey, patchKey} {
		if requests[i].URL.Host != "s3.invalid" || requests[i].URL.Path != "/featured-test/"+key || requests[i].Header.Get("Authorization") == "" {
			t.Fatalf("incorrect authenticated object request: %+v", requests[i])
		}
	}
	if requests[0].Method != "PUT" || requests[0].Header.Get("Content-Type") != "image/png" || requests[0].Header.Get("Content-Disposition") != "" || bodies[0] != "image-bytes" {
		t.Fatalf("incorrect image upload: %+v %s", requests[0], bodies[0])
	}
	if requests[1].Method != "PUT" || requests[1].Header.Get("Content-Type") != "application/octet-stream" ||
		requests[1].Header.Get("Content-Disposition") != `attachment; filename="`+sampleFeaturedMod().ID+`.ppf"` || bodies[1] != "PPF30-patch-bytes" {
		t.Fatalf("incorrect PPF attachment upload: %+v %s", requests[1], bodies[1])
	}
	if requests[2].Method != "DELETE" {
		t.Fatalf("incorrect object deletion: %+v", requests[2])
	}
}

func TestS3FeaturedAssetErrors(t *testing.T) {
	networkError := errors.New("offline transport error")
	client := offlineFeaturedS3Client(featuredAssetRoundTripper(func(*http.Request) (*http.Response, error) { return nil, networkError }))
	assets := S3Assets{Client: client, Bucket: "featured-test"}
	if err := assets.Upload(context.Background(), "images/"+sampleFeaturedMod().ID+".png", []byte("data"), "image/png"); !errors.Is(err, networkError) {
		t.Fatalf("upload transport error: %v", err)
	}
	if err := assets.Delete(context.Background(), "images/"+sampleFeaturedMod().ID+".png"); !errors.Is(err, networkError) {
		t.Fatalf("delete transport error: %v", err)
	}
	credentialError := errors.New("offline credential error")
	assets.Client = s3.NewFromConfig(aws.Config{
		Region: "us-west-2",
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{}, credentialError
		}),
	})
	if _, err := assets.URL(context.Background(), "images/"+sampleFeaturedMod().ID+".png"); !errors.Is(err, credentialError) {
		t.Fatalf("presigning credential error: %v", err)
	}
}
