package chat

import (
	"context"
	"errors"
	"mime"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/grampr/aster-server/internal/media"
)

const (
	maxAttachmentSize      = 25 << 20
	maxMessageAttachments  = 10
	maxUnusedAttachments   = 25
	uploadIntentLifetime   = 15 * time.Minute
	downloadIntentLifetime = 5 * time.Minute
)

var checksumPattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// WithStorage enables attachments by connecting the Service to an Object Storage.
func (s *Service) WithStorage(storage media.Storage) *Service {
	s.storage = storage
	return s
}

// AttachmentURL is the API path that redirects to the Attachment's Object.
func AttachmentURL(attachmentID uuid.UUID) string {
	return "/api/v1/attachments/" + attachmentID.String() + "/content"
}

// CreateAttachmentIntent records an upload and returns the one-Object URL the Client
// may PUT to. The file itself never passes through the Server.
func (s *Service) CreateAttachmentIntent(ctx context.Context, userID, channelID uuid.UUID, input CreateAttachmentInput) (Attachment, media.Upload, error) {
	if s.storage == nil {
		return Attachment{}, media.Upload{}, ErrStorageUnavailable
	}
	channel, err := s.store.GetChannel(ctx, userID, channelID)
	if err != nil {
		return Attachment{}, media.Upload{}, err
	}
	if !channel.IsText() {
		return Attachment{}, media.Upload{}, ErrNotFound
	}
	if channel.GuildID != nil {
		if _, err := s.require(ctx, userID, *channel.GuildID, PermSendMessages); err != nil {
			return Attachment{}, media.Upload{}, err
		}
	}
	filename, err := validateFilename(input.Filename)
	if err != nil {
		return Attachment{}, media.Upload{}, err
	}
	contentType, err := validateContentType(input.ContentType)
	if err != nil {
		return Attachment{}, media.Upload{}, err
	}
	if input.Size < 1 || input.Size > maxAttachmentSize {
		return Attachment{}, media.Upload{}, &ValidationError{Field: "size", Message: "must be between 1 byte and 25 MiB"}
	}
	if !checksumPattern.MatchString(input.ChecksumSHA256) {
		return Attachment{}, media.Upload{}, &ValidationError{Field: "checksum_sha256", Message: "must be a SHA-256 hex digest"}
	}
	id, err := newUUIDv7()
	if err != nil {
		return Attachment{}, media.Upload{}, err
	}
	attachment, err := s.store.CreateAttachment(ctx, Attachment{
		ID: id, ChannelID: channelID, UploaderID: userID, Filename: filename, ContentType: contentType,
		Size: input.Size, ChecksumSHA256: strings.ToLower(input.ChecksumSHA256),
		// The key holds only server-generated IDs, never the Client's file name.
		ObjectKey: "attachments/" + channelID.String() + "/" + id.String(), CreatedAt: s.now().UTC(),
	}, maxUnusedAttachments)
	if err != nil {
		return Attachment{}, media.Upload{}, err
	}
	upload, err := s.storage.PresignUpload(attachment.ObjectKey, media.UploadRequest{
		ContentType: attachment.ContentType, Size: attachment.Size, ChecksumSHA256: attachment.ChecksumSHA256,
	}, uploadIntentLifetime)
	if err != nil {
		return Attachment{}, media.Upload{}, err
	}
	return attachment, upload, nil
}

// FinalizeAttachment confirms that the uploaded Object matches its declaration.
func (s *Service) FinalizeAttachment(ctx context.Context, userID, attachmentID uuid.UUID) (Attachment, error) {
	if s.storage == nil {
		return Attachment{}, ErrStorageUnavailable
	}
	attachment, err := s.store.GetAttachment(ctx, userID, attachmentID)
	if err != nil {
		return Attachment{}, err
	}
	if attachment.UploaderID != userID {
		return Attachment{}, ErrForbidden
	}
	if attachment.Status == AttachmentReady {
		return attachment, nil
	}
	info, err := s.storage.Stat(ctx, attachment.ObjectKey)
	if errors.Is(err, media.ErrObjectNotFound) {
		return Attachment{}, ErrAttachmentMismatch
	}
	if err != nil {
		return Attachment{}, err
	}
	if info.Size != attachment.Size || !sameMediaType(info.ContentType, attachment.ContentType) {
		return Attachment{}, ErrAttachmentMismatch
	}
	// The store already rejected a body that differs from the signed checksum header;
	// compare again whenever it reports one.
	if info.ChecksumSHA256 != "" && !strings.EqualFold(info.ChecksumSHA256, attachment.ChecksumSHA256) {
		return Attachment{}, ErrAttachmentMismatch
	}
	return s.store.MarkAttachmentReady(ctx, attachmentID)
}

func (s *Service) GetAttachment(ctx context.Context, userID, attachmentID uuid.UUID) (Attachment, error) {
	return s.store.GetAttachment(ctx, userID, attachmentID)
}

func (s *Service) DeleteAttachment(ctx context.Context, userID, attachmentID uuid.UUID) error {
	attachment, err := s.store.GetAttachment(ctx, userID, attachmentID)
	if err != nil {
		return err
	}
	if attachment.UploaderID != userID {
		return ErrForbidden
	}
	return s.store.DeleteAttachment(ctx, userID, attachmentID)
}

// AttachmentDownload returns a short-lived URL for a finalized Attachment in a Channel
// the caller can read.
func (s *Service) AttachmentDownload(ctx context.Context, userID, attachmentID uuid.UUID) (media.Download, error) {
	if s.storage == nil {
		return media.Download{}, ErrStorageUnavailable
	}
	attachment, err := s.store.GetAttachment(ctx, userID, attachmentID)
	if err != nil {
		return media.Download{}, err
	}
	if attachment.Status != AttachmentReady {
		return media.Download{}, ErrNotFound
	}
	return s.storage.PresignDownload(attachment.ObjectKey, attachment.Filename, attachment.ContentType, downloadIntentLifetime)
}

func validateFilename(value string) (string, error) {
	value = strings.TrimSpace(value)
	if length := utf8.RuneCountInString(value); length < 1 || length > 255 {
		return "", &ValidationError{Field: "filename", Message: "must contain between 1 and 255 characters"}
	}
	for _, character := range value {
		if unicode.IsControl(character) || character == '/' || character == '\\' {
			return "", &ValidationError{Field: "filename", Message: "must not contain control characters or path separators"}
		}
	}
	return value, nil
}

// validateContentType returns the media type without parameters, lowercased.
func validateContentType(value string) (string, error) {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil || !strings.Contains(mediaType, "/") || len(mediaType) < 3 || len(mediaType) > 255 {
		return "", &ValidationError{Field: "content_type", Message: "must be a valid media type"}
	}
	return mediaType, nil
}

func sameMediaType(reported, declared string) bool {
	mediaType, _, err := mime.ParseMediaType(reported)
	return err == nil && mediaType == declared
}
