package tests

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestChatLifecycle(t *testing.T) {
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

	hasher, err := auth.NewPasswordHasher(auth.PasswordParams{
		Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
	})
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
	server := httptest.NewServer(httpapi.New(authService, chatService, slog.New(slog.NewTextHandler(io.Discard, nil)), "test"))
	defer server.Close()

	aliceSession := registerTestUser(t, server, "alice@example.com", "Alice")
	bobSession := registerTestUser(t, server, "bob@example.com", "Bob")
	alice := requestJSON[protocolgo.UserSelf](t, server.Client(), http.MethodGet, server.URL+"/api/v1/users/@me", nil, aliceSession.AccessToken, http.StatusOK)
	bob := requestJSON[protocolgo.UserSelf](t, server.Client(), http.MethodGet, server.URL+"/api/v1/users/@me", nil, bobSession.AccessToken, http.StatusOK)

	guild := requestJSON[protocolgo.Guild](t, server.Client(), http.MethodPost, server.URL+"/api/v1/guilds", protocolgo.CreateGuildRequest{
		Name: "星屑コミュニティ", Description: stringPointer("イベント企画"),
	}, aliceSession.AccessToken, http.StatusCreated)
	if guild.OwnerId != alice.Id {
		t.Fatalf("creator must own the guild: %+v", guild)
	}
	requestJSON[protocolgo.Error](t, server.Client(), http.MethodGet, server.URL+"/api/v1/guilds/"+guild.Id.String(), nil, bobSession.AccessToken, http.StatusNotFound)
	if _, err := pool.Exec(ctx, `INSERT INTO guild_members (guild_id, user_id, joined_at) VALUES ($1, $2, now())`, guild.Id, bob.Id); err != nil {
		t.Fatal(err)
	}

	textChannel := requestJSON[protocolgo.Channel](t, server.Client(), http.MethodPost, server.URL+"/api/v1/guilds/"+guild.Id.String()+"/channels", protocolgo.CreateChannelRequest{
		Type: protocolgo.TEXT, Name: "イベント企画", Topic: stringPointer("日程を相談します"),
	}, aliceSession.AccessToken, http.StatusCreated)
	voiceChannel := requestJSON[protocolgo.Channel](t, server.Client(), http.MethodPost, server.URL+"/api/v1/guilds/"+guild.Id.String()+"/channels", protocolgo.CreateChannelRequest{
		Type: protocolgo.VOICE, Name: "イベント会議",
	}, aliceSession.AccessToken, http.StatusCreated)
	if textChannel.Position != 0 || voiceChannel.Position != 1 {
		t.Fatalf("channels must receive stable positions: text=%d voice=%d", textChannel.Position, voiceChannel.Position)
	}
	requestJSON[protocolgo.Error](t, server.Client(), http.MethodPatch, server.URL+"/api/v1/channels/"+textChannel.Id.String(), map[string]any{
		"name": "変更不可",
	}, bobSession.AccessToken, http.StatusForbidden)
	requestJSON[protocolgo.Error](t, server.Client(), http.MethodPost, server.URL+"/api/v1/channels/"+voiceChannel.Id.String()+"/messages", protocolgo.CreateMessageRequest{
		Content: "voiceには送れない",
	}, bobSession.AccessToken, http.StatusNotFound)

	first := requestJSON[protocolgo.Message](t, server.Client(), http.MethodPost, server.URL+"/api/v1/channels/"+textChannel.Id.String()+"/messages", protocolgo.CreateMessageRequest{
		Content: "1つ目",
	}, bobSession.AccessToken, http.StatusCreated)
	second := requestJSON[protocolgo.Message](t, server.Client(), http.MethodPost, server.URL+"/api/v1/channels/"+textChannel.Id.String()+"/messages", protocolgo.CreateMessageRequest{
		Content: "2つ目",
	}, bobSession.AccessToken, http.StatusCreated)
	third := requestJSON[protocolgo.Message](t, server.Client(), http.MethodPost, server.URL+"/api/v1/channels/"+textChannel.Id.String()+"/messages", protocolgo.CreateMessageRequest{
		Content: "3つ目",
	}, bobSession.AccessToken, http.StatusCreated)
	if first.Author.Id != bob.Id || second.Author.DisplayName != "Bob" {
		t.Fatalf("message author snapshot is incorrect: %+v", first.Author)
	}

	page := requestJSON[protocolgo.MessageList](t, server.Client(), http.MethodGet, server.URL+"/api/v1/channels/"+textChannel.Id.String()+"/messages?limit=2", nil, bobSession.AccessToken, http.StatusOK)
	if len(page.Items) != 2 || !page.Page.HasMore || page.Page.NextCursor == nil || page.Items[0].Id != third.Id {
		t.Fatalf("unexpected first message page: %+v", page)
	}
	nextURL := server.URL + "/api/v1/channels/" + textChannel.Id.String() + "/messages?limit=2&cursor=" + url.QueryEscape(*page.Page.NextCursor)
	nextPage := requestJSON[protocolgo.MessageList](t, server.Client(), http.MethodGet, nextURL, nil, bobSession.AccessToken, http.StatusOK)
	if len(nextPage.Items) != 1 || nextPage.Page.HasMore || nextPage.Items[0].Id != first.Id {
		t.Fatalf("unexpected second message page: %+v", nextPage)
	}

	requestJSON[protocolgo.Error](t, server.Client(), http.MethodPatch, server.URL+"/api/v1/channels/"+textChannel.Id.String()+"/messages/"+first.Id.String(), protocolgo.UpdateMessageRequest{
		Content: "Aliceは編集できない",
	}, aliceSession.AccessToken, http.StatusForbidden)
	edited := requestJSON[protocolgo.Message](t, server.Client(), http.MethodPatch, server.URL+"/api/v1/channels/"+textChannel.Id.String()+"/messages/"+first.Id.String(), protocolgo.UpdateMessageRequest{
		Content: "編集済み",
	}, bobSession.AccessToken, http.StatusOK)
	if edited.EditedAt == nil || edited.Content != "編集済み" {
		t.Fatalf("message was not edited: %+v", edited)
	}

	cleared := requestJSON[protocolgo.Guild](t, server.Client(), http.MethodPatch, server.URL+"/api/v1/guilds/"+guild.Id.String(), map[string]any{
		"description": nil,
	}, aliceSession.AccessToken, http.StatusOK)
	if cleared.Description != nil {
		t.Fatalf("explicit null must clear description: %+v", cleared)
	}
	requestJSON[struct{}](t, server.Client(), http.MethodDelete, server.URL+"/api/v1/channels/"+textChannel.Id.String()+"/messages/"+second.Id.String(), nil, aliceSession.AccessToken, http.StatusNoContent)
	requestJSON[protocolgo.Error](t, server.Client(), http.MethodGet, server.URL+"/api/v1/channels/"+textChannel.Id.String()+"/messages/"+second.Id.String(), nil, bobSession.AccessToken, http.StatusNotFound)
}

func registerTestUser(t *testing.T, server *httptest.Server, email, displayName string) protocolgo.SessionTokenResponse {
	t.Helper()
	return requestJSON[protocolgo.SessionTokenResponse](t, server.Client(), http.MethodPost, server.URL+"/api/v1/auth/password/register", protocolgo.RegisterPasswordRequest{
		Email: protocolgo.Email(email), Password: "correct horse battery staple", DisplayName: displayName,
	}, "", http.StatusCreated)
}

func stringPointer(value string) *string { return &value }
