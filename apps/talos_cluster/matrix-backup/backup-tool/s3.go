// s3.go — S3 client initialisation, helpers, and age encryption/decryption.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"filippo.io/age"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

var s3c *s3.Client

func initS3(ctx context.Context) error {
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(s3Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			s3AccessKey, s3SecretKey, "",
		)),
	)
	if err != nil {
		return err
	}
	endpoint := "https://" + s3Endpoint
	s3c = s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = true
		o.BaseEndpoint = &endpoint
	})
	return nil
}

func s3Get(ctx context.Context, key string) ([]byte, error) {
	out, err := s3c.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s3BucketName),
		Key:    aws.String(key),
	})
	if err != nil {
		var nsk *s3types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil, nil
		}
		return nil, err
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

func s3GetJSON(ctx context.Context, key string, out interface{}) error {
	data, err := s3Get(ctx, key)
	if err != nil || data == nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func s3Put(ctx context.Context, key string, data []byte, contentType string) error {
	_, err := s3c.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s3BucketName),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String(contentType),
	})
	return err
}

func s3Exists(ctx context.Context, key string) bool {
	_, err := s3c.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s3BucketName),
		Key:    aws.String(key),
	})
	return err == nil
}

// s3DeletePrefix deletes all objects whose key starts with prefix.
func s3DeletePrefix(ctx context.Context, prefix string) error {
	paginator := s3.NewListObjectsV2Paginator(s3c, &s3.ListObjectsV2Input{
		Bucket: aws.String(s3BucketName),
		Prefix: aws.String(prefix),
	})
	deleted := 0
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, obj := range page.Contents {
			if _, err := s3c.DeleteObject(ctx, &s3.DeleteObjectInput{
				Bucket: aws.String(s3BucketName),
				Key:    obj.Key,
			}); err != nil {
				slog.Warn("Failed to delete S3 object", "key", *obj.Key, "error", err)
			} else {
				deleted++
			}
		}
	}
	slog.Info("Deleted S3 prefix", "prefix", prefix, "count", deleted)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Age encryption / decryption
// ─────────────────────────────────────────────────────────────────────────────

// ageEncrypt encrypts data for all ageRecipients and returns the ciphertext.
func ageEncrypt(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, ageRecipients...)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// getDecryptedAgeFromS3 fetches an .age object from S3 and decrypts it.
// Returns (nil, nil) when the object doesn't exist or no identity is available.
func getDecryptedAgeFromS3(ctx context.Context, key string) ([]byte, error) {
	if ageIdentity == nil {
		return nil, nil
	}
	enc, err := s3Get(ctx, key)
	if err != nil || enc == nil {
		return nil, err
	}
	r, err := age.Decrypt(bytes.NewReader(enc), ageIdentity)
	if err != nil {
		return nil, fmt.Errorf("age decrypt %s: %w", key, err)
	}
	return io.ReadAll(r)
}

// s3PutAge age-encrypts data then uploads it under key+".age".
func s3PutAge(ctx context.Context, key string, data []byte) error {
	enc, err := ageEncrypt(data)
	if err != nil {
		return fmt.Errorf("age encrypt %s: %w", key, err)
	}
	return s3Put(ctx, key+".age", enc, "application/octet-stream")
}
