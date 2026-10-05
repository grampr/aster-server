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
	"github.com/grampr/aster-server/migrations"
)

func TestGuildInvitesAndMembers(t *testing.T) {
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
		IdentifyTimeout: time.Second, SessionRetention: time.Minute, EventBufferSize: 16,
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(authService, chatService, gatewayService, logger, "test"))
	defer server.Close()
	client := server.Client()
	base := server.URL + "/api/v1"

	aliceSession := registerTestUser(t, server, "alice@example.com", "Alice")
	bobSession := registerTestUser(t, server, "bob@example.com", "Bob")
	carolSession := registerTestUser(t, server, "carol@example.com", "Carol")
	daveSession := registerTestUser(t, server, "dave@example.com", "Dave")
	alice := requestJSON[protocolgo.UserSelf](t, client, http.MethodGet, base+"/users/@me", nil, aliceSession.AccessToken, http.StatusOK)
	bob := requestJSON[protocolgo.UserSelf](t, client, http.MethodGet, base+"/users/@me", nil, bobSession.AccessToken, http.StatusOK)
	carol := requestJSON[protocolgo.UserSelf](t, client, http.MethodGet, base+"/users/@me", nil, carolSession.AccessToken, http.StatusOK)

	bobGateway, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/gateway/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bobGateway.Close()
	if message := readGatewayMessage(t, bobGateway); message.Op != 10 {
		t.Fatalf("expected HELLO, got %+v", message)
	}
	if err := bobGateway.WriteJSON(map[string]any{"op": 2, "d": map[string]any{"token": bobSession.AccessToken, "intents": 2 | 64}}); err != nil {
		t.Fatal(err)
	}
	if message := readGatewayMessage(t, bobGateway); message.Type != "READY" {
		t.Fatalf("expected READY, got %+v", message)
	}

	guild := requestJSON[protocolgo.Guild](t, client, http.MethodPost, base+"/guilds", protocolgo.CreateGuildRequest{Name: "星屑コミュニティ"}, aliceSession.AccessToken, http.StatusCreated)
	guildPath := base + "/guilds/" + guild.Id.String()

	// Only the Owner manages Invites, and the limits follow the protocol.
	requestJSON[protocolgo.Error](t, client, http.MethodPost, guildPath+"/invites", map[string]any{}, daveSession.AccessToken, http.StatusNotFound)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, guildPath+"/invites", map[string]any{"expires_in": 10}, aliceSession.AccessToken, http.StatusBadRequest)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, guildPath+"/invites", map[string]any{"max_uses": 1001}, aliceSession.AccessToken, http.StatusBadRequest)
	invite := requestJSON[protocolgo.Invite](t, client, http.MethodPost, guildPath+"/invites", map[string]any{"expires_in": 3600, "max_uses": 2}, aliceSession.AccessToken, http.StatusCreated)
	if invite.Uses != 0 || invite.MaxUses == nil || *invite.MaxUses != 2 || invite.ExpiresAt == nil || invite.Inviter.Id != alice.Id || invite.Guild.Id != guild.Id || len(invite.Code) < 16 {
		t.Fatalf("unexpected invite: %+v", invite)
	}
	invitePath := base + "/invites/" + invite.Code
	requestJSON[protocolgo.Error](t, client, http.MethodGet, base+"/invites/short", nil, daveSession.AccessToken, http.StatusNotFound)
	if preview := requestJSON[protocolgo.Invite](t, client, http.MethodGet, invitePath, nil, daveSession.AccessToken, http.StatusOK); preview.Guild.Name != guild.Name {
		t.Fatalf("unexpected invite preview: %+v", preview)
	}

	// Accepting joins the Guild once; a retry returns the same Member without using the Invite again.
	joined := requestJSON[protocolgo.GuildMember](t, client, http.MethodPost, invitePath+"/accept", nil, bobSession.AccessToken, http.StatusOK)
	if joined.User.Id != bob.Id || joined.GuildId != guild.Id || joined.Presence.Status != protocolgo.PresenceStatusOFFLINE || len(joined.RoleIds) != 0 {
		t.Fatalf("unexpected joined member: %+v", joined)
	}
	assertMemberEvent(t, readGatewayMessage(t, bobGateway), "MEMBER_JOIN", guild.Id, bob.Id, nil)
	retry := requestJSON[protocolgo.GuildMember](t, client, http.MethodPost, invitePath+"/accept", nil, bobSession.AccessToken, http.StatusOK)
	if !retry.JoinedAt.Equal(joined.JoinedAt) {
		t.Fatalf("retry must return the existing member: %+v", retry)
	}
	requestJSON[protocolgo.GuildMember](t, client, http.MethodPost, invitePath+"/accept", nil, carolSession.AccessToken, http.StatusOK)
	assertMemberEvent(t, readGatewayMessage(t, bobGateway), "MEMBER_JOIN", guild.Id, carol.Id, nil)

	// The Invite is used up: new users are refused, and it is no longer listed or previewable.
	if used := requestJSON[protocolgo.InviteList](t, client, http.MethodGet, guildPath+"/invites", nil, aliceSession.AccessToken, http.StatusOK); len(used.Items) != 0 {
		t.Fatalf("exhausted invite must not be listed: %+v", used)
	}
	requestJSON[protocolgo.Error](t, client, http.MethodGet, invitePath, nil, daveSession.AccessToken, http.StatusNotFound)
	if refused := requestJSON[protocolgo.Error](t, client, http.MethodPost, invitePath+"/accept", nil, daveSession.AccessToken, http.StatusConflict); refused.Code != "INVITE_UNAVAILABLE" {
		t.Fatalf("unexpected error: %+v", refused)
	}
	// An already-joined member can still replay an exhausted Invite.
	requestJSON[protocolgo.GuildMember](t, client, http.MethodPost, invitePath+"/accept", nil, carolSession.AccessToken, http.StatusOK)

	// Expired Invites are refused too.
	expiring := requestJSON[protocolgo.Invite](t, client, http.MethodPost, guildPath+"/invites", map[string]any{"expires_in": 300}, aliceSession.AccessToken, http.StatusCreated)
	if _, err := pool.Exec(ctx, `UPDATE guild_invites SET expires_at = now() - interval '1 minute' WHERE id = $1`, expiring.Id); err != nil {
		t.Fatal(err)
	}
	requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/invites/"+expiring.Code+"/accept", nil, daveSession.AccessToken, http.StatusConflict)

	// Revoked Invites behave as if they do not exist.
	open := requestJSON[protocolgo.Invite](t, client, http.MethodPost, guildPath+"/invites", map[string]any{}, aliceSession.AccessToken, http.StatusCreated)
	if open.MaxUses != nil || open.ExpiresAt != nil {
		t.Fatalf("unlimited invite must have no limits: %+v", open)
	}
	requestJSON[protocolgo.Error](t, client, http.MethodGet, guildPath+"/invites", nil, bobSession.AccessToken, http.StatusForbidden)
	requestJSON[struct{}](t, client, http.MethodDelete, guildPath+"/invites/"+open.Id.String(), nil, bobSession.AccessToken, http.StatusForbidden)
	requestJSON[struct{}](t, client, http.MethodDelete, guildPath+"/invites/"+open.Id.String(), nil, aliceSession.AccessToken, http.StatusNoContent)
	requestJSON[protocolgo.Error](t, client, http.MethodDelete, guildPath+"/invites/"+open.Id.String(), nil, aliceSession.AccessToken, http.StatusNotFound)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/invites/"+open.Code+"/accept", nil, daveSession.AccessToken, http.StatusNotFound)

	// Members are listed in join order with stable paging, and only to members.
	requestJSON[protocolgo.Error](t, client, http.MethodGet, guildPath+"/members", nil, daveSession.AccessToken, http.StatusNotFound)
	firstPage := requestJSON[protocolgo.GuildMemberList](t, client, http.MethodGet, guildPath+"/members?limit=2", nil, aliceSession.AccessToken, http.StatusOK)
	if len(firstPage.Items) != 2 || !firstPage.Page.HasMore || firstPage.Page.NextCursor == nil ||
		firstPage.Items[0].User.Id != alice.Id || firstPage.Items[1].User.Id != bob.Id {
		t.Fatalf("unexpected first member page: %+v", firstPage)
	}
	secondPage := requestJSON[protocolgo.GuildMemberList](t, client, http.MethodGet, guildPath+"/members?limit=2&cursor="+*firstPage.Page.NextCursor, nil, aliceSession.AccessToken, http.StatusOK)
	if len(secondPage.Items) != 1 || secondPage.Page.HasMore || secondPage.Items[0].User.Id != carol.Id {
		t.Fatalf("unexpected second member page: %+v", secondPage)
	}
	requestJSON[protocolgo.Error](t, client, http.MethodGet, guildPath+"/members?cursor=invalid", nil, aliceSession.AccessToken, http.StatusBadRequest)
	requestJSON[protocolgo.GuildMember](t, client, http.MethodGet, guildPath+"/members/"+carol.Id.String(), nil, bobSession.AccessToken, http.StatusOK)
	requestJSON[protocolgo.Error](t, client, http.MethodGet, guildPath+"/members/"+uuid.NewString(), nil, bobSession.AccessToken, http.StatusNotFound)
	requestJSON[protocolgo.Error](t, client, http.MethodGet, guildPath+"/members/"+bob.Id.String(), nil, daveSession.AccessToken, http.StatusNotFound)

	// A member changes their own nickname; the Owner can too; others cannot.
	nicknamed := requestJSON[protocolgo.GuildMember](t, client, http.MethodPatch, guildPath+"/members/"+bob.Id.String(), map[string]any{"nickname": "  ボブ  "}, bobSession.AccessToken, http.StatusOK)
	if nicknamed.Nickname == nil || *nicknamed.Nickname != "ボブ" {
		t.Fatalf("unexpected nickname: %+v", nicknamed)
	}
	assertMemberEvent(t, readGatewayMessage(t, bobGateway), "MEMBER_UPDATE", guild.Id, bob.Id, stringPointer("ボブ"))
	requestJSON[protocolgo.Error](t, client, http.MethodPatch, guildPath+"/members/"+bob.Id.String(), map[string]any{"nickname": "乗っ取り"}, carolSession.AccessToken, http.StatusForbidden)
	requestJSON[protocolgo.Error](t, client, http.MethodPatch, guildPath+"/members/"+bob.Id.String(), map[string]any{"nickname": strings.Repeat("あ", 65)}, bobSession.AccessToken, http.StatusBadRequest)
	requestJSON[protocolgo.Error](t, client, http.MethodPatch, guildPath+"/members/"+bob.Id.String(), map[string]any{"role_ids": []string{uuid.NewString()}}, aliceSession.AccessToken, http.StatusBadRequest)
	requestJSON[protocolgo.Error](t, client, http.MethodPatch, guildPath+"/members/"+bob.Id.String(), map[string]any{}, bobSession.AccessToken, http.StatusBadRequest)
	cleared := requestJSON[protocolgo.GuildMember](t, client, http.MethodPatch, guildPath+"/members/"+bob.Id.String(), map[string]any{"nickname": nil}, aliceSession.AccessToken, http.StatusOK)
	if cleared.Nickname != nil {
		t.Fatalf("nickname must be cleared: %+v", cleared)
	}
	assertMemberEvent(t, readGatewayMessage(t, bobGateway), "MEMBER_UPDATE", guild.Id, bob.Id, nil)

	// Presence is published by the user, reported on Members and pushed to the Guild.
	requestJSON[protocolgo.Error](t, client, http.MethodPut, base+"/users/@me/presence", map[string]any{"status": "OFFLINE"}, carolSession.AccessToken, http.StatusBadRequest)
	presence := requestJSON[protocolgo.Presence](t, client, http.MethodPut, base+"/users/@me/presence", map[string]any{"status": "DO_NOT_DISTURB", "custom_text": " 作業中 "}, carolSession.AccessToken, http.StatusOK)
	if presence.UserId != carol.Id || presence.Status != protocolgo.PresenceStatusDONOTDISTURB || presence.CustomText == nil || *presence.CustomText != "作業中" {
		t.Fatalf("unexpected presence: %+v", presence)
	}
	presenceEvent := readGatewayMessage(t, bobGateway)
	var presenceData struct {
		GuildID  uuid.UUID `json:"guild_id"`
		Presence struct {
			UserID uuid.UUID `json:"user_id"`
			Status string    `json:"status"`
		} `json:"presence"`
	}
	if err := json.Unmarshal(presenceEvent.Data, &presenceData); err != nil {
		t.Fatal(err)
	}
	if presenceEvent.Type != "PRESENCE_UPDATE" || presenceData.GuildID != guild.Id || presenceData.Presence.UserID != carol.Id || presenceData.Presence.Status != "DO_NOT_DISTURB" {
		t.Fatalf("unexpected presence event: %+v %+v", presenceEvent, presenceData)
	}
	if member := requestJSON[protocolgo.GuildMember](t, client, http.MethodGet, guildPath+"/members/"+carol.Id.String(), nil, aliceSession.AccessToken, http.StatusOK); member.Presence.Status != protocolgo.PresenceStatusDONOTDISTURB {
		t.Fatalf("member must report its presence: %+v", member)
	}

	// Only the Owner removes members, and the Owner can neither be removed nor leave.
	requestJSON[protocolgo.Error](t, client, http.MethodDelete, guildPath+"/members/"+carol.Id.String(), nil, bobSession.AccessToken, http.StatusForbidden)
	requestJSON[protocolgo.Error](t, client, http.MethodDelete, guildPath+"/members/"+alice.Id.String(), nil, aliceSession.AccessToken, http.StatusForbidden)
	requestJSON[protocolgo.Error](t, client, http.MethodDelete, guildPath+"/members/@me", nil, aliceSession.AccessToken, http.StatusForbidden)
	requestJSON[protocolgo.Error](t, client, http.MethodDelete, guildPath+"/members/"+uuid.NewString(), nil, aliceSession.AccessToken, http.StatusNotFound)
	requestJSON[struct{}](t, client, http.MethodDelete, guildPath+"/members/"+carol.Id.String(), nil, aliceSession.AccessToken, http.StatusNoContent)
	assertMemberLeaveEvent(t, readGatewayMessage(t, bobGateway), guild.Id, carol.Id)
	requestJSON[protocolgo.Error](t, client, http.MethodGet, guildPath, nil, carolSession.AccessToken, http.StatusNotFound)
	requestJSON[struct{}](t, client, http.MethodDelete, guildPath+"/members/@me", nil, bobSession.AccessToken, http.StatusNoContent)
	assertMemberLeaveEvent(t, readGatewayMessage(t, bobGateway), guild.Id, bob.Id)
	requestJSON[protocolgo.Error](t, client, http.MethodGet, guildPath+"/members", nil, bobSession.AccessToken, http.StatusNotFound)
	remaining := requestJSON[protocolgo.GuildMemberList](t, client, http.MethodGet, guildPath+"/members", nil, aliceSession.AccessToken, http.StatusOK)
	if len(remaining.Items) != 1 || remaining.Items[0].User.Id != alice.Id {
		t.Fatalf("only the owner should remain: %+v", remaining)
	}
}

