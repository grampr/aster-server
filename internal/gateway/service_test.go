package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/grampr/aster-server/internal/auth"
	"github.com/grampr/aster-server/internal/community"
)

type fakeAuthenticator struct {
	users map[string]auth.User
}

func (f fakeAuthenticator) Authenticate(_ context.Context, token string) (auth.User, error) {
	user, ok := f.users[token]
	if !ok {
		return auth.User{}, auth.ErrUnauthorized
	}
	return user, nil
}

type wireMessage struct {
	Op int             `json:"op"`
	T  string          `json:"t"`
	S  *int64          `json:"s"`
	D  json.RawMessage `json:"d"`
}

func TestGatewayIdentifyHeartbeatPublishAndResume(t *testing.T) {
	userID := uuid.MustParse("0198b8ef-1c5d-7b34-892e-81d2e1e2b090")
	service := newTestService(t, fakeAuthenticator{users: map[string]auth.User{
		"access-token":  {ID: userID},
		"rotated-token": {ID: userID},
	}})
	server := httptest.NewServer(service)
	defer server.Close()
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http")

	connection := dialGateway(t, endpoint, nil)
	assertOpcode(t, readGateway(t, connection), opHello)
	writeGateway(t, connection, map[string]any{
		"op": opIdentify,
		"d":  map[string]any{"token": "access-token", "intents": intentGuildMessages | intentMessageContent},
	})
	ready := readGateway(t, connection)
	assertDispatch(t, ready, eventReady, 0)
	var readyData struct {
		SessionID uuid.UUID `json:"session_id"`
	}
	if err := json.Unmarshal(ready.D, &readyData); err != nil || readyData.SessionID == uuid.Nil {
		t.Fatalf("invalid READY payload: %s (%v)", ready.D, err)
	}

	message := testMessage()
	service.PublishMessageCreate([]uuid.UUID{userID}, false, message)
	created := readGateway(t, connection)
	assertDispatch(t, created, eventMessageCreate, 1)
	var createdData messagePayload
	if err := json.Unmarshal(created.D, &createdData); err != nil {
		t.Fatal(err)
	}
	if createdData.Content == nil || *createdData.Content != message.Content {
		t.Fatalf("message content was not delivered: %+v", createdData)
	}
	if createdData.ReplyToMessageID == nil || createdData.ReplyTo == nil || createdData.ReplyTo.Content == nil || *createdData.ReplyTo.Content != message.ReplyTo.Content {
		t.Fatalf("message reply was not delivered: %+v", createdData)
	}

	writeGateway(t, connection, map[string]any{"op": opHeartbeat, "d": 1})
	assertOpcode(t, readGateway(t, connection), opHeartbeatAck)
	_ = connection.Close()
	waitFor(t, time.Second, func() bool {
		service.hub.mu.Lock()
		defer service.hub.mu.Unlock()
		s := service.hub.sessions[readyData.SessionID]
		return s != nil && s.client == nil
	})

	message.Content = "切断中に更新されました"
	service.PublishMessageUpdate([]uuid.UUID{userID}, false, message)
	resumedConnection := dialGateway(t, endpoint, nil)
	defer resumedConnection.Close()
	assertOpcode(t, readGateway(t, resumedConnection), opHello)
	writeGateway(t, resumedConnection, map[string]any{
		"op": opResume,
		"d":  map[string]any{"token": "rotated-token", "session_id": readyData.SessionID, "sequence": 1},
	})
	replayed := readGateway(t, resumedConnection)
	assertDispatch(t, replayed, eventMessageUpdate, 2)
	resumed := readGateway(t, resumedConnection)
	assertDispatch(t, resumed, eventResumed, 3)
}

