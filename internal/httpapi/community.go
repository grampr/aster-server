package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	protocolgo "github.com/grampr/Aster-protocol/packages/protocol-go/generated"
	"github.com/grampr/aster-server/internal/community"
)

func (s *Server) listGuildMembers(w http.ResponseWriter, r *http.Request) {
	user, guildID, ok := s.communityGuildRequest(w, r, "members_list")
	if !ok {
		return
	}
	cursor, limit, err := pagination(r)
	if err != nil {
		s.handleChatError(w, r, err)
		return
	}
	page, err := s.community.ListMembers(r.Context(), user, guildID, cursor, limit)
	if err != nil {
		s.handleCommunityError(w, r, err)
		return
	}
	items := make([]protocolgo.GuildMember, len(page.Items))
	for i := range page.Items {
		items[i] = memberResponse(page.Items[i])
	}
	writeJSON(w, http.StatusOK, protocolgo.GuildMemberList{Items: items, Page: pageResponse(page.HasMore, page.NextCursor)})
}

func (s *Server) getGuildMember(w http.ResponseWriter, r *http.Request) {
	user, guildID, ok := s.communityGuildRequest(w, r, "members_get")
	if !ok {
		return
	}
	targetID, ok := s.pathID(w, r, "user_id")
	if !ok {
		return
	}
	member, err := s.community.GetMember(r.Context(), user, guildID, targetID)
	if err != nil {
		s.handleCommunityError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, memberResponse(member))
}

type memberPatchBody struct {
	Nickname optionalNullableString `json:"nickname,omitempty"`
	RoleIDs  *[]uuid.UUID           `json:"role_ids,omitempty"`
}

func (s *Server) updateGuildMember(w http.ResponseWriter, r *http.Request) {
	user, guildID, ok := s.communityGuildRequest(w, r, "members_update")
	if !ok {
		return
	}
	targetID, ok := s.pathID(w, r, "user_id")
	if !ok {
		return
	}
	var body memberPatchBody
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	input := community.UpdateMemberInput{NicknameSet: body.Nickname.Set, Nickname: body.Nickname.Value, RoleIDsSet: body.RoleIDs != nil}
	if body.RoleIDs != nil {
		input.RoleIDs = *body.RoleIDs
	}
	member, err := s.community.UpdateMember(r.Context(), user, guildID, targetID, input)
	if err != nil {
		s.handleCommunityError(w, r, err)
		return
	}
	s.publishMemberEvent(r, "update", member)
	writeJSON(w, http.StatusOK, memberResponse(member))
}

