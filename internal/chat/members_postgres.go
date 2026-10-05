package chat

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

const memberColumns = `
		gm.guild_id, u.id, u.display_name, u.avatar_url, gm.nickname, gm.joined_at,
		COALESCE(ARRAY(
			SELECT mr.role_id FROM guild_member_roles mr
			WHERE mr.guild_id = gm.guild_id AND mr.user_id = gm.user_id
			ORDER BY mr.role_id), '{}'::uuid[])`

const inviteColumns = `
		i.id, i.code, i.uses, i.max_uses, i.expires_at, i.created_at,
		g.id, g.owner_id, g.name, g.description, g.icon_url, g.created_at,
		u.id, u.display_name, u.avatar_url`

func scanMember(row rowScanner, member *Member) error {
	return row.Scan(
		&member.GuildID, &member.User.ID, &member.User.DisplayName, &member.User.AvatarURL,
		&member.Nickname, &member.JoinedAt, &member.RoleIDs,
	)
}

func scanInvite(row rowScanner, invite *Invite) error {
	return row.Scan(
		&invite.ID, &invite.Code, &invite.Uses, &invite.MaxUses, &invite.ExpiresAt, &invite.CreatedAt,
		&invite.Guild.ID, &invite.Guild.OwnerID, &invite.Guild.Name, &invite.Guild.Description, &invite.Guild.IconURL, &invite.Guild.CreatedAt,
		&invite.Inviter.ID, &invite.Inviter.DisplayName, &invite.Inviter.AvatarURL,
	)
}

func (s *PostgresStore) ListMembers(ctx context.Context, userID, guildID uuid.UUID, cursor *pageCursor, limit int) ([]Member, error) {
	var isMember bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM guild_members WHERE guild_id = $1 AND user_id = $2)`, guildID, userID).Scan(&isMember); err != nil {
		return nil, fmt.Errorf("check guild membership: %w", err)
	}
	if !isMember {
		return nil, ErrNotFound
	}
	var cursorTime *time.Time
	cursorID := uuid.Nil
	if cursor != nil {
		cursorTime, cursorID = &cursor.Time, cursor.ID
	}
	rows, err := s.pool.Query(ctx, `
		SELECT`+memberColumns+`
		FROM guild_members gm
		JOIN users u ON u.id = gm.user_id
		WHERE gm.guild_id = $1
		  AND ($2::timestamptz IS NULL OR (gm.joined_at, gm.user_id) > ($2, $3))
		ORDER BY gm.joined_at, gm.user_id
		LIMIT $4`, guildID, cursorTime, cursorID, limit)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()
	items := make([]Member, 0, limit)
	for rows.Next() {
		var item Member
		if err := scanMember(rows, &item); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate members: %w", err)
	}
	return items, nil
}

func (s *PostgresStore) GetMember(ctx context.Context, requesterID, guildID, userID uuid.UUID) (Member, error) {
	var member Member
	err := scanMember(s.pool.QueryRow(ctx, `
		SELECT`+memberColumns+`
		FROM guild_members gm
		JOIN users u ON u.id = gm.user_id
		WHERE gm.guild_id = $1 AND gm.user_id = $2
		  AND EXISTS (SELECT 1 FROM guild_members self WHERE self.guild_id = $1 AND self.user_id = $3)`,
		guildID, userID, requesterID), &member)
	if errors.Is(err, pgx.ErrNoRows) {
		return Member{}, ErrNotFound
	}
	if err != nil {
		return Member{}, fmt.Errorf("get member: %w", err)
	}
	return member, nil
}

func (s *PostgresStore) UpdateMemberNickname(ctx context.Context, guildID, userID uuid.UUID, nickname *string) (Member, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE guild_members SET nickname = $3 WHERE guild_id = $1 AND user_id = $2`, guildID, userID, nickname)
	if err != nil {
		return Member{}, fmt.Errorf("update member nickname: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return Member{}, ErrNotFound
	}
	return s.getMemberByID(ctx, s.pool, guildID, userID)
}

func (s *PostgresStore) GetMemberByID(ctx context.Context, guildID, userID uuid.UUID) (Member, error) {
	return s.getMemberByID(ctx, s.pool, guildID, userID)
}

func (s *PostgresStore) getMemberByID(ctx context.Context, queryer queryRower, guildID, userID uuid.UUID) (Member, error) {
	var member Member
	err := scanMember(queryer.QueryRow(ctx, `
		SELECT`+memberColumns+`
		FROM guild_members gm
		JOIN users u ON u.id = gm.user_id
		WHERE gm.guild_id = $1 AND gm.user_id = $2`, guildID, userID), &member)
	if errors.Is(err, pgx.ErrNoRows) {
		return Member{}, ErrNotFound
	}
	if err != nil {
		return Member{}, fmt.Errorf("get member: %w", err)
	}
	return member, nil
}

func (s *PostgresStore) RemoveMember(ctx context.Context, guildID, userID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM guild_members WHERE guild_id = $1 AND user_id = $2`, guildID, userID)
	if err != nil {
		return fmt.Errorf("remove member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) ListUserGuildIDs(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `SELECT guild_id FROM guild_members WHERE user_id = $1 ORDER BY guild_id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list user guild IDs: %w", err)
	}
	defer rows.Close()
	guildIDs := make([]uuid.UUID, 0)
	for rows.Next() {
		var guildID uuid.UUID
		if err := rows.Scan(&guildID); err != nil {
			return nil, fmt.Errorf("scan user guild ID: %w", err)
		}
		guildIDs = append(guildIDs, guildID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate user guild IDs: %w", err)
	}
	return guildIDs, nil
}

