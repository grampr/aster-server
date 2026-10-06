package httpapi

import (
	"errors"
	"net/http"
	"strings"

	protocolgo "github.com/grampr/Aster-protocol/packages/protocol-go/generated"
	"github.com/grampr/aster-server/internal/auth"
)

// takeMail applies the strict hourly limit shared by endpoints that send email.
func (s *Server) takeMail(writer http.ResponseWriter, request *http.Request, bucket, subject string) bool {
	result := s.mailLimiter.Take(clientIP(request) + "|" + bucket + "|" + subject)
	if !result.Allowed {
		s.writeRateLimited(writer, request, bucket, result)
		return false
	}
	return true
}

func (s *Server) requestEmailVerification(writer http.ResponseWriter, request *http.Request) {
	if !s.allowRequest(writer, request, "auth_email_verification") {
		return
	}
	user, ok := s.bearerUser(writer, request)
	if !ok {
		return
	}
	if !s.takeMail(writer, request, "auth_email_verification_mail", user.ID.String()) {
		return
	}
	if err := s.auth.RequestEmailVerification(request.Context(), user); err != nil {
		s.handleAuthError(writer, request, err)
		return
	}
	s.audit(request, "email_verification_request", "accepted", "user_id", user.ID)
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) verifyEmail(writer http.ResponseWriter, request *http.Request) {
	if !s.allowRequest(writer, request, "auth_email_verify") {
		return
	}
	var body protocolgo.VerifyEmailRequest
	if err := decodeJSON(writer, request, &body); err != nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	if err := s.auth.VerifyEmail(request.Context(), string(body.Token)); err != nil {
		s.audit(request, "email_verify", "rejected")
		if errors.Is(err, auth.ErrInvalidToken) {
			s.writeError(writer, request, http.StatusBadRequest, "INVALID_VERIFICATION_TOKEN", "Verification token is invalid or expired", nil)
			return
		}
		s.handleAuthError(writer, request, err)
		return
	}
	s.audit(request, "email_verify", "succeeded")
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) requestPasswordReset(writer http.ResponseWriter, request *http.Request) {
	if !s.allowRequest(writer, request, "auth_password_reset_request") {
		return
	}
	var body protocolgo.RequestPasswordResetRequest
	if err := decodeJSON(writer, request, &body); err != nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	if !s.takeMail(writer, request, "auth_password_reset_mail", strings.ToLower(strings.TrimSpace(string(body.Email)))) {
		return
	}
	if err := s.auth.RequestPasswordReset(request.Context(), string(body.Email)); err != nil {
		s.handleAuthError(writer, request, err)
		return
	}
	// The same answer whether or not an Account exists.
	s.audit(request, "password_reset_request", "accepted")
	writer.WriteHeader(http.StatusAccepted)
}

func (s *Server) resetPassword(writer http.ResponseWriter, request *http.Request) {
	if !s.allowRequest(writer, request, "auth_password_reset") {
		return
	}
	var body protocolgo.ResetPasswordRequest
	if err := decodeJSON(writer, request, &body); err != nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	if err := s.auth.ResetPassword(request.Context(), string(body.Token), string(body.NewPassword)); err != nil {
		s.audit(request, "password_reset", "rejected")
		if errors.Is(err, auth.ErrInvalidToken) {
			s.writeError(writer, request, http.StatusBadRequest, "INVALID_RESET_TOKEN", "Reset token is invalid or expired", nil)
			return
		}
		s.handleAuthError(writer, request, err)
		return
	}
	s.audit(request, "password_reset", "succeeded")
	writer.WriteHeader(http.StatusNoContent)
}
