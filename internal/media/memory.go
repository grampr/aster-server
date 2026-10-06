package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// MemoryStorage is an in-process Storage for tests and local development without an
// Object Storage. Its URLs are not fetchable; use Put to simulate a Client upload.
type MemoryStorage struct {
	mu      sync.Mutex
	objects map[string]memoryObject
	now     func() time.Time
}

type memoryObject struct {
	body        []byte
	contentType string
}

func NewMemoryStorage() *MemoryStorage {
	return &MemoryStorage{objects: make(map[string]memoryObject), now: time.Now}
}

func (m *MemoryStorage) PresignUpload(key string, request UploadRequest, ttl time.Duration) (Upload, error) {
	return Upload{
		URL:       "memory://upload/" + key,
		Headers:   map[string]string{"Content-Type": request.ContentType},
		ExpiresAt: m.now().Add(ttl),
	}, nil
}

func (m *MemoryStorage) PresignDownload(key, filename, contentType string, ttl time.Duration) (Download, error) {
	return Download{URL: "memory://download/" + key, ExpiresAt: m.now().Add(ttl)}, nil
}

// Put stores an Object as a successful Client upload would.
func (m *MemoryStorage) Put(key, contentType string, body []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = memoryObject{body: body, contentType: contentType}
}

func (m *MemoryStorage) Has(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.objects[key]
	return ok
}

func (m *MemoryStorage) Stat(_ context.Context, key string) (ObjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	object, ok := m.objects[key]
	if !ok {
		return ObjectInfo{}, ErrObjectNotFound
	}
	digest := sha256.Sum256(object.body)
	return ObjectInfo{Size: int64(len(object.body)), ContentType: object.contentType, ChecksumSHA256: hex.EncodeToString(digest[:])}, nil
}

func (m *MemoryStorage) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}
