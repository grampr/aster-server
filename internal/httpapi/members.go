package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	protocolgo "github.com/grampr/Aster-protocol/packages/protocol-go/generated"
	"github.com/grampr/aster-server/internal/auth"
	"github.com/grampr/aster-server/internal/chat"
	"github.com/grampr/aster-server/internal/gateway"
)

type memberPatchBody struct {
	Nickname optionalNullableString `json:"nickname,omitempty"`
	RoleIDs  *[]uuid.UUID           `json:"role_ids,omitempty"`
}

func (s *Server) listMembers(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "members_list")
	if !ok {
		return
	}
	guildID, ok := s.pathID(writer, request, "guild_id")
	if !ok {
		return
	}
	cursor, limit, err := pagination(request)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	page, err := s.chat.ListMembers(request.Context(), user.ID, guildID, cursor, limit)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	items := make([]protocolgo.GuildMember, len(page.Items))
	for index := range page.Items {
		items[index] = memberResponse(page.Items[index])
	}
	writeJSON(writer, http.StatusOK, protocolgo.GuildMemberList{Items: items, Page: pageResponse(page.HasMore, page.NextCursor)})
}

func (s *Server) getMember(writer http.ResponseWriter, request *http.Request) {
	user, guildID, memberID, ok := s.memberRequest(writer, request, "members_get")
	if !ok {
		return
	}
	member, err := s.chat.GetMember(request.Context(), user.ID, guildID, memberID)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, memberResponse(member))
}

func (s *Server) updateMember(writer http.ResponseWriter, request *http.Request) {
	user, guildID, memberID, ok := s.memberRequest(writer, request, "members_update")
	if !ok {
		return
	}
	var body memberPatchBody
	if err := decodeJSON(writer, request, &body); err != nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	member, err := s.chat.UpdateMember(request.Context(), user.ID, guildID, memberID, chat.UpdateMemberInput{
		Nickname: chat.OptionalString{Set: body.Nickname.Set, Value: body.Nickname.Value},
		RoleIDs:  body.RoleIDs,
	})
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	if body.Nickname.Set || body.RoleIDs != nil {
		s.publishMember(request, "update", member)
	}
	writeJSON(writer, http.StatusOK, memberResponse(member))
}

func (s *Server) removeMember(writer http.ResponseWriter, request *http.Request) {
	user, guildID, memberID, ok := s.memberRequest(writer, request, "members_remove")
	if !ok {
		return
	}
	if err := s.chat.RemoveMember(request.Context(), user.ID, guildID, memberID); err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	s.publishMemberLeave(request, guildID, memberID)
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) leaveGuild(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "members_leave")
	if !ok {
		return
	}
	guildID, ok := s.pathID(writer, request, "guild_id")
	if !ok {
		return
	}
	if err := s.chat.LeaveGuild(request.Context(), user.ID, guildID); err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	s.publishMemberLeave(request, guildID, user.ID)
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) createInvite(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "invites_create")
	if !ok {
		return
	}
	guildID, ok := s.pathID(writer, request, "guild_id")
	if !ok {
		return
	}
	var body protocolgo.CreateInviteRequest
	if err := decodeJSON(writer, request, &body); err != nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	invite, err := s.chat.CreateInvite(request.Context(), user.ID, guildID, chat.CreateInviteInput{ExpiresIn: body.ExpiresIn, MaxUses: body.MaxUses})
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, inviteResponse(invite))
}

func (s *Server) listInvites(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "invites_list")
	if !ok {
		return
	}
	guildID, ok := s.pathID(writer, request, "guild_id")
	if !ok {
		return
	}
	invites, err := s.chat.ListInvites(request.Context(), user.ID, guildID)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	items := make([]protocolgo.Invite, len(invites))
	for index := range invites {
		items[index] = inviteResponse(invites[index])
	}
	writeJSON(writer, http.StatusOK, protocolgo.InviteList{Items: items})
}

func (s *Server) deleteInvite(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "invites_delete")
	if !ok {
		return
	}
	guildID, ok := s.pathID(writer, request, "guild_id")
	if !ok {
		return
	}
	inviteID, ok := s.pathID(writer, request, "invite_id")
	if !ok {
		return
	}
	if err := s.chat.RevokeInvite(request.Context(), user.ID, guildID, inviteID); err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) getInvite(writer http.ResponseWriter, request *http.Request) {
	if _, ok := s.chatUser(writer, request, "invites_get"); !ok {
		return
	}
	invite, err := s.chat.GetInvite(request.Context(), request.PathValue("invite_code"))
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, inviteResponse(invite))
}

func (s *Server) acceptInvite(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "invites_accept")
	if !ok {
		return
	}
	member, joined, err := s.chat.AcceptInvite(request.Context(), user.ID, request.PathValue("invite_code"))
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	if joined {
		s.publishMember(request, "join", member)
	}
	writeJSON(writer, http.StatusOK, memberResponse(member))
}

func (s *Server) memberRequest(writer http.ResponseWriter, request *http.Request, bucket string) (auth.User, uuid.UUID, uuid.UUID, bool) {
	user, ok := s.chatUser(writer, request, bucket)
	if !ok {
		return auth.User{}, uuid.Nil, uuid.Nil, false
	}
	guildID, ok := s.pathID(writer, request, "guild_id")
	if !ok {
		return auth.User{}, uuid.Nil, uuid.Nil, false
	}
	memberID, ok := s.pathID(writer, request, "user_id")
	if !ok {
		return auth.User{}, uuid.Nil, uuid.Nil, false
	}
	return user, guildID, memberID, true
}

