package community

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

const memberSelect = `
	SELECT gm.guild_id, u.id, u.display_name, u.avatar_url, gm.nickname, gm.joined_at,
	       COALESCE(array_agg(gmr.role_id ORDER BY r.position, gmr.role_id) FILTER (WHERE gmr.role_id IS NOT NULL), ARRAY[]::uuid[])
	FROM guild_members gm
	JOIN users u ON u.id = gm.user_id
	LEFT JOIN guild_member_roles gmr ON gmr.guild_id = gm.guild_id AND gmr.user_id = gm.user_id
	LEFT JOIN roles r ON r.id = gmr.role_id
`

func scanMember(row pgx.Row) (Member, error) {
	var member Member
	err := row.Scan(&member.GuildID, &member.User.ID, &member.User.DisplayName, &member.User.AvatarURL, &member.Nickname, &member.JoinedAt, &member.RoleIDs)
	return member, err
}

func (s *PostgresStore) ListMembers(ctx context.Context, actorID, guildID uuid.UUID, cursor *memberCursor, limit int) ([]Member, error) {
	var cursorTime *time.Time
	cursorID := uuid.Nil
	if cursor != nil {
		cursorTime, cursorID = &cursor.JoinedAt, cursor.UserID
	}
	rows, err := s.pool.Query(ctx, memberSelect+`
	WHERE gm.guild_id = $1
	  AND EXISTS (SELECT 1 FROM guild_members actor WHERE actor.guild_id = $1 AND actor.user_id = $2)
	  AND ($3::timestamptz IS NULL OR (gm.joined_at, gm.user_id) < ($3, $4))
	GROUP BY gm.guild_id, gm.user_id, u.id, u.display_name, u.avatar_url, gm.nickname, gm.joined_at
	ORDER BY gm.joined_at DESC, gm.user_id DESC LIMIT $5`, guildID, actorID, cursorTime, cursorID, limit)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()
	items := make([]Member, 0, limit)
	for rows.Next() {
		member, err := scanMember(rows)
		if err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		items = append(items, member)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate members: %w", err)
	}
	if len(items) == 0 {
		var allowed bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM guild_members WHERE guild_id=$1 AND user_id=$2)`, guildID, actorID).Scan(&allowed); err != nil {
			return nil, err
		}
		if !allowed {
			return nil, ErrNotFound
		}
	}
	return items, nil
}

func (s *PostgresStore) GetMember(ctx context.Context, actorID, guildID, userID uuid.UUID) (Member, error) {
	member, err := scanMember(s.pool.QueryRow(ctx, memberSelect+`
	WHERE gm.guild_id=$1 AND gm.user_id=$2
	  AND EXISTS (SELECT 1 FROM guild_members actor WHERE actor.guild_id=$1 AND actor.user_id=$3)
	GROUP BY gm.guild_id, gm.user_id, u.id, u.display_name, u.avatar_url, gm.nickname, gm.joined_at`, guildID, userID, actorID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Member{}, ErrNotFound
	}
	if err != nil {
		return Member{}, fmt.Errorf("get member: %w", err)
	}
	return member, nil
}

func (s *PostgresStore) UpdateMember(ctx context.Context, guildID, userID uuid.UUID, input UpdateMemberInput, changedAt time.Time) (Member, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Member{}, fmt.Errorf("begin member update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE guild_members SET nickname=CASE WHEN $3 THEN $4 ELSE nickname END WHERE guild_id=$1 AND user_id=$2`, guildID, userID, input.NicknameSet, input.Nickname)
	if err != nil {
		return Member{}, fmt.Errorf("update member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return Member{}, ErrNotFound
	}
	if input.RoleIDsSet {
		var valid int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM roles WHERE guild_id=$1 AND id=ANY($2) AND NOT managed`, guildID, input.RoleIDs).Scan(&valid); err != nil {
			return Member{}, err
		}
		if valid != len(input.RoleIDs) {
			return Member{}, &ValidationError{Field: "role_ids", Message: "contains an unknown or managed role"}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM guild_member_roles WHERE guild_id=$1 AND user_id=$2`, guildID, userID); err != nil {
			return Member{}, err
		}
		for _, roleID := range input.RoleIDs {
			if _, err := tx.Exec(ctx, `INSERT INTO guild_member_roles(guild_id,user_id,role_id,assigned_at) VALUES($1,$2,$3,$4)`, guildID, userID, roleID, changedAt); err != nil {
				return Member{}, err
			}
		}
	}
	member, err := scanMember(tx.QueryRow(ctx, memberSelect+` WHERE gm.guild_id=$1 AND gm.user_id=$2 GROUP BY gm.guild_id,gm.user_id,u.id,u.display_name,u.avatar_url,gm.nickname,gm.joined_at`, guildID, userID))
	if err != nil {
		return Member{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Member{}, err
	}
	return member, nil
}

