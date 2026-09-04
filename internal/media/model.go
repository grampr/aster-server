package media

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrNotFound       = errors.New("attachment not found")
	ErrForbidden      = errors.New("attachment operation forbidden")
	ErrConflict       = errors.New("attachment upload is incomplete or invalid")
	ErrQuota          = errors.New("attachment quota exceeded")
	ErrObjectNotFound = errors.New("attachment object not found")
)

const (
	StatusPending           = "PENDING"
	StatusReady             = "READY"
	maxPendingUploads       = 20
	maxStoredBytes    int64 = 1 << 30
)

type Attachment struct {
	ID             uuid.UUID
	UploaderID     uuid.UUID
	ChannelID      uuid.UUID
	ObjectKey      string
	Filename       string
	ContentType    string
	Size           int64
	ChecksumSHA256 string
	Status         string
	CreatedAt      time.Time
	FinalizedAt    *time.Time
}

type UploadIntent struct {
	Attachment Attachment
	URL        string
	Headers    map[string]string
	ExpiresAt  time.Time
}

type ObjectMetadata struct {
	Size           int64
	ContentType    string
	ChecksumSHA256 string
}

type ObjectStorage interface {
	PresignPut(context.Context, string, ObjectMetadata, time.Duration) (string, map[string]string, error)
	PresignGet(context.Context, string, time.Duration) (string, error)
	Stat(context.Context, string) (ObjectMetadata, error)
	Delete(context.Context, string) error
}

type Store interface {
	ExpirePending(context.Context, uuid.UUID, time.Time) ([]string, error)
	Create(context.Context, Attachment, int, int64) error
	Get(context.Context, uuid.UUID) (Attachment, error)
	Finalize(context.Context, uuid.UUID, uuid.UUID, time.Time) (Attachment, error)
	Delete(context.Context, uuid.UUID, uuid.UUID) (string, error)
}

type ValidationError struct{ Field, Message string }

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }
