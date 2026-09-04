package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/grampr/aster-server/internal/auth"
	"github.com/grampr/aster-server/internal/community"
)

const (
	maxGatewayMessageBytes = 64 << 10
	writeTimeout           = 5 * time.Second
	closeUnauthorized      = 4001
	closeProtocolError     = 4002
	closeInvalidSession    = 4003
)

type Authenticator interface {
	Authenticate(context.Context, string) (auth.User, error)
}

type Config struct {
	URL               string
	HeartbeatInterval time.Duration
	IdentifyTimeout   time.Duration
	SessionRetention  time.Duration
	EventBufferSize   int
	AllowedOrigins    []string
}

type Service struct {
	auth              Authenticator
	hub               *hub
	logger            *slog.Logger
	heartbeatInterval time.Duration
	identifyTimeout   time.Duration
	allowedOrigins    map[string]struct{}
	eventBufferSize   int
	upgrader          websocket.Upgrader
}

func New(authenticator Authenticator, config Config, logger *slog.Logger) (*Service, error) {
	if authenticator == nil || logger == nil {
		return nil, errors.New("gateway authenticator and logger are required")
	}
	gatewayURL, err := url.Parse(config.URL)
	if err != nil || (gatewayURL.Scheme != "ws" && gatewayURL.Scheme != "wss") || gatewayURL.Host == "" || gatewayURL.User != nil || gatewayURL.Fragment != "" {
		return nil, errors.New("gateway URL must use ws or wss")
	}
	if config.HeartbeatInterval <= 0 || config.IdentifyTimeout <= 0 || config.SessionRetention <= 0 || config.EventBufferSize < 1 {
		return nil, errors.New("gateway timeouts and event buffer size must be positive")
	}
	allowedOrigins := make(map[string]struct{}, len(config.AllowedOrigins))
	for _, origin := range config.AllowedOrigins {
		origin = strings.TrimSuffix(strings.TrimSpace(origin), "/")
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Path != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, errors.New("gateway allowed origins must be absolute origins")
		}
		allowedOrigins[origin] = struct{}{}
	}
	service := &Service{
		auth: authenticator, hub: newHub(config.URL, config.SessionRetention, config.EventBufferSize), logger: logger,
		heartbeatInterval: config.HeartbeatInterval, identifyTimeout: config.IdentifyTimeout, allowedOrigins: allowedOrigins,
		eventBufferSize: config.EventBufferSize,
	}
	service.upgrader = websocket.Upgrader{CheckOrigin: service.checkOrigin}
	return service, nil
}

func (s *Service) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	connection, err := s.upgrader.Upgrade(writer, request, nil)
	if err != nil {
		return
	}
	connection.SetReadLimit(maxGatewayMessageBytes)
	client := newClient(connection, s.eventBufferSize+8)
	go client.writeLoop()
	defer client.terminate()

	if !client.enqueueJSON(gatewayMessage{Op: opHello, D: map[string]any{"heartbeat_interval_ms": s.heartbeatInterval.Milliseconds()}}) {
		return
	}
	_ = connection.SetReadDeadline(time.Now().Add(s.identifyTimeout))

	message, ok := s.readInbound(client)
	if !ok {
		return
	}
	var sessionID uuid.UUID
	var accessToken string
	switch message.Op {
	case opIdentify:
		var payload identifyPayload
		if err := decodePayload(message.D, &payload); err != nil || payload.Token == "" || payload.Intents < 0 || payload.Intents > 1<<31-1 {
			client.closeWith(closeProtocolError, "invalid identify payload")
			return
		}
		user, err := s.auth.Authenticate(request.Context(), payload.Token)
		if err != nil {
			client.closeWith(closeUnauthorized, "authentication failed")
			return
		}
		sessionID, err = s.hub.identify(user.ID, payload.Intents, client)
		if err != nil {
			if errors.Is(err, errSessionCapacity) {
				client.closeWith(websocket.CloseTryAgainLater, "gateway session capacity reached")
				return
			}
			s.logger.Error("create gateway session", "error", err)
			client.closeWith(websocket.CloseInternalServerErr, "internal error")
			return
		}
		accessToken = payload.Token
		s.logger.Info("gateway session identified", "session_id", sessionID, "user_id", user.ID, "intents", payload.Intents)
	case opResume:
		var payload resumePayload
		if err := decodePayload(message.D, &payload); err != nil || payload.Token == "" || payload.SessionID == uuid.Nil || payload.Sequence < 0 {
			client.closeWith(closeProtocolError, "invalid resume payload")
			return
		}
		user, err := s.auth.Authenticate(request.Context(), payload.Token)
		if err != nil {
			client.closeWith(closeUnauthorized, "authentication failed")
			return
		}
		if !s.hub.resume(user.ID, payload.SessionID, payload.Sequence, client) {
			client.writeJSONNow(gatewayMessage{Op: opInvalidSession, D: map[string]bool{"resumable": false}})
			client.closeWith(closeInvalidSession, "session cannot be resumed")
			return
		}
		sessionID = payload.SessionID
		accessToken = payload.Token
		s.logger.Info("gateway session resumed", "session_id", sessionID, "user_id", user.ID, "sequence", payload.Sequence)
	default:
		client.closeWith(closeProtocolError, "identify or resume is required")
		return
	}
	defer func() {
		s.hub.detach(sessionID, client)
		s.logger.Info("gateway session disconnected", "session_id", sessionID)
	}()
	_ = connection.SetReadDeadline(time.Now().Add(2 * s.heartbeatInterval))

	for {
		message, ok = s.readInbound(client)
		if !ok {
			return
		}
		if message.Op != opHeartbeat || !validHeartbeatPayload(message.D) {
			client.closeWith(closeProtocolError, "heartbeat is required")
			return
		}
		if _, err := s.auth.Authenticate(request.Context(), accessToken); err != nil {
			client.closeWith(closeUnauthorized, "session expired")
			return
		}
		if !client.enqueueJSON(gatewayMessage{Op: opHeartbeatAck, D: nil}) {
			return
		}
		_ = connection.SetReadDeadline(time.Now().Add(2 * s.heartbeatInterval))
	}
}

