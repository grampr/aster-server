package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	protocolgo "github.com/grampr/Aster-protocol/packages/protocol-go/generated"
	"github.com/grampr/aster-server/internal/auth"
)

const maxRequestBodyBytes = 64 << 10

type Server struct {
	auth           *auth.Service
	logger         *slog.Logger
	version        string
	requestLimiter *fixedWindowLimiter
	loginLimiter   *fixedWindowLimiter
}

func New(authService *auth.Service, logger *slog.Logger, version string) http.Handler {
	server := &Server{
		auth: authService, logger: logger, version: version,
		requestLimiter: newFixedWindowLimiter(60, time.Minute),
		loginLimiter:   newFixedWindowLimiter(5, 15*time.Minute),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", server.health)
	mux.HandleFunc("POST /api/v1/auth/password/register", server.register)
	mux.HandleFunc("POST /api/v1/auth/password/login", server.login)
	mux.HandleFunc("POST /api/v1/auth/google/authorize", server.googleAuthorize)
	mux.HandleFunc("GET /api/v1/auth/google/callback", server.googleCallback)
	mux.HandleFunc("POST /api/v1/auth/google/exchange", server.googleExchange)
	mux.HandleFunc("POST /api/v1/auth/token/refresh", server.refresh)
	mux.HandleFunc("POST /api/v1/auth/logout", server.logout)
	mux.HandleFunc("GET /api/v1/users/@me", server.currentUser)
	return server.requestID(server.recoverPanic(mux))
}

func (s *Server) health(writer http.ResponseWriter, request *http.Request) {
	writeJSON(writer, http.StatusOK, protocolgo.HealthResponse{
		Status: protocolgo.Ok, Version: s.version, Time: time.Now().UTC(),
	})
}

func (s *Server) register(writer http.ResponseWriter, request *http.Request) {
	if !s.allowRequest(writer, request, "auth_register") {
		return
	}
	var body protocolgo.RegisterPasswordRequest
	if err := decodeJSON(writer, request, &body); err != nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	tokens, err := s.auth.Register(request.Context(), auth.RegisterInput{
		Email: string(body.Email), Password: string(body.Password), DisplayName: body.DisplayName,
	})
	if err != nil {
		s.handleAuthError(writer, request, err)
		return
	}
	s.audit(request, "password_register", "succeeded", "session_id", tokens.SessionID)
	setNoStore(writer.Header())
	writeJSON(writer, http.StatusCreated, tokenResponse(tokens))
}

func (s *Server) login(writer http.ResponseWriter, request *http.Request) {
	if !s.allowRequest(writer, request, "auth_login") {
		return
	}
	var body protocolgo.LoginPasswordRequest
	if err := decodeJSON(writer, request, &body); err != nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	loginKey := clientIP(request) + "|" + strings.ToLower(strings.TrimSpace(string(body.Email)))
	result := s.loginLimiter.Take(loginKey)
	if !result.Allowed {
		s.writeRateLimited(writer, request, "auth_login_failure", result)
		return
	}
	tokens, err := s.auth.Login(request.Context(), auth.LoginInput{Email: string(body.Email), Password: string(body.Password)})
	if err != nil {
		s.handleAuthError(writer, request, err)
		return
	}
	s.loginLimiter.Reset(loginKey)
	s.audit(request, "password_login", "succeeded", "session_id", tokens.SessionID)
	setNoStore(writer.Header())
	writeJSON(writer, http.StatusOK, tokenResponse(tokens))
}

func (s *Server) googleAuthorize(writer http.ResponseWriter, request *http.Request) {
	if !s.allowRequest(writer, request, "auth_google_authorize") {
		return
	}
	var body protocolgo.GoogleAuthorizationRequest
	if err := decodeJSON(writer, request, &body); err != nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	if body.CodeChallengeMethod != protocolgo.S256 {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "code_challenge_method must be S256", nil)
		return
	}
	authorization, err := s.auth.BeginGoogleAuthorization(request.Context(), auth.GoogleAuthorizationInput{
		RedirectURI: string(body.RedirectUri), CodeChallenge: string(body.CodeChallenge),
		ClientState: string(body.ClientState),
	})
	if err != nil {
		s.handleAuthError(writer, request, err)
		return
	}
	s.audit(request, "google_authorization", "started")
	setNoStore(writer.Header())
	writeJSON(writer, http.StatusOK, protocolgo.GoogleAuthorizationResponse{
		AuthorizationUrl: authorization.URL, ExpiresIn: int(authorization.ExpiresIn / time.Second),
	})
}

