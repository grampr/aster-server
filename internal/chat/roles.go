package chat

import (
	"context"
	"fmt"
	"regexp"
	"sort"

	"github.com/google/uuid"
)

var roleColorPattern = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// require returns the caller's Access when they hold permission. A non-member gets
// ErrNotFound so that the Guild's existence is not revealed.
func (s *Service) require(ctx context.Context, userID, guildID uuid.UUID, permission int64) (Access, error) {
	access, err := s.store.GetAccess(ctx, guildID, userID)
	if err != nil {
		return Access{}, err
	}
	if !access.Has(permission) {
		return Access{}, ErrForbidden
	}
	return access, nil
}

func (s *Service) ListRoles(ctx context.Context, userID, guildID uuid.UUID) ([]Role, error) {
	if _, err := s.store.GetAccess(ctx, guildID, userID); err != nil {
		return nil, err
	}
	return s.store.ListRoles(ctx, guildID)
}

func (s *Service) CreateRole(ctx context.Context, userID, guildID uuid.UUID, input CreateRoleInput) (Role, error) {
	access, err := s.require(ctx, userID, guildID, PermManageRoles)
	if err != nil {
		return Role{}, err
	}
	name, err := validateName("name", input.Name)
	if err != nil {
		return Role{}, err
	}
	color, err := validateRoleColor(input.Color)
	if err != nil {
		return Role{}, err
	}
	var permissions int64
	if input.Permissions != nil {
		if permissions, err = validatePermissions(*input.Permissions); err != nil {
			return Role{}, err
		}
	}
	if !access.Has(permissions) {
		return Role{}, ErrForbidden
	}
	position := 1
	if access.Owner {
		roles, err := s.store.ListRoles(ctx, guildID)
		if err != nil {
			return Role{}, err
		}
		position = roles[len(roles)-1].Position + 1
	} else if access.TopPosition <= position {
		// A new Role starts at the bottom, so the creator must outrank it.
		return Role{}, ErrForbidden
	}
	id, err := newUUIDv7()
	if err != nil {
		return Role{}, err
	}
	return s.store.CreateRole(ctx, Role{
		ID: id, GuildID: guildID, Name: name, Color: color, Permissions: permissions, Position: position, CreatedAt: s.now().UTC(),
	})
}

func (s *Service) UpdateRole(ctx context.Context, userID, guildID, roleID uuid.UUID, input UpdateRoleInput) (Role, error) {
	if input.Name == nil && !input.Color.Set && input.Permissions == nil && input.Position == nil {
		return Role{}, &ValidationError{Field: "body", Message: "must contain at least one field"}
	}
	access, err := s.require(ctx, userID, guildID, PermManageRoles)
	if err != nil {
		return Role{}, err
	}
	role, err := s.findRole(ctx, guildID, roleID)
	if err != nil {
		return Role{}, err
	}
	if !access.Outranks(role.Position) && !role.IsDefault {
		return Role{}, ErrForbidden
	}
	if role.IsDefault && (input.Name != nil || input.Color.Set || input.Position != nil) {
		return Role{}, &ValidationError{Field: "body", Message: "only permissions of the default role can be changed"}
	}
	if input.Name != nil {
		if role.Name, err = validateName("name", *input.Name); err != nil {
			return Role{}, err
		}
	}
	if input.Color.Set {
		if role.Color, err = validateRoleColor(input.Color.Value); err != nil {
			return Role{}, err
		}
	}
	if input.Permissions != nil {
		permissions, err := validatePermissions(*input.Permissions)
		if err != nil {
			return Role{}, err
		}
		// Only permissions the caller holds may be granted or revoked.
		if !access.Has(permissions ^ role.Permissions) {
			return Role{}, ErrForbidden
		}
		role.Permissions = permissions
	}
	if input.Position != nil {
		if *input.Position < 1 {
			return Role{}, &ValidationError{Field: "position", Message: "must be one or greater"}
		}
		if !access.Outranks(*input.Position) {
			return Role{}, ErrForbidden
		}
		role.Position = *input.Position
	}
	return s.store.UpdateRole(ctx, role)
}

func (s *Service) DeleteRole(ctx context.Context, userID, guildID, roleID uuid.UUID) error {
	access, err := s.require(ctx, userID, guildID, PermManageRoles)
	if err != nil {
		return err
	}
	role, err := s.findRole(ctx, guildID, roleID)
	if err != nil {
		return err
	}
	if role.Managed || !access.Outranks(role.Position) {
		return ErrForbidden
	}
	return s.store.DeleteRole(ctx, guildID, roleID)
}

func (s *Service) findRole(ctx context.Context, guildID, roleID uuid.UUID) (Role, error) {
	roles, err := s.store.ListRoles(ctx, guildID)
	if err != nil {
		return Role{}, err
	}
	for _, role := range roles {
		if role.ID == roleID {
			return role, nil
		}
	}
	return Role{}, ErrNotFound
}

// assignRoles replaces a Member's Roles after checking that the caller may add and
// remove every Role that changes.
func (s *Service) assignRoles(ctx context.Context, access Access, member Member, requested []uuid.UUID) error {
	roles, err := s.store.ListRoles(ctx, member.GuildID)
	if err != nil {
		return err
	}
	byID := make(map[uuid.UUID]Role, len(roles))
	for _, role := range roles {
		byID[role.ID] = role
	}
	next := make(map[uuid.UUID]struct{}, len(requested))
	for _, roleID := range requested {
		role, exists := byID[roleID]
		if !exists || role.Managed {
			return &ValidationError{Field: "role_ids", Message: fmt.Sprintf("role %s cannot be assigned", roleID)}
		}
		next[roleID] = struct{}{}
	}
	current := make(map[uuid.UUID]struct{}, len(member.RoleIDs))
	for _, roleID := range member.RoleIDs {
		current[roleID] = struct{}{}
	}
	for roleID := range next {
		if _, held := current[roleID]; !held && !access.Outranks(byID[roleID].Position) {
			return ErrForbidden
		}
	}
	for roleID := range current {
		if _, kept := next[roleID]; !kept && !access.Outranks(byID[roleID].Position) {
			return ErrForbidden
		}
	}
	ids := make([]uuid.UUID, 0, len(next))
	for roleID := range next {
		ids = append(ids, roleID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	return s.store.SetMemberRoles(ctx, member.GuildID, member.User.ID, ids)
}

func validateRoleColor(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	if !roleColorPattern.MatchString(*value) {
		return nil, &ValidationError{Field: "color", Message: "must be a #RRGGBB color"}
	}
	return value, nil
}

func validatePermissions(value int64) (int64, error) {
	if value < 0 || value&^AllPermissions != 0 {
		return 0, &ValidationError{Field: "permissions", Message: "contains unknown permission bits"}
	}
	return value, nil
}