func (s *PostgresStore) RemoveMember(ctx context.Context, guildID, userID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM guild_members WHERE guild_id=$1 AND user_id=$2`, guildID, userID)
	if err != nil {
		return fmt.Errorf("remove member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) IsOwner(ctx context.Context, guildID, userID uuid.UUID) (bool, error) {
	var owner bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM guilds WHERE id=$1 AND owner_id=$2)`, guildID, userID).Scan(&owner)
	return owner, err
}

func (s *PostgresStore) EffectivePermissions(ctx context.Context, guildID, userID uuid.UUID) (int64, error) {
	var permissions int64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(bit_or(r.permissions),0)
		FROM roles r
		WHERE r.guild_id=$1 AND (r.managed OR r.id IN (
			SELECT role_id FROM guild_member_roles WHERE guild_id=$1 AND user_id=$2
		)) AND EXISTS(SELECT 1 FROM guild_members WHERE guild_id=$1 AND user_id=$2)`, guildID, userID).Scan(&permissions)
	if err != nil {
		return 0, fmt.Errorf("effective permissions: %w", err)
	}
	var member bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM guild_members WHERE guild_id=$1 AND user_id=$2)`, guildID, userID).Scan(&member); err != nil {
		return 0, err
	}
	if !member {
		return 0, ErrNotFound
	}
	return permissions, nil
}

func (s *PostgresStore) ListRoles(ctx context.Context, actorID, guildID uuid.UUID) ([]Role, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,guild_id,name,color,permissions,position,managed,created_at FROM roles WHERE guild_id=$1 AND EXISTS(SELECT 1 FROM guild_members WHERE guild_id=$1 AND user_id=$2) ORDER BY position,id`, guildID, actorID)
	if err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}
	defer rows.Close()
	items := []Role{}
	for rows.Next() {
		var role Role
		if err := rows.Scan(&role.ID, &role.GuildID, &role.Name, &role.Color, &role.Permissions, &role.Position, &role.Managed, &role.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, role)
	}
	if len(items) == 0 {
		return nil, ErrNotFound
	}
	return items, rows.Err()
}

func (s *PostgresStore) CreateRole(ctx context.Context, role Role) (Role, error) {
	err := s.pool.QueryRow(ctx, `INSERT INTO roles(id,guild_id,name,color,permissions,position,managed,created_at,updated_at) SELECT $1,$2,$3,$4,$5,COALESCE(max(position),0)+1,false,$6,$6 FROM roles WHERE guild_id=$2 RETURNING position`, role.ID, role.GuildID, role.Name, role.Color, role.Permissions, role.CreatedAt).Scan(&role.Position)
	if err != nil {
		return Role{}, fmt.Errorf("create role: %w", err)
	}
	return role, nil
}

func (s *PostgresStore) UpdateRole(ctx context.Context, guildID, roleID uuid.UUID, input UpdateRoleInput, changedAt time.Time) (Role, error) {
	var role Role
	err := s.pool.QueryRow(ctx, `UPDATE roles SET name=COALESCE($3,name),color=CASE WHEN $4 THEN $5 ELSE color END,permissions=COALESCE($6,permissions),position=COALESCE($7,position),updated_at=$8 WHERE id=$1 AND guild_id=$2 AND NOT managed RETURNING id,guild_id,name,color,permissions,position,managed,created_at`, roleID, guildID, input.Name, input.ColorSet, input.Color, input.Permissions, input.Position, changedAt).Scan(&role.ID, &role.GuildID, &role.Name, &role.Color, &role.Permissions, &role.Position, &role.Managed, &role.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Role{}, ErrNotFound
	}
	if err != nil {
		return Role{}, fmt.Errorf("update role: %w", err)
	}
	return role, nil
}

func (s *PostgresStore) DeleteRole(ctx context.Context, guildID, roleID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM roles WHERE id=$1 AND guild_id=$2 AND NOT managed`, roleID, guildID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) CreateInvite(ctx context.Context, invite Invite) (Invite, error) {
	err := s.pool.QueryRow(ctx, `INSERT INTO guild_invites(id,guild_id,inviter_id,code,max_uses,expires_at,created_at) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING uses`, invite.ID, invite.Guild.ID, invite.Inviter.ID, invite.Code, invite.MaxUses, invite.ExpiresAt, invite.CreatedAt).Scan(&invite.Uses)
	if err != nil {
		return Invite{}, fmt.Errorf("create invite: %w", err)
	}
	return s.GetInvite(ctx, invite.Code, invite.CreatedAt)
}

