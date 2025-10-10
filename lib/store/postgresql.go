package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"database/sql"

	// Postgres db library loading
	"crovlune/plaxt/lib/common"
	_ "github.com/lib/pq"
)

// PostgresqlStore is a storage engine that writes to postgres
type PostgresqlStore struct {
	db *sql.DB
}

// NewPostgresqlClient creates a new db client object
func NewPostgresqlClient(connStr string) *sql.DB {
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		panic(err)
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			id varchar(255) NOT NULL,
			username varchar(255) NOT NULL,
			access varchar(255) NOT NULL,
			refresh varchar(255) NOT NULL,
			trakt_display_name varchar(50),
			updated timestamp with time zone NOT NULL,
			PRIMARY KEY(id)
		)
	`); err != nil {
		panic(err)
	}
	if _, err := db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS trakt_display_name varchar(50)`); err != nil {
		panic(err)
	}
	return db
}

// NewPostgresqlStore creates new store
func NewPostgresqlStore(db *sql.DB) PostgresqlStore {
	return PostgresqlStore{
		db: db,
	}
}

// Ping will check if the connection works right
func (s PostgresqlStore) Ping(ctx context.Context) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	return conn.PingContext(ctx)
}

// WriteUser will write a user object to postgres
func (s PostgresqlStore) WriteUser(user User) {
	_, err := s.db.Exec(
		`
			INSERT INTO users
				(id, username, access, refresh, trakt_display_name, updated)
				VALUES($1, $2, $3, $4, $5, $6)
			ON CONFLICT(id)
			DO UPDATE set username=EXCLUDED.username, access=EXCLUDED.access, refresh=EXCLUDED.refresh, trakt_display_name=EXCLUDED.trakt_display_name, updated=EXCLUDED.updated
		`,
		user.ID,
		user.Username,
		user.AccessToken,
		user.RefreshToken,
		user.TraktDisplayName,
		user.Updated,
	)
	if err != nil {
		panic(err)
	}
}

// GetUser will load a user from postgres
func (s PostgresqlStore) GetUser(id string) *User {
	var username string
	var access string
	var refresh string
	var updated time.Time
	var displayName sql.NullString

	err := s.db.QueryRow(
		"SELECT username, access, refresh, trakt_display_name, updated FROM users WHERE id=$1",
		id,
	).Scan(
		&username,
		&access,
		&refresh,
		&displayName,
		&updated,
	)
	switch {
	case err == sql.ErrNoRows:
		panic(fmt.Errorf("no user with id %s", id))
	case err != nil:
		panic(fmt.Errorf("query error: %v", err))
	}
	user := User{
		ID:               id,
		Username:         strings.ToLower(username),
		AccessToken:      access,
		RefreshToken:     refresh,
		TraktDisplayName: displayName.String,
		Updated:          updated,
		store:            s,
	}

	return &user
}

// GetUserByName will load a user from postgres
func (s PostgresqlStore) GetUserByName(username string) *User {
	return nil
}

// TODO: Not Implemented
func (s PostgresqlStore) DeleteUser(id, username string) bool {
	return true
}

func (s PostgresqlStore) ListUsers() []User {
	rows, err := s.db.Query(`SELECT id, username, access, refresh, trakt_display_name, updated FROM users ORDER BY updated DESC`)
	if err != nil {
		panic(err)
	}
	defer rows.Close()

	users := []User{}
	for rows.Next() {
		var (
			id       string
			username string
			access   string
			refresh  string
			display  sql.NullString
			updated  time.Time
		)
		if err := rows.Scan(&id, &username, &access, &refresh, &display, &updated); err != nil {
			panic(err)
		}
		user := User{
			ID:               id,
			Username:         strings.ToLower(username),
			AccessToken:      access,
			RefreshToken:     refresh,
			TraktDisplayName: display.String,
			Updated:          updated,
			store:            s,
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		panic(err)
	}
	return users
}

func (s PostgresqlStore) GetScrobbleBody(playerUuid, ratingKey string) common.CacheItem {
	return common.CacheItem{
		Body: common.ScrobbleBody{
			Progress: 0,
		},
	}
}

func (s PostgresqlStore) WriteScrobbleBody(item common.CacheItem) {
}
