// Package media holds the Object Storage boundary for Message attachments.
package media

import (
	"context"
	"errors"
	"time"
)

var ErrObjectNotFound = errors.New("object not found")

// ObjectInfo is what the Object Storage reports about a stored Object.
type ObjectInfo struct {
	Size        int64
	ContentType string
	// ChecksumSHA256 is the lowercase hex SHA-256 when the store reports one, else empty.
	ChecksumSHA256 string
}

// UploadRequest declares the single Object a Client may upload.
type UploadRequest struct {
	ContentType    string
	Size           int64
	ChecksumSHA256 string
}

// Upload is a short-lived instruction to PUT one Object directly to the store.
type Upload struct {
	URL       string
	Headers   map[string]string
	ExpiresAt time.Time
}

// Download is a short-lived URL that serves one Object with a safe disposition.
type Download struct {
	URL       string
	ExpiresAt time.Time
}

// Storage is an Object Storage that Clients upload to and download from directly.
type Storage interface {
	PresignUpload(key string, request UploadRequest, ttl time.Duration) (Upload, error)
	PresignDownload(key, filename, contentType string, ttl time.Duration) (Download, error)
	Stat(ctx context.Context, key string) (ObjectInfo, error)
	Delete(ctx context.Context, key string) error
}
