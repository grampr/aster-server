package tests

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/grampr/aster-server/internal/media"
	postgresplatform "github.com/grampr/aster-server/internal/platform/postgres"
	"github.com/grampr/aster-server/migrations"
)

func TestAttachments(t *testing.T) {
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
	if _, err := pool.Exec(ctx, `TRUNCATE messages, channels, guild_members, guilds, session_refresh_tokens, sessions, auth_identities, users, storage_deletions CASCADE`); err != nil {
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
	chatStore := chat.NewPostgresStore(pool)
	storage := media.NewMemoryStorage()
	chatService, err := chat.NewService(chatStore)
	if err != nil {
		t.Fatal(err)
	}
	chatService.WithStorage(storage)
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
	janitor := media.NewJanitor(chatStore, storage, logger)

	aliceSession := registerTestUser(t, server, "alice@example.com", "Alice")
	bobSession := registerTestUser(t, server, "bob@example.com", "Bob")
	carolSession := registerTestUser(t, server, "carol@example.com", "Carol")

	bobGateway, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/gateway/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bobGateway.Close()
	readGatewayMessage(t, bobGateway)
	if err := bobGateway.WriteJSON(map[string]any{"op": 2, "d": map[string]any{"token": bobSession.AccessToken, "intents": 4 | 16}}); err != nil {
		t.Fatal(err)
	}
	readGatewayMessage(t, bobGateway)

	guild := requestJSON[protocolgo.Guild](t, client, http.MethodPost, base+"/guilds", protocolgo.CreateGuildRequest{Name: "星屑コミュニティ"}, aliceSession.AccessToken, http.StatusCreated)
	guildPath := base + "/guilds/" + guild.Id.String()
	invite := requestJSON[protocolgo.Invite](t, client, http.MethodPost, guildPath+"/invites", map[string]any{}, aliceSession.AccessToken, http.StatusCreated)
	requestJSON[protocolgo.GuildMember](t, client, http.MethodPost, base+"/invites/"+invite.Code+"/accept", nil, bobSession.AccessToken, http.StatusOK)
	text := requestJSON[protocolgo.Channel](t, client, http.MethodPost, guildPath+"/channels", protocolgo.CreateChannelRequest{Type: protocolgo.CreateChannelRequestTypeTEXT, Name: "雑談"}, aliceSession.AccessToken, http.StatusCreated)
	other := requestJSON[protocolgo.Channel](t, client, http.MethodPost, guildPath+"/channels", protocolgo.CreateChannelRequest{Type: protocolgo.CreateChannelRequestTypeTEXT, Name: "別件"}, aliceSession.AccessToken, http.StatusCreated)
	voice := requestJSON[protocolgo.Channel](t, client, http.MethodPost, guildPath+"/channels", protocolgo.CreateChannelRequest{Type: protocolgo.CreateChannelRequestTypeVOICE, Name: "通話"}, aliceSession.AccessToken, http.StatusCreated)
	intentPath := base + "/channels/" + text.Id.String() + "/attachments/intents"

	digest := sha256.Sum256([]byte("hello"))
	declaration := func(overrides map[string]any) map[string]any {
		body := map[string]any{"filename": "メモ.txt", "content_type": "text/plain; charset=utf-8", "size": 5, "checksum_sha256": strings.ToUpper(hex.EncodeToString(digest[:]))}
		for key, value := range overrides {
			body[key] = value
		}
		return body
	}

	// Intents validate the declaration and the caller's access to the Channel.
	for name, overrides := range map[string]map[string]any{
		"empty file":     {"size": 0},
		"too large":      {"size": 25<<20 + 1},
		"bad checksum":   {"checksum_sha256": "abc"},
		"path separator": {"filename": "../etc/passwd"},
		"blank name":     {"filename": "  "},
		"bad type":       {"content_type": "plain"},
	} {
		if status := requestJSON[protocolgo.Error](t, client, http.MethodPost, intentPath, declaration(overrides), aliceSession.AccessToken, http.StatusBadRequest); status.Code != "INVALID_REQUEST" {
			t.Fatalf("%s: unexpected error %+v", name, status)
		}
	}
	requestJSON[protocolgo.Error](t, client, http.MethodPost, intentPath, declaration(nil), carolSession.AccessToken, http.StatusNotFound)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/channels/"+voice.Id.String()+"/attachments/intents", declaration(nil), aliceSession.AccessToken, http.StatusNotFound)

	intent := requestJSON[protocolgo.AttachmentUploadIntent](t, client, http.MethodPost, intentPath, declaration(nil), aliceSession.AccessToken, http.StatusCreated)
	attachment := intent.Attachment
	if attachment.Status != protocolgo.PENDING || attachment.ContentType != "text/plain" || attachment.ChecksumSha256 != hex.EncodeToString(digest[:]) ||
		attachment.DownloadUrl != "/api/v1/attachments/"+attachment.Id.String()+"/content" || attachment.ChannelId != text.Id ||
		intent.UploadMethod != protocolgo.PUT || intent.UploadUrl == "" || intent.UploadHeaders["Content-Type"] != "text/plain" {
		t.Fatalf("unexpected upload intent: %+v", intent)
	}
	var objectKey string
	if err := pool.QueryRow(ctx, `SELECT object_key FROM attachments WHERE id = $1`, attachment.Id).Scan(&objectKey); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(objectKey, "メモ") || !strings.HasSuffix(objectKey, attachment.Id.String()) {
		t.Fatalf("the object key must not contain client-provided names: %s", objectKey)
	}
	attachmentPath := base + "/attachments/" + attachment.Id.String()

	// A pending upload is private to its uploader and cannot be finalized or downloaded.
	requestJSON[protocolgo.Error](t, client, http.MethodGet, attachmentPath, nil, bobSession.AccessToken, http.StatusNotFound)
	requestJSON[protocolgo.Attachment](t, client, http.MethodGet, attachmentPath, nil, aliceSession.AccessToken, http.StatusOK)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, attachmentPath+"/download-intents", nil, aliceSession.AccessToken, http.StatusNotFound)
	if refused := requestJSON[protocolgo.Error](t, client, http.MethodPost, attachmentPath+"/finalize", nil, aliceSession.AccessToken, http.StatusConflict); refused.Code != "ATTACHMENT_MISMATCH" {
		t.Fatalf("a missing object must not finalize: %+v", refused)
	}
	storage.Put(objectKey, "text/plain", []byte("hello!"))
	requestJSON[protocolgo.Error](t, client, http.MethodPost, attachmentPath+"/finalize", nil, aliceSession.AccessToken, http.StatusConflict)
	storage.Put(objectKey, "text/html", []byte("hello"))
	requestJSON[protocolgo.Error](t, client, http.MethodPost, attachmentPath+"/finalize", nil, aliceSession.AccessToken, http.StatusConflict)
	storage.Put(objectKey, "text/plain", []byte("HELLO"))
	requestJSON[protocolgo.Error](t, client, http.MethodPost, attachmentPath+"/finalize", nil, aliceSession.AccessToken, http.StatusConflict)
	storage.Put(objectKey, "text/plain", []byte("hello"))
	requestJSON[protocolgo.Error](t, client, http.MethodPost, attachmentPath+"/finalize", nil, bobSession.AccessToken, http.StatusNotFound)
	ready := requestJSON[protocolgo.Attachment](t, client, http.MethodPost, attachmentPath+"/finalize", nil, aliceSession.AccessToken, http.StatusOK)
	if ready.Status != protocolgo.READY {
		t.Fatalf("unexpected finalized attachment: %+v", ready)
	}
	requestJSON[protocolgo.Attachment](t, client, http.MethodPost, attachmentPath+"/finalize", nil, aliceSession.AccessToken, http.StatusOK)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, attachmentPath+"/finalize", nil, bobSession.AccessToken, http.StatusForbidden)
	requestJSON[protocolgo.Attachment](t, client, http.MethodGet, attachmentPath, nil, bobSession.AccessToken, http.StatusOK)
	requestJSON[protocolgo.Error](t, client, http.MethodGet, attachmentPath, nil, carolSession.AccessToken, http.StatusNotFound)

	// Messages take attachments alone or with text, once each, in the right Channel.
	messagesPath := base + "/channels/" + text.Id.String() + "/messages"
	badRequests := []protocolgo.CreateMessageRequest{
		{},
		{AttachmentIds: &[]uuid.UUID{uuid.New()}},
		{AttachmentIds: &[]uuid.UUID{attachment.Id, attachment.Id}},
	}
	for _, body := range badRequests {
		requestJSON[protocolgo.Error](t, client, http.MethodPost, messagesPath, body, aliceSession.AccessToken, http.StatusBadRequest)
	}
	requestJSON[protocolgo.Error](t, client, http.MethodPost, messagesPath, protocolgo.CreateMessageRequest{AttachmentIds: &[]uuid.UUID{attachment.Id}}, bobSession.AccessToken, http.StatusBadRequest)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/channels/"+other.Id.String()+"/messages", protocolgo.CreateMessageRequest{AttachmentIds: &[]uuid.UUID{attachment.Id}}, aliceSession.AccessToken, http.StatusBadRequest)
	message := requestJSON[protocolgo.Message](t, client, http.MethodPost, messagesPath, protocolgo.CreateMessageRequest{AttachmentIds: &[]uuid.UUID{attachment.Id}}, aliceSession.AccessToken, http.StatusCreated)
	if message.Content != "" || len(message.Attachments) != 1 || message.Attachments[0].Id != attachment.Id || message.Attachments[0].Status != protocolgo.READY {
		t.Fatalf("unexpected message: %+v", message)
	}
	event := readGatewayMessage(t, bobGateway)
	var eventData struct {
		Attachments []struct {
			ID          uuid.UUID `json:"id"`
			DownloadURL string    `json:"download_url"`
			Status      string    `json:"status"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(event.Data, &eventData); err != nil {
		t.Fatal(err)
	}
	if event.Type != "MESSAGE_CREATE" || len(eventData.Attachments) != 1 || eventData.Attachments[0].ID != attachment.Id || eventData.Attachments[0].Status != "READY" {
		t.Fatalf("unexpected message event: %s %+v", event.Type, eventData)
	}
	requestJSON[protocolgo.Error](t, client, http.MethodPost, messagesPath, protocolgo.CreateMessageRequest{AttachmentIds: &[]uuid.UUID{attachment.Id}}, aliceSession.AccessToken, http.StatusBadRequest)
	listed := requestJSON[protocolgo.MessageList](t, client, http.MethodGet, messagesPath, nil, bobSession.AccessToken, http.StatusOK)
	if len(listed.Items) != 1 || len(listed.Items[0].Attachments) != 1 {
		t.Fatalf("listed messages must carry attachments: %+v", listed)
	}

	// Downloads redirect to a short-lived URL after the access check.
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	redirectRequest, _ := http.NewRequest(http.MethodGet, attachmentPath+"/content", nil)
	redirectRequest.Header.Set("Authorization", "Bearer "+bobSession.AccessToken)
	redirect, err := noRedirect.Do(redirectRequest)
	if err != nil {
		t.Fatal(err)
	}
	redirect.Body.Close()
	if redirect.StatusCode != http.StatusSeeOther || redirect.Header.Get("Location") != "memory://download/"+objectKey || redirect.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected download redirect: %d %v", redirect.StatusCode, redirect.Header)
	}
	downloadIntent := requestJSON[protocolgo.AttachmentDownloadIntent](t, client, http.MethodPost, attachmentPath+"/download-intents", nil, bobSession.AccessToken, http.StatusOK)
	if downloadIntent.DownloadUrl != "memory://download/"+objectKey || !downloadIntent.ExpiresAt.After(time.Now()) {
		t.Fatalf("unexpected download intent: %+v", downloadIntent)
	}
	requestJSON[protocolgo.Error](t, client, http.MethodPost, attachmentPath+"/download-intents", nil, carolSession.AccessToken, http.StatusNotFound)

	// Only unused attachments can be deleted, and only by the uploader.
	requestJSON[protocolgo.Error](t, client, http.MethodDelete, attachmentPath, nil, aliceSession.AccessToken, http.StatusConflict)
	spare := requestJSON[protocolgo.AttachmentUploadIntent](t, client, http.MethodPost, intentPath, declaration(nil), aliceSession.AccessToken, http.StatusCreated).Attachment
	var spareKey string
	if err := pool.QueryRow(ctx, `SELECT object_key FROM attachments WHERE id = $1`, spare.Id).Scan(&spareKey); err != nil {
		t.Fatal(err)
	}
	storage.Put(spareKey, "text/plain", []byte("hello"))
	requestJSON[protocolgo.Attachment](t, client, http.MethodPost, base+"/attachments/"+spare.Id.String()+"/finalize", nil, aliceSession.AccessToken, http.StatusOK)
	requestJSON[protocolgo.Error](t, client, http.MethodDelete, base+"/attachments/"+spare.Id.String(), nil, bobSession.AccessToken, http.StatusForbidden)
	requestJSON[struct{}](t, client, http.MethodDelete, base+"/attachments/"+spare.Id.String(), nil, aliceSession.AccessToken, http.StatusNoContent)
	requestJSON[protocolgo.Error](t, client, http.MethodGet, base+"/attachments/"+spare.Id.String(), nil, aliceSession.AccessToken, http.StatusNotFound)

	// The janitor removes Objects of deleted attachments, including those that go away
	// with their Message.
	janitor.Sweep(ctx)
	if storage.Has(spareKey) || !storage.Has(objectKey) {
		t.Fatalf("only the deleted attachment's object must be removed: spare=%v used=%v", storage.Has(spareKey), storage.Has(objectKey))
	}
	requestJSON[struct{}](t, client, http.MethodDelete, messagesPath+"/"+message.Id.String(), nil, aliceSession.AccessToken, http.StatusNoContent)
	janitor.Sweep(ctx)
	if storage.Has(objectKey) {
		t.Fatal("deleting a message must remove its attachment objects")
	}
	var queued int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM storage_deletions`).Scan(&queued); err != nil || queued != 0 {
		t.Fatalf("the deletion queue must drain: %d %v", queued, err)
	}

	// Abandoned uploads expire, and the number of unused attachments is capped.
	stale := requestJSON[protocolgo.AttachmentUploadIntent](t, client, http.MethodPost, intentPath, declaration(nil), aliceSession.AccessToken, http.StatusCreated).Attachment
	if _, err := pool.Exec(ctx, `UPDATE attachments SET created_at = now() - interval '2 hours' WHERE id = $1`, stale.Id); err != nil {
		t.Fatal(err)
	}
	janitor.Sweep(ctx)
	requestJSON[protocolgo.Error](t, client, http.MethodGet, base+"/attachments/"+stale.Id.String(), nil, aliceSession.AccessToken, http.StatusNotFound)
	for index := 0; index < 25; index++ {
		requestJSON[protocolgo.AttachmentUploadIntent](t, client, http.MethodPost, intentPath, declaration(nil), aliceSession.AccessToken, http.StatusCreated)
	}
	if limited := requestJSON[protocolgo.Error](t, client, http.MethodPost, intentPath, declaration(nil), aliceSession.AccessToken, http.StatusTooManyRequests); limited.Code != "UPLOAD_QUOTA_EXCEEDED" {
		t.Fatalf("unexpected quota error: %+v", limited)
	}

	// Without an Object Storage, attachments report themselves unavailable.
	bare, err := chat.NewService(chatStore)
	if err != nil {
		t.Fatal(err)
	}
	bareServer := httptest.NewServer(httpapi.New(authService, bare, nil, logger, "test"))
	defer bareServer.Close()
	if unavailable := requestJSON[protocolgo.Error](t, bareServer.Client(), http.MethodPost, bareServer.URL+"/api/v1/channels/"+text.Id.String()+"/attachments/intents", declaration(nil), bobSession.AccessToken, http.StatusServiceUnavailable); unavailable.Code != "STORAGE_UNAVAILABLE" {
		t.Fatalf("unexpected error: %+v", unavailable)
	}
}