func assertMemberEvent(t *testing.T, message integrationGatewayMessage, eventType string, guildID, userID uuid.UUID, nickname *string) {
	t.Helper()
	var data struct {
		GuildID  uuid.UUID `json:"guild_id"`
		Nickname *string   `json:"nickname"`
		RoleIDs  []string  `json:"role_ids"`
		User     struct {
			ID uuid.UUID `json:"id"`
		} `json:"user"`
		Presence struct {
			UserID uuid.UUID `json:"user_id"`
			Status string    `json:"status"`
		} `json:"presence"`
	}
	if err := json.Unmarshal(message.Data, &data); err != nil {
		t.Fatal(err)
	}
	sameNickname := (nickname == nil && data.Nickname == nil) || (nickname != nil && data.Nickname != nil && *nickname == *data.Nickname)
	if message.Type != eventType || data.GuildID != guildID || data.User.ID != userID || !sameNickname ||
		data.RoleIDs == nil || data.Presence.UserID != userID || data.Presence.Status != "OFFLINE" {
		t.Fatalf("unexpected %s event: type=%s data=%+v", eventType, message.Type, data)
	}
}

func assertMemberLeaveEvent(t *testing.T, message integrationGatewayMessage, guildID, userID uuid.UUID) {
	t.Helper()
	var data struct {
		GuildID uuid.UUID `json:"guild_id"`
		UserID  uuid.UUID `json:"user_id"`
	}
	if err := json.Unmarshal(message.Data, &data); err != nil {
		t.Fatal(err)
	}
	if message.Type != "MEMBER_LEAVE" || data.GuildID != guildID || data.UserID != userID {
		t.Fatalf("unexpected MEMBER_LEAVE event: type=%s data=%+v", message.Type, data)
	}
}
