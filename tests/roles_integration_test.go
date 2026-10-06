package tests

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	protocolgo "github.com/grampr/Aster-protocol/packages/protocol-go/generated"
	"github.com/grampr/aster-server/internal/auth"
	"github.com/grampr/aster-server/internal/chat"
	"github.com/grampr/aster-server/internal/httpapi"
	postgresplatform "github.com/grampr/aster-server/internal/platform/postgres"
	"github.com/grampr/aster-server/migrations"
)

func TestRolesAndPermissions(t *testing.T) {
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
	server := httptest.NewServer(httpapi.New(authService, chatService, nil, logger, "test"))
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

	guild := requestJSON[protocolgo.Guild](t, client, http.MethodPost, base+"/guilds", protocolgo.CreateGuildRequest{Name: "星屑コミュニティ"}, aliceSession.AccessToken, http.StatusCreated)
	guildPath := base + "/guilds/" + guild.Id.String()
	invite := requestJSON[protocolgo.Invite](t, client, http.MethodPost, guildPath+"/invites", map[string]any{}, aliceSession.AccessToken, http.StatusCreated)
	for _, session := range []protocolgo.SessionTokenResponse{bobSession, carolSession} {
		requestJSON[protocolgo.GuildMember](t, client, http.MethodPost, base+"/invites/"+invite.Code+"/accept", nil, session.AccessToken, http.StatusOK)
	}

	// Every Guild has a managed default Role, visible only to members.
	requestJSON[protocolgo.Error](t, client, http.MethodGet, guildPath+"/roles", nil, daveSession.AccessToken, http.StatusNotFound)
	roles := requestJSON[protocolgo.RoleList](t, client, http.MethodGet, guildPath+"/roles", nil, bobSession.AccessToken, http.StatusOK)
	if len(roles.Items) != 1 || !roles.Items[0].Managed || roles.Items[0].Position != 0 || roles.Items[0].Name != "@everyone" || roles.Items[0].Permissions != 1795 {
		t.Fatalf("unexpected default roles: %+v", roles)
	}
	everyone := roles.Items[0]

	// Members without MANAGE_ROLES cannot create Roles, and bad input is rejected.
	requestJSON[protocolgo.Error](t, client, http.MethodPost, guildPath+"/roles", map[string]any{"name": "x"}, bobSession.AccessToken, http.StatusForbidden)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, guildPath+"/roles", map[string]any{"name": "x", "color": "red"}, aliceSession.AccessToken, http.StatusBadRequest)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, guildPath+"/roles", map[string]any{"name": "x", "permissions": 1 << 20}, aliceSession.AccessToken, http.StatusBadRequest)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, guildPath+"/roles", map[string]any{"name": "  "}, aliceSession.AccessToken, http.StatusBadRequest)

	const (
		sendMessages   = 1 << 1
		manageMessages = 1 << 2
		manageGuild    = 1 << 4
		manageRoles    = 1 << 5
		manageMembers  = 1 << 6
		createInvite   = 1 << 7
	)
	helper := requestJSON[protocolgo.Role](t, client, http.MethodPost, guildPath+"/roles", map[string]any{"name": "Helper", "permissions": sendMessages}, aliceSession.AccessToken, http.StatusCreated)
	moderator := requestJSON[protocolgo.Role](t, client, http.MethodPost, guildPath+"/roles", map[string]any{
		"name": "Moderator", "color": "#FF0000", "permissions": manageMessages | manageRoles | manageMembers | createInvite,
	}, aliceSession.AccessToken, http.StatusCreated)
	if helper.Position != 1 || moderator.Position != 2 || moderator.Managed || moderator.Color == nil || *moderator.Color != "#FF0000" {
		t.Fatalf("unexpected roles: %+v %+v", helper, moderator)
	}

	// Assigning a Role changes the Member's permissions.
	assigned := requestJSON[protocolgo.GuildMember](t, client, http.MethodPatch, guildPath+"/members/"+bob.Id.String(), map[string]any{"role_ids": []uuid.UUID{moderator.Id}}, aliceSession.AccessToken, http.StatusOK)
	if len(assigned.RoleIds) != 1 || assigned.RoleIds[0] != moderator.Id {
		t.Fatalf("unexpected role assignment: %+v", assigned)
	}
	requestJSON[protocolgo.Error](t, client, http.MethodPatch, guildPath+"/members/"+bob.Id.String(), map[string]any{"role_ids": []uuid.UUID{everyone.Id}}, aliceSession.AccessToken, http.StatusBadRequest)
	requestJSON[protocolgo.Error](t, client, http.MethodPatch, guildPath+"/members/"+bob.Id.String(), map[string]any{"role_ids": []uuid.UUID{uuid.New()}}, aliceSession.AccessToken, http.StatusBadRequest)
	requestJSON[protocolgo.Invite](t, client, http.MethodPost, guildPath+"/invites", map[string]any{}, bobSession.AccessToken, http.StatusCreated)
	requestJSON[protocolgo.Error](t, client, http.MethodPost, guildPath+"/invites", map[string]any{}, carolSession.AccessToken, http.StatusForbidden)

	// A moderator manages only Roles below their own and cannot grant permissions they lack.
	guest := requestJSON[protocolgo.Role](t, client, http.MethodPost, guildPath+"/roles", map[string]any{"name": "Guest", "permissions": 0}, bobSession.AccessToken, http.StatusCreated)
	if guest.Position != 1 {
		t.Fatalf("a delegated role must start at the bottom: %+v", guest)
	}
	requestJSON[protocolgo.Error](t, client, http.MethodPost, guildPath+"/roles", map[string]any{"name": "Admin", "permissions": manageGuild}, bobSession.AccessToken, http.StatusForbidden)
	requestJSON[protocolgo.Error](t, client, http.MethodPatch, guildPath+"/roles/"+moderator.Id.String(), map[string]any{"name": "乗っ取り"}, bobSession.AccessToken, http.StatusForbidden)
	requestJSON[protocolgo.Error](t, client, http.MethodPatch, guildPath+"/roles/"+helper.Id.String(), map[string]any{"permissions": sendMessages | manageGuild}, bobSession.AccessToken, http.StatusForbidden)
	requestJSON[protocolgo.Error](t, client, http.MethodPatch, guildPath+"/roles/"+helper.Id.String(), map[string]any{"position": 2}, bobSession.AccessToken, http.StatusForbidden)
	renamed := requestJSON[protocolgo.Role](t, client, http.MethodPatch, guildPath+"/roles/"+helper.Id.String(), map[string]any{"name": "Chatter", "color": nil}, bobSession.AccessToken, http.StatusOK)
	if renamed.Name != "Chatter" || renamed.Color != nil {
		t.Fatalf("unexpected renamed role: %+v", renamed)
	}
	requestJSON[protocolgo.Error](t, client, http.MethodPatch, guildPath+"/roles/"+everyone.Id.String(), map[string]any{"name": "x"}, aliceSession.AccessToken, http.StatusBadRequest)
	requestJSON[protocolgo.Error](t, client, http.MethodPatch, guildPath+"/roles/"+helper.Id.String(), map[string]any{}, aliceSession.AccessToken, http.StatusBadRequest)
	requestJSON[protocolgo.Error](t, client, http.MethodPatch, guildPath+"/members/"+carol.Id.String(), map[string]any{"role_ids": []uuid.UUID{moderator.Id}}, bobSession.AccessToken, http.StatusForbidden)
	requestJSON[protocolgo.Error](t, client, http.MethodPatch, guildPath+"/members/"+alice.Id.String(), map[string]any{"nickname": "x"}, bobSession.AccessToken, http.StatusForbidden)
	requestJSON[protocolgo.Error](t, client, http.MethodDelete, guildPath+"/members/"+alice.Id.String(), nil, bobSession.AccessToken, http.StatusForbidden)
	requestJSON[protocolgo.Error](t, client, http.MethodDelete, guildPath+"/roles/"+everyone.Id.String(), nil, aliceSession.AccessToken, http.StatusForbidden)
	requestJSON[protocolgo.Error](t, client, http.MethodDelete, guildPath+"/roles/"+moderator.Id.String(), nil, bobSession.AccessToken, http.StatusForbidden)

	// SEND_MESSAGES comes from the default Role plus assigned Roles.
	textChannel := requestJSON[protocolgo.Channel](t, client, http.MethodPost, guildPath+"/channels", protocolgo.CreateChannelRequest{Type: protocolgo.CreateChannelRequestTypeTEXT, Name: "雑談"}, aliceSession.AccessToken, http.StatusCreated)
	messagesPath := base + "/channels/" + textChannel.Id.String() + "/messages"
	requestJSON[protocolgo.Error](t, client, http.MethodPost, base+"/guilds/"+guild.Id.String()+"/channels", protocolgo.CreateChannelRequest{Type: protocolgo.CreateChannelRequestTypeTEXT, Name: "不可"}, bobSession.AccessToken, http.StatusForbidden)
	requestJSON[protocolgo.Message](t, client, http.MethodPost, messagesPath, protocolgo.CreateMessageRequest{Content: stringPointer("投稿できる")}, carolSession.AccessToken, http.StatusCreated)
	revoked := requestJSON[protocolgo.Role](t, client, http.MethodPatch, guildPath+"/roles/"+everyone.Id.String(), map[string]any{"permissions": 1795 &^ sendMessages}, aliceSession.AccessToken, http.StatusOK)
	if revoked.Permissions != 1795&^sendMessages {
		t.Fatalf("unexpected default permissions: %+v", revoked)
	}
	requestJSON[protocolgo.Error](t, client, http.MethodPost, messagesPath, protocolgo.CreateMessageRequest{Content: stringPointer("投稿できない")}, carolSession.AccessToken, http.StatusForbidden)
	requestJSON[protocolgo.GuildMember](t, client, http.MethodPatch, guildPath+"/members/"+carol.Id.String(), map[string]any{"role_ids": []uuid.UUID{helper.Id}}, bobSession.AccessToken, http.StatusOK)
	posted := requestJSON[protocolgo.Message](t, client, http.MethodPost, messagesPath, protocolgo.CreateMessageRequest{Content: stringPointer("Roleで投稿できる")}, carolSession.AccessToken, http.StatusCreated)

	// MANAGE_MESSAGES lets a moderator delete another member's message; others cannot.
	requestJSON[protocolgo.Error](t, client, http.MethodDelete, messagesPath+"/"+posted.Id.String(), nil, daveSession.AccessToken, http.StatusNotFound)
	requestJSON[struct{}](t, client, http.MethodDelete, messagesPath+"/"+posted.Id.String(), nil, bobSession.AccessToken, http.StatusNoContent)

	// Deleting a Role removes it from its Members.
	requestJSON[struct{}](t, client, http.MethodDelete, guildPath+"/roles/"+helper.Id.String(), nil, bobSession.AccessToken, http.StatusNoContent)
	if member := requestJSON[protocolgo.GuildMember](t, client, http.MethodGet, guildPath+"/members/"+carol.Id.String(), nil, carolSession.AccessToken, http.StatusOK); len(member.RoleIds) != 0 {
		t.Fatalf("deleted role must be unassigned: %+v", member)
	}
	requestJSON[protocolgo.Error](t, client, http.MethodPost, messagesPath, protocolgo.CreateMessageRequest{Content: stringPointer("また投稿できない")}, carolSession.AccessToken, http.StatusForbidden)

	// Removing a member needs MANAGE_MEMBERS and a higher Role.
	requestJSON[protocolgo.Error](t, client, http.MethodDelete, guildPath+"/members/"+bob.Id.String(), nil, carolSession.AccessToken, http.StatusForbidden)
	requestJSON[struct{}](t, client, http.MethodDelete, guildPath+"/members/"+carol.Id.String(), nil, bobSession.AccessToken, http.StatusNoContent)
}