func (s *Server) removeGuildMember(w http.ResponseWriter, r *http.Request) {
	user, guildID, ok := s.communityGuildRequest(w, r, "members_remove")
	if !ok {
		return
	}
	targetID, ok := s.pathID(w, r, "user_id")
	if !ok {
		return
	}
	recipients, err := s.community.ListGuildMemberIDs(r.Context(), guildID)
	if err != nil {
		s.handleCommunityError(w, r, err)
		return
	}
	if err := s.community.RemoveMember(r.Context(), user, guildID, targetID); err != nil {
		s.handleCommunityError(w, r, err)
		return
	}
	if s.gateway != nil {
		s.gateway.PublishMemberLeave(recipients, guildID, targetID)
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) leaveGuild(w http.ResponseWriter, r *http.Request) {
	user, guildID, ok := s.communityGuildRequest(w, r, "guilds_leave")
	if !ok {
		return
	}
	recipients, err := s.community.ListGuildMemberIDs(r.Context(), guildID)
	if err != nil {
		s.handleCommunityError(w, r, err)
		return
	}
	if err := s.community.RemoveMember(r.Context(), user, guildID, user); err != nil {
		s.handleCommunityError(w, r, err)
		return
	}
	if s.gateway != nil {
		s.gateway.PublishMemberLeave(recipients, guildID, user)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listGuildRoles(w http.ResponseWriter, r *http.Request) {
	user, guildID, ok := s.communityGuildRequest(w, r, "roles_list")
	if !ok {
		return
	}
	roles, err := s.community.ListRoles(r.Context(), user, guildID)
	if err != nil {
		s.handleCommunityError(w, r, err)
		return
	}
	items := make([]protocolgo.Role, len(roles))
	for i := range roles {
		items[i] = roleResponse(roles[i])
	}
	writeJSON(w, http.StatusOK, protocolgo.RoleList{Items: items})
}
func (s *Server) createGuildRole(w http.ResponseWriter, r *http.Request) {
	user, guildID, ok := s.communityGuildRequest(w, r, "roles_create")
	if !ok {
		return
	}
	var body protocolgo.CreateRoleRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	permissions := int64(0)
	if body.Permissions != nil {
		permissions = *body.Permissions
	}
	role, err := s.community.CreateRole(r.Context(), user, guildID, community.CreateRoleInput{Name: body.Name, Color: body.Color, Permissions: permissions})
	if err != nil {
		s.handleCommunityError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, roleResponse(role))
}

type rolePatchBody struct {
	Name        *string                `json:"name,omitempty"`
	Color       optionalNullableString `json:"color,omitempty"`
	Permissions *int64                 `json:"permissions,omitempty"`
	Position    *int                   `json:"position,omitempty"`
}

func (s *Server) updateGuildRole(w http.ResponseWriter, r *http.Request) {
	user, guildID, ok := s.communityGuildRequest(w, r, "roles_update")
	if !ok {
		return
	}
	roleID, ok := s.pathID(w, r, "role_id")
	if !ok {
		return
	}
	var body rolePatchBody
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	role, err := s.community.UpdateRole(r.Context(), user, guildID, roleID, community.UpdateRoleInput{Name: body.Name, ColorSet: body.Color.Set, Color: body.Color.Value, Permissions: body.Permissions, Position: body.Position})
	if err != nil {
		s.handleCommunityError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, roleResponse(role))
}
func (s *Server) deleteGuildRole(w http.ResponseWriter, r *http.Request) {
	user, guildID, ok := s.communityGuildRequest(w, r, "roles_delete")
	if !ok {
		return
	}
	roleID, ok := s.pathID(w, r, "role_id")
	if !ok {
		return
	}
	if err := s.community.DeleteRole(r.Context(), user, guildID, roleID); err != nil {
		s.handleCommunityError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listGuildInvites(w http.ResponseWriter, r *http.Request) {
	user, guildID, ok := s.communityGuildRequest(w, r, "invites_list")
	if !ok {
		return
	}
	invites, err := s.community.ListInvites(r.Context(), user, guildID)
	if err != nil {
		s.handleCommunityError(w, r, err)
		return
	}
	items := make([]protocolgo.Invite, len(invites))
	for i := range invites {
		items[i] = inviteResponse(invites[i])
	}
	writeJSON(w, http.StatusOK, protocolgo.InviteList{Items: items})
}
func (s *Server) createGuildInvite(w http.ResponseWriter, r *http.Request) {
	user, guildID, ok := s.communityGuildRequest(w, r, "invites_create")
	if !ok {
		return
	}
	var body protocolgo.CreateInviteRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	var expires *time.Duration
	if body.ExpiresIn != nil {
		value := time.Duration(*body.ExpiresIn) * time.Second
		expires = &value
	}
	invite, err := s.community.CreateInvite(r.Context(), user, guildID, community.CreateInviteInput{ExpiresIn: expires, MaxUses: body.MaxUses})
	if err != nil {
		s.handleCommunityError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, inviteResponse(invite))
}
func (s *Server) deleteGuildInvite(w http.ResponseWriter, r *http.Request) {
	user, guildID, ok := s.communityGuildRequest(w, r, "invites_delete")
	if !ok {
		return
	}
	inviteID, ok := s.pathID(w, r, "invite_id")
	if !ok {
		return
	}
	if err := s.community.DeleteInvite(r.Context(), user, guildID, inviteID); err != nil {
		s.handleCommunityError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) getInvite(w http.ResponseWriter, r *http.Request) {
	user, ok := s.communityUser(w, r, "invites_get")
	if !ok {
		return
	}
	_ = user
	invite, err := s.community.GetInvite(r.Context(), r.PathValue("invite_code"))
	if err != nil {
		s.handleCommunityError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, inviteResponse(invite))
}
func (s *Server) acceptInvite(w http.ResponseWriter, r *http.Request) {
	user, ok := s.communityUser(w, r, "invites_accept")
	if !ok {
		return
	}
	member, err := s.community.AcceptInvite(r.Context(), r.PathValue("invite_code"), user)
	if err != nil {
		s.handleCommunityError(w, r, err)
		return
	}
	s.publishMemberEvent(r, "join", member)
	writeJSON(w, http.StatusOK, memberResponse(member))
}
func (s *Server) updateCurrentUserPresence(w http.ResponseWriter, r *http.Request) {
	user, ok := s.communityUser(w, r, "presence_update")
	if !ok {
		return
	}
	var body protocolgo.UpdatePresenceRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	presence, err := s.community.UpdatePresence(user, string(body.Status), body.CustomText)
	if err != nil {
		s.handleCommunityError(w, r, err)
		return
	}
	if s.gateway != nil {
		guildIDs, listErr := s.community.ListUserGuildIDs(r.Context(), user)
		if listErr != nil {
			s.logger.Error("list presence guilds", "user_id", user, "error", listErr)
		} else {
			for _, guildID := range guildIDs {
				recipients, recipientErr := s.community.ListGuildMemberIDs(r.Context(), guildID)
				if recipientErr != nil {
					s.logger.Error("list presence recipients", "guild_id", guildID, "error", recipientErr)
					continue
				}
				s.gateway.PublishPresenceUpdate(recipients, guildID, presence)
			}
		}
	}
	writeJSON(w, http.StatusOK, presenceResponse(presence))
}

func (s *Server) publishMemberEvent(r *http.Request, kind string, member community.Member) {
	if s.gateway == nil {
		return
	}
	recipients, err := s.community.ListGuildMemberIDs(r.Context(), member.GuildID)
	if err != nil {
		s.logger.Error("list member event recipients", "guild_id", member.GuildID, "error", err)
		return
	}
	if kind == "join" {
		s.gateway.PublishMemberJoin(recipients, member)
	} else {
		s.gateway.PublishMemberUpdate(recipients, member)
	}
}

func (s *Server) communityUser(w http.ResponseWriter, r *http.Request, bucket string) (uuid.UUID, bool) {
	user, ok := s.chatUser(w, r, bucket)
	if !ok {
		return uuid.Nil, false
	}
	return user.ID, true
}
func (s *Server) communityGuildRequest(w http.ResponseWriter, r *http.Request, bucket string) (uuid.UUID, uuid.UUID, bool) {
	user, ok := s.communityUser(w, r, bucket)
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	guildID, ok := s.pathID(w, r, "guild_id")
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	return user, guildID, true
}

func (s *Server) handleCommunityError(w http.ResponseWriter, r *http.Request, err error) {
	var validation *community.ValidationError
	switch {
	case errors.As(err, &validation):
		s.writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", validation.Error(), nil)
	case errors.Is(err, community.ErrForbidden), errors.Is(err, community.ErrOwnerCannotLeave):
		s.writeError(w, r, http.StatusForbidden, "FORBIDDEN", "You do not have permission to perform this operation", nil)
	case errors.Is(err, community.ErrNotFound):
		s.writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found", nil)
	case errors.Is(err, community.ErrInviteExpired):
		s.writeError(w, r, http.StatusConflict, "INVITE_UNAVAILABLE", "Invite is expired or has no remaining uses", nil)
	default:
		s.writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", err)
	}
}

func memberResponse(value community.Member) protocolgo.GuildMember {
	return protocolgo.GuildMember{GuildId: value.GuildID, User: protocolgo.UserSummary{Id: value.User.ID, DisplayName: value.User.DisplayName, AvatarUrl: value.User.AvatarURL}, Nickname: value.Nickname, RoleIds: value.RoleIDs, JoinedAt: value.JoinedAt, Presence: presenceResponse(value.Presence)}
}
func presenceResponse(value community.Presence) protocolgo.Presence {
	return protocolgo.Presence{UserId: value.UserID, Status: protocolgo.PresenceStatus(value.Status), CustomText: value.CustomText, UpdatedAt: value.UpdatedAt}
}
func roleResponse(value community.Role) protocolgo.Role {
	return protocolgo.Role{Id: value.ID, GuildId: value.GuildID, Name: value.Name, Color: value.Color, Permissions: value.Permissions, Position: value.Position, Managed: value.Managed, CreatedAt: value.CreatedAt}
}
func inviteResponse(value community.Invite) protocolgo.Invite {
	return protocolgo.Invite{Id: value.ID, Code: value.Code, Guild: protocolgo.Guild{Id: value.Guild.ID, OwnerId: value.Guild.OwnerID, Name: value.Guild.Name, Description: value.Guild.Description, IconUrl: value.Guild.IconURL, CreatedAt: value.Guild.CreatedAt}, Inviter: protocolgo.UserSummary{Id: value.Inviter.ID, DisplayName: value.Inviter.DisplayName, AvatarUrl: value.Inviter.AvatarURL}, Uses: value.Uses, MaxUses: value.MaxUses, ExpiresAt: value.ExpiresAt, CreatedAt: value.CreatedAt}
}
