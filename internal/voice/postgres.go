package voice

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

func (s *PostgresStore) GetRoom(ctx context.Context, channelID uuid.UUID) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `SELECT provider_room_id FROM voice_rooms WHERE channel_id=$1`, channelID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}
func (s *PostgresStore) CreateRoom(ctx context.Context, channelID uuid.UUID, providerID string, createdAt time.Time) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `INSERT INTO voice_rooms(channel_id,provider_room_id,created_at) VALUES($1,$2,$3) ON CONFLICT(channel_id) DO UPDATE SET channel_id=EXCLUDED.channel_id RETURNING provider_room_id`, channelID, providerID, createdAt).Scan(&id)
	return id, err
}

func scanState(row pgx.Row, state *State) error {
	var channelID, sessionID uuid.UUID
	if err := row.Scan(&state.UserID, &channelID, &sessionID, &state.ProviderParticipantID, &state.SelfMute, &state.SelfDeaf, &state.SelfVideo, &state.SelfStream, &state.UpdatedAt, &state.ExpiresAt); err != nil {
		return err
	}
	state.ChannelID = &channelID
	state.SessionID = &sessionID
	return nil
}

func (s *PostgresStore) PutState(ctx context.Context, state State) (*State, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var previous State
	err = scanState(tx.QueryRow(ctx, `SELECT user_id,channel_id,session_id,provider_participant_id,self_mute,self_deaf,self_video,self_stream,updated_at,expires_at FROM voice_states WHERE user_id=$1 FOR UPDATE`, state.UserID), &previous)
	var prior *State
	if err == nil {
		prior = &previous
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO voice_states(user_id,channel_id,session_id,provider_participant_id,self_mute,self_deaf,self_video,self_stream,updated_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(user_id) DO UPDATE SET channel_id=EXCLUDED.channel_id,session_id=EXCLUDED.session_id,provider_participant_id=EXCLUDED.provider_participant_id,self_mute=EXCLUDED.self_mute,self_deaf=EXCLUDED.self_deaf,self_video=EXCLUDED.self_video,self_stream=EXCLUDED.self_stream,updated_at=EXCLUDED.updated_at,expires_at=EXCLUDED.expires_at`, state.UserID, *state.ChannelID, *state.SessionID, state.ProviderParticipantID, state.SelfMute, state.SelfDeaf, state.SelfVideo, state.SelfStream, state.UpdatedAt, state.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("save voice state: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return prior, nil
}

func (s *PostgresStore) GetState(ctx context.Context, userID uuid.UUID, now time.Time) (State, error) {
	var state State
	err := scanState(s.pool.QueryRow(ctx, `SELECT user_id,channel_id,session_id,provider_participant_id,self_mute,self_deaf,self_video,self_stream,updated_at,expires_at FROM voice_states WHERE user_id=$1 AND expires_at>$2`, userID, now), &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return State{}, ErrNotFound
	}
	return state, err
}
func (s *PostgresStore) ListStates(ctx context.Context, channelID uuid.UUID, now time.Time) ([]State, error) {
	_, _ = s.pool.Exec(ctx, `DELETE FROM voice_states WHERE expires_at<=$1`, now)
	rows, err := s.pool.Query(ctx, `SELECT user_id,channel_id,session_id,provider_participant_id,self_mute,self_deaf,self_video,self_stream,updated_at,expires_at FROM voice_states WHERE channel_id=$1 AND expires_at>$2 ORDER BY updated_at,user_id`, channelID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []State{}
	for rows.Next() {
		var item State
		if err := scanState(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (s *PostgresStore) UpdateState(ctx context.Context, userID uuid.UUID, input UpdateInput, updatedAt time.Time) (State, error) {
	var state State
	err := scanState(s.pool.QueryRow(ctx, `UPDATE voice_states SET self_mute=COALESCE($2,self_mute),self_deaf=COALESCE($3,self_deaf),self_video=COALESCE($4,self_video),self_stream=COALESCE($5,self_stream),updated_at=$6 WHERE user_id=$1 AND expires_at>$6 RETURNING user_id,channel_id,session_id,provider_participant_id,self_mute,self_deaf,self_video,self_stream,updated_at,expires_at`, userID, input.SelfMute, input.SelfDeaf, input.SelfVideo, input.SelfStream, updatedAt), &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return State{}, ErrNotFound
	}
	return state, err
}
func (s *PostgresStore) DeleteState(ctx context.Context, userID uuid.UUID) (State, error) {
	var state State
	err := scanState(s.pool.QueryRow(ctx, `DELETE FROM voice_states WHERE user_id=$1 RETURNING user_id,channel_id,session_id,provider_participant_id,self_mute,self_deaf,self_video,self_stream,updated_at,expires_at`, userID), &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return State{}, ErrNotFound
	}
	return state, err
}
