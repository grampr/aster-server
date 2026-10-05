package chat

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrForbidden = errors.New("forbidden")
	ErrNotFound  = errors.New("resource not found")
)

const (
	ChannelTypeText  = "TEXT"
	ChannelTypeVoice = "VOICE"
)

type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

type Guild struct {
	ID          uuid.UUID
	OwnerID     uuid.UUID
	Name        string
	Description *string
	IconURL     *string
	CreatedAt   time.Time
}

type Channel struct {
	ID        uuid.UUID
	GuildID   uuid.UUID
	Type      string
	Name      string
	Topic     *string
	Position  int
	CreatedAt time.Time
}

type UserSummary struct {
	ID          uuid.UUID
	DisplayName string
	AvatarURL   *string
}

type Message struct {
	ID               uuid.UUID
	ChannelID        uuid.UUID
	Author           UserSummary
	Content          string
	ReplyToMessageID *uuid.UUID
	ReplyTo          *MessageReply
	Reactions        []MessageReaction
	CreatedAt        time.Time
	EditedAt         *time.Time
}

type MessageReaction struct {
	Emoji string
	Count int
	Me    bool
}

type MessageReply struct {
	ID        uuid.UUID
	ChannelID uuid.UUID
	Author    UserSummary
	Content   string
	CreatedAt time.Time
	EditedAt  *time.Time
}

type OptionalString struct {
	Set   bool
	Value *string
}

type CreateGuildInput struct {
	Name        string
	Description *string
}

type UpdateGuildInput struct {
	Name        *string
	Description OptionalString
}

type CreateChannelInput struct {
	Type  string
	Name  string
	Topic *string
}

type UpdateChannelInput struct {
	Name     *string
	Topic    OptionalString
	Position *int
}

type Page[T any] struct {
	Items      []T
	HasMore    bool
	NextCursor *string
}

type guildListRow struct {
	Guild
	JoinedAt time.Time
}

type pageCursor struct {
	Kind     string    `json:"k"`
	Time     time.Time `json:"t,omitempty"`
	Position int       `json:"p,omitempty"`
	ID       uuid.UUID `json:"id"`
}

type Store interface {
	MemberStore
	RoleStore

	CreateGuild(ctx context.Context, ownerID uuid.UUID, guild Guild) error
	ListGuilds(ctx context.Context, userID uuid.UUID, cursor *pageCursor, limit int) ([]guildListRow, error)
	GetGuild(ctx context.Context, userID, guildID uuid.UUID) (Guild, error)
	UpdateGuild(ctx context.Context, ownerID, guildID uuid.UUID, input UpdateGuildInput, updatedAt time.Time) (Guild, error)
	DeleteGuild(ctx context.Context, ownerID, guildID uuid.UUID) error

	CreateChannel(ctx context.Context, channel Channel) (Channel, error)
	ListChannels(ctx context.Context, userID, guildID uuid.UUID, cursor *pageCursor, limit int) ([]Channel, error)
	GetChannel(ctx context.Context, userID, channelID uuid.UUID) (Channel, error)
	UpdateChannel(ctx context.Context, ownerID, channelID uuid.UUID, input UpdateChannelInput, updatedAt time.Time) (Channel, error)
	DeleteChannel(ctx context.Context, ownerID, channelID uuid.UUID) error

	CreateMessage(ctx context.Context, message Message) (Message, error)
	ListMessages(ctx context.Context, userID, channelID uuid.UUID, cursor *pageCursor, limit int) ([]Message, error)
	GetMessage(ctx context.Context, userID, channelID, messageID uuid.UUID) (Message, error)
	UpdateMessage(ctx context.Context, authorID, channelID, messageID uuid.UUID, content string, editedAt time.Time) (Message, error)
	DeleteMessage(ctx context.Context, userID, channelID, messageID uuid.UUID, canManage bool) error
	AddMessageReaction(ctx context.Context, userID, channelID, messageID uuid.UUID, emoji string, createdAt time.Time) (MessageReaction, bool, error)
	RemoveMessageReaction(ctx context.Context, userID, channelID, messageID uuid.UUID, emoji string) (MessageReaction, bool, error)
	ListChannelMemberIDs(ctx context.Context, channelID uuid.UUID) ([]uuid.UUID, error)
}

var ErrInviteUnavailable = errors.New("invite is expired or has no remaining uses")

const (
	PresenceOffline = "OFFLINE"

	cursorMembers = "members"
)

type Member struct {
	GuildID  uuid.UUID
	User     UserSummary
	Nickname *string
	RoleIDs  []uuid.UUID
	JoinedAt time.Time
	Presence *Presence
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

type CreateInviteInput struct {
	ExpiresIn *int
	MaxUses   *int
}

type UpdateMemberInput struct {
	Nickname OptionalString
	RoleIDs  *[]uuid.UUID
}

// MemberStore persists Guild Members and Invites.
type MemberStore interface {
	ListMembers(ctx context.Context, userID, guildID uuid.UUID, cursor *pageCursor, limit int) ([]Member, error)
	GetMember(ctx context.Context, requesterID, guildID, userID uuid.UUID) (Member, error)
	UpdateMemberNickname(ctx context.Context, guildID, userID uuid.UUID, nickname *string) (Member, error)
	GetMemberByID(ctx context.Context, guildID, userID uuid.UUID) (Member, error)
	RemoveMember(ctx context.Context, guildID, userID uuid.UUID) error
	ListUserGuildIDs(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error)
	ListGuildMemberIDs(ctx context.Context, guildID uuid.UUID) ([]uuid.UUID, error)

	CreateInvite(ctx context.Context, invite Invite) (Invite, error)
	ListInvites(ctx context.Context, guildID uuid.UUID, now time.Time) ([]Invite, error)
	GetInvite(ctx context.Context, code string, now time.Time) (Invite, error)
	RevokeInvite(ctx context.Context, guildID, inviteID uuid.UUID, revokedAt time.Time) error
	AcceptInvite(ctx context.Context, code string, userID uuid.UUID, now time.Time) (Member, bool, error)
}

type Role struct {
	ID          uuid.UUID
	GuildID     uuid.UUID
	Name        string
	Color       *string
	Permissions int64
	Position    int
	Managed     bool
	IsDefault   bool
	CreatedAt   time.Time
}

type CreateRoleInput struct {
	Name        string
	Color       *string
	Permissions *int64
}

type UpdateRoleInput struct {
	Name        *string
	Color       OptionalString
	Permissions *int64
	Position    *int
}

// RoleStore persists Roles and Role assignments.
type RoleStore interface {
	GetAccess(ctx context.Context, guildID, userID uuid.UUID) (Access, error)
	ListRoles(ctx context.Context, guildID uuid.UUID) ([]Role, error)
	CreateRole(ctx context.Context, role Role) (Role, error)
	UpdateRole(ctx context.Context, role Role) (Role, error)
	DeleteRole(ctx context.Context, guildID, roleID uuid.UUID) error
	SetMemberRoles(ctx context.Context, guildID, userID uuid.UUID, roleIDs []uuid.UUID) error
}
