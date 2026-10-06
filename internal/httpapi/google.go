package httpapi

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	protocolgo "github.com/grampr/Aster-protocol/packages/protocol-go/generated"
	"github.com/grampr/aster-server/internal/auth"
)

func (s *Server) beginGoogleLogin(writer http.ResponseWriter, request *http.Request) {
	if !s.allowRequest(writer, request, "auth_google_authorize") {
		return
	}
	var body protocolgo.GoogleAuthorizationRequest
	if err := decodeJSON(writer, request, &body); err != nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	if string(body.RedirectUri) != auth.DesktopRedirectURI || string(body.CodeChallengeMethod) != "S256" {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "redirect_uri or code_challenge_method is not allowed", nil)
		return
	}
	// An Access Token makes this an attempt to link Google to the signed-in Account.
	var linkUserID *uuid.UUID
	if request.Header.Get("Authorization") != "" {
		user, ok := s.bearerUser(writer, request)
		if !ok {
			return
		}
		linkUserID = &user.ID
	}
	authorizationURL, expiresIn, err := s.auth.BeginGoogleLogin(request.Context(), string(body.CodeChallenge), string(body.ClientState), linkUserID)
	if err != nil {
		s.handleAuthError(writer, request, err)
		return
	}
	s.audit(request, "google_login_begin", "started", "link", linkUserID != nil)
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, protocolgo.GoogleAuthorizationResponse{AuthorizationUrl: authorizationURL, ExpiresIn: expiresIn})
}

// completeGoogleLogin receives Google's redirect and forwards only an Aster Exchange
// Code, never a Google token, to the Desktop Deep Link.
func (s *Server) completeGoogleLogin(writer http.ResponseWriter, request *http.Request) {
	if !s.allowRequest(writer, request, "auth_google_callback") {
		return
	}
	query := request.URL.Query()
	result, err := s.auth.CompleteGoogleLogin(request.Context(), query.Get("state"), query.Get("code"), query.Get("error"))
	if errors.Is(err, auth.ErrInvalidOAuthCallback) {
		s.audit(request, "google_login_callback", "rejected")
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_OAUTH_CALLBACK", "OAuth callback is invalid or expired", nil)
		return
	}
	if err != nil {
		s.handleAuthError(writer, request, err)
		return
	}
	if result.Cause != nil {
		s.logger.Warn("google login failed", "request_id", requestIDFromContext(request), "error", result.Cause)
	}
	s.audit(request, "google_login_callback", "redirected")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Location", result.RedirectURL)
	writer.WriteHeader(http.StatusFound)
}

func (s *Server) exchangeGoogleLogin(writer http.ResponseWriter, request *http.Request) {
	if !s.allowRequest(writer, request, "auth_google_exchange") {
		return
	}
	var body protocolgo.GoogleExchangeRequest
	if err := decodeJSON(writer, request, &body); err != nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	tokens, err := s.auth.ExchangeGoogleLogin(request.Context(), string(body.ExchangeCode), string(body.CodeVerifier))
	if err != nil {
		s.audit(request, "google_login_exchange", "rejected")
		s.handleAuthError(writer, request, err)
		return
	}
	s.audit(request, "google_login_exchange", "succeeded", "session_id", tokens.SessionID)
	writeJSON(writer, http.StatusOK, tokenResponse(tokens))
}

func (s *Server) linkGoogleIdentity(writer http.ResponseWriter, request *http.Request) {
	if !s.allowRequest(writer, request, "auth_google_link") {
		return
	}
	user, ok := s.bearerUser(writer, request)
	if !ok {
		return
	}
	var body protocolgo.GoogleExchangeRequest
	if err := decodeJSON(writer, request, &body); err != nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	linked, err := s.auth.LinkGoogleIdentity(request.Context(), user.ID, string(body.ExchangeCode), string(body.CodeVerifier))
	if err != nil {
		s.audit(request, "google_identity_link", "rejected")
		s.handleAuthError(writer, request, err)
		return
	}
	s.audit(request, "google_identity_link", "succeeded", "user_id", user.ID)
	response, err := userSelfResponse(linked)
	if err != nil {
		s.writeError(writer, request, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (s *Server) unlinkAuthenticationMethod(writer http.ResponseWriter, request *http.Request) {
	if !s.allowRequest(writer, request, "users_unlink_method") {
		return
	}
	user, ok := s.bearerUser(writer, request)
	if !ok {
		return
	}
	if err := s.auth.UnlinkAuthenticationMethod(request.Context(), user.ID, request.PathValue("method")); err != nil {
		s.handleAuthError(writer, request, err)
		return
	}
	s.audit(request, "authentication_method_unlink", "succeeded", "user_id", user.ID, "method", request.PathValue("method"))
	writer.WriteHeader(http.StatusNoContent)
}

// bearerUser authenticates the request's Access Token.
func (s *Server) bearerUser(writer http.ResponseWriter, request *http.Request) (auth.User, bool) {
	accessToken, err := bearerToken(request)
	if err != nil {
		s.writeError(writer, request, http.StatusUnauthorized, "UNAUTHORIZED", "Authentication is required", nil)
		return auth.User{}, false
	}
	user, err := s.auth.Authenticate(request.Context(), accessToken)
	if err != nil {
		s.handleAuthError(writer, request, err)
		return auth.User{}, false
	}
	return user, true
}
