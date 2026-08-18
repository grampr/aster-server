package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	protocolgo "github.com/grampr/Aster-protocol/packages/protocol-go/generated"
	"github.com/grampr/aster-server/internal/auth"
	"github.com/grampr/aster-server/internal/httpapi"
	postgresplatform "github.com/grampr/aster-server/internal/platform/postgres"
	"github.com/grampr/aster-server/migrations"
	"golang.org/x/oauth2"
)

type fakeGoogleProvider struct {
	oauthState       string
	nonce            string
	providerVerifier string
	identities       map[string]auth.GoogleIdentity
}

func (p *fakeGoogleProvider) AuthorizationURL(oauthState, nonce, codeVerifier string) string {
	p.oauthState, p.nonce, p.providerVerifier = oauthState, nonce, codeVerifier
	query := url.Values{"state": {oauthState}, "nonce": {nonce}}
	return "https://accounts.google.test/authorize?" + query.Encode()
}

func (p *fakeGoogleProvider) VerifyAuthorization(_ context.Context, code, codeVerifier, nonce string) (auth.GoogleIdentity, error) {
	if codeVerifier != p.providerVerifier || nonce != p.nonce {
		return auth.GoogleIdentity{}, auth.ErrInvalidOAuthCallback
	}
	identity, ok := p.identities[code]
	if !ok {
		return auth.GoogleIdentity{}, auth.ErrInvalidOAuthCallback
	}
	return identity, nil
}

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
	if _, err := pool.Exec(ctx, `TRUNCATE google_exchange_grants, google_login_attempts, session_refresh_tokens, sessions, auth_identities, users CASCADE`); err != nil {
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
	googleProvider := &fakeGoogleProvider{identities: map[string]auth.GoogleIdentity{
		"bob-code": {
			Subject: "google-bob", Email: "bob@example.com", EmailVerified: true, DisplayName: "Bob",
		},
		"bob-second-code": {
			Subject: "google-bob", Email: "bob@example.com", EmailVerified: true, DisplayName: "Bob",
		},
		"alice-code": {
			Subject: "google-alice", Email: "alice@example.com", EmailVerified: true, DisplayName: "Alice Google",
		},
	}}
	if err := service.EnableGoogle(googleProvider, 5*time.Minute, 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(service, slog.New(slog.NewTextHandler(io.Discard, nil)), "test"))
	defer server.Close()
	client := server.Client()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

	registerBody := protocolgo.RegisterPasswordRequest{
		Email: "Alice@Example.com", Password: "correct horse battery staple", DisplayName: "Alice",
	}
	registerResponse := requestJSON[protocolgo.SessionTokenResponse](t, client, http.MethodPost, server.URL+"/api/v1/auth/password/register", registerBody, "", http.StatusCreated)
	if registerResponse.TokenType != protocolgo.Bearer || registerResponse.AccessToken == "" || registerResponse.RefreshToken == "" {
		t.Fatal("registration did not return an Aster session")
	}

	user := requestJSON[protocolgo.UserSelf](t, client, http.MethodGet, server.URL+"/api/v1/users/@me", nil, registerResponse.AccessToken, http.StatusOK)
	if string(user.Email) != "Alice@Example.com" || len(user.AuthenticationMethods) != 1 || user.AuthenticationMethods[0] != protocolgo.AuthenticationMethodPassword {
		t.Fatalf("unexpected current user: %+v", user)
	}

	requestJSON[protocolgo.Error](t, client, http.MethodPost, server.URL+"/api/v1/auth/password/login", protocolgo.LoginPasswordRequest{
		Email: "alice@example.com", Password: "this password is incorrect",
	}, "", http.StatusUnauthorized)

	loginResponse := requestJSON[protocolgo.SessionTokenResponse](t, client, http.MethodPost, server.URL+"/api/v1/auth/password/login", protocolgo.LoginPasswordRequest{
		Email: "alice@example.com", Password: "correct horse battery staple",
	}, "", http.StatusOK)
	refreshResponse := requestJSON[protocolgo.SessionTokenResponse](t, client, http.MethodPost, server.URL+"/api/v1/auth/token/refresh", protocolgo.RefreshSessionRequest{
		RefreshToken: loginResponse.RefreshToken,
	}, "", http.StatusOK)
	if refreshResponse.SessionId != loginResponse.SessionId || refreshResponse.RefreshToken == loginResponse.RefreshToken {
		t.Fatal("refresh must rotate the token while retaining the session ID")
	}

	requestJSON[protocolgo.Error](t, client, http.MethodGet, server.URL+"/api/v1/users/@me", nil, loginResponse.AccessToken, http.StatusUnauthorized)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, server.URL+"/api/v1/auth/token/refresh", protocolgo.RefreshSessionRequest{
		RefreshToken: loginResponse.RefreshToken,
	}, "", http.StatusUnauthorized)
	requestJSON[protocolgo.Error](t, client, http.MethodGet, server.URL+"/api/v1/users/@me", nil, refreshResponse.AccessToken, http.StatusUnauthorized)

	requestJSON[struct{}](t, client, http.MethodPost, server.URL+"/api/v1/auth/logout", protocolgo.LogoutRequest{
		RefreshToken: registerResponse.RefreshToken,
	}, registerResponse.AccessToken, http.StatusNoContent)
	requestJSON[protocolgo.Error](t, client, http.MethodGet, server.URL+"/api/v1/users/@me", nil, registerResponse.AccessToken, http.StatusUnauthorized)

	beginGoogle := func(verifier, clientState string) string {
		t.Helper()
		authorization := requestJSON[protocolgo.GoogleAuthorizationResponse](t, client, http.MethodPost, server.URL+"/api/v1/auth/google/authorize", protocolgo.GoogleAuthorizationRequest{
			RedirectUri: protocolgo.Asterauthcallback, CodeChallengeMethod: protocolgo.S256,
			CodeChallenge: protocolgo.PkceCodeChallenge(oauth2.S256ChallengeFromVerifier(verifier)),
			ClientState:   protocolgo.OAuthClientState(clientState),
		}, "", http.StatusOK)
		authorizationURL, err := url.Parse(authorization.AuthorizationUrl)
		if err != nil {
			t.Fatal(err)
		}
		return authorizationURL.Query().Get("state")
	}
	completeGoogle := func(oauthState, code string) *url.URL {
		t.Helper()
		response, err := client.Get(server.URL + "/api/v1/auth/google/callback?state=" + url.QueryEscape(oauthState) + "&code=" + url.QueryEscape(code))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusFound {
			payload, _ := io.ReadAll(response.Body)
			t.Fatalf("google callback returned %d: %s", response.StatusCode, payload)
		}
		location, err := url.Parse(response.Header.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		return location
	}

	bobVerifier := strings.Repeat("v", 43)
	bobState := strings.Repeat("s", 43)
	bobCallback := completeGoogle(beginGoogle(bobVerifier, bobState), "bob-code")
	if bobCallback.Scheme != "aster" || bobCallback.Query().Get("state") != bobState || bobCallback.Query().Get("code") == "" {
		t.Fatalf("unexpected Google callback: %s", bobCallback)
	}
	replayedCallback, err := client.Get(server.URL + "/api/v1/auth/google/callback?state=" + url.QueryEscape(googleProvider.oauthState) + "&code=bob-code")
	if err != nil {
		t.Fatal(err)
	}
	_ = replayedCallback.Body.Close()
	if replayedCallback.StatusCode != http.StatusBadRequest {
		t.Fatalf("replayed Google callback returned %d", replayedCallback.StatusCode)
	}
	bobExchange := protocolgo.GoogleExchangeRequest{
		ExchangeCode: protocolgo.AsterExchangeCode(bobCallback.Query().Get("code")),
		CodeVerifier: protocolgo.PkceCodeVerifier(bobVerifier),
	}
	bobSession := requestJSON[protocolgo.SessionTokenResponse](t, client, http.MethodPost, server.URL+"/api/v1/auth/google/exchange", bobExchange, "", http.StatusOK)
	var transientRows int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM google_login_attempts) + (SELECT count(*) FROM google_exchange_grants)`).Scan(&transientRows); err != nil {
		t.Fatal(err)
	}
	if transientRows != 0 {
		t.Fatalf("consumed Google authentication data was not deleted: %d rows", transientRows)
	}
	bobUser := requestJSON[protocolgo.UserSelf](t, client, http.MethodGet, server.URL+"/api/v1/users/@me", nil, bobSession.AccessToken, http.StatusOK)
	if string(bobUser.Email) != "bob@example.com" || len(bobUser.AuthenticationMethods) != 1 || bobUser.AuthenticationMethods[0] != protocolgo.AuthenticationMethodGoogle {
		t.Fatalf("unexpected Google user: %+v", bobUser)
	}
	requestJSON[protocolgo.Error](t, client, http.MethodPost, server.URL+"/api/v1/auth/google/exchange", bobExchange, "", http.StatusBadRequest)

	secondVerifier := strings.Repeat("w", 43)
	secondCallback := completeGoogle(beginGoogle(secondVerifier, strings.Repeat("t", 43)), "bob-second-code")
	secondExchangeCode := protocolgo.AsterExchangeCode(secondCallback.Query().Get("code"))
	requestJSON[protocolgo.Error](t, client, http.MethodPost, server.URL+"/api/v1/auth/google/exchange", protocolgo.GoogleExchangeRequest{
		ExchangeCode: secondExchangeCode, CodeVerifier: protocolgo.PkceCodeVerifier(strings.Repeat("x", 43)),
	}, "", http.StatusBadRequest)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, server.URL+"/api/v1/auth/google/exchange", protocolgo.GoogleExchangeRequest{
		ExchangeCode: secondExchangeCode, CodeVerifier: protocolgo.PkceCodeVerifier(secondVerifier),
	}, "", http.StatusBadRequest)

	aliceVerifier := strings.Repeat("a", 43)
	aliceCallback := completeGoogle(beginGoogle(aliceVerifier, strings.Repeat("u", 43)), "alice-code")
	aliceExchange := protocolgo.GoogleExchangeRequest{
		ExchangeCode: protocolgo.AsterExchangeCode(aliceCallback.Query().Get("code")),
		CodeVerifier: protocolgo.PkceCodeVerifier(aliceVerifier),
	}
	requestJSON[protocolgo.Error](t, client, http.MethodPost, server.URL+"/api/v1/auth/google/exchange", aliceExchange, "", http.StatusConflict)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, server.URL+"/api/v1/auth/google/exchange", aliceExchange, "", http.StatusBadRequest)
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