func (s *Service) PublishMessageCreate(recipients []uuid.UUID, direct bool, message Message) {
	s.hub.publishMessage(eventMessageCreate, messageIntent(direct), recipients, message)
}

func (s *Service) PublishMessageUpdate(recipients []uuid.UUID, direct bool, message Message) {
	s.hub.publishMessage(eventMessageUpdate, messageIntent(direct), recipients, message)
}

func (s *Service) PublishMessageDelete(recipients []uuid.UUID, direct bool, messageID, channelID uuid.UUID) {
	s.hub.publishMessageDelete(messageIntent(direct), recipients, messageID, channelID)
}

func messageIntent(direct bool) int64 {
	if direct {
		return intentDirectMessages
	}
	return intentGuildMessages
}

func (s *Service) PublishMessageReaction(recipients []uuid.UUID, add bool, reaction MessageReaction) {
	eventName := eventMessageReactionRemove
	if add {
		eventName = eventMessageReactionAdd
	}
	s.hub.publishMessageReaction(eventName, recipients, reaction)
}

func (s *Service) PublishTypingStart(recipients []uuid.UUID, typing TypingStart) {
	s.hub.publishTypingStart(recipients, typing)
}

func (s *Service) PublishMemberJoin(recipients []uuid.UUID, member community.Member) {
	s.hub.publishForIntent(eventMemberJoin, intentGuildMembers, recipients, communityMemberPayload(member))
}

func (s *Service) PublishMemberUpdate(recipients []uuid.UUID, member community.Member) {
	s.hub.publishForIntent(eventMemberUpdate, intentGuildMembers, recipients, communityMemberPayload(member))
}

func (s *Service) PublishMemberLeave(recipients []uuid.UUID, guildID, userID uuid.UUID) {
	s.hub.publishForIntent(eventMemberLeave, intentGuildMembers, recipients, memberLeavePayload{GuildID: guildID, UserID: userID})
}

func (s *Service) PublishPresenceUpdate(recipients []uuid.UUID, guildID uuid.UUID, presence community.Presence) {
	s.hub.publishForIntent(eventPresenceUpdate, intentGuildPresences, recipients, presenceUpdatePayload{GuildID: guildID, Presence: communityPresencePayload(presence)})
}

func (s *Service) PublishChannelCreate(recipients []uuid.UUID, direct bool, channel Channel) {
	s.hub.publishForIntent(eventChannelCreate, channelIntent(direct), recipients, channelEventPayload(channel))
}

