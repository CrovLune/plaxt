package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/lib/pq"
)

type rowScanner interface {
	Scan(dest ...any) error
}

func scanFamilyGroupRow(rs rowScanner) (*FamilyGroup, error) {
	var fg FamilyGroup
	if err := rs.Scan(&fg.ID, &fg.PlexUsername, &fg.CreatedAt, &fg.UpdatedAt); err != nil {
		return nil, err
	}
	return &fg, nil
}

func scanGroupMemberRow(rs rowScanner) (*GroupMember, error) {
	var (
		gm          GroupMember
		trakt       sql.NullString
		access      sql.NullString
		refresh     sql.NullString
		tokenExpiry sql.NullTime
	)
	if err := rs.Scan(
		&gm.ID,
		&gm.FamilyGroupID,
		&gm.TempLabel,
		&trakt,
		&access,
		&refresh,
		&tokenExpiry,
		&gm.AuthorizationStatus,
		&gm.CreatedAt,
	); err != nil {
		return nil, err
	}

	gm.TraktUsername = strings.TrimSpace(trakt.String)
	gm.AccessToken = access.String
	gm.RefreshToken = refresh.String
	if tokenExpiry.Valid {
		gm.TokenExpiry = &tokenExpiry.Time
	}
	return &gm, nil
}

func nullableString(value string) sql.NullString {
	value = strings.TrimSpace(value)
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func nullableTime(t *time.Time) sql.NullTime {
	if t == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *t, Valid: true}
}

func (s PostgresqlStore) CreateFamilyGroup(ctx context.Context, group *FamilyGroup) error {
	if group == nil {
		return ErrInvalidFamilyGroup
	}
	if err := group.Validate(); err != nil {
		return err
	}
	if group.ID == "" {
		group.ID = uuid()
	}

	err := s.db.QueryRowContext(ctx, `
		INSERT INTO family_groups (id, plex_username)
		VALUES ($1, $2)
		RETURNING created_at, updated_at
	`, group.ID, group.PlexUsername).Scan(&group.CreatedAt, &group.UpdatedAt)
	if err != nil {
		if pqErr, ok := err.(*pq.Error); ok && pqErr.Code == "23505" {
			return ErrDuplicateFamilyGroup
		}
		return err
	}
	return nil
}

func (s PostgresqlStore) GetFamilyGroup(ctx context.Context, groupID string) (*FamilyGroup, error) {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return nil, ErrFamilyGroupNotFound
	}

	row := s.db.QueryRowContext(ctx, `
		SELECT id, plex_username, created_at, updated_at
		FROM family_groups
		WHERE id = $1
	`, groupID)

	fg, err := scanFamilyGroupRow(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrFamilyGroupNotFound
		}
		return nil, err
	}
	return fg, nil
}

func (s PostgresqlStore) GetFamilyGroupByPlex(ctx context.Context, plexUsername string) (*FamilyGroup, error) {
	plexUsername = strings.TrimSpace(strings.ToLower(plexUsername))
	if plexUsername == "" {
		return nil, ErrFamilyGroupNotFound
	}

	row := s.db.QueryRowContext(ctx, `
		SELECT id, plex_username, created_at, updated_at
		FROM family_groups
		WHERE plex_username = $1
	`, plexUsername)

	fg, err := scanFamilyGroupRow(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrFamilyGroupNotFound
		}
		return nil, err
	}
	return fg, nil
}

