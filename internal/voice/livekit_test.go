package voice

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func decodeToken(t *testing.T, token, secret string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("a JWT has three parts: %s", token)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil)); parts[2] != want {
		t.Fatalf("invalid signature: got %s want %s", parts[2], want)
	}
	header, _ := base64.RawURLEncoding.DecodeString(parts[0])
	if string(header) != `{"alg":"HS256","typ":"JWT"}` {
		t.Fatalf("unexpected header: %s", header)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

// RFC 7515 Appendix A.1 gives a worked HS256 example.
func TestSignHS256MatchesRFC7515(t *testing.T) {
	key, err := base64.RawURLEncoding.DecodeString("AyM1SysPpbyDfgZld3umj1qzKObwVMkoqQ-EstJQLr_T-1qS0gZH75aKtMN3Yj0iPS4hcgUuTwjAzZr1Z9CAow")
	if err != nil {
		t.Fatal(err)
	}
	header := []byte("{\"typ\":\"JWT\",\r\n \"alg\":\"HS256\"}")
	payload := []byte("{\"iss\":\"joe\",\r\n \"exp\":1300819380,\r\n \"http://example.com/is_root\":true}")
	const want = "eyJ0eXAiOiJKV1QiLA0KICJhbGciOiJIUzI1NiJ9.eyJpc3MiOiJqb2UiLA0KICJleHAiOjEzMDA4MTkzODAsDQogImh0dHA6Ly9leGFtcGxlLmNvbS9pc19yb290Ijp0cnVlfQ.dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	if got := signHS256(header, payload, key); got != want {
		t.Fatalf("unexpected JWS:\n got %s\nwant %s", got, want)
	}
}

func TestLiveKitIssuesScopedTokens(t *testing.T) {
	provider, err := NewLiveKit(LiveKitConfig{URL: "wss://voice.example.com", APIKey: "key", APISecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.October, 5, 12, 0, 0, 0, time.UTC)
	provider.now = func() time.Time { return now }
	room, identity := uuid.New(), uuid.New()

	session, err := provider.IssueSession(context.Background(), SessionRequest{Room: room, Identity: identity, DisplayName: "Alice", CanSpeak: true, TTL: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if session.Provider != "livekit" || session.Endpoint != "wss://voice.example.com" || !session.ExpiresAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("unexpected session: %+v", session)
	}
	claims := decodeToken(t, session.Credential, "secret")
	video := claims["video"].(map[string]any)
	if claims["iss"] != "key" || claims["sub"] != identity.String() || claims["name"] != "Alice" ||
		int64(claims["exp"].(float64)) != now.Add(10*time.Minute).Unix() ||
		video["room"] != room.String() || video["roomJoin"] != true || video["canPublish"] != true || video["canSubscribe"] != true {
		t.Fatalf("unexpected claims: %+v", claims)
	}
	if sources := video["canPublishSources"].([]any); len(sources) != 1 || sources[0] != "microphone" {
		t.Fatalf("a member without STREAM may only publish audio: %+v", sources)
	}

	listener, err := provider.IssueSession(context.Background(), SessionRequest{Room: room, Identity: identity, CanStream: true, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	video = decodeToken(t, listener.Credential, "secret")["video"].(map[string]any)
	if video["canPublish"] != false || len(video["canPublishSources"].([]any)) != 4 {
		t.Fatalf("unexpected listener grant: %+v", video)
	}
}

func TestLiveKitRemovesParticipantsThroughTheRoomService(t *testing.T) {
	room, identity := uuid.New(), uuid.New()
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/twirp/livekit.RoomService/RemoveParticipant" {
			t.Errorf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		claims := decodeToken(t, strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "), "secret")
		if video := claims["video"].(map[string]any); video["roomAdmin"] != true || video["room"] != room.String() {
			t.Errorf("the admin token must be scoped to the room: %+v", video)
		}
		var body map[string]string
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body["room"] != room.String() || body["identity"] != identity.String() {
			t.Errorf("unexpected body: %+v %v", body, err)
		}
		writer.WriteHeader(status)
	}))
	defer server.Close()
	provider, err := NewLiveKit(LiveKitConfig{URL: "ws://voice.example.com", APIURL: server.URL + "/", APIKey: "key", APISecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.CloseSession(context.Background(), room, identity); err != nil {
		t.Fatal(err)
	}
	status = http.StatusNotFound
	if err := provider.CloseSession(context.Background(), room, identity); err != nil {
		t.Fatalf("a participant that is already gone is not an error: %v", err)
	}
	status = http.StatusInternalServerError
	if err := provider.CloseSession(context.Background(), room, identity); err == nil {
		t.Fatal("a server error must be reported")
	}
}

func TestNewLiveKitValidatesConfiguration(t *testing.T) {
	provider, err := NewLiveKit(LiveKitConfig{URL: "wss://voice.example.com:7880", APIKey: "k", APISecret: "s"})
	if err != nil || provider.config.APIURL != "https://voice.example.com:7880" {
		t.Fatalf("the API URL must default from the WebSocket URL: %v %+v", err, provider)
	}
	for _, bad := range []LiveKitConfig{
		{URL: "http://x", APIKey: "k", APISecret: "s"},
		{URL: "ws://x", APISecret: "s"},
		{URL: "ws://x", APIKey: "k"},
		{URL: "ws://x", APIURL: "ftp://x", APIKey: "k", APISecret: "s"},
	} {
		if _, err := NewLiveKit(bad); err == nil {
			t.Fatalf("configuration %+v must be rejected", bad)
		}
	}
}
