package objectstorage

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/grampr/aster-server/internal/media"
)

type Config struct {
	Endpoint, Region, Bucket, AccessKeyID, SecretAccessKey string
	PathStyle                                              bool
}

type S3 struct {
	bucket  string
	client  *s3.Client
	presign *s3.PresignClient
}

func NewS3(config Config) (*S3, error) {
	if config.Endpoint == "" || config.Bucket == "" || config.AccessKeyID == "" || config.SecretAccessKey == "" {
		return nil, errors.New("object storage endpoint, bucket, and credentials are required")
	}
	region := config.Region
	if region == "" {
		region = "auto"
	}
	awsConfig := aws.Config{Region: region, Credentials: credentials.NewStaticCredentialsProvider(config.AccessKeyID, config.SecretAccessKey, "")}
	client := s3.NewFromConfig(awsConfig, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(strings.TrimSuffix(config.Endpoint, "/"))
		options.UsePathStyle = config.PathStyle
	})
	return &S3{bucket: config.Bucket, client: client, presign: s3.NewPresignClient(client)}, nil
}

func (s *S3) PresignPut(ctx context.Context, key string, metadata media.ObjectMetadata, ttl time.Duration) (string, map[string]string, error) {
	checksumBytes, err := hex.DecodeString(metadata.ChecksumSHA256)
	if err != nil {
		return "", nil, err
	}
	checksumBase64 := base64.StdEncoding.EncodeToString(checksumBytes)
	result, err := s.presign.PresignPutObject(ctx, &s3.PutObjectInput{Bucket: &s.bucket, Key: &key, ContentType: &metadata.ContentType, ContentLength: &metadata.Size, ChecksumSHA256: &checksumBase64, Metadata: map[string]string{"sha256": metadata.ChecksumSHA256}}, func(options *s3.PresignOptions) { options.Expires = ttl })
	if err != nil {
		return "", nil, err
	}
	headers := map[string]string{"Content-Type": metadata.ContentType, "Content-Length": fmt.Sprint(metadata.Size), "x-amz-checksum-sha256": checksumBase64, "x-amz-meta-sha256": metadata.ChecksumSHA256}
	return result.URL, headers, nil
}

func (s *S3) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	result, err := s.presign.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key}, func(options *s3.PresignOptions) { options.Expires = ttl })
	if err != nil {
		return "", err
	}
	return result.URL, nil
}

func (s *S3) Stat(ctx context.Context, key string) (media.ObjectMetadata, error) {
	result, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.bucket, Key: &key, ChecksumMode: types.ChecksumModeEnabled})
	if err != nil {
		if IsNotFound(err) {
			return media.ObjectMetadata{}, media.ErrObjectNotFound
		}
		return media.ObjectMetadata{}, err
	}
	checksum := result.Metadata["sha256"]
	if result.ChecksumSHA256 != nil {
		if decoded, decodeErr := base64.StdEncoding.DecodeString(*result.ChecksumSHA256); decodeErr == nil {
			checksum = hex.EncodeToString(decoded)
		}
	}
	return media.ObjectMetadata{Size: aws.ToInt64(result.ContentLength), ContentType: aws.ToString(result.ContentType), ChecksumSHA256: checksum}, nil
}

func (s *S3) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &key})
	return err
}

func IsNotFound(err error) bool {
	var api smithy.APIError
	return errors.As(err, &api) && (api.ErrorCode() == "NotFound" || api.ErrorCode() == "NoSuchKey")
}
