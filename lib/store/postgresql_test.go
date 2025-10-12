package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
)

func TestPostgresqlLoadingUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("an error '%s' was not expected when opening a stub database connection", err)
	}
	defer db.Close()

	tokenExpiry := time.Date(2019, 05, 25, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(
		"SELECT username, access, refresh, trakt_display_name, updated, token_expiry FROM users WHERE id=.*",
	).WithArgs(
		"id123",
	).WillReturnRows(
		sqlmock.NewRows([]string{"username", "access", "refresh", "trakt_display_name", "updated", "token_expiry"}).
			AddRow(
				"halkeye",
				"access123",
				"refresh123",
				"Halkeye",
				time.Date(2019, 02, 25, 0, 0, 0, 0, time.UTC),
				tokenExpiry,
			),
	)

	store := NewPostgresqlStore(db)

	expected, _ := json.Marshal(&User{
		ID:               "id123",
		Username:         "halkeye",
		AccessToken:      "access123",
		RefreshToken:     "refresh123",
		TraktDisplayName: "Halkeye",
		Updated:          time.Date(2019, 02, 25, 0, 0, 0, 0, time.UTC),
		TokenExpiry:      tokenExpiry,
	})
	actual, _ := json.Marshal(store.GetUser("id123"))

	assert.EqualValues(t, string(expected), string(actual))
}

func TestPostgresqlSavingUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("an error '%s' was not expected when opening a stub database connection", err)
	}
	defer db.Close()

	tokenExpiry := time.Date(2019, 05, 25, 0, 0, 0, 0, time.UTC)
	mock.ExpectExec("INSERT INTO ").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT").WithArgs("id123").WillReturnRows(
		sqlmock.NewRows([]string{"username", "access", "refresh", "trakt_display_name", "updated", "token_expiry"}).
			AddRow(
				"halkeye",
				"access123",
				"refresh123",
				"Halkeye",
				time.Date(2019, 02, 25, 0, 0, 0, 0, time.UTC),
				tokenExpiry,
			),
	)

	store := NewPostgresqlStore(db)
	originalUser := &User{
		ID:               "id123",
		Username:         "halkeye",
		AccessToken:      "access123",
		RefreshToken:     "refresh123",
		TraktDisplayName: "Halkeye",
		Updated:          time.Date(2019, 02, 25, 0, 0, 0, 0, time.UTC),
		TokenExpiry:      tokenExpiry,
		store:            store,
	}

	originalUser.save()

	expected, err := json.Marshal(originalUser)
	actual, err := json.Marshal(store.GetUser("id123"))

	assert.EqualValues(t, string(expected), string(actual))
}

func TestPostgresqlListUsers(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("an error '%s' was not expected when opening a stub database connection", err)
	}
	defer db.Close()

	tokenExpiry1 := time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC)
	tokenExpiry2 := time.Date(2020, 5, 1, 0, 0, 0, 0, time.UTC)
	rows := sqlmock.NewRows([]string{"id", "username", "access", "refresh", "trakt_display_name", "updated", "token_expiry"}).
		AddRow("newest", "Alice", "access-new", "refresh-new", "Alice Smith", time.Date(2020, 3, 1, 0, 0, 0, 0, time.UTC), tokenExpiry1).
		AddRow("older", "Bob", "access-old", "refresh-old", nil, time.Date(2020, 2, 1, 0, 0, 0, 0, time.UTC), tokenExpiry2)

	mock.ExpectQuery("SELECT id, username, access, refresh, trakt_display_name, updated, token_expiry FROM users ORDER BY updated DESC").
		WillReturnRows(rows)

	store := NewPostgresqlStore(db)
	users := store.ListUsers()

	assert.Len(t, users, 2)
	assert.Equal(t, "newest", users[0].ID)
	assert.Equal(t, "alice", users[0].Username)
	assert.Equal(t, "Alice Smith", users[0].TraktDisplayName)
	assert.Equal(t, "older", users[1].ID)
	assert.Equal(t, "bob", users[1].Username)
	assert.Equal(t, "", users[1].TraktDisplayName)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("there were unfulfilled expectations: %s", err)
	}
}

func TestPostgresqlStoreCreateFamilyGroup(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now()
	mock.ExpectQuery("INSERT INTO family_groups").
		WithArgs(sqlmock.AnyArg(), "plexuser").
		WillReturnRows(sqlmock.NewRows([]string{"created_at", "updated_at"}).AddRow(now, now))

	store := NewPostgresqlStore(db)
	group := &FamilyGroup{PlexUsername: "PlexUser"}
	err = store.CreateFamilyGroup(context.Background(), group)
	assert.NoError(t, err)
	assert.Equal(t, "plexuser", group.PlexUsername)
	assert.NotEmpty(t, group.ID)
	assert.WithinDuration(t, now, group.CreatedAt, time.Second)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}

	dupErr := &pq.Error{Code: "23505"}
	mock.ExpectQuery("INSERT INTO family_groups").
		WithArgs(sqlmock.AnyArg(), "plexuser").
		WillReturnError(dupErr)

	err = store.CreateFamilyGroup(context.Background(), &FamilyGroup{PlexUsername: "plexuser"})
	assert.ErrorIs(t, err, ErrDuplicateFamilyGroup)
}

