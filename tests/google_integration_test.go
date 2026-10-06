package tests

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	protocolgo "github.com/grampr/Aster-protocol/packages/protocol-go/generated"
	"github.com/grampr/aster-server/internal/auth"
	"github.com/grampr/aster-server/internal/chat"
	"github.com/grampr/aster-server/internal/httpapi"
	postgresplatform "github.com/grampr/aster-server/internal/platform/postgres"
	"github.com/grampr/aster-server/migrations"
)

// fakeGoogleProvider serves a JWKS and a token endpoint like Google's.
type fakeGoogleProvider struct {
	key *rsa.PrivateKey
	mu  sync.Mutex
	// codes maps an authorization code to the claims its ID token carries; "fail" codes error out.
	codes map[string]map[string]any
	nonce string
}

func (f *fakeGoogleProvider) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/certs", func(writer http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(writer).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "alg": "RS256", "kid": "test-key",
			"n": base64.RawURLEncoding.EncodeToString(f.key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(f.key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", func(writer http.ResponseWriter, request *http.Request) {
		_ = request.ParseForm()
		f.mu.Lock()
		claims, ok := f.codes[request.PostForm.Get("code")]
		nonce := f.nonce
		f.mu.Unlock()
		if request.PostForm.Get("client_secret") != "client-secret" || request.PostForm.Get("grant_type") != "authorization_code" || !ok || claims == nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		claimsCopy := map[string]any{"iss": "https://accounts.google.com", "aud": "client-id", "exp": time.Now().Add(time.Hour).Unix(), "nonce": nonce}
		for key, value := range claims {
			claimsCopy[key] = value
		}
		_ = json.NewEncoder(writer).Encode(map[string]string{"id_token": f.sign(claimsCopy)})
	})
	return mux
}

