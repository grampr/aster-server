package community

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

type stubStore struct {
	owner       bool
	permissions int64
	updated     bool
}

func (s *stubStore) ListMembers(context.Context, uuid.UUID, uuid.UUID, *memberCursor, int) ([]Member, error) {
	return nil, nil
}
func (s *stubStore) GetMember(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (Member, error) {
	return Member{}, nil
}
func (s *stubStore) UpdateMember(_ context.Context, guildID, userID uuid.UUID, input UpdateMemberInput, _ time.Time) (Member, error) {
	s.updated = true
	return Member{GuildID: guildID, User: UserSummary{ID: userID}, RoleIDs: input.RoleIDs}, nil
}
func (s *stubStore) RemoveMember(context.Context, uuid.UUID, uuid.UUID) error    { return nil }
func (s *stubStore) IsOwner(context.Context, uuid.UUID, uuid.UUID) (bool, error) { return s.owner, nil }
func (s *stubStore) EffectivePermissions(context.Context, uuid.UUID, uuid.UUID) (int64, error) {
	return s.permissions, nil
}
func (s *stubStore) ListRoles(context.Context, uuid.UUID, uuid.UUID) ([]Role, error) { return nil, nil }
func (s *stubStore) CreateRole(context.Context, Role) (Role, error)                  { return Role{}, nil }
func (s *stubStore) UpdateRole(context.Context, uuid.UUID, uuid.UUID, UpdateRoleInput, time.Time) (Role, error) {
	return Role{}, nil
}
func (s *stubStore) DeleteRole(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (s *stubStore) CreateInvite(context.Context, Invite) (Invite, error)   { return Invite{}, nil }
func (s *stubStore) ListInvites(context.Context, uuid.UUID, uuid.UUID, time.Time) ([]Invite, error) {
	return nil, nil
}
func (s *stubStore) GetInvite(context.Context, string, time.Time) (Invite, error) {
	return Invite{}, nil
}
func (s *stubStore) AcceptInvite(context.Context, string, uuid.UUID, time.Time) (Member, error) {
	return Member{}, nil
}
func (s *stubStore) DeleteInvite(context.Context, uuid.UUID, uuid.UUID, time.Time) error { return nil }
func (s *stubStore) ListGuildMemberIDs(context.Context, uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}
func (s *stubStore) ListUserGuildIDs(context.Context, uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}

func TestMemberCannotAssignRoleToSelfWithoutManageRoles(t *testing.T) {
	store := &stubStore{}
	service, _ := NewService(store)
	id := uuid.New()
	_, err := service.UpdateMember(context.Background(), id, uuid.New(), id, UpdateMemberInput{RoleIDsSet: true, RoleIDs: []uuid.UUID{uuid.New()}})
	if err != ErrForbidden {
		t.Fatalf("expected forbidden, got %v", err)
	}
	if store.updated {
		t.Fatal("store must not be updated")
	}
}

func TestOwnerCanAssignRoles(t *testing.T) {
	store := &stubStore{owner: true}
	service, _ := NewService(store)
	id := uuid.New()
	if _, err := service.UpdateMember(context.Background(), id, uuid.New(), id, UpdateMemberInput{RoleIDsSet: true, RoleIDs: []uuid.UUID{uuid.New()}}); err != nil {
		t.Fatal(err)
	}
	if !store.updated {
		t.Fatal("store was not updated")
	}
}

func TestPresenceRejectsOversizedCustomText(t *testing.T) {
	service, _ := NewService(&stubStore{})
	text := ""
	for i := 0; i < 129; i++ {
		text += "あ"
	}
	if _, err := service.UpdatePresence(uuid.New(), "ONLINE", &text); err == nil {
		t.Fatal("expected validation error")
	}
}
