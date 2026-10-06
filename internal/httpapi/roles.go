package httpapi

import (
	"net/http"

	protocolgo "github.com/grampr/Aster-protocol/packages/protocol-go/generated"
	"github.com/grampr/aster-server/internal/chat"
)

type rolePatchBody struct {
	Name        *string                `json:"name,omitempty"`
	Color       optionalNullableString `json:"color,omitempty"`
	Permissions *int64                 `json:"permissions,omitempty"`
	Position    *int                   `json:"position,omitempty"`
}

func (s *Server) listRoles(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "roles_list")
	if !ok {
		return
	}
	guildID, ok := s.pathID(writer, request, "guild_id")
	if !ok {
		return
	}
	roles, err := s.chat.ListRoles(request.Context(), user.ID, guildID)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	items := make([]protocolgo.Role, len(roles))
	for index := range roles {
		items[index] = roleResponse(roles[index])
	}
	writeJSON(writer, http.StatusOK, protocolgo.RoleList{Items: items})
}

func (s *Server) createRole(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "roles_create")
	if !ok {
		return
	}
	guildID, ok := s.pathID(writer, request, "guild_id")
	if !ok {
		return
	}
	var body protocolgo.CreateRoleRequest
	if err := decodeJSON(writer, request, &body); err != nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	role, err := s.chat.CreateRole(request.Context(), user.ID, guildID, chat.CreateRoleInput{
		Name: body.Name, Color: body.Color, Permissions: body.Permissions,
	})
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, roleResponse(role))
}

func (s *Server) updateRole(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "roles_update")
	if !ok {
		return
	}
	guildID, ok := s.pathID(writer, request, "guild_id")
	if !ok {
		return
	}
	roleID, ok := s.pathID(writer, request, "role_id")
	if !ok {
		return
	}
	var body rolePatchBody
	if err := decodeJSON(writer, request, &body); err != nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	role, err := s.chat.UpdateRole(request.Context(), user.ID, guildID, roleID, chat.UpdateRoleInput{
		Name: body.Name, Color: chat.OptionalString{Set: body.Color.Set, Value: body.Color.Value},
		Permissions: body.Permissions, Position: body.Position,
	})
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, roleResponse(role))
}

func (s *Server) deleteRole(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "roles_delete")
	if !ok {
		return
	}
	guildID, ok := s.pathID(writer, request, "guild_id")
	if !ok {
		return
	}
	roleID, ok := s.pathID(writer, request, "role_id")
	if !ok {
		return
	}
	if err := s.chat.DeleteRole(request.Context(), user.ID, guildID, roleID); err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func roleResponse(role chat.Role) protocolgo.Role {
	return protocolgo.Role{
		Id: role.ID, GuildId: role.GuildID, Name: role.Name, Color: role.Color,
		Permissions: role.Permissions, Position: role.Position, Managed: role.Managed, CreatedAt: role.CreatedAt,
	}
}