func (s *Server) googleCallback(writer http.ResponseWriter, request *http.Request) {
	if !s.allowRequest(writer, request, "auth_google_callback") {
		return
	}
	query := request.URL.Query()
	result, err := s.auth.CompleteGoogleAuthorization(request.Context(), auth.GoogleCallbackInput{
		Code: query.Get("code"), State: query.Get("state"), Error: query.Get("error"),
	})
	if result.RedirectURI != "" {
		if err != nil {
			s.logger.Error("google callback failed", "request_id", requestIDFromContext(request), "error", err)
		}
		location, buildErr := googleCallbackLocation(result)
		if buildErr != nil {
			s.writeError(writer, request, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", buildErr)
			return
		}
		outcome := "succeeded"
		if result.ErrorCode != "" {
			outcome = "rejected"
		}
		s.audit(request, "google_callback", outcome)
		writer.Header().Set("Cache-Control", "no-store")
		http.Redirect(writer, request, location, http.StatusFound)
		return
	}
	if err != nil {
		s.handleAuthError(writer, request, err)
		return
	}
	s.writeError(writer, request, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", errors.New("google callback produced no redirect"))
}

func (s *Server) googleExchange(writer http.ResponseWriter, request *http.Request) {
	if !s.allowRequest(writer, request, "auth_google_exchange") {
		return
	}
	var body protocolgo.GoogleExchangeRequest
	if err := decodeJSON(writer, request, &body); err != nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	tokens, err := s.auth.ExchangeGoogleAuthorization(request.Context(), string(body.ExchangeCode), string(body.CodeVerifier))
	if err != nil {
		s.handleAuthError(writer, request, err)
		return
	}
	s.audit(request, "google_exchange", "succeeded", "session_id", tokens.SessionID)
	setNoStore(writer.Header())
	writeJSON(writer, http.StatusOK, tokenResponse(tokens))
}

func (s *Server) refresh(writer http.ResponseWriter, request *http.Request) {
	if !s.allowRequest(writer, request, "auth_refresh") {
		return
	}
	var body protocolgo.RefreshSessionRequest
	if err := decodeJSON(writer, request, &body); err != nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	tokens, err := s.auth.Refresh(request.Context(), string(body.RefreshToken))
	if err != nil {
		s.handleAuthError(writer, request, err)
		return
	}
	s.audit(request, "session_refresh", "succeeded", "session_id", tokens.SessionID)
	setNoStore(writer.Header())
	writeJSON(writer, http.StatusOK, tokenResponse(tokens))
}

func (s *Server) logout(writer http.ResponseWriter, request *http.Request) {
	if !s.allowRequest(writer, request, "auth_logout") {
		return
	}
	accessToken, err := bearerToken(request)
	if err != nil {
		s.writeError(writer, request, http.StatusUnauthorized, "UNAUTHORIZED", "Authentication is required", nil)
		return
	}
	var body protocolgo.LogoutRequest
	if err := decodeJSON(writer, request, &body); err != nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	if err := s.auth.Logout(request.Context(), accessToken, string(body.RefreshToken)); err != nil {
		s.handleAuthError(writer, request, err)
		return
	}
	s.audit(request, "session_logout", "succeeded")
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) currentUser(writer http.ResponseWriter, request *http.Request) {
	if !s.allowRequest(writer, request, "users_me") {
		return
	}
	accessToken, err := bearerToken(request)
	if err != nil {
		s.writeError(writer, request, http.StatusUnauthorized, "UNAUTHORIZED", "Authentication is required", nil)
		return
	}
	user, err := s.auth.Authenticate(request.Context(), accessToken)
	if err != nil {
		s.handleAuthError(writer, request, err)
		return
	}
	methods := make([]protocolgo.AuthenticationMethod, 0, len(user.AuthenticationMethods))
	for _, method := range user.AuthenticationMethods {
		value := protocolgo.AuthenticationMethod(method)
		if !value.Valid() {
			s.writeError(writer, request, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", fmt.Errorf("unsupported authentication method %q", method))
			return
		}
		methods = append(methods, value)
	}
	writeJSON(writer, http.StatusOK, protocolgo.UserSelf{
		Id: user.ID, Email: protocolgo.Email(user.Email), EmailVerified: user.EmailVerified,
		DisplayName: user.DisplayName, AvatarUrl: user.AvatarURL,
		AuthenticationMethods: methods, CreatedAt: user.CreatedAt,
	})
}

func (s *Server) allowRequest(writer http.ResponseWriter, request *http.Request, bucket string) bool {
	result := s.requestLimiter.Take(clientIP(request) + "|" + bucket)
	setRateLimitHeaders(writer.Header(), bucket, result)
	if !result.Allowed {
		s.writeRateLimited(writer, request, bucket, result)
		return false
	}
	return true
}

func (s *Server) handleAuthError(writer http.ResponseWriter, request *http.Request, err error) {
	var validationError *auth.ValidationError
	switch {
	case errors.As(err, &validationError):
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", validationError.Error(), nil)
	case errors.Is(err, auth.ErrEmailAlreadyRegistered):
		s.writeError(writer, request, http.StatusConflict, "EMAIL_ALREADY_REGISTERED", "Email is already registered", nil)
	case errors.Is(err, auth.ErrInvalidCredentials):
		s.writeError(writer, request, http.StatusUnauthorized, "INVALID_CREDENTIALS", "Email or password is incorrect", nil)
	case errors.Is(err, auth.ErrInvalidRefreshToken):
		s.writeError(writer, request, http.StatusUnauthorized, "INVALID_REFRESH_TOKEN", "Refresh token is invalid or expired", nil)
	case errors.Is(err, auth.ErrInvalidAuthorizationGrant):
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_AUTHORIZATION_GRANT", "Authorization grant is invalid or expired", nil)
	case errors.Is(err, auth.ErrAccountLinkRequired):
		s.writeError(writer, request, http.StatusConflict, "ACCOUNT_LINK_REQUIRED", "Sign in to the existing account before linking Google", nil)
	case errors.Is(err, auth.ErrInvalidOAuthCallback):
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_OAUTH_CALLBACK", "OAuth callback is invalid or expired", nil)
	case errors.Is(err, auth.ErrGoogleUnavailable):
		s.writeError(writer, request, http.StatusServiceUnavailable, "GOOGLE_AUTHENTICATION_UNAVAILABLE", "Google authentication is unavailable", nil)
	case errors.Is(err, auth.ErrUnauthorized):
		s.writeError(writer, request, http.StatusUnauthorized, "UNAUTHORIZED", "Authentication is required", nil)
	default:
		s.writeError(writer, request, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", err)
	}
}

func (s *Server) writeError(writer http.ResponseWriter, request *http.Request, status int, code, message string, cause error) {
	requestID := requestIDFromContext(request)
	if cause != nil {
		s.logger.Error("request failed", "request_id", requestID, "method", request.Method, "path", request.URL.Path, "error", cause)
	}
	if status == http.StatusUnauthorized || status == http.StatusConflict || status == http.StatusTooManyRequests {
		s.audit(request, "authentication", "rejected", "code", code)
	}
	if status == http.StatusUnauthorized {
		writer.Header().Set("WWW-Authenticate", "Bearer")
	}
	id, _ := uuid.Parse(requestID)
	writeJSON(writer, status, protocolgo.Error{Code: code, Message: message, RequestId: id})
}

func (s *Server) audit(request *http.Request, event, outcome string, attributes ...any) {
	base := []any{
		"event", event,
		"outcome", outcome,
		"request_id", requestIDFromContext(request),
		"client_ip", clientIP(request),
	}
	s.logger.Info("authentication audit", append(base, attributes...)...)
}

func (s *Server) writeRateLimited(writer http.ResponseWriter, request *http.Request, bucket string, result limitResult) {
	setRateLimitHeaders(writer.Header(), bucket, result)
	retrySeconds := int64(result.RetryAfter.Round(time.Second) / time.Second)
	if retrySeconds < 1 {
		retrySeconds = 1
	}
	writer.Header().Set("Retry-After", strconv.FormatInt(retrySeconds, 10))
	details := map[string]any{"retry_after_ms": result.RetryAfter.Milliseconds()}
	id, _ := uuid.Parse(requestIDFromContext(request))
	writeJSON(writer, http.StatusTooManyRequests, protocolgo.Error{
		Code: "RATE_LIMITED", Message: "Too many requests", RequestId: id, Details: &details,
	})
}

func tokenResponse(tokens auth.SessionTokens) protocolgo.SessionTokenResponse {
	return protocolgo.SessionTokenResponse{
		AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken,
		TokenType: protocolgo.Bearer, SessionId: tokens.SessionID,
		ExpiresIn:        int(tokens.AccessExpiresIn / time.Second),
		RefreshExpiresIn: int(tokens.RefreshExpiresIn / time.Second),
	}
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, target any) error {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("Content-Type must be application/json")
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func writeJSON(writer http.ResponseWriter, status int, body any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(body)
}

func setNoStore(header http.Header) {
	header.Set("Cache-Control", "no-store")
	header.Set("Pragma", "no-cache")
}

func googleCallbackLocation(result auth.GoogleCallbackResult) (string, error) {
	location, err := url.Parse(result.RedirectURI)
	if err != nil || location.Scheme != "aster" || location.Host != "auth" || location.Path != "/callback" {
		return "", errors.New("google callback redirect URI is not allowed")
	}
	query := location.Query()
	query.Set("state", result.ClientState)
	if result.ExchangeCode != "" && result.ErrorCode == "" {
		query.Set("code", result.ExchangeCode)
	} else if result.ErrorCode != "" && result.ExchangeCode == "" {
		query.Set("error", result.ErrorCode)
	} else {
		return "", errors.New("google callback result must contain either code or error")
	}
	location.RawQuery = query.Encode()
	return location.String(), nil
}

func bearerToken(request *http.Request) (string, error) {
	parts := strings.Fields(request.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", auth.ErrUnauthorized
	}
	return parts[1], nil
}

func clientIP(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err == nil {
		return host
	}
	return request.RemoteAddr
}

func setRateLimitHeaders(header http.Header, bucket string, result limitResult) {
	header.Set("X-RateLimit-Limit", strconv.Itoa(result.Limit))
	header.Set("X-RateLimit-Remaining", strconv.Itoa(result.Remaining))
	header.Set("X-RateLimit-Reset", strconv.FormatInt(result.Reset.Unix(), 10))
	header.Set("X-RateLimit-Bucket", bucket)
}
