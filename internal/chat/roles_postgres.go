package chat

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const roleColumns = `id, guild_id, name, color, permissions, position, managed, is_default, created_at`

func scanRole(row rowScanner, role *Role) error {
	var permissions int32
	if err := row.Scan(&role.ID, &role.GuildID, &role.Name, &role.Color, &permissions, &role.Position, &role.Managed, &role.IsDefault, &role.CreatedAt); err != nil {
		return err
	}
	role.Permissions = int64(permissions)
	return nil
}

// GetAccess returns the effective authority of a Guild Member, or ErrNotFound for a non-member.
func (s *PostgresStore) GetAccess(ctx context.Context, guildID, userID uuid.UUID) (Access, error) {
	var isOwner bool
	var permissions int32
	var top int
	err := s.pool.QueryRow(ctx, `
		SELECT g.owner_id = gm.user_id,
		       COALESCE(bit_or(r.permissions), 0)::integer,
		       COALESCE(max(r.position), 0)
		FROM guild_members gm
		JOIN guilds g ON g.id = gm.guild_id
		LEFT JOIN guild_roles r ON r.guild_id = gm.guild_id AND (
			r.is_default OR EXISTS (
				SELECT 1 FROM guild_member_roles mr
				WHERE mr.guild_id = gm.guild_id AND mr.user_id = gm.user_id AND mr.role_id = r.id))
		WHERE gm.guild_id = $1 AND gm.user_id = $2
		GROUP BY g.owner_id, gm.user_id`, guildID, userID,
	).Scan(&isOwner, &permissions, &top)
	if errors.Is(err, pgx.ErrNoRows) {
		return Access{}, ErrNotFound
	}
	if err != nil {
		return Access{}, fmt.Errorf("get access: %w", err)
	}
	if isOwner {
		return ownerAccess(), nil
	}
	return Access{Permissions: int64(permissions), TopPosition: top}, nil
}

func (s *PostgresStore) ListRoles(ctx context.Context, guildID uuid.UUID) ([]Role, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+roleColumns+` FROM guild_roles WHERE guild_id = $1 ORDER BY position, id`, guildID)
	if err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}
	defer rows.Close()
	roles := make([]Role, 0)
	for rows.Next() {
		var role Role
		if err := scanRole(rows, &role); err != nil {
			return nil, fmt.Errorf("scan role: %w", err)
		}
		roles = append(roles, role)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate roles: %w", err)
	}
	return roles, nil
}

func (s *PostgresStore) CreateRole(ctx context.Context, role Role) (Role, error) {
	err := scanRole(s.pool.QueryRow(ctx, `
		INSERT INTO guild_roles (id, guild_id, name, color, permissions, position, managed, is_default, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, FALSE, FALSE, $7)
		RETURNING `+roleColumns,
		role.ID, role.GuildID, role.Name, role.Color, int32(role.Permissions), role.Position, role.CreatedAt), &role)
	if err != nil {
		return Role{}, fmt.Errorf("insert role: %w", err)
	}
	return role, nil
}

func (s *PostgresStore) UpdateRole(ctx context.Context, role Role) (Role, error) {
	err := scanRole(s.pool.QueryRow(ctx, `
		UPDATE guild_roles
		SET name = $3, color = $4, permissions = $5, position = $6
		WHERE guild_id = $1 AND id = $2
		RETURNING `+roleColumns,
		role.GuildID, role.ID, role.Name, role.Color, int32(role.Permissions), role.Position), &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return Role{}, ErrNotFound
	}
	if err != nil {
		return Role{}, fmt.Errorf("update role: %w", err)
	}
	return role, nil
}

func (s *PostgresStore) DeleteRole(ctx context.Context, guildID, roleID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM guild_roles WHERE guild_id = $1 AND id = $2 AND NOT managed`, guildID, roleID)
	if err != nil {
		return fmt.Errorf("delete role: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetMemberRoles replaces the non-default Roles assigned to a Member.
func (s *PostgresStore) SetMemberRoles(ctx context.Context, guildID, userID uuid.UUID, roleIDs []uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin role assignment: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM guild_member_roles WHERE guild_id = $1 AND user_id = $2`, guildID, userID); err != nil {
		return fmt.Errorf("clear member roles: %w", err)
	}
	for _, roleID := range roleIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO guild_member_roles (guild_id, user_id, role_id) VALUES ($1, $2, $3)`, guildID, userID, roleID); err != nil {
			return fmt.Errorf("assign member role: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit role assignment: %w", err)
	}
	return nil
}
