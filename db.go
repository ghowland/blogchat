package main

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound reports a missing row. Handlers turn this into status 404.
var ErrNotFound = errors.New("not found")

// boolInt converts a Go boolean into the integer form that SQLite stores,
// because SQLite has no boolean type.
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// migrations holds the schema history. Never edit an entry after a release.
// Add a new entry for each change. PRAGMA user_version holds the position.
var migrations = []string{
	// 0 -> 1: the complete initial schema.
	`
	CREATE TABLE users (
		id          INTEGER PRIMARY KEY,
		email       TEXT    NOT NULL UNIQUE,
		handle      TEXT    NOT NULL UNIQUE,
		invited_by  INTEGER REFERENCES users(id),
		created_at  INTEGER NOT NULL,
		last_login  INTEGER,
		enabled     INTEGER NOT NULL DEFAULT 1
	);

	CREATE TABLE login_tokens (
		id          INTEGER PRIMARY KEY,
		user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		token_hash  BLOB    NOT NULL UNIQUE,
		created_at  INTEGER NOT NULL,
		expires_at  INTEGER NOT NULL,
		used_at     INTEGER
	);

	CREATE TABLE sessions (
		id          INTEGER PRIMARY KEY,
		user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		token_hash  BLOB    NOT NULL UNIQUE,
		created_at  INTEGER NOT NULL,
		last_seen   INTEGER NOT NULL,
		expires_at  INTEGER NOT NULL,
		ip          TEXT    NOT NULL,
		agent       TEXT    NOT NULL
	);

	CREATE TABLE posts (
		id          INTEGER PRIMARY KEY,
		user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		subject     TEXT    NOT NULL,
		body        TEXT    NOT NULL,
		created_at  INTEGER NOT NULL,
		updated_at  INTEGER NOT NULL,
		is_chat     INTEGER NOT NULL DEFAULT 0,
		last_at     INTEGER NOT NULL DEFAULT 0
	);

	CREATE TABLE replies (
		id          INTEGER PRIMARY KEY,
		post_id     INTEGER NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
		user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		body        TEXT    NOT NULL,
		created_at  INTEGER NOT NULL
	);

	CREATE INDEX idx_sessions_user  ON sessions(user_id);
	CREATE INDEX idx_tokens_user    ON login_tokens(user_id);
	CREATE INDEX idx_replies_post   ON replies(post_id, id);
	CREATE INDEX idx_posts_kind_time ON posts(is_chat, created_at DESC);
	CREATE INDEX idx_posts_kind_last ON posts(is_chat, last_at DESC);
	CREATE INDEX idx_users_inviter  ON users(invited_by);
	`,
}

// OpenDB opens or creates the database file and applies all migrations.
// The connection limit is one, because the program is a single process and
// SQLite permits one writer. This removes every SQLITE_BUSY condition.
func OpenDB(path string) (*sql.DB, error) {
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)"

	dbh, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	dbh.SetMaxOpenConns(1)
	dbh.SetMaxIdleConns(1)
	dbh.SetConnMaxLifetime(0)

	if err := dbh.Ping(); err != nil {
		dbh.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	if err := Migrate(dbh); err != nil {
		dbh.Close()
		return nil, err
	}
	return dbh, nil
}

