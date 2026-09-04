package community

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

var colorPattern = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

type Service struct {
	store      Store
	now        func() time.Time
	presenceMu sync.RWMutex
	presences  map[uuid.UUID]Presence
}

func NewService(store Store) (*Service, error) {
	if store == nil {
		return nil, errors.New("community store is required")
	}
	return &Service{store: store, now: time.Now, presences: make(map[uuid.UUID]Presence)}, nil
}

func (s *Service) ListMembers(ctx context.Context, actorID, guildID uuid.UUID, cursorValue string, limit int) (MemberPage, error) {
	if limit < 1 || limit > 100 {
		return MemberPage{}, &ValidationError{Field: "limit", Message: "must be between 1 and 100"}
	}
	cursor, err := decodeMemberCursor(cursorValue)
	if err != nil {
		return MemberPage{}, err
	}
	items, err := s.store.ListMembers(ctx, actorID, guildID, cursor, limit+1)
	if err != nil {
		return MemberPage{}, err
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	for i := range items {
		items[i].Presence = s.presenceFor(items[i].User.ID)
	}
	var next *string
	if hasMore && len(items) > 0 {
		value, err := encodeMemberCursor(memberCursor{JoinedAt: items[len(items)-1].JoinedAt, UserID: items[len(items)-1].User.ID})
		if err != nil {
			return MemberPage{}, err
		}
		next = &value
	}
	return MemberPage{Items: items, HasMore: hasMore, NextCursor: next}, nil
}

func (s *Service) GetMember(ctx context.Context, actorID, guildID, userID uuid.UUID) (Member, error) {
	member, err := s.store.GetMember(ctx, actorID, guildID, userID)
	if err != nil {
		return Member{}, err
	}
	member.Presence = s.presenceFor(userID)
	return member, nil
}

func (s *Service) UpdateMember(ctx context.Context, actorID, guildID, userID uuid.UUID, input UpdateMemberInput) (Member, error) {
	if !input.NicknameSet && !input.RoleIDsSet {
		return Member{}, &ValidationError{Field: "body", Message: "must contain at least one field"}
	}
	if input.NicknameSet {
		value, err := normalizeOptional(input.Nickname, 64)
		if err != nil {
			return Member{}, &ValidationError{Field: "nickname", Message: "must contain at most 64 characters"}
		}
		input.Nickname = value
	}
	if input.RoleIDsSet {
		if len(input.RoleIDs) > 100 {
			return Member{}, &ValidationError{Field: "role_ids", Message: "must contain at most 100 roles"}
		}
		if err := s.Require(ctx, actorID, guildID, PermissionManageRoles); err != nil {
			return Member{}, err
		}
	} else if actorID != userID {
		if err := s.Require(ctx, actorID, guildID, PermissionManageMembers); err != nil {
			return Member{}, err
		}
	}
	member, err := s.store.UpdateMember(ctx, guildID, userID, input, s.now().UTC())
	if err != nil {
		return Member{}, err
	}
	member.Presence = s.presenceFor(userID)
	return member, nil
}

func (s *Service) RemoveMember(ctx context.Context, actorID, guildID, userID uuid.UUID) error {
	if actorID != userID {
		if err := s.Require(ctx, actorID, guildID, PermissionManageMembers); err != nil {
			return err
		}
	}
	owner, err := s.store.IsOwner(ctx, guildID, userID)
	if err != nil {
		return err
	}
	if owner {
		return ErrOwnerCannotLeave
	}
	return s.store.RemoveMember(ctx, guildID, userID)
}

func (s *Service) Require(ctx context.Context, userID, guildID uuid.UUID, permission int64) error {
	allowed, err := s.HasPermission(ctx, userID, guildID, permission)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}

func (s *Service) HasPermission(ctx context.Context, userID, guildID uuid.UUID, permission int64) (bool, error) {
	owner, err := s.store.IsOwner(ctx, guildID, userID)
	if err != nil {
		return false, err
	}
	if owner {
		return true, nil
	}
	permissions, err := s.store.EffectivePermissions(ctx, guildID, userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return permissions&permission == permission, nil
}

func (s *Service) ListRoles(ctx context.Context, actorID, guildID uuid.UUID) ([]Role, error) {
	return s.store.ListRoles(ctx, actorID, guildID)
}

func (s *Service) CreateRole(ctx context.Context, actorID, guildID uuid.UUID, input CreateRoleInput) (Role, error) {
	if err := s.Require(ctx, actorID, guildID, PermissionManageRoles); err != nil {
		return Role{}, err
	}
	name, err := validateName(input.Name)
	if err != nil {
		return Role{}, err
	}
	if err := validateColor(input.Color); err != nil {
		return Role{}, err
	}
	if err := validatePermissions(input.Permissions); err != nil {
		return Role{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Role{}, err
	}
	return s.store.CreateRole(ctx, Role{ID: id, GuildID: guildID, Name: name, Color: input.Color, Permissions: input.Permissions, CreatedAt: s.now().UTC()})
}

func (s *Service) UpdateRole(ctx context.Context, actorID, guildID, roleID uuid.UUID, input UpdateRoleInput) (Role, error) {
	if err := s.Require(ctx, actorID, guildID, PermissionManageRoles); err != nil {
		return Role{}, err
	}
	if input.Name == nil && !input.ColorSet && input.Permissions == nil && input.Position == nil {
		return Role{}, &ValidationError{Field: "body", Message: "must contain at least one field"}
	}
	if input.Name != nil {
		value, err := validateName(*input.Name)
		if err != nil {
			return Role{}, err
		}
		input.Name = &value
	}
	if input.ColorSet {
		if err := validateColor(input.Color); err != nil {
			return Role{}, err
		}
	}
	if input.Permissions != nil {
		if err := validatePermissions(*input.Permissions); err != nil {
			return Role{}, err
		}
	}
	if input.Position != nil && *input.Position < 0 {
		return Role{}, &ValidationError{Field: "position", Message: "must be zero or greater"}
	}
	return s.store.UpdateRole(ctx, guildID, roleID, input, s.now().UTC())
}

func (s *Service) DeleteRole(ctx context.Context, actorID, guildID, roleID uuid.UUID) error {
	if err := s.Require(ctx, actorID, guildID, PermissionManageRoles); err != nil {
		return err
	}
	return s.store.DeleteRole(ctx, guildID, roleID)
}

func (s *Service) CreateInvite(ctx context.Context, actorID, guildID uuid.UUID, input CreateInviteInput) (Invite, error) {
	if err := s.Require(ctx, actorID, guildID, PermissionCreateInvite); err != nil {
		return Invite{}, err
	}
	if input.ExpiresIn != nil && (*input.ExpiresIn < 5*time.Minute || *input.ExpiresIn > 7*24*time.Hour) {
		return Invite{}, &ValidationError{Field: "expires_in", Message: "must be between 300 and 604800 seconds"}
	}
	if input.MaxUses != nil && (*input.MaxUses < 1 || *input.MaxUses > 1000) {
		return Invite{}, &ValidationError{Field: "max_uses", Message: "must be between 1 and 1000"}
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Invite{}, err
	}
	codeBytes := make([]byte, 24)
	if _, err := rand.Read(codeBytes); err != nil {
		return Invite{}, err
	}
	now := s.now().UTC()
	var expiresAt *time.Time
	if input.ExpiresIn != nil {
		value := now.Add(*input.ExpiresIn)
		expiresAt = &value
	}
	return s.store.CreateInvite(ctx, Invite{ID: id, Code: base64.RawURLEncoding.EncodeToString(codeBytes), Guild: Guild{ID: guildID}, Inviter: UserSummary{ID: actorID}, MaxUses: input.MaxUses, ExpiresAt: expiresAt, CreatedAt: now})
}

func (s *Service) ListInvites(ctx context.Context, actorID, guildID uuid.UUID) ([]Invite, error) {
	if err := s.Require(ctx, actorID, guildID, PermissionCreateInvite); err != nil {
		return nil, err
	}
	return s.store.ListInvites(ctx, actorID, guildID, s.now().UTC())
}

func (s *Service) GetInvite(ctx context.Context, code string) (Invite, error) {
	return s.store.GetInvite(ctx, code, s.now().UTC())
}
func (s *Service) AcceptInvite(ctx context.Context, code string, userID uuid.UUID) (Member, error) {
	member, err := s.store.AcceptInvite(ctx, code, userID, s.now().UTC())
	if err != nil {
		return Member{}, err
	}
	member.Presence = s.presenceFor(userID)
	return member, nil
}
func (s *Service) DeleteInvite(ctx context.Context, actorID, guildID, inviteID uuid.UUID) error {
	if err := s.Require(ctx, actorID, guildID, PermissionCreateInvite); err != nil {
		return err
	}
	return s.store.DeleteInvite(ctx, guildID, inviteID, s.now().UTC())
}

func (s *Service) UpdatePresence(userID uuid.UUID, status string, customText *string) (Presence, error) {
	if status != "ONLINE" && status != "IDLE" && status != "DO_NOT_DISTURB" {
		return Presence{}, &ValidationError{Field: "status", Message: "is unsupported"}
	}
	value, err := normalizeOptional(customText, 128)
	if err != nil {
		return Presence{}, &ValidationError{Field: "custom_text", Message: "must contain at most 128 characters"}
	}
	customText = value
	presence := Presence{UserID: userID, Status: status, CustomText: customText, UpdatedAt: s.now().UTC()}
	s.presenceMu.Lock()
	s.presences[userID] = presence
	s.presenceMu.Unlock()
	return presence, nil
}

func (s *Service) presenceFor(userID uuid.UUID) Presence {
	s.presenceMu.RLock()
	value, ok := s.presences[userID]
	s.presenceMu.RUnlock()
	if ok {
		return value
	}
	return Presence{UserID: userID, Status: "OFFLINE", UpdatedAt: s.now().UTC()}
}

func validateName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if n := utf8.RuneCountInString(value); n < 1 || n > 100 {
		return "", &ValidationError{Field: "name", Message: "must contain between 1 and 100 characters"}
	}
	return value, nil
}
func validateColor(value *string) error {
	if value != nil && !colorPattern.MatchString(*value) {
		return &ValidationError{Field: "color", Message: "must be a six-digit hex color"}
	}
	return nil
}
func validatePermissions(value int64) error {
	if value < 0 || value > 2147483647 {
		return &ValidationError{Field: "permissions", Message: "must be a 32-bit permission bitfield"}
	}
	return nil
}
func normalizeOptional(value *string, maximum int) (*string, error) {
	if value == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(trimmed) > maximum {
		return nil, errors.New("text is too long")
	}
	return &trimmed, nil
}

func (s *Service) ListGuildMemberIDs(ctx context.Context, guildID uuid.UUID) ([]uuid.UUID, error) {
	return s.store.ListGuildMemberIDs(ctx, guildID)
}

func (s *Service) ListUserGuildIDs(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	return s.store.ListUserGuildIDs(ctx, userID)
}
func encodeMemberCursor(value memberCursor) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}
func decodeMemberCursor(value string) (*memberCursor, error) {
	if value == "" {
		return nil, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, &ValidationError{Field: "cursor", Message: "is invalid"}
	}
	var cursor memberCursor
	if json.Unmarshal(payload, &cursor) != nil || cursor.UserID == uuid.Nil || cursor.JoinedAt.IsZero() {
		return nil, &ValidationError{Field: "cursor", Message: "is invalid"}
	}
	return &cursor, nil
}
