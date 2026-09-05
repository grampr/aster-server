package media

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/grampr/aster-server/internal/chat"
)

type Service struct {
	store       Store
	objects     ObjectStorage
	chat        *chat.Service
	now         func() time.Time
	uploadTTL   time.Duration
	downloadTTL time.Duration
}

func NewService(store Store, objects ObjectStorage, chatService *chat.Service) (*Service, error) {
	if store == nil || objects == nil || chatService == nil {
		return nil, errors.New("media dependencies are required")
	}
	return &Service{store: store, objects: objects, chat: chatService, now: time.Now, uploadTTL: 15 * time.Minute, downloadTTL: 5 * time.Minute}, nil
}

type CreateUploadInput struct {
	Filename, ContentType, ChecksumSHA256 string
	Size                                  int64
}

func (s *Service) CreateUploadIntent(ctx context.Context, userID, channelID uuid.UUID, input CreateUploadInput) (UploadIntent, error) {
	if _, err := s.chat.CanSendMessages(ctx, userID, channelID); err != nil {
		return UploadIntent{}, err
	}
	filename := strings.TrimSpace(filepath.Base(input.Filename))
	if filename == "." || filename == "" || utf8.RuneCountInString(filename) > 255 {
		return UploadIntent{}, &ValidationError{"filename", "must contain between 1 and 255 characters"}
	}
	for _, r := range filename {
		if unicode.IsControl(r) {
			return UploadIntent{}, &ValidationError{"filename", "must not contain control characters"}
		}
	}
	mediaType, _, err := mime.ParseMediaType(input.ContentType)
	if err != nil || len(mediaType) < 3 || len(mediaType) > 255 {
		return UploadIntent{}, &ValidationError{"content_type", "must be a valid media type"}
	}
	if input.Size < 1 || input.Size > 25<<20 {
		return UploadIntent{}, &ValidationError{"size", "must be between 1 byte and 25 MiB"}
	}
	checksum := strings.ToLower(strings.TrimSpace(input.ChecksumSHA256))
	decoded, err := hex.DecodeString(checksum)
	if err != nil || len(decoded) != 32 {
		return UploadIntent{}, &ValidationError{"checksum_sha256", "must be a 64-character SHA-256 hex digest"}
	}
	id, err := uuid.NewV7()
	if err != nil {
		return UploadIntent{}, err
	}
	createdAt := s.now().UTC()
	expired, err := s.store.ExpirePending(ctx, userID, createdAt.Add(-24*time.Hour))
	if err != nil {
		return UploadIntent{}, err
	}
	for _, key := range expired {
		_ = s.objects.Delete(ctx, key)
	}
	attachment := Attachment{ID: id, UploaderID: userID, ChannelID: channelID, ObjectKey: fmt.Sprintf("attachments/%s/%s", userID, id), Filename: filename, ContentType: mediaType, Size: input.Size, ChecksumSHA256: checksum, Status: StatusPending, CreatedAt: createdAt}
	if err := s.store.Create(ctx, attachment, maxPendingUploads, maxStoredBytes); err != nil {
		return UploadIntent{}, err
	}
	metadata := ObjectMetadata{Size: attachment.Size, ContentType: attachment.ContentType, ChecksumSHA256: attachment.ChecksumSHA256}
	url, headers, err := s.objects.PresignPut(ctx, attachment.ObjectKey, metadata, s.uploadTTL)
	if err != nil {
		_, _ = s.store.Delete(ctx, userID, id)
		return UploadIntent{}, fmt.Errorf("presign attachment upload: %w", err)
	}
	return UploadIntent{Attachment: attachment, URL: url, Headers: headers, ExpiresAt: createdAt.Add(s.uploadTTL)}, nil
}

func (s *Service) Get(ctx context.Context, userID, attachmentID uuid.UUID) (Attachment, error) {
	attachment, err := s.store.Get(ctx, attachmentID)
	if err != nil {
		return Attachment{}, err
	}
	if _, err := s.chat.GetChannel(ctx, userID, attachment.ChannelID); err != nil {
		return Attachment{}, ErrNotFound
	}
	return attachment, nil
}

func (s *Service) Finalize(ctx context.Context, userID, attachmentID uuid.UUID) (Attachment, error) {
	attachment, err := s.store.Get(ctx, attachmentID)
	if err != nil {
		return Attachment{}, err
	}
	if attachment.UploaderID != userID {
		return Attachment{}, ErrNotFound
	}
	if attachment.Status == StatusReady {
		return attachment, nil
	}
	metadata, err := s.objects.Stat(ctx, attachment.ObjectKey)
	if err != nil {
		if errors.Is(err, ErrObjectNotFound) {
			return Attachment{}, ErrConflict
		}
		return Attachment{}, fmt.Errorf("inspect attachment upload: %w", err)
	}
	if metadata.Size != attachment.Size || !strings.EqualFold(metadata.ContentType, attachment.ContentType) || !strings.EqualFold(metadata.ChecksumSHA256, attachment.ChecksumSHA256) {
		return Attachment{}, ErrConflict
	}
	return s.store.Finalize(ctx, userID, attachmentID, s.now().UTC())
}

func (s *Service) DownloadURL(ctx context.Context, userID, attachmentID uuid.UUID) (string, error) {
	intent, err := s.CreateDownloadIntent(ctx, userID, attachmentID)
	return intent.URL, err
}

func (s *Service) CreateDownloadIntent(ctx context.Context, userID, attachmentID uuid.UUID) (DownloadIntent, error) {
	attachment, err := s.Get(ctx, userID, attachmentID)
	if err != nil {
		return DownloadIntent{}, err
	}
	if attachment.Status != StatusReady {
		return DownloadIntent{}, ErrNotFound
	}
	url, err := s.objects.PresignGet(ctx, attachment.ObjectKey, s.downloadTTL)
	if err != nil {
		return DownloadIntent{}, fmt.Errorf("presign attachment download: %w", err)
	}
	return DownloadIntent{URL: url, ExpiresAt: s.now().UTC().Add(s.downloadTTL)}, nil
}

func (s *Service) Delete(ctx context.Context, userID, attachmentID uuid.UUID) error {
	objectKey, err := s.store.Delete(ctx, userID, attachmentID)
	if err != nil {
		return err
	}
	if err := s.objects.Delete(ctx, objectKey); err != nil {
		return fmt.Errorf("delete attachment object: %w", err)
	}
	return nil
}