func (s *PostgresStore) ListGuildMemberIDs(ctx context.Context, guildID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `SELECT user_id FROM guild_members WHERE guild_id = $1 ORDER BY user_id`, guildID)
	if err != nil {
		return nil, fmt.Errorf("list guild member IDs: %w", err)
	}
	defer rows.Close()
	memberIDs := make([]uuid.UUID, 0)
	for rows.Next() {
		var memberID uuid.UUID
		if err := rows.Scan(&memberID); err != nil {
			return nil, fmt.Errorf("scan guild member ID: %w", err)
		}
		memberIDs = append(memberIDs, memberID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate guild member IDs: %w", err)
	}
	return memberIDs, nil
}

func (s *PostgresStore) CreateInvite(ctx context.Context, invite Invite) (Invite, error) {
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO guild_invites (id, guild_id, inviter_id, code, max_uses, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		invite.ID, invite.Guild.ID, invite.Inviter.ID, invite.Code, invite.MaxUses, invite.ExpiresAt, invite.CreatedAt,
	); err != nil {
		return Invite{}, fmt.Errorf("insert invite: %w", err)
	}
	var created Invite
	err := scanInvite(s.pool.QueryRow(ctx, `
		SELECT`+inviteColumns+`
		FROM guild_invites i
		JOIN guilds g ON g.id = i.guild_id
		JOIN users u ON u.id = i.inviter_id
		WHERE i.id = $1`, invite.ID), &created)
	if err != nil {
		return Invite{}, fmt.Errorf("get created invite: %w", err)
	}
	return created, nil
}

func (s *PostgresStore) ListInvites(ctx context.Context, guildID uuid.UUID, now time.Time) ([]Invite, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT`+inviteColumns+`
		FROM guild_invites i
		JOIN guilds g ON g.id = i.guild_id
		JOIN users u ON u.id = i.inviter_id
		WHERE i.guild_id = $1
		  AND i.revoked_at IS NULL
		  AND (i.expires_at IS NULL OR i.expires_at > $2)
		  AND (i.max_uses IS NULL OR i.uses < i.max_uses)
		ORDER BY i.created_at DESC, i.id DESC
		LIMIT 1000`, guildID, now)
	if err != nil {
		return nil, fmt.Errorf("list invites: %w", err)
	}
	defer rows.Close()
	items := make([]Invite, 0)
	for rows.Next() {
		var item Invite
		if err := scanInvite(rows, &item); err != nil {
			return nil, fmt.Errorf("scan invite: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate invites: %w", err)
	}
	return items, nil
}

func (s *PostgresStore) GetInvite(ctx context.Context, code string, now time.Time) (Invite, error) {
	var invite Invite
	err := scanInvite(s.pool.QueryRow(ctx, `
		SELECT`+inviteColumns+`
		FROM guild_invites i
		JOIN guilds g ON g.id = i.guild_id
		JOIN users u ON u.id = i.inviter_id
		WHERE i.code = $1
		  AND i.revoked_at IS NULL
		  AND (i.expires_at IS NULL OR i.expires_at > $2)
		  AND (i.max_uses IS NULL OR i.uses < i.max_uses)`, code, now), &invite)
	if errors.Is(err, pgx.ErrNoRows) {
		return Invite{}, ErrNotFound
	}
	if err != nil {
		return Invite{}, fmt.Errorf("get invite: %w", err)
	}
	return invite, nil
}

func (s *PostgresStore) RevokeInvite(ctx context.Context, guildID, inviteID uuid.UUID, revokedAt time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE guild_invites SET revoked_at = $3
		WHERE id = $2 AND guild_id = $1 AND revoked_at IS NULL`, guildID, inviteID, revokedAt)
	if err != nil {
		return fmt.Errorf("revoke invite: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AcceptInvite adds userID to the Invite's Guild. The returned bool reports whether
// a new membership was created; an existing Member is returned unchanged without
// consuming a use, which makes retries idempotent.
func (s *PostgresStore) AcceptInvite(ctx context.Context, code string, userID uuid.UUID, now time.Time) (Member, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Member{}, false, fmt.Errorf("begin invite acceptance: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var inviteID, guildID uuid.UUID
	var maxUses *int
	var uses int
	var expiresAt, revokedAt *time.Time
	err = tx.QueryRow(ctx, `
		SELECT id, guild_id, max_uses, uses, expires_at, revoked_at
		FROM guild_invites WHERE code = $1 FOR UPDATE`, code,
	).Scan(&inviteID, &guildID, &maxUses, &uses, &expiresAt, &revokedAt)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && revokedAt != nil) {
		return Member{}, false, ErrNotFound
	}
	if err != nil {
		return Member{}, false, fmt.Errorf("lock invite: %w", err)
	}

	existing, err := s.getMemberByID(ctx, tx, guildID, userID)
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Member{}, false, err
	}

	if (expiresAt != nil && !expiresAt.After(now)) || (maxUses != nil && uses >= *maxUses) {
		return Member{}, false, ErrInviteUnavailable
	}
	if _, err := tx.Exec(ctx, `INSERT INTO guild_members (guild_id, user_id, joined_at) VALUES ($1, $2, $3)`, guildID, userID, now); err != nil {
		return Member{}, false, fmt.Errorf("insert guild member: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE guild_invites SET uses = uses + 1 WHERE id = $1`, inviteID); err != nil {
		return Member{}, false, fmt.Errorf("increment invite uses: %w", err)
	}
	member, err := s.getMemberByID(ctx, tx, guildID, userID)
	if err != nil {
		return Member{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Member{}, false, fmt.Errorf("commit invite acceptance: %w", err)
	}
	return member, true, nil
}
