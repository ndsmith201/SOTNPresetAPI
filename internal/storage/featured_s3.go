package storage

import (
	"bytes"
	"context"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type S3Assets struct {
	Client *s3.Client
	Bucket string
}

func (s S3Assets) Upload(ctx context.Context, key string, data []byte, contentType string) error {
	in := &s3.PutObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(key), Body: bytes.NewReader(data), ContentType: aws.String(contentType)}
	if strings.HasPrefix(key, "patches/") {
		in.ContentDisposition = aws.String(`attachment; filename="` + path.Base(key) + `"`)
	}
	_, err := s.Client.PutObject(ctx, in)
	return err
}

func (s S3Assets) URL(ctx context.Context, key string) (string, error) {
	result, err := s3.NewPresignClient(s.Client).PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(key)}, func(o *s3.PresignOptions) { o.Expires = 15 * time.Minute })
	if err != nil {
		return "", err
	}
	return result.URL, nil
}

func (s S3Assets) Delete(ctx context.Context, key string) error {
	_, err := s.Client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(key)})
	return err
}