func (s *Server) publishMember(request *http.Request, event string, member chat.Member) {
	if s.gateway == nil {
		return
	}
	publishContext, cancel := context.WithTimeout(context.WithoutCancel(request.Context()), 2*time.Second)
	defer cancel()
	recipients, err := s.chat.ListGuildMemberIDs(publishContext, member.GuildID)
	if err != nil {
		s.logger.Error("list gateway member recipients", "request_id", requestIDFromContext(request), "guild_id", member.GuildID, "error", err)
		return
	}
	payload := gateway.Member{
		GuildID: member.GuildID, Nickname: member.Nickname, RoleIDs: member.RoleIDs, JoinedAt: member.JoinedAt,
		User: gateway.UserSummary{ID: member.User.ID, DisplayName: member.User.DisplayName, AvatarURL: member.User.AvatarURL},
	}
	if member.Presence != nil {
		payload.Presence = &gateway.Presence{
			UserID: member.Presence.UserID, Status: member.Presence.Status,
			CustomText: member.Presence.CustomText, UpdatedAt: member.Presence.UpdatedAt,
		}
	}
	if event == "join" {
		s.gateway.PublishMemberJoin(recipients, payload)
		return
	}
	s.gateway.PublishMemberUpdate(recipients, payload)
}

// publishMemberLeave also notifies the removed User, who is no longer in the Guild.
func (s *Server) publishMemberLeave(request *http.Request, guildID, userID uuid.UUID) {
	if s.gateway == nil {
		return
	}
	publishContext, cancel := context.WithTimeout(context.WithoutCancel(request.Context()), 2*time.Second)
	defer cancel()
	recipients, err := s.chat.ListGuildMemberIDs(publishContext, guildID)
	if err != nil {
		s.logger.Error("list gateway member recipients", "request_id", requestIDFromContext(request), "guild_id", guildID, "error", err)
		return
	}
	s.gateway.PublishMemberLeave(append(recipients, userID), guildID, userID)
}

func memberResponse(member chat.Member) protocolgo.GuildMember {
	presence := protocolgo.Presence{UserId: member.User.ID, Status: protocolgo.PresenceStatusOFFLINE, UpdatedAt: member.JoinedAt}
	if member.Presence != nil {
		presence = protocolgo.Presence{
			UserId: member.Presence.UserID, Status: protocolgo.PresenceStatus(member.Presence.Status),
			CustomText: member.Presence.CustomText, UpdatedAt: member.Presence.UpdatedAt,
		}
	}
	return protocolgo.GuildMember{
		GuildId: member.GuildID, Nickname: member.Nickname, JoinedAt: member.JoinedAt, RoleIds: roleIDsResponse(member.RoleIDs),
		User:     protocolgo.UserSummary{Id: member.User.ID, DisplayName: member.User.DisplayName, AvatarUrl: member.User.AvatarURL},
		Presence: presence,
	}
}

func inviteResponse(invite chat.Invite) protocolgo.Invite {
	return protocolgo.Invite{
		Id: invite.ID, Code: invite.Code, Guild: guildResponse(invite.Guild),
		Inviter: protocolgo.UserSummary{Id: invite.Inviter.ID, DisplayName: invite.Inviter.DisplayName, AvatarUrl: invite.Inviter.AvatarURL},
		Uses:    invite.Uses, MaxUses: invite.MaxUses, ExpiresAt: invite.ExpiresAt, CreatedAt: invite.CreatedAt,
	}
}

func roleIDsResponse(ids []uuid.UUID) []protocolgo.UUID {
	response := make([]protocolgo.UUID, len(ids))
	copy(response, ids)
	return response
}

func (s *Server) updatePresence(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "presence_update")
	if !ok {
		return
	}
	var body struct {
		Status     string  `json:"status"`
		CustomText *string `json:"custom_text"`
	}
	if err := decodeJSON(writer, request, &body); err != nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	presence, guildIDs, err := s.chat.SetPresence(request.Context(), user.ID, body.Status, body.CustomText)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	s.publishPresence(request, guildIDs, presence)
	writeJSON(writer, http.StatusOK, protocolgo.Presence{
		UserId: presence.UserID, Status: protocolgo.PresenceStatus(presence.Status),
		CustomText: presence.CustomText, UpdatedAt: presence.UpdatedAt,
	})
}

func (s *Server) publishPresence(request *http.Request, guildIDs []uuid.UUID, presence chat.Presence) {
	if s.gateway == nil {
		return
	}
	publishContext, cancel := context.WithTimeout(context.WithoutCancel(request.Context()), 2*time.Second)
	defer cancel()
	payload := gateway.Presence{UserID: presence.UserID, Status: presence.Status, CustomText: presence.CustomText, UpdatedAt: presence.UpdatedAt}
	for _, guildID := range guildIDs {
		recipients, err := s.chat.ListGuildMemberIDs(publishContext, guildID)
		if err != nil {
			s.logger.Error("list gateway presence recipients", "request_id", requestIDFromContext(request), "guild_id", guildID, "error", err)
			continue
		}
		s.gateway.PublishPresenceUpdate(recipients, guildID, payload)
	}
}