func (s PostgresqlStore) ListFamilyGroups(ctx context.Context) ([]*FamilyGroup, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, plex_username, created_at, updated_at
		FROM family_groups
		ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []*FamilyGroup
	for rows.Next() {
		fg, err := scanFamilyGroupRow(rows)
		if err != nil {
			return nil, err
		}
		groups = append(groups, fg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return groups, nil
}

func (s PostgresqlStore) DeleteFamilyGroup(ctx context.Context, groupID string) error {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return ErrFamilyGroupNotFound
	}

	res, err := s.db.ExecContext(ctx, `DELETE FROM family_groups WHERE id = $1`, groupID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrFamilyGroupNotFound
	}
	return nil
}

func (s PostgresqlStore) AddGroupMember(ctx context.Context, member *GroupMember) error {
	if member == nil {
		return ErrInvalidGroupMember
	}
	if member.AuthorizationStatus == "" {
		member.AuthorizationStatus = GroupMemberStatusPending
	}
	if member.ID == "" {
		member.ID = uuid()
	}
	if member.TraktUsername != "" {
		member.TraktUsername = strings.ToLower(member.TraktUsername)
	}
	if err := member.Validate(); err != nil {
		return err
	}

	err := s.db.QueryRowContext(ctx, `
		INSERT INTO group_members (
			id, family_group_id, temp_label, trakt_username,
			access_token, refresh_token, token_expiry, authorization_status
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING created_at
	`,
		member.ID,
		member.FamilyGroupID,
		member.TempLabel,
		nullableString(member.TraktUsername),
		nullableString(member.AccessToken),
		nullableString(member.RefreshToken),
		nullableTime(member.TokenExpiry),
		member.AuthorizationStatus,
	).Scan(&member.CreatedAt)
	if err != nil {
		if pqErr, ok := err.(*pq.Error); ok {
			switch pqErr.Code {
			case "23503":
				return ErrFamilyGroupNotFound
			case "23505":
				return ErrDuplicateGroupMember
			}
		}
		return err
	}
	return nil
}

func (s PostgresqlStore) GetGroupMember(ctx context.Context, memberID string) (*GroupMember, error) {
	memberID = strings.TrimSpace(memberID)
	if memberID == "" {
		return nil, ErrGroupMemberNotFound
	}

	row := s.db.QueryRowContext(ctx, `
		SELECT id, family_group_id, temp_label, trakt_username,
		       access_token, refresh_token, token_expiry, authorization_status, created_at
		FROM group_members
		WHERE id = $1
	`, memberID)

	gm, err := scanGroupMemberRow(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrGroupMemberNotFound
		}
		return nil, err
	}
	return gm, nil
}

func (s PostgresqlStore) UpdateGroupMember(ctx context.Context, member *GroupMember) error {
	if member == nil {
		return ErrInvalidGroupMember
	}
	if member.AuthorizationStatus == "" {
		member.AuthorizationStatus = GroupMemberStatusPending
	}
	if member.TraktUsername != "" {
		member.TraktUsername = strings.ToLower(member.TraktUsername)
	}
	if err := member.Validate(); err != nil {
		return err
	}

	res, err := s.db.ExecContext(ctx, `
		UPDATE group_members
		SET temp_label = $2,
			trakt_username = $3,
			access_token = $4,
			refresh_token = $5,
			token_expiry = $6,
			authorization_status = $7
		WHERE id = $1
	`,
		member.ID,
		member.TempLabel,
		nullableString(member.TraktUsername),
		nullableString(member.AccessToken),
		nullableString(member.RefreshToken),
		nullableTime(member.TokenExpiry),
		member.AuthorizationStatus,
	)
	if err != nil {
		if pqErr, ok := err.(*pq.Error); ok && pqErr.Code == "23505" {
			return ErrDuplicateGroupMember
		}
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrGroupMemberNotFound
	}
	return nil
}

func (s PostgresqlStore) RemoveGroupMember(ctx context.Context, groupID, memberID string) error {
	groupID = strings.TrimSpace(groupID)
	memberID = strings.TrimSpace(memberID)
	if groupID == "" || memberID == "" {
		return ErrGroupMemberNotFound
	}

	res, err := s.db.ExecContext(ctx, `
		DELETE FROM group_members
		WHERE family_group_id = $1 AND id = $2
	`, groupID, memberID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrGroupMemberNotFound
	}
	return nil
}

