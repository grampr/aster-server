package gateway

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/grampr/aster-server/internal/community"
)

const (
	opDispatch       = 0
	opHeartbeat      = 1
	opIdentify       = 2
	opResume         = 6
	opInvalidSession = 9
	opHello          = 10
	opHeartbeatAck   = 11

	intentGuildMessages  int64 = 1 << 2
	intentDirectMessages int64 = 1 << 3
	intentMessageContent int64 = 1 << 4
	intentReactions      int64 = 1 << 7
	intentTyping         int64 = 1 << 9
	intentGuildMembers   int64 = 1 << 1
	intentGuildPresences int64 = 1 << 6
	intentGuilds         int64 = 1 << 0
)

const (
	eventReady                 = "READY"
	eventResumed               = "RESUMED"
	eventMessageCreate         = "MESSAGE_CREATE"
	eventMessageUpdate         = "MESSAGE_UPDATE"
	eventMessageDelete         = "MESSAGE_DELETE"
	eventMessageReactionAdd    = "MESSAGE_REACTION_ADD"
	eventMessageReactionRemove = "MESSAGE_REACTION_REMOVE"
	eventTypingStart           = "TYPING_START"
	eventMemberJoin            = "MEMBER_JOIN"
	eventMemberUpdate          = "MEMBER_UPDATE"
	eventMemberLeave           = "MEMBER_LEAVE"
	eventPresenceUpdate        = "PRESENCE_UPDATE"
	eventChannelCreate         = "CHANNEL_CREATE"
	eventChannelUpdate         = "CHANNEL_UPDATE"
	eventChannelDelete         = "CHANNEL_DELETE"
	eventReadStateUpdate       = "READ_STATE_UPDATE"
	eventVoiceStateUpdate      = "VOICE_STATE_UPDATE"
)

type inboundMessage struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
}

type identifyPayload struct {
	Token   string `json:"token"`
	Intents int64  `json:"intents"`
}

type resumePayload struct {
	Token     string    `json:"token"`
	SessionID uuid.UUID `json:"session_id"`
	Sequence  int64     `json:"sequence"`
}

type gatewayMessage struct {
	Op int    `json:"op"`
	T  string `json:"t,omitempty"`
	S  *int64 `json:"s,omitempty"`
	D  any    `json:"d"`
}

type Message struct {
	ID               uuid.UUID
	ChannelID        uuid.UUID
	Author           UserSummary
	Content          string
	ReplyToMessageID *uuid.UUID
	ReplyTo          *MessageReply
	CreatedAt        time.Time
	EditedAt         *time.Time
	Attachments      []Attachment
}

type Attachment struct {
	ID             uuid.UUID `json:"id"`
	UploaderID     uuid.UUID `json:"uploader_id"`
	ChannelID      uuid.UUID `json:"channel_id"`
	Filename       string    `json:"filename"`
	ContentType    string    `json:"content_type"`
	Size           int64     `json:"size"`
	ChecksumSHA256 string    `json:"checksum_sha256"`
	Status         string    `json:"status"`
	DownloadURL    string    `json:"download_url"`
	CreatedAt      time.Time `json:"created_at"`
}

type MessageReply struct {
	ID        uuid.UUID
	ChannelID uuid.UUID
	Author    UserSummary
	Content   string
	CreatedAt time.Time
	EditedAt  *time.Time
}

type MessageReaction struct {
	MessageID uuid.UUID
	ChannelID uuid.UUID
	UserID    uuid.UUID
	Emoji     string
	Count     int
}

type TypingStart struct {
	ChannelID uuid.UUID
	User      UserSummary
	StartedAt time.Time
}

type UserSummary struct {
	ID          uuid.UUID
	DisplayName string
	AvatarURL   *string
}

type Channel struct {
	ID         uuid.UUID
	GuildID    *uuid.UUID
	ParentID   *uuid.UUID
	Type       string
	Name       *string
	Topic      *string
	Position   int
	CreatedAt  time.Time
	Recipients []UserSummary
}

type ReadState struct {
	ChannelID         uuid.UUID
	LastReadMessageID *uuid.UUID
	UpdatedAt         time.Time
}

