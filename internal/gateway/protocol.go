package gateway

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

const (
	opDispatch       = 0
	opHeartbeat      = 1
	opIdentify       = 2
	opResume         = 6
	opInvalidSession = 9
	opHello          = 10
	opHeartbeatAck   = 11

	intentGuildMembers   int64 = 1 << 1
	intentGuildMessages  int64 = 1 << 2
	intentMessageContent int64 = 1 << 4
	intentReactions      int64 = 1 << 7
	intentTyping         int64 = 1 << 9
)

const (
	eventReady                 = "READY"
	eventResumed               = "RESUMED"
	eventMemberJoin            = "MEMBER_JOIN"
	eventMemberUpdate          = "MEMBER_UPDATE"
	eventMemberLeave           = "MEMBER_LEAVE"
	eventMessageCreate         = "MESSAGE_CREATE"
	eventMessageUpdate         = "MESSAGE_UPDATE"
	eventMessageDelete         = "MESSAGE_DELETE"
	eventMessageReactionAdd    = "MESSAGE_REACTION_ADD"
	eventMessageReactionRemove = "MESSAGE_REACTION_REMOVE"
	eventTypingStart           = "TYPING_START"
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

// Member is a Guild Member as delivered by MEMBER_JOIN and MEMBER_UPDATE.
type Member struct {
	GuildID  uuid.UUID
	User     UserSummary
	Nickname *string
	RoleIDs  []uuid.UUID
	JoinedAt time.Time
}

type UserSummary struct {
	ID          uuid.UUID
	DisplayName string
	AvatarURL   *string
}

type messagePayload struct {
	ID               uuid.UUID            `json:"id"`
	ChannelID        uuid.UUID            `json:"channel_id"`
	Author           userPayload          `json:"author"`
	Content          *string              `json:"content"`
	ReplyToMessageID *uuid.UUID           `json:"reply_to_message_id"`
	ReplyTo          *messageReplyPayload `json:"reply_to"`
	Attachments      []struct{}           `json:"attachments"`
	CreatedAt        time.Time            `json:"created_at"`
	EditedAt         *time.Time           `json:"edited_at"`
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

func messageEventPayload(message Message, includeContent bool) messagePayload {
	var content *string
	if includeContent {
		content = &message.Content
	}
	payload := messagePayload{
		ID: message.ID, ChannelID: message.ChannelID,
		Author:  userPayload{ID: message.Author.ID, DisplayName: message.Author.DisplayName, AvatarURL: message.Author.AvatarURL},
		Content: content, ReplyToMessageID: message.ReplyToMessageID, Attachments: []struct{}{},
		CreatedAt: message.CreatedAt, EditedAt: message.EditedAt,
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

type memberPayload struct {
	GuildID  uuid.UUID       `json:"guild_id"`
	User     userPayload     `json:"user"`
	Nickname *string         `json:"nickname"`
	RoleIDs  []uuid.UUID     `json:"role_ids"`
	JoinedAt time.Time       `json:"joined_at"`
	Presence presencePayload `json:"presence"`
}

type presencePayload struct {
	UserID     uuid.UUID `json:"user_id"`
	Status     string    `json:"status"`
	CustomText *string   `json:"custom_text"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type memberLeavePayload struct {
	GuildID uuid.UUID `json:"guild_id"`
	UserID  uuid.UUID `json:"user_id"`
}

func memberEventPayload(member Member) memberPayload {
	return memberPayload{
		GuildID:  member.GuildID,
		User:     userPayload{ID: member.User.ID, DisplayName: member.User.DisplayName, AvatarURL: member.User.AvatarURL},
		Nickname: member.Nickname, RoleIDs: nonNilIDs(member.RoleIDs), JoinedAt: member.JoinedAt,
		// Presence is not tracked yet, so every Member is reported as offline.
		Presence: presencePayload{UserID: member.User.ID, Status: "OFFLINE", UpdatedAt: member.JoinedAt},
	}
}

func nonNilIDs(ids []uuid.UUID) []uuid.UUID {
	if ids == nil {
		return []uuid.UUID{}
	}
	return ids
}