const inviteSelect = `SELECT i.id,i.code,g.id,g.owner_id,g.name,g.description,g.icon_url,g.created_at,u.id,u.display_name,u.avatar_url,i.uses,i.max_uses,i.expires_at,i.created_at FROM guild_invites i JOIN guilds g ON g.id=i.guild_id JOIN users u ON u.id=i.inviter_id `

func scanInvite(row pgx.Row) (Invite, error) {
	var i Invite
	err := row.Scan(&i.ID, &i.Code, &i.Guild.ID, &i.Guild.OwnerID, &i.Guild.Name, &i.Guild.Description, &i.Guild.IconURL, &i.Guild.CreatedAt, &i.Inviter.ID, &i.Inviter.DisplayName, &i.Inviter.AvatarURL, &i.Uses, &i.MaxUses, &i.ExpiresAt, &i.CreatedAt)
	return i, err
}

func (s *PostgresStore) ListInvites(ctx context.Context, actorID, guildID uuid.UUID, now time.Time) ([]Invite, error) {
	rows, err := s.pool.Query(ctx, inviteSelect+`WHERE i.guild_id=$1 AND i.revoked_at IS NULL AND (i.expires_at IS NULL OR i.expires_at>$3) AND (i.max_uses IS NULL OR i.uses<i.max_uses) AND EXISTS(SELECT 1 FROM guild_members WHERE guild_id=$1 AND user_id=$2) ORDER BY i.created_at DESC`, guildID, actorID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Invite{}
	for rows.Next() {
		i, err := scanInvite(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

func (s *PostgresStore) GetInvite(ctx context.Context, code string, now time.Time) (Invite, error) {
	i, err := scanInvite(s.pool.QueryRow(ctx, inviteSelect+`WHERE i.code=$1 AND i.revoked_at IS NULL AND (i.expires_at IS NULL OR i.expires_at>$2) AND (i.max_uses IS NULL OR i.uses<i.max_uses)`, code, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return Invite{}, ErrNotFound
	}
	if err != nil {
		return Invite{}, err
	}
	return i, nil
}

func (s *PostgresStore) AcceptInvite(ctx context.Context, code string, userID uuid.UUID, now time.Time) (Member, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Member{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var guildID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT guild_id FROM guild_invites WHERE code=$1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>$2) AND (max_uses IS NULL OR uses<max_uses) FOR UPDATE`, code, now).Scan(&guildID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Member{}, ErrInviteExpired
	}
	if err != nil {
		return Member{}, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO guild_members(guild_id,user_id,joined_at) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, guildID, userID, now)
	if err != nil {
		return Member{}, err
	}
	if tag.RowsAffected() == 1 {
		if _, err := tx.Exec(ctx, `UPDATE guild_invites SET uses=uses+1 WHERE code=$1`, code); err != nil {
			return Member{}, err
		}
	}
	member, err := scanMember(tx.QueryRow(ctx, memberSelect+`WHERE gm.guild_id=$1 AND gm.user_id=$2 GROUP BY gm.guild_id,gm.user_id,u.id,u.display_name,u.avatar_url,gm.nickname,gm.joined_at`, guildID, userID))
	if err != nil {
		return Member{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Member{}, err
	}
	return member, nil
}

func (s *PostgresStore) DeleteInvite(ctx context.Context, guildID, inviteID uuid.UUID, now time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE guild_invites SET revoked_at=$3 WHERE id=$1 AND guild_id=$2 AND revoked_at IS NULL`, inviteID, guildID, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) ListGuildMemberIDs(ctx context.Context, guildID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `SELECT user_id FROM guild_members WHERE guild_id=$1 ORDER BY user_id`, guildID)
	if err != nil {
		return nil, fmt.Errorf("list guild member IDs: %w", err)
	}
	defer rows.Close()
	items := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		items = append(items, id)
	}
	return items, rows.Err()
}

func (s *PostgresStore) ListUserGuildIDs(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `SELECT guild_id FROM guild_members WHERE user_id=$1 ORDER BY guild_id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list user guild IDs: %w", err)
	}
	defer rows.Close()
	items := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		items = append(items, id)
	}
	return items, rows.Err()
}