func (s PostgresqlStore) ListGroupMembers(ctx context.Context, groupID string) ([]*GroupMember, error) {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return nil, ErrFamilyGroupNotFound
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, family_group_id, temp_label, trakt_username,
		       access_token, refresh_token, token_expiry, authorization_status, created_at
		FROM group_members
		WHERE family_group_id = $1
		ORDER BY created_at ASC
	`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []*GroupMember
	for rows.Next() {
		gm, err := scanGroupMemberRow(rows)
		if err != nil {
			return nil, err
		}
		members = append(members, gm)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return members, nil
}

func (s PostgresqlStore) GetGroupMemberByTrakt(ctx context.Context, groupID, traktUsername string) (*GroupMember, error) {
	groupID = strings.TrimSpace(groupID)
	traktUsername = strings.TrimSpace(strings.ToLower(traktUsername))
	if groupID == "" || traktUsername == "" {
		return nil, ErrGroupMemberNotFound
	}

	row := s.db.QueryRowContext(ctx, `
		SELECT id, family_group_id, temp_label, trakt_username,
		       access_token, refresh_token, token_expiry, authorization_status, created_at
		FROM group_members
		WHERE family_group_id = $1 AND trakt_username = $2
	`, groupID, traktUsername)

	gm, err := scanGroupMemberRow(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrGroupMemberNotFound
		}
		return nil, err
	}
	return gm, nil
}

func (s PostgresqlStore) EnqueueRetryItem(ctx context.Context, item *RetryQueueItem) error {
	if item == nil {
		return ErrInvalidRetryItem
	}
	if item.ID == "" {
		item.ID = uuid()
	}
	if item.Status == "" {
		item.Status = RetryQueueStatusQueued
	}
	if err := item.Validate(); err != nil {
		return err
	}

	err := s.db.QueryRowContext(ctx, `
		INSERT INTO retry_queue_items (
			id, family_group_id, group_member_id, payload,
			attempt_count, next_attempt_at, last_error, status
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING created_at, updated_at
	`,
		item.ID,
		item.FamilyGroupID,
		item.GroupMemberID,
		[]byte(item.Payload),
		item.AttemptCount,
		item.NextAttemptAt,
		nullableString(item.LastError),
		item.Status,
	).Scan(&item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		if pqErr, ok := err.(*pq.Error); ok && pqErr.Code == "23503" {
			return ErrGroupMemberNotFound
		}
		return err
	}
	return nil
}

func (s PostgresqlStore) ListDueRetryItems(ctx context.Context, now time.Time, limit int) ([]*RetryQueueItem, error) {
	if limit <= 0 {
		limit = 50
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT id, family_group_id, group_member_id, payload,
		       attempt_count, next_attempt_at, last_error, status,
		       created_at, updated_at
		FROM retry_queue_items
		WHERE status IN ($1, $2)
		  AND next_attempt_at <= $3
		ORDER BY next_attempt_at ASC
		LIMIT $4
		FOR UPDATE SKIP LOCKED
	`, RetryQueueStatusQueued, RetryQueueStatusRetrying, now, limit)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	defer rows.Close()

	var (
		items []*RetryQueueItem
		ids   []string
	)

	for rows.Next() {
		var (
			payload []byte
			lastErr sql.NullString
			item    RetryQueueItem
		)
		if err := rows.Scan(
			&item.ID,
			&item.FamilyGroupID,
			&item.GroupMemberID,
			&payload,
			&item.AttemptCount,
			&item.NextAttemptAt,
			&lastErr,
			&item.Status,
			&item.CreatedAt,
			&item.UpdatedAt,
		); err != nil {
			tx.Rollback()
			return nil, err
		}
		item.Payload = json.RawMessage(payload)
		if lastErr.Valid {
			item.LastError = lastErr.String
		}
		items = append(items, &item)
		ids = append(ids, item.ID)
	}
	if err := rows.Err(); err != nil {
		tx.Rollback()
		return nil, err
	}

	if len(ids) > 0 {
		if _, err := tx.ExecContext(ctx,
			`UPDATE retry_queue_items SET status = $1, updated_at = NOW() WHERE id = ANY($2)`,
			RetryQueueStatusRetrying,
			pq.Array(ids),
		); err != nil {
			tx.Rollback()
			return nil, err
		}
		updateTime := time.Now().UTC()
		for _, item := range items {
			item.Status = RetryQueueStatusRetrying
			item.UpdatedAt = updateTime
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return items, nil
}

func (s PostgresqlStore) MarkRetrySuccess(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrRetryItemNotFound
	}

	res, err := s.db.ExecContext(ctx, `DELETE FROM retry_queue_items WHERE id = $1`, id)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrRetryItemNotFound
	}
	return nil
}

func (s PostgresqlStore) MarkRetryFailure(ctx context.Context, id string, attempt int, nextAttempt time.Time, lastErr string, permanent bool) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrRetryItemNotFound
	}

	status := RetryQueueStatusQueued
	if permanent {
		status = RetryQueueStatusPermanentFailure
		attempt = MaxRetryAttempts
	}

	res, err := s.db.ExecContext(ctx, `
		UPDATE retry_queue_items
		SET attempt_count = $2,
			next_attempt_at = $3,
			last_error = $4,
			status = $5,
			updated_at = NOW()
		WHERE id = $1
	`,
		id,
		attempt,
		nextAttempt,
		nullableString(lastErr),
		status,
	)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrRetryItemNotFound
	}
	return nil
}
