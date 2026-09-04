package httpapi

import (
	"errors"
	"net/http"

	protocolgo "github.com/grampr/Aster-protocol/packages/protocol-go/generated"
	"github.com/grampr/aster-server/internal/chat"
	"github.com/grampr/aster-server/internal/media"
)

func (s *Server) createAttachmentUploadIntent(w http.ResponseWriter, r *http.Request) {
	user, ok := s.chatUser(w, r, "attachment_intents_create")
	if !ok {
		return
	}
	channelID, ok := s.pathID(w, r, "channel_id")
	if !ok {
		return
	}
	var body protocolgo.CreateAttachmentUploadIntentRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	intent, err := s.media.CreateUploadIntent(r.Context(), user.ID, channelID, media.CreateUploadInput{Filename: body.Filename, ContentType: body.ContentType, Size: body.Size, ChecksumSHA256: body.ChecksumSha256})
	if err != nil {
		s.handleMediaError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, protocolgo.AttachmentUploadIntent{Attachment: mediaAttachmentResponse(intent.Attachment), UploadUrl: intent.URL, UploadMethod: protocolgo.PUT, UploadHeaders: intent.Headers, ExpiresAt: intent.ExpiresAt})
}
func (s *Server) getAttachment(w http.ResponseWriter, r *http.Request) {
	user, ok := s.chatUser(w, r, "attachments_get")
	if !ok {
		return
	}
	id, ok := s.pathID(w, r, "attachment_id")
	if !ok {
		return
	}
	item, err := s.media.Get(r.Context(), user.ID, id)
	if err != nil {
		s.handleMediaError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mediaAttachmentResponse(item))
}
func (s *Server) finalizeAttachment(w http.ResponseWriter, r *http.Request) {
	user, ok := s.chatUser(w, r, "attachments_finalize")
	if !ok {
		return
	}
	id, ok := s.pathID(w, r, "attachment_id")
	if !ok {
		return
	}
	item, err := s.media.Finalize(r.Context(), user.ID, id)
	if err != nil {
		s.handleMediaError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mediaAttachmentResponse(item))
}
func (s *Server) downloadAttachment(w http.ResponseWriter, r *http.Request) {
	user, ok := s.chatUser(w, r, "attachments_download")
	if !ok {
		return
	}
	id, ok := s.pathID(w, r, "attachment_id")
	if !ok {
		return
	}
	location, err := s.media.DownloadURL(r.Context(), user.ID, id)
	if err != nil {
		s.handleMediaError(w, r, err)
		return
	}
	w.Header().Set("Location", location)
	w.WriteHeader(http.StatusSeeOther)
}

func (s *Server) createAttachmentDownloadIntent(w http.ResponseWriter, r *http.Request) {
	user, ok := s.chatUser(w, r, "attachment_download_intents_create")
	if !ok {
		return
	}
	id, ok := s.pathID(w, r, "attachment_id")
	if !ok {
		return
	}
	intent, err := s.media.CreateDownloadIntent(r.Context(), user.ID, id)
	if err != nil {
		s.handleMediaError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, protocolgo.AttachmentDownloadIntent{DownloadUrl: intent.URL, ExpiresAt: intent.ExpiresAt})
}
func (s *Server) deleteAttachment(w http.ResponseWriter, r *http.Request) {
	user, ok := s.chatUser(w, r, "attachments_delete")
	if !ok {
		return
	}
	id, ok := s.pathID(w, r, "attachment_id")
	if !ok {
		return
	}
	if err := s.media.Delete(r.Context(), user.ID, id); err != nil {
		s.handleMediaError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) handleMediaError(w http.ResponseWriter, r *http.Request, err error) {
	var validation *media.ValidationError
	switch {
	case errors.As(err, &validation):
		s.writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", validation.Error(), nil)
	case errors.Is(err, media.ErrQuota):
		s.writeError(w, r, http.StatusTooManyRequests, "UPLOAD_QUOTA_EXCEEDED", "Attachment quota exceeded", nil)
	case errors.Is(err, media.ErrConflict):
		s.writeError(w, r, http.StatusConflict, "UPLOAD_NOT_READY", "Uploaded object does not match the declared metadata", nil)
	case errors.Is(err, media.ErrForbidden), errors.Is(err, chat.ErrForbidden):
		s.writeError(w, r, http.StatusForbidden, "FORBIDDEN", "Operation is not permitted", nil)
	case errors.Is(err, media.ErrNotFound), errors.Is(err, chat.ErrNotFound):
		s.writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource was not found", nil)
	default:
		s.logger.Error("media request failed", "request_id", requestIDFromContext(r), "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", nil)
	}
}
func mediaAttachmentResponse(item media.Attachment) protocolgo.Attachment {
	return protocolgo.Attachment{Id: item.ID, UploaderId: item.UploaderID, ChannelId: item.ChannelID, Filename: item.Filename, ContentType: item.ContentType, Size: item.Size, ChecksumSha256: item.ChecksumSHA256, Status: protocolgo.AttachmentStatus(item.Status), DownloadUrl: "/api/v1/attachments/" + item.ID.String() + "/content", CreatedAt: item.CreatedAt}
}
