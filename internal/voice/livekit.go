package voice

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

const ProviderLiveKit = "livekit"

// LiveKitConfig configures a LiveKit server. Tokens are signed locally, so issuing a
// Session needs no network call.
type LiveKitConfig struct {
	// URL is the WebSocket URL Clients connect to (ws or wss).
	URL string
	// APIURL is the HTTP URL the Server uses for room administration. It defaults to URL
	// with ws/wss replaced by http/https.
	APIURL    string
	APIKey    string
	APISecret string
}

type LiveKit struct {
	config LiveKitConfig
	client *http.Client
	now    func() time.Time
}

func NewLiveKit(config LiveKitConfig) (*LiveKit, error) {
	if config.APIKey == "" || config.APISecret == "" {
		return nil, errors.New("livekit API key and secret are required")
	}
	endpoint, err := url.Parse(config.URL)
	if err != nil || (endpoint.Scheme != "ws" && endpoint.Scheme != "wss") || endpoint.Host == "" {
		return nil, errors.New("livekit URL must use ws or wss")
	}
	if config.APIURL == "" {
		scheme := map[string]string{"ws": "http", "wss": "https"}[endpoint.Scheme]
		config.APIURL = scheme + "://" + endpoint.Host
	}
	apiURL, err := url.Parse(config.APIURL)
	if err != nil || (apiURL.Scheme != "http" && apiURL.Scheme != "https") || apiURL.Host == "" {
		return nil, errors.New("livekit API URL must use http or https")
	}
	config.APIURL = strings.TrimRight(config.APIURL, "/")
	return &LiveKit{config: config, client: &http.Client{Timeout: 5 * time.Second}, now: time.Now}, nil
}

func (l *LiveKit) IssueSession(_ context.Context, request SessionRequest) (ProviderSession, error) {
	now := l.now().UTC()
	expiresAt := now.Add(request.TTL)
	sources := []string{"microphone"}
	if request.CanStream {
		sources = append(sources, "camera", "screen_share", "screen_share_audio")
	}
	token, err := l.sign(map[string]any{
		"iss": l.config.APIKey, "sub": request.Identity.String(), "name": request.DisplayName,
		"nbf": now.Unix(), "exp": expiresAt.Unix(),
		"video": map[string]any{
			"room": request.Room.String(), "roomJoin": true, "canSubscribe": true,
			"canPublish": request.CanSpeak, "canPublishSources": sources, "canPublishData": false,
		},
	})
	if err != nil {
		return ProviderSession{}, err
	}
	return ProviderSession{Provider: ProviderLiveKit, Endpoint: l.config.URL, Credential: token, ExpiresAt: expiresAt}, nil
}

// CloseSession removes the participant through LiveKit's RoomService.
func (l *LiveKit) CloseSession(ctx context.Context, room, identity uuid.UUID) error {
	now := l.now().UTC()
	token, err := l.sign(map[string]any{
		"iss": l.config.APIKey, "nbf": now.Unix(), "exp": now.Add(time.Minute).Unix(),
		"video": map[string]any{"roomAdmin": true, "room": room.String()},
	})
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{"room": room.String(), "identity": identity.String()})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, l.config.APIURL+"/twirp/livekit.RoomService/RemoveParticipant", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := l.client.Do(request)
	if err != nil {
		return fmt.Errorf("remove livekit participant: %w", err)
	}
	defer response.Body.Close()
	// LiveKit answers 404 when the room or participant is already gone.
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNotFound {
		return fmt.Errorf("remove livekit participant: unexpected status %d", response.StatusCode)
	}
	return nil
}

func (l *LiveKit) sign(claims map[string]any) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	return signHS256([]byte(`{"alg":"HS256","typ":"JWT"}`), payload, []byte(l.config.APISecret)), nil
}

// signHS256 returns a compact JWS using HMAC-SHA256.
func signHS256(header, payload, secret []byte) string {
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
