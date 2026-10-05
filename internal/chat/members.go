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

// UpdateMember changes a Member's nickname and Roles. A Member may change their own
// nickname; every other change needs MANAGE_MEMBERS or MANAGE_ROLES and a higher Role
// than the target.
func (s *Service) UpdateMember(ctx context.Context, requesterID, guildID, userID uuid.UUID, input UpdateMemberInput) (Member, error) {
	if !input.Nickname.Set && input.RoleIDs == nil {
		return Member{}, &ValidationError{Field: "body", Message: "must contain at least one field"}
	}
	access, err := s.store.GetAccess(ctx, guildID, requesterID)
	if err != nil {
		return Member{}, err
	}
	member, err := s.store.GetMember(ctx, requesterID, guildID, userID)
	if err != nil {
		return Member{}, err
	}
	if input.Nickname.Set && requesterID != userID {
		if !access.Has(PermManageMembers) {
			return Member{}, ErrForbidden
		}
		if err := s.checkOutranksMember(ctx, access, guildID, userID); err != nil {
			return Member{}, err
		}
	}
	var nickname *string
	if input.Nickname.Set {
		if nickname, err = normalizeNickname(input.Nickname.Value); err != nil {
			return Member{}, err
		}
	}
	if input.RoleIDs != nil {
		if !access.Has(PermManageRoles) {
			return Member{}, ErrForbidden
		}
		if requesterID != userID {
			if err := s.checkOutranksMember(ctx, access, guildID, userID); err != nil {
				return Member{}, err
			}
		}
		if err := s.assignRoles(ctx, access, member, *input.RoleIDs); err != nil {
			return Member{}, err
		}
	}
	if input.Nickname.Set {
		return s.store.UpdateMemberNickname(ctx, guildID, userID, nickname)
	}
	return s.store.GetMemberByID(ctx, guildID, userID)
}

// checkOutranksMember rejects actions against the Owner or a Member whose highest
// Role is not below the caller's.
func (s *Service) checkOutranksMember(ctx context.Context, access Access, guildID, targetID uuid.UUID) error {
	target, err := s.store.GetAccess(ctx, guildID, targetID)
	if err != nil {
		return err
	}
	if target.Owner || !access.Outranks(target.TopPosition) {
		return ErrForbidden
	}
	return nil
}

// RemoveMember removes another Member from the Guild. The Owner can never be removed.
func (s *Service) RemoveMember(ctx context.Context, requesterID, guildID, userID uuid.UUID) error {
	access, err := s.require(ctx, requesterID, guildID, PermManageMembers)
	if err != nil {
		return err
	}
	if requesterID == userID {
		return ErrForbidden
	}
	if err := s.checkOutranksMember(ctx, access, guildID, userID); err != nil {
		return err
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
	if _, err := s.require(ctx, userID, guildID, PermCreateInvite); err != nil {
		return Invite{}, err
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
	if _, err := s.require(ctx, userID, guildID, PermManageGuild); err != nil {
		return nil, err
	}
	return s.store.ListInvites(ctx, guildID, s.now().UTC())
}

func (s *Service) RevokeInvite(ctx context.Context, userID, guildID, inviteID uuid.UUID) error {
	if _, err := s.require(ctx, userID, guildID, PermManageGuild); err != nil {
		return err
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