// Migrate applies each pending migration in its own transaction.
func Migrate(dbh *sql.DB) error {
	var version int
	if err := dbh.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	for version < len(migrations) {
		txn, err := dbh.Begin()
		if err != nil {
			return err
		}
		if _, err := txn.Exec(migrations[version]); err != nil {
			txn.Rollback()
			return fmt.Errorf("migration %d: %w", version, err)
		}
		version++
		// PRAGMA does not accept a parameter, so the value comes from the
		// loop counter only. No external input reaches this statement.
		if _, err := txn.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
			txn.Rollback()
			return fmt.Errorf("set user_version %d: %w", version, err)
		}
		if err := txn.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Checkpoint writes the write-ahead log back into the main file and empties
// the log file. Run this at shutdown.
func Checkpoint(dbh *sql.DB) error {
	_, err := dbh.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

// ---------- users ----------

const userColumns = `
	usr.id, usr.email, usr.handle, usr.invited_by, usr.created_at, usr.enabled,
	COALESCE(inv.handle, '')`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	usr := &User{}
	err := row.Scan(&usr.ID, &usr.Email, &usr.Handle, &usr.InvitedBy,
		&usr.CreatedAt, &usr.Enabled, &usr.InviterHandle)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return usr, nil
}

// FindUserByEmail returns the member with the given address.
func (app *App) FindUserByEmail(email string) (*User, error) {
	row := app.dbh.QueryRow(`
		SELECT `+userColumns+`
		FROM users usr LEFT JOIN users inv ON inv.id = usr.invited_by
		WHERE usr.email = ?`, email)
	return scanUser(row)
}

// FindUserByID returns the member with the given row identifier.
func (app *App) FindUserByID(uid int64) (*User, error) {
	row := app.dbh.QueryRow(`
		SELECT `+userColumns+`
		FROM users usr LEFT JOIN users inv ON inv.id = usr.invited_by
		WHERE usr.id = ?`, uid)
	return scanUser(row)
}

// CountUsers returns the number of member rows.
func (app *App) CountUsers() (int64, error) {
	var total int64
	err := app.dbh.QueryRow("SELECT COUNT(*) FROM users").Scan(&total)
	return total, err
}

// CreateUser adds a member. This is the whole of the invite operation,
// because the platform has no registration page. Pass a zero inviter for
// the root member.
func (app *App) CreateUser(email, handle string, inviter int64) (int64, error) {
	var parent any
	if inviter > 0 {
		parent = inviter
	}
	result, err := app.dbh.Exec(`
		INSERT INTO users (email, handle, invited_by, created_at, enabled)
		VALUES (?, ?, ?, ?, 1)`,
		email, handle, parent, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// CountOpenInvites returns the number of members that this member invited
// and that never completed a login. The invite quota uses this number.
func (app *App) CountOpenInvites(uid int64) (int, error) {
	var total int
	err := app.dbh.QueryRow(`
		SELECT COUNT(*) FROM users
		WHERE invited_by = ? AND last_login IS NULL`, uid).Scan(&total)
	return total, err
}

// ---------- login tokens ----------

// CreateLoginToken stores the hash of a one-time login token.
func (app *App) CreateLoginToken(uid int64, sum []byte, life time.Duration) error {
	now := time.Now().Unix()
	_, err := app.dbh.Exec(`
		INSERT INTO login_tokens (user_id, token_hash, created_at, expires_at)
		VALUES (?, ?, ?, ?)`,
		uid, sum, now, now+int64(life.Seconds()))
	return err
}

// ConsumeLoginToken marks a token as used and returns the owner. The update
// carries the used_at and expires_at conditions, so two parallel requests
// cannot both succeed with the same token.
func (app *App) ConsumeLoginToken(sum []byte) (int64, error) {
	now := time.Now().Unix()
	txn, err := app.dbh.Begin()
	if err != nil {
		return 0, err
	}
	defer txn.Rollback()

	result, err := txn.Exec(`
		UPDATE login_tokens SET used_at = ?
		WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?`,
		now, sum, now)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, ErrNotFound
	}

	var uid int64
	var enabled bool
	err = txn.QueryRow(`
		SELECT usr.id, usr.enabled FROM login_tokens tok
		JOIN users usr ON usr.id = tok.user_id
		WHERE tok.token_hash = ?`, sum).Scan(&uid, &enabled)
	if err != nil {
		return 0, err
	}
	if !enabled {
		return 0, ErrNotFound
	}
	if _, err := txn.Exec(`UPDATE users SET last_login = ? WHERE id = ?`, now, uid); err != nil {
		return 0, err
	}
	return uid, txn.Commit()
}

// ---------- sessions ----------

// CreateSession stores a new login key and returns its row identifier.
func (app *App) CreateSession(uid int64, sum []byte, addr, agent string, life time.Duration) (int64, error) {
	now := time.Now().Unix()
	if len(agent) > 120 {
		agent = agent[:120]
	}
	result, err := app.dbh.Exec(`
		INSERT INTO sessions
			(user_id, token_hash, created_at, last_seen, expires_at, ip, agent)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		uid, sum, now, now, now+int64(life.Seconds()), addr, agent)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// SessionByHash returns the member and the session row identifier for a
// session token hash. A disabled member gets no session.
func (app *App) SessionByHash(sum []byte) (*User, int64, error) {
	usr := &User{}
	var sid int64
	err := app.dbh.QueryRow(`
		SELECT ses.id, `+userColumns+`
		FROM sessions ses
		JOIN users usr ON usr.id = ses.user_id
		LEFT JOIN users inv ON inv.id = usr.invited_by
		WHERE ses.token_hash = ? AND ses.expires_at > ? AND usr.enabled = 1`,
		sum, time.Now().Unix()).
		Scan(&sid, &usr.ID, &usr.Email, &usr.Handle, &usr.InvitedBy,
			&usr.CreatedAt, &usr.Enabled, &usr.InviterHandle)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, ErrNotFound
	}
	if err != nil {
		return nil, 0, err
	}
	return usr, sid, nil
}

// ListSessions returns all active keys of one member, newest first.
func (app *App) ListSessions(uid, current int64) ([]Session, error) {
	rows, err := app.dbh.Query(`
		SELECT id, user_id, created_at, last_seen, ip, agent
		FROM sessions WHERE user_id = ? AND expires_at > ?
		ORDER BY created_at DESC`, uid, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]Session, 0, 8)
	for rows.Next() {
		var ses Session
		if err := rows.Scan(&ses.ID, &ses.UserID, &ses.CreatedAt,
			&ses.LastSeen, &ses.IP, &ses.Agent); err != nil {
			return nil, err
		}
		ses.Current = ses.ID == current
		list = append(list, ses)
	}
	return list, rows.Err()
}

// DeleteOtherSessions removes every key of the member except the given one.
// This is the "log out all other keys" operation.
func (app *App) DeleteOtherSessions(uid, keep int64) (int64, error) {
	result, err := app.dbh.Exec(
		`DELETE FROM sessions WHERE user_id = ? AND id <> ?`, uid, keep)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// DeleteSession removes one key. The user identifier prevents the removal
// of a key that belongs to a different member.
func (app *App) DeleteSession(uid, sid int64) error {
	_, err := app.dbh.Exec(
		`DELETE FROM sessions WHERE id = ? AND user_id = ?`, sid, uid)
	return err
}

// TouchSession updates the last-seen time. SeenCache limits how often this
// runs, because a write on each request is not necessary.
func (app *App) TouchSession(sid int64) error {
	_, err := app.dbh.Exec(
		`UPDATE sessions SET last_seen = ? WHERE id = ?`, time.Now().Unix(), sid)
	return err
}

// PurgeExpired removes old sessions and old login tokens. Run this each hour.
func (app *App) PurgeExpired() error {
	now := time.Now().Unix()
	if _, err := app.dbh.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, now); err != nil {
		return err
	}
	_, err := app.dbh.Exec(`DELETE FROM login_tokens WHERE expires_at <= ?`, now-86400)
	return err
}

// ---------- posts ----------

// CreatePost adds a thread or a channel. The member becomes the owner.
// For a channel, the body holds the topic line and can be empty.
func (app *App) CreatePost(uid int64, subject, body string, chat bool) (int64, error) {
	now := time.Now().Unix()
	result, err := app.dbh.Exec(`
		INSERT INTO posts (user_id, subject, body, created_at, updated_at, is_chat, last_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		uid, subject, body, now, now, boolInt(chat), now)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// ListPosts returns one page of the blog feed or of the channel list.
// The blog feed sorts on the creation time. The channel list sorts on the
// time of the newest message, so that an active channel goes to the top.
func (app *App) ListPosts(chat bool, limit, offset int) ([]FeedRow, error) {
	// The order clause comes from a local constant only. No external input
	// reaches this concatenation. Every other value stays a parameter.
	order := "pst.created_at DESC"
	if chat {
		order = "pst.last_at DESC"
	}
	rows, err := app.dbh.Query(`
		SELECT pst.id, pst.subject, usr.handle, pst.created_at, pst.last_at,
			(SELECT COUNT(*) FROM replies WHERE replies.post_id = pst.id)
		FROM posts pst JOIN users usr ON usr.id = pst.user_id
		WHERE pst.is_chat = ?
		ORDER BY `+order+` LIMIT ? OFFSET ?`, boolInt(chat), limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]FeedRow, 0, limit)
	for rows.Next() {
		var row FeedRow
		if err := rows.Scan(&row.ID, &row.Subject, &row.Handle,
			&row.CreatedAt, &row.LastAt, &row.Replies); err != nil {
			return nil, err
		}
		row.IsChat = chat
		list = append(list, row)
	}
	return list, rows.Err()
}

// CountPosts returns the number of threads or of channels, for the page links.
func (app *App) CountPosts(chat bool) (int, error) {
	var total int
	err := app.dbh.QueryRow(
		"SELECT COUNT(*) FROM posts WHERE is_chat = ?", boolInt(chat)).Scan(&total)
	return total, err
}

// GetPost returns one thread or one channel with the handle of the owner.
// The query does not filter on the kind, because the handler must know the
// kind to select the correct page.
func (app *App) GetPost(pid int64) (*Post, error) {
	pst := &Post{}
	err := app.dbh.QueryRow(`
		SELECT pst.id, pst.user_id, usr.handle, pst.subject, pst.body,
			pst.created_at, pst.updated_at, pst.is_chat, pst.last_at
		FROM posts pst JOIN users usr ON usr.id = pst.user_id
		WHERE pst.id = ?`, pid).
		Scan(&pst.ID, &pst.UserID, &pst.Handle, &pst.Subject, &pst.Body,
			&pst.CreatedAt, &pst.UpdatedAt, &pst.IsChat, &pst.LastAt)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return pst, nil
}

// DeletePost removes a thread and, through the foreign key, its replies.
// Only the owner can do this.
func (app *App) DeletePost(pid, uid int64) error {
	result, err := app.dbh.Exec(
		`DELETE FROM posts WHERE id = ? AND user_id = ?`, pid, uid)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------- replies ----------

// CreateReply adds a comment to a thread.
func (app *App) CreateReply(pid, uid int64, body string) (int64, error) {
	result, err := app.dbh.Exec(`
		INSERT INTO replies (post_id, user_id, body, created_at)
		VALUES (?, ?, ?, ?)`, pid, uid, body, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// ListReplies returns every comment of a thread, oldest first.
func (app *App) ListReplies(pid int64) ([]Reply, error) {
	rows, err := app.dbh.Query(`
		SELECT rep.id, rep.post_id, rep.user_id, usr.handle, rep.body, rep.created_at
		FROM replies rep JOIN users usr ON usr.id = rep.user_id
		WHERE rep.post_id = ? ORDER BY rep.id ASC`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]Reply, 0, 16)
	for rows.Next() {
		var rep Reply
		if err := rows.Scan(&rep.ID, &rep.PostID, &rep.UserID, &rep.Handle,
			&rep.Body, &rep.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, rep)
	}
	return list, rows.Err()
}

// ListChatLines returns the newest messages of a channel. The result is in
// the old-to-new order, so that the page reads from top to bottom.
func (app *App) ListChatLines(pid int64, limit int) ([]Reply, error) {
	rows, err := app.dbh.Query(`
		SELECT rep.id, rep.post_id, rep.user_id, usr.handle, rep.body, rep.created_at
		FROM (
			SELECT * FROM replies WHERE post_id = ? ORDER BY id DESC LIMIT ?
		) rep JOIN users usr ON usr.id = rep.user_id
		ORDER BY rep.id ASC`, pid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]Reply, 0, limit)
	for rows.Next() {
		var rep Reply
		if err := rows.Scan(&rep.ID, &rep.PostID, &rep.UserID, &rep.Handle,
			&rep.Body, &rep.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, rep)
	}
	return list, rows.Err()
}

// ListChatLinesAfter returns the messages of a channel above a row
// identifier. The poll on the chat page uses this to fetch new messages
// only. The index idx_replies_post serves the query, so a poll that finds
// nothing costs one index seek.
func (app *App) ListChatLinesAfter(pid, after int64, limit int) ([]Reply, error) {
	rows, err := app.dbh.Query(`
		SELECT rep.id, rep.post_id, rep.user_id, usr.handle, rep.body, rep.created_at
		FROM replies rep JOIN users usr ON usr.id = rep.user_id
		WHERE rep.post_id = ? AND rep.id > ?
		ORDER BY rep.id ASC LIMIT ?`, pid, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]Reply, 0, 16)
	for rows.Next() {
		var rep Reply
		if err := rows.Scan(&rep.ID, &rep.PostID, &rep.UserID, &rep.Handle,
			&rep.Body, &rep.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, rep)
	}
	return list, rows.Err()
}

// CreateChatLine adds one message to a channel, moves the channel to the top
// of the list, and removes the oldest messages above the keep limit. The
// three operations are in one transaction, so that the count cannot drift.
func (app *App) CreateChatLine(pid, uid int64, body string, keep int) (int64, error) {
	now := time.Now().Unix()

	txn, err := app.dbh.Begin()
	if err != nil {
		return 0, err
	}
	defer txn.Rollback()

	result, err := txn.Exec(`
		INSERT INTO replies (post_id, user_id, body, created_at)
		VALUES (?, ?, ?, ?)`, pid, uid, body, now)
	if err != nil {
		return 0, err
	}
	rid, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}

	if _, err := txn.Exec(
		`UPDATE posts SET last_at = ? WHERE id = ?`, now, pid); err != nil {
		return 0, err
	}

	// The inner select finds the row at position keep, counted from the
	// newest. The delete removes that row and every older row, so exactly
	// keep rows stay. When the channel holds fewer rows, the inner select
	// returns nothing, the comparison is null, and nothing is deleted.
	// The trim uses the row identifier and not the time, because two
	// messages can share one second.
	if _, err := txn.Exec(`
		DELETE FROM replies WHERE post_id = ? AND id <= (
			SELECT id FROM replies WHERE post_id = ?
			ORDER BY id DESC LIMIT 1 OFFSET ?
		)`, pid, pid, keep); err != nil {
		return 0, err
	}

	return rid, txn.Commit()
}

// DeleteReply removes one comment. The author of the comment can do this,
// and the owner of the thread can do this.
func (app *App) DeleteReply(rid, uid int64) error {
	result, err := app.dbh.Exec(`
		DELETE FROM replies WHERE id = ? AND (
			user_id = ? OR
			post_id IN (SELECT id FROM posts WHERE user_id = ?)
		)`, rid, uid, uid)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------- bootstrap ----------

// SeedFirstUser makes the root member when the database is empty. The
// platform has no registration page, so the first member cannot invite
// himself. The function prints a login link to standard output.
func (app *App) SeedFirstUser(email, handle string) error {
	total, err := app.CountUsers()
	if err != nil {
		return err
	}
	if total > 0 {
		return nil
	}
	if email == "" || handle == "" {
		return errors.New("the database has no members: " +
			"start once with -seed-email and -seed-handle")
	}
	uid, err := app.CreateUser(email, handle, 0)
	if err != nil {
		return err
	}
	raw, sum := NewToken()
	if err := app.CreateLoginToken(uid, sum, 24*time.Hour); err != nil {
		return err
	}
	fmt.Printf("root member %q created\nlogin link, valid 24 hours:\n%s/l/%s\n",
		handle, app.Conf().SiteURL, raw)
	return nil
}
