package objectstorage

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/grampr/aster-server/internal/media"
)

func TestS3PresignPutScopesURLAndIntegrityHeaders(t *testing.T) {
	storage, err := NewS3(Config{Endpoint: "https://account.r2.cloudflarestorage.com", Region: "auto", Bucket: "aster-media", AccessKeyID: "access", SecretAccessKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	checksum := strings.Repeat("ab", 32)
	url, headers, err := storage.PresignPut(context.Background(), "attachments/user/file", media.ObjectMetadata{Size: 128, ContentType: "image/png", ChecksumSHA256: checksum}, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(url, "aster-media") || !strings.Contains(url, "attachments/user/file") || !strings.Contains(url, "X-Amz-Signature=") {
		t.Fatalf("unexpected presigned URL: %s", url)
	}
	if headers["Content-Type"] != "image/png" || headers["Content-Length"] != "128" || headers["x-amz-checksum-sha256"] == "" {
		t.Fatalf("missing integrity headers: %+v", headers)
	}
}
