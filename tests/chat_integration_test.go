package tests

import (
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

	aliceSession := registerTestUser(t, server, "alice@example.com", "Alice")
	bobSession := registerTestUser(t, server, "bob@example.com", "Bob")
	gatewayConnection, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/gateway/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer gatewayConnection.Close()
	if message := readGatewayMessage(t, gatewayConnection); message.Op != 10 {
		t.Fatalf("expected HELLO, got %+v", message)
	}
	if err := gatewayConnection.WriteJSON(map[string]any{
		"op": 2, "d": map[string]any{"token": bobSession.AccessToken, "intents": 4 | 16 | 128},
	}); err != nil {
		t.Fatal(err)
	}
	if message := readGatewayMessage(t, gatewayConnection); message.Type != "READY" {
		t.Fatalf("expected READY, got %+v", message)
	}
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
	otherTextChannel := requestJSON[protocolgo.Channel](t, server.Client(), http.MethodPost, server.URL+"/api/v1/guilds/"+guild.Id.String()+"/channels", protocolgo.CreateChannelRequest{
		Type: protocolgo.TEXT, Name: "別の企画",
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
	otherMessage := requestJSON[protocolgo.Message](t, server.Client(), http.MethodPost, server.URL+"/api/v1/channels/"+otherTextChannel.Id.String()+"/messages", protocolgo.CreateMessageRequest{
		Content: "別チャンネルのMessage",
	}, aliceSession.AccessToken, http.StatusCreated)
	_ = readGatewayMessage(t, gatewayConnection)
	requestJSON[protocolgo.Error](t, server.Client(), http.MethodPost, server.URL+"/api/v1/channels/"+textChannel.Id.String()+"/messages", protocolgo.CreateMessageRequest{
		Content: "別チャンネルには返信できない", ReplyToMessageId: &otherMessage.Id,
	}, bobSession.AccessToken, http.StatusNotFound)

	first := requestJSON[protocolgo.Message](t, server.Client(), http.MethodPost, server.URL+"/api/v1/channels/"+textChannel.Id.String()+"/messages", protocolgo.CreateMessageRequest{
		Content: "1つ目",
	}, bobSession.AccessToken, http.StatusCreated)
	firstEvent := readGatewayMessage(t, gatewayConnection)
	if firstEvent.Type != "MESSAGE_CREATE" || gatewayMessageID(t, firstEvent) != first.Id {
		t.Fatalf("unexpected first message event: %+v", firstEvent)
	}
	if len(first.Reactions) != 0 {
		t.Fatalf("new message must have no reactions: %+v", first.Reactions)
	}
	reactionURL := server.URL + "/api/v1/channels/" + textChannel.Id.String() + "/messages/" + first.Id.String() + "/reactions/" + url.PathEscape("👍")
	bobReaction := requestJSON[protocolgo.MessageReaction](t, server.Client(), http.MethodPut, reactionURL, nil, bobSession.AccessToken, http.StatusOK)
	if bobReaction.Count != 1 || !bobReaction.Me || bobReaction.Emoji != "👍" {
		t.Fatalf("unexpected first reaction: %+v", bobReaction)
	}
	assertReactionEvent(t, readGatewayMessage(t, gatewayConnection), "MESSAGE_REACTION_ADD", first.Id, bob.Id, 1)
	duplicateReaction := requestJSON[protocolgo.MessageReaction](t, server.Client(), http.MethodPut, reactionURL, nil, bobSession.AccessToken, http.StatusOK)
	if duplicateReaction.Count != 1 || !duplicateReaction.Me {
		t.Fatalf("duplicate reaction must be idempotent: %+v", duplicateReaction)
	}
	aliceReaction := requestJSON[protocolgo.MessageReaction](t, server.Client(), http.MethodPut, reactionURL, nil, aliceSession.AccessToken, http.StatusOK)
	if aliceReaction.Count != 2 || !aliceReaction.Me {
		t.Fatalf("unexpected second user reaction: %+v", aliceReaction)
	}
	assertReactionEvent(t, readGatewayMessage(t, gatewayConnection), "MESSAGE_REACTION_ADD", first.Id, alice.Id, 2)
	messageWithReactions := requestJSON[protocolgo.Message](t, server.Client(), http.MethodGet, server.URL+"/api/v1/channels/"+textChannel.Id.String()+"/messages/"+first.Id.String(), nil, bobSession.AccessToken, http.StatusOK)
	if len(messageWithReactions.Reactions) != 1 || messageWithReactions.Reactions[0].Count != 2 || !messageWithReactions.Reactions[0].Me {
		t.Fatalf("message reaction summary is incorrect: %+v", messageWithReactions.Reactions)
	}
	removedReaction := requestJSON[protocolgo.MessageReaction](t, server.Client(), http.MethodDelete, reactionURL, nil, bobSession.AccessToken, http.StatusOK)
	if removedReaction.Count != 1 || removedReaction.Me {
		t.Fatalf("unexpected removed reaction: %+v", removedReaction)
	}
	assertReactionEvent(t, readGatewayMessage(t, gatewayConnection), "MESSAGE_REACTION_REMOVE", first.Id, bob.Id, 1)
	duplicateRemoval := requestJSON[protocolgo.MessageReaction](t, server.Client(), http.MethodDelete, reactionURL, nil, bobSession.AccessToken, http.StatusOK)
	if duplicateRemoval.Count != 1 || duplicateRemoval.Me {
		t.Fatalf("duplicate removal must be idempotent: %+v", duplicateRemoval)
	}
	second := requestJSON[protocolgo.Message](t, server.Client(), http.MethodPost, server.URL+"/api/v1/channels/"+textChannel.Id.String()+"/messages", protocolgo.CreateMessageRequest{
		Content: "2つ目", ReplyToMessageId: &first.Id,
	}, bobSession.AccessToken, http.StatusCreated)
	secondEvent := readGatewayMessage(t, gatewayConnection)
	if secondEvent.Type != "MESSAGE_CREATE" || gatewayMessageID(t, secondEvent) != second.Id {
		t.Fatalf("unexpected reply event: %+v", secondEvent)
	}
	if second.ReplyToMessageId == nil || *second.ReplyToMessageId != first.Id || second.ReplyTo == nil || second.ReplyTo.Content != first.Content {
		t.Fatalf("message reply was not resolved: %+v", second)
	}
	var secondEventData struct {
		ReplyToMessageID *uuid.UUID `json:"reply_to_message_id"`
		ReplyTo          *struct {
			ID      uuid.UUID `json:"id"`
			Content *string   `json:"content"`
		} `json:"reply_to"`
	}
	if err := json.Unmarshal(secondEvent.Data, &secondEventData); err != nil {
		t.Fatal(err)
	}
	if secondEventData.ReplyToMessageID == nil || *secondEventData.ReplyToMessageID != first.Id || secondEventData.ReplyTo == nil || secondEventData.ReplyTo.Content == nil || *secondEventData.ReplyTo.Content != first.Content {
		t.Fatalf("gateway reply was not resolved: %+v", secondEventData)
	}
	third := requestJSON[protocolgo.Message](t, server.Client(), http.MethodPost, server.URL+"/api/v1/channels/"+textChannel.Id.String()+"/messages", protocolgo.CreateMessageRequest{
		Content: "3つ目",
	}, bobSession.AccessToken, http.StatusCreated)
	_ = readGatewayMessage(t, gatewayConnection)
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
	if event := readGatewayMessage(t, gatewayConnection); event.Type != "MESSAGE_UPDATE" || gatewayMessageID(t, event) != first.Id {
		t.Fatalf("unexpected message update event: %+v", event)
	}

	cleared := requestJSON[protocolgo.Guild](t, server.Client(), http.MethodPatch, server.URL+"/api/v1/guilds/"+guild.Id.String(), map[string]any{
		"description": nil,
	}, aliceSession.AccessToken, http.StatusOK)
	if cleared.Description != nil {
		t.Fatalf("explicit null must clear description: %+v", cleared)
	}
	requestJSON[struct{}](t, server.Client(), http.MethodDelete, server.URL+"/api/v1/channels/"+textChannel.Id.String()+"/messages/"+first.Id.String(), nil, aliceSession.AccessToken, http.StatusNoContent)
	if event := readGatewayMessage(t, gatewayConnection); event.Type != "MESSAGE_DELETE" || gatewayMessageID(t, event) != first.Id {
		t.Fatalf("unexpected message delete event: %+v", event)
	}
	requestJSON[protocolgo.Error](t, server.Client(), http.MethodGet, server.URL+"/api/v1/channels/"+textChannel.Id.String()+"/messages/"+first.Id.String(), nil, bobSession.AccessToken, http.StatusNotFound)
	replyAfterDelete := requestJSON[protocolgo.Message](t, server.Client(), http.MethodGet, server.URL+"/api/v1/channels/"+textChannel.Id.String()+"/messages/"+second.Id.String(), nil, bobSession.AccessToken, http.StatusOK)
	if replyAfterDelete.ReplyToMessageId == nil || *replyAfterDelete.ReplyToMessageId != first.Id || replyAfterDelete.ReplyTo != nil {
		t.Fatalf("deleted reply source must keep only its ID: %+v", replyAfterDelete)
	}
}

func registerTestUser(t *testing.T, server *httptest.Server, email, displayName string) protocolgo.SessionTokenResponse {
	t.Helper()
	return requestJSON[protocolgo.SessionTokenResponse](t, server.Client(), http.MethodPost, server.URL+"/api/v1/auth/password/register", protocolgo.RegisterPasswordRequest{
		Email: protocolgo.Email(email), Password: "correct horse battery staple", DisplayName: displayName,
	}, "", http.StatusCreated)
}

func stringPointer(value string) *string { return &value }

type integrationGatewayMessage struct {
	Op   int             `json:"op"`
	Type string          `json:"t"`
	Data json.RawMessage `json:"d"`
}

func readGatewayMessage(t *testing.T, connection *websocket.Conn) integrationGatewayMessage {
	t.Helper()
	_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	var message integrationGatewayMessage
	if err := connection.ReadJSON(&message); err != nil {
		t.Fatal(err)
	}
	return message
}

func gatewayMessageID(t *testing.T, message integrationGatewayMessage) uuid.UUID {
	t.Helper()
	var data struct {
		ID uuid.UUID `json:"id"`
	}
	if err := json.Unmarshal(message.Data, &data); err != nil {
		t.Fatal(err)
	}
	return data.ID
}

func assertReactionEvent(t *testing.T, message integrationGatewayMessage, eventType string, messageID, userID uuid.UUID, count int) {
	t.Helper()
	var data struct {
		MessageID uuid.UUID `json:"message_id"`
		UserID    uuid.UUID `json:"user_id"`
		Emoji     string    `json:"emoji"`
		Count     int       `json:"count"`
	}
	if err := json.Unmarshal(message.Data, &data); err != nil {
		t.Fatal(err)
	}
	if message.Type != eventType || data.MessageID != messageID || data.UserID != userID || data.Emoji != "👍" || data.Count != count {
		t.Fatalf("unexpected reaction event: type=%s data=%+v", message.Type, data)
	}
}
