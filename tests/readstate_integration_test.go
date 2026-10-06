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

func TestReadStatesAndSearch(t *testing.T) {
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

	// Read state events go to the owner's Sessions even with no intents.
	bobGateway, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/gateway/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bobGateway.Close()
	readGatewayMessage(t, bobGateway)
	if err := bobGateway.WriteJSON(map[string]any{"op": 2, "d": map[string]any{"token": bobSession.AccessToken, "intents": 0}}); err != nil {
		t.Fatal(err)
	}
	if message := readGatewayMessage(t, bobGateway); message.Type != "READY" {
		t.Fatalf("expected READY, got %+v", message)
	}

	guild := requestJSON[protocolgo.Guild](t, client, http.MethodPost, base+"/guilds", protocolgo.CreateGuildRequest{Name: "星屑コミュニティ"}, aliceSession.AccessToken, http.StatusCreated)
	guildPath := base + "/guilds/" + guild.Id.String()
	invite := requestJSON[protocolgo.Invite](t, client, http.MethodPost, guildPath+"/invites", map[string]any{}, aliceSession.AccessToken, http.StatusCreated)
	bob := requestJSON[protocolgo.GuildMember](t, client, http.MethodPost, base+"/invites/"+invite.Code+"/accept", nil, bobSession.AccessToken, http.StatusOK).User
	text := requestJSON[protocolgo.Channel](t, client, http.MethodPost, guildPath+"/channels", protocolgo.CreateChannelRequest{Type: protocolgo.CreateChannelRequestTypeTEXT, Name: "雑談"}, aliceSession.AccessToken, http.StatusCreated)
	other := requestJSON[protocolgo.Channel](t, client, http.MethodPost, guildPath+"/channels", protocolgo.CreateChannelRequest{Type: protocolgo.CreateChannelRequestTypeTEXT, Name: "別件"}, aliceSession.AccessToken, http.StatusCreated)
	post := func(session protocolgo.SessionTokenResponse, channelID uuid.UUID, content string) protocolgo.Message {
		t.Helper()
		return requestJSON[protocolgo.Message](t, client, http.MethodPost, base+"/channels/"+channelID.String()+"/messages", protocolgo.CreateMessageRequest{Content: stringPointer(content)}, session.AccessToken, http.StatusCreated)
	}
	first := post(aliceSession, text.Id, "星空を見に行こう")
	second := post(aliceSession, text.Id, "Hello WORLD")
	third := post(aliceSession, text.Id, "100%のやる気_だよ")
	foreign := post(aliceSession, other.Id, "別チャンネルの星空")
	readPath := base + "/channels/" + text.Id.String() + "/read-state"

	// The read position only moves forward, and only changes are pushed.
	assertReadState := func(state protocolgo.ReadState, messageID uuid.UUID) {
		t.Helper()
		if state.ChannelId != text.Id || state.LastReadMessageId == nil || *state.LastReadMessageId != messageID {
			t.Fatalf("unexpected read state: %+v", state)
		}
	}
	assertReadState(requestJSON[protocolgo.ReadState](t, client, http.MethodPut, readPath, map[string]any{"last_read_message_id": second.Id}, bobSession.AccessToken, http.StatusOK), second.Id)
	if event := readGatewayMessage(t, bobGateway); event.Type != "READ_STATE_UPDATE" {
		t.Fatalf("expected READ_STATE_UPDATE, got %+v", event)
	}
	assertReadState(requestJSON[protocolgo.ReadState](t, client, http.MethodPut, readPath, map[string]any{"last_read_message_id": first.Id}, bobSession.AccessToken, http.StatusOK), second.Id)
	assertReadState(requestJSON[protocolgo.ReadState](t, client, http.MethodPut, readPath, map[string]any{"last_read_message_id": second.Id}, bobSession.AccessToken, http.StatusOK), second.Id)
	assertReadState(requestJSON[protocolgo.ReadState](t, client, http.MethodPut, readPath, map[string]any{"last_read_message_id": third.Id}, bobSession.AccessToken, http.StatusOK), third.Id)
	updateEvent := readGatewayMessage(t, bobGateway)
	var updateData struct {
		ChannelID         uuid.UUID `json:"channel_id"`
		LastReadMessageID uuid.UUID `json:"last_read_message_id"`
	}
	if err := json.Unmarshal(updateEvent.Data, &updateData); err != nil {
		t.Fatal(err)
	}
	if updateEvent.Type != "READ_STATE_UPDATE" || updateData.ChannelID != text.Id || updateData.LastReadMessageID != third.Id {
		t.Fatalf("a backward or repeated update must not be pushed: %+v %+v", updateEvent, updateData)
	}
	requestJSON[protocolgo.Error](t, client, http.MethodPut, readPath, map[string]any{"last_read_message_id": uuid.New()}, bobSession.AccessToken, http.StatusNotFound)
	requestJSON[protocolgo.Error](t, client, http.MethodPut, readPath, map[string]any{"last_read_message_id": foreign.Id}, bobSession.AccessToken, http.StatusNotFound)
	requestJSON[protocolgo.Error](t, client, http.MethodPut, readPath, map[string]any{"last_read_message_id": third.Id}, carolSession.AccessToken, http.StatusNotFound)
	// After the last-read Message is deleted, any Message becomes a valid new position.
	requestJSON[struct{}](t, client, http.MethodDelete, base+"/channels/"+text.Id.String()+"/messages/"+third.Id.String(), nil, aliceSession.AccessToken, http.StatusNoContent)
	assertReadState(requestJSON[protocolgo.ReadState](t, client, http.MethodPut, readPath, map[string]any{"last_read_message_id": first.Id}, bobSession.AccessToken, http.StatusOK), first.Id)

	states := requestJSON[protocolgo.ReadStateList](t, client, http.MethodGet, base+"/users/@me/read-states", nil, bobSession.AccessToken, http.StatusOK)
	positions := map[uuid.UUID]*uuid.UUID{}
	for _, state := range states.Items {
		positions[state.ChannelId] = state.LastReadMessageId
	}
	if len(positions) != 2 || positions[text.Id] == nil || *positions[text.Id] != first.Id || positions[other.Id] != nil {
		t.Fatalf("unexpected read states: %+v", states)
	}
	if none := requestJSON[protocolgo.ReadStateList](t, client, http.MethodGet, base+"/users/@me/read-states", nil, carolSession.AccessToken, http.StatusOK); len(none.Items) != 0 {
		t.Fatalf("carol can read no channels: %+v", none)
	}

	// Search is case-insensitive, treats LIKE wildcards literally, and is newest first.
	searchPath := guildPath + "/messages/search?query="
	third = post(aliceSession, text.Id, "100%のやる気_だよ")
	post(bobSession, text.Id, "星空きれいだね")
	thread := requestJSON[protocolgo.Channel](t, client, http.MethodPost, base+"/channels/"+text.Id.String()+"/threads", map[string]any{"name": "星の話"}, aliceSession.AccessToken, http.StatusCreated)
	threadMessage := post(aliceSession, thread.Id, "スレッドでも星空")
	results := requestJSON[protocolgo.MessageSearchResultList](t, client, http.MethodGet, searchPath+url.QueryEscape("hello world"), nil, bobSession.AccessToken, http.StatusOK)
	if len(results.Items) != 1 || results.Items[0].Message.Id != second.Id || results.Items[0].Excerpt != "Hello WORLD" {
		t.Fatalf("unexpected case-insensitive result: %+v", results)
	}
	if literal := requestJSON[protocolgo.MessageSearchResultList](t, client, http.MethodGet, searchPath+url.QueryEscape("0%の"), nil, bobSession.AccessToken, http.StatusOK); len(literal.Items) != 1 || literal.Items[0].Message.Id != third.Id {
		t.Fatalf("percent must match literally: %+v", literal)
	}
	if literal := requestJSON[protocolgo.MessageSearchResultList](t, client, http.MethodGet, searchPath+url.QueryEscape("%%"), nil, bobSession.AccessToken, http.StatusOK); len(literal.Items) != 0 {
		t.Fatalf("wildcards must not match everything: %+v", literal)
	}
	firstPage := requestJSON[protocolgo.MessageSearchResultList](t, client, http.MethodGet, searchPath+url.QueryEscape("星空")+"&limit=2", nil, aliceSession.AccessToken, http.StatusOK)
	if len(firstPage.Items) != 2 || firstPage.Items[0].Message.Id != threadMessage.Id || !firstPage.Page.HasMore || firstPage.Page.NextCursor == nil {
		t.Fatalf("unexpected first search page: %+v", firstPage)
	}
	secondPage := requestJSON[protocolgo.MessageSearchResultList](t, client, http.MethodGet, searchPath+url.QueryEscape("星空")+"&limit=2&cursor="+*firstPage.Page.NextCursor, nil, aliceSession.AccessToken, http.StatusOK)
	if len(secondPage.Items) != 2 || secondPage.Page.HasMore || secondPage.Items[1].Message.Id != first.Id {
		t.Fatalf("unexpected second search page: %+v", secondPage)
	}
	byAuthor := requestJSON[protocolgo.MessageSearchResultList](t, client, http.MethodGet, searchPath+url.QueryEscape("星空")+"&author_id="+bob.Id.String(), nil, aliceSession.AccessToken, http.StatusOK)
	if len(byAuthor.Items) != 1 || byAuthor.Items[0].Message.Author.Id != bob.Id {
		t.Fatalf("unexpected author filter: %+v", byAuthor)
	}
	byChannel := requestJSON[protocolgo.MessageSearchResultList](t, client, http.MethodGet, searchPath+url.QueryEscape("星空")+"&channel_id="+other.Id.String(), nil, aliceSession.AccessToken, http.StatusOK)
	if len(byChannel.Items) != 1 || byChannel.Items[0].Message.Id != foreign.Id {
		t.Fatalf("unexpected channel filter: %+v", byChannel)
	}
	if byChannel.Items[0].Message.Author.Id != alice.Id {
		t.Fatalf("unexpected author: %+v", byChannel)
	}
	requestJSON[protocolgo.Error](t, client, http.MethodGet, searchPath+url.QueryEscape("星"), nil, aliceSession.AccessToken, http.StatusBadRequest)
	requestJSON[protocolgo.Error](t, client, http.MethodGet, searchPath+url.QueryEscape(strings.Repeat("あ", 101)), nil, aliceSession.AccessToken, http.StatusBadRequest)
	requestJSON[protocolgo.Error](t, client, http.MethodGet, guildPath+"/messages/search", nil, aliceSession.AccessToken, http.StatusBadRequest)
	requestJSON[protocolgo.Error](t, client, http.MethodGet, searchPath+"星空", nil, carolSession.AccessToken, http.StatusNotFound)

	// Excerpts are cut around the match and never exceed the protocol limit.
	post(aliceSession, text.Id, strings.Repeat("あ", 600)+"目印"+strings.Repeat("い", 100))
	excerpt := requestJSON[protocolgo.MessageSearchResultList](t, client, http.MethodGet, searchPath+url.QueryEscape("目印"), nil, aliceSession.AccessToken, http.StatusOK)
	if len(excerpt.Items) != 1 || !strings.HasPrefix(excerpt.Items[0].Excerpt, "…") || !strings.Contains(excerpt.Items[0].Excerpt, "目印") || len([]rune(excerpt.Items[0].Excerpt)) > 500 {
		t.Fatalf("unexpected excerpt: %q", excerpt.Items[0].Excerpt)
	}
}