func (f *fakeGoogleProvider) sign(claims map[string]any) string {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "test-key"})
	payload, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(input))
	signature, _ := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	return input + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func TestGoogleLogin(t *testing.T) {
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
	if _, err := pool.Exec(ctx, `TRUNCATE messages, channels, guild_members, guilds, session_refresh_tokens, sessions, auth_identities, users, oauth_logins CASCADE`); err != nil {
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
	authService.WithGoogle(googleClient)
	chatService, err := chat.NewService(chat.NewPostgresStore(pool))
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := httptest.NewServer(httpapi.New(authService, chatService, nil, logger, "test"))
	defer server.Close()
	client := server.Client()
	base := server.URL + "/api/v1"
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	clientState := "yxE4J7vB63qQj8VWfKE7i3wmMl7E2kY5gD0hT2uS9_A"

	// begin starts an attempt and returns the OAuth state that Google would echo back.
	begin := func() (state string) {
		t.Helper()
		response := requestJSON[protocolgo.GoogleAuthorizationResponse](t, client, http.MethodPost, base+"/auth/google/authorize", map[string]any{
			"redirect_uri": "aster://auth/callback", "code_challenge": challenge, "code_challenge_method": "S256", "client_state": clientState,
		}, "", http.StatusOK)
		authorization, err := url.Parse(response.AuthorizationUrl)
		if err != nil {
			t.Fatal(err)
		}
		query := authorization.Query()
		if authorization.Host != "accounts.google.com" || query.Get("client_id") != "client-id" || query.Get("response_type") != "code" ||
			query.Get("redirect_uri") != "https://aster.example/api/v1/auth/google/callback" || !strings.Contains(query.Get("scope"), "openid") ||
			len(query.Get("state")) < 32 || len(query.Get("nonce")) < 32 || query.Get("state") == clientState || response.ExpiresIn != 300 {
			t.Fatalf("unexpected authorization URL: %s", response.AuthorizationUrl)
		}
		provider.mu.Lock()
		provider.nonce = query.Get("nonce")
		provider.mu.Unlock()
		return query.Get("state")
	}
	callback := func(query url.Values) *http.Response {
		t.Helper()
		response, err := noRedirect.Get(base + "/auth/google/callback?" + query.Encode())
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response
	}
	redirectTo := func(response *http.Response) url.Values {
		t.Helper()
		if response.StatusCode != http.StatusFound {
			t.Fatalf("expected a redirect, got %d", response.StatusCode)
		}
		location, err := url.Parse(response.Header.Get("Location"))
		if err != nil || location.Scheme != "aster" || location.Host != "auth" || location.Path != "/callback" {
			t.Fatalf("unexpected redirect: %s", response.Header.Get("Location"))
		}
		return location.Query()
	}
	setCode := func(code string, claims map[string]any) {
		provider.mu.Lock()
		defer provider.mu.Unlock()
		provider.codes[code] = claims
	}

	// Request validation: only the Desktop Deep Link and S256 are allowed.
	requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/auth/google/authorize", map[string]any{
		"redirect_uri": "https://evil.example/cb", "code_challenge": challenge, "code_challenge_method": "S256", "client_state": clientState,
	}, "", http.StatusBadRequest)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/auth/google/authorize", map[string]any{
		"redirect_uri": "aster://auth/callback", "code_challenge": challenge, "code_challenge_method": "plain", "client_state": clientState,
	}, "", http.StatusBadRequest)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/auth/google/authorize", map[string]any{
		"redirect_uri": "aster://auth/callback", "code_challenge": "short", "code_challenge_method": "S256", "client_state": clientState,
	}, "", http.StatusBadRequest)

	// A first login creates a verified Google-only User; the deep link carries no Google token.
	setCode("google-code-1", map[string]any{"sub": "google-subject-1", "email": "Taro@Example.com", "email_verified": true, "name": "山田 太郎", "picture": "https://example.com/taro.png"})
	state := begin()
	granted := redirectTo(callback(url.Values{"state": {state}, "code": {"google-code-1"}}))
	exchangeCode := granted.Get("code")
	if granted.Get("state") != clientState || len(exchangeCode) < 32 || granted.Get("error") != "" || strings.Contains(granted.Encode(), "google-code-1") {
		t.Fatalf("unexpected deep link: %v", granted)
	}
	// The state works once; an unknown or malformed one is rejected without a redirect.
	if replay := callback(url.Values{"state": {state}, "code": {"google-code-1"}}); replay.StatusCode != http.StatusBadRequest || replay.Header.Get("Location") != "" {
		t.Fatalf("a replayed state must be rejected: %d", replay.StatusCode)
	}
	if unknown := callback(url.Values{"state": {strings.Repeat("x", 43)}, "code": {"c"}}); unknown.StatusCode != http.StatusBadRequest {
		t.Fatalf("an unknown state must be rejected: %d", unknown.StatusCode)
	}
	if missing := callback(url.Values{"code": {"c"}}); missing.StatusCode != http.StatusBadRequest {
		t.Fatalf("a missing state must be rejected: %d", missing.StatusCode)
	}

	// A wrong PKCE verifier fails and still spends the code.
	exchange := func(code, codeVerifier string) *http.Response {
		t.Helper()
		payload, _ := json.Marshal(map[string]string{"exchange_code": code, "code_verifier": codeVerifier})
		response, err := client.Post(base+"/auth/google/exchange", "application/json", strings.NewReader(string(payload)))
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	wrong := exchange(exchangeCode, strings.Repeat("a", 43))
	wrong.Body.Close()
	if wrong.StatusCode != http.StatusBadRequest {
		t.Fatalf("a wrong verifier must be rejected: %d", wrong.StatusCode)
	}
	spent := requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/auth/google/exchange", map[string]any{"exchange_code": exchangeCode, "code_verifier": verifier}, "", http.StatusBadRequest)
	if spent.Code != "INVALID_AUTHORIZATION_GRANT" {
		t.Fatalf("a spent code must be rejected: %+v", spent)
	}
	requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/auth/google/exchange", map[string]any{"exchange_code": "short", "code_verifier": verifier}, "", http.StatusBadRequest)

	setCode("google-code-2", map[string]any{"sub": "google-subject-1", "email": "taro@example.com", "email_verified": true, "name": "山田 太郎"})
	login := func(code string) protocolgo.SessionTokenResponse {
		t.Helper()
		redirect := redirectTo(callback(url.Values{"state": {begin()}, "code": {code}}))
		return requestJSON[protocolgo.SessionTokenResponse](t, client, http.MethodPost, base+"/auth/google/exchange", map[string]any{"exchange_code": redirect.Get("code"), "code_verifier": verifier}, "", http.StatusOK)
	}
	session := login("google-code-2")
	user := requestJSON[protocolgo.UserSelf](t, client, http.MethodGet, base+"/users/@me", nil, session.AccessToken, http.StatusOK)
	if !strings.EqualFold(string(user.Email), "taro@example.com") || !user.EmailVerified || user.DisplayName != "山田 太郎" ||
		len(user.AuthenticationMethods) != 1 || user.AuthenticationMethods[0] != protocolgo.AuthenticationMethodGoogle {
		t.Fatalf("unexpected google user: %+v", user)
	}

	// The same Google subject signs in as the same User.
	setCode("google-code-3", map[string]any{"sub": "google-subject-1", "email": "changed@example.com", "email_verified": true, "name": "改名"})
	again := requestJSON[protocolgo.UserSelf](t, client, http.MethodGet, base+"/users/@me", nil, login("google-code-3").AccessToken, http.StatusOK)
	if again.Id != user.Id {
		t.Fatalf("the same subject must map to the same user: %+v %+v", again, user)
	}

	// A Google Identity is never auto-linked to an existing Account with the same email.
	requestJSON[protocolgo.SessionTokenResponse](t, client, http.MethodPost, base+"/auth/password/register", protocolgo.RegisterPasswordRequest{
		Email: "existing@example.com", Password: "correct horse battery staple", DisplayName: "Existing",
	}, "", http.StatusCreated)
	setCode("google-code-4", map[string]any{"sub": "google-subject-2", "email": "Existing@example.com", "email_verified": true, "name": "Impostor"})
	linkRedirect := redirectTo(callback(url.Values{"state": {begin()}, "code": {"google-code-4"}}))
	if conflict := requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/auth/google/exchange", map[string]any{"exchange_code": linkRedirect.Get("code"), "code_verifier": verifier}, "", http.StatusConflict); conflict.Code != "ACCOUNT_LINK_REQUIRED" {
		t.Fatalf("unexpected conflict: %+v", conflict)
	}
	var identities, users int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE provider = 'GOOGLE'), (SELECT count(*) FROM users) FROM auth_identities`).Scan(&identities, &users); err != nil || identities != 1 || users != 2 {
		t.Fatalf("a refused link must create nothing: google identities=%d users=%d %v", identities, users, err)
	}

	// Failures at Google come back as safe error codes with the client state, never details.
	for name, scenario := range map[string]struct {
		query url.Values
		claim map[string]any
		want  string
	}{
		"user denied":     {url.Values{"error": {"access_denied"}, "error_description": {"secret detail"}}, nil, "access_denied"},
		"google error":    {url.Values{"error": {"server_error"}}, nil, "provider_error"},
		"missing code":    {url.Values{}, nil, "provider_error"},
		"rejected code":   {url.Values{"code": {"never-issued"}}, nil, "provider_error"},
		"unverified mail": {url.Values{"code": {"google-unverified"}}, map[string]any{"sub": "s", "email": "u@example.com", "email_verified": false}, "provider_error"},
		"wrong nonce":     {url.Values{"code": {"google-bad-nonce"}}, map[string]any{"sub": "s", "email": "u@example.com", "email_verified": true, "nonce": "forged"}, "provider_error"},
		"wrong audience":  {url.Values{"code": {"google-bad-aud"}}, map[string]any{"sub": "s", "email": "u@example.com", "email_verified": true, "aud": "someone-else"}, "provider_error"},
	} {
		if scenario.claim != nil {
			setCode(scenario.query.Get("code"), scenario.claim)
		}
		query := scenario.query
		query.Set("state", begin())
		failure := redirectTo(callback(query))
		if failure.Get("error") != scenario.want || failure.Get("state") != clientState || failure.Get("code") != "" || strings.Contains(failure.Encode(), "secret") {
			t.Fatalf("%s: unexpected failure redirect: %v", name, failure)
		}
	}

	// An attempt that expired can no longer complete.
	expiredState := begin()
	if _, err := pool.Exec(ctx, `UPDATE oauth_logins SET expires_at = now() - interval '1 second' WHERE callback_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	if expired := callback(url.Values{"state": {expiredState}, "code": {"google-code-2"}}); expired.StatusCode != http.StatusBadRequest {
		t.Fatalf("an expired attempt must be rejected: %d", expired.StatusCode)
	}

	// Without a Google client the endpoints report themselves unavailable.
	bareAuth, err := auth.NewService(auth.NewPostgresStore(pool), hasher, 15*time.Minute, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	bare := httptest.NewServer(httpapi.New(bareAuth, chatService, nil, logger, "test"))
	defer bare.Close()
	if unavailable := requestJSON[protocolgo.Error](t, bare.Client(), http.MethodPost, bare.URL+"/api/v1/auth/google/authorize", map[string]any{
		"redirect_uri": "aster://auth/callback", "code_challenge": challenge, "code_challenge_method": "S256", "client_state": clientState,
	}, "", http.StatusServiceUnavailable); unavailable.Code != "GOOGLE_UNAVAILABLE" {
		t.Fatalf("unexpected error: %+v", unavailable)
	}
}