func TestPostgresqlStoreGetFamilyGroup(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	created := time.Now().Add(-time.Hour)
	updated := time.Now()
	mock.ExpectQuery(`SELECT id, plex_username, created_at, updated_at FROM family_groups WHERE id = \$1`).
		WithArgs("group-id").
		WillReturnRows(sqlmock.NewRows([]string{"id", "plex_username", "created_at", "updated_at"}).
			AddRow("group-id", "plexuser", created, updated))

	store := NewPostgresqlStore(db)
	fg, err := store.GetFamilyGroup(context.Background(), "group-id")
	assert.NoError(t, err)
	assert.Equal(t, "group-id", fg.ID)
	assert.Equal(t, "plexuser", fg.PlexUsername)

	mock.ExpectQuery(`SELECT id, plex_username, created_at, updated_at FROM family_groups WHERE id = \$1`).
		WithArgs("missing").
		WillReturnError(sql.ErrNoRows)

	fg, err = store.GetFamilyGroup(context.Background(), "missing")
	assert.Nil(t, fg)
	assert.ErrorIs(t, err, ErrFamilyGroupNotFound)
}

func TestPostgresqlStoreAddGroupMember(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now()
	mock.ExpectQuery("INSERT INTO group_members").
		WithArgs(sqlmock.AnyArg(), "group-id", "Dad", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), GroupMemberStatusPending).
		WillReturnRows(sqlmock.NewRows([]string{"created_at"}).AddRow(now))

	store := NewPostgresqlStore(db)
	member := &GroupMember{FamilyGroupID: "group-id", TempLabel: "Dad"}
	err = store.AddGroupMember(context.Background(), member)
	assert.NoError(t, err)
	assert.NotEmpty(t, member.ID)
	assert.Equal(t, GroupMemberStatusPending, member.AuthorizationStatus)

	mock.ExpectQuery("INSERT INTO group_members").
		WithArgs(sqlmock.AnyArg(), "group-id", "Dad", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), GroupMemberStatusPending).
		WillReturnError(&pq.Error{Code: "23505"})

	err = store.AddGroupMember(context.Background(), &GroupMember{FamilyGroupID: "group-id", TempLabel: "Dad", TraktUsername: "existing"})
	assert.ErrorIs(t, err, ErrDuplicateGroupMember)
}

func TestPostgresqlStoreEnqueueRetryItem(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now()
	mock.ExpectQuery("INSERT INTO retry_queue_items").
		WithArgs(sqlmock.AnyArg(), "group-id", "member-id", sqlmock.AnyArg(), 0, now, sqlmock.AnyArg(), RetryQueueStatusQueued).
		WillReturnRows(sqlmock.NewRows([]string{"created_at", "updated_at"}).AddRow(now, now))

	store := NewPostgresqlStore(db)
	item := &RetryQueueItem{
		FamilyGroupID: "group-id",
		GroupMemberID: "member-id",
		Payload:       json.RawMessage(`{"foo":"bar"}`),
		NextAttemptAt: now,
	}
	err = store.EnqueueRetryItem(context.Background(), item)
	assert.NoError(t, err)
	assert.NotEmpty(t, item.ID)
}

func TestPostgresqlStoreListDueRetryItems(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now()
	rows := sqlmock.NewRows([]string{
		"id", "family_group_id", "group_member_id", "payload", "attempt_count", "next_attempt_at",
		"last_error", "status", "created_at", "updated_at",
	}).AddRow(
		"retry-1", "group-id", "member-id", []byte(`{"foo":"bar"}`),
		1, now.Add(-time.Minute), sql.NullString{String: "timeout", Valid: true}, RetryQueueStatusQueued, now.Add(-2*time.Minute), now.Add(-time.Minute),
	)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, family_group_id, group_member_id, payload").
		WithArgs(RetryQueueStatusQueued, RetryQueueStatusRetrying, now, 10).
		WillReturnRows(rows)
	mock.ExpectExec(`UPDATE retry_queue_items SET status = \$1, updated_at = NOW\(\) WHERE id = ANY\(\$2\)`).
		WithArgs(RetryQueueStatusRetrying, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	store := NewPostgresqlStore(db)
	items, err := store.ListDueRetryItems(context.Background(), now, 10)
	assert.NoError(t, err)
	assert.Len(t, items, 1)
	assert.Equal(t, "retry-1", items[0].ID)
	assert.Equal(t, RetryQueueStatusRetrying, items[0].Status)
	assert.Equal(t, "timeout", items[0].LastError)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresqlStoreMarkRetrySuccessAndFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectExec(`DELETE FROM retry_queue_items WHERE id = \$1`).
		WithArgs("retry-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	store := NewPostgresqlStore(db)
	assert.NoError(t, store.MarkRetrySuccess(context.Background(), "retry-1"))

	mock.ExpectExec(`DELETE FROM retry_queue_items WHERE id = \$1`).
		WithArgs("missing").
		WillReturnResult(sqlmock.NewResult(0, 0))
	assert.ErrorIs(t, store.MarkRetrySuccess(context.Background(), "missing"), ErrRetryItemNotFound)

	next := time.Now().Add(time.Minute)
	mock.ExpectExec(`UPDATE retry_queue_items\s+SET attempt_count`).
		WithArgs("retry-2", 2, next, sqlmock.AnyArg(), RetryQueueStatusQueued).
		WillReturnResult(sqlmock.NewResult(0, 1))
	assert.NoError(t, store.MarkRetryFailure(context.Background(), "retry-2", 2, next, "429", false))

	mock.ExpectExec(`UPDATE retry_queue_items\s+SET attempt_count`).
		WithArgs("retry-missing", MaxRetryAttempts, sqlmock.AnyArg(), sqlmock.AnyArg(), RetryQueueStatusPermanentFailure).
		WillReturnResult(sqlmock.NewResult(0, 0))
	assert.ErrorIs(t, store.MarkRetryFailure(context.Background(), "retry-missing", MaxRetryAttempts, next, "fail", true), ErrRetryItemNotFound)
}
