package community

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrForbidden        = errors.New("forbidden")
	ErrNotFound         = errors.New("resource not found")
	ErrInviteExpired    = errors.New("invite expired")
	ErrOwnerCannotLeave = errors.New("guild owner cannot leave")
)

const (
	PermissionViewChannel    int64 = 1 << 0
	PermissionSendMessages   int64 = 1 << 1
	PermissionManageMessages int64 = 1 << 2
	PermissionManageChannels int64 = 1 << 3
	PermissionManageGuild    int64 = 1 << 4
	PermissionManageRoles    int64 = 1 << 5
	PermissionManageMembers  int64 = 1 << 6
	PermissionCreateInvite   int64 = 1 << 7
	PermissionConnect        int64 = 1 << 8
	PermissionSpeak          int64 = 1 << 9
	PermissionStream         int64 = 1 << 10
	PermissionAll                  = PermissionViewChannel | PermissionSendMessages | PermissionManageMessages |
		PermissionManageChannels | PermissionManageGuild | PermissionManageRoles | PermissionManageMembers |
		PermissionCreateInvite | PermissionConnect | PermissionSpeak | PermissionStream
	DefaultPermissions = PermissionViewChannel | PermissionSendMessages | PermissionCreateInvite | PermissionConnect
)

type ValidationError struct{ Field, Message string }

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

type UserSummary struct {
	ID          uuid.UUID
	DisplayName string
	AvatarURL   *string
}

type Presence struct {
	UserID     uuid.UUID
	Status     string
	CustomText *string
	UpdatedAt  time.Time
}

type Member struct {
	GuildID  uuid.UUID
	User     UserSummary
	Nickname *string
	RoleIDs  []uuid.UUID
	JoinedAt time.Time
	Presence Presence
}

type Role struct {
	ID          uuid.UUID
	GuildID     uuid.UUID
	Name        string
	Color       *string
	Permissions int64
	Position    int
	Managed     bool
	CreatedAt   time.Time
}

type Guild struct {
	ID          uuid.UUID
	OwnerID     uuid.UUID
	Name        string
	Description *string
	IconURL     *string
	CreatedAt   time.Time
}

type Invite struct {
	ID        uuid.UUID
	Code      string
	Guild     Guild
	Inviter   UserSummary
	Uses      int
	MaxUses   *int
	ExpiresAt *time.Time
	CreatedAt time.Time
}

type MemberPage struct {
	Items      []Member
	HasMore    bool
	NextCursor *string
}

type UpdateMemberInput struct {
	NicknameSet bool
	Nickname    *string
	RoleIDsSet  bool
	RoleIDs     []uuid.UUID
}

type CreateRoleInput struct {
	Name        string
	Color       *string
	Permissions int64
}

type UpdateRoleInput struct {
	Name        *string
	ColorSet    bool
	Color       *string
	Permissions *int64
	Position    *int
}

type CreateInviteInput struct {
	ExpiresIn *time.Duration
	MaxUses   *int
}

type memberCursor struct {
	JoinedAt time.Time `json:"joined_at"`
	UserID   uuid.UUID `json:"user_id"`
}

type Store interface {
	ListMembers(context.Context, uuid.UUID, uuid.UUID, *memberCursor, int) ([]Member, error)
	GetMember(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (Member, error)
	UpdateMember(context.Context, uuid.UUID, uuid.UUID, UpdateMemberInput, time.Time) (Member, error)
	RemoveMember(context.Context, uuid.UUID, uuid.UUID) error
	IsOwner(context.Context, uuid.UUID, uuid.UUID) (bool, error)
	EffectivePermissions(context.Context, uuid.UUID, uuid.UUID) (int64, error)
	ListRoles(context.Context, uuid.UUID, uuid.UUID) ([]Role, error)
	CreateRole(context.Context, Role) (Role, error)
	UpdateRole(context.Context, uuid.UUID, uuid.UUID, UpdateRoleInput, time.Time) (Role, error)
	DeleteRole(context.Context, uuid.UUID, uuid.UUID) error
	CreateInvite(context.Context, Invite) (Invite, error)
	ListInvites(context.Context, uuid.UUID, uuid.UUID, time.Time) ([]Invite, error)
	GetInvite(context.Context, string, time.Time) (Invite, error)
	AcceptInvite(context.Context, string, uuid.UUID, time.Time) (Member, error)
	DeleteInvite(context.Context, uuid.UUID, uuid.UUID, time.Time) error
	ListGuildMemberIDs(context.Context, uuid.UUID) ([]uuid.UUID, error)
	ListUserGuildIDs(context.Context, uuid.UUID) ([]uuid.UUID, error)
}