func (s *Service) PublishChannelUpdate(recipients []uuid.UUID, direct bool, channel Channel) {
	s.hub.publishForIntent(eventChannelUpdate, channelIntent(direct), recipients, channelEventPayload(channel))
}

func (s *Service) PublishChannelDelete(recipients []uuid.UUID, direct bool, channelID uuid.UUID, guildID *uuid.UUID) {
	s.hub.publishForIntent(eventChannelDelete, channelIntent(direct), recipients, channelDeletePayload{ID: channelID, GuildID: guildID})
}

func (s *Service) PublishReadStateUpdate(userID uuid.UUID, state ReadState) {
	s.hub.publishForUser(eventReadStateUpdate, userID, readStatePayload{ChannelID: state.ChannelID, LastReadMessageID: state.LastReadMessageID, UpdatedAt: state.UpdatedAt})
}

func (s *Service) PublishVoiceStateUpdate(recipients []uuid.UUID, state VoiceState) {
	s.hub.publishForIntent(eventVoiceStateUpdate, 1<<5, recipients, state)
}

func channelIntent(direct bool) int64 {
	if direct {
		return intentDirectMessages
	}
	return intentGuilds
}

func channelEventPayload(channel Channel) channelPayload {
	recipients := make([]userPayload, len(channel.Recipients))
	for index, user := range channel.Recipients {
		recipients[index] = userPayload{ID: user.ID, DisplayName: user.DisplayName, AvatarURL: user.AvatarURL}
	}
	return channelPayload{
		ID: channel.ID, GuildID: channel.GuildID, ParentID: channel.ParentID, Type: channel.Type,
		Name: channel.Name, Topic: channel.Topic, Position: channel.Position, CreatedAt: channel.CreatedAt, Recipients: recipients,
	}
}

func (s *Service) readInbound(client *client) (inboundMessage, bool) {
	messageType, payload, err := client.connection.ReadMessage()
	if err != nil {
		return inboundMessage{}, false
	}
	if messageType != websocket.TextMessage {
		client.closeWith(closeProtocolError, "text messages are required")
		return inboundMessage{}, false
	}
	var message inboundMessage
	if err := json.Unmarshal(payload, &message); err != nil || len(message.D) == 0 {
		client.closeWith(websocket.CloseInvalidFramePayloadData, "invalid gateway message")
		return inboundMessage{}, false
	}
	return message, true
}

func (s *Service) checkOrigin(request *http.Request) bool {
	origin := strings.TrimSuffix(request.Header.Get("Origin"), "/")
	if origin == "" {
		return true
	}
	if _, allowed := s.allowedOrigins[origin]; allowed {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && parsed.Host == request.Host
}

func decodePayload(payload json.RawMessage, destination any) error {
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	return decoder.Decode(destination)
}

func validHeartbeatPayload(payload json.RawMessage) bool {
	if string(payload) == "null" {
		return true
	}
	var sequence int64
	return json.Unmarshal(payload, &sequence) == nil && sequence >= 0
}

type client struct {
	connection *websocket.Conn
	send       chan []byte
	done       chan struct{}
	once       sync.Once
	writeMu    sync.Mutex
}

func newClient(connection *websocket.Conn, queueSize int) *client {
	return &client{connection: connection, send: make(chan []byte, queueSize), done: make(chan struct{})}
}

func (c *client) enqueueJSON(message any) bool {
	payload, err := json.Marshal(message)
	return err == nil && c.enqueue(payload)
}

func (c *client) enqueue(payload []byte) bool {
	select {
	case <-c.done:
		return false
	case c.send <- payload:
		return true
	default:
		c.terminate()
		return false
	}
}

func (c *client) writeLoop() {
	for {
		select {
		case <-c.done:
			return
		case payload := <-c.send:
			if err := c.writePayload(payload); err != nil {
				c.terminate()
				return
			}
		}
	}
}

func (c *client) writeJSONNow(message any) bool {
	payload, err := json.Marshal(message)
	return err == nil && c.writePayload(payload) == nil
}

func (c *client) writePayload(payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.connection.SetWriteDeadline(time.Now().Add(writeTimeout))
	return c.connection.WriteMessage(websocket.TextMessage, payload)
}

func (c *client) closeWith(code int, reason string) {
	_ = c.connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(writeTimeout))
	c.terminate()
}

func (c *client) terminate() {
	c.once.Do(func() {
		close(c.done)
		_ = c.connection.Close()
	})
}
