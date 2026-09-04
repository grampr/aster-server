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

	protocolgo "github.com/grampr/Aster-protocol/packages/protocol-go/generated"
	"github.com/grampr/aster-server/internal/auth"
	"github.com/grampr/aster-server/internal/chat"
	"github.com/grampr/aster-server/internal/community"
	"github.com/grampr/aster-server/internal/httpapi"
	postgresplatform "github.com/grampr/aster-server/internal/platform/postgres"
	"github.com/grampr/aster-server/migrations"
)

func TestCommunityLifecycle(t *testing.T) {
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
	if _, err := pool.Exec(ctx, `TRUNCATE users CASCADE`); err != nil {
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
	communityService, err := community.NewService(community.NewPostgresStore(pool))
	if err != nil {
		t.Fatal(err)
	}
	chatService, err := chat.NewService(chat.NewPostgresStore(pool), communityService)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(authService, chatService, communityService, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), "test"))
	defer server.Close()

	aliceSession := registerTestUser(t, server, "community-alice@example.com", "Alice")
	bobSession := registerTestUser(t, server, "community-bob@example.com", "Bob")
	alice := requestJSON[protocolgo.UserSelf](t, server.Client(), http.MethodGet, server.URL+"/api/v1/users/@me", nil, aliceSession.AccessToken, http.StatusOK)
	bob := requestJSON[protocolgo.UserSelf](t, server.Client(), http.MethodGet, server.URL+"/api/v1/users/@me", nil, bobSession.AccessToken, http.StatusOK)
	guild := requestJSON[protocolgo.Guild](t, server.Client(), http.MethodPost, server.URL+"/api/v1/guilds", protocolgo.CreateGuildRequest{Name: "Community Test"}, aliceSession.AccessToken, http.StatusCreated)
	membersURL := server.URL + "/api/v1/guilds/" + guild.Id.String() + "/members"
	requestJSON[protocolgo.Error](t, server.Client(), http.MethodGet, membersURL, nil, bobSession.AccessToken, http.StatusNotFound)

	expires := 3600
	maxUses := 2
	invite := requestJSON[protocolgo.Invite](t, server.Client(), http.MethodPost, server.URL+"/api/v1/guilds/"+guild.Id.String()+"/invites", protocolgo.CreateInviteRequest{ExpiresIn: &expires, MaxUses: &maxUses}, aliceSession.AccessToken, http.StatusCreated)
	accepted := requestJSON[protocolgo.GuildMember](t, server.Client(), http.MethodPost, server.URL+"/api/v1/invites/"+invite.Code+"/accept", nil, bobSession.AccessToken, http.StatusOK)
	if accepted.User.Id != bob.Id || accepted.GuildId != guild.Id {
		t.Fatalf("unexpected accepted member: %+v", accepted)
	}
	requestJSON[protocolgo.GuildMember](t, server.Client(), http.MethodPost, server.URL+"/api/v1/invites/"+invite.Code+"/accept", nil, bobSession.AccessToken, http.StatusOK)
	members := requestJSON[protocolgo.GuildMemberList](t, server.Client(), http.MethodGet, membersURL, nil, aliceSession.AccessToken, http.StatusOK)
	if len(members.Items) != 2 {
		t.Fatalf("expected two members, got %+v", members.Items)
	}

	rolesURL := server.URL + "/api/v1/guilds/" + guild.Id.String() + "/roles"
	roles := requestJSON[protocolgo.RoleList](t, server.Client(), http.MethodGet, rolesURL, nil, aliceSession.AccessToken, http.StatusOK)
	if len(roles.Items) != 1 || !roles.Items[0].Managed {
		t.Fatalf("missing managed everyone role: %+v", roles.Items)
	}
	manageMessages := int64(4)
	moderator := requestJSON[protocolgo.Role](t, server.Client(), http.MethodPost, rolesURL, protocolgo.CreateRoleRequest{Name: "Moderator", Permissions: &manageMessages}, aliceSession.AccessToken, http.StatusCreated)
	requestJSON[protocolgo.Error](t, server.Client(), http.MethodPatch, server.URL+"/api/v1/guilds/"+guild.Id.String()+"/members/"+bob.Id.String(), map[string]any{"role_ids": []string{moderator.Id.String()}}, bobSession.AccessToken, http.StatusForbidden)
	updated := requestJSON[protocolgo.GuildMember](t, server.Client(), http.MethodPatch, server.URL+"/api/v1/guilds/"+guild.Id.String()+"/members/"+bob.Id.String(), map[string]any{"role_ids": []string{moderator.Id.String()}, "nickname": "進行役"}, aliceSession.AccessToken, http.StatusOK)
	if updated.Nickname == nil || *updated.Nickname != "進行役" || len(updated.RoleIds) != 1 {
		t.Fatalf("member was not updated: %+v", updated)
	}

	presence := requestJSON[protocolgo.Presence](t, server.Client(), http.MethodPut, server.URL+"/api/v1/users/@me/presence", map[string]any{"status": "ONLINE", "custom_text": "テスト中"}, bobSession.AccessToken, http.StatusOK)
	if presence.UserId != bob.Id || presence.Status != protocolgo.PresenceStatusONLINE {
		t.Fatalf("unexpected presence: %+v", presence)
	}
	requestJSON[struct{}](t, server.Client(), http.MethodDelete, server.URL+"/api/v1/guilds/"+guild.Id.String()+"/members/@me", nil, bobSession.AccessToken, http.StatusNoContent)
	requestJSON[protocolgo.Error](t, server.Client(), http.MethodDelete, server.URL+"/api/v1/guilds/"+guild.Id.String()+"/members/@me", nil, aliceSession.AccessToken, http.StatusForbidden)
	_ = alice
}
