package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/grampr/aster-server/internal/voice"
)

type RealtimeKitConfig struct {
	AccountID, AppID, APIToken                            string
	ListenerPresetName, VoicePresetName, StreamPresetName string
	APIBase, ClientEndpoint                               string
}
type RealtimeKit struct {
	config RealtimeKitConfig
	client *http.Client
}

func NewRealtimeKit(config RealtimeKitConfig) (*RealtimeKit, error) {
	if config.AccountID == "" || config.AppID == "" || config.APIToken == "" || config.ListenerPresetName == "" || config.VoicePresetName == "" || config.StreamPresetName == "" {
		return nil, errors.New("Cloudflare RealtimeKit account, app, API token, and listener/voice/stream presets are required")
	}
	if config.APIBase == "" {
		config.APIBase = "https://api.cloudflare.com/client/v4"
	}
	if config.ClientEndpoint == "" {
		config.ClientEndpoint = "https://realtime.cloudflare.com"
	}
	return &RealtimeKit{config: config, client: &http.Client{Timeout: 10 * time.Second}}, nil
}
func (r *RealtimeKit) Name() string     { return "cloudflare-realtimekit" }
func (r *RealtimeKit) Endpoint() string { return r.config.ClientEndpoint }
func (r *RealtimeKit) CreateRoom(ctx context.Context, title string) (string, error) {
	var response struct {
		Success bool `json:"success"`
		Data    *struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := r.call(ctx, http.MethodPost, "/accounts/"+url.PathEscape(r.config.AccountID)+"/realtime/kit/"+url.PathEscape(r.config.AppID)+"/meetings", map[string]string{"title": title}, &response); err != nil {
		return "", err
	}
	if !response.Success || response.Data == nil || response.Data.ID == "" {
		return "", errors.New("Cloudflare returned an invalid meeting")
	}
	return response.Data.ID, nil
}
func (r *RealtimeKit) AddParticipant(ctx context.Context, roomID, customID, name string, canSpeak, canStream bool) (voice.ProviderParticipant, error) {
	var response struct {
		Success bool `json:"success"`
		Data    *struct {
			ID    string `json:"id"`
			Token string `json:"token"`
		} `json:"data"`
	}
	preset := r.config.ListenerPresetName
	if canSpeak {
		preset = r.config.VoicePresetName
	}
	if canStream {
		preset = r.config.StreamPresetName
	}
	body := map[string]string{"custom_participant_id": customID, "preset_name": preset, "name": name}
	path := "/accounts/" + url.PathEscape(r.config.AccountID) + "/realtime/kit/" + url.PathEscape(r.config.AppID) + "/meetings/" + url.PathEscape(roomID) + "/participants"
	if err := r.call(ctx, http.MethodPost, path, body, &response); err != nil {
		return voice.ProviderParticipant{}, err
	}
	if !response.Success || response.Data == nil || response.Data.ID == "" || response.Data.Token == "" {
		return voice.ProviderParticipant{}, errors.New("Cloudflare returned an invalid participant")
	}
	return voice.ProviderParticipant{ID: response.Data.ID, Credential: response.Data.Token, ExpiresAt: time.Now().UTC().Add(time.Hour)}, nil
}
func (r *RealtimeKit) RemoveParticipant(ctx context.Context, roomID, participantID string) error {
	path := "/accounts/" + url.PathEscape(r.config.AccountID) + "/realtime/kit/" + url.PathEscape(r.config.AppID) + "/meetings/" + url.PathEscape(roomID) + "/participants/" + url.PathEscape(participantID)
	return r.call(ctx, http.MethodDelete, path, nil, nil)
}
func (r *RealtimeKit) call(ctx context.Context, method, path string, body any, destination any) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(r.config.APIBase, "/")+path, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+r.config.APIToken)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := r.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.CopyN(io.Discard, response.Body, 1<<20)
		return fmt.Errorf("Cloudflare RealtimeKit returned HTTP %d", response.StatusCode)
	}
	if destination == nil {
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode Cloudflare RealtimeKit response: %w", err)
	}
	return nil
}
