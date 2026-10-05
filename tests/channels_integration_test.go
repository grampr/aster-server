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

func TestCategoriesThreadsAndDirectMessages(t *testing.T) {
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
	server := httptest.NewServer(httpapi.New(authService, chatService, gatewayService, logger, "test"))
	defer server.Close()
	client := server.Client()
	base := server.URL + "/api/v1"

	aliceSession := registerTestUser(t, server, "alice@example.com", "Alice")
	bobSession := registerTestUser(t, server, "bob@example.com", "Bob")
	carolSession := registerTestUser(t, server, "carol@example.com", "Carol")
	alice := requestJSON[protocolgo.UserSelf](t, client, http.MethodGet, base+"/users/@me", nil, aliceSession.AccessToken, http.StatusOK)
	bob := requestJSON[protocolgo.UserSelf](t, client, http.MethodGet, base+"/users/@me", nil, bobSession.AccessToken, http.StatusOK)

	// Bob listens to Guild Channel, Message and Direct Message events.
	bobGateway, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/gateway/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bobGateway.Close()
	readGatewayMessage(t, bobGateway)
	if err := bobGateway.WriteJSON(map[string]any{"op": 2, "d": map[string]any{"token": bobSession.AccessToken, "intents": 1 | 4 | 8 | 16}}); err != nil {
		t.Fatal(err)
	}
	if message := readGatewayMessage(t, bobGateway); message.Type != "READY" {
		t.Fatalf("expected READY, got %+v", message)
	}
	expectEvent := func(eventType string) integrationGatewayMessage {
		t.Helper()
		message := readGatewayMessage(t, bobGateway)
		if message.Type != eventType {
			t.Fatalf("expected %s, got %s: %s", eventType, message.Type, message.Data)
		}
		return message
	}

	guild := requestJSON[protocolgo.Guild](t, client, http.MethodPost, base+"/guilds", protocolgo.CreateGuildRequest{Name: "星屑コミュニティ"}, aliceSession.AccessToken, http.StatusCreated)
	guildPath := base + "/guilds/" + guild.Id.String()
	invite := requestJSON[protocolgo.Invite](t, client, http.MethodPost, guildPath+"/invites", map[string]any{}, aliceSession.AccessToken, http.StatusCreated)
	requestJSON[protocolgo.GuildMember](t, client, http.MethodPost, base+"/invites/"+invite.Code+"/accept", nil, bobSession.AccessToken, http.StatusOK)

	// Categories group Channels but cannot hold Messages or nest.
	category := requestJSON[protocolgo.Channel](t, client, http.MethodPost, guildPath+"/channels", map[string]any{"type": "CATEGORY", "name": "企画"}, aliceSession.AccessToken, http.StatusCreated)
	if category.Type != protocolgo.ChannelTypeCATEGORY || category.ParentId != nil {
		t.Fatalf("unexpected category: %+v", category)
	}
	expectEvent("CHANNEL_CREATE")
	text := requestJSON[protocolgo.Channel](t, client, http.MethodPost, guildPath+"/channels", map[string]any{"type": "TEXT", "name": "雑談", "parent_id": category.Id}, aliceSession.AccessToken, http.StatusCreated)
	if text.ParentId == nil || *text.ParentId != category.Id || text.GuildId == nil || *text.GuildId != guild.Id {
		t.Fatalf("unexpected text channel: %+v", text)
	}
	expectEvent("CHANNEL_CREATE")
	requestJSON[protocolgo.Error](t, client, http.MethodPost, guildPath+"/channels", map[string]any{"type": "TEXT", "name": "x", "parent_id": text.Id}, aliceSession.AccessToken, http.StatusBadRequest)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, guildPath+"/channels", map[string]any{"type": "TEXT", "name": "x", "parent_id": uuid.New()}, aliceSession.AccessToken, http.StatusBadRequest)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, guildPath+"/channels", map[string]any{"type": "CATEGORY", "name": "x", "parent_id": category.Id}, aliceSession.AccessToken, http.StatusBadRequest)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, guildPath+"/channels", map[string]any{"type": "CATEGORY", "name": "x", "topic": "t"}, aliceSession.AccessToken, http.StatusBadRequest)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/channels/"+category.Id.String()+"/messages", protocolgo.CreateMessageRequest{Content: stringPointer("投稿不可")}, aliceSession.AccessToken, http.StatusNotFound)
	requestJSON[protocolgo.Error](t, client, http.MethodPatch, base+"/channels/"+text.Id.String(), map[string]any{"parent_id": nil}, bobSession.AccessToken, http.StatusForbidden)
	detached := requestJSON[protocolgo.Channel](t, client, http.MethodPatch, base+"/channels/"+text.Id.String(), map[string]any{"parent_id": nil}, aliceSession.AccessToken, http.StatusOK)
	if detached.ParentId != nil {
		t.Fatalf("channel must leave the category: %+v", detached)
	}
	expectEvent("CHANNEL_UPDATE")
	requestJSON[protocolgo.Channel](t, client, http.MethodPatch, base+"/channels/"+text.Id.String(), map[string]any{"parent_id": category.Id}, aliceSession.AccessToken, http.StatusOK)
	expectEvent("CHANNEL_UPDATE")
	requestJSON[struct{}](t, client, http.MethodDelete, base+"/channels/"+category.Id.String(), nil, aliceSession.AccessToken, http.StatusNoContent)
	expectEvent("CHANNEL_DELETE")
	if orphan := requestJSON[protocolgo.Channel](t, client, http.MethodGet, base+"/channels/"+text.Id.String(), nil, bobSession.AccessToken, http.StatusOK); orphan.ParentId != nil {
		t.Fatalf("deleting a category must keep its channels: %+v", orphan)
	}

	// Threads hang off a Text Channel, optionally from one Message, once per Message.
	messagesPath := base + "/channels/" + text.Id.String() + "/messages"
	starter := requestJSON[protocolgo.Message](t, client, http.MethodPost, messagesPath, protocolgo.CreateMessageRequest{Content: stringPointer("日程を決めましょう")}, aliceSession.AccessToken, http.StatusCreated)
	expectEvent("MESSAGE_CREATE")
	threadsPath := base + "/channels/" + text.Id.String() + "/threads"
	thread := requestJSON[protocolgo.Channel](t, client, http.MethodPost, threadsPath, map[string]any{"name": "日程", "message_id": starter.Id}, bobSession.AccessToken, http.StatusCreated)
	if thread.Type != protocolgo.ChannelTypeTHREAD || thread.ParentId == nil || *thread.ParentId != text.Id || thread.GuildId == nil || *thread.GuildId != guild.Id {
		t.Fatalf("unexpected thread: %+v", thread)
	}
	expectEvent("CHANNEL_CREATE")
	if duplicate := requestJSON[protocolgo.Error](t, client, http.MethodPost, threadsPath, map[string]any{"name": "重複", "message_id": starter.Id}, aliceSession.AccessToken, http.StatusConflict); duplicate.Code != "THREAD_ALREADY_EXISTS" {
		t.Fatalf("unexpected error: %+v", duplicate)
	}
	requestJSON[protocolgo.Error](t, client, http.MethodPost, threadsPath, map[string]any{"name": "存在しない", "message_id": uuid.New()}, aliceSession.AccessToken, http.StatusNotFound)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, threadsPath, map[string]any{"name": "外部"}, carolSession.AccessToken, http.StatusNotFound)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, threadsPath, map[string]any{"name": "  "}, aliceSession.AccessToken, http.StatusBadRequest)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/channels/"+thread.Id.String()+"/threads", map[string]any{"name": "入れ子"}, aliceSession.AccessToken, http.StatusNotFound)
	standalone := requestJSON[protocolgo.Channel](t, client, http.MethodPost, threadsPath, map[string]any{"name": "雑談スレッド"}, aliceSession.AccessToken, http.StatusCreated)
	expectEvent("CHANNEL_CREATE")

	// Activity moves a Thread to the front of the update-ordered list; Guild lists omit Threads.
	requestJSON[protocolgo.Message](t, client, http.MethodPost, base+"/channels/"+thread.Id.String()+"/messages", protocolgo.CreateMessageRequest{Content: stringPointer("来週はどうですか")}, aliceSession.AccessToken, http.StatusCreated)
	expectEvent("MESSAGE_CREATE")
	threads := requestJSON[protocolgo.ChannelList](t, client, http.MethodGet, threadsPath+"?limit=1", nil, bobSession.AccessToken, http.StatusOK)
	if len(threads.Items) != 1 || threads.Items[0].Id != thread.Id || !threads.Page.HasMore || threads.Page.NextCursor == nil {
		t.Fatalf("unexpected first thread page: %+v", threads)
	}
	rest := requestJSON[protocolgo.ChannelList](t, client, http.MethodGet, threadsPath+"?limit=1&cursor="+*threads.Page.NextCursor, nil, bobSession.AccessToken, http.StatusOK)
	if len(rest.Items) != 1 || rest.Items[0].Id != standalone.Id || rest.Page.HasMore {
		t.Fatalf("unexpected second thread page: %+v", rest)
	}
	requestJSON[protocolgo.Error](t, client, http.MethodGet, threadsPath, nil, carolSession.AccessToken, http.StatusNotFound)
	channels := requestJSON[protocolgo.ChannelList](t, client, http.MethodGet, guildPath+"/channels", nil, bobSession.AccessToken, http.StatusOK)
	if len(channels.Items) != 1 || channels.Items[0].Id != text.Id {
		t.Fatalf("guild channel list must omit threads: %+v", channels)
	}
	requestJSON[protocolgo.Error](t, client, http.MethodPatch, base+"/channels/"+thread.Id.String(), map[string]any{"topic": "不可"}, aliceSession.AccessToken, http.StatusBadRequest)
	if renamed := requestJSON[protocolgo.Channel](t, client, http.MethodPatch, base+"/channels/"+thread.Id.String(), map[string]any{"name": "日程調整"}, aliceSession.AccessToken, http.StatusOK); renamed.Name == nil || *renamed.Name != "日程調整" {
		t.Fatalf("unexpected renamed thread: %+v", renamed)
	}
	expectEvent("CHANNEL_UPDATE")
	requestJSON[struct{}](t, client, http.MethodDelete, base+"/channels/"+text.Id.String(), nil, aliceSession.AccessToken, http.StatusNoContent)
	expectEvent("CHANNEL_DELETE")
	requestJSON[protocolgo.Error](t, client, http.MethodGet, base+"/channels/"+thread.Id.String(), nil, aliceSession.AccessToken, http.StatusNotFound)

	// Direct Messages are private to their two participants and unique per pair.
	requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/users/@me/channels", map[string]any{"recipient_id": alice.Id}, aliceSession.AccessToken, http.StatusBadRequest)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/users/@me/channels", map[string]any{"recipient_id": uuid.New()}, aliceSession.AccessToken, http.StatusNotFound)
	direct := requestJSON[protocolgo.Channel](t, client, http.MethodPost, base+"/users/@me/channels", map[string]any{"recipient_id": bob.Id}, aliceSession.AccessToken, http.StatusOK)
	if direct.Type != protocolgo.ChannelTypeDIRECT || direct.GuildId != nil || direct.Name != nil || len(direct.Recipients) != 2 {
		t.Fatalf("unexpected direct channel: %+v", direct)
	}
	directEvent := expectEvent("CHANNEL_CREATE")
	var directData struct {
		ID         uuid.UUID `json:"id"`
		GuildID    *string   `json:"guild_id"`
		Recipients []struct {
			ID uuid.UUID `json:"id"`
		} `json:"recipients"`
	}
	if err := json.Unmarshal(directEvent.Data, &directData); err != nil {
		t.Fatal(err)
	}
	if directData.ID != direct.Id || directData.GuildID != nil || len(directData.Recipients) != 2 {
		t.Fatalf("unexpected direct channel event: %+v", directData)
	}
	again := requestJSON[protocolgo.Channel](t, client, http.MethodPost, base+"/users/@me/channels", map[string]any{"recipient_id": alice.Id}, bobSession.AccessToken, http.StatusOK)
	if again.Id != direct.Id {
		t.Fatalf("the same pair must share one direct channel: %+v %+v", again, direct)
	}
	directMessages := base + "/channels/" + direct.Id.String() + "/messages"
	requestJSON[protocolgo.Message](t, client, http.MethodPost, directMessages, protocolgo.CreateMessageRequest{Content: stringPointer("こんにちは")}, aliceSession.AccessToken, http.StatusCreated)
	expectEvent("MESSAGE_CREATE")
	requestJSON[protocolgo.Message](t, client, http.MethodPost, directMessages, protocolgo.CreateMessageRequest{Content: stringPointer("やあ")}, bobSession.AccessToken, http.StatusCreated)
	expectEvent("MESSAGE_CREATE")
	requestJSON[struct{}](t, client, http.MethodPost, base+"/channels/"+direct.Id.String()+"/typing", nil, aliceSession.AccessToken, http.StatusNoContent)
	for _, forbidden := range []struct{ method, path string }{
		{http.MethodGet, base + "/channels/" + direct.Id.String()},
		{http.MethodGet, directMessages},
	} {
		requestJSON[protocolgo.Error](t, client, forbidden.method, forbidden.path, nil, carolSession.AccessToken, http.StatusNotFound)
	}
	requestJSON[protocolgo.Error](t, client, http.MethodPost, directMessages, protocolgo.CreateMessageRequest{Content: stringPointer("盗み聞き")}, carolSession.AccessToken, http.StatusNotFound)
	requestJSON[protocolgo.Error](t, client, http.MethodPatch, base+"/channels/"+direct.Id.String(), map[string]any{"position": 1}, aliceSession.AccessToken, http.StatusForbidden)
	requestJSON[protocolgo.Error](t, client, http.MethodDelete, base+"/channels/"+direct.Id.String(), nil, aliceSession.AccessToken, http.StatusForbidden)
	if list := requestJSON[protocolgo.ChannelList](t, client, http.MethodGet, base+"/users/@me/channels", nil, bobSession.AccessToken, http.StatusOK); len(list.Items) != 1 || list.Items[0].Id != direct.Id {
		t.Fatalf("unexpected direct channel list: %+v", list)
	}
	if list := requestJSON[protocolgo.ChannelList](t, client, http.MethodGet, base+"/users/@me/channels", nil, carolSession.AccessToken, http.StatusOK); len(list.Items) != 0 {
		t.Fatalf("carol has no direct channels: %+v", list)
	}
}
