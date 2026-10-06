package httpapi

import (
	"net/http"

	protocolgo "github.com/grampr/Aster-protocol/packages/protocol-go/generated"
	"github.com/grampr/aster-server/internal/chat"
	"github.com/grampr/aster-server/internal/gateway"
)

func (s *Server) createAttachmentIntent(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "attachments_intent")
	if !ok {
		return
	}
	channelID, ok := s.pathID(writer, request, "channel_id")
	if !ok {
		return
	}
	var body protocolgo.CreateAttachmentUploadIntentRequest
	if err := decodeJSON(writer, request, &body); err != nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	attachment, upload, err := s.chat.CreateAttachmentIntent(request.Context(), user.ID, channelID, chat.CreateAttachmentInput{
		Filename: body.Filename, ContentType: body.ContentType, Size: body.Size, ChecksumSHA256: body.ChecksumSha256,
	})
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, protocolgo.AttachmentUploadIntent{
		Attachment: attachmentResponse(attachment), UploadUrl: upload.URL, UploadMethod: protocolgo.PUT,
		UploadHeaders: upload.Headers, ExpiresAt: upload.ExpiresAt,
	})
}

func (s *Server) finalizeAttachment(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "attachments_finalize")
	if !ok {
		return
	}
	attachmentID, ok := s.pathID(writer, request, "attachment_id")
	if !ok {
		return
	}
	attachment, err := s.chat.FinalizeAttachment(request.Context(), user.ID, attachmentID)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, attachmentResponse(attachment))
}

func (s *Server) getAttachment(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "attachments_get")
	if !ok {
		return
	}
	attachmentID, ok := s.pathID(writer, request, "attachment_id")
	if !ok {
		return
	}
	attachment, err := s.chat.GetAttachment(request.Context(), user.ID, attachmentID)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, attachmentResponse(attachment))
}

func (s *Server) deleteAttachment(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "attachments_delete")
	if !ok {
		return
	}
	attachmentID, ok := s.pathID(writer, request, "attachment_id")
	if !ok {
		return
	}
	if err := s.chat.DeleteAttachment(request.Context(), user.ID, attachmentID); err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// downloadAttachment redirects to a short-lived Object Storage URL.
func (s *Server) downloadAttachment(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "attachments_download")
	if !ok {
		return
	}
	attachmentID, ok := s.pathID(writer, request, "attachment_id")
	if !ok {
		return
	}
	download, err := s.chat.AttachmentDownload(request.Context(), user.ID, attachmentID)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Location", download.URL)
	writer.WriteHeader(http.StatusSeeOther)
}

func (s *Server) createAttachmentDownloadIntent(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "attachments_download_intent")
	if !ok {
		return
	}
	attachmentID, ok := s.pathID(writer, request, "attachment_id")
	if !ok {
		return
	}
	download, err := s.chat.AttachmentDownload(request.Context(), user.ID, attachmentID)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, protocolgo.AttachmentDownloadIntent{DownloadUrl: download.URL, ExpiresAt: download.ExpiresAt})
}

func attachmentResponse(attachment chat.Attachment) protocolgo.Attachment {
	return protocolgo.Attachment{
		Id: attachment.ID, UploaderId: attachment.UploaderID, ChannelId: attachment.ChannelID, Filename: attachment.Filename,
		ContentType: attachment.ContentType, Size: attachment.Size, ChecksumSha256: attachment.ChecksumSHA256,
		Status: protocolgo.AttachmentStatus(attachment.Status), DownloadUrl: chat.AttachmentURL(attachment.ID), CreatedAt: attachment.CreatedAt,
	}
}

func gatewayAttachments(attachments []chat.Attachment) []gateway.Attachment {
	items := make([]gateway.Attachment, len(attachments))
	for index, attachment := range attachments {
		items[index] = gateway.Attachment{
			ID: attachment.ID, UploaderID: attachment.UploaderID, ChannelID: attachment.ChannelID, Filename: attachment.Filename,
			ContentType: attachment.ContentType, Size: attachment.Size, ChecksumSHA256: attachment.ChecksumSHA256,
			Status: attachment.Status, DownloadURL: chat.AttachmentURL(attachment.ID), CreatedAt: attachment.CreatedAt,
		}
	}
	return items
}
