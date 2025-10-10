package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
)

func TestPostgresqlLoadingUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("an error '%s' was not expected when opening a stub database connection", err)
	}
	defer db.Close()

	mock.ExpectQuery(
		"SELECT username, access, refresh, trakt_display_name, updated FROM users WHERE id=.*",
	).WithArgs(
		"id123",
	).WillReturnRows(
		sqlmock.NewRows([]string{"username", "access", "refresh", "trakt_display_name", "updated"}).
			AddRow(
				"halkeye",
				"access123",
				"refresh123",
				"Halkeye",
				time.Date(2019, 02, 25, 0, 0, 0, 0, time.UTC),
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

	mock.ExpectExec("INSERT INTO ").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT").WithArgs("id123").WillReturnRows(
		sqlmock.NewRows([]string{"username", "access", "refresh", "trakt_display_name", "updated"}).
			AddRow(
				"halkeye",
				"access123",
				"refresh123",
				"Halkeye",
				time.Date(2019, 02, 25, 0, 0, 0, 0, time.UTC),
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

	rows := sqlmock.NewRows([]string{"id", "username", "access", "refresh", "trakt_display_name", "updated"}).
		AddRow("newest", "Alice", "access-new", "refresh-new", "Alice Smith", time.Date(2020, 3, 1, 0, 0, 0, 0, time.UTC)).
		AddRow("older", "Bob", "access-old", "refresh-old", nil, time.Date(2020, 2, 1, 0, 0, 0, 0, time.UTC))

	mock.ExpectQuery("SELECT id, username, access, refresh, trakt_display_name, updated FROM users ORDER BY updated DESC").
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