type VoiceState struct {
	UserID     uuid.UUID  `json:"user_id"`
	ChannelID  *uuid.UUID `json:"channel_id"`
	SessionID  *uuid.UUID `json:"session_id"`
	SelfMute   bool       `json:"self_mute"`
	SelfDeaf   bool       `json:"self_deaf"`
	SelfVideo  bool       `json:"self_video"`
	SelfStream bool       `json:"self_stream"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

type channelPayload struct {
	ID         uuid.UUID     `json:"id"`
	GuildID    *uuid.UUID    `json:"guild_id"`
	ParentID   *uuid.UUID    `json:"parent_id"`
	Type       string        `json:"type"`
	Name       *string       `json:"name"`
	Topic      *string       `json:"topic"`
	Position   int           `json:"position"`
	CreatedAt  time.Time     `json:"created_at"`
	Recipients []userPayload `json:"recipients"`
}

type channelDeletePayload struct {
	ID      uuid.UUID  `json:"id"`
	GuildID *uuid.UUID `json:"guild_id"`
}

type readStatePayload struct {
	ChannelID         uuid.UUID  `json:"channel_id"`
	LastReadMessageID *uuid.UUID `json:"last_read_message_id"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type messagePayload struct {
	ID               uuid.UUID            `json:"id"`
	ChannelID        uuid.UUID            `json:"channel_id"`
	Author           userPayload          `json:"author"`
	Content          *string              `json:"content"`
	ReplyToMessageID *uuid.UUID           `json:"reply_to_message_id"`
	ReplyTo          *messageReplyPayload `json:"reply_to"`
	CreatedAt        time.Time            `json:"created_at"`
	EditedAt         *time.Time           `json:"edited_at"`
	Attachments      []Attachment         `json:"attachments"`
}

type messageReplyPayload struct {
	ID        uuid.UUID   `json:"id"`
	ChannelID uuid.UUID   `json:"channel_id"`
	Author    userPayload `json:"author"`
	Content   *string     `json:"content"`
	CreatedAt time.Time   `json:"created_at"`
	EditedAt  *time.Time  `json:"edited_at"`
}

type userPayload struct {
	ID          uuid.UUID `json:"id"`
	DisplayName string    `json:"display_name"`
	AvatarURL   *string   `json:"avatar_url"`
}

type messageDeletePayload struct {
	ID        uuid.UUID `json:"id"`
	ChannelID uuid.UUID `json:"channel_id"`
}

type messageReactionPayload struct {
	MessageID uuid.UUID `json:"message_id"`
	ChannelID uuid.UUID `json:"channel_id"`
	UserID    uuid.UUID `json:"user_id"`
	Emoji     string    `json:"emoji"`
	Count     int       `json:"count"`
}

type typingStartPayload struct {
	ChannelID uuid.UUID   `json:"channel_id"`
	User      userPayload `json:"user"`
	StartedAt time.Time   `json:"started_at"`
}

type presencePayload struct {
	UserID     uuid.UUID `json:"user_id"`
	Status     string    `json:"status"`
	CustomText *string   `json:"custom_text,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type guildMemberPayload struct {
	GuildID  uuid.UUID       `json:"guild_id"`
	User     userPayload     `json:"user"`
	Nickname *string         `json:"nickname"`
	RoleIDs  []uuid.UUID     `json:"role_ids"`
	JoinedAt time.Time       `json:"joined_at"`
	Presence presencePayload `json:"presence"`
}

type memberLeavePayload struct {
	GuildID uuid.UUID `json:"guild_id"`
	UserID  uuid.UUID `json:"user_id"`
}

type presenceUpdatePayload struct {
	GuildID  uuid.UUID       `json:"guild_id"`
	Presence presencePayload `json:"presence"`
}

func communityPresencePayload(value community.Presence) presencePayload {
	return presencePayload{UserID: value.UserID, Status: value.Status, CustomText: value.CustomText, UpdatedAt: value.UpdatedAt}
}

func communityMemberPayload(value community.Member) guildMemberPayload {
	return guildMemberPayload{
		GuildID:  value.GuildID,
		User:     userPayload{ID: value.User.ID, DisplayName: value.User.DisplayName, AvatarURL: value.User.AvatarURL},
		Nickname: value.Nickname, RoleIDs: value.RoleIDs, JoinedAt: value.JoinedAt,
		Presence: communityPresencePayload(value.Presence),
	}
}

func messageEventPayload(message Message, includeContent bool) messagePayload {
	var content *string
	attachments := message.Attachments
	if attachments == nil {
		attachments = []Attachment{}
	}
	if includeContent {
		content = &message.Content
	}
	payload := messagePayload{
		ID: message.ID, ChannelID: message.ChannelID,
		Author:  userPayload{ID: message.Author.ID, DisplayName: message.Author.DisplayName, AvatarURL: message.Author.AvatarURL},
		Content: content, ReplyToMessageID: message.ReplyToMessageID,
		CreatedAt: message.CreatedAt, EditedAt: message.EditedAt, Attachments: attachments,
	}
	if message.ReplyTo != nil {
		var replyContent *string
		if includeContent {
			replyContent = &message.ReplyTo.Content
		}
		payload.ReplyTo = &messageReplyPayload{
			ID: message.ReplyTo.ID, ChannelID: message.ReplyTo.ChannelID,
			Author: userPayload{
				ID: message.ReplyTo.Author.ID, DisplayName: message.ReplyTo.Author.DisplayName, AvatarURL: message.ReplyTo.Author.AvatarURL,
			},
			Content: replyContent, CreatedAt: message.ReplyTo.CreatedAt, EditedAt: message.ReplyTo.EditedAt,
		}
	}
	return payload
}