func TestGatewayRedactsMessageContentWithoutIntent(t *testing.T) {
	userID := uuid.MustParse("0198b8ef-1c5d-7b34-892e-81d2e1e2b090")
	service := newTestService(t, fakeAuthenticator{users: map[string]auth.User{"access-token": {ID: userID}}})
	server := httptest.NewServer(service)
	defer server.Close()
	connection := dialGateway(t, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	defer connection.Close()
	assertOpcode(t, readGateway(t, connection), opHello)
	writeGateway(t, connection, map[string]any{
		"op": opIdentify, "d": map[string]any{"token": "access-token", "intents": intentGuildMessages},
	})
	assertDispatch(t, readGateway(t, connection), eventReady, 0)
	service.PublishMessageCreate([]uuid.UUID{userID}, false, testMessage())
	event := readGateway(t, connection)
	var payload messagePayload
	if err := json.Unmarshal(event.D, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Content != nil {
		t.Fatalf("content must be redacted without MESSAGE_CONTENT: %+v", payload)
	}
	if payload.ReplyTo == nil || payload.ReplyTo.Content != nil {
		t.Fatalf("reply content must be redacted without MESSAGE_CONTENT: %+v", payload)
	}
}

func TestGatewayPublishesReactionWithReactionIntent(t *testing.T) {
	userID := uuid.MustParse("0198b8ef-1c5d-7b34-892e-81d2e1e2b090")
	service := newTestService(t, fakeAuthenticator{users: map[string]auth.User{"access-token": {ID: userID}}})
	server := httptest.NewServer(service)
	defer server.Close()
	connection := dialGateway(t, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	defer connection.Close()
	assertOpcode(t, readGateway(t, connection), opHello)
	writeGateway(t, connection, map[string]any{
		"op": opIdentify, "d": map[string]any{"token": "access-token", "intents": intentReactions},
	})
	assertDispatch(t, readGateway(t, connection), eventReady, 0)

	reaction := MessageReaction{
		MessageID: uuid.MustParse("0198b8f2-4f80-7e67-b250-b4051415e3c2"),
		ChannelID: uuid.MustParse("0198b8f1-3e7f-7d56-a14f-a3f40304d2b1"),
		UserID:    userID, Emoji: "👍", Count: 2,
	}
	service.PublishMessageReaction([]uuid.UUID{userID}, true, reaction)
	event := readGateway(t, connection)
	assertDispatch(t, event, eventMessageReactionAdd, 1)
	var payload messageReactionPayload
	if err := json.Unmarshal(event.D, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.MessageID != reaction.MessageID || payload.UserID != userID || payload.Emoji != "👍" || payload.Count != 2 {
		t.Fatalf("unexpected reaction payload: %+v", payload)
	}
}

func TestGatewayPublishesTypingWithTypingIntent(t *testing.T) {
	userID := uuid.MustParse("0198b8ef-1c5d-7b34-892e-81d2e1e2b090")
	service := newTestService(t, fakeAuthenticator{users: map[string]auth.User{"access-token": {ID: userID}}})
	server := httptest.NewServer(service)
	defer server.Close()
	connection := dialGateway(t, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	defer connection.Close()
	assertOpcode(t, readGateway(t, connection), opHello)
	writeGateway(t, connection, map[string]any{
		"op": opIdentify, "d": map[string]any{"token": "access-token", "intents": intentTyping},
	})
	assertDispatch(t, readGateway(t, connection), eventReady, 0)

	startedAt := time.Date(2026, time.August, 17, 6, 16, 0, 0, time.UTC)
	typing := TypingStart{
		ChannelID: uuid.MustParse("0198b8f1-3e7f-7d56-a14f-a3f40304d2b1"),
		User: UserSummary{
			ID: userID, DisplayName: "Alice",
		},
		StartedAt: startedAt,
	}
	service.PublishTypingStart([]uuid.UUID{userID}, typing)
	event := readGateway(t, connection)
	assertDispatch(t, event, eventTypingStart, 1)
	var payload typingStartPayload
	if err := json.Unmarshal(event.D, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ChannelID != typing.ChannelID || payload.User.ID != userID || payload.User.DisplayName != "Alice" || !payload.StartedAt.Equal(startedAt) {
		t.Fatalf("unexpected typing payload: %+v", payload)
	}
}

func TestGatewayPublishesMemberAndPresenceEventsByIntent(t *testing.T) {
	userID := uuid.MustParse("0198b8ef-1c5d-7b34-892e-81d2e1e2b090")
	guildID := uuid.MustParse("0198b8f0-2d6e-7c45-9a3f-92e3f2f3c1a0")
	service := newTestService(t, fakeAuthenticator{users: map[string]auth.User{"access-token": {ID: userID}}})
	server := httptest.NewServer(service)
	defer server.Close()
	connection := dialGateway(t, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	defer connection.Close()
	assertOpcode(t, readGateway(t, connection), opHello)
	writeGateway(t, connection, map[string]any{"op": opIdentify, "d": map[string]any{"token": "access-token", "intents": intentGuildMembers | intentGuildPresences}})
	assertDispatch(t, readGateway(t, connection), eventReady, 0)

	updatedAt := time.Date(2026, time.September, 4, 2, 0, 0, 0, time.UTC)
	presence := community.Presence{UserID: userID, Status: "ONLINE", UpdatedAt: updatedAt}
	member := community.Member{GuildID: guildID, User: community.UserSummary{ID: userID, DisplayName: "Alice"}, RoleIDs: []uuid.UUID{}, JoinedAt: updatedAt, Presence: presence}
	service.PublishMemberJoin([]uuid.UUID{userID}, member)
	assertDispatch(t, readGateway(t, connection), eventMemberJoin, 1)
	service.PublishPresenceUpdate([]uuid.UUID{userID}, guildID, presence)
	event := readGateway(t, connection)
	assertDispatch(t, event, eventPresenceUpdate, 2)
	var payload presenceUpdatePayload
	if err := json.Unmarshal(event.D, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.GuildID != guildID || payload.Presence.UserID != userID || payload.Presence.Status != "ONLINE" {
		t.Fatalf("unexpected presence payload: %+v", payload)
	}
}

func TestGatewayPublishesDirectChannelAndReadStateEvents(t *testing.T) {
	userID := uuid.MustParse("0198b8ef-1c5d-7b34-892e-81d2e1e2b090")
	channelID := uuid.MustParse("0198b8f1-3e7f-7d56-a14f-a3f40304d2b1")
	service := newTestService(t, fakeAuthenticator{users: map[string]auth.User{"access-token": {ID: userID}}})
	server := httptest.NewServer(service)
	defer server.Close()
	connection := dialGateway(t, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	defer connection.Close()
	assertOpcode(t, readGateway(t, connection), opHello)
	writeGateway(t, connection, map[string]any{
		"op": opIdentify, "d": map[string]any{"token": "access-token", "intents": intentDirectMessages | intentMessageContent},
	})
	assertDispatch(t, readGateway(t, connection), eventReady, 0)

	createdAt := time.Date(2026, time.September, 4, 3, 0, 0, 0, time.UTC)
	service.PublishChannelCreate([]uuid.UUID{userID}, true, Channel{
		ID: channelID, Type: "DIRECT", CreatedAt: createdAt,
		Recipients: []UserSummary{{ID: userID, DisplayName: "Alice"}},
	})
	channelEvent := readGateway(t, connection)
	assertDispatch(t, channelEvent, eventChannelCreate, 1)
	var channel channelPayload
	if err := json.Unmarshal(channelEvent.D, &channel); err != nil {
		t.Fatal(err)
	}
	if channel.ID != channelID || channel.GuildID != nil || channel.Type != "DIRECT" || len(channel.Recipients) != 1 {
		t.Fatalf("unexpected direct channel payload: %+v", channel)
	}

	message := testMessage()
	message.ChannelID = channelID
	service.PublishMessageCreate([]uuid.UUID{userID}, true, message)
	assertDispatch(t, readGateway(t, connection), eventMessageCreate, 2)

	service.PublishReadStateUpdate(userID, ReadState{ChannelID: channelID, LastReadMessageID: &message.ID, UpdatedAt: createdAt})
	readEvent := readGateway(t, connection)
	assertDispatch(t, readEvent, eventReadStateUpdate, 3)
	var state readStatePayload
	if err := json.Unmarshal(readEvent.D, &state); err != nil {
		t.Fatal(err)
	}
	if state.ChannelID != channelID || state.LastReadMessageID == nil || *state.LastReadMessageID != message.ID {
		t.Fatalf("unexpected read state payload: %+v", state)
	}
}

func TestGatewayClosesWhenAccessTokenIsNoLongerValid(t *testing.T) {
	userID := uuid.MustParse("0198b8ef-1c5d-7b34-892e-81d2e1e2b090")
	authenticator := fakeAuthenticator{users: map[string]auth.User{"access-token": {ID: userID}}}
	service := newTestService(t, authenticator)
	server := httptest.NewServer(service)
	defer server.Close()
	connection := dialGateway(t, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	defer connection.Close()
	assertOpcode(t, readGateway(t, connection), opHello)
	writeGateway(t, connection, map[string]any{
		"op": opIdentify, "d": map[string]any{"token": "access-token", "intents": 0},
	})
	assertDispatch(t, readGateway(t, connection), eventReady, 0)
	delete(authenticator.users, "access-token")
	writeGateway(t, connection, map[string]any{"op": opHeartbeat, "d": 0})
	_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err := connection.ReadMessage()
	var closeError *websocket.CloseError
	if !errors.As(err, &closeError) || closeError.Code != closeUnauthorized {
		t.Fatalf("expected unauthorized close, got %v", err)
	}
}

func TestGatewayRejectsUnlistedCrossOrigin(t *testing.T) {
	service := newTestService(t, fakeAuthenticator{users: map[string]auth.User{}})
	server := httptest.NewServer(service)
	defer server.Close()
	header := http.Header{"Origin": []string{"https://untrusted.example"}}
	_, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), header)
	if response != nil {
		defer response.Body.Close()
	}
	if err == nil {
		t.Fatal("unlisted cross-origin connection must be rejected")
	}
	if response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 response, got %+v", response)
	}
}

func newTestService(t *testing.T, authenticator Authenticator) *Service {
	t.Helper()
	service, err := New(authenticator, Config{
		URL: "ws://gateway.example/gateway/v1", HeartbeatInterval: time.Second,
		IdentifyTimeout: time.Second, SessionRetention: time.Minute,
		EventBufferSize: 16, AllowedOrigins: []string{"http://localhost:5173"},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func dialGateway(t *testing.T, endpoint string, header http.Header) *websocket.Conn {
	t.Helper()
	connection, response, err := websocket.DefaultDialer.Dial(endpoint, header)
	if err != nil {
		if response != nil {
			_ = response.Body.Close()
		}
		t.Fatal(err)
	}
	return connection
}

func readGateway(t *testing.T, connection *websocket.Conn) wireMessage {
	t.Helper()
	_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	var message wireMessage
	if err := connection.ReadJSON(&message); err != nil {
		t.Fatal(err)
	}
	return message
}

func writeGateway(t *testing.T, connection *websocket.Conn, message any) {
	t.Helper()
	if err := connection.WriteJSON(message); err != nil {
		t.Fatal(err)
	}
}

func assertOpcode(t *testing.T, message wireMessage, opcode int) {
	t.Helper()
	if message.Op != opcode {
		t.Fatalf("expected opcode %d, got %+v", opcode, message)
	}
}

func assertDispatch(t *testing.T, message wireMessage, event string, sequence int64) {
	t.Helper()
	if message.Op != opDispatch || message.T != event || message.S == nil || *message.S != sequence {
		t.Fatalf("unexpected dispatch: %+v", message)
	}
}

func testMessage() Message {
	replyID := uuid.MustParse("0198b8f0-1b72-73a2-a2ef-75cf3cd276d8")
	return Message{
		ID:               uuid.MustParse("0198b8f2-4f80-7e67-b250-b4051415e3c2"),
		ChannelID:        uuid.MustParse("0198b8f1-3e7f-7d56-a14f-a3f40304d2b1"),
		Author:           UserSummary{ID: uuid.MustParse("0198b8ef-1c5d-7b34-892e-81d2e1e2b090"), DisplayName: "Alice"},
		Content:          "10月24日で進める方向でよいでしょうか？",
		ReplyToMessageID: &replyID,
		ReplyTo: &MessageReply{
			ID: replyID, ChannelID: uuid.MustParse("0198b8f1-3e7f-7d56-a14f-a3f40304d2b1"),
			Author:  UserSummary{ID: uuid.MustParse("0198b8ed-7ba1-7165-b028-9ecf14ed1e7b"), DisplayName: "Bob"},
			Content: "10月24日で進めませんか？", CreatedAt: time.Date(2026, 8, 17, 6, 10, 0, 0, time.UTC),
		},
		CreatedAt: time.Date(2026, 8, 17, 6, 12, 0, 0, time.UTC),
	}
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(errors.New("condition was not met before timeout"))
}
