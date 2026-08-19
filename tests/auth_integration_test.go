package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	protocolgo "github.com/grampr/Aster-protocol/packages/protocol-go/generated"
	"github.com/grampr/aster-server/internal/auth"
	"github.com/grampr/aster-server/internal/chat"
	"github.com/grampr/aster-server/internal/httpapi"
	postgresplatform "github.com/grampr/aster-server/internal/platform/postgres"
	"github.com/grampr/aster-server/migrations"
)

func TestAuthenticationLifecycle(t *testing.T) {
	databaseURL := os.Getenv("ASTER_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("ASTER_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := postgresplatform.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := postgresplatform.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE session_refresh_tokens, sessions, auth_identities, users CASCADE`); err != nil {
		t.Fatal(err)
	}

	hasher, err := auth.NewPasswordHasher(auth.PasswordParams{
		Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := auth.NewService(auth.NewPostgresStore(pool), hasher, 15*time.Minute, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	chatService, err := chat.NewService(chat.NewPostgresStore(pool))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(service, chatService, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), "test"))
	defer server.Close()

	registerBody := protocolgo.RegisterPasswordRequest{
		Email: "Alice@Example.com", Password: "correct horse battery staple", DisplayName: "Alice",
	}
	registerResponse := requestJSON[protocolgo.SessionTokenResponse](t, server.Client(), http.MethodPost, server.URL+"/api/v1/auth/password/register", registerBody, "", http.StatusCreated)
	if registerResponse.TokenType != protocolgo.Bearer || registerResponse.AccessToken == "" || registerResponse.RefreshToken == "" {
		t.Fatal("registration did not return an Aster session")
	}

	user := requestJSON[protocolgo.UserSelf](t, server.Client(), http.MethodGet, server.URL+"/api/v1/users/@me", nil, registerResponse.AccessToken, http.StatusOK)
	if string(user.Email) != "Alice@Example.com" || len(user.AuthenticationMethods) != 1 || user.AuthenticationMethods[0] != protocolgo.AuthenticationMethodPassword {
		t.Fatalf("unexpected current user: %+v", user)
	}

	requestJSON[protocolgo.Error](t, server.Client(), http.MethodPost, server.URL+"/api/v1/auth/password/login", protocolgo.LoginPasswordRequest{
		Email: "alice@example.com", Password: "this password is incorrect",
	}, "", http.StatusUnauthorized)

	loginResponse := requestJSON[protocolgo.SessionTokenResponse](t, server.Client(), http.MethodPost, server.URL+"/api/v1/auth/password/login", protocolgo.LoginPasswordRequest{
		Email: "alice@example.com", Password: "correct horse battery staple",
	}, "", http.StatusOK)
	refreshResponse := requestJSON[protocolgo.SessionTokenResponse](t, server.Client(), http.MethodPost, server.URL+"/api/v1/auth/token/refresh", protocolgo.RefreshSessionRequest{
		RefreshToken: loginResponse.RefreshToken,
	}, "", http.StatusOK)
	if refreshResponse.SessionId != loginResponse.SessionId || refreshResponse.RefreshToken == loginResponse.RefreshToken {
		t.Fatal("refresh must rotate the token while retaining the session ID")
	}

	requestJSON[protocolgo.Error](t, server.Client(), http.MethodGet, server.URL+"/api/v1/users/@me", nil, loginResponse.AccessToken, http.StatusUnauthorized)
	requestJSON[protocolgo.Error](t, server.Client(), http.MethodPost, server.URL+"/api/v1/auth/token/refresh", protocolgo.RefreshSessionRequest{
		RefreshToken: loginResponse.RefreshToken,
	}, "", http.StatusUnauthorized)
	requestJSON[protocolgo.Error](t, server.Client(), http.MethodGet, server.URL+"/api/v1/users/@me", nil, refreshResponse.AccessToken, http.StatusUnauthorized)

	requestJSON[struct{}](t, server.Client(), http.MethodPost, server.URL+"/api/v1/auth/logout", protocolgo.LogoutRequest{
		RefreshToken: registerResponse.RefreshToken,
	}, registerResponse.AccessToken, http.StatusNoContent)
	requestJSON[protocolgo.Error](t, server.Client(), http.MethodGet, server.URL+"/api/v1/users/@me", nil, registerResponse.AccessToken, http.StatusUnauthorized)
}

func requestJSON[Response any](t *testing.T, client *http.Client, method, url string, body any, accessToken string, expectedStatus int) Response {
	t.Helper()
	var encoded io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		encoded = bytes.NewReader(payload)
	}
	request, err := http.NewRequest(method, url, encoded)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if accessToken != "" {
		request.Header.Set("Authorization", "Bearer "+accessToken)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != expectedStatus {
		t.Fatalf("%s %s returned %d, expected %d: %s", method, url, response.StatusCode, expectedStatus, payload)
	}
	var result Response
	if response.StatusCode != http.StatusNoContent {
		if err := json.Unmarshal(payload, &result); err != nil {
			t.Fatalf("decode response: %v: %s", err, payload)
		}
	}
	return result
}
