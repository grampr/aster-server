package tests

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/grampr/aster-server/internal/community"
	"github.com/grampr/aster-server/internal/gateway"
	"github.com/grampr/aster-server/internal/httpapi"
	mediaapi "github.com/grampr/aster-server/internal/media"
	postgresplatform "github.com/grampr/aster-server/internal/platform/postgres"
	voiceapi "github.com/grampr/aster-server/internal/voice"
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
	communityService, err := community.NewService(community.NewPostgresStore(pool))
	if err != nil {
		t.Fatal(err)
	}
	chatService, err := chat.NewService(chat.NewPostgresStore(pool), communityService)
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
	objects := newFakeObjectStorage()
	mediaService, err := mediaapi.NewService(mediaapi.NewPostgresStore(pool), objects, chatService)
	if err != nil {
		t.Fatal(err)
	}
	voiceProvider := newFakeVoiceProvider()
	voiceService, err := voiceapi.NewService(voiceapi.NewPostgresStore(pool), voiceProvider, chatService)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.NewWithMedia(authService, chatService, communityService, gatewayService, mediaService, voiceService, logger, "test"))
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
		"op": 2, "d": map[string]any{"token": bobSession.AccessToken, "intents": 4 | 16 | 32 | 128 | 512},
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
		Type: protocolgo.CreateChannelRequestTypeTEXT, Name: "イベント企画", Topic: stringPointer("日程を相談します"),
	}, aliceSession.AccessToken, http.StatusCreated)
	voiceChannel := requestJSON[protocolgo.Channel](t, server.Client(), http.MethodPost, server.URL+"/api/v1/guilds/"+guild.Id.String()+"/channels", protocolgo.CreateChannelRequest{
		Type: protocolgo.CreateChannelRequestTypeVOICE, Name: "イベント会議",
	}, aliceSession.AccessToken, http.StatusCreated)
	otherTextChannel := requestJSON[protocolgo.Channel](t, server.Client(), http.MethodPost, server.URL+"/api/v1/guilds/"+guild.Id.String()+"/channels", protocolgo.CreateChannelRequest{
		Type: protocolgo.CreateChannelRequestTypeTEXT, Name: "別の企画",
	}, aliceSession.AccessToken, http.StatusCreated)
	if textChannel.Position != 0 || voiceChannel.Position != 1 {
		t.Fatalf("channels must receive stable positions: text=%d voice=%d", textChannel.Position, voiceChannel.Position)
	}
	requestJSON[struct{}](t, server.Client(), http.MethodPost, server.URL+"/api/v1/channels/"+textChannel.Id.String()+"/typing", nil, aliceSession.AccessToken, http.StatusNoContent)
	assertTypingEvent(t, readGatewayMessage(t, gatewayConnection), textChannel.Id, alice.Id, "Alice")
	requestJSON[protocolgo.Error](t, server.Client(), http.MethodPost, server.URL+"/api/v1/channels/"+voiceChannel.Id.String()+"/typing", nil, aliceSession.AccessToken, http.StatusNotFound)
	requestJSON[protocolgo.Error](t, server.Client(), http.MethodPatch, server.URL+"/api/v1/channels/"+textChannel.Id.String(), map[string]any{
		"name": "変更不可",
	}, bobSession.AccessToken, http.StatusForbidden)
	requestJSON[protocolgo.Error](t, server.Client(), http.MethodPost, server.URL+"/api/v1/channels/"+voiceChannel.Id.String()+"/messages", protocolgo.CreateMessageRequest{
		Content: stringPointer("voiceには送れない"),
	}, bobSession.AccessToken, http.StatusNotFound)
	otherMessage := requestJSON[protocolgo.Message](t, server.Client(), http.MethodPost, server.URL+"/api/v1/channels/"+otherTextChannel.Id.String()+"/messages", protocolgo.CreateMessageRequest{
		Content: stringPointer("別チャンネルのMessage"),
	}, aliceSession.AccessToken, http.StatusCreated)
	_ = readGatewayMessage(t, gatewayConnection)
	requestJSON[protocolgo.Error](t, server.Client(), http.MethodPost, server.URL+"/api/v1/channels/"+textChannel.Id.String()+"/messages", protocolgo.CreateMessageRequest{
		Content: stringPointer("別チャンネルには返信できない"), ReplyToMessageId: &otherMessage.Id,
	}, bobSession.AccessToken, http.StatusNotFound)

	first := requestJSON[protocolgo.Message](t, server.Client(), http.MethodPost, server.URL+"/api/v1/channels/"+textChannel.Id.String()+"/messages", protocolgo.CreateMessageRequest{
		Content: stringPointer("1つ目"),
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
		Content: stringPointer("2つ目"), ReplyToMessageId: &first.Id,
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
		Content: stringPointer("3つ目"),
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

	category := requestJSON[protocolgo.Channel](t, server.Client(), http.MethodPost, server.URL+"/api/v1/guilds/"+guild.Id.String()+"/channels", protocolgo.CreateChannelRequest{
		Type: protocolgo.CreateChannelRequestTypeCATEGORY, Name: "企画カテゴリ",
	}, aliceSession.AccessToken, http.StatusCreated)
	if category.Type != protocolgo.ChannelTypeCATEGORY || category.GuildId == nil || *category.GuildId != guild.Id {
		t.Fatalf("unexpected category: %+v", category)
	}
	thread := requestJSON[protocolgo.Channel](t, server.Client(), http.MethodPost, server.URL+"/api/v1/channels/"+textChannel.Id.String()+"/threads", protocolgo.CreateThreadRequest{
		Name: "2つ目について", MessageId: &second.Id,
	}, bobSession.AccessToken, http.StatusCreated)
	if thread.Type != protocolgo.ChannelTypeTHREAD || thread.ParentId == nil || *thread.ParentId != textChannel.Id {
		t.Fatalf("unexpected thread: %+v", thread)
	}
	threadMessage := requestJSON[protocolgo.Message](t, server.Client(), http.MethodPost, server.URL+"/api/v1/channels/"+thread.Id.String()+"/messages", protocolgo.CreateMessageRequest{
		Content: stringPointer("スレッド内の返信"),
	}, bobSession.AccessToken, http.StatusCreated)
	if threadMessage.ChannelId != thread.Id {
		t.Fatalf("message must belong to thread: %+v", threadMessage)
	}
	_ = readGatewayMessage(t, gatewayConnection)

	direct := requestJSON[protocolgo.Channel](t, server.Client(), http.MethodPost, server.URL+"/api/v1/users/@me/channels", protocolgo.CreateDirectChannelRequest{
		RecipientId: bob.Id,
	}, aliceSession.AccessToken, http.StatusOK)
	if direct.Type != protocolgo.ChannelTypeDIRECT || direct.GuildId != nil || len(direct.Recipients) != 2 {
		t.Fatalf("unexpected direct channel: %+v", direct)
	}
	directAgain := requestJSON[protocolgo.Channel](t, server.Client(), http.MethodPost, server.URL+"/api/v1/users/@me/channels", protocolgo.CreateDirectChannelRequest{
		RecipientId: alice.Id,
	}, bobSession.AccessToken, http.StatusOK)
	if directAgain.Id != direct.Id {
		t.Fatalf("direct channel creation must be idempotent: first=%s second=%s", direct.Id, directAgain.Id)
	}
	directMessage := requestJSON[protocolgo.Message](t, server.Client(), http.MethodPost, server.URL+"/api/v1/channels/"+direct.Id.String()+"/messages", protocolgo.CreateMessageRequest{
		Content: stringPointer("AliceからBobへのDM"),
	}, aliceSession.AccessToken, http.StatusCreated)
	directMessages := requestJSON[protocolgo.MessageList](t, server.Client(), http.MethodGet, server.URL+"/api/v1/channels/"+direct.Id.String()+"/messages?limit=20", nil, bobSession.AccessToken, http.StatusOK)
	if len(directMessages.Items) != 1 || directMessages.Items[0].Id != directMessage.Id {
		t.Fatalf("unexpected direct messages: %+v", directMessages)
	}
	requestJSON[protocolgo.Error](t, server.Client(), http.MethodDelete, server.URL+"/api/v1/channels/"+direct.Id.String()+"/messages/"+directMessage.Id.String(), nil, bobSession.AccessToken, http.StatusForbidden)

	search := requestJSON[protocolgo.MessageSearchResultList](t, server.Client(), http.MethodGet, server.URL+"/api/v1/guilds/"+guild.Id.String()+"/messages/search?query="+url.QueryEscape("2つ目")+"&limit=20", nil, bobSession.AccessToken, http.StatusOK)
	if len(search.Items) == 0 || search.Items[0].Message.Id != second.Id {
		t.Fatalf("message search did not find the expected message: %+v", search)
	}
	state := requestJSON[protocolgo.ReadState](t, server.Client(), http.MethodPut, server.URL+"/api/v1/channels/"+textChannel.Id.String()+"/read-state", protocolgo.UpdateReadStateRequest{
		LastReadMessageId: second.Id,
	}, bobSession.AccessToken, http.StatusOK)
	if state.LastReadMessageId == nil || *state.LastReadMessageId != second.Id {
		t.Fatalf("unexpected read state: %+v", state)
	}
	if event := readGatewayMessage(t, gatewayConnection); event.Type != "READ_STATE_UPDATE" {
		t.Fatalf("unexpected read state event: %+v", event)
	}
	states := requestJSON[protocolgo.ReadStateList](t, server.Client(), http.MethodGet, server.URL+"/api/v1/users/@me/read-states", nil, bobSession.AccessToken, http.StatusOK)
	if len(states.Items) != 1 || states.Items[0].ChannelId != textChannel.Id {
		t.Fatalf("unexpected read state list: %+v", states)
	}

	checksum := strings.Repeat("a", 64)
	upload := requestJSON[protocolgo.AttachmentUploadIntent](t, server.Client(), http.MethodPost, server.URL+"/api/v1/channels/"+otherTextChannel.Id.String()+"/attachments/intents", protocolgo.CreateAttachmentUploadIntentRequest{
		Filename: "企画書.pdf", ContentType: "application/pdf", Size: 128, ChecksumSha256: checksum,
	}, bobSession.AccessToken, http.StatusCreated)
	if upload.Attachment.Status != protocolgo.PENDING || upload.UploadMethod != protocolgo.PUT || upload.UploadUrl == "" {
		t.Fatalf("unexpected upload intent: %+v", upload)
	}
	readyAttachment := requestJSON[protocolgo.Attachment](t, server.Client(), http.MethodPost, server.URL+"/api/v1/attachments/"+upload.Attachment.Id.String()+"/finalize", nil, bobSession.AccessToken, http.StatusOK)
	if readyAttachment.Status != protocolgo.READY {
		t.Fatalf("attachment was not finalized: %+v", readyAttachment)
	}
	attachmentIDs := []uuid.UUID{readyAttachment.Id}
	attachmentMessage := requestJSON[protocolgo.Message](t, server.Client(), http.MethodPost, server.URL+"/api/v1/channels/"+otherTextChannel.Id.String()+"/messages", protocolgo.CreateMessageRequest{AttachmentIds: &attachmentIDs}, bobSession.AccessToken, http.StatusCreated)
	if attachmentMessage.Content != "" || len(attachmentMessage.Attachments) != 1 || attachmentMessage.Attachments[0].Id != readyAttachment.Id {
		t.Fatalf("unexpected attachment message: %+v", attachmentMessage)
	}
	if event := readGatewayMessage(t, gatewayConnection); event.Type != "MESSAGE_CREATE" || gatewayMessageID(t, event) != attachmentMessage.Id {
		t.Fatalf("unexpected attachment message event: %+v", event)
	}
	requestJSON[protocolgo.Error](t, server.Client(), http.MethodDelete, server.URL+"/api/v1/attachments/"+readyAttachment.Id.String(), nil, bobSession.AccessToken, http.StatusForbidden)
	downloadRequest, _ := http.NewRequest(http.MethodGet, server.URL+"/api/v1/attachments/"+readyAttachment.Id.String()+"/content", nil)
	downloadRequest.Header.Set("Authorization", "Bearer "+bobSession.AccessToken)
	noRedirectClient := *server.Client()
	noRedirectClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	downloadResponse, err := noRedirectClient.Do(downloadRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = downloadResponse.Body.Close()
	if downloadResponse.StatusCode != http.StatusSeeOther || downloadResponse.Header.Get("Location") == "" {
		t.Fatalf("unexpected attachment redirect: status=%d location=%q", downloadResponse.StatusCode, downloadResponse.Header.Get("Location"))
	}

	voiceSession := requestJSON[protocolgo.VoiceSession](t, server.Client(), http.MethodPost, server.URL+"/api/v1/channels/"+voiceChannel.Id.String()+"/voice", protocolgo.JoinVoiceChannelRequest{}, bobSession.AccessToken, http.StatusOK)
	if voiceSession.Provider != "fake-voice" || voiceSession.Credential == "" || voiceSession.State.ChannelId == nil || *voiceSession.State.ChannelId != voiceChannel.Id {
		t.Fatalf("unexpected voice session: %+v", voiceSession)
	}
	if event := readGatewayMessage(t, gatewayConnection); event.Type != "VOICE_STATE_UPDATE" {
		t.Fatalf("unexpected voice join event: %+v", event)
	}
	muted := true
	voiceState := requestJSON[protocolgo.VoiceState](t, server.Client(), http.MethodPatch, server.URL+"/api/v1/voice/sessions/@me", protocolgo.UpdateVoiceStateRequest{SelfMute: &muted}, bobSession.AccessToken, http.StatusOK)
	if !voiceState.SelfMute {
		t.Fatalf("voice state was not updated: %+v", voiceState)
	}
	if event := readGatewayMessage(t, gatewayConnection); event.Type != "VOICE_STATE_UPDATE" {
		t.Fatalf("unexpected voice update event: %+v", event)
	}
	streaming := true
	requestJSON[protocolgo.Error](t, server.Client(), http.MethodPatch, server.URL+"/api/v1/voice/sessions/@me", protocolgo.UpdateVoiceStateRequest{SelfStream: &streaming}, bobSession.AccessToken, http.StatusForbidden)
	voiceStates := requestJSON[protocolgo.VoiceStateList](t, server.Client(), http.MethodGet, server.URL+"/api/v1/channels/"+voiceChannel.Id.String()+"/voice", nil, bobSession.AccessToken, http.StatusOK)
	if len(voiceStates.Items) != 1 || voiceStates.Items[0].UserId != bob.Id {
		t.Fatalf("unexpected voice state list: %+v", voiceStates)
	}
	requestJSON[struct{}](t, server.Client(), http.MethodDelete, server.URL+"/api/v1/voice/sessions/@me", nil, bobSession.AccessToken, http.StatusNoContent)
	if event := readGatewayMessage(t, gatewayConnection); event.Type != "VOICE_STATE_UPDATE" {
		t.Fatalf("unexpected voice leave event: %+v", event)
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

func assertTypingEvent(t *testing.T, message integrationGatewayMessage, channelID, userID uuid.UUID, displayName string) {
	t.Helper()
	var data struct {
		ChannelID uuid.UUID `json:"channel_id"`
		User      struct {
			ID          uuid.UUID `json:"id"`
			DisplayName string    `json:"display_name"`
		} `json:"user"`
		StartedAt time.Time `json:"started_at"`
	}
	if err := json.Unmarshal(message.Data, &data); err != nil {
		t.Fatal(err)
	}
	if message.Type != "TYPING_START" || data.ChannelID != channelID || data.User.ID != userID || data.User.DisplayName != displayName || data.StartedAt.IsZero() {
		t.Fatalf("unexpected typing event: type=%s data=%+v", message.Type, data)
	}
}

type fakeObjectStorage struct {
	metadata map[string]mediaapi.ObjectMetadata
}

func newFakeObjectStorage() *fakeObjectStorage {
	return &fakeObjectStorage{metadata: map[string]mediaapi.ObjectMetadata{}}
}
func (f *fakeObjectStorage) PresignPut(_ context.Context, key string, metadata mediaapi.ObjectMetadata, _ time.Duration) (string, map[string]string, error) {
	f.metadata[key] = metadata
	return "https://upload.example/" + key, map[string]string{"Content-Type": metadata.ContentType}, nil
}
func (f *fakeObjectStorage) PresignGet(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://download.example/" + key, nil
}
func (f *fakeObjectStorage) Stat(_ context.Context, key string) (mediaapi.ObjectMetadata, error) {
	metadata, ok := f.metadata[key]
	if !ok {
		return mediaapi.ObjectMetadata{}, mediaapi.ErrObjectNotFound
	}
	return metadata, nil
}
func (f *fakeObjectStorage) Delete(_ context.Context, key string) error {
	delete(f.metadata, key)
	return nil
}

type fakeVoiceProvider struct{ rooms int }

func newFakeVoiceProvider() *fakeVoiceProvider { return &fakeVoiceProvider{} }
func (f *fakeVoiceProvider) Name() string      { return "fake-voice" }
func (f *fakeVoiceProvider) Endpoint() string  { return "https://voice.example/connect" }
func (f *fakeVoiceProvider) CreateRoom(_ context.Context, _ string) (string, error) {
	f.rooms++
	return fmt.Sprintf("room-%d", f.rooms), nil
}
func (f *fakeVoiceProvider) AddParticipant(_ context.Context, _ string, customID, _ string, _, _ bool) (voiceapi.ProviderParticipant, error) {
	return voiceapi.ProviderParticipant{ID: "participant-" + customID, Credential: "voice-token", ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func (f *fakeVoiceProvider) RemoveParticipant(_ context.Context, _, _ string) error { return nil }
