package tests

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	protocolgo "github.com/grampr/Aster-protocol/packages/protocol-go/generated"
	"github.com/grampr/aster-server/internal/auth"
	"github.com/grampr/aster-server/internal/chat"
	"github.com/grampr/aster-server/internal/httpapi"
	"github.com/grampr/aster-server/internal/mail"
	postgresplatform "github.com/grampr/aster-server/internal/platform/postgres"
	"github.com/grampr/aster-server/migrations"
)

var mailedToken = regexp.MustCompile(`Code: (\S+)`)

func TestEmailVerificationPasswordResetAndAccountLinking(t *testing.T) {
	databaseURL := os.Getenv("ASTER_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("ASTER_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := postgresplatform.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := postgresplatform.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE messages, channels, guild_members, guilds, session_refresh_tokens, sessions, auth_identities, users, oauth_logins, email_tokens CASCADE`); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeGoogleProvider{key: key, codes: map[string]map[string]any{}}
	googleServer := httptest.NewServer(provider.handler())
	defer googleServer.Close()
	hasher, err := auth.NewPasswordHasher(auth.PasswordParams{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mailer := &mail.MemoryMailer{}
	authService, err := auth.NewService(auth.NewPostgresStore(pool), hasher, 15*time.Minute, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	googleClient, err := auth.NewGoogleClient(auth.GoogleConfig{
		ClientID: "client-id", ClientSecret: "client-secret", RedirectURL: "https://aster.example/api/v1/auth/google/callback",
		TokenEndpoint: googleServer.URL + "/token", JWKSEndpoint: googleServer.URL + "/certs",
	})
	if err != nil {
		t.Fatal(err)
	}
	authService.WithGoogle(googleClient).WithMailer(mailer, logger)
	chatService, err := chat.NewService(chat.NewPostgresStore(pool))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(authService, chatService, nil, logger, "test"))
	defer server.Close()
	client := server.Client()
	base := server.URL + "/api/v1"
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	const password = "correct horse battery staple"
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	clientState := "yxE4J7vB63qQj8VWfKE7i3wmMl7E2kY5gD0hT2uS9_A"
	tokenFrom := func(message mail.Message) string {
		t.Helper()
		match := mailedToken.FindStringSubmatch(message.Body)
		if match == nil {
			t.Fatalf("the email must contain a token: %q", message.Body)
		}
		return match[1]
	}
	sentTo := func(address string) []mail.Message {
		authService.WaitForMail()
		var found []mail.Message
		for _, message := range mailer.Messages() {
			if message.To == address {
				found = append(found, message)
			}
		}
		return found
	}
	register := func(email string) protocolgo.SessionTokenResponse {
		return requestJSON[protocolgo.SessionTokenResponse](t, client, http.MethodPost, base+"/auth/password/register", protocolgo.RegisterPasswordRequest{
			Email: protocolgo.Email(email), Password: password, DisplayName: "User",
		}, "", http.StatusCreated)
	}
	me := func(session protocolgo.SessionTokenResponse) protocolgo.UserSelf {
		return requestJSON[protocolgo.UserSelf](t, client, http.MethodGet, base+"/users/@me", nil, session.AccessToken, http.StatusOK)
	}
	setCooldownPassed := func(email string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE email_tokens SET created_at = created_at - interval '2 minutes' WHERE email = $1`, email); err != nil {
			t.Fatal(err)
		}
	}

	// Email verification: a token confirms the address once.
	alice := register("alice@example.com")
	if me(alice).EmailVerified {
		t.Fatal("a new account must start unverified")
	}
	requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/auth/email/verification", nil, "", http.StatusUnauthorized)
	requestJSON[struct{}](t, client, http.MethodPost, base+"/auth/email/verification", nil, alice.AccessToken, http.StatusNoContent)
	requestJSON[struct{}](t, client, http.MethodPost, base+"/auth/email/verification", nil, alice.AccessToken, http.StatusNoContent)
	messages := sentTo("alice@example.com")
	if len(messages) != 1 || !strings.Contains(messages[0].Subject, "Aster") || !strings.Contains(messages[0].Body, "aster://auth/verify-email?token=") {
		t.Fatalf("a repeat inside the cooldown must not send a second email: %+v", messages)
	}
	firstToken := tokenFrom(messages[0])
	var storedHash []byte
	if err := pool.QueryRow(ctx, `SELECT token_hash FROM email_tokens WHERE email = 'alice@example.com'`).Scan(&storedHash); err != nil || string(storedHash) == firstToken || len(storedHash) != 32 {
		t.Fatalf("only a hash of the token may be stored: %v", err)
	}
	// A later token invalidates the earlier one.
	setCooldownPassed("alice@example.com")
	requestJSON[struct{}](t, client, http.MethodPost, base+"/auth/email/verification", nil, alice.AccessToken, http.StatusNoContent)
	messages = sentTo("alice@example.com")
	if len(messages) != 2 {
		t.Fatalf("a second email must be sent after the cooldown: %d", len(messages))
	}
	requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/auth/email/verify", map[string]any{"token": firstToken}, "", http.StatusBadRequest)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/auth/email/verify", map[string]any{"token": "short"}, "", http.StatusBadRequest)
	if invalid := requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/auth/email/verify", map[string]any{"token": strings.Repeat("x", 43)}, "", http.StatusBadRequest); invalid.Code != "INVALID_VERIFICATION_TOKEN" {
		t.Fatalf("unexpected error: %+v", invalid)
	}
	secondToken := tokenFrom(messages[1])
	requestJSON[struct{}](t, client, http.MethodPost, base+"/auth/email/verify", map[string]any{"token": secondToken}, "", http.StatusNoContent)
	if !me(alice).EmailVerified {
		t.Fatal("the address must be verified")
	}
	requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/auth/email/verify", map[string]any{"token": secondToken}, "", http.StatusBadRequest)
	requestJSON[struct{}](t, client, http.MethodPost, base+"/auth/email/verification", nil, alice.AccessToken, http.StatusNoContent)
	if len(sentTo("alice@example.com")) != 2 {
		t.Fatal("a verified account must not be mailed again")
	}
	// Expired tokens and a changed address both fail.
	bobSession := register("bob@example.com")
	requestJSON[struct{}](t, client, http.MethodPost, base+"/auth/email/verification", nil, bobSession.AccessToken, http.StatusNoContent)
	bobToken := tokenFrom(sentTo("bob@example.com")[0])
	if _, err := pool.Exec(ctx, `UPDATE email_tokens SET expires_at = now() - interval '1 second' WHERE email = 'bob@example.com'`); err != nil {
		t.Fatal(err)
	}
	requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/auth/email/verify", map[string]any{"token": bobToken}, "", http.StatusBadRequest)
	// Sending is limited per user and hour.
	carol := register("carol@example.com")
	for index := 0; index < 5; index++ {
		requestJSON[struct{}](t, client, http.MethodPost, base+"/auth/email/verification", nil, carol.AccessToken, http.StatusNoContent)
	}
	requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/auth/email/verification", nil, carol.AccessToken, http.StatusTooManyRequests)

	// Password reset: no account enumeration, one use, and every session ends.
	requestJSON[struct{}](t, client, http.MethodPost, base+"/auth/password/reset-request", map[string]any{"email": "nobody@example.com"}, "", http.StatusAccepted)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/auth/password/reset-request", map[string]any{"email": "not-an-email"}, "", http.StatusBadRequest)
	if len(sentTo("nobody@example.com")) != 0 {
		t.Fatal("an unknown address must not receive email")
	}
	oldSession := requestJSON[protocolgo.SessionTokenResponse](t, client, http.MethodPost, base+"/auth/password/login", protocolgo.LoginPasswordRequest{Email: "alice@example.com", Password: password}, "", http.StatusOK)
	requestJSON[struct{}](t, client, http.MethodPost, base+"/auth/password/reset-request", map[string]any{"email": "Alice@Example.com"}, "", http.StatusAccepted)
	resetMail := sentTo("alice@example.com")
	resetMail = resetMail[len(resetMail)-1:]
	if !strings.Contains(resetMail[0].Body, "aster://auth/reset-password?token=") {
		t.Fatalf("unexpected reset email: %q", resetMail[0].Body)
	}
	resetToken := tokenFrom(resetMail[0])
	requestJSON[struct{}](t, client, http.MethodPost, base+"/auth/password/reset-request", map[string]any{"email": "alice@example.com"}, "", http.StatusAccepted)
	if len(sentTo("alice@example.com")) != 3 {
		t.Fatal("a second reset request inside the cooldown must not send email")
	}
	if weak := requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/auth/password/reset", map[string]any{"token": resetToken, "new_password": "short"}, "", http.StatusBadRequest); weak.Code != "INVALID_REQUEST" {
		t.Fatalf("unexpected error: %+v", weak)
	}
	const newPassword = "an entirely different passphrase"
	requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/auth/password/reset", map[string]any{"token": firstToken, "new_password": newPassword}, "", http.StatusBadRequest)
	requestJSON[struct{}](t, client, http.MethodPost, base+"/auth/password/reset", map[string]any{"token": resetToken, "new_password": newPassword}, "", http.StatusNoContent)
	requestJSON[protocolgo.Error](t, client, http.MethodGet, base+"/users/@me", nil, oldSession.AccessToken, http.StatusUnauthorized)
	requestJSON[protocolgo.Error](t, client, http.MethodGet, base+"/users/@me", nil, alice.AccessToken, http.StatusUnauthorized)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/auth/token/refresh", map[string]any{"refresh_token": oldSession.RefreshToken}, "", http.StatusUnauthorized)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/auth/password/login", protocolgo.LoginPasswordRequest{Email: "alice@example.com", Password: password}, "", http.StatusUnauthorized)
	renewed := requestJSON[protocolgo.SessionTokenResponse](t, client, http.MethodPost, base+"/auth/password/login", protocolgo.LoginPasswordRequest{Email: "alice@example.com", Password: newPassword}, "", http.StatusOK)
	if !me(renewed).EmailVerified {
		t.Fatal("a successful reset proves ownership of the address")
	}
	if replay := requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/auth/password/reset", map[string]any{"token": resetToken, "new_password": newPassword}, "", http.StatusBadRequest); replay.Code != "INVALID_RESET_TOKEN" {
		t.Fatalf("a spent reset token must be rejected: %+v", replay)
	}
	// A verification token cannot reset a password and vice versa.
	requestJSON[struct{}](t, client, http.MethodPost, base+"/auth/password/reset-request", map[string]any{"email": "bob@example.com"}, "", http.StatusAccepted)
	bobMails := sentTo("bob@example.com")
	requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/auth/email/verify", map[string]any{"token": tokenFrom(bobMails[len(bobMails)-1])}, "", http.StatusBadRequest)

	// Google: a password account is never linked by email, but its owner can link explicitly.
	googleAuthorize := func(bearer string) string {
		t.Helper()
		body, _ := jsonBody(map[string]any{"redirect_uri": "aster://auth/callback", "code_challenge": challenge, "code_challenge_method": "S256", "client_state": clientState})
		request, _ := http.NewRequest(http.MethodPost, base+"/auth/google/authorize", body)
		request.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			request.Header.Set("Authorization", "Bearer "+bearer)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("authorize returned %d", response.StatusCode)
		}
		payload, _ := io.ReadAll(response.Body)
		var authorization protocolgo.GoogleAuthorizationResponse
		if err := json.Unmarshal(payload, &authorization); err != nil {
			t.Fatal(err)
		}
		parsed, err := url.Parse(authorization.AuthorizationUrl)
		if err != nil {
			t.Fatal(err)
		}
		provider.mu.Lock()
		provider.nonce = parsed.Query().Get("nonce")
		provider.mu.Unlock()
		return parsed.Query().Get("state")
	}
	googleExchangeCode := func(bearer, googleCode string, claims map[string]any) string {
		t.Helper()
		provider.mu.Lock()
		provider.codes[googleCode] = claims
		provider.mu.Unlock()
		state := googleAuthorize(bearer)
		response, err := noRedirect.Get(base + "/auth/google/callback?" + url.Values{"state": {state}, "code": {googleCode}}.Encode())
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		location, err := url.Parse(response.Header.Get("Location"))
		if err != nil || location.Query().Get("code") == "" {
			t.Fatalf("%s: expected an exchange code, got status %d location %q", googleCode, response.StatusCode, response.Header.Get("Location"))
		}
		return location.Query().Get("code")
	}
	googleBody := func(code string) map[string]any {
		return map[string]any{"exchange_code": code, "code_verifier": verifier}
	}
	aliceClaims := map[string]any{"sub": "google-alice", "email": "alice@example.com", "email_verified": true, "name": "Alice G"}

	// A link attempt's code cannot be used to sign in, and is spent by trying.
	linkCode := googleExchangeCode(renewed.AccessToken, "link-1", aliceClaims)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/auth/google/exchange", googleBody(linkCode), "", http.StatusBadRequest)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/auth/google/link", googleBody(linkCode), renewed.AccessToken, http.StatusBadRequest)
	// An invalid Access Token on authorize is refused rather than silently ignored.
	invalidBody, _ := jsonBody(map[string]any{"redirect_uri": "aster://auth/callback", "code_challenge": challenge, "code_challenge_method": "S256", "client_state": clientState})
	invalidRequest, _ := http.NewRequest(http.MethodPost, base+"/auth/google/authorize", invalidBody)
	invalidRequest.Header.Set("Content-Type", "application/json")
	invalidRequest.Header.Set("Authorization", "Bearer invalid")
	invalidResponse, err := client.Do(invalidRequest)
	if err != nil {
		t.Fatal(err)
	}
	invalidResponse.Body.Close()
	if invalidResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an invalid token must be refused: %d", invalidResponse.StatusCode)
	}

	// Only the user who started the attempt can complete it.
	otherCode := googleExchangeCode(renewed.AccessToken, "link-2", aliceClaims)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/auth/google/link", googleBody(otherCode), bobSession.AccessToken, http.StatusBadRequest)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/auth/google/link", googleBody("x"), "", http.StatusUnauthorized)

	linkCode = googleExchangeCode(renewed.AccessToken, "link-3", aliceClaims)
	linked := requestJSON[protocolgo.UserSelf](t, client, http.MethodPost, base+"/auth/google/link", googleBody(linkCode), renewed.AccessToken, http.StatusOK)
	if len(linked.AuthenticationMethods) != 2 || !linked.EmailVerified {
		t.Fatalf("unexpected linked account: %+v", linked)
	}
	// Linking again, or linking the same Google account elsewhere, is a conflict.
	requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/auth/google/link", googleBody(googleExchangeCode(renewed.AccessToken, "link-4", map[string]any{"sub": "google-alice-2", "email": "x@example.com", "email_verified": true})), renewed.AccessToken, http.StatusConflict)
	if conflict := requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/auth/google/link", googleBody(googleExchangeCode(bobSession.AccessToken, "link-5", aliceClaims)), bobSession.AccessToken, http.StatusConflict); conflict.Code != "IDENTITY_ALREADY_LINKED" {
		t.Fatalf("unexpected conflict: %+v", conflict)
	}
	// Now the Google login signs in as the same account.
	signedIn := requestJSON[protocolgo.SessionTokenResponse](t, client, http.MethodPost, base+"/auth/google/exchange", googleBody(googleExchangeCode("", "login-1", aliceClaims)), "", http.StatusOK)
	if me(signedIn).Id != linked.Id {
		t.Fatal("google login must reach the linked account")
	}

	// Unlinking keeps at least one way in.
	requestJSON[protocolgo.Error](t, client, http.MethodDelete, base+"/users/@me/authentication-methods/GOOGLE", nil, "", http.StatusUnauthorized)
	requestJSON[protocolgo.Error](t, client, http.MethodDelete, base+"/users/@me/authentication-methods/FACEBOOK", nil, renewed.AccessToken, http.StatusNotFound)
	requestJSON[struct{}](t, client, http.MethodDelete, base+"/users/@me/authentication-methods/GOOGLE", nil, renewed.AccessToken, http.StatusNoContent)
	requestJSON[protocolgo.Error](t, client, http.MethodDelete, base+"/users/@me/authentication-methods/GOOGLE", nil, renewed.AccessToken, http.StatusNotFound)
	if last := requestJSON[protocolgo.Error](t, client, http.MethodDelete, base+"/users/@me/authentication-methods/PASSWORD", nil, renewed.AccessToken, http.StatusConflict); last.Code != "LAST_AUTHENTICATION_METHOD" {
		t.Fatalf("unexpected error: %+v", last)
	}
	if after := me(renewed); len(after.AuthenticationMethods) != 1 || after.AuthenticationMethods[0] != protocolgo.AuthenticationMethodPassword {
		t.Fatalf("unexpected methods after unlink: %+v", after)
	}

	// A Google-only account cannot reset a password it does not have, and cannot unlink Google.
	googleOnly := requestJSON[protocolgo.SessionTokenResponse](t, client, http.MethodPost, base+"/auth/google/exchange", googleBody(googleExchangeCode("", "login-2", map[string]any{"sub": "google-dana", "email": "dana@example.com", "email_verified": true, "name": "Dana"})), "", http.StatusOK)
	requestJSON[struct{}](t, client, http.MethodPost, base+"/auth/password/reset-request", map[string]any{"email": "dana@example.com"}, "", http.StatusAccepted)
	if len(sentTo("dana@example.com")) != 0 {
		t.Fatal("an account without a password must not receive a reset email")
	}
	requestJSON[protocolgo.Error](t, client, http.MethodDelete, base+"/users/@me/authentication-methods/GOOGLE", nil, googleOnly.AccessToken, http.StatusConflict)
	requestJSON[struct{}](t, client, http.MethodPost, base+"/auth/email/verification", nil, googleOnly.AccessToken, http.StatusNoContent)
	if len(sentTo("dana@example.com")) != 0 {
		t.Fatal("a Google-verified address needs no verification email")
	}

	// Without a mail server the email flows report themselves unavailable.
	bare, err := auth.NewService(auth.NewPostgresStore(pool), hasher, 15*time.Minute, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	bareServer := httptest.NewServer(httpapi.New(bare, chatService, nil, logger, "test"))
	defer bareServer.Close()
	if unavailable := requestJSON[protocolgo.Error](t, bareServer.Client(), http.MethodPost, bareServer.URL+"/api/v1/auth/password/reset-request", map[string]any{"email": "alice@example.com"}, "", http.StatusServiceUnavailable); unavailable.Code != "MAIL_UNAVAILABLE" {
		t.Fatalf("unexpected error: %+v", unavailable)
	}
	requestJSON[protocolgo.Error](t, bareServer.Client(), http.MethodPost, bareServer.URL+"/api/v1/auth/email/verification", nil, renewed.AccessToken, http.StatusServiceUnavailable)
}

func jsonBody(value any) (io.Reader, error) {
	payload, err := json.Marshal(value)
	return bytes.NewReader(payload), err
}
