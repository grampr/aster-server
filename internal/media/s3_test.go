package media

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The expected signature is the worked example from the AWS Signature Version 4
// documentation for query-string authentication.
func TestSignURLMatchesAWSDocumentationExample(t *testing.T) {
	got := signURL(signInput{
		Method: http.MethodGet, Scheme: "https", Host: "examplebucket.s3.amazonaws.com", Path: "/test.txt",
		Region: "us-east-1", AccessKey: "AKIAIOSFODNN7EXAMPLE", SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		Now: time.Date(2013, time.May, 24, 0, 0, 0, 0, time.UTC), TTL: 86400 * time.Second,
	})
	const want = "https://examplebucket.s3.amazonaws.com/test.txt?X-Amz-Algorithm=AWS4-HMAC-SHA256" +
		"&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request" +
		"&X-Amz-Date=20130524T000000Z&X-Amz-Expires=86400&X-Amz-SignedHeaders=host" +
		"&X-Amz-Signature=aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404"
	if got != want {
		t.Fatalf("signature mismatch:\n got %s\nwant %s", got, want)
	}
}

func newTestStorage(t *testing.T, endpoint string, pathStyle bool) *S3Storage {
	t.Helper()
	storage, err := NewS3Storage(S3Config{
		Endpoint: endpoint, Region: "us-east-1", Bucket: "aster", AccessKey: "key", SecretKey: "secret", PathStyle: pathStyle,
	})
	if err != nil {
		t.Fatal(err)
	}
	storage.now = func() time.Time { return time.Date(2026, time.October, 5, 12, 0, 0, 0, time.UTC) }
	return storage
}

func TestPresignUploadSignsTheDeclaredHeaders(t *testing.T) {
	storage := newTestStorage(t, "http://localhost:9000", true)
	checksum := strings.Repeat("ab", 32)
	upload, err := storage.PresignUpload("attachments/c/a", UploadRequest{ContentType: "image/png", Size: 3, ChecksumSHA256: checksum}, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(upload.URL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "localhost:9000" || parsed.Path != "/aster/attachments/c/a" {
		t.Fatalf("unexpected path-style URL: %s", upload.URL)
	}
	if signed := parsed.Query().Get("X-Amz-SignedHeaders"); signed != "content-type;host;x-amz-checksum-sha256" {
		t.Fatalf("unexpected signed headers: %s", signed)
	}
	if upload.Headers["Content-Type"] != "image/png" || upload.Headers["x-amz-checksum-sha256"] != "q6urq6urq6urq6urq6urq6urq6urq6urq6urq6urq6s=" {
		t.Fatalf("unexpected upload headers: %+v", upload.Headers)
	}
	if !upload.ExpiresAt.Equal(storage.now().Add(10 * time.Minute)) {
		t.Fatalf("unexpected expiry: %s", upload.ExpiresAt)
	}
	if _, err := storage.PresignUpload("k", UploadRequest{ChecksumSHA256: "zz"}, time.Minute); err == nil {
		t.Fatal("an invalid checksum must be rejected")
	}
}

func TestPresignDownloadForcesAttachmentExceptRasterImages(t *testing.T) {
	storage := newTestStorage(t, "https://s3.example.com", false)
	for contentType, want := range map[string]string{"text/html": "attachment", "image/svg+xml": "attachment", "image/png": "inline"} {
		download, err := storage.PresignDownload("k", "メモ.html", contentType, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		parsed, _ := url.Parse(download.URL)
		if parsed.Host != "aster.s3.example.com" {
			t.Fatalf("unexpected virtual-hosted URL: %s", download.URL)
		}
		if disposition := parsed.Query().Get("response-content-disposition"); !strings.HasPrefix(disposition, want+";") {
			t.Fatalf("%s must be served as %s, got %q", contentType, want, disposition)
		}
	}
}

func TestStatAndDeleteTalkToTheStore(t *testing.T) {
	var deleted bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("X-Amz-Signature") == "" {
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		switch {
		case request.Method == http.MethodHead && request.URL.Path == "/aster/present":
			if request.Header.Get("x-amz-checksum-mode") != "ENABLED" {
				t.Errorf("checksum mode header is not sent")
			}
			writer.Header().Set("Content-Type", "image/png")
			writer.Header().Set("Content-Length", "3")
			writer.Header().Set("x-amz-checksum-sha256", "zc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc0=")
		case request.Method == http.MethodHead:
			writer.WriteHeader(http.StatusNotFound)
		case request.Method == http.MethodDelete:
			deleted = true
			writer.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	storage := newTestStorage(t, server.URL, true)
	info, err := storage.Stat(context.Background(), "present")
	if err != nil {
		t.Fatal(err)
	}
	if info.Size != 3 || info.ContentType != "image/png" || len(info.ChecksumSHA256) != 64 {
		t.Fatalf("unexpected object info: %+v", info)
	}
	if _, err := storage.Stat(context.Background(), "missing"); err != ErrObjectNotFound {
		t.Fatalf("expected ErrObjectNotFound, got %v", err)
	}
	if err := storage.Delete(context.Background(), "present"); err != nil || !deleted {
		t.Fatalf("delete failed: %v deleted=%v", err, deleted)
	}
}

func TestNewS3StorageValidatesConfiguration(t *testing.T) {
	if _, err := NewS3Storage(S3Config{Endpoint: "ftp://x", Region: "r", Bucket: "b", AccessKey: "a", SecretKey: "s"}); err == nil {
		t.Fatal("a non-http endpoint must be rejected")
	}
	if _, err := NewS3Storage(S3Config{Endpoint: "http://x", Region: "r", Bucket: "b", AccessKey: "a"}); err == nil {
		t.Fatal("a missing secret key must be rejected")
	}
}
