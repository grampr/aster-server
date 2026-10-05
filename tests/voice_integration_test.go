package tests

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	protocolgo "github.com/grampr/Aster-protocol/packages/protocol-go/generated"
	"github.com/grampr/aster-server/internal/auth"
	"github.com/grampr/aster-server/internal/chat"
	"github.com/grampr/aster-server/internal/gateway"
	"github.com/grampr/aster-server/internal/httpapi"
	postgresplatform "github.com/grampr/aster-server/internal/platform/postgres"
	"github.com/grampr/aster-server/internal/voice"
	"github.com/grampr/aster-server/migrations"
)

type fakeVoiceProvider struct {
	mu     sync.Mutex
	issued []voice.SessionRequest
	closed [][2]uuid.UUID
}

func (f *fakeVoiceProvider) IssueSession(_ context.Context, request voice.SessionRequest) (voice.ProviderSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issued = append(f.issued, request)
	return voice.ProviderSession{Provider: "fake", Endpoint: "wss://voice.example.com", Credential: "credential-" + request.Identity.String(), ExpiresAt: time.Now().Add(request.TTL)}, nil
}

func (f *fakeVoiceProvider) CloseSession(_ context.Context, room, identity uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, [2]uuid.UUID{room, identity})
	return nil
}

func TestVoiceChannels(t *testing.T) {
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
	if _, err := pool.Exec(ctx, `TRUNCATE messages, channels, guild_members, guilds, session_refresh_tokens, sessions, auth_identities, users CASCADE`); err != nil {
		t.Fatal(err)
	}
	hasher, err := auth.NewPasswordHasher(auth.PasswordParams{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32})
	if err != nil {
		t.Fatal(err)
	}
	authService, err := auth.NewService(auth.NewPostgresStore(pool), hasher, 15*time.Minute, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	chatService, err := chat.NewService(chat.NewPostgresStore(pool))
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	gatewayService, err := gateway.New(authService, gateway.Config{
		URL: "ws://gateway.example/gateway/v1", HeartbeatInterval: time.Second,
		IdentifyTimeout: time.Second, SessionRetention: time.Minute, EventBufferSize: 32,
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeVoiceProvider{}
	voiceService := voice.New(chatService, provider, logger)
	server := httptest.NewServer(httpapi.New(authService, chatService, gatewayService, logger, "test", httpapi.WithVoice(voiceService)))
	defer server.Close()
	client := server.Client()
	base := server.URL + "/api/v1"

	aliceSession := registerTestUser(t, server, "alice@example.com", "Alice")
	bobSession := registerTestUser(t, server, "bob@example.com", "Bob")
	carolSession := registerTestUser(t, server, "carol@example.com", "Carol")
	alice := requestJSON[protocolgo.UserSelf](t, client, http.MethodGet, base+"/users/@me", nil, aliceSession.AccessToken, http.StatusOK)
	bob := requestJSON[protocolgo.UserSelf](t, client, http.MethodGet, base+"/users/@me", nil, bobSession.AccessToken, http.StatusOK)

	bobGateway, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/gateway/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bobGateway.Close()
	readGatewayMessage(t, bobGateway)
	if err := bobGateway.WriteJSON(map[string]any{"op": 2, "d": map[string]any{"token": bobSession.AccessToken, "intents": 32}}); err != nil {
		t.Fatal(err)
	}
	readGatewayMessage(t, bobGateway)
	expectVoiceEvent := func(userID uuid.UUID, channelID *uuid.UUID) map[string]any {
		t.Helper()
		message := readGatewayMessage(t, bobGateway)
		var data map[string]any
		if err := json.Unmarshal(message.Data, &data); err != nil {
			t.Fatal(err)
		}
		gotChannel, _ := data["channel_id"].(string)
		wantChannel := ""
		if channelID != nil {
			wantChannel = channelID.String()
		}
		if message.Type != "VOICE_STATE_UPDATE" || data["user_id"] != userID.String() || gotChannel != wantChannel {
			t.Fatalf("unexpected voice event: %s %+v (want user %s channel %q)", message.Type, data, userID, wantChannel)
		}
		return data
	}

	guild := requestJSON[protocolgo.Guild](t, client, http.MethodPost, base+"/guilds", protocolgo.CreateGuildRequest{Name: "星屑コミュニティ"}, aliceSession.AccessToken, http.StatusCreated)
	guildPath := base + "/guilds/" + guild.Id.String()
	invite := requestJSON[protocolgo.Invite](t, client, http.MethodPost, guildPath+"/invites", map[string]any{}, aliceSession.AccessToken, http.StatusCreated)
	requestJSON[protocolgo.GuildMember](t, client, http.MethodPost, base+"/invites/"+invite.Code+"/accept", nil, bobSession.AccessToken, http.StatusOK)
	text := requestJSON[protocolgo.Channel](t, client, http.MethodPost, guildPath+"/channels", protocolgo.CreateChannelRequest{Type: protocolgo.CreateChannelRequestTypeTEXT, Name: "雑談"}, aliceSession.AccessToken, http.StatusCreated)
	room := requestJSON[protocolgo.Channel](t, client, http.MethodPost, guildPath+"/channels", protocolgo.CreateChannelRequest{Type: protocolgo.CreateChannelRequestTypeVOICE, Name: "通話"}, aliceSession.AccessToken, http.StatusCreated)
	second := requestJSON[protocolgo.Channel](t, client, http.MethodPost, guildPath+"/channels", protocolgo.CreateChannelRequest{Type: protocolgo.CreateChannelRequestTypeVOICE, Name: "控室"}, aliceSession.AccessToken, http.StatusCreated)
	roomPath := base + "/channels/" + room.Id.String() + "/voice"

	// Only voice channels of guilds the user belongs to can be joined.
	requestJSON[protocolgo.Error](t, client, http.MethodPost, roomPath, nil, carolSession.AccessToken, http.StatusNotFound)
	requestJSON[protocolgo.Error](t, client, http.MethodGet, roomPath, nil, carolSession.AccessToken, http.StatusNotFound)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/channels/"+text.Id.String()+"/voice", nil, aliceSession.AccessToken, http.StatusNotFound)
	requestJSON[protocolgo.Error](t, client, http.MethodGet, base+"/channels/"+text.Id.String()+"/voice", nil, aliceSession.AccessToken, http.StatusNotFound)
	requestJSON[protocolgo.Error](t, client, http.MethodPatch, base+"/voice/sessions/@me", map[string]any{"self_mute": true}, aliceSession.AccessToken, http.StatusNotFound)
	requestJSON[protocolgo.Error](t, client, http.MethodDelete, base+"/voice/sessions/@me", nil, aliceSession.AccessToken, http.StatusNotFound)

	// Joining needs CONNECT, which the default role grants.
	everyone := requestJSON[protocolgo.RoleList](t, client, http.MethodGet, guildPath+"/roles", nil, aliceSession.AccessToken, http.StatusOK).Items[0]
	requestJSON[protocolgo.Role](t, client, http.MethodPatch, guildPath+"/roles/"+everyone.Id.String(), map[string]any{"permissions": everyone.Permissions &^ (1 << 8)}, aliceSession.AccessToken, http.StatusOK)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, roomPath, nil, bobSession.AccessToken, http.StatusForbidden)
	requestJSON[protocolgo.Role](t, client, http.MethodPatch, guildPath+"/roles/"+everyone.Id.String(), map[string]any{"permissions": everyone.Permissions}, aliceSession.AccessToken, http.StatusOK)

	session := requestJSON[protocolgo.VoiceSession](t, client, http.MethodPost, roomPath, nil, aliceSession.AccessToken, http.StatusOK)
	if session.Provider != "fake" || session.Endpoint != "wss://voice.example.com" || session.Credential != "credential-"+alice.Id.String() ||
		session.State.UserId != alice.Id || session.State.ChannelId == nil || *session.State.ChannelId != room.Id ||
		session.State.SessionId == nil || *session.State.SessionId != session.Id || session.State.SelfMute || session.State.SelfDeaf {
		t.Fatalf("unexpected voice session: %+v", session)
	}
	expectVoiceEvent(alice.Id, &room.Id)
	muted := requestJSON[protocolgo.VoiceSession](t, client, http.MethodPost, roomPath, map[string]any{"self_mute": true, "self_deaf": true}, bobSession.AccessToken, http.StatusOK)
	if !muted.State.SelfMute || !muted.State.SelfDeaf {
		t.Fatalf("the join flags must be kept: %+v", muted)
	}
	expectVoiceEvent(bob.Id, &room.Id)
	provider.mu.Lock()
	issued := provider.issued
	provider.mu.Unlock()
	if len(issued) != 2 || issued[0].Room != room.Id || issued[0].Identity != alice.Id || !issued[0].CanSpeak || !issued[0].CanStream || issued[0].DisplayName != "Alice" {
		t.Fatalf("unexpected provider requests: %+v", issued)
	}
	requestJSON[protocolgo.Error](t, client, http.MethodPost, roomPath, map[string]any{"unknown": true}, aliceSession.AccessToken, http.StatusBadRequest)
	if states := requestJSON[protocolgo.VoiceStateList](t, client, http.MethodGet, roomPath, nil, bobSession.AccessToken, http.StatusOK); len(states.Items) != 2 {
		t.Fatalf("unexpected voice states: %+v", states)
	}

	// Public flags can be changed while joined; video and screen share need STREAM.
	requestJSON[protocolgo.Error](t, client, http.MethodPatch, base+"/voice/sessions/@me", map[string]any{}, aliceSession.AccessToken, http.StatusBadRequest)
	updated := requestJSON[protocolgo.VoiceState](t, client, http.MethodPatch, base+"/voice/sessions/@me", map[string]any{"self_mute": true, "self_video": true}, aliceSession.AccessToken, http.StatusOK)
	if !updated.SelfMute || !updated.SelfVideo || updated.SelfDeaf {
		t.Fatalf("unexpected updated state: %+v", updated)
	}
	if data := expectVoiceEvent(alice.Id, &room.Id); data["self_mute"] != true || data["self_video"] != true {
		t.Fatalf("unexpected event data: %+v", data)
	}
	requestJSON[protocolgo.Role](t, client, http.MethodPatch, guildPath+"/roles/"+everyone.Id.String(), map[string]any{"permissions": everyone.Permissions &^ (1 << 10)}, aliceSession.AccessToken, http.StatusOK)
	requestJSON[protocolgo.Error](t, client, http.MethodPatch, base+"/voice/sessions/@me", map[string]any{"self_stream": true}, bobSession.AccessToken, http.StatusForbidden)
	requestJSON[protocolgo.VoiceState](t, client, http.MethodPatch, base+"/voice/sessions/@me", map[string]any{"self_stream": false}, bobSession.AccessToken, http.StatusOK)
	expectVoiceEvent(bob.Id, &room.Id)

	// Joining another channel moves the user: the old room is closed and left first.
	secondPath := base + "/channels/" + second.Id.String() + "/voice"
	moved := requestJSON[protocolgo.VoiceSession](t, client, http.MethodPost, secondPath, nil, aliceSession.AccessToken, http.StatusOK)
	if moved.Id == session.Id || moved.State.ChannelId == nil || *moved.State.ChannelId != second.Id {
		t.Fatalf("unexpected moved session: %+v", moved)
	}
	expectVoiceEvent(alice.Id, nil)
	expectVoiceEvent(alice.Id, &second.Id)
	provider.mu.Lock()
	closed := provider.closed
	provider.mu.Unlock()
	if len(closed) != 1 || closed[0] != [2]uuid.UUID{room.Id, alice.Id} {
		t.Fatalf("the previous room must be closed: %+v", closed)
	}
	if states := requestJSON[protocolgo.VoiceStateList](t, client, http.MethodGet, roomPath, nil, aliceSession.AccessToken, http.StatusOK); len(states.Items) != 1 || states.Items[0].UserId != bob.Id {
		t.Fatalf("only bob must remain in the first room: %+v", states)
	}

	// Leaving, and losing access, both remove the user and tell the guild.
	requestJSON[struct{}](t, client, http.MethodDelete, base+"/voice/sessions/@me", nil, aliceSession.AccessToken, http.StatusNoContent)
	left := expectVoiceEvent(alice.Id, nil)
	if left["session_id"] != nil || left["self_mute"] != false {
		t.Fatalf("a left state must be cleared: %+v", left)
	}
	requestJSON[protocolgo.Error](t, client, http.MethodDelete, base+"/voice/sessions/@me", nil, aliceSession.AccessToken, http.StatusNotFound)
	requestJSON[struct{}](t, client, http.MethodDelete, guildPath+"/members/"+bob.Id.String(), nil, aliceSession.AccessToken, http.StatusNoContent)
	requestJSON[protocolgo.Error](t, client, http.MethodPatch, base+"/voice/sessions/@me", map[string]any{"self_mute": false}, bobSession.AccessToken, http.StatusNotFound)
	if states := requestJSON[protocolgo.VoiceStateList](t, client, http.MethodGet, roomPath, nil, aliceSession.AccessToken, http.StatusOK); len(states.Items) != 0 {
		t.Fatalf("a removed member must be evicted from voice: %+v", states)
	}

	// Without a provider, voice reports itself unavailable.
	bare := httptest.NewServer(httpapi.New(authService, chatService, nil, logger, "test"))
	defer bare.Close()
	if unavailable := requestJSON[protocolgo.Error](t, bare.Client(), http.MethodPost, bare.URL+"/api/v1/channels/"+room.Id.String()+"/voice", nil, aliceSession.AccessToken, http.StatusServiceUnavailable); unavailable.Code != "VOICE_UNAVAILABLE" {
		t.Fatalf("unexpected error: %+v", unavailable)
	}
	unconfigured := voice.New(chatService, nil, logger)
	bareVoice := httptest.NewServer(httpapi.New(authService, chatService, nil, logger, "test", httpapi.WithVoice(unconfigured)))
	defer bareVoice.Close()
	requestJSON[protocolgo.Error](t, bareVoice.Client(), http.MethodPost, bareVoice.URL+"/api/v1/channels/"+room.Id.String()+"/voice", nil, aliceSession.AccessToken, http.StatusServiceUnavailable)
}
