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
	DeleteMessage(ctx context.Context, userID, channelID, messageID uuid.UUID) error
	AddMessageReaction(ctx context.Context, userID, channelID, messageID uuid.UUID, emoji string, createdAt time.Time) (MessageReaction, bool, error)
	RemoveMessageReaction(ctx context.Context, userID, channelID, messageID uuid.UUID, emoji string) (MessageReaction, bool, error)
	ListChannelMemberIDs(ctx context.Context, channelID uuid.UUID) ([]uuid.UUID, error)
}

type PermissionChecker interface {
	HasPermission(context.Context, uuid.UUID, uuid.UUID, int64) (bool, error)
}

const (
	permissionViewChannel    int64 = 1 << 0
	permissionSendMessages   int64 = 1 << 1
	permissionManageMessages int64 = 1 << 2
	permissionManageChannels int64 = 1 << 3
	permissionManageGuild    int64 = 1 << 4
)
