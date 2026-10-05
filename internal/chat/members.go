package chat

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	inviteCodeBytes   = 24
	minInviteLifetime = 300
	maxInviteLifetime = 604800
	maxInviteUses     = 1000
	maxNicknameLength = 64
)

func (s *Service) ListMembers(ctx context.Context, userID, guildID uuid.UUID, cursorValue string, limit int) (Page[Member], error) {
	cursor, err := decodeCursor(cursorValue, cursorMembers)
	if err != nil {
		return Page[Member]{}, err
	}
	if err := validateLimit(limit); err != nil {
		return Page[Member]{}, err
	}
	rows, err := s.store.ListMembers(ctx, userID, guildID, cursor, limit+1)
	if err != nil {
		return Page[Member]{}, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	var next *string
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		value, err := encodeCursor(pageCursor{Kind: cursorMembers, Time: last.JoinedAt, ID: last.User.ID})
		if err != nil {
			return Page[Member]{}, err
		}
		next = &value
	}
	return Page[Member]{Items: rows, HasMore: hasMore, NextCursor: next}, nil
}

func (s *Service) GetMember(ctx context.Context, requesterID, guildID, userID uuid.UUID) (Member, error) {
	return s.store.GetMember(ctx, requesterID, guildID, userID)
}

// UpdateMember changes a Member's Guild nickname. The Member themself or the Guild
// Owner may change it. Role assignment is rejected until Role storage exists.
func (s *Service) UpdateMember(ctx context.Context, requesterID, guildID, userID uuid.UUID, input UpdateMemberInput) (Member, error) {
	if !input.Nickname.Set && input.RoleIDs == nil {
		return Member{}, &ValidationError{Field: "body", Message: "must contain at least one field"}
	}
	if input.RoleIDs != nil && len(*input.RoleIDs) > 0 {
		return Member{}, &ValidationError{Field: "role_ids", Message: "role assignment is not supported yet"}
	}
	guild, err := s.store.GetGuild(ctx, requesterID, guildID)
	if err != nil {
		return Member{}, err
	}
	if requesterID != userID && guild.OwnerID != requesterID {
		return Member{}, ErrForbidden
	}
	if !input.Nickname.Set {
		return s.store.GetMember(ctx, requesterID, guildID, userID)
	}
	nickname, err := normalizeNickname(input.Nickname.Value)
	if err != nil {
		return Member{}, err
	}
	return s.store.UpdateMemberNickname(ctx, guildID, userID, nickname)
}

// RemoveMember removes another Member from the Guild. Only the Owner may do so and
// the Owner can never be removed.
func (s *Service) RemoveMember(ctx context.Context, requesterID, guildID, userID uuid.UUID) error {
	guild, err := s.store.GetGuild(ctx, requesterID, guildID)
	if err != nil {
		return err
	}
	if guild.OwnerID != requesterID || userID == guild.OwnerID {
		return ErrForbidden
	}
	return s.store.RemoveMember(ctx, guildID, userID)
}

func (s *Service) LeaveGuild(ctx context.Context, userID, guildID uuid.UUID) error {
	guild, err := s.store.GetGuild(ctx, userID, guildID)
	if err != nil {
		return err
	}
	if guild.OwnerID == userID {
		return ErrForbidden
	}
	return s.store.RemoveMember(ctx, guildID, userID)
}

func (s *Service) ListGuildMemberIDs(ctx context.Context, guildID uuid.UUID) ([]uuid.UUID, error) {
	return s.store.ListGuildMemberIDs(ctx, guildID)
}

func (s *Service) CreateInvite(ctx context.Context, userID, guildID uuid.UUID, input CreateInviteInput) (Invite, error) {
	guild, err := s.store.GetGuild(ctx, userID, guildID)
	if err != nil {
		return Invite{}, err
	}
	if guild.OwnerID != userID {
		return Invite{}, ErrForbidden
	}
	if input.ExpiresIn != nil && (*input.ExpiresIn < minInviteLifetime || *input.ExpiresIn > maxInviteLifetime) {
		return Invite{}, &ValidationError{Field: "expires_in", Message: fmt.Sprintf("must be between %d and %d seconds", minInviteLifetime, maxInviteLifetime)}
	}
	if input.MaxUses != nil && (*input.MaxUses < 1 || *input.MaxUses > maxInviteUses) {
		return Invite{}, &ValidationError{Field: "max_uses", Message: fmt.Sprintf("must be between 1 and %d", maxInviteUses)}
	}
	id, err := newUUIDv7()
	if err != nil {
		return Invite{}, err
	}
	code, err := newInviteCode()
	if err != nil {
		return Invite{}, err
	}
	now := s.now().UTC()
	invite := Invite{ID: id, Code: code, Guild: Guild{ID: guildID}, Inviter: UserSummary{ID: userID}, MaxUses: input.MaxUses, CreatedAt: now}
	if input.ExpiresIn != nil {
		expiresAt := now.Add(time.Duration(*input.ExpiresIn) * time.Second)
		invite.ExpiresAt = &expiresAt
	}
	return s.store.CreateInvite(ctx, invite)
}

func (s *Service) ListInvites(ctx context.Context, userID, guildID uuid.UUID) ([]Invite, error) {
	guild, err := s.store.GetGuild(ctx, userID, guildID)
	if err != nil {
		return nil, err
	}
	if guild.OwnerID != userID {
		return nil, ErrForbidden
	}
	return s.store.ListInvites(ctx, guildID, s.now().UTC())
}

func (s *Service) RevokeInvite(ctx context.Context, userID, guildID, inviteID uuid.UUID) error {
	guild, err := s.store.GetGuild(ctx, userID, guildID)
	if err != nil {
		return err
	}
	if guild.OwnerID != userID {
		return ErrForbidden
	}
	return s.store.RevokeInvite(ctx, guildID, inviteID, s.now().UTC())
}

func (s *Service) GetInvite(ctx context.Context, code string) (Invite, error) {
	if !validInviteCode(code) {
		return Invite{}, ErrNotFound
	}
	return s.store.GetInvite(ctx, code, s.now().UTC())
}

// AcceptInvite joins userID to the Invite's Guild. The returned bool is true only
// when a new membership was created.
func (s *Service) AcceptInvite(ctx context.Context, userID uuid.UUID, code string) (Member, bool, error) {
	if !validInviteCode(code) {
		return Member{}, false, ErrNotFound
	}
	return s.store.AcceptInvite(ctx, code, userID, s.now().UTC())
}

func newInviteCode() (string, error) {
	buffer := make([]byte, inviteCodeBytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate invite code: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func validInviteCode(code string) bool {
	if len(code) < 16 || len(code) > 64 {
		return false
	}
	for _, character := range code {
		switch {
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9', character == '-', character == '_':
		default:
			return false
		}
	}
	return true
}

func normalizeNickname(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(trimmed) > maxNicknameLength {
		return nil, &ValidationError{Field: "nickname", Message: fmt.Sprintf("must contain at most %d characters", maxNicknameLength)}
	}
	return &trimmed, nil
}
